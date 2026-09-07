package main

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const sampleDiff = `diff --git a/config/injection.go b/config/injection.go
index 1111111..2222222 100644
--- a/config/injection.go
+++ b/config/injection.go
@@ -25,0 +26,3 @@ func (c *Config) InjectInto(target any) error {
+	a := 1
+	b := 2
+	_ = a + b
@@ -40 +44 @@ func x() {
-	old := 1
+	new := 1
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1,5 +0,0 @@
-gone
diff --git a/config/new_file.go b/config/new_file.go
new file mode 100644
--- /dev/null
+++ b/config/new_file.go
@@ -0,0 +1,2 @@
+package config
+var z = 3
`

func TestParseUnifiedDiffExtractsNewFileRanges(t *testing.T) {
	got, err := parseUnifiedDiff(sampleDiff)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	want := map[string][]lineRange{
		"config/injection.go": {{Start: 26, End: 29}, {Start: 44, End: 45}},
		"config/new_file.go":  {{Start: 1, End: 3}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseUnifiedDiff = %#v, want %#v", got, want)
	}
}

func TestParseUnifiedDiffSkipsPureDeletionHunks(t *testing.T) {
	diff := "--- a/f.go\n+++ b/f.go\n@@ -10,2 +9,0 @@\n-x\n-y\n"
	got, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	if len(got["f.go"]) != 0 {
		t.Errorf("pure deletion produced ranges: %#v", got)
	}
}

func TestMutationScopeFiltersNonTargets(t *testing.T) {
	in := map[string][]lineRange{
		"config/injection.go":            {{Start: 1, End: 2}},
		"config/injection_test.go":       {{Start: 1, End: 2}},
		"tools/migration/main.go":        {{Start: 1, End: 2}},
		"database/testdata/fixture.go":   {{Start: 1, End: 2}},
		"wiki/testing.md":                {{Start: 1, End: 2}},
		"scripts/mutatediff/diff.go":     {{Start: 1, End: 2}},
		"testing/containers/rabbitmq.go": {{Start: 1, End: 2}},
		"testing/mocks/registry.go":      {{Start: 1, End: 2}},
	}
	got := mutationScope(in)
	want := map[string][]lineRange{
		"config/injection.go":       {{Start: 1, End: 2}},
		"testing/mocks/registry.go": {{Start: 1, End: 2}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mutationScope = %#v, want %#v", got, want)
	}
}

func TestPackagesOfDedupesAndSorts(t *testing.T) {
	got := packagesOf(map[string][]lineRange{
		"database/query_builder.go": nil,
		"database/factory.go":       nil,
		"config/injection.go":       nil,
		"main.go":                   nil,
	})
	want := []string{".", "./config", "./database"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("packagesOf = %#v, want %#v", got, want)
	}
}

func TestParseUnifiedDiffErrorsOnScanFailure(t *testing.T) {
	if _, err := parseUnifiedDiff(strings.Repeat("x", 2<<20)); err == nil {
		t.Error("expected error for an oversized diff line")
	}
}

func TestParseHunkRangePinsNewSideBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   lineRange
		wantOK bool
	}{
		{"multi_line_hunk", "@@ -25,0 +26,3 @@ func x() {", lineRange{Start: 26, End: 29}, true},
		{"count_omitted_means_one", "@@ -40 +44 @@", lineRange{Start: 44, End: 45}, true},
		{"explicit_count_one", "@@ -40,1 +44,1 @@", lineRange{Start: 44, End: 45}, true},
		{"new_file_from_line_one", "@@ -0,0 +1,2 @@", lineRange{Start: 1, End: 3}, true},
		{"pure_deletion", "@@ -10,2 +9,0 @@", lineRange{}, false},
		{"empty_new_side_with_omitted_count", "@@ -1 +0 @@", lineRange{}, false},
		{"empty_new_side_with_explicit_zero_count", "@@ -1 +0,0 @@", lineRange{}, false},
		{"not_a_hunk_header", "@@ nonsense", lineRange{}, false},
		{"trailing_junk_before_at", "x@@ -1 +1 @@", lineRange{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseHunkRange(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parseHunkRange(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("parseHunkRange(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

// End is exclusive, so a 3-line hunk starting at 26 must cover exactly 26..28.
func TestParseHunkRangeEndIsExclusive(t *testing.T) {
	got, ok := parseHunkRange("@@ -25,0 +26,3 @@")
	if !ok {
		t.Fatal("parseHunkRange rejected a valid hunk")
	}
	if got.Start != 26 {
		t.Errorf("first line = %d, want 26", got.Start)
	}
	if got.End-1 != 28 {
		t.Errorf("last line = %d, want 28", got.End-1)
	}
	if got.End-got.Start != 3 {
		t.Errorf("span = %d, want 3", got.End-got.Start)
	}
}

func TestParseHunkRangeRejectsUnrepresentableHeaders(t *testing.T) {
	const tooBig = "99999999999999999999999" // parses to MaxInt with ErrRange
	maxInt := strconv.Itoa(math.MaxInt)
	tests := []struct {
		name string
		line string
	}{
		{"count_out_of_int_range", "@@ -1,1 +1," + tooBig + " @@"},
		{"count_out_of_int_range_at_line_zero", "@@ -1,1 +0," + tooBig + " @@"},
		{"start_out_of_int_range", "@@ -1,1 +" + tooBig + ",1 @@"},
		{"start_plus_count_overflows", "@@ -1,1 +" + maxInt + ",2 @@"},
		{"implicit_count_overflows_at_max_start", "@@ -1,1 +" + maxInt + " @@"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseHunkRange(tt.line)
			if ok {
				t.Errorf("parseHunkRange(%q) = %#v, true; want ok=false", tt.line, got)
			}
			if got != (lineRange{}) {
				t.Errorf("parseHunkRange(%q) range = %#v, want zero value", tt.line, got)
			}
		})
	}
}

func TestParseHunkRangeAcceptsTheLargestRepresentableHeader(t *testing.T) {
	line := "@@ -1,1 +" + strconv.Itoa(math.MaxInt-1) + ",1 @@"
	got, ok := parseHunkRange(line)
	if !ok {
		t.Fatalf("parseHunkRange(%q) = _, false; want ok=true", line)
	}
	want := lineRange{Start: math.MaxInt - 1, End: math.MaxInt}
	if got != want {
		t.Errorf("parseHunkRange(%q) = %#v, want %#v", line, got, want)
	}
}

func TestParseUnifiedDiffSkipsUnrepresentableHunks(t *testing.T) {
	diff := "--- a/f.go\n+++ b/f.go\n" +
		"@@ -1,1 +0,99999999999999999999999 @@\n+x\n" +
		"@@ -9,1 +" + strconv.Itoa(math.MaxInt) + ",2 @@\n+y\n" +
		"@@ -20,0 +21,2 @@\n+ok1\n+ok2\n"
	got, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	want := map[string][]lineRange{"f.go": {{Start: 21, End: 23}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseUnifiedDiff = %#v, want %#v", got, want)
	}
}
