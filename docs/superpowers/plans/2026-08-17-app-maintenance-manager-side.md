# Stack A · PR4 — Idle-Cleanup Maintenance Is Manager-Side — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move idle-cleanup lifecycle out of `app/` and into the managers that own the pools — `database.DbManager` and `messaging.Manager` self-start their sweep at construction and stop it in `Close()`, exactly as `cache.NewCacheManager` already does — and delete `App.startMaintenanceLoops`, `App.warnIfCleanupIntervalTooLate`, `cleanupIntervalTooLate` and `App.shutdownManagers`.

**Architecture:** `internal/resourcepool.Pool.StartCleanup` is already idempotent and `Pool.Close` already stops+joins the loop, so the managers need only (a) a new additive `CleanupInterval` option, (b) a `StartCleanup` call at the end of their constructor, and (c) the interval-vs-idle-TTL WARN, which moves into one shared helper beside the pool (`internal/resourcepool/cleanup_warning.go`) so both managers emit the identical message. `app/managers.go` threads `database.manager.cleanupinterval` / `messaging.publisher.cleanupinterval` into the options structs it already builds. `app/lifecycle.go` then loses two whole phases: `prepareRuntime`'s trailing `startMaintenanceLoops()` and `Shutdown`'s step 5.

**Tech Stack:** Go 1.26 · testify (`assert`/`require`) · `internal/resourcepool` generic pool · `logger.Logger` interface (zerolog behind it) · no new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-16-app-readiness-and-lifecycle-slots-design.md` (§Lifecycle slots, decision 3) and `wiki/adr_067_lifecycle_slots.md` (§Decision item 4, §Delivery "PR4", §Consequences "Watch").

## Global Constraints

Copied verbatim from the task brief. Every task's requirements implicitly include this section.

- No exported API removal (`StartCleanup`/`StopCleanup` stay; if `CleanupInterval` is added to `DbManagerOptions`/`ManagerOptions` it is an additive field — note apidiff: adding a field to an exported struct is apidiff-compatible, though unkeyed composite literals of it stop compiling with "too few values"; recommend keyed literals); managers self-start exactly like `cache.NewCacheManager`; ADR-045 respected (no manager interface); ADR-029 shutdown order preserved (cleanup stops in `Close`, which the closers run last); camelCase / snake_case; `git commit -F <file>`, never `--no-gpg-sign`; implementers do not run make check/mutate or push.

Additional standing rules for this repo that apply to every task below:

- **Test naming:** camelCase Go test function names (`TestNewDbManagerStartsIdleCleanup`), snake_case table-case names (`{name: "cleanup_equals_idle_warns"}`).
- **Comments:** bare minimum — only non-obvious intent. No narrating comments.
- **Commit messages:** write the message to a file under
  `/private/tmp/claude-501/-Users-gaborage-Projects-gaborage-code-go-bricks/76c1e566-1d9f-4523-9a92-958252ff2da3/scratchpad/` and run `git commit -F <that file>`. Never `git commit -m` with a heredoc; never `--no-gpg-sign`. After each commit run `git log -1 --pretty='%h %G? %s'` and confirm the signature marker is **not** `N`.
- **Branch:** work on `feature/app-maintenance-manager-side` (Stack A PR4, stacked on PR3b `feature/app-slots-start-stop`; Tasks 1–3 touch no `app/` file and were cut while PR3b was in flight, so the branch is re-parented onto PR3b before Task 4). Never push; the controller pushes.
- **Every commit step is preceded by two checks:** `git branch --show-current` must print `feature/app-maintenance-manager-side`, and `make check` must pass on the tree about to be committed. Scoped `go test` commands in each step are the other test runs an implementer performs.
- **Do not run** `make mutate`, `/simplify`, `/security-audit` or `/code-review` — those are the controller's gates (Task 6).

## File Structure

| File | Status | Responsibility after this PR |
| --- | --- | --- |
| `internal/resourcepool/cleanup_warning.go` | **create** | The single implementation of the cleanup-interval-vs-idle-TTL predicate and its WARN, shared by every manager that owns a pool. Imports `logger` (cycle-free: `logger` has zero go-bricks imports). |
| `internal/resourcepool/cleanup_warning_test.go` | **create** | Table test of the predicate through the exported warner + a field-level assertion; holds the package's capturing `logger.Logger` double. |
| `internal/resourcepool/resourcepool_test.go` | modify | Gains one test proving a second `StartCleanup` spawns no second goroutine (channel identity). |
| `database/manager.go` | modify | `DbManagerOptions.CleanupInterval` (additive), `defaultCleanupInterval` const, constructor starts the sweep + emits the WARN, `Close` doc names the stop. |
| `database/manager_test.go` | modify | New constructor-starts-cleanup and WARN tests; two existing `StartCleanup` tests adjusted for the now-running loop. |
| `database/testhelpers_test.go` | modify | Gains `warnRecorder`, the package's capturing `logger.Logger` double. |
| `messaging/manager.go` | modify | Same three changes as `database/manager.go`, with the publisher pool and the 2-minute default. |
| `messaging/manager_test.go` | modify | New constructor-starts-cleanup and WARN tests; `TestMessagingManagerStatsTracksIdleCleanups` and the idempotency test adjusted. |
| `app/managers.go` | modify | `BuildDatabaseOptions`/`BuildMessagingOptions` thread the two `cleanupinterval` config keys. |
| `app/managers_test.go` | modify | Two assertions pinning that threading. |
| `app/lifecycle.go` | modify | Loses `startMaintenanceLoops`, `cleanupIntervalTooLate`, `warnIfCleanupIntervalTooLate`, `shutdownManagers` and their two call sites. |
| `app/lifecycle_test.go` | modify | Loses the four tests for those functions, the `testmocks` import and the `testKey` const. |
| `wiki/migrations.md` | modify | New atom `[C60.5]`; E60 gist + ladder row updated. |
| `wiki/adr_067_lifecycle_slots.md` | modify | "Watch" bullet names `[C60.5]`. |
| `wiki/adr_029_graceful_shutdown_order.md` | modify | Superseded-in-part note for the retired phase 5. |
| `wiki/architecture_decisions.md` | modify | ADR-029 index entry carries the same note. |
| `wiki/database.md`, `wiki/messaging.md` | modify | One sentence each: the sweep starts at construction, stops in `Close`. |

**Not edited, deliberately:** `wiki/cache.md` (cache behavior is unchanged — it is the precedent being copied), `llms.txt` (its `cleanupinterval` lines are YAML samples with no timing claim), `wiki/startup_defaults.md` (contains no cleanup-loop statement — verified by grep), `CLAUDE.md` (over its 40,960-byte ceiling; this is a silent-behavior change with no Go API break, so it does not earn a `## Breaking Changes` line).

## Facts verified against the tree before writing this plan

Read these before doubting a code block — they are the load-bearing surprises:

1. **`resourcepool.Pool.StartCleanup` is already idempotent** (`if p.cleanupStop != nil { return // already running }`) and its doc comment already says so. Task 1 therefore *pins* the behavior rather than building it, and verifies RED by temporarily removing that guard.
2. **`Pool.Close` already stops the loop**: `closeOnce.Do` calls `p.StopCleanup()` first, and `StopCleanup` *joins* the goroutine. `DbManager.Close` → `pool.Close`, `Manager.Close` → `pubPool.Close`. So "stop it in `Close()`" needs **no production change** — only a doc sentence and a test that the manager still closes cleanly with a live loop.
3. **`IdleTTL > 0` is always true inside these two constructors.** `NewDbManager` coerces `opts.IdleTTL <= 0` to 30m and `NewMessagingManager` coerces it to 1h *before* building the pool. So an `if opts.IdleTTL > 0` guard around the self-start would be a permanently-true branch — dead code and a guaranteed surviving mutant. The plan therefore calls `StartCleanup` unconditionally and leaves the "no TTL → no loop" guard where it already lives and is already tested: `Pool.StartCleanup`'s `if p.idleTTL <= 0` (`TestPoolStartCleanupNoOpConditions`).
4. **`messaging/manager_test.go`'s `TestMessagingManagerStatsTracksIdleCleanups` breaks** unless it is updated: it builds a manager with `IdleTTL: 10ms` and then calls `StartCleanup(10ms)`. Once the constructor self-starts with the 2-minute default, that call becomes a no-op and the test's `assert.Eventually` times out. Task 3 fixes it by passing `CleanupInterval: 10 * time.Millisecond` in the options.
5. `logger` imports **no** go-bricks package, so `internal/resourcepool` → `logger` introduces no import cycle.
6. Only `app/managers.go` constructs these two managers in non-test code. No caller passes a nil logger, so the shared warner needs no nil-logger guard.

---

### Task 1: `internal/resourcepool` — pin `StartCleanup` idempotency and add the shared cleanup-interval WARN

**Files:**

- Create: `internal/resourcepool/cleanup_warning.go`
- Create: `internal/resourcepool/cleanup_warning_test.go`
- Modify: `internal/resourcepool/resourcepool_test.go` (append one test)

**Interfaces:**

- Consumes: `logger.Logger` / `logger.LogEvent` from `github.com/gaborage/go-bricks/logger`; the existing unexported `Pool.cleanupMu`, `Pool.cleanupStop`, `Pool.cleanupDone` fields (same-package test access).
- Produces: `resourcepool.WarnIfCleanupIntervalTooLate(log logger.Logger, keyPrefix string, cleanupInterval, idleTTL time.Duration)` — the exported helper Tasks 2 and 3 call from `NewDbManager` and `NewMessagingManager`. Also the unexported `cleanupIntervalTooLate(cleanupInterval, idleTTL time.Duration) bool` used only inside the package.

- [ ] **Step 1: Write the failing idempotency pin**

Append to `internal/resourcepool/resourcepool_test.go` (after `TestPoolStartStopCleanupIdempotent`, which stays — it covers the no-panic plumbing):

```go
// TestPoolStartCleanupSecondCallKeepsOneLoop pins that a second StartCleanup spawns no second
// goroutine. A second loop would have to overwrite cleanupStop/cleanupDone (StartCleanup assigns
// both), orphaning the first loop with no channel left to stop it — so channel identity across
// the two calls is the observable proof that exactly one loop exists.
func TestPoolStartCleanupSecondCallKeepsOneLoop(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 40*time.Millisecond, tr.closer)
	defer p.Close()

	p.StartCleanup(20 * time.Millisecond)
	p.cleanupMu.Lock()
	firstStop, firstDone := p.cleanupStop, p.cleanupDone
	p.cleanupMu.Unlock()
	require.NotNil(t, firstStop, "the first StartCleanup must start a loop")

	p.StartCleanup(5 * time.Millisecond)
	p.cleanupMu.Lock()
	secondStop, secondDone := p.cleanupStop, p.cleanupDone
	p.cleanupMu.Unlock()

	assert.Equal(t, firstStop, secondStop, "a second StartCleanup must not replace the running loop's stop channel")
	assert.Equal(t, firstDone, secondDone, "a second StartCleanup must not replace the running loop's done channel")

	p.StopCleanup()
	select {
	case <-firstDone:
	default:
		t.Fatal("one StopCleanup must have joined the single running loop")
	}
}
```

- [ ] **Step 2: Verify it fails — by mutation, because the production behavior already exists**

This test pins behavior that `StartCleanup` already has, so it passes on an unmodified tree. Prove it can actually catch the regression by removing the guard, watching it fail, and restoring:

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
# 1. Confirm it passes on the real code.
go test ./internal/resourcepool/ -run TestPoolStartCleanupSecondCallKeepsOneLoop -count=1 -race
# 2. Mutate: delete the "already running" short-circuit in StartCleanup.
python3 - <<'PY'
import pathlib
p = pathlib.Path("internal/resourcepool/resourcepool.go")
s = p.read_text()
guard = "\tif p.cleanupStop != nil {\n\t\treturn // already running\n\t}\n"
assert guard in s, "guard text drifted — re-read StartCleanup before mutating"
p.write_text(s.replace(guard, "", 1))
PY
go test ./internal/resourcepool/ -run TestPoolStartCleanupSecondCallKeepsOneLoop -count=1
# 3. Restore.
git checkout -- internal/resourcepool/resourcepool.go
```

Expected: run 1 PASS · run 2 **FAIL** with `a second StartCleanup must not replace the running loop's stop channel` · after restore, `git diff --stat internal/resourcepool/resourcepool.go` prints nothing.

- [ ] **Step 3: Write the failing WARN test**

Create `internal/resourcepool/cleanup_warning_test.go`:

```go
package resourcepool

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// warnRecorder is a logger.Logger double that records the message and fields of every Warn()
// event. Non-Warn levels are discarded (nil sink).
type warnRecorder struct {
	msgs   []string
	fields []map[string]any
}

func (r *warnRecorder) Info() logger.LogEvent                   { return &warnEvent{} }
func (r *warnRecorder) Error() logger.LogEvent                  { return &warnEvent{} }
func (r *warnRecorder) Debug() logger.LogEvent                  { return &warnEvent{} }
func (r *warnRecorder) Fatal() logger.LogEvent                  { return &warnEvent{} }
func (r *warnRecorder) Warn() logger.LogEvent                   { return &warnEvent{sink: r, fields: map[string]any{}} }
func (r *warnRecorder) WithContext(any) logger.Logger           { return r }
func (r *warnRecorder) WithFields(map[string]any) logger.Logger { return r }

type warnEvent struct {
	sink   *warnRecorder
	fields map[string]any
}

func (e *warnEvent) Msg(msg string) {
	if e.sink == nil {
		return
	}
	e.sink.msgs = append(e.sink.msgs, msg)
	e.sink.fields = append(e.sink.fields, e.fields)
}
func (e *warnEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }
func (e *warnEvent) Err(error) logger.LogEvent       { return e }
func (e *warnEvent) Str(k, v string) logger.LogEvent { return e.set(k, v) }
func (e *warnEvent) Int(k string, v int) logger.LogEvent {
	return e.set(k, v)
}
func (e *warnEvent) Int64(k string, v int64) logger.LogEvent   { return e.set(k, v) }
func (e *warnEvent) Uint64(k string, v uint64) logger.LogEvent { return e.set(k, v) }
func (e *warnEvent) Dur(k string, v time.Duration) logger.LogEvent {
	return e.set(k, v)
}
func (e *warnEvent) Interface(k string, v any) logger.LogEvent { return e.set(k, v) }
func (e *warnEvent) Bytes(k string, v []byte) logger.LogEvent  { return e.set(k, v) }
func (e *warnEvent) Bool(k string, v bool) logger.LogEvent     { return e.set(k, v) }
func (e *warnEvent) Enabled() bool                             { return true }

func (e *warnEvent) set(k string, v any) logger.LogEvent {
	if e.fields != nil {
		e.fields[k] = v
	}
	return e
}

func TestWarnIfCleanupIntervalTooLate(t *testing.T) {
	tests := []struct {
		name            string
		cleanupInterval time.Duration
		idleTTL         time.Duration
		wantWarn        bool
	}{
		{name: "cleanup_greater_than_idle_warns", cleanupInterval: 15 * time.Minute, idleTTL: 10 * time.Minute, wantWarn: true},
		{name: "cleanup_equals_idle_warns", cleanupInterval: 10 * time.Minute, idleTTL: 10 * time.Minute, wantWarn: true},
		{name: "cleanup_below_idle_ok", cleanupInterval: 2 * time.Minute, idleTTL: 1 * time.Hour, wantWarn: false},
		{name: "zero_idle_ttl_skipped", cleanupInterval: 5 * time.Minute, idleTTL: 0, wantWarn: false},
		{name: "zero_cleanup_below_positive_idle_ok", cleanupInterval: 0, idleTTL: 1 * time.Hour, wantWarn: false},
		{name: "negative_idle_ttl_skipped", cleanupInterval: 5 * time.Minute, idleTTL: -1 * time.Second, wantWarn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &warnRecorder{}
			WarnIfCleanupIntervalTooLate(rec, "database.manager", tc.cleanupInterval, tc.idleTTL)

			if !tc.wantWarn {
				assert.Empty(t, rec.msgs, "an interval that sweeps more often than the TTL must stay silent")
				return
			}
			require.Len(t, rec.msgs, 1, "a late cleanup interval must WARN exactly once")
			assert.Equal(t,
				"database.manager.cleanupinterval is >= database.manager.idlettl; "+
					"idle handle eviction will lag by up to one extra cleanup cycle "+
					"(lower database.manager.cleanupinterval or raise database.manager.idlettl)",
				rec.msgs[0])
			assert.Equal(t, "database.manager", rec.fields[0]["resource"])
			assert.Equal(t, tc.cleanupInterval, rec.fields[0]["cleanupinterval"])
			assert.Equal(t, tc.idleTTL, rec.fields[0]["idlettl"])
		})
	}
}

// TestWarnIfCleanupIntervalTooLateUsesTheCallersKeyPrefix pins that the message and the
// "resource" field are built from the caller's prefix, so the messaging manager's WARN names
// messaging.publisher rather than the database's keys.
func TestWarnIfCleanupIntervalTooLateUsesTheCallersKeyPrefix(t *testing.T) {
	rec := &warnRecorder{}
	WarnIfCleanupIntervalTooLate(rec, "messaging.publisher", time.Minute, time.Minute)

	require.Len(t, rec.msgs, 1)
	assert.Contains(t, rec.msgs[0], "messaging.publisher.cleanupinterval is >= messaging.publisher.idlettl")
	assert.Equal(t, "messaging.publisher", rec.fields[0]["resource"])
}
```

- [ ] **Step 4: Run to verify it fails**

Run: `go test ./internal/resourcepool/ -run TestWarnIfCleanupIntervalTooLate -count=1`
Expected: **FAIL** to compile — `undefined: WarnIfCleanupIntervalTooLate`.

- [ ] **Step 5: Write the minimal implementation**

Create `internal/resourcepool/cleanup_warning.go`:

```go
package resourcepool

import (
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// cleanupIntervalTooLate reports whether cleanupInterval sweeps no more often than idleTTL.
// A non-positive idleTTL disables idle cleanup outright, so there is nothing to lag behind.
func cleanupIntervalTooLate(cleanupInterval, idleTTL time.Duration) bool {
	if idleTTL <= 0 {
		return false
	}
	return cleanupInterval >= idleTTL
}

// WarnIfCleanupIntervalTooLate WARNs (never fails) when a pool's sweep runs no more often than
// its idle TTL, so an idle handle lingers up to one extra cycle. It lives beside the pool that
// owns both values — not in config.Validate, which has no logger — and is shared by every
// manager that owns a pool so the message cannot drift between kinds. keyPrefix is the
// operator-facing config prefix, e.g. "database.manager" or "messaging.publisher".
func WarnIfCleanupIntervalTooLate(log logger.Logger, keyPrefix string, cleanupInterval, idleTTL time.Duration) {
	if !cleanupIntervalTooLate(cleanupInterval, idleTTL) {
		return
	}
	log.Warn().
		Str("resource", keyPrefix).
		Dur("cleanupinterval", cleanupInterval).
		Dur("idlettl", idleTTL).
		Msg(keyPrefix + ".cleanupinterval is >= " + keyPrefix + ".idlettl; " +
			"idle handle eviction will lag by up to one extra cleanup cycle " +
			"(lower " + keyPrefix + ".cleanupinterval or raise " + keyPrefix + ".idlettl)")
}
```

- [ ] **Step 6: Run to verify the package is green**

Run: `go test ./internal/resourcepool/ -count=1 -race`
Expected: PASS, no warnings.

- [ ] **Step 7: Commit**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
SCRATCH=/private/tmp/claude-501/-Users-gaborage-Projects-gaborage-code-go-bricks/76c1e566-1d9f-4523-9a92-958252ff2da3/scratchpad
cat > "$SCRATCH/msg1.txt" <<'EOF'
refactor(resourcepool): add the shared cleanup-interval WARN beside the pool

The cleanup-interval-vs-idle-TTL advisory lived in app/lifecycle.go, which is
the wrong owner: the pool holds both values, and after ADR-067 the managers —
not App — drive the sweep. Move the predicate and its WARN here so DbManager
and messaging.Manager share one implementation and one message string, and
pin StartCleanup's existing idempotency with a test that actually observes the
single loop (channel identity) rather than only asserting no panic.

Refs: ADR-067
EOF
git add internal/resourcepool/cleanup_warning.go internal/resourcepool/cleanup_warning_test.go internal/resourcepool/resourcepool_test.go
git commit -F "$SCRATCH/msg1.txt"
git log -1 --pretty='%h %G? %s'
```

Expected: the `%G?` column is `G` or `U` — **never** `N`. If it prints `N` or the commit fails with `failed to fill whole buffer`, stop and ask the user to unlock 1Password; do not pass `--no-gpg-sign`.

---

### Task 2: `database.DbManager` self-starts idle cleanup

**Files:**

- Modify: `database/manager.go` (`DbManagerOptions`, `NewDbManager`, `StartCleanup`, `Close` doc)
- Modify: `database/manager_test.go` (2 new tests, 2 existing tests adjusted)
- Modify: `database/testhelpers_test.go` (add the capturing logger)
- Modify: `app/managers.go` (`BuildDatabaseOptions`)
- Modify: `app/managers_test.go` (1 assertion)

**Interfaces:**

- Consumes: `resourcepool.WarnIfCleanupIntervalTooLate` (Task 1); `config.DatabaseManagerConfig.CleanupInterval` (already exists, `koanf:"cleanupinterval"`, defaulted to 5m by `config.Validate`).
- Produces: `database.DbManagerOptions.CleanupInterval time.Duration` — the additive field `app.ManagerConfigBuilder.BuildDatabaseOptions` fills and Task 4 relies on for the deletion to be behavior-preserving. `database.defaultCleanupInterval = 5 * time.Minute` (unexported).

- [ ] **Step 1: Write the failing tests**

Add to `database/testhelpers_test.go` (append; keep the existing two helpers):

```go
// warnRecorder is a logger.Logger double that records the message of every Warn() event.
// Other levels are discarded. NewDbManager logs nothing but this WARN at construction.
type warnRecorder struct{ warns []string }

func (r *warnRecorder) Info() logger.LogEvent                   { return &recordedEvent{} }
func (r *warnRecorder) Error() logger.LogEvent                  { return &recordedEvent{} }
func (r *warnRecorder) Debug() logger.LogEvent                  { return &recordedEvent{} }
func (r *warnRecorder) Fatal() logger.LogEvent                  { return &recordedEvent{} }
func (r *warnRecorder) Warn() logger.LogEvent                   { return &recordedEvent{sink: r} }
func (r *warnRecorder) WithContext(any) logger.Logger           { return r }
func (r *warnRecorder) WithFields(map[string]any) logger.Logger { return r }

// recordedEvent appends to its sink on Msg; a nil sink discards, which is how the non-Warn
// levels are served.
type recordedEvent struct{ sink *warnRecorder }

func (e *recordedEvent) Msg(msg string) {
	if e.sink != nil {
		e.sink.warns = append(e.sink.warns, msg)
	}
}
func (e *recordedEvent) Msgf(format string, args ...any)           { e.Msg(fmt.Sprintf(format, args...)) }
func (e *recordedEvent) Err(error) logger.LogEvent                 { return e }
func (e *recordedEvent) Str(_, _ string) logger.LogEvent           { return e }
func (e *recordedEvent) Int(string, int) logger.LogEvent           { return e }
func (e *recordedEvent) Int64(string, int64) logger.LogEvent       { return e }
func (e *recordedEvent) Uint64(string, uint64) logger.LogEvent     { return e }
func (e *recordedEvent) Dur(string, time.Duration) logger.LogEvent { return e }
func (e *recordedEvent) Interface(string, any) logger.LogEvent     { return e }
func (e *recordedEvent) Bytes(string, []byte) logger.LogEvent      { return e }
func (e *recordedEvent) Bool(string, bool) logger.LogEvent         { return e }
func (e *recordedEvent) Enabled() bool                             { return true }
```

Its import block becomes:

```go
import (
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/logger"
	testconsts "github.com/gaborage/go-bricks/testing"
)
```

Add to `database/manager_test.go` (place them right before the existing `TestStartCleanupIsIdempotent`):

```go
// TestNewDbManagerStartsIdleCleanup pins ADR-067 decision 4: the manager starts its own idle
// sweep at construction, exactly as cache.NewCacheManager does. No StartCleanup call appears
// in this test — a swept connection is the proof that the constructor started the loop.
func TestNewDbManagerStartsIdleCleanup(t *testing.T) {
	src := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		tenantA: {Type: "postgresql", Database: tenantA},
	}}
	m := NewDbManager(src, newErrorTestLogger(), DbManagerOptions{
		MaxSize:         5,
		IdleTTL:         10 * time.Millisecond,
		CleanupInterval: 10 * time.Millisecond,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{key: tenantA}, nil })
	defer func() { _ = m.Close() }()

	_, release, err := m.Get(context.Background(), tenantA)
	require.NoError(t, err)
	release()

	assert.Eventually(t, func() bool {
		return m.Stats()["active_connections"] == 0
	}, 2*time.Second, 10*time.Millisecond, "the constructor must start the idle-cleanup sweep")
}

// TestNewDbManagerClosesCleanlyWithALiveCleanupLoop pins the other half of ADR-067 decision 4:
// the sweep the constructor started is stopped by Close (pool.Close joins the loop), so a
// caller that never touches StartCleanup/StopCleanup still shuts down cleanly.
func TestNewDbManagerClosesCleanlyWithALiveCleanupLoop(t *testing.T) {
	m := NewDbManager(&stubResourceSource{}, newErrorTestLogger(), DbManagerOptions{
		MaxSize:         5,
		IdleTTL:         10 * time.Millisecond,
		CleanupInterval: 10 * time.Millisecond,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{}, nil })

	require.NoError(t, m.Close(), "Close must stop the constructor-started sweep and report success")
	require.NoError(t, m.Close(), "Close stays idempotent")
}

// TestNewDbManagerWarnsWhenCleanupIntervalIsNotBelowIdleTTL pins that the advisory that used to
// live in App.warnIfCleanupIntervalTooLate now fires from the manager that owns the pool.
func TestNewDbManagerWarnsWhenCleanupIntervalIsNotBelowIdleTTL(t *testing.T) {
	tests := []struct {
		name            string
		cleanupInterval time.Duration
		idleTTL         time.Duration
		wantWarn        bool
	}{
		{name: "interval_equals_ttl_warns", cleanupInterval: time.Minute, idleTTL: time.Minute, wantWarn: true},
		{name: "interval_above_ttl_warns", cleanupInterval: 2 * time.Minute, idleTTL: time.Minute, wantWarn: true},
		{name: "interval_below_ttl_silent", cleanupInterval: time.Minute, idleTTL: time.Hour, wantWarn: false},
		{name: "unset_interval_takes_the_default_and_stays_silent", cleanupInterval: 0, idleTTL: time.Hour, wantWarn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &warnRecorder{}
			m := NewDbManager(&stubResourceSource{}, rec, DbManagerOptions{
				MaxSize:         5,
				IdleTTL:         tc.idleTTL,
				CleanupInterval: tc.cleanupInterval,
			}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{}, nil })
			defer func() { _ = m.Close() }()

			if !tc.wantWarn {
				assert.Empty(t, rec.warns, "a sweep that outpaces the TTL must not WARN")
				return
			}
			require.Len(t, rec.warns, 1, "the advisory must fire exactly once per manager")
			assert.Contains(t, rec.warns[0], "database.manager.cleanupinterval is >= database.manager.idlettl")
		})
	}
}
```

Adjust the two existing tests so they still exercise what their names claim now that a loop is already running (replace the bodies in place):

```go
func TestStartCleanupIsIdempotent(t *testing.T) {
	m := NewDbManager(&stubResourceSource{}, newTestLogger(), DbManagerOptions{
		MaxSize: 5,
		IdleTTL: time.Hour,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{}, nil })
	defer func() { _ = m.Close() }()

	// The constructor already started a loop (ADR-067); stop it so the first call below is
	// the one that starts a loop and the second is the one that must short-circuit.
	m.StopCleanup()

	m.StartCleanup(10 * time.Second)
	require.NotPanics(t, func() {
		m.StartCleanup(10 * time.Second)
	})

	m.StopCleanup()
	// Second StopCleanup hits the early-return path (no loop running).
	require.NotPanics(t, func() {
		m.StopCleanup()
	})
}

func TestStartCleanupAppliesDefaultIntervalForNonPositive(t *testing.T) {
	m := NewDbManager(&stubResourceSource{}, newTestLogger(), DbManagerOptions{
		MaxSize: 5,
		IdleTTL: time.Hour,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{}, nil })
	defer func() { _ = m.Close() }()

	m.StopCleanup() // drop the constructor's loop so these calls are the ones that start one

	// Zero substitutes the documented 5-min default; we can't inspect the
	// ticker directly so the contract is "no panic + clean stop".
	require.NotPanics(t, func() { m.StartCleanup(0) })
	m.StopCleanup()

	require.NotPanics(t, func() { m.StartCleanup(-5 * time.Second) })
	m.StopCleanup()
}
```

Add to `app/managers_test.go`, inside `TestManagerConfigBuilderHonorsConfigDefaults`'s `"operator override reaches database options"` subtest (extend the existing subtest, do not add a new one):

```go
		builder.dbConfig = config.DatabaseManagerConfig{MaxSize: 33, IdleTTL: 8 * time.Minute, CleanupInterval: 90 * time.Second}
		opts := builder.BuildDatabaseOptions()
		assert.Equal(t, 33, opts.MaxSize, "operator database.manager.maxsize override must reach DbManagerOptions")
		assert.Equal(t, 8*time.Minute, opts.IdleTTL, "operator database.manager.idlettl override must reach DbManagerOptions")
		assert.Equal(t, 90*time.Second, opts.CleanupInterval, "operator database.manager.cleanupinterval override must reach DbManagerOptions")
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
go test ./database/ -run 'TestNewDbManager' -count=1
go test ./app/ -run TestManagerConfigBuilderHonorsConfigDefaults -count=1
```

Expected: both **FAIL** to compile — `unknown field CleanupInterval in struct literal of type DbManagerOptions`.

- [ ] **Step 3: Write the minimal implementation**

In `database/manager.go`, add the const above `DbManagerOptions`:

```go
// defaultCleanupInterval is the documented idle-sweep frequency (database.manager.cleanupinterval)
// applied when the caller supplies none.
const defaultCleanupInterval = 5 * time.Minute
```

Replace `DbManagerOptions`:

```go
// DbManagerOptions configures the DbManager
type DbManagerOptions struct {
	MaxSize int           // Cached-connection cap; <=0 uses a default (not unlimited).
	IdleTTL time.Duration // Idle-connection lifetime; <=0 uses a default (not disabled).
	// CleanupInterval is how often the idle sweep runs; <=0 uses the documented 5-minute
	// default. The manager starts that sweep itself at construction (ADR-067).
	CleanupInterval time.Duration
}
```

Replace `NewDbManager`:

```go
// NewDbManager creates a new database manager. The idle-cleanup sweep starts here and stops in
// Close, so callers need not drive it (ADR-067); StartCleanup remains available and idempotent.
func NewDbManager(resourceSource DBConfigProvider, log logger.Logger, opts DbManagerOptions, connector Connector) *DbManager {
	if opts.MaxSize <= 0 {
		opts.MaxSize = 100 // sensible default
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 30 * time.Minute // sensible default
	}
	if opts.CleanupInterval <= 0 {
		opts.CleanupInterval = defaultCleanupInterval
	}

	// Default to real connection factory if none provided
	if connector == nil {
		connector = NewConnection
	}

	m := &DbManager{
		logger:         log,
		resourceSource: resourceSource,
		connector:      connector,
		pool: resourcepool.New[Interface](opts.MaxSize, opts.IdleTTL, func(conn Interface) error {
			return conn.Close()
		}),
	}

	resourcepool.WarnIfCleanupIntervalTooLate(log, "database.manager", opts.CleanupInterval, opts.IdleTTL)
	m.pool.StartCleanup(opts.CleanupInterval)

	return m
}
```

Replace `StartCleanup`'s literal with the const:

```go
// StartCleanup starts the background cleanup routine for idle connections. A non-positive
// interval substitutes the documented 5-minute default. The constructor already started a
// sweep, so this is a no-op unless StopCleanup ran first (the pool's loop is single-instance).
func (m *DbManager) StartCleanup(interval time.Duration) {
	if m.pool == nil {
		return // zero-value manager: nothing to run, consistent with the other nil-pool guards
	}
	if interval <= 0 {
		interval = defaultCleanupInterval
	}
	m.pool.StartCleanup(interval)
}
```

Extend `Close`'s doc comment first line (leave the body untouched — `pool.Close` already stops and joins the loop):

```go
// Close closes all database connections and stops the idle-cleanup sweep the constructor
// started. A connection still borrowed by in-flight work is closed at its final release
// instead of by this call (wiki/migrations.md C581.3).
```

In `app/managers.go`, replace `BuildDatabaseOptions`:

```go
// BuildDatabaseOptions creates database manager options from validated config.
func (b *ManagerConfigBuilder) BuildDatabaseOptions() database.DbManagerOptions {
	return database.DbManagerOptions{
		MaxSize:         b.resolveMaxSize(b.dbConfig.MaxSize),
		IdleTTL:         b.dbConfig.IdleTTL,
		CleanupInterval: b.dbConfig.CleanupInterval,
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
go test ./database/ -count=1 -race
go test ./app/ -run TestManagerConfigBuilder -count=1
```

Expected: both PASS. If `TestNewDbManagerStartsIdleCleanup` is flaky under load, that is a real signal — the sweep is not running; do not raise the timeout past 2s without checking `git diff database/manager.go`.

- [ ] **Step 5: Commit**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
SCRATCH=/private/tmp/claude-501/-Users-gaborage-Projects-gaborage-code-go-bricks/76c1e566-1d9f-4523-9a92-958252ff2da3/scratchpad
cat > "$SCRATCH/msg2.txt" <<'EOF'
refactor(database): DbManager starts and stops its own idle cleanup

ADR-067 decision 4: maintenance belongs to the manager that owns the pool, not
to App. DbManagerOptions gains an additive CleanupInterval (apidiff-compatible;
app.ManagerConfigBuilder now threads database.manager.cleanupinterval into it),
the constructor starts the sweep and emits the interval-vs-idle-TTL advisory
from its new home in internal/resourcepool, and Close already stopped the loop
via pool.Close. StartCleanup and StopCleanup stay exported and idempotent, so a
consumer that drives the sweep by hand keeps working with no second goroutine.

The sweep now begins at construction rather than at prepareRuntime — a few
milliseconds earlier on the framework's own boot path.

Refs: ADR-067
EOF
git add database/manager.go database/manager_test.go database/testhelpers_test.go app/managers.go app/managers_test.go
git commit -F "$SCRATCH/msg2.txt"
git log -1 --pretty='%h %G? %s'
```

---

### Task 3: `messaging.Manager` self-starts publisher idle cleanup

**Files:**

- Modify: `messaging/manager.go` (`ManagerOptions`, `NewMessagingManager`, `StartCleanup`, `Close` doc)
- Modify: `messaging/manager_test.go` (2 new tests, 2 existing tests adjusted)
- Modify: `app/managers.go` (`BuildMessagingOptions`)
- Modify: `app/managers_test.go` (1 assertion)

**Interfaces:**

- Consumes: `resourcepool.WarnIfCleanupIntervalTooLate` (Task 1); `config.PublisherPoolConfig.CleanupInterval` (exists, defaulted to 2m by `config.Validate`); the existing same-package `stubLogger` in `messaging/amqp_test.go` (records every `Msg` into `entries`, read via `getEntries()`).
- Produces: `messaging.ManagerOptions.CleanupInterval time.Duration` (additive) and `messaging.defaultPublisherCleanupInterval = 2 * time.Minute` (unexported).

- [ ] **Step 1: Write the failing tests**

Add to `messaging/manager_test.go` (place before the existing `TestMessagingManagerStartCleanupIsIdempotent`):

```go
// TestNewMessagingManagerStartsIdleCleanup pins ADR-067 decision 4 for the publisher pool: the
// sweep starts at construction, with no StartCleanup call from the caller.
func TestNewMessagingManagerStartsIdleCleanup(t *testing.T) {
	ctx := context.Background()
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond, CleanupInterval: 10 * time.Millisecond},
		factory,
	)
	defer func() { _ = manager.Close() }()

	_, rel, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	rel()

	assert.Eventually(t, func() bool {
		count, _ := manager.Stats()["idle_cleanups"].(int)
		return count >= 1
	}, 2*time.Second, 10*time.Millisecond, "the constructor must start the idle-publisher sweep")
}

// TestNewMessagingManagerClosesCleanlyWithALiveCleanupLoop pins that Close stops the sweep the
// constructor started, so a caller that never touches StartCleanup/StopCleanup shuts down clean.
func TestNewMessagingManagerClosesCleanlyWithALiveCleanupLoop(t *testing.T) {
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond, CleanupInterval: 10 * time.Millisecond},
		factory,
	)

	require.NoError(t, manager.Close(), "Close must stop the constructor-started sweep and report success")
	require.NoError(t, manager.Close(), "Close stays idempotent")
}

// TestNewMessagingManagerWarnsWhenCleanupIntervalIsNotBelowIdleTTL pins that the advisory that
// used to live in App.warnIfCleanupIntervalTooLate now fires from the manager, naming the
// messaging.publisher keys.
func TestNewMessagingManagerWarnsWhenCleanupIntervalIsNotBelowIdleTTL(t *testing.T) {
	tests := []struct {
		name            string
		cleanupInterval time.Duration
		idleTTL         time.Duration
		wantWarn        bool
	}{
		{name: "interval_equals_ttl_warns", cleanupInterval: time.Minute, idleTTL: time.Minute, wantWarn: true},
		{name: "interval_above_ttl_warns", cleanupInterval: 2 * time.Minute, idleTTL: time.Minute, wantWarn: true},
		{name: "interval_below_ttl_silent", cleanupInterval: time.Minute, idleTTL: time.Hour, wantWarn: false},
		{name: "unset_interval_takes_the_default_and_stays_silent", cleanupInterval: 0, idleTTL: time.Hour, wantWarn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log := &stubLogger{}
			factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
			manager := NewMessagingManager(
				&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
				log,
				ManagerOptions{MaxPublishers: 5, IdleTTL: tc.idleTTL, CleanupInterval: tc.cleanupInterval},
				factory,
			)
			defer func() { _ = manager.Close() }()

			entries := log.getEntries()
			if !tc.wantWarn {
				assert.Empty(t, entries, "a sweep that outpaces the TTL must not WARN")
				return
			}
			require.Len(t, entries, 1, "the advisory must fire exactly once per manager")
			assert.Contains(t, entries[0], "messaging.publisher.cleanupinterval is >= messaging.publisher.idlettl")
		})
	}
}
```

Adjust `TestMessagingManagerStatsTracksIdleCleanups` — its explicit `StartCleanup(10ms)` becomes a no-op once the constructor owns the loop, so move the interval into the options (replace the two marked lines, keep the rest of the test as-is):

```go
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond, CleanupInterval: 10 * time.Millisecond},
		factory,
	)
	defer func() { _ = manager.Close() }()

	_, rel, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	rel() // release so the idle publisher becomes eligible for cleanup

	assert.Eventually(t, func() bool {
		count, _ := manager.Stats()["idle_cleanups"].(int)
		return count >= 1
	}, 2*time.Second, 20*time.Millisecond, "Stats must surface the pool's idle-cleanup count under idle_cleanups")
```

(The `manager.StartCleanup(10 * time.Millisecond)` and `defer manager.StopCleanup()` lines are deleted.)

Adjust `TestMessagingManagerStartCleanupIsIdempotent` so the first call is the one that starts a loop — insert `m.StopCleanup()` immediately after the constructor call and before the first `m.StartCleanup(10 * time.Second)`, with this comment:

```go
	// The constructor already started a loop (ADR-067); stop it so the first call below is the
	// one that starts a loop and the second is the one that must short-circuit.
	m.StopCleanup()
```

Do the same in `TestMessagingManagerStartCleanupAppliesDefaultForNonPositive` (insert `m.StopCleanup()` right after construction).

Extend `app/managers_test.go`'s `"operator override reaches messaging options"` subtest:

```go
		builder.publisherConfig = config.PublisherPoolConfig{MaxCached: 77, IdleTTL: 3 * time.Minute, CleanupInterval: 45 * time.Second}
		opts := builder.BuildMessagingOptions()
		assert.Equal(t, 77, opts.MaxPublishers, "operator messaging.publisher.maxcached override must reach ManagerOptions")
		assert.Equal(t, 3*time.Minute, opts.IdleTTL, "operator messaging.publisher.idlettl override must reach ManagerOptions")
		assert.Equal(t, 45*time.Second, opts.CleanupInterval, "operator messaging.publisher.cleanupinterval override must reach ManagerOptions")
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
go test ./messaging/ -run 'TestNewMessagingManager' -count=1
```

Expected: **FAIL** to compile — `unknown field CleanupInterval in struct literal of type ManagerOptions`.

- [ ] **Step 3: Write the minimal implementation**

In `messaging/manager.go`, add the const above `ManagerOptions`:

```go
// defaultPublisherCleanupInterval is the documented idle-sweep frequency
// (messaging.publisher.cleanupinterval) applied when the caller supplies none.
const defaultPublisherCleanupInterval = 2 * time.Minute
```

Add the field to `ManagerOptions`, immediately after `IdleTTL`:

```go
	// CleanupInterval is how often the idle-publisher sweep runs; <=0 uses the documented
	// 2-minute default. The manager starts that sweep itself at construction (ADR-067).
	CleanupInterval time.Duration
```

Replace `NewMessagingManager` (note it must now bind the manager to a variable before returning):

```go
// NewMessagingManager creates a new messaging manager. The idle-publisher sweep starts here and
// stops in Close, so callers need not drive it (ADR-067); StartCleanup remains available and
// idempotent.
func NewMessagingManager(resourceSource BrokerURLProvider, log logger.Logger, opts ManagerOptions, clientFactory ClientFactory) *Manager {
	if opts.MaxPublishers <= 0 {
		opts.MaxPublishers = 50 // sensible default
	}
	if opts.IdleTTL <= 0 {
		// Interface default for bare callers constructing a manager without the app
		// builder; single-tenant value — a bare caller supplies no deployment-mode
		// signal. The app path always arrives with IdleTTL already stamped by
		// config.Validate (ADR-064).
		opts.IdleTTL = 1 * time.Hour
	}
	if opts.CleanupInterval <= 0 {
		opts.CleanupInterval = defaultPublisherCleanupInterval
	}

	// Default to real client factory if none provided
	if clientFactory == nil {
		clientFactory = func(url string, log logger.Logger) AMQPClient {
			return NewAMQPClient(url, log,
				WithConnectionTimeout(opts.ConnectionTimeout),
				WithMaxPublishAttempts(opts.MaxPublishAttempts),
				WithReadyTimeout(opts.ReadyTimeout),
				WithReconnectDelay(opts.ReconnectDelay),
				WithReconnectMaxDelay(opts.ReconnectMaxDelay),
				WithReinitDelay(opts.ReinitDelay),
				WithResendDelay(opts.ResendDelay),
			)
		}
	}

	m := &Manager{
		logger:         log,
		resourceSource: resourceSource,
		clientFactory:  clientFactory,
		// The pool closer surfaces each publisher's raw Close() error; pool.Close() joins them.
		// Unlike the consumer loop below, these are not wrapped with the per-key label — the
		// closer receives only the AMQPClient value (and key=="" uses a bare client), matching
		// the database rewire's deliberate tradeoff: error coverage and the aggregate prefix are
		// preserved, only the per-publisher-key context is dropped.
		pubPool: resourcepool.New[AMQPClient](opts.MaxPublishers, opts.IdleTTL, func(client AMQPClient) error {
			return client.Close()
		}),
		consumers:     make(map[string]*consumerEntry),
		replayedHashs: make(map[string]uint64),
	}

	resourcepool.WarnIfCleanupIntervalTooLate(log, "messaging.publisher", opts.CleanupInterval, opts.IdleTTL)
	m.pubPool.StartCleanup(opts.CleanupInterval)

	return m
}
```

Replace `StartCleanup`'s literal with the const:

```go
// StartCleanup starts the background cleanup routine for idle publishers. A non-positive
// interval substitutes the documented 2-minute default. The constructor already started a
// sweep, so this is a no-op unless StopCleanup ran first (the pool's loop is single-instance).
func (m *Manager) StartCleanup(interval time.Duration) {
	if m.pubPool == nil {
		return // zero-value manager: nothing to run, consistent with the other nil-pool guards
	}
	if interval <= 0 {
		interval = defaultPublisherCleanupInterval
	}
	m.pubPool.StartCleanup(interval)
}
```

Extend `Close`'s doc comment's first sentence (body unchanged):

```go
// Close closes all clients and stops the idle-publisher sweep the constructor started. Publisher
// closes go through the pool (which stops its own cleanup loop and joins every per-publisher
// close failure); consumer closes are handled directly. A publisher client still borrowed by
// ...
```

In `app/managers.go`, add the field to `BuildMessagingOptions`'s literal, immediately after `IdleTTL`:

```go
		CleanupInterval:    b.publisherConfig.CleanupInterval,
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
go test ./messaging/ -count=1 -race
go test ./app/ -run TestManagerConfigBuilder -count=1
```

Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
SCRATCH=/private/tmp/claude-501/-Users-gaborage-Projects-gaborage-code-go-bricks/76c1e566-1d9f-4523-9a92-958252ff2da3/scratchpad
cat > "$SCRATCH/msg3.txt" <<'EOF'
refactor(messaging): Manager starts and stops its own publisher cleanup

Mirrors the DbManager change one commit earlier (ADR-067 decision 4).
ManagerOptions gains an additive CleanupInterval (apidiff-compatible) that
app.ManagerConfigBuilder fills from messaging.publisher.cleanupinterval; the
constructor starts the idle-publisher sweep and emits the interval-vs-idle-TTL
advisory; Close already stopped the loop via pubPool.Close.

TestMessagingManagerStatsTracksIdleCleanups moved its 10ms interval into the
options: with the manager owning the loop, its explicit StartCleanup call would
now be a no-op and the sweep would never fire inside the test window.

Refs: ADR-067
EOF
git add messaging/manager.go messaging/manager_test.go app/managers.go app/managers_test.go
git commit -F "$SCRATCH/msg3.txt"
git log -1 --pretty='%h %G? %s'
```

---

### Task 4: delete the `app/` maintenance phase

**Files:**

- Modify: `app/lifecycle.go` (delete 4 functions + 2 call sites, renumber one comment)
- Modify: `app/lifecycle_test.go` (delete 4 tests, the `testmocks` import, the `testKey` const)

**Interfaces:**

- Consumes: the manager-side self-start from Tasks 2 and 3 — this deletion is only behavior-preserving because those landed first. Do not reorder.
- Produces: nothing new. `App` keeps `registerCloser("database manager", …)` / `("messaging manager", …)` in `Builder.RegisterClosers`, and `shutdownClosers` still runs them last, so ADR-029's ordering survives via each manager's `Close()`.

- [ ] **Step 1: Write the failing test — none; this step is a deletion, verified by the suite**

There is no new behavior to test: every assertion the four deleted tests made now lives in `database/manager_test.go` and `messaging/manager_test.go` (Tasks 2–3), which is the "replace, don't layer" rule the spec states for this stack. The RED signal for this task is the compiler plus the existing app suite. Proceed to Step 2.

- [ ] **Step 2: Delete the app-side maintenance code**

In `app/lifecycle.go`:

- (1) Delete the whole block from `// startMaintenanceLoops starts background cleanup processes for managers` through the closing brace of `warnIfCleanupIntervalTooLate` — that is `startMaintenanceLoops`, `cleanupIntervalTooLate` and `warnIfCleanupIntervalTooLate`, three consecutive functions ending just before `// prepareRuntime prepares the application for runtime execution.`
- (2) In `prepareRuntime`, delete the trailing call so the tail reads:

```go
	a.registry.RegisterRoutes(a.server.ModuleGroup())
	if err := a.checkRouteConflicts(); err != nil {
		return err
	}

	return nil
}
```

- (3) Delete `shutdownManagers` entirely (the block starting `// shutdownManagers stops cleanup loops for database and messaging managers`).
- (4) In `Shutdown`, delete step 5 and renumber step 6, so the tail of the phase list reads:

```go
	// 4. Flush and shutdown observability (export pending telemetry).
	a.shutdownObservability(ctx, &errs)

	// 5. Close remaining resources (DB pools, messaging connections, etc.). Each manager's
	//    Close stops the idle-cleanup sweep it started (ADR-067), so the loops still stop
	//    last, in the order ADR-029 fixed.
	a.shutdownClosers(&errs)
```

Leave every other import in `app/lifecycle.go` alone — `time`, `config` and `messaging` all remain in use (`shutdownTimeouts`, `assertMessagingConfiguredIfDeclared`, `readyCheck`).

In `app/lifecycle_test.go`:

- (5) Delete these four tests in full: `TestStartMaintenanceLoopsUsesConfiguredPublisherCleanupInterval`, `TestStartMaintenanceLoopsUsesConfiguredDatabaseCleanupInterval`, `TestCleanupIntervalTooLate`, `TestStartMaintenanceLoopsWarnsOnLateCleanupInterval` (a contiguous run ending just before `// TestCheckRouteConflictsAggregatesAndSkips`).
- (6) Delete the now-unused import line `testmocks "github.com/gaborage/go-bricks/testing/mocks"`.
- (7) Delete the now-unused const `testKey = "test-key"`, leaving:

```go
const (
	testApp = "test-app"
)
```

- [ ] **Step 3: Run the suite to verify it passes**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
go build ./... && go vet ./app/...
go test ./app/ -count=1 -race
git grep -nE 'startMaintenanceLoops|warnIfCleanupIntervalTooLate|cleanupIntervalTooLate|shutdownManagers' -- '*.go'
```

Expected: build and vet clean; `./app/` PASS; the final `git grep` prints **nothing** and exits 1. `go vet` is load-bearing here — `go build` does not compile `_test.go`, so an orphaned `testmocks` reference would otherwise slip through.

- [ ] **Step 4: Commit**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
SCRATCH=/private/tmp/claude-501/-Users-gaborage-Projects-gaborage-code-go-bricks/76c1e566-1d9f-4523-9a92-958252ff2da3/scratchpad
cat > "$SCRATCH/msg4.txt" <<'EOF'
refactor(app)!: drop the maintenance phase from the app lifecycle

startMaintenanceLoops, cleanupIntervalTooLate, warnIfCleanupIntervalTooLate and
shutdownManagers all exist only to drive a loop the managers now own, so
prepareRuntime loses its trailing maintenance call and Shutdown loses a whole
phase (ADR-067 decision 4, and the "no maintenance phase" half of decision 2).

ADR-029's ordering is preserved rather than relaxed: cleanup stops inside each
manager's Close, and shutdownClosers still runs those closers last, so the
loops stop exactly where they stopped before — one phase later in the file,
same point in wall-clock order.

The four app tests for these helpers are deleted rather than ported: their
assertions now live in database/manager_test.go and messaging/manager_test.go,
against the code that actually starts the sweep.

Refs: ADR-067
EOF
git add app/lifecycle.go app/lifecycle_test.go
git commit -F "$SCRATCH/msg4.txt"
git log -1 --pretty='%h %G? %s'
```

---

### Task 5: documentation — atom `[C60.5]`, hop E60, ADR notes, two wiki sentences

**Files:**

- Modify: `wiki/migrations.md` (ladder row for E60; E60 gist; new atom `[C60.5]`)
- Modify: `wiki/adr_067_lifecycle_slots.md` (the "Watch:" bullet)
- Modify: `wiki/adr_029_graceful_shutdown_order.md` (superseded-in-part note)
- Modify: `wiki/architecture_decisions.md` (ADR-029 index entry)
- Modify: `wiki/database.md` (one sentence), `wiki/messaging.md` (one sentence)

**Interfaces:**

- Consumes: the behavior shipped in Tasks 2–4.
- Produces: atom id `[C60.5]`, referenced by the E60 row's atom count and by ADR-067's "Watch" bullet.

- [ ] **Step 1: Add the atom to `wiki/migrations.md`**

Insert this block immediately after the `[C60.4]` atom's `- ref:` line and its following `---`, i.e. directly before the italic line that begins `*The sections below are reference material:`:

```markdown
### [C60.5] idle cleanup starts at manager construction, not at `prepareRuntime` · silent-behavior · when: always

- detect: nothing in your Go code changes, so the grep is for operations —
  search your log backend for the five retired lines `Starting database manager cleanup loop`,
  `Starting messaging manager cleanup loop`, `Stopping database manager cleanup loop`,
  `Stopping messaging manager cleanup loop` and `Manager cleanup loops stopped`. If you also
  construct a manager yourself, `git grep -nE '(database\.NewDbManager|messaging\.NewMessagingManager)\(' -- '*.go'`
  finds the call sites the second half of `apply` covers.
- scope: `database.DbManager` and `messaging.Manager` now start their idle-eviction sweep inside
  their constructor and stop it inside `Close()`, exactly as `cache.NewCacheManager` has always
  done (ADR-067 decision 4). On the framework's own boot path that moves the start a few
  milliseconds earlier — from the end of `prepareRuntime` to the Builder's manager-construction
  step — and moves the stop one shutdown phase later, from a dedicated phase to the closers that
  already ran last. **The wall-clock shutdown order is unchanged**: the closers run after modules
  and observability either way (ADR-029). Sweep frequency, idle TTL and eviction semantics are
  untouched, and the HTTP response schema and status codes are stable; manager statistics and
  counters can move with the sweep's earlier start. Three things do change for an
  operator: (1) the five INFO lines above retire with no renamed equivalent — the sweep is no
  longer an app-level phase, so there is no phase to announce; (2) the
  `<prefix>.cleanupinterval is >= <prefix>.idlettl` advisory now fires at manager construction
  instead of at `prepareRuntime`, so it appears earlier in the startup log, with its message and
  its `resource`/`cleanupinterval`/`idlettl` fields byte-identical; and (3) `DbManagerOptions`
  and `messaging.ManagerOptions` gain an additive `CleanupInterval` field — adding a field to an
  exported struct is `apidiff`-compatible, and an unset field takes the same 5m/2m defaults
  `StartCleanup` has always applied.
- gate: always — every service that configures a database or messaging manager starts its sweep
  at a different moment, whether or not it sets `cleanupinterval`.
- apply: for the common case (you call `app.New`/`app.NewWithConfig` and let the framework build
  the managers) there is nothing to do — repoint any alert or saved query that matched one of the
  five retired lines at the manager's own lifecycle, or drop it. If you construct
  `database.NewDbManager` or `messaging.NewMessagingManager` yourself and call `StartCleanup`
  afterwards, that call is now redundant: it is a **no-op** while the constructor's loop is
  running, and it leaks no second goroutine (`StartCleanup` is idempotent). Delete it, or pass
  the interval through the new `CleanupInterval` option instead. The one shape that needs a real
  edit is a caller that used a second `StartCleanup` to *change* the interval on a live manager:
  that no longer takes effect, so call `StopCleanup()` first and then `StartCleanup(newInterval)`.
- verify: start the service and confirm the log carries no `Starting database manager cleanup
  loop` / `Starting messaging manager cleanup loop` line, and that idle eviction still happens —
  `curl -s localhost:8080/ready | jq '.messaging_stats.idle_cleanups'` climbs over a window
  longer than `messaging.publisher.idlettl` on a service with idle tenants. On a deliberately
  misconfigured `cleanupinterval >= idlettl`, the same advisory text appears, now among the
  earliest startup lines.
- ref: [ADR-067](adr_067_lifecycle_slots.md) · `database/manager.go` (`NewDbManager`,
  `StartCleanup`) · `messaging/manager.go` (`NewMessagingManager`, `StartCleanup`) ·
  `internal/resourcepool/cleanup_warning.go` (`WarnIfCleanupIntervalTooLate`) ·
  `app/lifecycle.go` (the retired `startMaintenanceLoops` / `shutdownManagers`)

---
```

- [ ] **Step 2: Update the E60 gist and the ladder row**

In the `## E60 · v0.59.0 → v0.60.0 — readiness speaks one status vocabulary` section, append to the end of the `- gist:` paragraph (after the sentence ending `…with no change to any emitted JSON (C60.4).`):

```markdown
  Idle-cleanup maintenance also moves manager-side: `database.DbManager` and
  `messaging.Manager` start their idle sweep at construction and stop it in
  `Close()`, as the cache manager already did, so five app-level cleanup-loop
  log lines retire and the cleanup-interval advisory fires earlier; sweep
  frequency, shutdown order and every emitted body are unchanged (C60.5).
```

In the ladder table row for `E60` (the single long line beginning `| E60 | v0.59.0 → v0.60.0 |`), make three edits:

1. In the *worst risk* cell, append to the end of the cell text: ` + silent-behavior (C60.5 — idle-cleanup sweeps start at manager construction; five startup/shutdown log lines retire)`.
2. Change the *atoms* cell from `2` to `3`.
3. In the *preflight* cell, append before the closing `|`: ` And grep log-based alerts and saved queries for the five retired cleanup-loop lines (\`Starting/Stopping database manager cleanup loop\`, \`Starting/Stopping messaging manager cleanup loop\`, \`Manager cleanup loops stopped\`) — they have no renamed equivalent (C60.5).`

(The *compiler-caught* cell stays `C60.4`: C60.5 breaks no build.)

- [ ] **Step 3: Point ADR-067's "Watch" bullet at the atom it promised**

In `wiki/adr_067_lifecycle_slots.md`, replace the final bullet:

```markdown
- **Watch:** `StartCleanup` becoming idempotent (PR4) means a caller that starts it twice
  no longer leaks a goroutine, but a caller that relied on a *second* call changing the
  interval must call `StopCleanup` first. That lands with PR4's own atom, not this one.
```

with:

```markdown
- **Watch:** `StartCleanup` becoming idempotent (PR4) means a caller that starts it twice
  no longer leaks a goroutine, but a caller that relied on a *second* call changing the
  interval must call `StopCleanup` first. PR4 ships that, plus the construction-time start
  and the five retired cleanup-loop log lines, as [C60.5](migrations.md) — not this ADR's atom.
```

- [ ] **Step 4: Note the retired phase in ADR-029 and its index entry**

In `wiki/adr_029_graceful_shutdown_order.md`, insert directly under the `**Status:** Accepted` / `**Date:** 2026-06-10` header block:

```markdown
> **Superseded in part by [ADR-067](adr_067_lifecycle_slots.md) (2026-08-17):** phase 5's separate
> "manager cleanup loops" step no longer exists. `DbManager` and `messaging.Manager` start idle
> cleanup at construction and stop it inside their own `Close()`, which the closers run — so the
> ordering this ADR established is preserved, with one fewer phase to keep in sync.
```

In `wiki/architecture_decisions.md`, in the ADR-029 index entry, append one sentence to the paragraph that ends `…without closing connections.`:

```markdown
Superseded in part by [ADR-067](adr_067_lifecycle_slots.md): the manager-cleanup-loop phase is
gone — each manager stops its own sweep in `Close()`, which the closers still run last.
```

Do **not** touch the `ADR-001 through ADR-067` counter: this PR adds no new ADR.

- [ ] **Step 5: Add the two wiki sentences**

In `wiki/database.md`, append to the paragraph that begins `Each key also binds from the environment (\`DATABASE_MANAGER_MAXSIZE\`…`:

```markdown
The sweep starts when the manager is constructed — before the first request — and stops in `DbManager.Close()`; calling `StartCleanup` yourself is not required, and a second call while a loop is already running is a no-op.
```

In `wiki/messaging.md`, append to the paragraph that begins `Idle-TTL eviction is sweep-driven:`:

```markdown
The sweep starts when the manager is constructed and stops in `Manager.Close()`; calling `StartCleanup` yourself is not required, and a second call while a loop is already running is a no-op.
```

- [ ] **Step 6: Verify the docs**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
git grep -n 'C60.5' -- wiki/ | sort
git grep -c 'Superseded in part by \[ADR-067\]' -- wiki/adr_029_graceful_shutdown_order.md wiki/architecture_decisions.md
git grep -n '| E60 |' -- wiki/migrations.md | grep -c ' | 3 | C60.4 |'
```

Expected: the first prints at least four lines — the ladder row, the E60 gist, the atom heading, and ADR-067's Watch bullet; the second prints `1` for the ADR file and `1` for the index (the index sentence names ADR-067, so adjust the pattern if you worded it differently — the requirement is one occurrence in each file); the third prints `1`. Do not run `markdownlint-cli2` with hand-written globs; the controller's `make check` owns `lint-md`.

- [ ] **Step 7: Commit**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
SCRATCH=/private/tmp/claude-501/-Users-gaborage-Projects-gaborage-code-go-bricks/76c1e566-1d9f-4523-9a92-958252ff2da3/scratchpad
cat > "$SCRATCH/msg5.txt" <<'EOF'
docs(migrations): atom C60.5 for construction-time idle cleanup

The behavior change is invisible to the compiler and to every emitted body, so
it needs an always-gated silent-behavior atom: the sweep starts a few
milliseconds earlier, the interval-vs-idle-TTL advisory moves up the startup
log, five app-level cleanup-loop log lines retire with no renamed equivalent,
and DbManagerOptions/ManagerOptions gain an additive CleanupInterval.

ADR-029 gains a superseded-in-part note (its phase 5 is folded into the
closers, order unchanged) in both the ADR and the index, and ADR-067's Watch
bullet now names the atom it promised. wiki/database.md and wiki/messaging.md
each gain the one sentence an operator needs; wiki/cache.md is untouched
because the cache manager is the precedent, not the change.

Refs: ADR-067
EOF
git add wiki/migrations.md wiki/adr_067_lifecycle_slots.md wiki/adr_029_graceful_shutdown_order.md wiki/architecture_decisions.md wiki/database.md wiki/messaging.md
git commit -F "$SCRATCH/msg5.txt"
git log -1 --pretty='%h %G? %s'
```

---

### Task 6: controller gates and handoff

**Files:** none — this task is the controller's, not an implementer's. An implementer who reaches it stops and reports.

**Interfaces:**

- Consumes: the five commits from Tasks 1–5.
- Produces: a pushed branch and an open PR (controller-side only).

- [ ] **Step 1: Confirm the tree is committed and the branch is right**

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks
git status --porcelain
git rev-parse --abbrev-ref HEAD
git log --oneline origin/main..HEAD
git log --pretty='%h %G? %s' origin/main..HEAD
```

Expected: `git status --porcelain` prints nothing (uncommitted work makes `make mutate` report `no mutatable changes` and exit 0 — a false pass); the branch is the PR4 branch, never `main`; five commits; no `%G?` column reads `N`.

- [ ] **Step 2: Machine gate**

Run in the **background** (`run_in_background`), never foreground — `make check` and especially `make mutate` (~440s median) exceed the 600s foreground ceiling:

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks && pwd && make check
```

Expected: green. `make check` runs fmt, so afterwards confirm the tree is still clean with `git status --porcelain` (a `gofmt`/`gci` repair leaves changes that `gofmt -l` alone does not report); commit any repair before continuing.

- [ ] **Step 3: Pre-push agent gates, in order**

`/simplify` → `make check` if it changed code → `/security-audit` → `make check` if it changed code → `/code-review` (CodeRabbit) last, so it sees the final diff. Re-run `/code-review` if findings are applied after its pass.

Points worth flagging to the reviewers, since they are the judgment calls in this diff:

- The self-start is **unconditional** in both constructors (no `if opts.IdleTTL > 0` guard) because both constructors coerce a non-positive `IdleTTL` to a positive default first; the "no TTL → no loop" guard lives in `Pool.StartCleanup` and is already tested there. A reviewer asking for the guard is asking for a permanently-true branch.
- `Close()` needed no production change — `pool.Close()` has always called `StopCleanup()` and joined the loop.
- `internal/resourcepool` now imports `logger`; `logger` imports no go-bricks package, so there is no cycle.

- [ ] **Step 4: Mutation gate**

Background, after every code-changing gate has settled:

```bash
cd /Users/gaborage/Projects/gaborage/code/go-bricks && pwd && make mutate
```

Expected: the summary reports `(N mutants on changed lines)` with **N > 0** and no survivors. A run with no summary line was terminated, not passed — re-run it. `no mutatable changes` with exit 0 means something is uncommitted; go back to Step 1.

Likely mutant sites to watch: `cleanupIntervalTooLate`'s `idleTTL <= 0` and `cleanupInterval >= idleTTL` (covered by the `zero_idle_ttl_skipped` / `cleanup_equals_idle_warns` table rows), and each constructor's `opts.CleanupInterval <= 0` default (covered by the `unset_interval_takes_the_default_and_stays_silent` row plus the construction-starts-cleanup tests).

- [ ] **Step 5: Push and open the PR**

Push the branch and open the PR against its stack predecessor (PR3's branch), not `main` — this is Stack A PR4. Use `/gh-stack` to sync. Remember CodeRabbit **skips** stacked PRs whose base is not `main`: post `@coderabbitai review` on the PR after opening it, and again after any restack push. PR body: three headings only (`## What`, `## Impact`, `## Verification`), each ≤3 sentences, whole body under 150 words. `## Impact` must carry the `CleanupInterval` option and the retired log lines, because those are what a consumer does differently.

---

## Self-Review

**1. Spec coverage.** ADR-067 decision 4 has five clauses, each mapped: "self-start idle cleanup at construction when `IdleTTL > 0`" → Tasks 2/3 Step 3 (with the verified-fact note that `IdleTTL` is always positive there, so the guard would be dead code and the pool's own guard covers the case); "stop it in `Close()`" → verified as already true, pinned by `TestNewDbManagerClosesCleanlyWithALiveCleanupLoop` / its messaging twin and documented in both `Close` doc comments; "`StartCleanup` stays exported and becomes idempotent" → Task 1 Steps 1–2 (already idempotent, pinned by a mutation-verified channel-identity test) and the manager-level idempotency tests adjusted in Tasks 2/3; "`StopCleanup` stays" → untouched, still exported, still tested; "the WARN moves beside the pool that owns both values" → Task 1's `internal/resourcepool/cleanup_warning.go`. Decision 2's "There is no maintenance phase" → Task 4. The atom line the brief demanded (`[C60.5]` silent-behavior, plus the E60 row/gist gain) → Task 5 Steps 1–2, including the direct-construction note the brief asked for (it is the second half of the atom's `apply`). The doc grep the brief asked for was run: `cleanupinterval|CleanupInterval|StartCleanup` across `wiki/*.md` and `llms.txt` yields edits only in `wiki/database.md` and `wiki/messaging.md`; `wiki/cache.md`, `llms.txt` and `wiki/startup_defaults.md` carry no start-timing claim and are deliberately left alone, which the File Structure table states.

**2. Placeholder scan.** No `TBD`, no "similar to Task N", no "add appropriate error handling". Every code step carries the literal Go or Markdown to write; the two structural edits that are easier described than pasted (deleting three consecutive functions in `app/lifecycle.go`; the three cell edits in one 2,000-character Markdown table row) name their exact anchors and show the resulting text.

**3. Type consistency.** `WarnIfCleanupIntervalTooLate(log logger.Logger, keyPrefix string, cleanupInterval, idleTTL time.Duration)` is declared once in Task 1 Step 5 and called with exactly that signature in Task 2 Step 3 (`"database.manager"`) and Task 3 Step 3 (`"messaging.publisher"`). `CleanupInterval` is spelled identically in `DbManagerOptions`, `ManagerOptions`, `config.DatabaseManagerConfig`, `config.PublisherPoolConfig` and `cache.ManagerConfig`. `defaultCleanupInterval` (database, 5m) and `defaultPublisherCleanupInterval` (messaging, 2m) are distinct names in distinct packages, each used by both its constructor and its `StartCleanup`, and neither collides with an existing identifier (verified by grep). The two test doubles share the name `warnRecorder` but live in different packages (`resourcepool` and `database`) and never meet; the messaging tests reuse the package's existing `stubLogger` rather than adding a third.

## Execution Handoff

Plan complete and saved. Two execution options:

**1. Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints.

Tasks 1–5 are strictly ordered: Task 4's deletion is only behavior-preserving after Tasks 2 and 3 land.
