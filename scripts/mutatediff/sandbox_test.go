package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGocacheCapParsesEnv(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
	}{
		{name: "empty_defaults", raw: "", want: defaultGocacheCapMiB * mib},
		{name: "positive_mib", raw: "1", want: mib},
		{name: "zero_disables_cap", raw: "0", want: 0},
		{name: "negative_defaults", raw: "-5", want: defaultGocacheCapMiB * mib},
		{name: "unparsable_defaults", raw: "abc", want: defaultGocacheCapMiB * mib},
		{name: "whitespace_trimmed", raw: " 2 ", want: 2 * mib},
		{name: "max_mib_that_fits", raw: "8796093022207", want: 8796093022207 * mib},
		{name: "overflow_defaults", raw: "8796093022208", want: defaultGocacheCapMiB * mib},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(gocacheCapEnv, tt.raw)
			assert.Equal(t, tt.want, gocacheCap())
		})
	}
}

func TestDirSizeSumsNestedFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a"), []byte("abc"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "b"), []byte("defgh"), 0o600))
	assert.Equal(t, int64(8), dirSize(root))
}

func TestDirSizeMissingRootIsZero(t *testing.T) {
	assert.Equal(t, int64(0), dirSize(filepath.Join(t.TempDir(), "absent")))
}

func TestSweepStaleRunDirsRemovesOnlyOldMatchingDirs(t *testing.T) {
	sysTmp := t.TempDir()
	now := time.Now()
	old := now.Add(-staleRunTTL - time.Hour)
	mkdir := func(name string, mtime time.Time) string {
		p := filepath.Join(sysTmp, name)
		require.NoError(t, os.Mkdir(p, 0o750))
		require.NoError(t, os.Chtimes(p, mtime, mtime))
		return p
	}
	stale := mkdir("mutatediff-run-stale", old)
	fresh := mkdir("mutatediff-run-fresh", now)
	// Exactly at the TTL is not yet stale: the sweep takes only what is older.
	edge := mkdir("mutatediff-run-edge", now.Add(-staleRunTTL))
	foreign := mkdir("other-tool-stale", old)
	staleFile := filepath.Join(sysTmp, "mutatediff-run-file")
	require.NoError(t, os.WriteFile(staleFile, []byte("x"), 0o600))
	require.NoError(t, os.Chtimes(staleFile, old, old))

	var out strings.Builder
	sweepStaleRunDirs(sysTmp, now, &out)

	assert.NoDirExists(t, stale)
	assert.DirExists(t, fresh)
	assert.DirExists(t, edge)
	assert.DirExists(t, foreign)
	assert.FileExists(t, staleFile)
	assert.Contains(t, out.String(), "removed stale run root")
}

func TestSandboxCleanupRemovesOnlyTheTempRoot(t *testing.T) {
	// A cap the seeded cache exceeds: cleanup must keep it anyway — pruning
	// belongs to the next run's setup.
	t.Setenv(gocacheCapEnv, "1")
	gocache := t.TempDir()
	runTmp := t.TempDir()
	obj := filepath.Join(gocache, "obj")
	require.NoError(t, os.WriteFile(obj, make([]byte, 2*mib), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(runTmp, "scratch"), []byte("x"), 0o600))

	s := &sandbox{gocache: gocache, runTmp: runTmp}
	require.NoError(t, s.cleanup())

	assert.NoDirExists(t, runTmp)
	assert.DirExists(t, gocache)
	assert.FileExists(t, obj)
}

func TestSetupSandboxAtEnforcesCacheCapAtStart(t *testing.T) {
	tests := []struct {
		name       string
		capMiB     string
		cacheBytes int
		wantPruned bool
		wantMsg    string
	}{
		{name: "over_cap_prunes", capMiB: "1", cacheBytes: 2 * mib, wantPruned: true, wantMsg: "build cache 2MiB (cap 1MiB) — pruned"},
		// At the cap is within it, not over it.
		{name: "at_cap_keeps", capMiB: "1", cacheBytes: mib, wantPruned: false, wantMsg: "build cache 1MiB (cap 1MiB) — kept"},
		{name: "zero_cap_keeps", capMiB: "0", cacheBytes: 2 * mib, wantPruned: false, wantMsg: "build cache 2MiB (cap disabled) — kept"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range append([]string{"GOCACHE"}, tempEnvVars...) {
				t.Setenv(key, "")
			}
			t.Setenv(gocacheCapEnv, tt.capMiB)
			cacheBase := t.TempDir()
			gocache := filepath.Join(cacheBase, "gocache")
			require.NoError(t, os.MkdirAll(gocache, 0o750))
			obj := filepath.Join(gocache, "obj")
			require.NoError(t, os.WriteFile(obj, make([]byte, tt.cacheBytes), 0o600))

			var out strings.Builder
			s, err := setupSandboxAt(context.Background(), cacheBase, t.TempDir(), time.Now(), &out)
			require.NoError(t, err)

			assert.Equal(t, gocache, s.gocache)
			assert.DirExists(t, gocache)
			if tt.wantPruned {
				assert.NoFileExists(t, obj)
			} else {
				assert.FileExists(t, obj)
			}
			assert.Contains(t, out.String(), tt.wantMsg)
		})
	}
}

func TestSetupSandboxAtReportsPruneFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only parent does not block removal on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only parent this test relies on")
	}
	for _, key := range append([]string{"GOCACHE"}, tempEnvVars...) {
		t.Setenv(key, "")
	}
	t.Setenv(gocacheCapEnv, "1")
	cacheBase := t.TempDir()
	gocache := filepath.Join(cacheBase, "gocache")
	require.NoError(t, os.MkdirAll(gocache, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(gocache, "obj"), make([]byte, 2*mib), 0o600))
	require.NoError(t, os.Chmod(cacheBase, 0o555))
	t.Cleanup(func() { _ = os.Chmod(cacheBase, 0o750) })

	var out strings.Builder
	s, err := setupSandboxAt(context.Background(), cacheBase, t.TempDir(), time.Now(), &out)

	require.Error(t, err)
	assert.Nil(t, s)
	assert.Contains(t, err.Error(), "wipe build cache")
}

func TestSetupSandboxAtPinsEnvAndCreatesRoots(t *testing.T) {
	// t.Setenv registers restoration for the vars setupSandboxAt overwrites.
	for _, key := range append([]string{"GOCACHE"}, tempEnvVars...) {
		t.Setenv(key, "")
	}
	cacheBase := t.TempDir()
	sysTmp := t.TempDir()
	now := time.Now()
	stale := filepath.Join(sysTmp, "mutatediff-run-orphan")
	require.NoError(t, os.Mkdir(stale, 0o750))
	old := now.Add(-staleRunTTL - time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	var out strings.Builder
	s, err := setupSandboxAt(context.Background(), cacheBase, sysTmp, now, &out)
	require.NoError(t, err)

	wantCache := filepath.Join(cacheBase, "gocache")
	assert.Equal(t, wantCache, s.gocache)
	assert.Equal(t, wantCache, os.Getenv("GOCACHE"))
	assert.DirExists(t, wantCache)
	assert.DirExists(t, s.runTmp)
	assert.True(t, strings.HasPrefix(filepath.Base(s.runTmp), runTmpPrefix))
	for _, key := range tempEnvVars {
		assert.Equal(t, s.runTmp, os.Getenv(key), key)
	}
	assert.NoDirExists(t, stale)
	// The cache-state line belongs to the start banner: it follows the path
	// line and both precede every baseline-measurement line.
	require.Contains(t, out.String(), "sandboxed build cache")
	require.Contains(t, out.String(), "build cache 0MiB")
	assert.Less(t, strings.Index(out.String(), "sandboxed build cache"), strings.Index(out.String(), "build cache 0MiB"))
}

func TestSetupSandboxAtCanceledContextCreatesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cacheBase := filepath.Join(t.TempDir(), "cachebase")

	var out strings.Builder
	s, err := setupSandboxAt(ctx, cacheBase, t.TempDir(), time.Now(), &out)

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, s)
	assert.NoDirExists(t, cacheBase)
}

func TestSandboxCleanupReportsRemovalFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only parent does not block removal on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only parent this test relies on")
	}
	parent := t.TempDir()
	runTmp := filepath.Join(parent, "run")
	require.NoError(t, os.Mkdir(runTmp, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(runTmp, "scratch"), []byte("x"), 0o600))
	require.NoError(t, os.Chmod(parent, 0o555))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o750) })

	s := &sandbox{gocache: t.TempDir(), runTmp: runTmp}
	err := s.cleanup()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove temp root")
}
