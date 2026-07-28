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

// readShard loads and validates one shard; ok=false means skip (already
// warned). Identity keys on mutants_total — the baseline loop's jq rewrite
// normalizes files to [] on any parseable JSON, so files proves nothing.
// Counter sanity: non-negative, and the verdicted counters cannot exceed the
// total (killed+lived+not_covered < total is legitimate — the remainder is
// TIMED OUT / NOT VIABLE, which gremlins reports separately).
func readShard(p string, out io.Writer) (shardReport, bool) {
	var s shardReport
	data, err := os.ReadFile(p) // #nosec G304 -- paths come from a glob over the loop's own report dir
	if err != nil {
		fmt.Fprintf(out, "WARN: skipping unreadable shard %s: %v\n", p, err)
		return s, false
	}
	if err := json.Unmarshal(data, &s); err != nil {
		fmt.Fprintf(out, "WARN: skipping unparsable shard %s: %v\n", p, err)
		return s, false
	}
	if s.MutantsTotal == nil {
		fmt.Fprintf(out, "WARN: skipping %s: JSON but not a gremlins report\n", p)
		return s, false
	}
	if *s.MutantsTotal < 0 || s.MutantsKilled < 0 || s.MutantsLived < 0 || s.MutantsNotCovered < 0 {
		fmt.Fprintf(out, "WARN: skipping %s: counters violate gremlins invariants\n", p)
		return s, false
	}
	// Checked subtraction instead of summing — a sum of near-MaxInt counters
	// wraps negative and would sneak past a plain comparison. NOT COVERED is
	// deliberately absent: gremlins counts it OUTSIDE mutants_total (real
	// capture: total=356 with killed=330, lived=26, not_covered=42) — including
	// it here silently rejected 49 of 54 shards in the first production merge.
	remaining := *s.MutantsTotal
	for _, c := range []int{s.MutantsKilled, s.MutantsLived} {
		if c > remaining {
			fmt.Fprintf(out, "WARN: skipping %s: counters violate gremlins invariants\n", p)
			return s, false
		}
		remaining -= c
	}
	return s, true
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
	readable, skipped := 0, 0
	for _, p := range paths {
		if abs, absErr := filepath.Abs(p); absErr == nil && abs == absOut {
			continue // never slurp our own output on a re-run
		}
		s, ok := readShard(p, out)
		if !ok {
			skipped++
			continue
		}
		if merged.MutantsKilled > math.MaxInt-s.MutantsKilled ||
			merged.MutantsLived > math.MaxInt-s.MutantsLived ||
			merged.MutantsNotCovered > math.MaxInt-s.MutantsNotCovered {
			return fail("aggregate counters would overflow at %s — refusing to write a corrupt report", p)
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
	fmt.Fprintf(out, "baseline: killed=%d lived=%d not_covered=%d efficacy=%.0f%% (from %d shards, %d skipped)\n",
		merged.MutantsKilled, merged.MutantsLived, merged.MutantsNotCovered, math.Floor(merged.TestEfficacy), readable, skipped)
	return 0
}
