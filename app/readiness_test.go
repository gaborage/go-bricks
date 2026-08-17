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
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
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
	m := newDBManagerFor(t, db)

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
	m := createTestMessagingManagerWithNotReadyClient(t)

	got := messagingProbe(m, false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, got.Status)
	assert.ErrorIs(t, got.Err, errPublisherNotReady)
	assert.False(t, got.Critical, "messaging is never critical")
}

func TestMessagingProbeCountsItsOwnPublisher(t *testing.T) {
	m := createTestMessagingManagerWithStats(t, nil)

	got := messagingProbe(m, false).Run(context.Background())

	assert.Equal(t, healthyStatus, got.Status)
	assert.Equal(t, 1, got.Details["active_publishers"], "stats are read while the probe's own lease is held")
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
	names := []string{}
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
