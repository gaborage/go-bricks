package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	cachetesting "github.com/gaborage/go-bricks/cache/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

// Note: Since the health probe functions work with concrete types (*database.DbManager, *messaging.Manager),
// and mocking these would require complex setup, we focus on testing the public interface behavior
// and the healthProbeFunc implementation pattern.

const (
	testProbe = "test-probe"

	// The shape pgconn actually returns on a failed dial: it redacts the password but
	// not the username, the database name, or the resolved internal address.
	pgconnIdentityError = "failed to connect to `user=app database=payments`: 10.0.0.5:5432 (10.0.0.5): dial error"

	// The strings /ready actually serves. Spelled out here rather than imported from a
	// production constant so the assertions pin the wire format instead of restating it.
	databaseUnavailableBody = "database unavailable"
	cacheUnavailableBody    = "cache unavailable"
)

func TestHealthProbeFuncRun(t *testing.T) {
	t.Run("successful probe with details", func(t *testing.T) {
		probe := healthProbeFunc{
			name:     testProbe,
			critical: true,
			fn: func(_ context.Context) (string, map[string]any, error) {
				return healthyStatus, map[string]any{"key": "value"}, nil
			},
		}

		result := probe.Run(context.Background())
		assert.Equal(t, testProbe, result.Name)
		assert.Equal(t, healthyStatus, result.Status)
		assert.Equal(t, map[string]any{"key": "value"}, result.Details)
		assert.NoError(t, result.Err)
		assert.True(t, result.Critical)
	})

	t.Run("probe with nil details", func(t *testing.T) {
		probe := healthProbeFunc{
			name: testProbe,
			fn: func(_ context.Context) (string, map[string]any, error) {
				return healthyStatus, nil, nil
			},
		}

		result := probe.Run(context.Background())
		assert.Equal(t, testProbe, result.Name)
		assert.Equal(t, healthyStatus, result.Status)
		assert.NotNil(t, result.Details)
		assert.Empty(t, result.Details)
		assert.NoError(t, result.Err)
		assert.False(t, result.Critical)
	})

	t.Run("probe with error", func(t *testing.T) {
		expectedError := errors.New("probe failed")
		probe := healthProbeFunc{
			name: "failing-probe",
			fn: func(_ context.Context) (string, map[string]any, error) {
				return "unhealthy", map[string]any{"error": "failed"}, expectedError
			},
		}

		result := probe.Run(context.Background())
		assert.Equal(t, "failing-probe", result.Name)
		assert.Equal(t, "unhealthy", result.Status)
		assert.Equal(t, map[string]any{"error": "failed"}, result.Details)
		assert.Equal(t, expectedError, result.Err)
	})

	t.Run("probe with a public error override", func(t *testing.T) {
		probe := healthProbeFunc{
			name:      testProbe,
			critical:  true,
			publicErr: "test-probe is degraded",
			fn: func(_ context.Context) (string, map[string]any, error) {
				return unhealthyStatus, nil, errors.New(pgconnIdentityError)
			},
		}

		result := probe.Run(context.Background())
		assert.Equal(t, "test-probe is degraded", result.PublicErr)
		assert.Equal(t, "test-probe is degraded", publicProbeError(&result),
			"an override must win over the synthesized default")
	})
}

func TestDatabaseManagerHealthProbe(t *testing.T) {
	mockLogger := logger.New("info", false)

	t.Run("nil database manager", func(t *testing.T) {
		probe := databaseManagerHealthProbe(nil, false, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "database", result.Name)
		assert.Equal(t, disabledStatus, result.Status)
		assert.Equal(t, map[string]any{"status": disabledStatus}, result.Details)
		assert.NoError(t, result.Err)
		assert.False(t, result.Critical)

		// The nil-manager variant can never produce an error and is not critical, so it
		// never reaches the /ready render path at all.
		probeFunc, ok := probe.(healthProbeFunc)
		require.True(t, ok)
		assert.False(t, probeFunc.critical)
	})

	// The configured cases live in the TestDatabaseManagerHealthProbe* functions below,
	// which drive a real DbManager over the real connector via newRealConnectorDBManager.
}

// TestDatabaseManagerHealthProbeRendersFixedPublicError pins what the constructor's probe
// contributes to the unauthenticated 503 body: it is critical, so readyCheck renders it,
// and the rendered string is the synthesized default rather than the pgconn identity
// string (`user=… database=…` plus the resolved host:port). The probe declares no
// publicErr — that it is safe anyway is the whole point of the inverted default.
func TestDatabaseManagerHealthProbeRendersFixedPublicError(t *testing.T) {
	probe := databaseManagerHealthProbe(newRealConnectorDBManager(&config.Config{}), false, logger.New("info", false))

	st := probe.Run(context.Background())
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
	probe := healthProbeFunc{
		name:     componentDatabase,
		critical: true,
		fn: func(context.Context) (string, map[string]any, error) {
			return unhealthyStatus, map[string]any{statusKey: "no_active_connections"}, driverErr
		},
	}

	result := probe.Run(context.Background())

	assert.Equal(t, databaseUnavailableBody, publicProbeError(&result))
	// /_sys/health-debug renders Err verbatim and must keep the detail operators need.
	require.ErrorIs(t, result.Err, driverErr)
	assert.Contains(t, result.Err.Error(), "user=app")
}

func TestMessagingManagerHealthProbe(t *testing.T) {
	mockLogger := logger.New("info", false)

	t.Run("nil messaging manager", func(t *testing.T) {
		probe := messagingManagerHealthProbe(nil, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "messaging", result.Name)
		assert.Equal(t, "disabled", result.Status)
		assert.Equal(t, map[string]any{"status": "disabled"}, result.Details)
		assert.NoError(t, result.Err)
		assert.False(t, result.Critical)
	})

	// Note: Since messagingManagerHealthProbe requires *messaging.Manager (concrete type),
	// and creating real Manager instances would require complex setup,
	// we focus on testing the nil case and the internal healthProbeFunc logic.
	// The healthProbeFunc is tested separately above.
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

func TestCacheManagerHealthProbe(t *testing.T) {
	mockLogger := logger.New("info", false)

	t.Run("nil cache manager", func(t *testing.T) {
		probe := cacheManagerHealthProbe(nil, mockLogger, false, false)
		result := probe.Run(context.Background())

		assert.Equal(t, "cache", result.Name)
		assert.Equal(t, disabledStatus, result.Status)
		assert.Equal(t, map[string]any{"status": disabledStatus}, result.Details)
		assert.NoError(t, result.Err)
		assert.False(t, result.Critical)
	})

	t.Run("cache not configured", func(t *testing.T) {
		notConfigErr := config.NewNotConfiguredError("cache", "CACHE_HOST", "cache.host")
		cacheManager := createTestCacheManagerWithGetError(t, notConfigErr)

		probe := cacheManagerHealthProbe(cacheManager, mockLogger, false, false)
		result := probe.Run(context.Background())

		assert.Equal(t, "cache", result.Name)
		assert.Equal(t, notConfiguredStatus, result.Status)
		assert.Equal(t, notConfiguredStatus, result.Details["status"])
		assert.NoError(t, result.Err)
	})

	t.Run("connection failed", func(t *testing.T) {
		connErr := errors.New(errorRedisDown)
		cacheManager := createTestCacheManagerWithGetError(t, connErr)

		probe := cacheManagerHealthProbe(cacheManager, mockLogger, false, false)
		result := probe.Run(context.Background())

		assert.Equal(t, "cache", result.Name)
		assert.Equal(t, unhealthyStatus, result.Status)
		assert.Equal(t, "connection_failed", result.Details["status"])
		// The error is wrapped by the cache manager, so check if it contains the original error
		assert.ErrorContains(t, result.Err, errorRedisDown)
	})

	t.Run("healthy cache", func(t *testing.T) {
		cacheManager := createTestCacheManager(t)

		probe := cacheManagerHealthProbe(cacheManager, mockLogger, false, false)
		result := probe.Run(context.Background())

		assert.Equal(t, "cache", result.Name)
		assert.Equal(t, healthyStatus, result.Status)
		assert.Equal(t, healthyStatus, result.Details["status"])
		assert.NoError(t, result.Err)
		assert.Contains(t, result.Details, "active_caches")
	})

	t.Run("critical_true_marks_failing_probe_critical", func(t *testing.T) {
		cacheManager := createTestCacheManagerWithGetError(t, errors.New(errorRedisDown))

		result := cacheManagerHealthProbe(cacheManager, mockLogger, true, false).Run(context.Background())

		assert.Equal(t, unhealthyStatus, result.Status)
		assert.Error(t, result.Err)
		assert.True(t, result.Critical)
	})

	t.Run("critical_false_marks_failing_probe_non_critical", func(t *testing.T) {
		cacheManager := createTestCacheManagerWithGetError(t, errors.New(errorRedisDown))

		result := cacheManagerHealthProbe(cacheManager, mockLogger, false, false).Run(context.Background())

		assert.Equal(t, unhealthyStatus, result.Status)
		assert.Error(t, result.Err)
		assert.False(t, result.Critical)
	})

	t.Run("critical_true_marks_healthy_probe_critical", func(t *testing.T) {
		result := cacheManagerHealthProbe(createTestCacheManager(t), mockLogger, true, false).Run(context.Background())

		assert.Equal(t, healthyStatus, result.Status)
		assert.True(t, result.Critical)
	})

	// The readiness guarantee for a cache-less deployment is pinned by
	// TestReadyCheckScenarios/cache_disabled_stays_ready_when_critical, which goes through
	// createHealthProbes; this only pins the nil branch's own contract.
	t.Run("nil_cache_manager_ignores_critical", func(t *testing.T) {
		result := cacheManagerHealthProbe(nil, mockLogger, true, false).Run(context.Background())

		assert.Equal(t, disabledStatus, result.Status)
		assert.False(t, result.Critical)
	})
}

// TestCacheProbeReportsWarmPoolOutage pins the instance.Health(ctx) call in
// cacheManagerHealthProbe: CacheManager.Get serves a warm pool from memory, so without that
// call a Redis outage that starts after instance creation stays invisible forever (#860).
func TestCacheProbeReportsWarmPoolOutage(t *testing.T) {
	mockLogger := logger.New("info", false)

	mc := cachetesting.NewMockCache()
	var connectorCalls atomic.Int32
	cacheManager := createTestCacheManagerWithConnector(t, func(context.Context, string) (cache.Cache, error) {
		connectorCalls.Add(1)
		return mc, nil
	})
	t.Cleanup(func() { assert.NoError(t, cacheManager.Close()) })

	probe := cacheManagerHealthProbe(cacheManager, mockLogger, false, false)

	warm := probe.Run(context.Background())
	require.Equal(t, healthyStatus, warm.Status)
	require.NoError(t, warm.Err)
	require.Equal(t, int32(1), connectorCalls.Load())

	mc.WithHealthFailure(errors.New(errorRedisDown))

	result := probe.Run(context.Background())

	assert.Equal(t, unhealthyStatus, result.Status)
	assert.ErrorContains(t, result.Err, errorRedisDown)
	assert.Equal(t, unhealthyStatus, result.Details["status"])
	assert.Equal(t, int32(1), connectorCalls.Load(), "probe must reuse the pooled instance, not re-create it")
	assert.Equal(t, int64(2), mc.OperationCount("Health"), "probe must ping the pooled instance on every run")
}

// TestCacheProbeBoundsHungPing pins the sub-budget on the per-probe PING: a Redis that drops
// packets must not consume the caller's whole readiness deadline (#860).
func TestCacheProbeBoundsHungPing(t *testing.T) {
	mockLogger := logger.New("info", false)

	mc := cachetesting.NewMockCache().WithDelay(time.Minute)
	cacheManager := cacheManagerServing(t, mc)

	probe := cacheManagerHealthProbe(cacheManager, mockLogger, false, false)

	// Parent budget mirrors the default server.timeout.middleware.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	result := probe.Run(ctx)
	elapsed := time.Since(start)

	assert.Equal(t, unhealthyStatus, result.Status)
	assert.ErrorIs(t, result.Err, context.DeadlineExceeded)
	assert.Equal(t, unhealthyStatus, result.Details["status"])
	assert.Less(t, elapsed, 2*cacheProbePingTimeout, "probe must cap the ping instead of burning the request budget")
	assert.NoError(t, ctx.Err(), "the parent budget must survive the probe")
}

// TestCacheProbePingHonorsCallerContext pins that the ping derives from the caller's context:
// a probe rooted at context.Background() would ignore an already-spent request budget.
func TestCacheProbePingHonorsCallerContext(t *testing.T) {
	mockLogger := logger.New("info", false)

	mc := cachetesting.NewMockCache().WithDelay(10 * time.Millisecond)
	cacheManager := cacheManagerServing(t, mc)

	probe := cacheManagerHealthProbe(cacheManager, mockLogger, false, false)

	// Warm the pool so the canceled context reaches Health rather than the create path.
	require.Equal(t, healthyStatus, probe.Run(context.Background()).Status)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := probe.Run(ctx)

	assert.Equal(t, unhealthyStatus, result.Status)
	assert.ErrorIs(t, result.Err, context.Canceled)
}

func TestCacheProbeReleasesLease(t *testing.T) {
	mockLogger := logger.New("info", false)

	tests := []struct {
		name       string
		healthErr  error
		wantStatus string
	}{
		{name: "healthy_path", healthErr: nil, wantStatus: healthyStatus},
		{name: "health_failure_path", healthErr: errors.New(errorRedisDown), wantStatus: unhealthyStatus},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc := cachetesting.NewMockCache().WithHealthFailure(tc.healthErr)
			cacheManager := cacheManagerServing(t, mc)

			result := cacheManagerHealthProbe(cacheManager, mockLogger, false, false).Run(context.Background())
			require.Equal(t, tc.wantStatus, result.Status)

			// Remove closes the instance only when no lease is outstanding.
			require.NoError(t, cacheManager.Remove(""))
			assert.True(t, mc.IsClosed(), "probe must release its lease before returning")
		})
	}
}

// TestCacheProbeSkipsLeaseWhenCacheAbsent pins that absent=true short-circuits before the
// doomed lease: the connector must never be called, and the pool's errors counter must
// stay flat across repeated polls (rootCacheAbsent).
func TestCacheProbeSkipsLeaseWhenCacheAbsent(t *testing.T) {
	mockLogger := logger.New("info", false)

	var connectorCalls atomic.Int32
	cacheManager := createTestCacheManagerWithConnector(t, func(context.Context, string) (cache.Cache, error) {
		connectorCalls.Add(1)
		return nil, config.NewNotConfiguredError("cache", "CACHE_REDIS_HOST", "cache.redis.host")
	})
	t.Cleanup(func() { assert.NoError(t, cacheManager.Close()) })

	probe := cacheManagerHealthProbe(cacheManager, mockLogger, false, true)

	before := cacheManager.Stats().Errors

	for i := 0; i < 3; i++ {
		result := probe.Run(context.Background())
		assert.Equal(t, notConfiguredStatus, result.Status)
		assert.Equal(t, notConfiguredStatus, result.Details["status"])
		assert.NoError(t, result.Err)
	}

	assert.Equal(t, before, cacheManager.Stats().Errors, "the pool's errors counter must not grow")
	assert.Equal(t, int32(0), connectorCalls.Load(), "the connector must never be reached")
}

// TestCacheProbeStillLeasesWhenCachePresent pins that the short-circuit does not eat the
// real probe: with absent=false a genuine connection failure still leases, still counts,
// and still surfaces as unhealthy.
func TestCacheProbeStillLeasesWhenCachePresent(t *testing.T) {
	mockLogger := logger.New("info", false)

	var connectorCalls atomic.Int32
	cacheManager := createTestCacheManagerWithConnector(t, func(context.Context, string) (cache.Cache, error) {
		connectorCalls.Add(1)
		return nil, errors.New(errorRedisDown)
	})
	t.Cleanup(func() { assert.NoError(t, cacheManager.Close()) })

	probe := cacheManagerHealthProbe(cacheManager, mockLogger, false, false)

	result := probe.Run(context.Background())

	assert.Equal(t, unhealthyStatus, result.Status)
	assert.Equal(t, "connection_failed", result.Details["status"])
	assert.ErrorContains(t, result.Err, errorRedisDown)
	assert.Equal(t, int32(1), connectorCalls.Load())
}

func TestHandleDatabaseConnectionError(t *testing.T) {
	t.Run("database not configured", func(t *testing.T) {
		notConfigErr := config.NewNotConfiguredError("database", "DATABASE_HOST", "database.host")
		dbManager := createTestDbManagerWithError(t, notConfigErr)

		status, stats, err := handleDatabaseConnectionError(notConfigErr, dbManager, false)

		assert.Equal(t, notConfiguredStatus, status)
		assert.Contains(t, stats, "status")
		assert.Equal(t, notConfiguredStatus, stats["status"])
		assert.NoError(t, err)
	})

	t.Run("connection error", func(t *testing.T) {
		connErr := errors.New(testutil.TestConnectionRefused)
		dbManager := createTestDbManagerWithError(t, connErr)

		status, stats, err := handleDatabaseConnectionError(connErr, dbManager, false)

		assert.Equal(t, unhealthyStatus, status)
		assert.Contains(t, stats, "status")
		assert.Equal(t, "no_active_connections", stats["status"])
		assert.Equal(t, connErr, err)
	})

	t.Run("nil stats map", func(t *testing.T) {
		connErr := errors.New(testutil.TestError)
		dbManager := createTestDbManagerWithNilStats(t)

		status, stats, err := handleDatabaseConnectionError(connErr, dbManager, false)

		assert.NotNil(t, stats)
		assert.Equal(t, "no_active_connections", stats["status"])
		assert.Equal(t, unhealthyStatus, status)
		assert.Equal(t, connErr, err)
	})
}

func TestGetStatsOrEmpty(t *testing.T) {
	t.Run("nil stats", func(t *testing.T) {
		result := getStatsOrEmpty(nil)

		assert.NotNil(t, result)
		assert.Empty(t, result)
	})

	t.Run("non-nil stats", func(t *testing.T) {
		input := map[string]any{"key": "value"}
		result := getStatsOrEmpty(input)

		assert.Equal(t, input, result)
	})
}

func TestMessagingManagerHealthProbeDetailed(t *testing.T) {
	mockLogger := logger.New("info", false)

	t.Run("messaging not configured", func(t *testing.T) {
		notConfigErr := config.NewNotConfiguredError("messaging", "MESSAGING_BROKER_URL", "messaging.broker.url")
		msgManager := createTestMessagingManagerWithGetPublisherError(t, notConfigErr)

		probe := messagingManagerHealthProbe(msgManager, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "messaging", result.Name)
		assert.Equal(t, notConfiguredStatus, result.Status)
		assert.Equal(t, notConfiguredStatus, result.Details["status"])
		assert.NoError(t, result.Err)
	})

	t.Run("connection failed", func(t *testing.T) {
		connErr := errors.New("AMQP connection refused")
		msgManager := createTestMessagingManagerWithGetPublisherError(t, connErr)

		probe := messagingManagerHealthProbe(msgManager, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "messaging", result.Name)
		assert.Equal(t, unhealthyStatus, result.Status)
		assert.Equal(t, "connection_failed", result.Details["status"])
		assert.Equal(t, connErr, result.Err)
	})

	t.Run("client not ready", func(t *testing.T) {
		msgManager := createTestMessagingManagerWithNotReadyClient(t)

		probe := messagingManagerHealthProbe(msgManager, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "messaging", result.Name)
		assert.Equal(t, unhealthyStatus, result.Status)
		assert.Equal(t, "not_ready", result.Details["status"])
		assert.NoError(t, result.Err)
	})

	t.Run("publisher_pool_cold_reports_healthy", func(t *testing.T) {
		msgManager := createTestMessagingManagerWithStats(t, map[string]any{
			"active_publishers": 0,
		})

		probe := messagingManagerHealthProbe(msgManager, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "messaging", result.Name)
		assert.Equal(t, healthyStatus, result.Status)
		assert.Equal(t, healthyStatus, result.Details["status"])
		assert.Equal(t, 1, result.Details["active_publishers"],
			"the probe must re-read stats after acquiring, or the body reports 0 publishers beside a healthy verdict")
		assert.NoError(t, result.Err)
	})

	t.Run("healthy with active publishers", func(t *testing.T) {
		msgManager := createTestMessagingManagerWithStats(t, map[string]any{
			"active_publishers": 3,
		})

		probe := messagingManagerHealthProbe(msgManager, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "messaging", result.Name)
		assert.Equal(t, healthyStatus, result.Status)
		assert.Equal(t, healthyStatus, result.Details["status"])
		// The manager returns its actual stats, so active_publishers should be present and greater than 0
		activePublishers, ok := result.Details["active_publishers"].(int)
		assert.True(t, ok, "active_publishers should be present in stats")
		assert.Greater(t, activePublishers, 0, "should have at least one active publisher")
		assert.NoError(t, result.Err)
	})
}

// Helper functions for creating test managers with various error scenarios

// createTestDbManagerWithNilStats creates a database manager with a mock that returns nil stats
func createTestDbManagerWithNilStats(t *testing.T) *database.DbManager {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "localhost",
			Port: 5432,
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("error", false)

	mockDB := &testmocks.MockDatabase{}
	mockDB.On("Stats").Return(nil, nil)

	return database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return mockDB, nil
		},
	)
}

// createTestMessagingManagerWithGetPublisherError creates a messaging manager that returns an error on GetPublisher()
func createTestMessagingManagerWithGetPublisherError(t *testing.T, err error) *messaging.Manager {
	t.Helper()

	// Create a resource source that returns the error when BrokerURL is called
	source := &stubMessagingSourceWithError{err: err}
	log := logger.New("error", false)

	return messaging.NewMessagingManager(source, log,
		messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
	)
}

// createTestMessagingManagerWithNotReadyClient creates a messaging manager with a client that reports not ready
func createTestMessagingManagerWithNotReadyClient(t *testing.T) *messaging.Manager {
	t.Helper()
	cfg := &config.Config{
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("error", false)

	// Create a mock client that reports not ready
	mockClient := testmocks.NewMockAMQPClient()
	mockClient.SetReady(false)

	return messaging.NewMessagingManager(resourceSource, log,
		messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return mockClient
		},
	)
}

// createTestMessagingManagerWithStats creates a messaging manager that returns specific stats
func createTestMessagingManagerWithStats(t *testing.T, stats map[string]any) *messaging.Manager {
	t.Helper()
	cfg := &config.Config{
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("error", false)

	// Create a manager and pre-populate it to get the desired stats
	manager := messaging.NewMessagingManager(resourceSource, log,
		messaging.ManagerOptions{MaxPublishers: 10, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
	)

	// If we need active publishers, create them. Hold the leases until the test ends (via
	// t.Cleanup) so the publishers stay active for assertions, then release them — discarding
	// the ReleaseFunc would pin refs and could mask the lease lifecycle this PR validates.
	if activePublishers, ok := stats["active_publishers"].(int); ok && activePublishers > 0 {
		releases := make([]func(), 0, activePublishers)
		for i := 0; i < activePublishers; i++ {
			_, release, err := manager.Publisher(context.Background(), "")
			require.NoError(t, err)
			releases = append(releases, release)
		}
		t.Cleanup(func() {
			for _, release := range releases {
				release()
			}
		})
	}

	return manager
}

// cacheManagerServing returns a cache manager whose connector always serves c, registering
// t.Cleanup to close it so callers don't repeat the connector + cleanup boilerplate.
func cacheManagerServing(t *testing.T, c cache.Cache) *cache.CacheManager {
	t.Helper()
	manager := createTestCacheManagerWithConnector(t, func(context.Context, string) (cache.Cache, error) {
		return c, nil
	})
	t.Cleanup(func() { assert.NoError(t, manager.Close()) })
	return manager
}

// createTestCacheManagerWithGetError creates a cache manager that returns an error on Get()
func createTestCacheManagerWithGetError(t *testing.T, err error) *cache.CacheManager {
	t.Helper()
	return createTestCacheManagerWithConnector(t, func(context.Context, string) (cache.Cache, error) {
		return nil, err
	})
}

// createWarmCacheManagerWithOutage returns a manager whose instance was already created and
// pooled before the backend went down — the #860 case a lease-only probe cannot see.
func createWarmCacheManagerWithOutage(t *testing.T) *cache.CacheManager {
	t.Helper()
	mc := cachetesting.NewMockCache()
	manager := cacheManagerServing(t, mc)

	_, release, err := manager.Get(context.Background(), "")
	require.NoError(t, err)
	release()

	// The shape redis.Client.Health actually returns on a live outage — it names the address,
	// which is what the sanitized /ready body must withhold.
	mc.WithHealthFailure(cache.NewConnectionError("ping", redisProbeAddress, errors.New(errorRedisDown)))
	return manager
}

// stubMessagingSourceWithError is a test stub that returns errors for BrokerURL
type stubMessagingSourceWithError struct {
	err error
}

func (s *stubMessagingSourceWithError) BrokerURL(_ context.Context, _ string) (string, error) {
	return "", s.err
}

// newRealConnectorDBManager builds a DbManager over the REAL config.TenantStore and the
// REAL database.NewConnection (nil connector). Every other app-level fixture injects a
// stub connector, which is exactly how #872 shipped: the defect lived in the seam
// between the config resolver and the connection factory, and a stub replaces that seam.
func newRealConnectorDBManager(cfg *config.Config) *database.DbManager {
	return database.NewDbManager(
		config.NewTenantStore(cfg),
		logger.New("info", false),
		database.DbManagerOptions{},
		nil,
	)
}

func TestDatabaseManagerHealthProbeReportsNotConfigured(t *testing.T) {
	log := logger.New("info", false)

	result := databaseManagerHealthProbe(newRealConnectorDBManager(&config.Config{}), false, log).
		Run(context.Background())

	assert.Equal(t, notConfiguredStatus, result.Status)
	assert.NoError(t, result.Err, "an absent database is not a readiness failure")
	assert.Equal(t, notConfiguredStatus, result.Details[statusKey])
	// Criticality is retained deliberately: a database that IS configured and down must
	// still fail readiness. Absence is handled by the status, never by demoting the probe.
	assert.True(t, result.Critical)
}

func TestDatabaseManagerHealthProbeStaysUnhealthyForUnsupportedType(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.Type = "mysql"
	cfg.Database.Host = "db.internal"

	result := databaseManagerHealthProbe(newRealConnectorDBManager(cfg), false, logger.New("info", false)).
		Run(context.Background())

	assert.Equal(t, unhealthyStatus, result.Status)
	require.Error(t, result.Err)
	// The other half of the fix: a type the operator actually asked for is a
	// misconfiguration, and must never be softened into "intentionally absent".
	assert.False(t, config.IsNotConfigured(result.Err))
}

func TestDatabaseManagerHealthProbeReportsPerTenantWhenDefaultKeyIsUnconfigured(t *testing.T) {
	cfg := &config.Config{}
	cfg.Multitenant.Enabled = true // no root database block: tenants carry their own

	result := databaseManagerHealthProbe(newRealConnectorDBManager(cfg), true, logger.New("info", false)).
		Run(context.Background())

	// not_configured would claim the service has no database — false when it has N
	// tenant databases that this fixed-key probe simply never covered.
	assert.Equal(t, perTenantStatus, result.Status)
	assert.NoError(t, result.Err)
}

func TestDatabaseManagerHealthProbeStillProbesPerTenantControlPlaneDatabase(t *testing.T) {
	// Multi-tenancy does NOT imply the "" key is unconfigured: a shared-ledger
	// deployment (outbox.tenancy: shared, ADR-041) resolves a real control-plane
	// database through exactly that key. Relabeling to per_tenant before resolving
	// would leave that database unprobed while /ready reported 200 — this test is what
	// catches that.
	cfg := &config.Config{}
	cfg.Multitenant.Enabled = true
	cfg.Database.Type = "mysql" // resolves, then fails to connect
	cfg.Database.Host = "control-plane.internal"

	result := databaseManagerHealthProbe(newRealConnectorDBManager(cfg), true, logger.New("info", false)).
		Run(context.Background())

	assert.Equal(t, unhealthyStatus, result.Status, "a resolvable control-plane database must be probed, not relabeled")
	require.Error(t, result.Err)
	assert.True(t, result.Critical, "and it must still gate readiness")
}
