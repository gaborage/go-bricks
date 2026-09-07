package main

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
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

func shardPaths(t *testing.T, dir string, names ...string) []string {
	t.Helper()
	paths := make([]string, 0, len(names))
	for _, n := range names {
		paths = append(paths, filepath.Join(dir, n))
	}
	return paths
}

type accumulateCase struct {
	name         string
	shards       map[string]string
	order        []string
	absOutName   string
	wantReadable int
	wantSkipped  int
	wantOverflow string
	wantKilled   int
	wantLived    int
	wantNotCov   int
	wantFiles    []string
}

func runAccumulateCase(t *testing.T, tt *accumulateCase) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range tt.shards {
		writeShard(t, dir, name, content)
	}
	absOut := ""
	if tt.absOutName != "" {
		abs, err := filepath.Abs(filepath.Join(dir, tt.absOutName))
		if err != nil {
			t.Fatal(err)
		}
		absOut = abs
	}

	merged := mergedReport{Files: []json.RawMessage{}}
	readable, skipped, overflow := accumulateShards(&merged, shardPaths(t, dir, tt.order...), absOut, io.Discard)

	if readable != tt.wantReadable || skipped != tt.wantSkipped {
		t.Errorf("readable/skipped = %d/%d, want %d/%d", readable, skipped, tt.wantReadable, tt.wantSkipped)
	}
	wantOverflow := ""
	if tt.wantOverflow != "" {
		wantOverflow = filepath.Join(dir, tt.wantOverflow)
	}
	if overflow != wantOverflow {
		t.Errorf("overflow = %q, want %q", overflow, wantOverflow)
	}
	if merged.MutantsKilled != tt.wantKilled || merged.MutantsLived != tt.wantLived || merged.MutantsNotCovered != tt.wantNotCov {
		t.Errorf("counters = %d/%d/%d, want %d/%d/%d",
			merged.MutantsKilled, merged.MutantsLived, merged.MutantsNotCovered,
			tt.wantKilled, tt.wantLived, tt.wantNotCov)
	}
	got := make([]string, 0, len(merged.Files))
	for _, f := range merged.Files {
		got = append(got, string(f))
	}
	if !reflect.DeepEqual(got, tt.wantFiles) {
		t.Errorf("files = %v, want %v", got, tt.wantFiles)
	}
}

func TestAccumulateShardsFoldsCountersAndFiles(t *testing.T) {
	const a = `{"mutants_total":60,"mutants_killed":51,"mutants_lived":7,"mutants_not_covered":2,"files":["a"]}`
	const b = `{"mutants_total":7,"mutants_killed":7,"mutants_lived":0,"mutants_not_covered":0,"files":["b"]}`
	const broken = `{truncated`
	const maxKilled = `{"mutants_total":9223372036854775807,"mutants_killed":9223372036854775807,"files":["m"]}`

	tests := []accumulateCase{
		{
			name:         "two_shards_sum_and_keep_path_order",
			shards:       map[string]string{"1.json": a, "2.json": b},
			order:        []string{"1.json", "2.json"},
			wantReadable: 2, wantKilled: 58, wantLived: 7, wantNotCov: 2,
			wantFiles: []string{`"a"`, `"b"`},
		},
		{
			name:         "reversed_paths_reverse_files",
			shards:       map[string]string{"1.json": a, "2.json": b},
			order:        []string{"2.json", "1.json"},
			wantReadable: 2, wantKilled: 58, wantLived: 7, wantNotCov: 2,
			wantFiles: []string{`"b"`, `"a"`},
		},
		{
			name:         "unreadable_shard_is_skipped_not_counted",
			shards:       map[string]string{"1.json": a, "2.json": broken},
			order:        []string{"1.json", "2.json"},
			wantReadable: 1, wantSkipped: 1, wantKilled: 51, wantLived: 7, wantNotCov: 2,
			wantFiles: []string{`"a"`},
		},
		{
			name:         "own_output_is_never_folded",
			shards:       map[string]string{"1.json": a, "merged.json": b},
			order:        []string{"1.json", "merged.json"},
			absOutName:   "merged.json",
			wantReadable: 1, wantKilled: 51, wantLived: 7, wantNotCov: 2,
			wantFiles: []string{`"a"`},
		},
		{
			name:         "overflow_names_the_offending_shard",
			shards:       map[string]string{"1.json": maxKilled, "2.json": maxKilled},
			order:        []string{"1.json", "2.json"},
			wantOverflow: "2.json",
			wantReadable: 1, wantKilled: math.MaxInt,
			wantFiles: []string{`"m"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runAccumulateCase(t, &tt) })
	}
}

func TestSetRatesDerivesPercentages(t *testing.T) {
	tests := []struct {
		name         string
		killed       int
		lived        int
		notCovered   int
		wantEfficacy float64
		wantCoverage float64
	}{
		{"real_capture", 330, 26, 42, 92.69662921348315, 89.44723618090453},
		{"single_shard", 51, 7, 2, 87.93103448275862, 96.66666666666667},
		{"not_covered_excluded_from_efficacy", 1, 1, 98, 50, 2},
		{"all_killed", 4, 0, 0, 100, 100},
		{"no_verdicts_but_not_covered", 0, 0, 5, 0, 0},
		{"nothing_seen", 0, 0, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged := mergedReport{MutantsKilled: tt.killed, MutantsLived: tt.lived, MutantsNotCovered: tt.notCovered}
			setRates(&merged)
			if math.Abs(merged.TestEfficacy-tt.wantEfficacy) > 1e-9 {
				t.Errorf("efficacy = %v, want %v", merged.TestEfficacy, tt.wantEfficacy)
			}
			if math.Abs(merged.MutationsCoverage-tt.wantCoverage) > 1e-9 {
				t.Errorf("coverage = %v, want %v", merged.MutationsCoverage, tt.wantCoverage)
			}
		})
	}
}
