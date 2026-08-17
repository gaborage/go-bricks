package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	cachetesting "github.com/gaborage/go-bricks/cache/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/streams"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
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
	notConfigured := config.NewNotConfiguredError("cache", "CACHE_REDIS_HOST", "cache.redis.host")
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

func TestDatabaseProbeLeasesThenChecksHealth(t *testing.T) {
	db := &testmocks.MockDatabase{}
	db.On("Health", mock.Anything).Return(nil).Once()
	db.On("Stats").Return(map[string]any{}, nil).Maybe()
	db.On("Close").Return(nil).Maybe()
	m := createTestDbManagerWithMock(t, db)

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
	m := createTestDbManagerWithMock(t, db)

	got := databaseProbe(m, false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, got.Status)
	assert.EqualError(t, got.Err, "pg down")
}

// TestDatabaseProbeRendersFixedPublicError pins what the database description contributes
// to the unauthenticated 503 body: it is critical, so readyCheck renders it, and the
// rendered string is the synthesized default rather than the pgconn identity string
// (`user=… database=…` plus the resolved host:port). The description declares no
// publicErr — that it is safe anyway is the whole point of the inverted default.
func TestDatabaseProbeRendersFixedPublicError(t *testing.T) {
	st := databaseProbe(newRealConnectorDBManager(&config.Config{}), false).Run(context.Background())
	require.True(t, st.Critical, "a non-critical probe would never reach the 503 render path")

	st.Err = errors.New(pgconnIdentityError)
	assert.Equal(t, databaseUnavailableBody, publicProbeError(&st))
}

// TestDatabaseProbePublicErrorHidesConnectionIdentity pins the split /ready performs: the
// sanitized string is what the unauthenticated body gets, while the full identity-bearing
// driver error stays on HealthStatus.Err for the app log and the IP-allowlisted
// /_sys/health-debug.
func TestDatabaseProbePublicErrorHidesConnectionIdentity(t *testing.T) {
	driverErr := errors.New(pgconnIdentityError)
	probe := probeDescription{
		name:     componentDatabase,
		critical: true,
		live:     func(context.Context) error { return driverErr },
	}

	result := probe.Run(context.Background())

	assert.Equal(t, databaseUnavailableBody, publicProbeError(&result))
	// /_sys/health-debug renders Err verbatim and must keep the detail operators need.
	require.ErrorIs(t, result.Err, driverErr)
	assert.Contains(t, result.Err.Error(), "user=app")
}

func TestDatabaseProbeReportsNotConfigured(t *testing.T) {
	result := databaseProbe(newRealConnectorDBManager(&config.Config{}), false).Run(context.Background())

	assert.Equal(t, notConfiguredStatus, result.Status)
	assert.NoError(t, result.Err, "an absent database is not a readiness failure")
	assert.Equal(t, notConfiguredStatus, result.Details[statusKey])
	// Criticality is retained deliberately: a database that IS configured and down must
	// still fail readiness. Absence is handled by the status, never by demoting the probe.
	assert.True(t, result.Critical)
}

func TestDatabaseProbeStaysUnhealthyForUnsupportedType(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.Type = "mysql"
	cfg.Database.Host = "db.internal"

	result := databaseProbe(newRealConnectorDBManager(cfg), false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, result.Status)
	require.Error(t, result.Err)
	// The other half of the fix: a type the operator actually asked for is a
	// misconfiguration, and must never be softened into "intentionally absent".
	assert.False(t, config.IsNotConfigured(result.Err))
}

func TestDatabaseProbeReportsPerTenantWhenDefaultKeyIsUnconfigured(t *testing.T) {
	cfg := &config.Config{}
	cfg.Multitenant.Enabled = true // no root database block: tenants carry their own

	result := databaseProbe(newRealConnectorDBManager(cfg), true).Run(context.Background())

	// not_configured would claim the service has no database — false when it has N
	// tenant databases that this fixed-key probe simply never covered.
	assert.Equal(t, perTenantStatus, result.Status)
	assert.Equal(t, perTenantStatus, result.Details[statusKey])
	assert.NoError(t, result.Err)
}

func TestDatabaseProbeStillProbesPerTenantControlPlaneDatabase(t *testing.T) {
	// Multi-tenancy does NOT imply the "" key is unconfigured: a shared-ledger
	// deployment (outbox.tenancy: shared, ADR-041) resolves a real control-plane
	// database through exactly that key. Relabeling to per_tenant before resolving
	// would leave that database unprobed while /ready reported 200 — this test is what
	// catches that.
	cfg := &config.Config{}
	cfg.Multitenant.Enabled = true
	cfg.Database.Type = "mysql" // resolves, then fails to connect
	cfg.Database.Host = "control-plane.internal"

	result := databaseProbe(newRealConnectorDBManager(cfg), true).Run(context.Background())

	assert.Equal(t, unhealthyStatus, result.Status, "a resolvable control-plane database must be probed, not relabeled")
	require.Error(t, result.Err)
	assert.True(t, result.Critical, "and it must still gate readiness")
}

func TestMessagingProbeNotReadyIsUnhealthyWithError(t *testing.T) {
	m := createTestMessagingManagerWithNotReadyClient(t)

	got := messagingProbe(m, false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, got.Status)
	assert.ErrorIs(t, got.Err, errPublisherNotReady)
	assert.False(t, got.Critical, "messaging is never critical")
}

func TestMessagingProbeCountsItsOwnPublisher(t *testing.T) {
	m := createTestMessagingManager(t)

	got := messagingProbe(m, false).Run(context.Background())

	assert.Equal(t, healthyStatus, got.Status)
	assert.Equal(t, 1, got.Details["active_publishers"], "stats are read while the probe's own lease is held")
}

// TestMessagingProbeReportsPerTenantWhenDefaultKeyIsUnconfigured pins the widened relabel:
// per_tenant is no longer a database-only verdict, so a multi-tenant deployment whose ""
// key resolves no broker reports per_tenant rather than claiming it has no messaging.
func TestMessagingProbeReportsPerTenantWhenDefaultKeyIsUnconfigured(t *testing.T) {
	m := newMessagingManagerWithSourceError(t,
		config.NewNotConfiguredError("messaging", "MESSAGING_BROKER_URL", "messaging.broker.url"))

	got := messagingProbe(m, true).Run(context.Background())

	assert.Equal(t, perTenantStatus, got.Status)
	assert.Equal(t, perTenantStatus, got.Details[statusKey])
	assert.NoError(t, got.Err)
}

// TestCacheProbeBoundsTheWarmPathPing pins the sub-budget on the warm-path PING: a pooled
// cache whose Health hangs must report unhealthy within cacheProbePingTimeout rather than
// consume the caller's whole readiness budget (#860 regression pin).
func TestCacheProbeBoundsTheWarmPathPing(t *testing.T) {
	m := createWarmCacheManagerWithHungPing(t)

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

// TestCacheProbeReportsPerTenantWhenDefaultKeyIsUnconfigured is the cache half of the
// widened relabel: the kinds share one rule, so a per-tenant cache reports per_tenant.
func TestCacheProbeReportsPerTenantWhenDefaultKeyIsUnconfigured(t *testing.T) {
	m := createTestCacheManagerWithGetError(t,
		config.NewNotConfiguredError("cache", "CACHE_REDIS_HOST", "cache.redis.host"))

	got := cacheProbe(m, false, false, true).Run(context.Background())

	assert.Equal(t, perTenantStatus, got.Status)
	assert.Equal(t, perTenantStatus, got.Details[statusKey])
	assert.NoError(t, got.Err)
}

// TestCacheProbePingHonorsCallerContext pins that the ping derives from the caller's context:
// a probe rooted at context.Background() would ignore an already-spent request budget.
func TestCacheProbePingHonorsCallerContext(t *testing.T) {
	mc := cachetesting.NewMockCache().WithDelay(10 * time.Millisecond)
	probe := cacheProbe(cacheManagerServing(t, mc), false, false, false)

	// Warm the pool so the canceled context reaches Health rather than the create path.
	require.Equal(t, healthyStatus, probe.Run(context.Background()).Status)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := probe.Run(ctx)

	assert.Equal(t, unhealthyStatus, result.Status)
	assert.ErrorIs(t, result.Err, context.Canceled)
}

func TestStreamsProbeNotOpenIsUnhealthy(t *testing.T) {
	m := streams.NewManager(streams.ManagerOptions{URI: unreachableStreamURI, Logger: logger.New("error", false)})

	got := streamsProbe(m).Run(context.Background())

	assert.Equal(t, componentStreams, got.Name)
	assert.Equal(t, unhealthyStatus, got.Status)
	assert.ErrorIs(t, got.Err, errStreamsNotOpen)
	assert.False(t, got.Critical, "a reconnecting stream consumer must not 503 the whole service")
	assert.Contains(t, got.Details, "stored_offsets")
}

func TestCreateHealthProbesAlwaysDescribesTheThreeClassicKinds(t *testing.T) {
	app := &App{cfg: defaultTestConfig()}

	probes := app.createHealthProbes()

	require.Len(t, probes, 3)
	names := make([]string, 0, len(probes))
	for _, p := range probes {
		names = append(names, p.Run(context.Background()).Name)
	}
	assert.Equal(t, []string{componentDatabase, componentMessaging, componentCache}, names)
	for _, p := range probes {
		assert.Equal(t, disabledStatus, p.Run(context.Background()).Status)
	}
}

func TestConvertCacheStatsToMap(t *testing.T) {
	t.Run("converts all fields correctly", func(t *testing.T) {
		stats := cache.ManagerStats{
			ActiveCaches: 5,
			TotalCreated: 10,
			Evictions:    2,
			IdleCleanups: 3,
			Errors:       1,
			MaxSize:      100,
			IdleTTL:      300,
		}

		result := convertCacheStatsToMap(stats)

		assert.Equal(t, 5, result["active_caches"])
		assert.Equal(t, 10, result["total_created"])
		assert.Equal(t, 2, result["evictions"])
		assert.Equal(t, 3, result["idle_cleanups"])
		assert.Equal(t, 1, result["errors"])
		assert.Equal(t, 100, result["max_size"])
		assert.Equal(t, int64(300), result["idle_ttl"])
	})

	t.Run("handles zero values", func(t *testing.T) {
		stats := cache.ManagerStats{}
		result := convertCacheStatsToMap(stats)

		assert.Equal(t, 0, result["active_caches"])
		assert.Equal(t, 0, result["total_created"])
		assert.Equal(t, 0, result["evictions"])
		assert.Equal(t, 0, result["idle_cleanups"])
		assert.Equal(t, 0, result["errors"])
		assert.Equal(t, 0, result["max_size"])
		assert.Equal(t, int64(0), result["idle_ttl"])
	})
}

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

// Fixtures used only by the per-kind descriptions above.

// stubMessagingSource fails every broker-URL resolution with err.
type stubMessagingSource struct {
	err error
}

func (s *stubMessagingSource) BrokerURL(_ context.Context, _ string) (string, error) {
	return "", s.err
}

// newMessagingManagerWithSourceError creates a messaging manager whose "" key never resolves.
func newMessagingManagerWithSourceError(t *testing.T, err error) *messaging.Manager {
	t.Helper()
	return messaging.NewMessagingManager(&stubMessagingSource{err: err}, logger.New("error", false),
		messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
	)
}

// createWarmCacheManagerWithHungPing returns a manager whose pooled instance answers PING
// only once the ping context expires — the hung-Redis case the probe's sub-budget bounds.
func createWarmCacheManagerWithHungPing(t *testing.T) *cache.CacheManager {
	t.Helper()
	return warmCacheManager(t, cachetesting.NewMockCache().WithDelay(time.Minute))
}
