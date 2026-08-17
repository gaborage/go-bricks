# Readiness: one body rule, one gate, one debug view — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish the readiness module (ADR-066) by deriving both readiness views — `/ready`'s verdict and body, and the access-controlled debug detail — from one probe run, one gate predicate and one per-kind body rule, deleting the four hand-written render sites they replace.

**Architecture:** PR1a made every kind hand `app/readiness.go` a *probe description* that one machine judges. This slice adds the render half: a description gains a `publicStats` allowlist (the only statistics keys its kind may publish on the unauthenticated `/ready` body); a new `app/readiness_render.go` collects probe outcomes into a `readinessReport` and renders all three outputs from it — the 200 body (`<name>` + `<name>_stats` per kind), the 503 body (ADR-048 sanitized), and the debug components + summary. The report has two entry points over one shared per-probe step: `runUntilBlocking` is `/ready`'s, judging in registration order and **stopping at the first failing critical result**; `runReadinessProbes` is the debug view's, which reports every kind and therefore runs every probe. `readyCheck` and `handleHealthDebug` shrink to transport wrappers. `App.cacheAbsent` deletes: the absence verdict is computed where `Options` lives (the Builder) and handed to `createHealthProbes`.

**Tech Stack:** Go 1.26, testify (`assert`/`require`), `golangci-lint` v2 via `make check`, zerolog. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-16-app-readiness-and-lifecycle-slots-design.md` — "Readiness" decisions 2 (publicStats allowlist), 4 (one gate), 5 (one body rule), 6 (one debug view), 7 (criticality once), 9 (tests replace, don't layer). Vocabulary: `CONTEXT.md` ("Readiness", "Probe description"). ADR: `wiki/adr_066_readiness_one_module.md` (already written — rules 2 and 3 are this PR). Atom: `[C60.3]` in `wiki/migrations.md` (already written — this PR **extends** it).

## Global Constraints

Copied verbatim from the controller's brief and CLAUDE.md. Every task's requirements implicitly include this section.

- **Branch:** work on `feature/app-readiness-probe-descriptions`. Never switch branches. Never push to `main`.
- **Test names:** camelCase for test function names (`TestReadinessViews`, never `Test_Readiness_Views`). Table-driven case names use snake_case (`{name: "critical_kind_gates"}`).
- **Commits:** `git commit -F <file>` with a message file — the commit hook blocks heredoc `-m`. **Never** pass `--no-gpg-sign`; if signing fails because 1Password is locked, stop and report it.
- **Implementers do not run `make check`, `make mutate`, or push.** The controller runs the gates (Task 6). Implementers run only the targeted `go test` commands named in their task, plus `gofmt -w` on the files they touched (the code blocks below are written for readability, not for gofmt's alignment).
- **ADR-048 holds:** `HealthStatus.Err` never renders on `/ready` — neither body, neither status code. Only `publicProbeError` picks the 503 text.
- **No exported API change.** `Prober`, `HealthStatus`, `ComponentHealth`, `HealthSummary`, `HealthDebugInfo`, `Info`, `Builder.CreateHealthProbes`, `Builder.RegisterClosers` keep their names, fields and signatures. Everything added here is unexported.
- **`/ready` status codes are unchanged for every deployment.** `healthy`, `not_configured`, `disabled`, `per_tenant` answer 200; only a *critical* kind whose status is `unhealthy` answers 503.
- **readyCheck short-circuits at the first failing critical probe exactly as before; the debug view runs every probe.**
- **`Prober` / `HealthStatus` are untouched** — no new fields, no new methods.
- **No `//nolint`.** If a linter fires, fix the code (see the goconst note in Task 1).

## File Structure

| File | Change | Responsibility after this PR |
| --- | --- | --- |
| `app/readiness.go` | modify (~235 → ~300 lines) | What a kind *is*: the probe description, the judge, the four per-kind constructors, and each kind's public-stats allowlist. |
| `app/readiness_render.go` | **create** (~200 lines) | How the two views are *produced*: run the probes once, the shared gate predicate, the 200/503 bodies, the sanitized error text, the debug components and summary. |
| `app/readiness_test.go` | modify | Description/judge tests (unchanged from PR1a) + allowlist content tests. |
| `app/readiness_render_test.go` | **create** | The one table over synthetic descriptions: statuses × `/ready` code+body × debug components+summary. |
| `app/lifecycle.go` | modify | `readyCheck` shrinks to transport; `publicProbeError`, `publicDBStats`, `publicStreamsStats`, `dbConnectionsKey`, `streamsOffsetsKey` leave. |
| `app/health.go` | modify (45 → ~28 lines) | `HealthStatus` + `Prober` only. `getStatsOrEmpty` and `componentReport` delete. |
| `app/debug_health.go` | modify (190 → ~105 lines) | The JSON types + `handleHealthDebug` + `getAppInfo`. `calculateHealthSummary` and `addManagerHealth` delete. |
| `app/app.go` | modify | `App.cacheAbsent` deletes; `createHealthProbes` takes `probeInputs`. |
| `app/app_builder.go` | modify | `CreateHealthProbes` computes the absence verdict; `preInitCache` calls `rootCacheAbsent` directly. |
| `app/lifecycle_test.go`, `app/app_test.go`, `app/debug_health_test.go`, `app/health_test.go`, `app/app_builder_test.go` | modify | Delete the per-writer tests the table replaces; retarget the end-to-end pins. |
| `wiki/migrations.md`, `wiki/cache.md`, `wiki/messaging.md`, `llms.txt` | modify | Atom `[C60.3]` extension, E60 hop row + gist, `db_stats` → `database_stats`, `messaging_manager` → `messaging`. |

**Two decisions worth stating up front, because they deviate slightly from the brief's sketch:**

1. **Split into two files.** `app/readiness.go` would land at ~465 lines if the render lived there. The brief allows a sibling; `app/readiness_render.go` takes the run + gate + three renders. The seam is "what a kind is" vs "how the views are produced" — the allowlists stay in `readiness.go` beside the constructors that reference them, because an allowlist is part of a kind's description.
2. **`isFailing` takes a `string`, and `probeResult` carries its own timing.** The brief sketched `isFailing(HealthStatus) bool` and `debugComponents(results, durations)`. A string parameter is what makes the predicate genuinely *shared* — `healthSummary` walks `ComponentHealth`, which has no `HealthStatus` — and folding the duration into the per-probe result removes the parallel-slice invariant a separate `durations` argument would create. Same behavior, one fewer thing to keep in sync.

**What "one gate" does NOT mean here:** the gate predicate is shared between the two views, but the *traversal* is not. `readyCheck` keeps today's short-circuit — probes are judged in registration order and evaluation stops at the first failing critical result, which is the 503; no later probe runs. That is why the run is a function (`runUntilBlocking`) rather than a predicate over a completed report: an outage must not add a Redis `PING` or a publisher lease to every `/ready` poll. Only the debug view, which reports every kind, runs every probe (`runReadinessProbes`). Both share one per-probe step (`runProbe`), so the result shape and the allowlist lookup cannot diverge.

---

## Task 1: The allowlist, the run, the gate, and the two `/ready` bodies

**Files:**

- Modify: `app/readiness.go` (add the `publicStats` field, the shared counter-name constants, the four allowlists; wire each constructor)
- Create: `app/readiness_render.go`
- Modify: `app/lifecycle.go:538-555` (move `publicProbeError` out — the function and its whole comment)
- Create: `app/readiness_render_test.go`
- Modify: `app/readiness_test.go` (add the allowlist-content test; add the two withheld-key test constants)
- Modify: `app/lifecycle_test.go:842-867` (move `TestPublicProbeError` to `app/readiness_render_test.go` verbatim — it now tests a function in that file)

**Interfaces:**

- Consumes (from PR1a, already on the branch): `probeDescription` (fields `name`, `critical`, `disabled`, `absent`, `perTenant`, `acquire`, `live`, `stats`), `probeDescription.Run`, `disabledProbe(name)`, `databaseProbe`, `messagingProbe`, `cacheProbe`, `streamsProbe`, `convertCacheStatsToMap`, `HealthStatus`, `Prober`, and the status constants in `app/app.go:23-42`.
- Produces (Tasks 2–4 rely on these exact names and signatures):
  - `type probeResult struct { status HealthStatus; publicStats []string; startedAt time.Time; duration time.Duration }`
  - `type readinessReport []probeResult`
  - `func runProbe(ctx context.Context, probe Prober) probeResult`
  - `func runReadinessProbes(ctx context.Context, probes []Prober) readinessReport` (the debug view's run: every probe)
  - `func runUntilBlocking(ctx context.Context, probes []Prober) (report readinessReport, blocking HealthStatus, found bool)` (`/ready`'s run: stops at the first failing critical probe)
  - `func isFailing(status string) bool`
  - `func isReadyEquivalent(status string) bool`
  - `func (r readinessReport) readyBody(app *config.AppConfig, now time.Time) map[string]any`
  - `func notReadyBody(result *HealthStatus) map[string]any`
  - `func publicProjection(details map[string]any, allow []string) map[string]any`
  - `func publicProbeError(result *HealthStatus) string` (moved, unchanged)
  - `const statsSuffix = "_stats"`
  - `var databasePublicStats, messagingPublicStats, cachePublicStats, streamsPublicStats []string`

### Step 1: Write the failing allowlist test

Append to `app/readiness_test.go` (after `TestConvertCacheStatsToMap`, before the `// Fixtures used only by the per-kind descriptions above.` comment block):

```go
// The two manager keys the allowlists deliberately withhold, spelled out rather than
// imported so the assertions pin the wire format instead of restating a production value.
const (
	connectionsStatsKey   = "connections"
	storedOffsetsStatsKey = "stored_offsets"
)

// TestPublicStatsAllowlistsMatchManagerCounters pins every allowlist against the keys the
// real manager publishes. SECURITY: an allowlist that silently falls behind a manager is
// how a new identifier-bearing counter reaches the unauthenticated /ready body — this test
// fails the day a manager gains a key, forcing the "publish or withhold" decision.
func TestPublicStatsAllowlistsMatchManagerCounters(t *testing.T) {
	tests := []struct {
		name     string
		stats    map[string]any
		allow    []string
		withheld []string
	}{
		{
			name:     "database",
			stats:    (&database.DbManager{}).Stats(),
			allow:    databasePublicStats,
			withheld: []string{connectionsStatsKey},
		},
		{
			name:  "messaging",
			stats: (&messaging.Manager{}).Stats(),
			allow: messagingPublicStats,
		},
		{
			name:  "cache",
			stats: convertCacheStatsToMap(cache.ManagerStats{}),
			allow: cachePublicStats,
		},
		{
			name:     "streams",
			stats:    (&streams.Manager{}).Stats(),
			allow:    streamsPublicStats,
			withheld: []string{storedOffsetsStatsKey},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			published := make([]string, 0, len(tt.stats))
			for key := range tt.stats {
				published = append(published, key)
			}
			assert.ElementsMatch(t, published, append(append([]string{}, tt.allow...), tt.withheld...),
				"every manager counter is either allowlisted or listed as deliberately withheld")
			for _, key := range tt.withheld {
				assert.NotContains(t, tt.allow, key)
			}
		})
	}
}

// TestPublicProjectionKeepsOnlyAllowlistedKeys pins both halves of the render-site filter:
// the /ready view carries the allowlisted counters plus the mirrored status and nothing
// else, and the map it was built from is untouched — the access-controlled debug view
// renders that same map and operators need the withheld keys there.
func TestPublicProjectionKeepsOnlyAllowlistedKeys(t *testing.T) {
	details := map[string]any{
		"active_connections": 2,
		"max_connections":    25,
		"idle_ttl_seconds":   3600,
		"errors":             0,
		statusKey:            healthyStatus,
		connectionsStatsKey: []map[string]any{
			{"key": "tenant-alpha", "last_used": "2026-08-05T10:00:00Z", "idle_duration": 4},
		},
	}

	public := publicProjection(details, databasePublicStats)

	assert.Equal(t, map[string]any{
		"active_connections": 2,
		"max_connections":    25,
		"idle_ttl_seconds":   3600,
		"errors":             0,
		statusKey:            healthyStatus,
	}, public, "every allowlisted counter survives; the keyed array is withheld")
	assert.Contains(t, details, connectionsStatsKey,
		"filtering in place would strip the array from the debug endpoint's view too")
}

// TestPublicProjectionWithoutAnAllowlist covers the two shapes with no counters to publish:
// a disabled kind, whose only detail is its own status, and a Prober from outside the
// framework, which declares no allowlist and therefore publishes its status alone.
func TestPublicProjectionWithoutAnAllowlist(t *testing.T) {
	assert.Equal(t, map[string]any{statusKey: disabledStatus},
		publicProjection(map[string]any{statusKey: disabledStatus}, nil))

	assert.Equal(t, map[string]any{statusKey: healthyStatus},
		publicProjection(map[string]any{statusKey: healthyStatus, "vault_addr": "10.0.0.9:8200"}, nil))

	assert.Equal(t, map[string]any{}, publicProjection(nil, databasePublicStats),
		"a nil details map must still render {} — never JSON null")
}
```

### Step 2: Run the tests to verify they fail

Run: `go test ./app/ -run 'TestPublicStatsAllowlistsMatchManagerCounters|TestPublicProjection' -count=1`

Expected: FAIL — the package does not compile, with `undefined: databasePublicStats`, `undefined: messagingPublicStats`, `undefined: cachePublicStats`, `undefined: streamsPublicStats`, `undefined: publicProjection`.

### Step 3: Add the allowlists to `app/readiness.go`

Add the `publicStats` field to `probeDescription`, immediately after the `stats` field (`app/readiness.go:59`):

```go
	// publicStats allowlists the statistics keys this kind may publish on the
	// unauthenticated /ready body; every other key stays on the access-controlled debug
	// view. nil means "status only".
	publicStats []string
```

Insert the constants and allowlists after `cacheProbePingTimeout` (`app/readiness.go:129`), before `databaseProbe`:

```go
// Counter names more than one kind's allowlist or snapshot spells, hoisted so the spelling
// cannot drift between a manager's snapshot and the allowlist that admits it.
const (
	statsErrorsKey       = "errors"
	statsEvictionsKey    = "evictions"
	statsIdleCleanupsKey = "idle_cleanups"
)

// The per-kind public-stats allowlists: the only statistics keys that may reach the
// unauthenticated /ready 200 body. An allowlist and not a denylist, so a counter added to a
// manager tomorrow stays off that body until someone reviews it.
//
// SECURITY: two manager keys are deliberately absent. DbManager.Stats()["connections"] holds
// one entry per live pooled connection, and each entry's "key" is the resourcepool key — the
// tenant ID in a multi-tenant deployment, the named-database key otherwise — alongside
// last_used and idle_duration, so polling /ready enumerated which tenants were active and
// when each was last served. streams.Manager.Stats()["stored_offsets"] is keyed
// "<stream>/<consumer>" — declared topology that usually names the domain — with live offsets
// as values, so differencing two polls yields the per-stream message rate. /ready carries no
// authentication and no IP allowlist, and its throttles are two IP-keyed rate limits
// (app.rate.limit, koanf default 100 rps; app.rate.ippreguard.threshold, koanf default
// 2000 rps/IP) that a Go-assembled config leaves at zero entirely (ADR-049) — no barrier to
// enumeration either way.
//
// The filtering belongs here at the render seam rather than in the managers or the probes:
// the access-controlled <debug.pathprefix>/health-debug renders the same details map
// unredacted, and operators need both withheld keys there.
var (
	databasePublicStats = []string{
		"active_connections", "max_connections", "idle_ttl_seconds", statsErrorsKey,
	}
	messagingPublicStats = []string{
		"active_publishers", "max_publishers", "active_consumers", "idle_ttl_seconds",
		statsEvictionsKey, statsIdleCleanupsKey, statsErrorsKey,
	}
	cachePublicStats = []string{
		"active_caches", "total_created", statsEvictionsKey, statsIdleCleanupsKey,
		statsErrorsKey, "max_size", "idle_ttl",
	}
	streamsPublicStats = []string{
		"started", "consumers", "publishers", "ready",
		"offset_store_count", "offset_flush_interval",
	}
)
```

Set the allowlist on each constructor — one line in each returned literal:

- `databaseProbe` (`app/readiness.go:138-150`): add `publicStats: databasePublicStats,` after `perTenant: perTenant,`
- `messagingProbe` (`app/readiness.go:159-175`): add `publicStats: messagingPublicStats,`
- `cacheProbe` (`app/readiness.go:185-202`): add `publicStats: cachePublicStats,`
- `streamsProbe` (`app/readiness.go:212-221`): add `publicStats: streamsPublicStats,` after `name: componentStreams,`

`disabledProbe` deliberately gets none: a disabled kind's only detail is its own status, which `publicProjection` always carries.

Rewrite `convertCacheStatsToMap` (`app/readiness.go:225-235`) to use the shared constants:

```go
// convertCacheStatsToMap renders cache.ManagerStats as the counters map every kind reports.
func convertCacheStatsToMap(stats cache.ManagerStats) map[string]any {
	return map[string]any{
		"active_caches":      stats.ActiveCaches,
		"total_created":      stats.TotalCreated,
		statsEvictionsKey:    stats.Evictions,
		statsIdleCleanupsKey: stats.IdleCleanups,
		statsErrorsKey:       stats.Errors,
		"max_size":           stats.MaxSize,
		"idle_ttl":           stats.IdleTTL,
	}
}
```

> **goconst note.** `goconst` (min-len 4, min-occurrences 3) counts string literals inside composite literals — verified against this repo's config. The three constants above exist because `errors` would otherwise appear four times and `evictions`/`idle_cleanups` three times each. If `make check` later reports another repeated key, hoist it into the same `const` block. Never add a `//nolint`.

### Step 4: Create `app/readiness_render.go` with `publicProjection`

```go
package app

import (
	"context"
	"time"

	"github.com/gaborage/go-bricks/config"
)

// The two readiness views — /ready's verdict and body, and the access-controlled debug
// detail — are produced here from one probe run and one predicate, so they cannot disagree
// (ADR-066, rules 2 and 3).

// statsSuffix turns a component name into its statistics key on the /ready 200 body.
const statsSuffix = "_stats"

// publicProjection copies the allowlisted counters, plus the mirrored status, out of a
// kind's details. It copies rather than filtering in place because the debug view renders
// that same map unredacted.
func publicProjection(details map[string]any, allow []string) map[string]any {
	public := make(map[string]any, len(allow)+1)
	for _, key := range allow {
		if value, ok := details[key]; ok {
			public[key] = value
		}
	}
	if status, ok := details[statusKey]; ok {
		public[statusKey] = status
	}
	return public
}
```

### Step 5: Run the tests to verify they pass

Run: `go test ./app/ -run 'TestPublicStatsAllowlistsMatchManagerCounters|TestPublicProjection' -count=1`

Expected: `ok  github.com/gaborage/go-bricks/app` — all four `TestPublicStatsAllowlistsMatchManagerCounters` subtests and both projection tests pass.

### Step 6: Write the failing run/gate/body test

Create `app/readiness_render_test.go`:

```go
package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

// describe builds a lease-less description whose liveness result and statistics are fixed,
// so a row can name the shape both views must render without standing up a manager.
func describe(name string, critical bool, liveErr error, stats map[string]any, allow []string) probeDescription {
	return probeDescription{
		name:        name,
		critical:    critical,
		publicStats: allow,
		live:        func(context.Context) error { return liveErr },
		stats:       func() map[string]any { return stats },
	}
}

// describeNotConfigured builds a description whose lease reports "not configured" — the
// shape a database-free deployment produces, and with perTenant the shape a multi-tenant
// one produces for the fixed "" key.
func describeNotConfigured(name string, critical, perTenant bool, stats map[string]any, allow []string) probeDescription {
	return probeDescription{
		name:        name,
		critical:    critical,
		perTenant:   perTenant,
		publicStats: allow,
		acquire: func(context.Context) (func(context.Context) error, func(), error) {
			return nil, nil, config.NewNotConfiguredError(name, "HOST", name+".host")
		},
		stats: func() map[string]any { return stats },
	}
}

// foreignProbe is a Prober from outside the framework: it declares no allowlist, so the
// unauthenticated body may carry its status and nothing else.
type foreignProbe struct {
	result HealthStatus
}

func (p foreignProbe) Run(context.Context) HealthStatus { return p.result }

// mustNotRunProbe fails the test if it is ever invoked — the standing proof that /ready
// stops at the first failing critical probe rather than paying for the rest.
type mustNotRunProbe struct {
	t *testing.T
}

func (p mustNotRunProbe) Run(context.Context) HealthStatus {
	p.t.Error("no probe after the first failing critical one may run")
	return HealthStatus{Name: "never"}
}

// wantComponent is a debug entry with the two varying fields (LastRun, Duration) left out.
type wantComponent struct {
	status   string
	critical bool
	errText  string
	details  map[string]any
}

// TestReadinessViews is the module's one table: each row is a probe set, and every row
// asserts the four things the two views must agree on — the /ready status code, how far
// /ready's run got, the exact /ready body, and the debug components plus summary.
// Asserting the whole body map (rather than key-by-key) is what pins the one-body rule: a
// kind that stops rendering, or a stats key that starts, fails the row. wantReadyRuns is
// the other half: /ready stops at the blocking probe, while the debug view runs them all.
func TestReadinessViews(t *testing.T) {
	const (
		fixedUnix   = int64(1755300000)
		streamKey   = "payments-ledger/fraud-scoring"
		redisAddr   = "10.0.0.9:6379"
		driverError = "failed to connect to `user=app database=payments`: 10.0.0.5:5432"
	)
	now := time.Unix(fixedUnix, 0)
	appCfg := &config.AppConfig{Name: "svc", Env: "test", Version: "1.0.0"}
	appBody := map[string]any{"name": "svc", "environment": "test", "version": "1.0.0"}

	dbStats := map[string]any{
		"active_connections": 2,
		"max_connections":    25,
		"idle_ttl_seconds":   3600,
		"errors":             0,
		"connections":        []map[string]any{{"key": "tenant-alpha"}},
	}
	dbPublic := map[string]any{
		"active_connections": 2, "max_connections": 25, "idle_ttl_seconds": 3600, "errors": 0,
	}
	streamStats := map[string]any{
		"started": true, "consumers": 1, "publishers": 1, "ready": true,
		"stored_offsets":        map[string]int64{streamKey: 4242},
		"offset_store_count":    500,
		"offset_flush_interval": "5s",
	}
	streamPublic := map[string]any{
		"started": true, "consumers": 1, "publishers": 1, "ready": true,
		"offset_store_count": 500, "offset_flush_interval": "5s",
	}
	cacheStats := map[string]any{"active_caches": 3, "errors": 0}

	// withStatus returns stats plus the status key the judge stamps on details.
	withStatus := func(stats map[string]any, status string) map[string]any {
		out := map[string]any{statusKey: status}
		for k, v := range stats {
			out[k] = v
		}
		return out
	}

	tests := []struct {
		name           string
		probes         []Prober
		wantCode       int
		wantReadyRuns  int // probes /ready evaluates before it answers
		wantBody       map[string]any
		forbidden      []string
		wantComponents map[string]wantComponent
		wantSummary    HealthSummary
	}{
		{
			name: "every_registered_kind_renders_status_and_stats",
			probes: []Prober{
				describe(componentDatabase, true, nil, dbStats, databasePublicStats),
				disabledProbe(componentMessaging),
				describeNotConfigured(componentCache, true, false, cacheStats, cachePublicStats),
				describe(componentStreams, false, nil, streamStats, streamsPublicStats),
			},
			wantCode: 200,
			wantReadyRuns: 4,
			wantBody: map[string]any{
				statusKey:                            readyStatus,
				"time":                               fixedUnix,
				"app":                                appBody,
				componentDatabase:                    healthyStatus,
				componentDatabase + statsSuffix:      withStatus(dbPublic, healthyStatus),
				componentMessaging:                   disabledStatus,
				componentMessaging + statsSuffix:     map[string]any{statusKey: disabledStatus},
				componentCache:                       notConfiguredStatus,
				componentCache + statsSuffix:         withStatus(cacheStats, notConfiguredStatus),
				componentStreams:                     healthyStatus,
				componentStreams + statsSuffix:       withStatus(streamPublic, healthyStatus),
			},
			forbidden: []string{"tenant-alpha", "connections", streamKey, "stored_offsets", "4242"},
			wantComponents: map[string]wantComponent{
				componentDatabase:  {status: healthyStatus, critical: true, details: withStatus(dbStats, healthyStatus)},
				componentMessaging: {status: disabledStatus, details: map[string]any{statusKey: disabledStatus}},
				componentCache:     {status: notConfiguredStatus, critical: true, details: withStatus(cacheStats, notConfiguredStatus)},
				componentStreams:   {status: healthyStatus, details: withStatus(streamStats, healthyStatus)},
			},
			wantSummary: HealthSummary{OverallStatus: healthyStatus, TotalProbes: 4, HealthyCount: 4},
		},
		{
			name: "first_failing_critical_kind_gates_and_sanitizes",
			probes: []Prober{
				describe(componentDatabase, true, errors.New(driverError), dbStats, databasePublicStats),
				describe(componentCache, true, errors.New(redisAddr+": connection refused"), cacheStats, cachePublicStats),
			},
			wantCode: 503,
			wantReadyRuns: 1,
			wantBody: map[string]any{
				statusKey:         "not ready",
				componentDatabase: unhealthyStatus,
				errorKey:          "database unavailable",
			},
			forbidden: []string{driverError, "user=app", "10.0.0.5", redisAddr, "active_connections"},
			wantComponents: map[string]wantComponent{
				componentDatabase: {status: unhealthyStatus, critical: true, errText: driverError, details: withStatus(dbStats, unhealthyStatus)},
				componentCache:    {status: unhealthyStatus, critical: true, errText: redisAddr + ": connection refused", details: withStatus(cacheStats, unhealthyStatus)},
			},
			wantSummary: HealthSummary{OverallStatus: "critical", TotalProbes: 2, CriticalCount: 2, ErrorCount: 2},
		},
		{
			name: "non_critical_failure_stays_ready_and_reads_degraded",
			probes: []Prober{
				describe(componentDatabase, true, nil, dbStats, databasePublicStats),
				describe(componentStreams, false, errStreamsNotOpen, streamStats, streamsPublicStats),
			},
			wantCode: 200,
			wantReadyRuns: 2,
			wantBody: map[string]any{
				statusKey:                       readyStatus,
				"time":                          fixedUnix,
				"app":                           appBody,
				componentDatabase:               healthyStatus,
				componentDatabase + statsSuffix: withStatus(dbPublic, healthyStatus),
				componentStreams:                unhealthyStatus,
				componentStreams + statsSuffix:  withStatus(streamPublic, unhealthyStatus),
			},
			forbidden: []string{streamKey, "4242", errorKey},
			wantComponents: map[string]wantComponent{
				componentDatabase: {status: healthyStatus, critical: true, details: withStatus(dbStats, healthyStatus)},
				componentStreams:  {status: unhealthyStatus, errText: errStreamsNotOpen.Error(), details: withStatus(streamStats, unhealthyStatus)},
			},
			// The drift decision 4 removes: this used to be `unknown`, because the debug
			// summary gated on a status list while /ready gated on Err && Critical.
			wantSummary: HealthSummary{OverallStatus: degradedStatus, TotalProbes: 2, HealthyCount: 1, ErrorCount: 1},
		},
		{
			name: "absence_is_ready_equivalent_in_both_views",
			probes: []Prober{
				describeNotConfigured(componentDatabase, true, false, dbStats, databasePublicStats),
				describeNotConfigured(componentMessaging, false, true, nil, messagingPublicStats),
				disabledProbe(componentCache),
			},
			wantCode: 200,
			wantReadyRuns: 3,
			wantBody: map[string]any{
				statusKey:                        readyStatus,
				"time":                           fixedUnix,
				"app":                            appBody,
				componentDatabase:                notConfiguredStatus,
				componentDatabase + statsSuffix:  withStatus(dbPublic, notConfiguredStatus),
				componentMessaging:               perTenantStatus,
				componentMessaging + statsSuffix: map[string]any{statusKey: perTenantStatus},
				componentCache:                   disabledStatus,
				componentCache + statsSuffix:     map[string]any{statusKey: disabledStatus},
			},
			forbidden: []string{"tenant-alpha", errorKey},
			wantComponents: map[string]wantComponent{
				componentDatabase:  {status: notConfiguredStatus, critical: true, details: withStatus(dbStats, notConfiguredStatus)},
				componentMessaging: {status: perTenantStatus, details: map[string]any{statusKey: perTenantStatus}},
				componentCache:     {status: disabledStatus, details: map[string]any{statusKey: disabledStatus}},
			},
			wantSummary: HealthSummary{OverallStatus: healthyStatus, TotalProbes: 3, HealthyCount: 3},
		},
		{
			name: "foreign_probe_publishes_only_its_status",
			probes: []Prober{
				foreignProbe{result: HealthStatus{
					Name:    "vault",
					Status:  healthyStatus,
					Details: map[string]any{statusKey: healthyStatus, "addr": "10.0.0.9:8200"},
				}},
			},
			wantCode: 200,
			wantReadyRuns: 1,
			wantBody: map[string]any{
				statusKey:            readyStatus,
				"time":               fixedUnix,
				"app":                appBody,
				"vault":              healthyStatus,
				"vault" + statsSuffix: map[string]any{statusKey: healthyStatus},
			},
			forbidden: []string{"10.0.0.9:8200", "addr"},
			wantComponents: map[string]wantComponent{
				"vault": {status: healthyStatus, details: map[string]any{statusKey: healthyStatus, "addr": "10.0.0.9:8200"}},
			},
			wantSummary: HealthSummary{OverallStatus: healthyStatus, TotalProbes: 1, HealthyCount: 1},
		},
		{
			name: "status_outside_the_vocabulary_reads_unknown",
			probes: []Prober{
				foreignProbe{result: HealthStatus{Name: "vault", Status: "starting", Details: map[string]any{statusKey: "starting"}}},
			},
			wantCode: 200,
			wantReadyRuns: 1,
			wantBody: map[string]any{
				statusKey:            readyStatus,
				"time":               fixedUnix,
				"app":                appBody,
				"vault":              "starting",
				"vault" + statsSuffix: map[string]any{statusKey: "starting"},
			},
			wantComponents: map[string]wantComponent{
				"vault": {status: "starting", details: map[string]any{statusKey: "starting"}},
			},
			wantSummary: HealthSummary{OverallStatus: unknownStatus, TotalProbes: 1},
		},
		{
			name:     "no_probes_renders_the_envelope_alone",
			probes:   []Prober{},
			wantCode: 200,
			wantReadyRuns: 0,
			wantBody: map[string]any{
				statusKey: readyStatus,
				"time":    fixedUnix,
				"app":     appBody,
			},
			wantComponents: map[string]wantComponent{},
			wantSummary:    HealthSummary{OverallStatus: unknownStatus},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// /ready's run: registration order, stopping at the first failing critical kind.
			report, blocking, found := runUntilBlocking(context.Background(), tt.probes)

			var body map[string]any
			if found {
				body = notReadyBody(&blocking)
			} else {
				body = report.readyBody(appCfg, now)
			}

			wantFound := tt.wantCode == 503
			assert.Equal(t, wantFound, found, "the gate decides the status code")
			assert.Len(t, report, tt.wantReadyRuns, "/ready must not evaluate past the blocking probe")
			assert.Equal(t, tt.wantBody, body)

			encoded, err := json.Marshal(body)
			require.NoError(t, err)
			for _, forbidden := range tt.forbidden {
				assert.NotContains(t, string(encoded), forbidden,
					"the unauthenticated body must not carry %q anywhere", forbidden)
			}

			// The debug view's run: every probe, whatever /ready decided.
			full := runReadinessProbes(context.Background(), tt.probes)
			require.Len(t, full, len(tt.probes), "the debug view reports every registered kind")

			components := full.debugComponents()
			require.Len(t, components, len(tt.wantComponents))
			for name, want := range tt.wantComponents {
				got, ok := components[name]
				require.Truef(t, ok, "the debug view must carry %q", name)
				assert.Equal(t, want.status, got.Status)
				assert.Equal(t, want.critical, got.Critical)
				assert.Equal(t, want.errText, got.Error)
				assert.Equal(t, want.details, got.Details, "the debug view carries the full unredacted details")
				assert.False(t, got.LastRun.IsZero())
				assert.NotEmpty(t, got.Duration)
			}

			assert.Equal(t, tt.wantSummary, healthSummary(components))
		})
	}
}

// TestReadinessProbeOrderIsRegistrationOrder pins that the report — and therefore the
// gate's "first failing critical" — follows registration order, which is what makes the
// 503 body name the database rather than whichever kind the map iteration happened to hit.
func TestReadinessProbeOrderIsRegistrationOrder(t *testing.T) {
	report := runReadinessProbes(context.Background(), []Prober{
		disabledProbe(componentDatabase),
		disabledProbe(componentMessaging),
		disabledProbe(componentCache),
		disabledProbe(componentStreams),
	})

	names := make([]string, 0, len(report))
	for i := range report {
		names = append(names, report[i].status.Name)
	}
	assert.Equal(t, []string{componentDatabase, componentMessaging, componentCache, componentStreams}, names)
}

// TestIsFailingAndIsReadyEquivalentPartitionTheVocabulary pins the one predicate both views
// share. Widening isReadyEquivalent until it swallows unhealthy is the mistake that would
// make the debug summary report healthy while /ready answers 503.
func TestIsFailingAndIsReadyEquivalentPartitionTheVocabulary(t *testing.T) {
	for _, status := range []string{healthyStatus, notConfiguredStatus, disabledStatus, perTenantStatus} {
		assert.Truef(t, isReadyEquivalent(status), "%q is ready-equivalent", status)
		assert.Falsef(t, isFailing(status), "%q is not failing", status)
	}
	assert.True(t, isFailing(unhealthyStatus))
	assert.False(t, isReadyEquivalent(unhealthyStatus))
	assert.False(t, isFailing("starting"))
	assert.False(t, isReadyEquivalent("starting"))
}

// TestRunUntilBlockingStopsAtTheFirstBlockingProbe pins both halves of /ready's traversal:
// a non-critical failure never gates, however early it is registered (the messaging and
// streams kinds depend on that), and nothing after the blocking probe runs at all — which
// is what keeps a database outage from adding a Redis PING and a publisher lease to every
// poll of an unauthenticated endpoint. The trailing probe fails the test if it is reached.
func TestRunUntilBlockingStopsAtTheFirstBlockingProbe(t *testing.T) {
	report, blocking, found := runUntilBlocking(context.Background(), []Prober{
		describe(componentStreams, false, errStreamsNotOpen, nil, streamsPublicStats),
		describe(componentCache, true, errors.New("connection refused"), nil, cachePublicStats),
		mustNotRunProbe{t: t},
	})

	require.True(t, found)
	assert.Equal(t, componentCache, blocking.Name, "the non-critical failure ahead of it must not gate")
	assert.Len(t, report, 2, "evaluation stops at the blocking probe")
}

// TestRunUntilBlockingRunsEveryProbeWhenNothingBlocks is the other direction: with no
// blocking kind, /ready's run reaches every probe, so a healthy deployment's body still
// carries all of them.
func TestRunUntilBlockingRunsEveryProbeWhenNothingBlocks(t *testing.T) {
	report, _, found := runUntilBlocking(context.Background(), []Prober{
		describe(componentDatabase, true, nil, nil, databasePublicStats),
		describe(componentStreams, false, errStreamsNotOpen, nil, streamsPublicStats),
		describe(componentCache, false, errors.New("connection refused"), nil, cachePublicStats),
	})

	assert.False(t, found)
	assert.Len(t, report, 3, "a non-critical failure must not truncate the body")
}
```

Then **move** `TestPublicProbeError` verbatim from `app/lifecycle_test.go:842-867` (including its comment block) into `app/readiness_render_test.go`, since the function under test moves in Step 7. It uses `pgconnIdentityError`, `databaseUnavailableBody` and `cacheUnavailableBody` from `app/health_test.go` — same package, no import change.

### Step 7: Run the test to verify it fails

Run: `go test ./app/ -run 'TestReadinessViews|TestReadinessProbeOrder|TestIsFailingAnd|TestRunUntilBlocking' -count=1`

Expected: FAIL — the package does not compile, with `undefined: runUntilBlocking`, `undefined: runReadinessProbes`, `undefined: readinessReport`, `undefined: notReadyBody`, `undefined: isFailing`, `undefined: isReadyEquivalent`, `undefined: healthSummary`, and a duplicate-declaration error for `TestPublicProbeError` if the old copy was not deleted.

### Step 8: Implement the run, the gate and the two bodies

Append to `app/readiness_render.go` (after `publicProjection`):

```go
// probeResult is one probe's outcome, the allowlist of the description that produced it,
// and the timing the debug view reports.
type probeResult struct {
	status      HealthStatus
	publicStats []string
	startedAt   time.Time
	duration    time.Duration
}

// readinessReport is every registered probe's result, in registration order.
type readinessReport []probeResult

// runProbe runs one probe and records its outcome, its description's allowlist and its
// timing. Both traversals below go through it, so the two views cannot disagree about what
// a result is.
func runProbe(ctx context.Context, probe Prober) probeResult {
	startedAt := time.Now()
	result := probeResult{status: probe.Run(ctx), startedAt: startedAt}
	result.duration = time.Since(startedAt)
	// SECURITY: only the framework's own descriptions declare an allowlist. A Prober from
	// outside publishes its status and nothing else, because nothing here knows which of
	// its detail keys are safe on an unauthenticated body.
	if description, ok := probe.(probeDescription); ok {
		result.publicStats = description.publicStats
	}
	return result
}

// runReadinessProbes runs every registered probe once, in registration order. This is the
// debug view's traversal: it reports one entry per kind, so it cannot stop early.
func runReadinessProbes(ctx context.Context, probes []Prober) readinessReport {
	report := make(readinessReport, 0, len(probes))
	for _, probe := range probes {
		report = append(report, runProbe(ctx, probe))
	}
	return report
}

// runUntilBlocking is /ready's traversal: judge in registration order and stop at the first
// failing critical kind, which is the 503. Nothing after it runs — a database outage must
// not add a publisher lease and a Redis PING to every poll of an endpoint that carries no
// authentication and no IP allowlist. The returned report is complete whenever found is
// false, which is exactly when readyBody renders it.
func runUntilBlocking(ctx context.Context, probes []Prober) (report readinessReport, blocking HealthStatus, found bool) {
	report = make(readinessReport, 0, len(probes))
	for _, probe := range probes {
		result := runProbe(ctx, probe)
		report = append(report, result)
		if isFailing(result.status.Status) && result.status.Critical {
			return report, result.status, true
		}
	}
	return report, HealthStatus{}, false
}

// isFailing is the one predicate both views share: a kind is failing exactly when its
// status is unhealthy, and judge guarantees such a status carries an Err.
func isFailing(status string) bool {
	return status == unhealthyStatus
}

// isReadyEquivalent reports the statuses /ready answers 200 for. Absence by design —
// not_configured, disabled, per_tenant — is not failure, so the debug summary must agree
// with /ready; otherwise the same database-free service reads "ready" on one endpoint and
// "critical" on the other.
func isReadyEquivalent(status string) bool {
	switch status {
	case healthyStatus, notConfiguredStatus, disabledStatus, perTenantStatus:
		return true
	default:
		return false
	}
}

// readyBody renders the unauthenticated 200 body: the fixed envelope, then every registered
// kind's status under <name> and its public statistics under <name>_stats.
func (r readinessReport) readyBody(app *config.AppConfig, now time.Time) map[string]any {
	body := make(map[string]any, 2*len(r)+3)
	body[statusKey] = readyStatus
	body["time"] = now.Unix()
	body["app"] = map[string]any{
		"name":        app.Name,
		"environment": app.Env,
		"version":     app.Version,
	}
	for i := range r {
		result := &r[i]
		body[result.status.Name] = result.status.Status
		body[result.status.Name+statsSuffix] = publicProjection(result.status.Details, result.publicStats)
	}
	return body
}

// notReadyBody renders the unauthenticated 503 body: the blocking kind's status and
// ADR-048's sanitized error text, never its statistics and never any other kind's status.
func notReadyBody(result *HealthStatus) map[string]any {
	return map[string]any{
		statusKey:   "not ready",
		result.Name: result.Status,
		errorKey:    publicProbeError(result),
	}
}

// debugComponents renders the access-controlled debug view: one entry per registered kind,
// carrying the full unredacted details the /ready projection withholds.
func (r readinessReport) debugComponents() map[string]ComponentHealth {
	components := make(map[string]ComponentHealth, len(r))
	for i := range r {
		result := &r[i]
		component := ComponentHealth{
			Status:   result.status.Status,
			Critical: result.status.Critical,
			Details:  result.status.Details,
			LastRun:  result.startedAt,
			Duration: result.duration.String(),
		}
		if result.status.Err != nil {
			component.Error = result.status.Err.Error()
		}
		if component.Details == nil {
			component.Details = make(map[string]any)
		}
		components[result.status.Name] = component
	}
	return components
}

// healthSummary aggregates the debug view from the predicate /ready gates on, so the two
// views cannot disagree about what counts as a failure. unknown survives for the two shapes
// the vocabulary does not cover: no probes at all, and a consumer Prober reporting a status
// of its own invention.
func healthSummary(components map[string]ComponentHealth) HealthSummary {
	summary := HealthSummary{TotalProbes: len(components)}
	for _, component := range components {
		switch {
		case isFailing(component.Status):
			summary.ErrorCount++
			if component.Critical {
				summary.CriticalCount++
			}
		case isReadyEquivalent(component.Status):
			summary.HealthyCount++
		}
	}

	switch {
	case summary.CriticalCount > 0:
		summary.OverallStatus = "critical"
	case summary.ErrorCount > 0:
		summary.OverallStatus = degradedStatus
	case summary.TotalProbes > 0 && summary.HealthyCount == summary.TotalProbes:
		summary.OverallStatus = healthyStatus
	default:
		summary.OverallStatus = unknownStatus
	}
	return summary
}
```

Now **move** `publicProbeError` from `app/lifecycle.go:538-555` into `app/readiness_render.go` (place it directly above `debugComponents`), copying the function **and its entire comment block verbatim** — the SECURITY rationale is the reason the function exists. Delete it from `lifecycle.go`.

### Step 9: Run the tests to verify they pass

Run: `go test ./app/ -run 'TestReadinessViews|TestReadinessProbeOrder|TestIsFailingAnd|TestRunUntilBlocking|TestPublicProbeError|TestPublicProjection|TestPublicStatsAllowlists' -count=1`

Expected: `ok  github.com/gaborage/go-bricks/app` — every subtest passes. (`go build ./...` still succeeds; `readyCheck` has not changed yet and still uses the old writers.)

### Step 10: Commit

```bash
git add app/readiness.go app/readiness_render.go app/readiness_render_test.go app/readiness_test.go app/lifecycle.go app/lifecycle_test.go
cat > /tmp/msg-readiness-render.txt <<'EOF'
feat(app): derive both readiness views from one probe run

Add the public-stats allowlist to the probe description and the render half of
the readiness module: probe outcomes collected into a readinessReport, one
failing predicate, and the 200/503 bodies plus the debug components and summary
derived from it. Two traversals share one per-probe step — runUntilBlocking is
/ready's and stops at the first failing critical kind, runReadinessProbes is the
debug view's and reports every kind.

The allowlist replaces the two per-writer denylists: a counter added to a
manager tomorrow stays off the unauthenticated /ready body until someone
reviews it, rather than reaching it and being removed afterwards. The two keys
deliberately withheld are DbManager.Stats()["connections"] (per-connection
entries keyed by the resourcepool key, which is the tenant ID in a multi-tenant
deployment) and streams.Manager.Stats()["stored_offsets"] (keyed
"<stream>/<consumer>" with live offsets, which differencing two polls turns
into a per-stream message rate).

publicProbeError moves beside the bodies it sanitizes; ADR-048 is unchanged.
readyCheck and handleHealthDebug still use the old writers — they are rewired
in the following commits.

Refs: ADR-066
EOF
git commit -F /tmp/msg-readiness-render.txt
```

---

## Task 2: Rewire `readyCheck` and delete the old `/ready` writers

**Files:**

- Modify: `app/lifecycle.go:557-669` (delete `dbConnectionsKey`, `publicDBStats`, `streamsOffsetsKey`, `publicStreamsStats`; rewrite `readyCheck`)
- Modify: `app/health.go:30-45` (delete `getStatsOrEmpty` and `componentReport`)
- Modify: `app/lifecycle_test.go` (delete four tests and one helper; retarget two)
- Modify: `app/health_test.go:32-46` (delete `TestGetStatsOrEmpty`)
- Modify: `app/app_test.go:870-1122` (retarget `TestReadyCheckScenarios`)
- Modify: `app/debug_health_test.go:459-536` (retarget the `db_stats` key and the withheld-key constant)

**Interfaces:**

- Consumes: `runUntilBlocking`, `readinessReport.readyBody`, `notReadyBody`, `publicProbeError` (all from Task 1). `runReadinessProbes` is the debug view's traversal and is consumed by Task 3, not here.
- Produces: nothing new — `readyCheck` keeps its signature `func (a *App) readyCheck(c server.HandlerContext) error`.

### Step 1: Delete the tests the module table replaces

From `app/lifecycle_test.go`, delete these five blocks **entirely, including their comment blocks**:

- `TestPublicDBStatsDropsConnectionsOnACopy` (lines 947-973) — replaced by `TestPublicProjectionKeepsOnlyAllowlistedKeys`.
- `TestPublicDBStatsPreservesAbsentDatabaseShape` (lines 975-983) — replaced by `TestPublicProjectionWithoutAnAllowlist`.
- `streamsProbeWithOffsets` helper (lines 1028-1043) and `TestReadyCheckWithholdsStreamIdentifiers` (lines 1045-1072) — replaced by the `forbidden` column of `TestReadinessViews`' first row.
- `TestHealthDebugRetainsStreamIdentifiers` (lines 1074-1091) — replaced by `TestReadinessViews`' `wantComponents.details` assertions.
- `TestPublicStreamsStatsCopiesRatherThanMutates` (lines 1093-1104) — replaced by `TestPublicProjectionKeepsOnlyAllowlistedKeys`.

From `app/health_test.go`, delete `TestGetStatsOrEmpty` (lines 32-46). Leave everything else in that file — the manager fixtures and the `pgconnIdentityError` / `databaseUnavailableBody` / `cacheUnavailableBody` constants are still used.

Retarget the two kept tests in `app/lifecycle_test.go`:

- `TestReadyCheckOmitsStreamsWhenNoneDeclared` (lines 996-1007): no change needed — an empty probe set still renders neither key.
- `TestReadyCheckReportsStreamsWhenProbed` (lines 1009-1026): the hand-built description now needs an allowlist, or its counter is filtered out. Replace the `probeDescription` literal with:

```go
		probeDescription{
			name:        componentStreams,
			publicStats: streamsPublicStats,
			live:        func(context.Context) error { return nil },
			stats:       func() map[string]any { return map[string]any{"consumers": 2} },
		},
```

In `app/app_test.go`, retarget `TestReadyCheckScenarios`:

- Row `healthy` (line 887): `body["db_stats"]` → `body["database_stats"]`. Add, after the `componentCache` assertion:

```go
					assert.Equal(t, map[string]any{statusKey: disabledStatus}, body[cacheStatsKey],
						"every registered kind renders both keys, disabled included")
```

- Row `database not configured` (lines 909-913): add, after the `componentDatabase` assertion:

```go
					dbStats, ok := body["database_stats"].(map[string]any)
					require.True(t, ok, "an absent database still renders database_stats")
					assert.Equal(t, notConfiguredStatus, dbStats[statusKey])
```

In `app/debug_health_test.go`, retarget `TestHealthDebugKeepsPooledConnectionKeysWhileReadyOmitsThem`:

- lines 489, 518, 531: `dbConnectionsKey` → `connectionsStatsKey` (the test constant added in Task 1).
- line 512: `readyBody["db_stats"]` → `readyBody["database_stats"]`, and the message `"the 200 body must still carry db_stats"` → `"the 200 body must still carry database_stats"`.

### Step 2: Run the tests to verify they fail

Run: `go test ./app/ -run 'TestReadyCheckScenarios|TestReadyCheckReportsStreamsWhenProbed|TestHealthDebugKeepsPooledConnectionKeys' -count=1`

Expected: FAIL. `TestReadyCheckScenarios/healthy` reports `Expected value not to be nil` / a failed `.(map[string]any)` type assertion on `body["database_stats"]`, because `readyCheck` still writes `db_stats`. `TestHealthDebugKeepsPooledConnectionKeys...` fails the same way.

### Step 3: Rewrite `readyCheck` and delete the old writers

In `app/lifecycle.go`, delete lines 557-606 entirely: `dbConnectionsKey`, `publicDBStats`, `streamsOffsetsKey`, `publicStreamsStats` and all their comments. (Their SECURITY rationale now lives on the allowlists in `app/readiness.go`.)

Replace `readyCheck` (lines 608-669) with:

```go
// readyCheck handles the readiness endpoint: one probe run, one gate, one body (ADR-066).
// The run stops at the first failing critical kind, so an outage costs the probes ahead of
// it and no more.
func (a *App) readyCheck(c server.HandlerContext) error {
	ctx := c.RequestContext()
	report, blocking, found := runUntilBlocking(ctx, a.healthProbes)

	if found {
		// /ready is unauthenticated and the limiters do not exempt it, but they key probes
		// by client IP (probeSkipper skips tenant resolution, not the limiters), so one
		// source can still abandon many requests in a row. That IP is derived through the
		// trusted-proxy chain (ADR-057), so only a caller already inside a default-trusted
		// range (loopback, link-local, RFC1918, IPv6 ULA) can still choose its own key, and
		// the budget is per-source either way. An abandoned request — the
		// caller's own context canceled, and the probe reports that same context.Canceled —
		// is not a readiness incident, so it logs WARN, not ERROR. The caller's context must
		// actually be done: a probe that reports context.Canceled while the request is still
		// live was canceled from inside, which is a genuine incident and stays ERROR.
		event := a.logger.Error()
		if errors.Is(ctx.Err(), context.Canceled) && errors.Is(blocking.Err, context.Canceled) {
			event = a.logger.Warn()
		}
		event.Err(blocking.Err).Str("component", blocking.Name).Msg("Readiness check failed")
		return c.JSON(http.StatusServiceUnavailable, notReadyBody(&blocking))
	}

	return c.JSON(http.StatusOK, report.readyBody(&a.cfg.App, time.Now()))
}
```

Remove the now-unused `"maps"` import from `app/lifecycle.go` (it was used only by the two deleted writers — verify with `grep -n 'maps\.' app/lifecycle.go`, which must print nothing).

In `app/health.go`, delete `getStatsOrEmpty` (lines 30-35) and `componentReport` (lines 37-45). The file is left with the `context` import, `HealthStatus` and `Prober` only.

### Step 4: Run the tests to verify they pass

Run: `go test ./app/ -count=1`

Expected: `ok  github.com/gaborage/go-bricks/app` — the whole package passes, including `TestReadyCheckScenarios` (13 subtests), `TestReadyCheckDowngradesCallerCancellationLog` (4 subtests), `TestReadyCheckWithholdsDatabaseIdentityFromBody`, `TestReadyCheckSanitizesCriticalProbeWithoutPublicError`, `TestReadyCheckOmitsStreamsWhenNoneDeclared`, `TestReadyCheckReportsStreamsWhenProbed`, and both `TestHealthDebug*` routing tests.

### Step 5: Commit

```bash
git add app/lifecycle.go app/health.go app/lifecycle_test.go app/health_test.go app/app_test.go app/debug_health_test.go
cat > /tmp/msg-readycheck.txt <<'EOF'
refactor(app): render /ready from the readiness module

readyCheck becomes transport: run the probes, ask the report for the first
failing critical kind, render one of two bodies. The four hand-written render
helpers it used — publicDBStats, publicStreamsStats, componentReport and
getStatsOrEmpty — delete, and with them the four-name key list that decided
which kinds appeared on the 200 body.

Visible consequence: the body now renders <kind> and <kind>_stats for every
registered kind, so db_stats becomes database_stats and a kind whose probe is
registered but whose manager is nil renders {"status":"disabled"} rather than
being omitted. Status codes are unchanged for every deployment.

The gate now reads status == unhealthy && Critical rather than Err != nil &&
Critical; PR1a's judge guarantees those agree for every framework probe. The
traversal is unchanged: evaluation still stops at the first failing critical
probe, so an outage never adds a publisher lease or a Redis PING to a poll.

Refs: ADR-066
EOF
git commit -F /tmp/msg-readycheck.txt
```

---

## Task 3: Rewire the debug view and delete its two writers

**Files:**

- Modify: `app/debug_health.go:54-95` (rewrite `handleHealthDebug`), delete lines 117-189 (`calculateHealthSummary`, `addManagerHealth`)
- Modify: `app/debug_health_test.go:202-388, 538-569` (delete three tests)

**Interfaces:**

- Consumes: `runReadinessProbes`, `readinessReport.debugComponents`, `healthSummary` (Task 1).
- Produces: nothing new — `handleHealthDebug` keeps its signature.

### Step 1: Delete the tests the module table replaces

From `app/debug_health_test.go`, delete entirely:

- `TestCalculateHealthSummary` (lines 202-287) — replaced by the `wantSummary` column of `TestReadinessViews`, whose rows cover healthy / degraded / critical / unknown / empty.
- `TestAddManagerHealth` (lines 289-388) — the function under test is deleted; the manager statistics it asserted are now the `database` and `messaging` entries' `details`, covered by `TestReadinessViews` and by `TestHealthDebugKeepsPooledConnectionKeysWhileReadyOmitsThem`.
- `TestCalculateHealthSummaryTreatsAbsenceAsHealthy` (lines 538-569) — replaced by the `absence_is_ready_equivalent_in_both_views` row and by `TestIsFailingAndIsReadyEquivalentPartitionTheVocabulary`.

Then remove the imports that become unused: `"github.com/gaborage/go-bricks/messaging"` (used only by `TestAddManagerHealth`). Keep `database` and `dbtesting` — `TestHealthDebugKeepsPooledConnectionKeysWhileReadyOmitsThem` still uses them.

Keep `TestDebugHealthHandlers`, `TestGetAppInfo`, `testHealthProbe`, `TestHealthDebugKeepsFullCacheErrorWhileReadySanitizes` and `TestHealthDebugKeepsPooledConnectionKeysWhileReadyOmitsThem` untouched.

### Step 2: Run the tests to verify they fail

Run: `go test ./app/ -run 'TestDebugHealthHandlers' -count=1`

Expected: PASS at this point — deleting tests cannot fail a build. This is the one step in the plan with no RED signal available at the deletion boundary; the RED for the *behavior* change was already taken in Task 1 (`TestReadinessViews`' `non_critical_failure_stays_ready_and_reads_degraded` row asserts `degraded` where the old `calculateHealthSummary` returned `unknown`, and it fails against the old function). Confirm that RED is still meaningful by running:

Run: `go test ./app/ -run 'TestReadinessViews/non_critical_failure_stays_ready_and_reads_degraded' -count=1`

Expected: PASS (it tests `healthSummary`, added in Task 1). The handler is what still calls the old writer — verified by the assertion added in Step 3 below.

### Step 3: Add the failing handler assertion

Append to `app/debug_health_test.go`:

```go
// TestHealthDebugRendersOneEntryPerKind pins the handler against the one-debug-view rule:
// the components map carries exactly the registered kinds, keyed by component name, and no
// separate *_manager entries — a manager's statistics are its kind's details. The summary
// comes from the same predicate /ready gates on, so a non-critical kind that is not live
// reads degraded rather than the unknown the two-model split produced.
func TestHealthDebugRendersOneEntryPerKind(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Name: appName, Env: testName, Version: appVersion}}
	app := &App{
		cfg:    cfg,
		logger: logger.New("error", false),
		healthProbes: []Prober{
			probeDescription{
				name:        componentDatabase,
				critical:    true,
				publicStats: databasePublicStats,
				live:        func(context.Context) error { return nil },
				stats:       func() map[string]any { return map[string]any{"active_connections": 1} },
			},
			probeDescription{
				name:        componentStreams,
				publicStats: streamsPublicStats,
				live:        func(context.Context) error { return errStreamsNotOpen },
				stats: func() map[string]any {
					return map[string]any{"stored_offsets": map[string]int64{"orders/projector": 7}}
				},
			},
		},
		dbManager:        &database.DbManager{},
		messagingManager: &messaging.Manager{},
	}

	handlers := NewDebugHandlers(app, &config.DebugConfig{Enabled: true, PathPrefix: "/_debug"}, app.logger)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health-debug", http.NoBody)
	rec := httptest.NewRecorder()
	require.NoError(t, handlers.handleHealthDebug(server.NewHandlerContextForTest(rec, req, cfg)))

	var decoded struct {
		Data struct {
			Components map[string]struct {
				Status  string         `json:"status"`
				Error   string         `json:"error"`
				Details map[string]any `json:"details"`
			} `json:"components"`
			Summary HealthSummary `json:"summary"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.ElementsMatch(t, []string{componentDatabase, componentStreams},
		keysOf(decoded.Data.Components), "one entry per registered kind, and no *_manager entries")
	assert.Equal(t, errStreamsNotOpen.Error(), decoded.Data.Components[componentStreams].Error)
	assert.Contains(t, decoded.Data.Components[componentStreams].Details, "stored_offsets",
		"the access-controlled view keeps what the /ready projection withholds")
	assert.Equal(t, HealthSummary{
		OverallStatus: degradedStatus,
		TotalProbes:   2,
		HealthyCount:  1,
		ErrorCount:    1,
	}, decoded.Data.Summary)
}

// keysOf returns a map's keys, so a components assertion can name the set rather than
// checking membership one key at a time.
func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
```

Run: `go test ./app/ -run TestHealthDebugRendersOneEntryPerKind -count=1`

Expected: FAIL — `elements differ: extra elements in list B: [database_manager messaging_manager]`, and the summary assertion reports `OverallStatus: "unknown"` where `degraded` was expected. Both are exactly the two behaviors this task removes.

### Step 4: Rewrite `handleHealthDebug` and delete its writers

Replace `handleHealthDebug` (`app/debug_health.go:54-95`) with:

```go
// handleHealthDebug provides comprehensive health debugging information
func (d *DebugHandlers) handleHealthDebug(c server.HandlerContext) error {
	start := time.Now()

	components := runReadinessProbes(c.RequestContext(), d.app.healthProbes).debugComponents()

	healthInfo := &HealthDebugInfo{
		Components: components,
		Summary:    healthSummary(components),
		App:        d.getAppInfo(),
	}

	resp := d.newDebugResponse(start, healthInfo, nil)
	return c.JSON(http.StatusOK, resp)
}
```

Delete `calculateHealthSummary` (lines 117-153) and `addManagerHealth` (lines 155-189) entirely. Keep the `unknownStatus` constant (lines 12-14) — `healthSummary` still uses it.

### Step 5: Run the tests to verify they pass

Run: `go test ./app/ -count=1`

Expected: `ok  github.com/gaborage/go-bricks/app` — `TestHealthDebugRendersOneEntryPerKind` passes and `TestDebugHealthHandlers` (3 subtests), `TestHealthDebugKeepsFullCacheErrorWhileReadySanitizes` and `TestHealthDebugKeepsPooledConnectionKeysWhileReadyOmitsThem` still pass.

### Step 6: Commit

```bash
git add app/debug_health.go app/debug_health_test.go
cat > /tmp/msg-debug-view.txt <<'EOF'
refactor(app): render the debug health view from the readiness module

handleHealthDebug becomes transport too: the components map and the summary
both come from one probe run, so the debug view and /ready cannot disagree
about what counts as a failure. calculateHealthSummary and addManagerHealth
delete.

Two visible changes. The components map no longer carries the separate
database_manager and messaging_manager entries — those statistics are the
database and messaging entries' details, and the pair had been two kinds behind
since the cache and streams kinds landed. And overall_status now reads degraded
where it read unknown for a non-critical kind that is not live, because the
summary counts from the same predicate the /ready gate uses rather than from
its own status list.

unknown survives for the two shapes the vocabulary does not cover: no probes at
all, and a consumer Prober reporting a status of its own invention.

Refs: ADR-066
EOF
git commit -F /tmp/msg-debug-view.txt
```

---

## Task 4: Criticality once — delete `App.cacheAbsent`

**Files:**

- Modify: `app/app.go:82-83` (delete the field), `app/app.go:98-110` (`createHealthProbes` takes `probeInputs`)
- Modify: `app/app_builder.go:202` (drop the field from the literal), `:402-405` (`preInitCache`), `:436` (`CreateHealthProbes`)
- Modify: `app/app_builder_test.go:1140-1179` (`TestPreInitCacheSkipsAbsentCache`)
- Modify: `app/app_test.go:507, 813, 842`, `app/lifecycle_test.go:820`, `app/debug_health_test.go:428, 496`, `app/readiness_test.go:366` (call sites)

**Decision and why:** the brief offered two shapes. This plan takes the recommended one — delete the field, keep `rootCacheAbsent` as the config-side verdict, and hand the resulting bool to `createHealthProbes` in a one-field unexported struct. The struct rather than a bare `bool` because every test call site then reads `createHealthProbes(probeInputs{})` — "no special inputs" — instead of a naked `false`, and because the absence verdict genuinely cannot be computed from `App` alone: it needs `Options`, which the Builder holds and `App` does not. `Builder.CreateHealthProbes` and `Builder.RegisterClosers` keep their names, so no exported surface moves.

**Interfaces:**

- Produces: `type probeInputs struct { cacheAbsent bool }` and `func (a *App) createHealthProbes(inputs probeInputs) []Prober`.

### Step 1: Write the failing test

Replace `TestPreInitCacheSkipsAbsentCache` in `app/app_builder_test.go` (lines 1140-1179) with:

```go
// TestPreInitCacheSkipsAbsentCache pins that preInitCache never leases when the cache is
// absent under the fixed "" key, so the pool's errors counter starts at a true zero (see
// rootCacheAbsent). The verdict is computed from the Builder's own config and options —
// App no longer carries a precomputed copy that could drift from them.
func TestPreInitCacheSkipsAbsentCache(t *testing.T) {
	newConnectorCountingManager := func(t *testing.T, calls *atomic.Int32) *cache.CacheManager {
		t.Helper()
		mgr := createTestCacheManagerWithConnector(t, func(context.Context, string) (cache.Cache, error) {
			calls.Add(1)
			return nil, config.NewNotConfiguredError("cache", "CACHE_REDIS_HOST", "cache.redis.host")
		})
		t.Cleanup(func() { assert.NoError(t, mgr.Close()) })
		return mgr
	}

	t.Run("absent_skips_the_connector", func(t *testing.T) {
		var connectorCalls atomic.Int32
		builder := &Builder{
			cfg:    &config.Config{},
			logger: logger.New("error", false),
			bundle: &dependencyBundle{cacheManager: newConnectorCountingManager(t, &connectorCalls)},
		}
		require.True(t, rootCacheAbsent(builder.cfg, builder.opts), "the fixture must model an absent cache")

		builder.preInitCache(context.Background(), time.Second)

		assert.Equal(t, int32(0), connectorCalls.Load(), "the connector must never be reached")
	})

	t.Run("present_reaches_the_connector", func(t *testing.T) {
		var connectorCalls atomic.Int32
		builder := &Builder{
			cfg:    &config.Config{Cache: config.CacheConfig{Enabled: true}},
			logger: logger.New("error", false),
			bundle: &dependencyBundle{cacheManager: newConnectorCountingManager(t, &connectorCalls)},
		}
		require.False(t, rootCacheAbsent(builder.cfg, builder.opts), "the fixture must model a present cache")

		builder.preInitCache(context.Background(), time.Second)

		assert.Equal(t, int32(1), connectorCalls.Load(), "an unexempt cache must still be probed")
	})
}
```

Append to `app/readiness_test.go`, after `TestCreateHealthProbesAlwaysDescribesTheThreeClassicKinds`:

```go
// TestCreateHealthProbesTakesAbsenceFromItsInputs pins that the cache description's absence
// arm is driven by the caller's verdict rather than by state stored on App: absence needs
// Options, which only the Builder holds, so a second copy on App could drift from it. The
// connector always fails, so the two arms are told apart by whether it was reached at all.
func TestCreateHealthProbesTakesAbsenceFromItsInputs(t *testing.T) {
	app := &App{cfg: defaultTestConfig(), cacheManager: createTestCacheManagerWithGetError(t,
		errors.New("the absent arm must never reach the connector"))}

	absent := app.createHealthProbes(probeInputs{cacheAbsent: true})[2].Run(context.Background())
	present := app.createHealthProbes(probeInputs{})[2].Run(context.Background())

	assert.Equal(t, notConfiguredStatus, absent.Status, "an absent cache is judged without leasing")
	assert.Equal(t, unhealthyStatus, present.Status, "a present cache leases and reports the connector's failure")
}
```

### Step 2: Run the tests to verify they fail

Run: `go test ./app/ -run 'TestPreInitCacheSkipsAbsentCache|TestCreateHealthProbesTakesAbsenceFromItsInputs' -count=1`

Expected: FAIL — the package does not compile, with `undefined: probeInputs` and `too many arguments in call to app.createHealthProbes`.

### Step 3: Make the change

`app/app.go` — delete the field (lines 82-83):

```go
	resourceProvider ResourceProvider
```

(that is, remove the `// cacheAbsent precomputes …` comment and the `cacheAbsent bool` line entirely).

`app/app.go` — replace `createHealthProbes` (lines 98-110):

```go
// probeInputs carries the verdicts createHealthProbes cannot reach from App alone: the
// cache's absence under the fixed "" key depends on Options (rootCacheAbsent), which the
// Builder holds and App does not.
type probeInputs struct {
	cacheAbsent bool
}

// createHealthProbes builds the readiness probe set: one description per classic kind, in
// registration order, a nil manager yielding a disabled one. Criticality and per-tenancy
// are decided here, once (nil-guarded like Config.IsCacheCritical, since a
// directly-constructed App may carry no config). The streams description is appended at
// runtime by prepareStreamConsumers.
func (a *App) createHealthProbes(inputs probeInputs) []Prober {
	perTenant := a.cfg != nil && a.cfg.Multitenant.Enabled
	return []Prober{
		databaseProbe(a.dbManager, perTenant),
		messagingProbe(a.messagingManager, perTenant),
		cacheProbe(a.cacheManager, a.cfg.IsCacheCritical(), inputs.cacheAbsent, perTenant),
	}
}
```

`app/app_builder.go:202` — delete the `cacheAbsent: rootCacheAbsent(b.cfg, b.opts),` line from the `&App{…}` literal.

`app/app_builder.go:403` — `preInitCache`'s guard:

```go
	if b.bundle.cacheManager == nil || rootCacheAbsent(b.cfg, b.opts) {
		return
	}
```

`app/app_builder.go:436` — inside `CreateHealthProbes`:

```go
	b.app.healthProbes = b.app.createHealthProbes(probeInputs{cacheAbsent: rootCacheAbsent(b.cfg, b.opts)})
```

Update the remaining call sites to `createHealthProbes(probeInputs{})` — the verdict was `false` at each of them before, so behavior is unchanged:

- `app/app_test.go:507` (inside `rebuildClosersAndHealth`), `:813`, `:842`
- `app/lifecycle_test.go:820`
- `app/debug_health_test.go:428`, `:496`
- `app/readiness_test.go:366`

`newTestAppFixture` and `rebuildClosersAndHealth` otherwise stay exactly as they are — PR3 (lifecycle slots) retires them; the only change here is the one call site inside the helper.

### Step 4: Run the tests to verify they pass

Run: `go test ./app/ -count=1`

Expected: `ok  github.com/gaborage/go-bricks/app`. Confirm the field is gone: `grep -rn 'cacheAbsent' --include='*.go' .` must print only the four `rootCacheAbsent` call/definition sites (`app/bootstrap.go`, `app/app_builder.go` ×2, `app/app_builder_test.go` ×2) and none naming `App.cacheAbsent`.

### Step 5: Commit

```bash
git add app/app.go app/app_builder.go app/app_builder_test.go app/app_test.go app/lifecycle_test.go app/debug_health_test.go app/readiness_test.go
cat > /tmp/msg-criticality-once.txt <<'EOF'
refactor(app): decide cache absence once, where Options lives

App.cacheAbsent was a second copy of a verdict rootCacheAbsent already
computes, stored at construction and read two steps later — one of the four
sites the readiness design set out to collapse. The field deletes:
Builder.CreateHealthProbes computes the verdict from the config and options it
holds and hands it to createHealthProbes, and preInitCache asks rootCacheAbsent
directly.

createHealthProbes takes a one-field probeInputs struct rather than a bare
bool so every call site names what it is passing. Builder.CreateHealthProbes
keeps its name and signature; no exported surface moves.

Refs: ADR-066
EOF
git commit -F /tmp/msg-criticality-once.txt
```

---

## Task 5: Documentation — extend atom `[C60.3]`, the E60 hop row and gist, and the `db_stats` mentions

**Files:**

- Modify: `wiki/migrations.md` line 48 (E60 hop-table row), lines 3150-3160 (`## E60` gist), lines 3166-3198 (atom `[C60.3]`)
- Modify: `wiki/cache.md:192`
- Modify: `wiki/messaging.md:551`
- Modify: `llms.txt:4667` and `llms.txt:2374-2376`

**Do NOT touch** (verified hits that are history or already correct):

| Location | Why it stays |
| --- | --- |
| `wiki/migrations.md:1006-1052, 1084, 1239-1290, 1440-1489` | E56/E57 atom history. Migration atoms describe the hop they shipped in; rewriting them would make an older upgrade's instructions wrong. |
| `wiki/adr_048_ready_sanitize_by_default.md:140` | Historical statement about the v0.57 change, in an accepted ADR. |
| `wiki/architecture_decisions.md:1364` | Already reads "`db_stats` becomes `database_stats`". |
| `wiki/adr_066_readiness_one_module.md` | Already describes rules 2 and 3 in full. Its Delivery note needs no PR number — no PR is open for this branch. |
| `CHANGELOG.md:140` | Generated release history. |
| `docs/superpowers/**` | Plan and spec files. |
| `README.md`, `CLAUDE.md` | No `db_stats` hits (verified with `git grep -n db_stats`). |

### Step 1: Extend the atom's `detect:` line

`wiki/migrations.md:3168-3172` — replace the `detect:` bullet with:

```markdown
- detect: `git grep -n '"/ready"' -- '*_test.go'` and, across dashboards, alerts, synthetic
  checks and contract fixtures, `git grep -rn 'db_stats\|not_ready\|connection_failed\|no_active_connections\|overall_status\|database_manager\|messaging_manager' --`.
  You are looking for anything that reads `db_stats`, matches one of the retired sub-status
  strings, pins a disabled kind's `messaging_stats`/`cache_stats` to `{}`, alerts on the debug
  summary's `overall_status == unknown`, or reads the debug view's `database_manager` /
  `messaging_manager` entries.
```

### Step 2: Extend the atom's `apply:` list

`wiki/migrations.md:3181-3193` — change the opening from "five changes" to "eight changes" and append items (6), (7) and (8) after "…in favor of `degraded`/`critical`." Replace the whole bullet with:

```markdown
- apply: eight changes, all in body strings or the debug view — (1) `<name>_stats.status`
  mirrors the component's status: `unhealthy` where it used to say `no_active_connections`
  (database lease failure), `connection_failed` (messaging or cache lease failure) or
  `not_ready` (messaging leased but not ready); (2) `streams` reports `unhealthy` where it
  reported `not_ready`; (3) a `disabled` kind's `<name>_stats` is `{"status":"disabled"}` for
  every kind (messaging and cache used to render `{}`); (4) in a multi-tenant deployment
  (`multitenant.enabled: true`) messaging and cache report `per_tenant` where they reported
  `not_configured` for the fixed `""` key — the database already did; (5)
  `/_sys/health-debug`'s `components` map now carries every classic kind (a nil manager
  appears as `disabled`, which counts as healthy in the summary); (6) the 200 body renders
  `<kind>` and `<kind>_stats` for **every** registered kind, so the database's stats key is
  now `database_stats` — `db_stats` was the one key that did not match its component name —
  and a kind that used to be omitted from the body now appears with `{"status":"disabled"}`;
  (7) `/_sys/health-debug`'s `overall_status` reads `degraded`, not `unknown`, for a
  non-critical kind that is not live, because the summary now counts from the same predicate
  `/ready` gates on; and (8) the debug `components` map no longer carries the separate
  `database_manager` and `messaging_manager` entries — the same manager statistics are the
  `database` and `messaging` entries' `details`. Match `unhealthy` instead of the retired
  strings, read `database_stats` instead of `db_stats`, read the `database`/`messaging`
  entries instead of the `*_manager` ones, and drop any `overall_status == unknown` alert in
  favor of `degraded`/`critical`.
```

### Step 3: Extend the atom's `verify:` line

`wiki/migrations.md:3194-3197` — replace with:

```markdown
- verify: `curl -s localhost:8080/ready | jq '.messaging_stats.status, .cache_stats.status'`
  never prints a retired string, and `curl -s localhost:8080/ready | jq 'has("database_stats"), has("db_stats")'`
  prints `true` then `false`; with the debug endpoint enabled,
  `curl -s localhost:8080/_sys/health-debug | jq '.data.components | keys'` lists `database`,
  `messaging`, `cache` (and `streams` once declared) and no `*_manager` entry.
```

### Step 4: Extend the `## E60` gist

`wiki/migrations.md:3150-3160` — replace the `gist:` bullet with:

```markdown
- gist: `app/readiness.go` judges every readiness kind (database, messaging,
  cache, streams) from one probe description with one lease → liveness →
  status machine, and renders both readiness views from one probe run
  (ADR-066). The strings each kind used to invent are gone: `details.status`
  mirrors the component status, `unhealthy` always carries an error, streams
  reports `unhealthy` where it reported `not_ready`, a disabled kind's stats
  render `{"status":"disabled"}` for every kind, and messaging and cache read
  `per_tenant` in a multi-tenant deployment where they read `not_configured`
  for the fixed `""` key. The 200 body now renders `<kind>` and `<kind>_stats`
  for every registered kind, so `db_stats` becomes `database_stats`; the debug
  health view lists every classic kind and drops its separate
  `database_manager` / `messaging_manager` entries, whose statistics are now
  the `database` and `messaging` entries' details; and both views count from
  one predicate, so a non-critical kind that is not live reads `degraded`
  instead of `unknown` on `overall_status`. No status code changes for any
  deployment: the kinds that answered 200 still answer 200, and only a critical
  kind whose status is `unhealthy` answers 503 — the same kinds as before.
```

### Step 5: Extend the E60 hop-table row

`wiki/migrations.md:48` — in the last cell, replace:

```text
and the debug view lists every classic kind; a critical kind that is down still answers 503 exactly as before (C60.3)
```

with:

```text
and the debug view lists every classic kind; the 200 body's `db_stats` key is now `database_stats`, the debug view's `database_manager`/`messaging_manager` entries are gone (their statistics are the `database`/`messaging` entries' details), and `overall_status` reads `degraded` where it read `unknown` for a non-critical kind that is not live; a critical kind that is down still answers 503 exactly as before (C60.3)
```

And in the same cell's grep list, replace `for \`not_ready\`, \`connection_failed\`, \`no_active_connections\`,` with `for \`db_stats\`, \`not_ready\`, \`connection_failed\`, \`no_active_connections\`, \`database_manager\`/\`messaging_manager\`,`.

### Step 6: Fix the remaining `db_stats` / `messaging_manager` prose

`wiki/cache.md:191-192` — replace:

```markdown
carries `cache` (a status string) alongside `cache_stats` (the manager counters), mirroring
`database`/`db_stats` and `messaging`/`messaging_stats` (abridged below — the `database`,
```

with:

```markdown
carries `cache` (a status string) alongside `cache_stats` (the manager counters), mirroring
`database`/`database_stats` and `messaging`/`messaging_stats` (abridged below — the `database`,
```

`wiki/messaging.md:551` — replace `and under the \`messaging_manager\` component of \`GET /_sys/health-debug\`` with `and under the \`messaging\` component of \`GET /_sys/health-debug\``.

`llms.txt:4667` — in the `GET /ready` row, replace `+ \`db_stats\` / \`messaging_stats\` / \`cache_stats\`` with `+ \`database_stats\` / \`messaging_stats\` / \`cache_stats\``.

`llms.txt:2374-2376` — this sentence has been stale since PR1a landed (a nil manager now registers a `disabled` description rather than no probe, and its stats are `{"status":"disabled"}` rather than empty). Replace:

```text
result is surfaced as the top-level `cache` and `cache_stats` keys in the `200` body (the
`503` body carries only `status`/`cache`/`error`; a cache manager that failed to construct
registers no probe at all and reports `cache: "disabled"` with empty `cache_stats`). The probe leases
```

with:

```text
result is surfaced as the top-level `cache` and `cache_stats` keys in the `200` body (the
`503` body carries only `status`/`cache`/`error`; where there is no cache manager at all the
kind reports `cache: "disabled"` with `cache_stats: {"status":"disabled"}`). The probe leases
```

### Step 7: Verify the edits

Run: `git grep -n 'db_stats' -- wiki/ llms.txt README.md CLAUDE.md`

Expected: hits only in `wiki/migrations.md` (E56/E57 history plus the new `detect:` grep string and the new `apply:` sentence), `wiki/adr_048_ready_sanitize_by_default.md:140`, `wiki/architecture_decisions.md:1364` and `wiki/adr_066_readiness_one_module.md`. No hit in `wiki/cache.md` or `llms.txt`.

Run: `git grep -n 'messaging_manager\|database_manager' -- wiki/ llms.txt`

Expected: hits only inside `wiki/migrations.md`'s E60 row/atom (the new grep strings). No hit in `wiki/messaging.md`.

Run: `npx --yes markdownlint-cli2@0.18.1 'wiki/migrations.md' 'wiki/cache.md' 'wiki/messaging.md'`

Expected: `Summary: 0 error(s)`. (`make check` runs the same tool over the whole tree; this is the targeted pre-check.)

### Step 8: Commit

```bash
git add wiki/migrations.md wiki/cache.md wiki/messaging.md llms.txt
cat > /tmp/msg-readiness-docs.txt <<'EOF'
docs: extend C60.3 with the one-body rule and the folded debug view

The readiness atom shipped with the vocabulary changes; this hop also renames
the 200 body's db_stats key to database_stats, folds the debug view's
database_manager and messaging_manager entries into the database and messaging
entries' details, and makes overall_status read degraded rather than unknown
for a non-critical kind that is not live. Three items join the atom's apply
list, and the detect grep and verify command grow to match; the E60 hop row and
gist carry the same three.

wiki/cache.md and llms.txt stop naming db_stats, wiki/messaging.md stops
pointing operators at the messaging_manager debug component, and llms.txt's
disabled-cache sentence catches up with the probe description that replaced
"registers no probe at all".

E56 and E57 atom history is left alone: those atoms describe the hops they
shipped in.

Refs: ADR-066
EOF
git commit -F /tmp/msg-readiness-docs.txt
```

---

## Task 6: Controller gates

**This task is the controller's, not an implementer's.** Run in order, per CLAUDE.md.

- [ ] **Step 1: Full build and test**

Run (background): `cd /Users/gaborage/Projects/gaborage/code/go-bricks && pwd && make check`

Expected: `make check` passes — fmt, lint (golangci-lint v2), markdownlint, `go test ./... -race`, alloc guards, govulncheck, gosec. Watch specifically for `goconst` on the new allowlists (see the Task 1 note) and for `unused` on anything left behind by the deletions.

- [ ] **Step 2: Confirm the deletions actually landed**

```bash
git grep -n 'publicDBStats\|publicStreamsStats\|componentReport\|getStatsOrEmpty\|calculateHealthSummary\|addManagerHealth\|dbConnectionsKey\|streamsOffsetsKey\|cacheAbsent bool' -- '*.go'
```

Expected: no output at all. (`rootCacheAbsent` survives and does not match these patterns.)

- [ ] **Step 3: Pre-push gates, in order**

`/simplify` → `make check` if it changed code → `/security-audit` → `make check` if it changed code → `/code-review` (CodeRabbit). The security audit should be pointed at the two things this diff moves: the `/ready` public projection (an allowlist replacing two denylists) and the fact that every probe now runs before the gate.

- [ ] **Step 4: Mutation gate**

Commit first (the gate scopes to `merge-base..HEAD`), then run in the background:

Run: `cd /Users/gaborage/Projects/gaborage/code/go-bricks && pwd && make mutate`

Expected: `(N mutants on changed lines)` with N > 0 and no survivors. An empty or `no mutatable changes` result is **not** a pass.

- [ ] **Step 5: Push and open the PR**

This is PR1b of Stack A; its base is PR1a's branch state. Follow `/gh-stack` if PR1a is already open as a stacked PR; otherwise this branch carries both slices and opens one PR against `main`.

---

## Self-Review

**1. Spec coverage.**

| Spec decision | Task |
| --- | --- |
| 2 — `publicStats` allowlist on the probe description | Task 1, Steps 1-5 |
| 4 — one gate (`isFailing` shared by `runUntilBlocking` and the summary counts) | Task 1 Steps 6-9, Task 3 |
| 5 — one body rule (`<name>` + `<name>_stats`, `db_stats` → `database_stats`, disabled shape) | Task 1 Steps 6-9, Task 2 |
| 6 — one debug view (`*_manager` entries fold in) | Task 3 |
| 7 — criticality once (`App.cacheAbsent` deletes, `rootCacheAbsent` stays, the WARN stays in the Builder) | Task 4 |
| 9 — tests replace, don't layer (one module table, the eight-fixture and per-writer tests go) | Task 1 Step 6, Task 2 Step 1, Task 3 Step 1 |
| 10 — docs (atom extension, hop row, gist) | Task 5 |

The cache-criticality opt-out WARN (`warnIfCacheCriticalityOptOut`) is deliberately untouched — decision 7 keeps it in the Builder step, which holds the `Options` it reads.

**2. Placeholder scan.** No TBD/TODO; every code step carries the code, every test step carries the assertions, and every doc step carries the exact replacement text. The one prose-only step (Task 3, Step 2) explains why no RED is available at that boundary and names the RED that already covers the behavior.

**3. Type consistency.** `probeResult`/`readinessReport`/`runProbe`/`runReadinessProbes`/`runUntilBlocking`/`isFailing`/`isReadyEquivalent`/`readyBody`/`notReadyBody`/`publicProjection`/`debugComponents`/`healthSummary`/`statsSuffix`/`probeInputs` are spelled identically in every task and in the Interfaces blocks. `publicProjection(details, allow)` argument order matches all four call sites. `readyBody(app *config.AppConfig, now time.Time)` matches its one production caller (`&a.cfg.App`, `time.Now()`) and its test callers. `ComponentHealth` and `HealthSummary` field names match `app/debug_health.go:24-40` exactly.
