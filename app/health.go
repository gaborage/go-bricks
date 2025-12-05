package app

import (
	"context"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// HealthStatus captures the outcome of a readiness probe.
type HealthStatus struct {
	Name     string
	Status   string
	Details  map[string]any
	Err      error
	Critical bool
}

// HealthProbe exposes a uniform interface for readiness probes.
type HealthProbe interface {
	Run(ctx context.Context) HealthStatus
}

type healthProbeFunc struct {
	name     string
	critical bool
	fn       func(ctx context.Context) (string, map[string]any, error)
}

func (h healthProbeFunc) Run(ctx context.Context) HealthStatus {
	status, details, err := h.fn(ctx)
	if details == nil {
		details = map[string]any{}
	}
	return HealthStatus{
		Name:     h.name,
		Status:   status,
		Details:  details,
		Err:      err,
		Critical: h.critical,
	}
}

// databaseManagerHealthProbe creates a health probe for the database manager
func databaseManagerHealthProbe(dbManager *database.DbManager, _ logger.Logger) HealthProbe {
	if dbManager == nil {
		return healthProbeFunc{
			name: "database",
			fn: func(context.Context) (string, map[string]any, error) {
				return disabledStatus, map[string]any{"status": disabledStatus}, nil
			},
		}
	}

	return healthProbeFunc{
		name:     "database",
		critical: true,
		fn: func(ctx context.Context) (string, map[string]any, error) {
			return checkDatabaseHealth(ctx, dbManager)
		},
	}
}

// checkDatabaseHealth checks database connection and health status
func checkDatabaseHealth(ctx context.Context, dbManager *database.DbManager) (status string, stats map[string]any, err error) {
	conn, err := dbManager.Get(ctx, "")
	if err != nil {
		return handleDatabaseConnectionError(err, dbManager)
	}

	if err := conn.Health(ctx); err != nil {
		dbStats := getStatsOrEmpty(dbManager.Stats())
		dbStats["status"] = "unhealthy"
		return unhealthyStatus, dbStats, err
	}

	dbStats := getStatsOrEmpty(dbManager.Stats())
	dbStats["status"] = "healthy"
	return healthyStatus, dbStats, nil
}

// handleDatabaseConnectionError handles errors when getting database connection
func handleDatabaseConnectionError(err error, dbManager *database.DbManager) (status string, stats map[string]any, e error) {
	dbStats := getStatsOrEmpty(dbManager.Stats())

	// Check if database is not configured (not a critical failure)
	if config.IsNotConfigured(err) {
		dbStats["status"] = notConfiguredStatus
		return notConfiguredStatus, dbStats, nil
	}

	// Other errors mean connection issues
	dbStats["status"] = "no_active_connections"
	return unhealthyStatus, dbStats, err
}

// getStatsOrEmpty returns stats or an empty map if stats is nil
func getStatsOrEmpty(stats map[string]any) map[string]any {
	if stats == nil {
		return map[string]any{}
	}
	return stats
}

// messagingManagerHealthProbe creates a health probe for the messaging manager
func messagingManagerHealthProbe(msgManager *messaging.Manager, _ logger.Logger) HealthProbe {
	if msgManager == nil {
		return healthProbeFunc{
			name: "messaging",
			fn: func(context.Context) (string, map[string]any, error) {
				return "disabled", map[string]any{}, nil
			},
		}
	}

	return healthProbeFunc{
		name: "messaging",
		fn: func(ctx context.Context) (string, map[string]any, error) {
			// Get manager stats
			stats := msgManager.Stats()
			if stats == nil {
				stats = map[string]any{}
			}

			// Attempt to verify readiness using an existing publisher key when available
			client, err := msgManager.Publisher(ctx, "")
			if err != nil {
				// Check if messaging is not configured (not a failure)
				if config.IsNotConfigured(err) {
					stats["status"] = notConfiguredStatus
					return notConfiguredStatus, stats, nil
				}
				// Other errors are actual failures
				stats["status"] = "connection_failed"
				return unhealthyStatus, stats, err
			}

			// Check if client is ready
			if !client.IsReady() {
				stats["status"] = "not_ready"
				return unhealthyStatus, stats, nil
			}

			if active, ok := stats["active_publishers"].(int); ok && active == 0 {
				stats["status"] = "no_active_publishers"
			} else {
				stats["status"] = healthyStatus
			}
			return healthyStatus, stats, nil
		},
	}
}

// cacheManagerHealthProbe creates a health probe for the cache manager
func cacheManagerHealthProbe(cacheManager *cache.CacheManager, _ logger.Logger) HealthProbe {
	if cacheManager == nil {
		return healthProbeFunc{
			name: "cache",
			fn: func(context.Context) (string, map[string]any, error) {
				return disabledStatus, map[string]any{"status": disabledStatus}, nil
			},
		}
	}

	return healthProbeFunc{
		name: "cache",
		fn: func(ctx context.Context) (string, map[string]any, error) {
			// Get manager stats and convert to map
			stats := convertCacheStatsToMap(cacheManager.Stats())

			// Attempt to verify readiness by getting cache instance
			_, err := cacheManager.Get(ctx, "")
			if err != nil {
				// Check if cache is not configured (not a failure)
				if config.IsNotConfigured(err) {
					stats["status"] = notConfiguredStatus
					return notConfiguredStatus, stats, nil
				}
				// Other errors are actual failures
				stats["status"] = "connection_failed"
				return unhealthyStatus, stats, err
			}

			// Cache manager is healthy if we can get an instance
			stats["status"] = healthyStatus
			return healthyStatus, stats, nil
		},
	}
}

// convertCacheStatsToMap converts cache.ManagerStats struct to map for health probe
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
