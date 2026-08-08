package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	appPkg     = "./app"
	appSource  = "app/app.go"
	appTest    = "app/app_test.go"
	libSource  = "lib/lib.go"
	testEngine = "go run example.com/engine@v1.0.0"
)

// moduleFiles is a compilable two-package module: app depends on lib, so a
// change to lib must invalidate app's cached verdict even though app's own files
// are untouched.
func moduleFiles() map[string]string {
	return map[string]string{
		"go.mod":                 "module example.com/m\n\ngo 1.24\n",
		libSource:                "package lib\n\n// Double returns n doubled.\nfunc Double(n int) int { return n * 2 }\n",
		appSource:                "package app\n\nimport \"example.com/m/lib\"\n\n// Quad returns n quadrupled.\nfunc Quad(n int) int { return lib.Double(lib.Double(n)) }\n",
		appTest:                  "package app\n\nimport \"testing\"\n\nfunc TestQuad(t *testing.T) {\n\tif Quad(2) != 8 {\n\t\tt.Fatal(\"quad\")\n\t}\n}\n",
		"app/testdata/gold.json": "{\"want\":8}\n",
	}
}

// cacheFixture is one simulated `make mutate` invocation over a throwaway
// module. reload rebuilds the cache the way a *second* invocation would: fresh
// package graph, fresh fingerprint, same directory on disk.
type cacheFixture struct {
	root    string
	engine  string
	changed map[string][]lineRange
	cache   *resultCache
	out     *bytes.Buffer
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	// Deterministic module resolution: an inherited GOWORK pointing at the
	// go-bricks workspace would reject a module living in the temp directory.
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	root := writeModule(t, moduleFiles())
	t.Chdir(root)
	f := &cacheFixture{
		root:   root,
		engine: testEngine,
		changed: map[string][]lineRange{
			appSource: {{Start: 5, End: 9}},
		},
		out: &bytes.Buffer{},
	}
	f.reload(t)
	return f
}

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for name, body := range files {
		writeFile(t, root, name, body)
	}
	return root
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
}

func (f *cacheFixture) reload(t *testing.T) {
	t.Helper()
	f.out.Reset()
	f.cache = newResultCache(t.Context(), f.engine, "", f.out)
	require.NotNil(t, f.cache, "cache init failed: %s", f.out.String())
}

// entryPath returns the single stored entry, failing loudly when the count is
// not one — a test that silently found zero entries would assert nothing.
func (f *cacheFixture) entryPath(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(f.root, cacheDirName, "*.json"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	return matches[0]
}

func TestResultCacheMissesUntilStoredThenHits(t *testing.T) {
	f := newCacheFixture(t)
	require.False(t, f.cache.hit(appPkg, f.changed, f.out), "an empty cache must never hit")
	f.cache.store(appPkg, f.changed, f.out)
	f.reload(t)
	require.True(t, f.cache.hit(appPkg, f.changed, f.out), "identical inputs must hit: %s", f.out.String())
}

// TestResultCacheInvalidation is the gate's real contract: every input that can
// change a verdict must move the key, and every doubt must resolve to a miss.
func TestResultCacheInvalidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(t *testing.T, f *cacheFixture)
		wantHit bool
	}{
		{
			name:    "untouched_tree_hits",
			mutate:  func(*testing.T, *cacheFixture) {},
			wantHit: true,
		},
		{
			name: "changed_package_source_misses",
			mutate: func(t *testing.T, f *cacheFixture) {
				writeFile(t, f.root, appSource, "package app\n\nimport \"example.com/m/lib\"\n\n// Quad returns n quadrupled.\nfunc Quad(n int) int { return lib.Double(n) * 2 }\n")
			},
		},
		{
			name: "changed_package_test_misses",
			mutate: func(t *testing.T, f *cacheFixture) {
				writeFile(t, f.root, appTest, "package app\n\nimport \"testing\"\n\nfunc TestQuad(t *testing.T) {\n\tif Quad(3) != 12 {\n\t\tt.Fatal(\"quad\")\n\t}\n}\n")
			},
		},
		{
			name: "changed_dependency_misses",
			mutate: func(t *testing.T, f *cacheFixture) {
				writeFile(t, f.root, libSource, "package lib\n\n// Double returns n doubled.\nfunc Double(n int) int { return n + n }\n")
			},
		},
		{
			// `go list` reports a package it cannot parse without a Deps list
			// rather than failing, so this pins that the edit still moves the key.
			name: "unparsable_dependency_misses",
			mutate: func(t *testing.T, f *cacheFixture) {
				writeFile(t, f.root, libSource, "package lib\n\nthis is not go\n")
			},
		},
		{
			name: "new_file_in_dependency_misses",
			mutate: func(t *testing.T, f *cacheFixture) {
				writeFile(t, f.root, "lib/helper.go", "package lib\n\n// Triple returns n tripled.\nfunc Triple(n int) int { return n * 3 }\n")
			},
		},
		{
			name: "changed_testdata_misses",
			mutate: func(t *testing.T, f *cacheFixture) {
				writeFile(t, f.root, "app/testdata/gold.json", "{\"want\":12}\n")
			},
		},
		{
			name: "changed_go_mod_misses",
			mutate: func(t *testing.T, f *cacheFixture) {
				writeFile(t, f.root, "go.mod", "module example.com/m\n\ngo 1.24\n\n// pin bumped\n")
			},
		},
		{
			name: "new_engine_config_misses",
			mutate: func(t *testing.T, f *cacheFixture) {
				writeFile(t, f.root, ".gremlins.yaml", "unleash:\n  workers: 4\n")
			},
		},
		{
			name: "changed_engine_version_misses",
			mutate: func(_ *testing.T, f *cacheFixture) {
				f.engine = "go run example.com/engine@v1.1.0"
			},
		},
		{
			name: "widened_line_range_misses",
			mutate: func(_ *testing.T, f *cacheFixture) {
				f.changed[appSource] = []lineRange{{Start: 5, End: 12}}
			},
		},
		{
			name: "narrowed_line_range_hits",
			mutate: func(_ *testing.T, f *cacheFixture) {
				f.changed[appSource] = []lineRange{{Start: 6, End: 8}}
			},
			wantHit: true,
		},
		{
			name: "newly_changed_file_in_package_misses",
			mutate: func(_ *testing.T, f *cacheFixture) {
				f.changed["app/extra.go"] = []lineRange{{Start: 1, End: 2}}
			},
		},
		{
			name: "newly_changed_file_outside_package_hits",
			mutate: func(_ *testing.T, f *cacheFixture) {
				// ./app's engine run can only judge files under app/, so a change
				// elsewhere is ./lib's problem, not a reason to re-run ./app.
				f.changed[libSource] = []lineRange{{Start: 4, End: 5}}
			},
			wantHit: true,
		},
		{
			name: "corrupt_entry_misses",
			mutate: func(t *testing.T, f *cacheFixture) {
				require.NoError(t, os.WriteFile(f.entryPath(t), []byte("{not json"), 0o600))
			},
		},
		{
			name: "truncated_entry_misses",
			mutate: func(t *testing.T, f *cacheFixture) {
				require.NoError(t, os.WriteFile(f.entryPath(t), nil, 0o600))
			},
		},
		{
			name:   "schema_mismatch_misses",
			mutate: rewriteEntry(func(e *cacheEntry) { e.Schema = cacheSchema + 1 }),
		},
		{
			name:   "key_mismatch_misses",
			mutate: rewriteEntry(func(e *cacheEntry) { e.Key = "not-the-key" }),
		},
		{
			name:   "package_mismatch_misses",
			mutate: rewriteEntry(func(e *cacheEntry) { e.Package = "./somewhere-else" }),
		},
		{
			name:   "expired_entry_misses",
			mutate: rewriteEntry(func(e *cacheEntry) { e.Stored = time.Now().Add(-cacheTTL - time.Hour) }),
		},
		{
			name:   "future_dated_entry_misses",
			mutate: rewriteEntry(func(e *cacheEntry) { e.Stored = time.Now().Add(24 * time.Hour) }),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCacheFixture(t)
			require.False(t, f.cache.hit(appPkg, f.changed, f.out))
			f.cache.store(appPkg, f.changed, f.out)
			require.FileExists(t, f.entryPath(t))

			tt.mutate(t, f)
			f.reload(t)

			assert.Equal(t, tt.wantHit, f.cache.hit(appPkg, f.changed, f.out), "output: %s", f.out.String())
		})
	}
}

// rewriteEntry edits the stored entry in place, which is how a schema bump, a
// filename collision, or a stale clock reaches the lookup.
func rewriteEntry(edit func(*cacheEntry)) func(*testing.T, *cacheFixture) {
	return func(t *testing.T, f *cacheFixture) {
		t.Helper()
		p := f.entryPath(t)
		raw, err := os.ReadFile(p) // #nosec G304 -- test-owned temp path
		require.NoError(t, err)
		var e cacheEntry
		require.NoError(t, json.Unmarshal(raw, &e))
		edit(&e)
		updated, err := json.Marshal(e)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p, updated, 0o600))
	}
}

// TestResultCacheDisablesItselfOnAnUnlistableTree pins rule 4 at the widest
// seam: if the package graph cannot be built, there is no cache at all rather
// than a cache with silent holes in its dependency closure.
func TestResultCacheDisablesItselfOnAnUnlistableTree(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	files := moduleFiles()
	delete(files, "go.mod") // no module: `go list ./...` cannot resolve anything
	t.Chdir(writeModule(t, files))

	var out bytes.Buffer
	cache := newResultCache(t.Context(), testEngine, "", &out)
	require.Nil(t, cache, "a tree go list cannot resolve must not produce a cache")
	assert.Contains(t, out.String(), "result cache disabled")
	assert.False(t, cache.hit(appPkg, map[string][]lineRange{}, &out), "a disabled cache must never hit")
}

// TestStoreRefusesAResultWhoseSourcesMovedMidRun covers the window the engine
// itself opens: it reads the working tree while it runs, so a verdict produced
// against sources that have since changed belongs to neither state.
func TestStoreRefusesAResultWhoseSourcesMovedMidRun(t *testing.T) {
	f := newCacheFixture(t)
	require.False(t, f.cache.hit(appPkg, f.changed, f.out))

	writeFile(t, f.root, appSource, "package app\n\nimport \"example.com/m/lib\"\n\n// Quad returns n quadrupled.\nfunc Quad(n int) int { return lib.Double(n) + lib.Double(n) }\n")
	f.out.Reset()
	f.cache.store(appPkg, f.changed, f.out)

	assert.Contains(t, f.out.String(), "changed while it was being mutated")
	matches, err := filepath.Glob(filepath.Join(f.root, cacheDirName, "*.json"))
	require.NoError(t, err)
	assert.Empty(t, matches, "no entry may be written for a tree that moved")
}

// TestMutateAllSkipsTheEngineOnACacheHit is the end-to-end proof that a hit
// really replaces work: the engine here is `false`, which fails on any
// invocation, so the run can only succeed by never reaching it.
func TestMutateAllSkipsTheEngineOnACacheHit(t *testing.T) {
	f := newCacheFixture(t)
	require.False(t, f.cache.hit(appPkg, f.changed, f.out))
	f.cache.store(appPkg, f.changed, f.out)
	f.reload(t)

	var out bytes.Buffer
	gr := &gateRun{
		engineArgs: []string{"false"},
		reportDir:  t.TempDir(),
		changed:    f.changed,
		workers:    1,
		cache:      f.cache,
		out:        &out,
	}
	v, err := gr.mutateAll(t.Context(), []string{appPkg})
	require.NoError(t, err, "the engine must not have been invoked; output: %s", out.String())
	assert.Contains(t, out.String(), "./app cached PASS (skipped)")
	assert.Empty(t, v.failures)
	assert.Empty(t, v.warnings)
	assert.Empty(t, v.unjudged)

	// The inverse pins that the skip is what saved it: without a cache the same
	// package reaches the failing engine.
	gr.cache = nil
	_, err = gr.mutateAll(t.Context(), []string{appPkg})
	require.Error(t, err, "an uncached package must still reach the engine")
}

func TestNilResultCacheIsInert(t *testing.T) {
	var out bytes.Buffer
	var cache *resultCache
	assert.False(t, cache.hit(appPkg, map[string][]lineRange{}, &out))
	assert.NotPanics(t, func() { cache.store(appPkg, map[string][]lineRange{}, &out) })
	assert.Empty(t, out.String())
}

func TestRangesCoveredEnforcesSubsetRule(t *testing.T) {
	tests := []struct {
		name   string
		now    map[string][]lineRange
		cached map[string][]lineRange
		want   bool
	}{
		{
			name:   "exact_match_hits",
			now:    map[string][]lineRange{"a.go": {{Start: 10, End: 14}}},
			cached: map[string][]lineRange{"a.go": {{Start: 10, End: 14}}},
			want:   true,
		},
		{
			name:   "strict_subset_hits",
			now:    map[string][]lineRange{"a.go": {{Start: 11, End: 13}}},
			cached: map[string][]lineRange{"a.go": {{Start: 10, End: 14}}},
			want:   true,
		},
		{
			name:   "subset_spanning_two_cached_ranges_hits",
			now:    map[string][]lineRange{"a.go": {{Start: 10, End: 11}, {Start: 20, End: 21}}},
			cached: map[string][]lineRange{"a.go": {{Start: 10, End: 14}, {Start: 18, End: 22}}},
			want:   true,
		},
		{
			name:   "widened_line_range_misses",
			now:    map[string][]lineRange{"a.go": {{Start: 10, End: 15}}},
			cached: map[string][]lineRange{"a.go": {{Start: 10, End: 14}}},
		},
		{
			name:   "range_shifted_by_one_line_misses",
			now:    map[string][]lineRange{"a.go": {{Start: 9, End: 13}}},
			cached: map[string][]lineRange{"a.go": {{Start: 10, End: 14}}},
		},
		{
			name:   "gap_between_cached_ranges_misses",
			now:    map[string][]lineRange{"a.go": {{Start: 10, End: 20}}},
			cached: map[string][]lineRange{"a.go": {{Start: 10, End: 14}, {Start: 15, End: 20}}},
		},
		{
			name:   "unjudged_file_misses",
			now:    map[string][]lineRange{"a.go": {{Start: 10, End: 11}}, "b.go": {{Start: 1, End: 2}}},
			cached: map[string][]lineRange{"a.go": {{Start: 10, End: 14}}},
		},
		{
			name:   "extra_cached_file_still_hits",
			now:    map[string][]lineRange{"a.go": {{Start: 10, End: 11}}},
			cached: map[string][]lineRange{"a.go": {{Start: 10, End: 14}}, "b.go": {{Start: 1, End: 9}}},
			want:   true,
		},
		{
			name:   "nothing_to_judge_hits",
			now:    map[string][]lineRange{},
			cached: map[string][]lineRange{"a.go": {{Start: 10, End: 14}}},
			want:   true,
		},
		{
			name: "empty_cache_misses",
			now:  map[string][]lineRange{"a.go": {{Start: 10, End: 11}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, rangesCovered(tt.now, tt.cached))
		})
	}
}

func TestScopedRangesSelectsThePackageSubtree(t *testing.T) {
	changed := map[string][]lineRange{
		"database/factory.go":     {{Start: 1, End: 2}},
		"database/types/spec.go":  {{Start: 3, End: 4}},
		"databaseutil/helper.go":  {{Start: 5, End: 6}},
		"config/injection.go":     {{Start: 7, End: 8}},
		"root.go":                 {{Start: 9, End: 10}},
		"database/types/more.go":  {{Start: 11, End: 12}},
		"databases/decoy/file.go": {{Start: 13, End: 14}},
	}
	tests := []struct {
		name string
		pkg  string
		want []string
	}{
		{
			name: "package_takes_its_whole_subtree",
			pkg:  "./database",
			// databaseutil and databases share a prefix but not a directory:
			// the trailing separator is what keeps them out.
			want: []string{"database/factory.go", "database/types/more.go", "database/types/spec.go"},
		},
		{
			name: "subpackage_takes_only_its_own_files",
			pkg:  "./database/types",
			want: []string{"database/types/more.go", "database/types/spec.go"},
		},
		{
			name: "root_package_takes_everything",
			pkg:  ".",
			want: []string{
				"config/injection.go", "database/factory.go", "database/types/more.go",
				"database/types/spec.go", "databaseutil/helper.go", "databases/decoy/file.go", "root.go",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scopedRanges(tt.pkg, changed)
			files := make([]string, 0, len(got))
			for f := range got {
				files = append(files, f)
			}
			assert.ElementsMatch(t, tt.want, files)
		})
	}
}

func TestPkgDirOfNormalizesEngineArguments(t *testing.T) {
	tests := []struct {
		name string
		pkg  string
		want string
	}{
		{name: "dot_stays_dot", pkg: ".", want: "."},
		{name: "dot_slash_prefix_stripped", pkg: "./config", want: "config"},
		{name: "nested_package", pkg: "./database/types", want: "database/types"},
		{name: "trailing_slash_stripped", pkg: "./config/", want: "config"},
		{name: "bare_dot_slash_is_root", pkg: "./", want: "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pkgDirOf(tt.pkg))
		})
	}
}

func testGraph() *packageGraph {
	return &packageGraph{
		dirOf: map[string]string{
			"example.com/m/app":     "app",
			"example.com/m/app/sub": "app/sub",
			"example.com/m/lib":     "lib",
			"example.com/m/unused":  "unused",
			"fmt":                   "",
		},
		depsOf: map[string][]string{
			"example.com/m/app":     {"example.com/m/lib", "fmt"},
			"example.com/m/app/sub": {"fmt"},
			"example.com/m/lib":     {"fmt"},
			"example.com/m/unused":  {"fmt"},
		},
		inDir: map[string][]string{
			"app":     {"example.com/m/app"},
			"app/sub": {"example.com/m/app/sub"},
			"lib":     {"example.com/m/lib"},
			"unused":  {"example.com/m/unused"},
		},
		dirs: []string{"app", "app/sub", "lib", "unused"},
	}
}

func TestClosureDirsCoversSubtreeAndFirstPartyDeps(t *testing.T) {
	tests := []struct {
		name string
		pkg  string
		want []string
	}{
		{
			name: "subtree_plus_dependencies",
			pkg:  "app",
			// app/sub because the engine mutates the whole subtree it is pointed
			// at; lib because app's behavior changes when lib does. Stdlib and
			// unrelated first-party packages stay out.
			want: []string{"app", "app/sub", "lib"},
		},
		{name: "leaf_package", pkg: "lib", want: []string{"lib"}},
		{name: "root_covers_the_module", pkg: ".", want: []string{"app", "app/sub", "lib", "unused"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := testGraph().closureDirs(tt.pkg)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClosureDirsFailsClosedOnAnIncompleteGraph(t *testing.T) {
	t.Run("unlisted_dependency", func(t *testing.T) {
		g := testGraph()
		g.depsOf["example.com/m/app"] = []string{"example.com/m/vanished"}
		_, err := g.closureDirs("app")
		require.ErrorContains(t, err, "missing from the package listing")
	})
	t.Run("unknown_package_directory", func(t *testing.T) {
		_, err := testGraph().closureDirs("nowhere")
		require.ErrorContains(t, err, "no Go package listed")
	})
}

// TestCacheDirIsGitIgnored guards the one thing a reviewer cannot see in a diff:
// the allowlist .gitignore ignores this path today only because nothing
// un-ignores it, and an added allowlist entry would start committing the cache.
func TestCacheDirIsGitIgnored(t *testing.T) {
	repoRootDir := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(repoRootDir, ".gitignore")); err != nil {
		t.Skip("not running inside the repository checkout")
	}
	cmd := exec.CommandContext(t.Context(), "git", "check-ignore", "-q", cacheDirName+"/deadbeef.json")
	cmd.Dir = repoRootDir
	err := cmd.Run()
	require.NoError(t, err, "%s/ must be git-ignored; `git check-ignore` said otherwise", cacheDirName)
}

func TestHashPackageDirDistinguishesContentAndLayout(t *testing.T) {
	root := writeModule(t, map[string]string{
		"pkg/a.go":                 "package pkg\n",
		"pkg/a_test.go":            "package pkg\n",
		"pkg/testdata/golden.json": "{}\n",
		"pkg/notes.txt":            "ignored\n",
	})
	digest := func(t *testing.T) string {
		t.Helper()
		var b strings.Builder
		require.NoError(t, hashPackageDir(&b, root, "pkg"))
		return b.String()
	}

	base := digest(t)

	writeFile(t, root, "pkg/notes.txt", "still ignored, differently\n")
	assert.Equal(t, base, digest(t), "a non-Go, non-testdata file must not invalidate")

	writeFile(t, root, "pkg/a_test.go", "package pkg\n\n// touched\n")
	afterTest := digest(t)
	assert.NotEqual(t, base, afterTest, "a test file decides whether a mutant dies")

	writeFile(t, root, "pkg/testdata/golden.json", "{\"v\":1}\n")
	assert.NotEqual(t, afterTest, digest(t), "a golden file decides outcomes like source does")
}
