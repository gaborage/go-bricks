package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note: Since the health probe functions work with concrete types (*database.DbManager, *messaging.Manager),
// and mocking these would require complex setup, we focus on testing the public interface behavior
// and the healthProbeFunc implementation pattern.

const (
	testProbe = "test-probe"
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
}

func TestDatabaseManagerHealthProbe(t *testing.T) {
	mockLogger := logger.New("info", false)

	t.Run("nil database manager", func(t *testing.T) {
		probe := databaseManagerHealthProbe(nil, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "database", result.Name)
		assert.Equal(t, disabledStatus, result.Status)
		assert.Equal(t, map[string]any{"status": disabledStatus}, result.Details)
		assert.NoError(t, result.Err)
		assert.False(t, result.Critical)
	})

	// Note: Since databaseManagerHealthProbe requires *database.DbManager (concrete type),
	// and creating real DbManager instances would require complex setup,
	// we focus on testing the nil case and the internal healthProbeFunc logic.
	// The healthProbeFunc is tested separately above.
}

func TestMessagingManagerHealthProbe(t *testing.T) {
	mockLogger := logger.New("info", false)

	t.Run("nil messaging manager", func(t *testing.T) {
		probe := messagingManagerHealthProbe(nil, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "messaging", result.Name)
		assert.Equal(t, "disabled", result.Status)
		assert.Empty(t, result.Details)
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
		probe := cacheManagerHealthProbe(nil, mockLogger)
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

		probe := cacheManagerHealthProbe(cacheManager, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "cache", result.Name)
		assert.Equal(t, notConfiguredStatus, result.Status)
		assert.Equal(t, notConfiguredStatus, result.Details["status"])
		assert.NoError(t, result.Err)
	})

	t.Run("connection failed", func(t *testing.T) {
		connErr := errors.New("Redis connection refused")
		cacheManager := createTestCacheManagerWithGetError(t, connErr)

		probe := cacheManagerHealthProbe(cacheManager, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "cache", result.Name)
		assert.Equal(t, unhealthyStatus, result.Status)
		assert.Equal(t, "connection_failed", result.Details["status"])
		// The error is wrapped by the cache manager, so check if it contains the original error
		assert.ErrorContains(t, result.Err, "Redis connection refused")
	})

	t.Run("healthy cache", func(t *testing.T) {
		cacheManager := createTestCacheManager(t)

		probe := cacheManagerHealthProbe(cacheManager, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "cache", result.Name)
		assert.Equal(t, healthyStatus, result.Status)
		assert.Equal(t, healthyStatus, result.Details["status"])
		assert.NoError(t, result.Err)
		assert.Contains(t, result.Details, "active_caches")
	})
}

func TestHandleDatabaseConnectionError(t *testing.T) {
	t.Run("database not configured", func(t *testing.T) {
		notConfigErr := config.NewNotConfiguredError("database", "DATABASE_HOST", "database.host")
		dbManager := createTestDbManagerWithError(t, notConfigErr)

		status, stats, err := handleDatabaseConnectionError(notConfigErr, dbManager)

		assert.Equal(t, notConfiguredStatus, status)
		assert.Contains(t, stats, "status")
		assert.Equal(t, notConfiguredStatus, stats["status"])
		assert.NoError(t, err)
	})

	t.Run("connection error", func(t *testing.T) {
		connErr := errors.New("connection refused")
		dbManager := createTestDbManagerWithError(t, connErr)

		status, stats, err := handleDatabaseConnectionError(connErr, dbManager)

		assert.Equal(t, unhealthyStatus, status)
		assert.Contains(t, stats, "status")
		assert.Equal(t, "no_active_connections", stats["status"])
		assert.Equal(t, connErr, err)
	})

	t.Run("nil stats map", func(t *testing.T) {
		connErr := errors.New("test error")
		dbManager := createTestDbManagerWithNilStats(t)

		status, stats, err := handleDatabaseConnectionError(connErr, dbManager)

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

	t.Run("no active publishers", func(t *testing.T) {
		msgManager := createTestMessagingManagerWithStats(t, map[string]any{
			"active_publishers": 0,
		})

		probe := messagingManagerHealthProbe(msgManager, mockLogger)
		result := probe.Run(context.Background())

		assert.Equal(t, "messaging", result.Name)
		assert.Equal(t, healthyStatus, result.Status)
		assert.Equal(t, "no_active_publishers", result.Details["status"])
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

	// If we need active publishers, create them
	if activePublishers, ok := stats["active_publishers"].(int); ok && activePublishers > 0 {
		for i := 0; i < activePublishers; i++ {
			_, _ = manager.Publisher(context.Background(), "")
		}
	}

	return manager
}

// createTestCacheManagerWithGetError creates a cache manager that returns an error on Get()
func createTestCacheManagerWithGetError(t *testing.T, err error) *cache.CacheManager {
	t.Helper()
	connector := func(_ context.Context, _ string) (cache.Cache, error) {
		return nil, err
	}

	manager, createErr := cache.NewCacheManager(
		cache.ManagerConfig{
			MaxSize:         10,
			IdleTTL:         time.Hour,
			CleanupInterval: 5 * time.Minute,
		},
		connector,
	)
	require.NoError(t, createErr)
	return manager
}

// stubMessagingSourceWithError is a test stub that returns errors for BrokerURL
type stubMessagingSourceWithError struct {
	err error
}

func (s *stubMessagingSourceWithError) BrokerURL(_ context.Context, _ string) (string, error) {
	return "", s.err
}
