package app

import (
	"context"
	"encoding/json"
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

// The four per-kind description helpers below stand where the four probe constructors
// stood: each kind's description is now built by its own slot (ADR-067 locality), so the
// tests ask the slot for it with exactly the inputs the constructor used to take.

func describingApp() *App {
	return &App{cfg: &config.Config{}}
}

// describedRow is the row a kind's description is asked under: per-tenant or single-tenant,
// with "" known absent or known present.
func describedRow(kind string, perTenant, absent bool) kindPlan {
	row := kindPlan{kind: kind, presence: keyPresent}
	if perTenant {
		row.tenancy = perTenantTenancy
	}
	if absent {
		row.presence = keyAbsent
	}
	return row
}

func databaseDescription(t *testing.T, m *database.DbManager, perTenant bool) probeDescription {
	t.Helper()
	a := describingApp()
	a.dbManager = m
	a.installSlots(resourcePlan{database: describedRow(componentDatabase, perTenant, false)})
	return slotDescription(t, a, componentDatabase)
}

func messagingDescription(t *testing.T, m *messaging.Manager, perTenant bool) probeDescription {
	t.Helper()
	a := describingApp()
	a.messagingManager = m
	a.installSlots(resourcePlan{messaging: describedRow(componentMessaging, perTenant, false)})
	return slotDescription(t, a, componentMessaging)
}

func cacheDescription(t *testing.T, m *cache.CacheManager, critical, absent, perTenant bool) probeDescription {
	t.Helper()
	a := describingApp()
	a.cfg.Cache.Critical = critical
	a.cacheManager = m
	a.installSlots(resourcePlan{cache: describedRow(componentCache, perTenant, absent)})
	return slotDescription(t, a, componentCache)
}

func streamsDescription(t *testing.T, m streamHandle) probeDescription {
	t.Helper()
	a := describingApp()
	a.streamsManager = m
	a.installSlots(fixturePlan(a.cfg))
	return slotDescription(t, a, componentStreams)
}

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
			assert.Equal(t, tt.wantAcquired, stub.acquired, "lease attempts")
			assert.Equal(t, tt.wantReleased, stub.released, "lease releases")
			if tt.wantReleased == 1 {
				assert.Equal(t, 1, stub.statsBefore, "stats snapshot is taken while the lease is held")
			}
			if tt.stub.stats != nil {
				assert.Equal(t, 1, got.Details["active"], "kind statistics are carried into details")
			}
			if tt.wantErr != nil {
				require.ErrorIs(t, got.Err, tt.wantErr)
			} else {
				require.NoError(t, got.Err)
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

	got := databaseDescription(t, m, false).Run(context.Background())

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

	got := databaseDescription(t, m, false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, got.Status)
	assert.EqualError(t, got.Err, "pg down")
}

// TestDatabaseProbeKeepsConnectionIdentityOnErrAlone pins the split the readiness module
// performs. SECURITY: the description is critical, so a failure gates /ready — whose body
// carries its verdict alone (ADR-120) — while the full identity-bearing driver error
// (`user=… database=…` plus the resolved host:port) stays on HealthStatus.Err for the app
// log and the IP-allowlisted /_sys/health-debug.
func TestDatabaseProbeKeepsConnectionIdentityOnErrAlone(t *testing.T) {
	require.True(t, databaseDescription(t, newRealConnectorDBManager(&config.Config{}), false).critical,
		"a non-critical probe would never gate /ready at all")

	driverErr := errors.New(pgconnIdentityError)
	probe := probeDescription{
		name:     componentDatabase,
		critical: true,
		live:     func(context.Context) error { return driverErr },
	}

	result := probe.Run(context.Background())

	require.ErrorIs(t, result.Err, driverErr)
	assert.Contains(t, result.Err.Error(), "user=app")

	// The premise the split rests on: the 503 the gate would serve is rendered without ever
	// reading the result, so the driver error has nowhere to reach.
	rendered, err := json.Marshal(notReadyBody())
	require.NoError(t, err)
	assert.NotContains(t, string(rendered), "user=app")
}

func TestDatabaseProbeReportsNotConfigured(t *testing.T) {
	result := databaseDescription(t, newRealConnectorDBManager(&config.Config{}), false).Run(context.Background())

	assert.Equal(t, notConfiguredStatus, result.Status)
	assert.Equal(t, notConfiguredStatus, result.Details[statusKey])
	// Criticality is retained deliberately: a database that IS configured and down must
	// still fail readiness. Absence is handled by the status, never by demoting the probe.
	assert.True(t, result.Critical)
	require.NoError(t, result.Err, "an absent database is not a readiness failure")
}

func TestDatabaseProbeStaysUnhealthyForUnsupportedType(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.Type = "mysql"
	cfg.Database.Host = "db.internal"

	result := databaseDescription(t, newRealConnectorDBManager(cfg), false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, result.Status)
	require.Error(t, result.Err)
	// The other half of the fix: a type the operator actually asked for is a
	// misconfiguration, and must never be softened into "intentionally absent".
	assert.False(t, config.IsNotConfigured(result.Err))
}

func TestDatabaseProbeReportsPerTenantWhenDefaultKeyIsUnconfigured(t *testing.T) {
	cfg := &config.Config{}
	cfg.Multitenant.Enabled = true // no root database block: tenants carry their own

	result := databaseDescription(t, newRealConnectorDBManager(cfg), true).Run(context.Background())

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

	result := databaseDescription(t, newRealConnectorDBManager(cfg), true).Run(context.Background())

	assert.Equal(t, unhealthyStatus, result.Status, "a resolvable control-plane database must be probed, not relabeled")
	require.Error(t, result.Err)
	assert.True(t, result.Critical, "and it must still gate readiness")
}

func TestMessagingProbeNotReadyIsUnhealthyWithError(t *testing.T) {
	m := createTestMessagingManagerWithNotReadyClient(t)

	got := messagingDescription(t, m, false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, got.Status)
	assert.False(t, got.Critical, "messaging is not critical without messaging.consumers.critical")
	require.ErrorIs(t, got.Err, errPublisherNotReady)
}

func TestMessagingProbeCountsItsOwnPublisher(t *testing.T) {
	m := createTestMessagingManager(t)

	got := messagingDescription(t, m, false).Run(context.Background())

	assert.Equal(t, healthyStatus, got.Status)
	assert.Equal(t, 1, got.Details["active_publishers"], "stats are read while the probe's own lease is held")
}

// TestMessagingProbeReportsPerTenantWhenDefaultKeyIsUnconfigured pins the widened relabel:
// per_tenant is no longer a database-only verdict, so a multi-tenant deployment whose ""
// key resolves no broker reports per_tenant rather than claiming it has no messaging.
func TestMessagingProbeReportsPerTenantWhenDefaultKeyIsUnconfigured(t *testing.T) {
	m := newMessagingManagerWithSourceError(t,
		config.NewNotConfiguredError("messaging", "MESSAGING_BROKER_URL", "messaging.broker.url"))

	got := messagingDescription(t, m, true).Run(context.Background())

	assert.Equal(t, perTenantStatus, got.Status)
	assert.Equal(t, perTenantStatus, got.Details[statusKey])
	assert.NoError(t, got.Err)
}

// TestMessagingProbeLabelFollowsItsRow takes the row as given: a not-configured "" reads
// per_tenant only where the messaging row relabels it, which under shared tenancy is D8.
func TestMessagingProbeLabelFollowsItsRow(t *testing.T) {
	sharedMT := &config.Config{
		Multitenant: config.MultitenantConfig{Enabled: true},
		Messaging:   config.MessagingConfig{Tenancy: config.TenancyShared},
	}
	for _, tt := range []struct {
		name string
		row  kindPlan
		want string
	}{
		{name: "single_tenant_row", row: describedRow(componentMessaging, false, false), want: notConfiguredStatus},
		{name: "shared_row", row: fixturePlan(sharedMT).messaging, want: perTenantStatus},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := describingApp()
			a.messagingManager = newMessagingManagerWithSourceError(t,
				config.NewNotConfiguredError("messaging", "MESSAGING_BROKER_URL", "messaging.broker.url"))
			a.installSlots(resourcePlan{messaging: tt.row})

			got := slotDescription(t, a, componentMessaging).Run(context.Background())

			assert.Equal(t, tt.want, got.Status)
		})
	}
}

// TestCacheProbeBoundsTheWarmPathPing pins the sub-budget on the warm-path PING: a pooled
// cache whose Health hangs must report unhealthy within cacheProbePingTimeout rather than
// consume the caller's whole readiness budget (#860 regression pin).
func TestCacheProbeBoundsTheWarmPathPing(t *testing.T) {
	m := createWarmCacheManagerWithHungPing(t)

	start := time.Now()
	got := cacheDescription(t, m, true, false, false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, got.Status)
	assert.Less(t, time.Since(start), cacheProbePingTimeout+200*time.Millisecond)
	require.Error(t, got.Err)
}

func TestCacheProbeAbsentNeverLeases(t *testing.T) {
	m := createTestCacheManagerWithGetError(t, errors.New("must not be called"))

	got := cacheDescription(t, m, true, true, false).Run(context.Background())

	assert.Equal(t, notConfiguredStatus, got.Status)
	assert.Contains(t, got.Details, "active_caches", "manager counters still render")
	require.NoError(t, got.Err)
}

// TestCacheProbeReportsPerTenantWhenDefaultKeyIsUnconfigured is the cache half of the
// widened relabel: the kinds share one rule, so a per-tenant cache reports per_tenant.
func TestCacheProbeReportsPerTenantWhenDefaultKeyIsUnconfigured(t *testing.T) {
	m := createTestCacheManagerWithGetError(t,
		config.NewNotConfiguredError("cache", "CACHE_REDIS_HOST", "cache.redis.host"))

	got := cacheDescription(t, m, false, false, true).Run(context.Background())

	assert.Equal(t, perTenantStatus, got.Status)
	assert.Equal(t, perTenantStatus, got.Details[statusKey])
	assert.NoError(t, got.Err)
}

// TestCacheProbePingHonorsCallerContext pins that the ping derives from the caller's context:
// a probe rooted at context.Background() would ignore an already-spent request budget.
func TestCacheProbePingHonorsCallerContext(t *testing.T) {
	mc := cachetesting.NewMockCache().WithDelay(10 * time.Millisecond)
	probe := cacheDescription(t, cacheManagerServing(t, mc), false, false, false)

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

	got := streamsDescription(t, m).Run(context.Background())

	assert.Equal(t, componentStreams, got.Name)
	assert.Equal(t, unhealthyStatus, got.Status)
	assert.False(t, got.Critical, "a reconnecting stream consumer must not 503 the whole service")
	assert.Contains(t, got.Details, "stored_offsets")
	require.ErrorIs(t, got.Err, errStreamsNotOpen)
}

func TestConvertCacheStatsToMap(t *testing.T) {
	t.Run("converts all fields correctly", func(t *testing.T) {
		stats := cache.ManagerStats{
			ActiveCaches: 5,
			TotalCreated: 10,
			Evictions:    2,
			Removals:     4,
			IdleCleanups: 3,
			Errors:       1,
			MaxSize:      100,
			IdleTTL:      300,
		}

		result := convertCacheStatsToMap(stats)

		assert.Equal(t, 5, result["active_caches"])
		assert.Equal(t, 10, result["total_created"])
		assert.Equal(t, 2, result["evictions"])
		assert.Equal(t, 4, result["removals"])
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
		assert.Equal(t, 0, result["removals"])
		assert.Equal(t, 0, result["idle_cleanups"])
		assert.Equal(t, 0, result["errors"])
		assert.Equal(t, 0, result["max_size"])
		assert.Equal(t, int64(0), result["idle_ttl"])
	})
}

// connectionsStatsKey is DbManager.Stats()' per-connection array: the one counter whose
// values are resourcepool keys — tenant IDs in a multi-tenant deployment. Spelled out rather
// than imported so the assertions pin the manager's own key instead of restating a value
// this package derives from it.
const connectionsStatsKey = "connections"

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
