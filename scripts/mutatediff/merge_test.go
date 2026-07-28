package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeShard(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMergeShardsAggregatesAndPrefixedFilesPassThrough(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "1-config.json",
		`{"mutants_total":60,"mutants_killed":51,"mutants_lived":7,"mutants_not_covered":2,"files":[{"file_name":"config/injection.go","mutations":[{"line":1,"type":"X","status":"LIVED"}]}]}`)
	writeShard(t, dir, "2-multitenant.json",
		`{"mutants_total":7,"mutants_killed":7,"mutants_lived":0,"mutants_not_covered":0,"files":[]}`)

	outPath := filepath.Join(dir, "merged.json")
	var buf bytes.Buffer
	if code := mergeShards(dir, outPath, &buf); code != 0 {
		t.Fatalf("mergeShards = %d, want 0; output: %s", code, buf.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, "merged.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		MutantsKilled     int     `json:"mutants_killed"`
		MutantsLived      int     `json:"mutants_lived"`
		MutantsNotCovered int     `json:"mutants_not_covered"`
		TestEfficacy      float64 `json:"test_efficacy"`
		MutationsCoverage float64 `json:"mutations_coverage"`
		Files             []struct {
			FileName string `json:"file_name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("merged report not valid JSON: %v", err)
	}
	if got.MutantsKilled != 58 || got.MutantsLived != 7 || got.MutantsNotCovered != 2 {
		t.Fatalf("sums wrong: %+v", got)
	}
	if got.TestEfficacy < 89.2 || got.TestEfficacy > 89.3 {
		t.Fatalf("efficacy = %v, want ~89.23", got.TestEfficacy)
	}
	if got.MutationsCoverage < 97.0 || got.MutationsCoverage > 97.1 {
		t.Fatalf("coverage = %v, want ~97.01", got.MutationsCoverage)
	}
	if len(got.Files) != 1 || got.Files[0].FileName != "config/injection.go" {
		t.Fatalf("files pass-through wrong: %+v", got.Files)
	}

	if !strings.Contains(buf.String(), "from 2 shards") {
		t.Fatalf("missing shard count in output: %s", buf.String())
	}
}

func TestMergeShardsNeverSlurpsItsOwnOutputOnRerun(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "1-config.json", `{"mutants_total":10,"mutants_killed":10,"mutants_lived":0,"files":[]}`)

	outPath := filepath.Join(dir, "merged.json") // output INSIDE the shard dir
	var buf bytes.Buffer
	if code := mergeShards(dir, outPath, &buf); code != 0 {
		t.Fatalf("first merge = %d; output: %s", code, buf.String())
	}
	buf.Reset()
	if code := mergeShards(dir, outPath, &buf); code != 0 {
		t.Fatalf("second merge = %d; output: %s", code, buf.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read merged report: %v", err)
	}
	if !strings.Contains(string(data), `"mutants_killed":10`) {
		t.Fatalf("re-run compounded its own output: %s", data)
	}
	if !strings.Contains(buf.String(), "from 1 shards") {
		t.Fatalf("re-run should still see exactly 1 shard: %s", buf.String())
	}
}

func TestMergeShardsSkipsJSONThatIsNotAReport(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "1-good.json", `{"mutants_total":2,"mutants_killed":2,"mutants_lived":0,"files":[]}`)
	writeShard(t, dir, "2-notareport.json", `{"hello":"world","files":[]}`)

	outPath := filepath.Join(t.TempDir(), "merged.json")
	var buf bytes.Buffer
	if code := mergeShards(dir, outPath, &buf); code != 0 {
		t.Fatalf("mergeShards = %d; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "not a gremlins report") {
		t.Fatalf("missing WARN for non-report JSON: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "from 1 shards") {
		t.Fatalf("non-report must not count as a shard: %s", buf.String())
	}
}

func TestMergeShardsSkipsUnparsableShardWithWarning(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "1-good.json", `{"mutants_total":4,"mutants_killed":3,"mutants_lived":1,"files":[]}`)
	writeShard(t, dir, "2-broken.json", `{truncated`)

	var buf bytes.Buffer
	outPath := filepath.Join(t.TempDir(), "merged.json")
	if code := mergeShards(dir, outPath, &buf); code != 0 {
		t.Fatalf("mergeShards = %d, want 0; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "skipping unparsable shard") {
		t.Fatalf("missing WARN for broken shard: %s", buf.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read merged report: %v", err)
	}
	if !strings.Contains(string(data), `"mutants_killed":3`) {
		t.Fatalf("good shard not merged: %s", data)
	}
}

func TestMergeShardsFailsClosedOnZeroReadableShards(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "1-broken.json", `{nope`)

	outPath := filepath.Join(t.TempDir(), "merged.json")
	var buf bytes.Buffer
	if code := mergeShards(dir, outPath, &buf); code != 2 {
		t.Fatalf("mergeShards = %d, want 2 for zero readable shards", code)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("empty merged report must not be written; stat err = %v", err)
	}
}

func TestMergeShardsSkipsInvariantViolatingCounters(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "1-good.json", `{"mutants_total":5,"mutants_killed":5,"mutants_lived":0,"files":[]}`)
	writeShard(t, dir, "2-negative.json", `{"mutants_total":3,"mutants_killed":-1,"mutants_lived":0,"files":[]}`)
	writeShard(t, dir, "3-overtotal.json", `{"mutants_total":1,"mutants_killed":2,"mutants_lived":1,"files":[]}`)

	outPath := filepath.Join(t.TempDir(), "merged.json")
	var buf bytes.Buffer
	if code := mergeShards(dir, outPath, &buf); code != 0 {
		t.Fatalf("mergeShards = %d; output: %s", code, buf.String())
	}
	if got := strings.Count(buf.String(), "counters violate gremlins invariants"); got != 2 {
		t.Fatalf("want 2 invariant WARNs, got %d: %s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "from 1 shards") {
		t.Fatalf("only the sane shard may count: %s", buf.String())
	}
}

func TestMergeShardsRejectsWrappingCounterSums(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "1-good.json", `{"mutants_total":1,"mutants_killed":1,"files":[]}`)
	writeShard(t, dir, "2-wrap.json",
		`{"mutants_total":9223372036854775807,"mutants_killed":9223372036854775807,"mutants_lived":1,"files":[]}`)

	outPath := filepath.Join(t.TempDir(), "merged.json")
	var buf bytes.Buffer
	if code := mergeShards(dir, outPath, &buf); code != 0 {
		t.Fatalf("mergeShards = %d; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "from 1 shards") {
		t.Fatalf("wrapping shard must be skipped: %s", buf.String())
	}
}

func TestMergeShardsFailsClosedOnAggregateOverflow(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "1-max.json", `{"mutants_total":9223372036854775807,"mutants_killed":9223372036854775807,"files":[]}`)
	writeShard(t, dir, "2-max.json", `{"mutants_total":9223372036854775807,"mutants_killed":9223372036854775807,"files":[]}`)

	outPath := filepath.Join(t.TempDir(), "merged.json")
	var buf bytes.Buffer
	if code := mergeShards(dir, outPath, &buf); code != 2 {
		t.Fatalf("mergeShards = %d, want 2 on aggregate overflow", code)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("corrupt report must not be written; stat err = %v", err)
	}
}

// Regression: gremlins counts NOT COVERED OUTSIDE mutants_total. This fixture
// mirrors the real Task-1 capture (total=356, killed=330, lived=26,
// not_covered=42; 330+26+42 > 356) — the first production merge rejected 49 of
// 54 shards because the invariant wrongly included not_covered.
func TestMergeShardsAcceptsRealCaptureShapedShard(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "1-config.json",
		`{"mutants_total":356,"mutants_killed":330,"mutants_lived":26,"mutants_not_covered":42,"files":[]}`)

	outPath := filepath.Join(t.TempDir(), "merged.json")
	var buf bytes.Buffer
	if code := mergeShards(dir, outPath, &buf); code != 0 {
		t.Fatalf("mergeShards = %d; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "from 1 shards, 0 skipped") {
		t.Fatalf("real-capture-shaped shard must be accepted: %s", buf.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read merged report: %v", err)
	}
	if !strings.Contains(string(data), `"mutants_not_covered":42`) {
		t.Fatalf("not_covered lost in merge: %s", data)
	}
}
