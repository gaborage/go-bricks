package app

import (
	"context"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// cacheProbePingTimeout caps the warm-path PING so a hung Redis reports unhealthy instead
// of consuming the caller's whole readiness budget. See wiki/cache.md#readiness for the
// cold-poll caveat.
const cacheProbePingTimeout = 500 * time.Millisecond

// HealthStatus captures the outcome of a readiness probe.
type HealthStatus struct {
	// Name is interpolated into the unauthenticated /ready body by publicProbeError.
	// Keep it a fixed component identifier — never a tenant, host, or database name.
	Name    string
	Status  string
	Details map[string]any
	Err     error
	// PublicErr overrides the error text on the unauthenticated /ready body. Empty
	// synthesizes "<Name> unavailable"; Err never reaches that body either way.
	PublicErr string
	Critical  bool
}

// Prober exposes a uniform interface for readiness probes. SECURITY: the /ready body is
// unauthenticated, so publicProbeError never renders HealthStatus.Err — an implementation
// that wants wording other than the synthesized "<name> unavailable" sets
// HealthStatus.PublicErr, which must be a fixed string and never derived from config. The
// same constraint binds Name, which the synthesized default interpolates.
type Prober interface {
	Run(ctx context.Context) HealthStatus
}

type healthProbeFunc struct {
	name      string
	critical  bool
	publicErr string
	fn        func(ctx context.Context) (string, map[string]any, error)
}

func (h healthProbeFunc) Run(ctx context.Context) HealthStatus {
	status, details, err := h.fn(ctx)
	if details == nil {
		details = map[string]any{}
	}
	return HealthStatus{
		Name:      h.name,
		Status:    status,
		Details:   details,
		Err:       err,
		PublicErr: h.publicErr,
		Critical:  h.critical,
	}
}

// databaseManagerHealthProbe creates a health probe for the database manager.
//
// perTenant marks a deployment whose database configuration is resolved per tenant. It
// only relabels the not-configured verdict (see handleDatabaseConnectionError) — the
// probe still resolves and connects first. Deciding up-front would be wrong: multi-tenancy
// does not imply the "" key is unconfigured, and a shared-ledger deployment
// (outbox.tenancy: shared, ADR-041) resolves a real control-plane database through
// exactly that key. Short-circuiting would leave that database unprobed while /ready
// reported 200.
func databaseManagerHealthProbe(dbManager *database.DbManager, perTenant bool, _ logger.Logger) Prober {
	if dbManager == nil {
		return healthProbeFunc{
			name: componentDatabase,
			fn: func(context.Context) (string, map[string]any, error) {
				return disabledStatus, map[string]any{statusKey: disabledStatus}, nil
			},
		}
	}

	return healthProbeFunc{
		name:     componentDatabase,
		critical: true,
		fn: func(ctx context.Context) (string, map[string]any, error) {
			return checkDatabaseHealth(ctx, dbManager, perTenant)
		},
	}
}

// checkDatabaseHealth checks database connection and health status
func checkDatabaseHealth(ctx context.Context, dbManager *database.DbManager, perTenant bool) (status string, stats map[string]any, err error) {
	conn, release, err := dbManager.Get(ctx, "")
	if err != nil {
		return handleDatabaseConnectionError(err, dbManager, perTenant)
	}
	defer release() // probe holds no scope; release the lease when the check returns

	if err := conn.Health(ctx); err != nil {
		dbStats := getStatsOrEmpty(dbManager.Stats())
		dbStats[statusKey] = unhealthyStatus
		return unhealthyStatus, dbStats, err
	}

	dbStats := getStatsOrEmpty(dbManager.Stats())
	dbStats[statusKey] = healthyStatus
	return healthyStatus, dbStats, nil
}

// handleDatabaseConnectionError handles errors when getting database connection
func handleDatabaseConnectionError(err error, dbManager *database.DbManager, perTenant bool) (status string, stats map[string]any, e error) {
	dbStats := getStatsOrEmpty(dbManager.Stats())

	// Check if database is not configured (not a critical failure)
	if config.IsNotConfigured(err) {
		// A per-tenant deployment whose "" key does not resolve has databases, just not
		// under this key — saying not_configured would claim it has none. Only reachable
		// once resolution has actually failed, so a shared-ledger control-plane database
		// still reports its real health.
		status := notConfiguredStatus
		if perTenant {
			status = perTenantStatus
		}
		dbStats[statusKey] = status
		return status, dbStats, nil
	}

	// Other errors mean connection issues
	dbStats[statusKey] = "no_active_connections"
	return unhealthyStatus, dbStats, err
}

// getStatsOrEmpty returns stats or an empty map if stats is nil
func getStatsOrEmpty(stats map[string]any) map[string]any {
	if stats == nil {
		return map[string]any{}
	}
	return stats
}

// componentReport resolves a component's status and stats for the /ready body,
// reporting disabled when no probe is registered for it.
func componentReport(all map[string]HealthStatus, name string) (status string, stats map[string]any) {
	result := all[name]
	if result.Status == "" {
		result.Status = disabledStatus
	}
	return result.Status, getStatsOrEmpty(result.Details)
}

// messagingManagerHealthProbe creates a health probe for the messaging manager
func messagingManagerHealthProbe(msgManager *messaging.Manager, _ logger.Logger) Prober {
	if msgManager == nil {
		return healthProbeFunc{
			name: componentMessaging,
			fn: func(context.Context) (string, map[string]any, error) {
				return disabledStatus, map[string]any{statusKey: disabledStatus}, nil
			},
		}
	}

	return healthProbeFunc{
		name: componentMessaging,
		fn: func(ctx context.Context) (string, map[string]any, error) {
			stats := msgManager.Stats()
			if stats == nil {
				stats = map[string]any{}
			}

			// Attempt to verify readiness using an existing publisher key when available
			client, release, err := msgManager.Publisher(ctx, "")
			if err != nil {
				// Check if messaging is not configured (not a failure)
				if config.IsNotConfigured(err) {
					stats[statusKey] = notConfiguredStatus
					return notConfiguredStatus, stats, nil
				}
				// Other errors are actual failures
				stats[statusKey] = "connection_failed"
				return unhealthyStatus, stats, err
			}
			defer release() // probe holds no scope; release the lease when the check returns

			if !client.IsReady() {
				stats[statusKey] = "not_ready"
				return unhealthyStatus, stats, nil
			}

			if active, ok := stats["active_publishers"].(int); ok && active == 0 {
				stats[statusKey] = "no_active_publishers"
			} else {
				stats[statusKey] = healthyStatus
			}
			return healthyStatus, stats, nil
		},
	}
}

// cacheManagerHealthProbe creates a health probe for the cache manager. A deployment
// without a cache stays non-critical, so cache.critical cannot fail its readiness.
func cacheManagerHealthProbe(cacheManager *cache.CacheManager, _ logger.Logger, critical bool) Prober {
	if cacheManager == nil {
		return healthProbeFunc{
			name: componentCache,
			fn: func(context.Context) (string, map[string]any, error) {
				return disabledStatus, map[string]any{statusKey: disabledStatus}, nil
			},
		}
	}

	return healthProbeFunc{
		name:     componentCache,
		critical: critical,
		fn: func(ctx context.Context) (string, map[string]any, error) {
			stats := convertCacheStatsToMap(cacheManager.Stats())

			// Attempt to verify readiness by getting cache instance
			instance, release, err := cacheManager.Get(ctx, "")
			if err != nil {
				// Check if cache is not configured (not a failure)
				if config.IsNotConfigured(err) {
					stats[statusKey] = notConfiguredStatus
					return notConfiguredStatus, stats, nil
				}
				// Other errors are actual failures
				stats[statusKey] = "connection_failed"
				return unhealthyStatus, stats, err
			}
			defer release() // probe holds no scope; release the lease when the check returns

			// A pooled instance is returned without a round trip, so ping it explicitly
			pingCtx, cancel := context.WithTimeout(ctx, cacheProbePingTimeout)
			defer cancel()
			if err := instance.Health(pingCtx); err != nil {
				stats[statusKey] = unhealthyStatus
				return unhealthyStatus, stats, err
			}

			stats[statusKey] = healthyStatus
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
