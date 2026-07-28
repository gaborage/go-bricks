package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// shardReport carries the per-package gremlins fields the merge aggregates.
// Unknown fields are ignored; files entries pass through verbatim (the
// baseline loop has already prefixed file_name with the package dir).
type shardReport struct {
	MutantsTotal      *int              `json:"mutants_total"`
	MutantsKilled     int               `json:"mutants_killed"`
	MutantsLived      int               `json:"mutants_lived"`
	MutantsNotCovered int               `json:"mutants_not_covered"`
	Files             []json.RawMessage `json:"files"`
}

type mergedReport struct {
	MutantsKilled     int               `json:"mutants_killed"`
	MutantsLived      int               `json:"mutants_lived"`
	MutantsNotCovered int               `json:"mutants_not_covered"`
	TestEfficacy      float64           `json:"test_efficacy"`
	MutationsCoverage float64           `json:"mutations_coverage"`
	Files             []json.RawMessage `json:"files"`
}

// mergeShards aggregates every *.json shard in dir into a single report at
// outPath. An unparsable shard is skipped with a WARN (the baseline is
// advisory; one bad shard must not erase the rest). Zero readable shards is
// an error — writing an empty report would silently pass downstream guards.
func mergeShards(dir, outPath string, out io.Writer) int {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json")) // Glob returns lexicographic order
	if err != nil {
		return fail("%v", err)
	}

	absOut, err := filepath.Abs(outPath)
	if err != nil {
		return fail("%v", err)
	}

	var merged mergedReport
	merged.Files = []json.RawMessage{}
	readable := 0
	for _, p := range paths {
		if abs, absErr := filepath.Abs(p); absErr == nil && abs == absOut {
			continue // never slurp our own output on a re-run
		}
		data, readErr := os.ReadFile(p) // #nosec G304 -- paths come from a glob over the loop's own report dir
		if readErr != nil {
			fmt.Fprintf(out, "WARN: skipping unreadable shard %s: %v\n", p, readErr)
			continue
		}
		var s shardReport
		if jsonErr := json.Unmarshal(data, &s); jsonErr != nil {
			fmt.Fprintf(out, "WARN: skipping unparsable shard %s: %v\n", p, jsonErr)
			continue
		}
		// Identity check must not lean on files: the baseline loop's jq rewrite
		// normalizes files to [] on any parseable JSON. mutants_total is the
		// gremlins-specific marker.
		if s.MutantsTotal == nil {
			fmt.Fprintf(out, "WARN: skipping %s: JSON but not a gremlins report\n", p)
			continue
		}
		readable++
		merged.MutantsKilled += s.MutantsKilled
		merged.MutantsLived += s.MutantsLived
		merged.MutantsNotCovered += s.MutantsNotCovered
		merged.Files = append(merged.Files, s.Files...)
	}
	if readable == 0 {
		return fail("no readable shards in %s — refusing to write an empty report", dir)
	}

	if verdicted := merged.MutantsKilled + merged.MutantsLived; verdicted > 0 {
		merged.TestEfficacy = float64(merged.MutantsKilled) * 100 / float64(verdicted)
	}
	if seen := merged.MutantsKilled + merged.MutantsLived + merged.MutantsNotCovered; seen > 0 {
		merged.MutationsCoverage = float64(merged.MutantsKilled+merged.MutantsLived) * 100 / float64(seen)
	}

	encoded, err := json.Marshal(merged)
	if err != nil {
		return fail("%v", err)
	}
	if err := os.WriteFile(outPath, encoded, 0o600); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(out, "baseline: killed=%d lived=%d not_covered=%d efficacy=%.0f%% (from %d shards)\n",
		merged.MutantsKilled, merged.MutantsLived, merged.MutantsNotCovered, math.Floor(merged.TestEfficacy), readable)
	return 0
}
