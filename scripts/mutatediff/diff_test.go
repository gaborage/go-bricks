package main

import (
	"reflect"
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
	got := parseUnifiedDiff(sampleDiff)
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
	if got := parseUnifiedDiff(diff); len(got["f.go"]) != 0 {
		t.Errorf("pure deletion produced ranges: %#v", got)
	}
}

func TestMutationScopeFiltersNonTargets(t *testing.T) {
	in := map[string][]lineRange{
		"config/injection.go":          {{Start: 1, End: 2}},
		"config/injection_test.go":     {{Start: 1, End: 2}},
		"tools/migration/main.go":      {{Start: 1, End: 2}},
		"database/testdata/fixture.go": {{Start: 1, End: 2}},
		"wiki/testing.md":              {{Start: 1, End: 2}},
		"scripts/mutatediff/diff.go":   {{Start: 1, End: 2}},
	}
	got := mutationScope(in)
	want := map[string][]lineRange{
		"config/injection.go": {{Start: 1, End: 2}},
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

func TestParseUnifiedDiffPanicsOnScanError(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("parseUnifiedDiff did not panic on an oversized diff line")
		}
	}()
	parseUnifiedDiff(strings.Repeat("x", 2<<20))
}
