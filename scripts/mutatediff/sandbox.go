package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The gate's children build thousands of mutants. Flowing those artifacts
// through the machine-shared GOCACHE fills the disk with objects no other
// build will ever read, and the only shared-cache remedy (`go clean -cache`)
// destroys every other session's warm build state. The sandbox scopes both
// to paths this run owns: a dedicated persistent build cache, pruned at run
// start when it outgrows its cap, and a per-run temp root that swallows the engine's
// working copies (gremlins-*), the report dir, and coverage scratch files.
//
// The dedicated cache lives under os.UserCacheDir, never inside the repo:
// gremlins copies the whole module root per worker with an unfiltered
// filepath.Walk (workdir.go), so an in-repo cache would be hauled into every
// working copy.
const (
	// gocacheCapEnv bounds, in MiB, what the dedicated build cache may hold
	// at run start. An explicit 0 disables the cap; unset or unparsable
	// falls back to the default (MUTATE_CEILING_FLOOR precedent).
	gocacheCapEnv = "MUTATE_GOCACHE_CAP"
	// defaultGocacheCapMiB bounds what a run may start on top of while
	// holding one heavy run's build state (measured: one database-package run
	// writes ~3.0 GiB, a repeat run grows it to ~5.7 GiB — repeat mutant
	// builds miss the cache because gremlins' per-run workdir paths enter the
	// action IDs), so at rest the machine holds the cap plus one run's growth.
	// At this cap the wipe fires roughly every second heavy run; the ~42s
	// cold-vs-warm penalty it re-imposes lands inside the measured baseline.
	defaultGocacheCapMiB = 4096
	// runTmpPrefix names the per-run temp root. The stale sweep keys on it:
	// a run killed with SIGKILL never reaches its defers, so the next run
	// removes what it left.
	runTmpPrefix = "mutatediff-run-"
	// staleRunTTL is how old an orphaned run root must be before the sweep
	// takes it. A gate run lasts minutes, so nothing this old is live, and a
	// concurrent session's tree is never yanked.
	staleRunTTL = 24 * time.Hour
	mib         = 1 << 20
)

// tempEnvVars covers os.TempDir on every platform: TMPDIR on Unix, TMP and
// TEMP on Windows. Pinning all three is harmless where a name is unused.
var tempEnvVars = []string{"TMPDIR", "TMP", "TEMP"}

// sandbox is the per-run temp root one run owns and must remove. The
// dedicated build cache needs no field: it is persistent, reached by every
// child through the pinned GOCACHE, and pruned at setup.
type sandbox struct {
	runTmp string
}

// setupSandbox resolves the machine-specific roots and delegates. Split from
// setupSandboxAt so the dir/env/sweep logic stays testable on every platform
// without faking os.UserCacheDir.
func setupSandbox(ctx context.Context, out io.Writer) (*sandbox, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user cache dir for the dedicated build cache: %w", err)
	}
	return setupSandboxAt(ctx, filepath.Join(base, "mutatediff"), os.TempDir(), time.Now(), out)
}

// setupSandboxAt creates both roots and pins them on this process's
// environment so every descendant inherits them — the same seam budget.apply
// uses, and for the same reason: measureSuite's warming passes and the
// engine's coverage read must land in one cache, or the measured baseline is
// a lie.
func setupSandboxAt(ctx context.Context, cacheBase, sysTmp string, now time.Time, out io.Writer) (*sandbox, error) {
	// A canceled run must not begin a lifecycle it would immediately abandon.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gocache := filepath.Join(cacheBase, "gocache")
	// Before anything else this run owns: a prune failure then aborts without
	// a temp root to leak, and the MkdirAll below is the only creation site.
	if err := pruneGocache(gocache, gocacheCap(), out); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(gocache, 0o750); err != nil {
		return nil, fmt.Errorf("create dedicated build cache: %w", err)
	}
	sweepStaleRunDirs(sysTmp, now, out)
	runTmp, err := os.MkdirTemp(sysTmp, runTmpPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("create per-run temp root: %w", err)
	}
	if err := os.Setenv("GOCACHE", gocache); err != nil {
		return nil, fmt.Errorf("pin GOCACHE: %w", err)
	}
	for _, key := range tempEnvVars {
		if err := os.Setenv(key, runTmp); err != nil {
			return nil, fmt.Errorf("pin %s: %w", key, err)
		}
	}
	fmt.Fprintf(out, "mutatediff: sandboxed build cache %s, temp root %s\n", gocache, runTmp)
	return &sandbox{runTmp: runTmp}, nil
}

// pruneGocache enforces the cap before any build runs and reports the cache
// state on the start banner. Pruning here, not at cleanup, puts the
// cold-rebuild cost inside measureSuite's baseline passes, which rewarm
// dependencies and stdlib before the per-mutant ceiling is measured from them;
// a wipe at cleanup instead left the next run's first mutants paying it
// against a ceiling measured warm, and they timed out. Wiping assumes gate
// runs on one machine do not overlap — they are serialized locally by
// convention, and the shared cache is not safe to wipe under a concurrent run.
func pruneGocache(gocache string, capBytes int64, out io.Writer) error {
	size := dirSize(gocache)
	capLabel := "cap disabled"
	if capBytes > 0 {
		capLabel = "cap " + formatMiB(capBytes)
	}
	if capBytes <= 0 || size <= capBytes {
		fmt.Fprintf(out, "mutatediff: build cache %s (%s) — kept\n", formatMiB(size), capLabel)
		return nil
	}
	// Reported after the removal lands: a banner claiming "pruned" above the
	// error that aborted the prune would assert something that did not happen.
	if err := os.RemoveAll(gocache); err != nil {
		return fmt.Errorf("wipe build cache: %w", err)
	}
	fmt.Fprintf(out, "mutatediff: build cache %s (%s) — pruned\n", formatMiB(size), capLabel)
	return nil
}

// cleanup releases what the run created and reports what it could not: a
// gate whose cleanup failed must not exit clean, or the debris this sandbox
// exists to prevent comes back silently. Deferred in run, so it fires on
// failures the same as on passes; only SIGKILL skips it, and the startup
// sweep covers that. The dedicated build cache survives cleanup — it is
// persistent by design and pruned at the next run's start.
func (s *sandbox) cleanup() error {
	if err := os.RemoveAll(s.runTmp); err != nil {
		return fmt.Errorf("remove temp root: %w", err)
	}
	return nil
}

// gocacheCap reads the cap in MiB and returns bytes. Unset or unparsable
// values fall back to the default rather than failing the gate; 0 is the
// documented opt-out.
func gocacheCap() int64 {
	raw := strings.TrimSpace(os.Getenv(gocacheCapEnv))
	if raw == "" {
		return defaultGocacheCapMiB * mib
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 || v > math.MaxInt64/mib {
		return defaultGocacheCapMiB * mib
	}
	return v * mib
}

// sweepStaleRunDirs removes orphaned per-run roots older than staleRunTTL.
func sweepStaleRunDirs(sysTmp string, now time.Time, out io.Writer) {
	entries, err := os.ReadDir(sysTmp)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), runTmpPrefix) {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		if now.Sub(info.ModTime()) <= staleRunTTL {
			continue
		}
		p := filepath.Join(sysTmp, e.Name())
		if rmErr := os.RemoveAll(p); rmErr != nil {
			fmt.Fprintf(out, "WARN: could not remove stale run root %s: %v\n", p, rmErr)
			continue
		}
		fmt.Fprintf(out, "mutatediff: removed stale run root %s\n", p)
	}
}

// dirSize sums regular file sizes best-effort: an unreadable entry counts as
// zero, degrading to a smaller measurement rather than a failed cleanup.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, infoErr := d.Info(); infoErr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func formatMiB(bytes int64) string {
	return fmt.Sprintf("%dMiB", bytes/mib)
}
