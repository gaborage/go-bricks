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

func TestSandboxCleanup(t *testing.T) {
	tests := []struct {
		name           string
		cacheBytes     int
		capBytes       int64
		wantCacheWiped bool
		// Empty means the output must not mention the cap at all.
		wantMsg string
	}{
		// Cap exactly at the size: at the cap is within it, not over it.
		{name: "at_cap_keeps_cache", cacheBytes: 4, capBytes: 4, wantCacheWiped: false, wantMsg: "within its"},
		{name: "over_cap_wipes_cache", cacheBytes: 5, capBytes: 4, wantCacheWiped: true, wantMsg: "exceeds its 0MiB cap — wiping"},
		{name: "zero_cap_never_wipes", cacheBytes: 5, capBytes: 0, wantCacheWiped: false, wantMsg: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gocache := t.TempDir()
			runTmp := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(gocache, "obj"), make([]byte, tt.cacheBytes), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(runTmp, "scratch"), []byte("x"), 0o600))

			var out strings.Builder
			s := &sandbox{gocache: gocache, runTmp: runTmp, capBytes: tt.capBytes}
			require.NoError(t, s.cleanup(&out))

			assert.NoDirExists(t, runTmp)
			if tt.wantCacheWiped {
				assert.NoDirExists(t, gocache)
			} else {
				assert.DirExists(t, gocache)
			}
			if tt.wantMsg == "" {
				assert.NotContains(t, out.String(), "cap")
			} else {
				assert.Contains(t, out.String(), tt.wantMsg)
			}
		})
	}
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
	assert.Equal(t, gocacheCap(), s.capBytes)
	assert.NoDirExists(t, stale)
	assert.Contains(t, out.String(), "sandboxed build cache")
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

	var out strings.Builder
	s := &sandbox{gocache: t.TempDir(), runTmp: runTmp, capBytes: 0}
	err := s.cleanup(&out)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove temp root")
}
