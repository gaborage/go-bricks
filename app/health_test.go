package app

import (
	"context"
	"errors"
	"testing"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/logger"
	"github.com/stretchr/testify/assert"
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

	// Note: Since cacheManagerHealthProbe requires *cache.CacheManager (concrete type),
	// and creating real CacheManager instances would require complex setup,
	// we focus on testing the nil case and the internal healthProbeFunc logic.
	// The healthProbeFunc is tested separately above.
}
