# App Readiness Module — Probe Descriptions (Stack A · PR1a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the four hand-written readiness probes in `app/health.go` with one generic probe judged from a per-kind *probe description*, so the lease → liveness → status machine, the status vocabulary and the criticality decision have one home.

**Architecture:** New `app/readiness.go` holds `probeDescription` (the module's input) and one `judge` that produces `HealthStatus`; `app/health.go` shrinks to the exported seam (`HealthStatus`, `Prober`). `createHealthProbes` builds one description per classic kind (database, messaging, cache — a nil manager yields a `disabled` description) and `prepareStreamConsumers` still appends the streams description at runtime. Rendering (`readyCheck` body, debug view, gate) is untouched in this PR — that is PR1b.

**Tech Stack:** Go 1.26, testify, existing `testing/mocks`, `cache/testing`, `database/testing`.

**Spec:** `docs/superpowers/specs/2026-08-16-app-readiness-and-lifecycle-slots-design.md` (decisions 1–4, 7–9). Vocabulary: `CONTEXT.md` — *Probe description*, *Readiness*.

## Global Constraints

- camelCase test function names; snake_case table case names (CLAUDE.md).
- `Prober` and `HealthStatus` (app/health.go) keep their exact exported shape.
- `/ready` and debug rendering code (`lifecycle.go:readyCheck`, `debug_health.go`) is NOT modified in this PR except where a deleted symbol forces it (`notReadyStatus`).
- Status vocabulary after this PR: `healthy · unhealthy · not_configured · disabled · per_tenant`; `unhealthy` always carries `Err`.
- Branch: `feature/app-readiness-probe-descriptions` off `main`; commit only via `git commit -F <file>` (commit hook blocks heredoc `-m`); never `--no-gpg-sign`.
- Gates before push (CLAUDE.md): `make check` → `/simplify` → `/security-audit` → `/code-review` → `make mutate` (background), all in the worktree.

---

### Task 1: The generic probe — `probeDescription` + `judge`

**Files:**

- Create: `app/readiness.go`
- Test: `app/readiness_test.go`

**Interfaces:**

- Produces: `type probeDescription struct{ name string; critical, absent, perTenant, disabled bool; acquire func(context.Context) (live func(context.Context) error, release func(), err error); live func(context.Context) error; stats func() map[string]any }`, method `(probeDescription) Run(context.Context) HealthStatus` (satisfies `Prober`), helper `disabledProbe(name string) probeDescription`, sentinel errors `errPublisherNotReady`, `errStreamsNotOpen`.

- [ ] **Step 1: Write the failing table test**

```go
// app/readiness_test.go
package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

// stubKind drives a probeDescription through every branch of the judge without a manager.
type stubKind struct {
	acquireErr  error
	liveErr     error
	stats       map[string]any
	acquired    int
	released    int
	statsCalls  int
	statsBefore int // statsCalls observed at release time — proves the snapshot is taken while held
}

func (s *stubKind) description(name string, critical, absent, perTenant, leaseless bool) probeDescription {
	d := probeDescription{name: name, critical: critical, absent: absent, perTenant: perTenant}
	d.stats = func() map[string]any {
		s.statsCalls++
		return s.stats
	}
	live := func(context.Context) error { return s.liveErr }
	if leaseless {
		d.live = live
		return d
	}
	d.acquire = func(context.Context) (func(context.Context) error, func(), error) {
		s.acquired++
		if s.acquireErr != nil {
			return nil, nil, s.acquireErr
		}
		return live, func() {
			s.released++
			s.statsBefore = s.statsCalls
		}, nil
	}
	return d
}

func TestProbeDescriptionJudge(t *testing.T) {
	notConfigured := config.NewConfigError("cache.enabled", nil, "not configured", "")
	require.True(t, config.IsNotConfigured(notConfigured), "fixture must be a not-configured error")
	boom := errors.New("dial tcp: connection refused")

	tests := []struct {
		name         string
		stub         stubKind
		critical     bool
		absent       bool
		perTenant    bool
		leaseless    bool
		wantStatus   string
		wantErr      error
		wantAcquired int
		wantReleased int
	}{
		{name: "healthy_when_lease_and_liveness_succeed", stub: stubKind{stats: map[string]any{"active": 1}}, critical: true, wantStatus: healthyStatus, wantAcquired: 1, wantReleased: 1},
		{name: "unhealthy_with_err_when_liveness_fails", stub: stubKind{liveErr: boom}, critical: true, wantStatus: unhealthyStatus, wantErr: boom, wantAcquired: 1, wantReleased: 1},
		{name: "unhealthy_with_err_when_lease_fails", stub: stubKind{acquireErr: boom}, wantStatus: unhealthyStatus, wantErr: boom, wantAcquired: 1},
		{name: "not_configured_when_lease_is_not_configured", stub: stubKind{acquireErr: notConfigured}, wantStatus: notConfiguredStatus, wantAcquired: 1},
		{name: "per_tenant_when_lease_is_not_configured_and_per_tenant", stub: stubKind{acquireErr: notConfigured}, perTenant: true, wantStatus: perTenantStatus, wantAcquired: 1},
		{name: "not_configured_without_leasing_when_absent", stub: stubKind{acquireErr: boom}, absent: true, wantStatus: notConfiguredStatus},
		{name: "per_tenant_without_leasing_when_absent_and_per_tenant", stub: stubKind{acquireErr: boom}, absent: true, perTenant: true, wantStatus: perTenantStatus},
		{name: "leaseless_kind_healthy", stub: stubKind{}, leaseless: true, wantStatus: healthyStatus},
		{name: "leaseless_kind_unhealthy_with_err", stub: stubKind{liveErr: boom}, leaseless: true, wantStatus: unhealthyStatus, wantErr: boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := tt.stub
			d := stub.description("kind", tt.critical, tt.absent, tt.perTenant, tt.leaseless)

			got := d.Run(context.Background())

			assert.Equal(t, "kind", got.Name)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.critical, got.Critical)
			assert.Equal(t, tt.wantStatus, got.Details[statusKey], "details.status mirrors the verdict")
			if tt.wantErr != nil {
				assert.ErrorIs(t, got.Err, tt.wantErr)
			} else {
				assert.NoError(t, got.Err)
			}
			assert.Equal(t, tt.wantAcquired, stub.acquired, "lease attempts")
			assert.Equal(t, tt.wantReleased, stub.released, "lease releases")
			if tt.wantReleased == 1 {
				assert.Equal(t, 1, stub.statsBefore, "stats snapshot is taken while the lease is held")
			}
			if tt.stub.stats != nil {
				assert.Equal(t, 1, got.Details["active"], "kind statistics are carried into details")
			}
		})
	}
}

func TestProbeDescriptionUnhealthyAlwaysCarriesAnError(t *testing.T) {
	// A liveness check that reports "not live" without an error still needs an Err on the
	// status — the vocabulary rule that lets one predicate serve /ready and the debug summary.
	d := probeDescription{name: "kind", live: func(context.Context) error { return errStreamsNotOpen }}
	got := d.Run(context.Background())
	assert.Equal(t, unhealthyStatus, got.Status)
	assert.ErrorIs(t, got.Err, errStreamsNotOpen)
}

func TestProbeDescriptionDisabled(t *testing.T) {
	got := disabledProbe(componentCache).Run(context.Background())
	assert.Equal(t, HealthStatus{
		Name:    componentCache,
		Status:  disabledStatus,
		Details: map[string]any{statusKey: disabledStatus},
	}, got)
}

func TestProbeDescriptionCopiesStats(t *testing.T) {
	source := map[string]any{"errors": 0}
	d := probeDescription{name: "kind", live: func(context.Context) error { return nil }, stats: func() map[string]any { return source }}
	got := d.Run(context.Background())
	got.Details["errors"] = 99
	assert.Equal(t, 0, source["errors"], "Run must not hand the caller the kind's own map")
	assert.NotContains(t, source, statusKey, "the status key is stamped on the copy, never on the source")
}

func TestProbeDescriptionNilStats(t *testing.T) {
	d := probeDescription{name: "kind", live: func(context.Context) error { return nil }, stats: func() map[string]any { return nil }}
	got := d.Run(context.Background())
	assert.Equal(t, map[string]any{statusKey: healthyStatus}, got.Details)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./app/ -run 'TestProbeDescription' -count=1`
Expected: FAIL — `undefined: probeDescription`, `undefined: disabledProbe`, `undefined: errStreamsNotOpen`.

- [ ] **Step 3: Write `app/readiness.go`**

```go
package app

import (
	"context"
	"errors"
	"maps"

	"github.com/gaborage/go-bricks/config"
)

// Readiness is one module: every kind is judged by the same machine from a probe
// description (CONTEXT.md), so the status vocabulary, the lease→liveness order and the
// criticality decision have one home. Prober and HealthStatus (health.go) stay the exported
// seam; this file is what sits behind it.

var (
	// errPublisherNotReady is the liveness error for a leased AMQP client that is not ready:
	// unhealthy always carries an Err (see judge), so /ready's gate and the debug summary can
	// share one predicate.
	errPublisherNotReady = errors.New("publisher not ready")
	// errStreamsNotOpen is the liveness error for a streams manager whose consumers or
	// publishers are not all open.
	errStreamsNotOpen = errors.New("stream consumers not open")
)

// probeDescription is what a slot hands readiness so its kind can be judged: a fixed
// component name, whether the kind is critical, how to lease it, how to check it is live,
// and its statistics. Zero-value fields mean "this kind has no such step".
//
// SECURITY: name is interpolated into the unauthenticated /ready body ("<name> unavailable",
// ADR-048) — keep it a fixed component identifier, never a tenant, host or database name.
type probeDescription struct {
	name string
	// critical is decided once, when the description is built (config verdict × absence);
	// judge never re-derives it.
	critical bool
	// disabled marks a kind with no manager at all: reported as disabled, nothing is leased.
	disabled bool
	// absent marks a kind whose fixed "" key can never resolve (see rootCacheAbsent):
	// reported as not_configured (or per_tenant) without attempting a lease.
	absent bool
	// perTenant relabels a not-configured verdict as per_tenant: a multi-tenant deployment
	// has the resource, just not under the fixed "" key. It never short-circuits the lease —
	// a shared-ledger control-plane database (ADR-041) resolves through exactly that key.
	perTenant bool
	// acquire leases the kind's fixed-key resource and returns how to check it is live and
	// how to release it. nil for kinds probed without a lease, which set live directly.
	acquire func(ctx context.Context) (live func(context.Context) error, release func(), err error)
	// live checks a lease-less kind (only read when acquire is nil).
	live func(ctx context.Context) error
	// stats snapshots the kind's counters; called while the lease is held so the entry the
	// probe itself pooled is counted (the messaging manager publishes active_publishers: 0
	// beside a healthy verdict otherwise).
	stats func() map[string]any
}

// disabledProbe describes a kind whose manager does not exist.
func disabledProbe(name string) probeDescription {
	return probeDescription{name: name, disabled: true}
}

// Run implements Prober: judge the kind, then carry its statistics under Details with
// details.status mirroring the verdict.
func (d probeDescription) Run(ctx context.Context) HealthStatus {
	status, stats, err := d.judge(ctx)
	details := make(map[string]any, len(stats)+1)
	maps.Copy(details, stats)
	details[statusKey] = status
	return HealthStatus{
		Name:     d.name,
		Status:   status,
		Details:  details,
		Err:      err,
		Critical: d.critical,
	}
}

// judge is the one lease→liveness→status machine. Every arm that returns unhealthy also
// returns a non-nil error, so "failing" is one predicate (status == unhealthy) for both
// the /ready gate and the debug summary.
func (d probeDescription) judge(ctx context.Context) (status string, stats map[string]any, err error) {
	if d.disabled {
		return disabledStatus, nil, nil
	}
	if d.absent {
		return d.notConfigured(), d.snapshot(), nil
	}
	live := d.live
	if d.acquire != nil {
		leasedLive, release, acquireErr := d.acquire(ctx)
		if acquireErr != nil {
			if config.IsNotConfigured(acquireErr) {
				return d.notConfigured(), d.snapshot(), nil
			}
			return unhealthyStatus, d.snapshot(), acquireErr
		}
		defer release() // the probe holds no scope; the snapshot below is taken before this runs
		live = leasedLive
	}
	if liveErr := live(ctx); liveErr != nil {
		return unhealthyStatus, d.snapshot(), liveErr
	}
	return healthyStatus, d.snapshot(), nil
}

// notConfigured is the verdict for a kind that has nothing under the fixed "" key.
func (d probeDescription) notConfigured() string {
	if d.perTenant {
		return perTenantStatus
	}
	return notConfiguredStatus
}

func (d probeDescription) snapshot() map[string]any {
	if d.stats == nil {
		return nil
	}
	return d.stats()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./app/ -run 'TestProbeDescription' -count=1`
Expected: PASS (5 test functions).

- [ ] **Step 5: Commit**

```bash
git add app/readiness.go app/readiness_test.go
printf 'feat(app): add the generic readiness probe judged from a probe description\n' > /tmp/msg-t1 && git commit -F /tmp/msg-t1
```

---

### Task 2: Per-kind descriptions replace the four probe constructors

**Files:**

- Modify: `app/readiness.go` (append the four constructors)
- Modify: `app/health.go` — delete lines 15-18 (`cacheProbePingTimeout` moves), 43-140 (`healthProbeFunc`, database probe + helpers), 152-273 (messaging/streams/cache probes); keep `HealthStatus`, `Prober`, `componentReport`, `getStatsOrEmpty` (still used by `readyCheck`), move `convertCacheStatsToMap` to readiness.go
- Modify: `app/app.go:23-44` — delete `notReadyStatus`; `app/app.go:100-121` — `createHealthProbes` builds three descriptions
- Modify: `app/streams_setup.go:71` — `streamsProbe(mgr)`
- Test: `app/readiness_test.go` (per-kind seam pins), `app/health_test.go` (rewrite)

**Interfaces:**

- Consumes: Task 1's `probeDescription`, `disabledProbe`, `errPublisherNotReady`, `errStreamsNotOpen`.
- Produces: `databaseProbe(m *database.DbManager, perTenant bool) probeDescription`, `messagingProbe(m *messaging.Manager, perTenant bool) probeDescription`, `cacheProbe(m *cache.CacheManager, critical, absent, perTenant bool) probeDescription`, `streamsProbe(m *streams.Manager) probeDescription`; `createHealthProbes` now returns exactly three `Prober`s (database, messaging, cache) in that order.

- [ ] **Step 1: Write the failing per-kind tests** (append to `app/readiness_test.go`)

```go
func TestDatabaseProbeLeasesThenChecksHealth(t *testing.T) {
	db := &testmocks.MockDatabase{}
	db.On("Health", mock.Anything).Return(nil).Once()
	db.On("Stats").Return(map[string]any{}, nil).Maybe()
	db.On("Close").Return(nil).Maybe()
	m := newDBManagerFor(t, db) // helper: DbManager over a connector returning db (kept from health_test.go's createTestDbManager)

	got := databaseProbe(m, false).Run(context.Background())

	assert.Equal(t, componentDatabase, got.Name)
	assert.True(t, got.Critical, "the database is always critical")
	assert.Equal(t, healthyStatus, got.Status)
	assert.Contains(t, got.Details, "active_connections", "DbManager.Stats() is carried into details")
	db.AssertExpectations(t)
}

func TestDatabaseProbeUnhealthyWhenHealthFails(t *testing.T) {
	db := &testmocks.MockDatabase{}
	db.On("Health", mock.Anything).Return(errors.New("pg down")).Once()
	db.On("Stats").Return(map[string]any{}, nil).Maybe()
	db.On("Close").Return(nil).Maybe()
	m := newDBManagerFor(t, db)

	got := databaseProbe(m, false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, got.Status)
	assert.EqualError(t, got.Err, "pg down")
}

func TestMessagingProbeNotReadyIsUnhealthyWithError(t *testing.T) {
	m := createTestMessagingManagerWithNotReadyClient(t) // survives from health_test.go

	got := messagingProbe(m, false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, got.Status)
	assert.ErrorIs(t, got.Err, errPublisherNotReady)
	assert.False(t, got.Critical, "messaging is never critical")
}

func TestMessagingProbeCountsItsOwnPublisher(t *testing.T) {
	m := createTestMessagingManagerWithStats(t, nil) // a manager with a working client
	got := messagingProbe(m, false).Run(context.Background())
	assert.Equal(t, healthyStatus, got.Status)
	assert.Equal(t, 1, got.Details["active_publishers"], "stats are read while the probe's own lease is held")
}

func TestCacheProbeBoundsTheWarmPathPing(t *testing.T) {
	// A pooled cache whose Health hangs must report unhealthy within cacheProbePingTimeout,
	// not consume the caller's whole readiness budget (#860 regression pin).
	m := createWarmCacheManagerWithHungPing(t) // helper: pooled instance whose Health blocks until ctx is done
	start := time.Now()
	got := cacheProbe(m, true, false, false).Run(context.Background())
	assert.Equal(t, unhealthyStatus, got.Status)
	assert.Error(t, got.Err)
	assert.Less(t, time.Since(start), cacheProbePingTimeout+200*time.Millisecond)
}

func TestCacheProbeAbsentNeverLeases(t *testing.T) {
	m := createTestCacheManagerWithGetError(t, errors.New("must not be called"))
	got := cacheProbe(m, true, true, false).Run(context.Background())
	assert.Equal(t, notConfiguredStatus, got.Status)
	assert.NoError(t, got.Err)
	assert.Contains(t, got.Details, "active_caches", "manager counters still render")
}

func TestStreamsProbeNotOpenIsUnhealthy(t *testing.T) {
	m := streams.NewManager(streams.ManagerOptions{URI: "rabbitmq-stream://localhost:5552"})
	got := streamsProbe(m).Run(context.Background())
	assert.Equal(t, unhealthyStatus, got.Status)
	assert.ErrorIs(t, got.Err, errStreamsNotOpen)
	assert.False(t, got.Critical)
	assert.Contains(t, got.Details, "stored_offsets")
}

func TestCreateHealthProbesAlwaysDescribesTheThreeClassicKinds(t *testing.T) {
	app := &App{cfg: defaultTestConfig()}
	probes := app.createHealthProbes()
	require.Len(t, probes, 3)
	names := []string{}
	for _, p := range probes {
		names = append(names, p.Run(context.Background()).Name)
	}
	assert.Equal(t, []string{componentDatabase, componentMessaging, componentCache}, names)
	for _, p := range probes {
		assert.Equal(t, disabledStatus, p.Run(context.Background()).Status)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./app/ -run 'TestDatabaseProbe|TestMessagingProbe|TestCacheProbe|TestStreamsProbe|TestCreateHealthProbesAlways' -count=1`
Expected: FAIL — `undefined: databaseProbe` etc.

- [ ] **Step 3: Append the constructors to `app/readiness.go`**

```go
// cacheProbePingTimeout caps the warm-path PING so a hung Redis reports unhealthy instead
// of consuming the caller's whole readiness budget. See wiki/cache.md#readiness for the
// cold-poll caveat.
const cacheProbePingTimeout = 500 * time.Millisecond

// databaseProbe describes the database kind: critical, leased through the "" key, live when
// the leased connection's Health passes. perTenant only relabels a not-configured verdict —
// the lease is always attempted (see probeDescription.perTenant).
func databaseProbe(m *database.DbManager, perTenant bool) probeDescription {
	if m == nil {
		return disabledProbe(componentDatabase)
	}
	return probeDescription{
		name:      componentDatabase,
		critical:  true,
		perTenant: perTenant,
		acquire: func(ctx context.Context) (func(context.Context) error, func(), error) {
			conn, release, err := m.Get(ctx, "")
			if err != nil {
				return nil, nil, err
			}
			return conn.Health, release, nil
		},
		stats: m.Stats,
	}
}

// messagingProbe describes the messaging kind: never critical, leased through the ""
// key, live when the leased client reports ready.
func messagingProbe(m *messaging.Manager, perTenant bool) probeDescription {
	if m == nil {
		return disabledProbe(componentMessaging)
	}
	return probeDescription{
		name:      componentMessaging,
		perTenant: perTenant,
		acquire: func(ctx context.Context) (func(context.Context) error, func(), error) {
			client, release, err := m.Publisher(ctx, "")
			if err != nil {
				return nil, nil, err
			}
			return func(context.Context) error {
				if !client.IsReady() {
					return errPublisherNotReady
				}
				return nil
			}, release, nil
		},
		stats: m.Stats,
	}
}

// cacheProbe describes the cache kind: critical per config (ADR-046), absent when the ""
// key can never resolve (rootCacheAbsent), live when a bounded PING of the leased instance
// passes — a pooled instance is returned without a round trip, so it is pinged explicitly.
func cacheProbe(m *cache.CacheManager, critical, absent, perTenant bool) probeDescription {
	if m == nil {
		return disabledProbe(componentCache)
	}
	return probeDescription{
		name:      componentCache,
		critical:  critical,
		absent:    absent,
		perTenant: perTenant,
		acquire: func(ctx context.Context) (func(context.Context) error, func(), error) {
			instance, release, err := m.Get(ctx, "")
			if err != nil {
				return nil, nil, err
			}
			return func(ctx context.Context) error {
				pingCtx, cancel := context.WithTimeout(ctx, cacheProbePingTimeout)
				defer cancel()
				return instance.Health(pingCtx)
			}, release, nil
		},
		stats: func() map[string]any { return convertCacheStatsToMap(m.Stats()) },
	}
}

// streamsProbe describes the native stream-protocol kind: NON-critical (the reliable
// consumers reconnect on their own, so a broker flap must not take the service out of the
// load balancer), lease-less, live when every consumer and publisher is open.
func streamsProbe(m *streams.Manager) probeDescription {
	if m == nil {
		return disabledProbe(componentStreams)
	}
	return probeDescription{
		name: componentStreams,
		live: func(context.Context) error {
			if !m.Ready() {
				return errStreamsNotOpen
			}
			return nil
		},
		stats: m.Stats,
	}
}

// convertCacheStatsToMap renders cache.ManagerStats as the counters map every kind reports.
func convertCacheStatsToMap(stats cache.ManagerStats) map[string]any {
	return map[string]any{
		"active_caches": stats.ActiveCaches,
		"total_created": stats.TotalCreated,
		"evictions":     stats.Evictions,
		"idle_cleanups": stats.IdleCleanups,
		"errors":        stats.Errors,
		"max_size":      stats.MaxSize,
		"idle_ttl":      stats.IdleTTL,
	}
}
```

Add imports `time`, `github.com/gaborage/go-bricks/cache`, `.../database`, `.../messaging`, `.../messaging/streams` to `app/readiness.go`.

- [ ] **Step 4: Rewire `createHealthProbes` (app/app.go:100-121)**

```go
// createHealthProbes builds the readiness probe set: one description per classic kind, in
// registration order, a nil manager yielding a disabled one. Criticality and per-tenancy
// are decided here, once (nil-guarded like Config.IsCacheCritical, since a
// directly-constructed App may carry no config). The streams description is appended at
// runtime by prepareStreamConsumers.
func (a *App) createHealthProbes() []Prober {
	perTenant := a.cfg != nil && a.cfg.Multitenant.Enabled
	return []Prober{
		databaseProbe(a.dbManager, perTenant),
		messagingProbe(a.messagingManager, perTenant),
		cacheProbe(a.cacheManager, a.cfg.IsCacheCritical(), a.cacheAbsent, perTenant),
	}
}
```

Delete `notReadyStatus` (app.go:41-42). In `app/streams_setup.go:71` replace `streamsManagerHealthProbe(mgr)` with `streamsProbe(mgr)`.

- [ ] **Step 5: Shrink `app/health.go`**

Keep only: package clause, imports `context`, `HealthStatus`, `Prober`, `getStatsOrEmpty`, `componentReport`. Delete `cacheProbePingTimeout` (moved), `healthProbeFunc`, `databaseManagerHealthProbe`, `checkDatabaseHealth`, `handleDatabaseConnectionError`, `messagingManagerHealthProbe`, `streamsManagerHealthProbe`, `cacheManagerHealthProbe`, `convertCacheStatsToMap` (moved).

- [ ] **Step 6: Rewrite `app/health_test.go`**

Delete: `TestHealthProbeFuncRun`, `TestDatabaseManagerHealthProbe`, `TestMessagingManagerHealthProbe`, `TestConvertCacheStatsToMap` (move to readiness_test.go unchanged), `TestCacheManagerHealthProbe`, `TestCacheProbeReportsWarmPoolOutage` (covered by `TestCacheProbeBoundsTheWarmPathPing` + generic liveness row), `TestCacheProbeBoundsHungPing`, `TestCacheProbePingHonorsCallerContext` (keep — rewrite against `cacheProbe`), `TestCacheProbeReleasesLease` (generic row `wantReleased`), `TestCacheProbeSkipsLeaseWhenCacheAbsent` (→ `TestCacheProbeAbsentNeverLeases`), `TestCacheProbeStillLeasesWhenCachePresent` (generic), `TestHandleDatabaseConnectionError` (generic rows), `TestGetStatsOrEmpty` (keep), `TestMessagingManagerHealthProbeDetailed` (→ the two messaging tests above), `TestStreamsManagerHealthProbeReportsNotReady` (→ `TestStreamsProbeNotOpenIsUnhealthy`).
Keep and retarget (they pin the real seams, not the branch logic): `TestDatabaseManagerHealthProbeRendersFixedPublicError`, `TestDatabaseProbePublicErrorHidesConnectionIdentity`, `TestDatabaseManagerHealthProbeReportsNotConfigured` (#872 seam via real connector), `TestDatabaseManagerHealthProbeStaysUnhealthyForUnsupportedType`, `TestDatabaseManagerHealthProbeReportsPerTenantWhenDefaultKeyIsUnconfigured`, `TestDatabaseManagerHealthProbeStillProbesPerTenantControlPlaneDatabase` (ADR-041) — each calls `databaseProbe(m, perTenant)` instead of `databaseManagerHealthProbe(m, perTenant, log)`.
Delete factories that no test uses afterwards: `createTestDbManagerWithNilStats`, `createTestMessagingManagerWithGetPublisherError`, `cacheManagerServing` (keep if `TestCacheProbePingHonorsCallerContext` needs it), `createWarmCacheManagerWithOutage` (replace with `createWarmCacheManagerWithHungPing`).

- [ ] **Step 7: Fix the other callers**

`app/lifecycle_test.go` (5 references) and `app/app_test.go`: `TestReadyCheckScenarios` and the streams tests reference `streamsManagerHealthProbe`/`messagingManagerHealthProbe` — switch to `streamsProbe`/`messagingProbe`. Any assertion pinning `messaging_stats: {}` or `cache_stats: {}` for a disabled kind now expects `{"status":"disabled"}`; any pinning `not_ready`, `connection_failed`, `no_active_connections` in `details.status` now expects `unhealthy`.

- [ ] **Step 8: Run the package tests**

Run: `go test ./app/ -count=1 -race`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add app/readiness.go app/readiness_test.go app/health.go app/health_test.go app/app.go app/streams_setup.go app/lifecycle_test.go app/app_test.go
printf 'refactor(app): judge every readiness kind from one probe description\n\nThe four probe constructors become descriptions for one machine; every classic kind always registers (a nil manager is a disabled description); the status vocabulary is healthy/unhealthy/not_configured/disabled/per_tenant with unhealthy always carrying an error; per_tenant relabeling applies to every leased kind.\n' > /tmp/msg-t2 && git commit -F /tmp/msg-t2
```

---

### Task 3: Gates, docs touch, push

**Files:**

- Modify: `wiki/cache.md` (readiness section mentions the cache probe — confirm wording still true), `llms.txt:4667` (mentions "Messaging is never critical" — still true), no ADR in this PR (PR1b carries ADR-066 and atom C60.3; PR1a's visible changes are listed there).

- [ ] **Step 1: `make check`** (background) — fix lint (importas ordering, unused, gocritic) until green.
- [ ] **Step 2: `/simplify` → `make check` if it changed code.**
- [ ] **Step 3: `/security-audit`** — the ADR-048 property: no probe error text reaches `/ready`; `Run` never leaks `Err` into `Details`.
- [ ] **Step 4: `/code-review`** (CodeRabbit) → apply → `make check` → re-run if code changed.
- [ ] **Step 5: `make mutate`** (background) — mutants on changed lines must die; add rows for survivors (typical: the `perTenant` relabel, the `disabled` short-circuit, the `defer release()` ordering).
- [ ] **Step 6: Push and open the PR** via `/gh-stack` (base `main`), title `refactor(app): judge every readiness kind from one probe description`, body per global PR rules (What / Impact / Verification).
