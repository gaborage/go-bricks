package app

import (
	"context"

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
			conn, err := dbManager.Get(ctx, "")
			if err != nil {
				stats := dbManager.Stats()
				if stats == nil {
					stats = map[string]any{}
				}
				stats["status"] = "no active connections"
				return healthyStatus, stats, err
			}

			if err := conn.Health(ctx); err != nil {
				stats := dbManager.Stats()
				if stats == nil {
					stats = map[string]any{}
				}
				return unhealthyStatus, stats, err
			}

			stats := dbManager.Stats()
			if stats == nil {
				stats = map[string]any{}
			}
			return healthyStatus, stats, nil
		},
	}
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
			if active, ok := stats["active_publishers"].(int); ok && active == 0 {
				stats["status"] = "no active publishers"
				return healthyStatus, stats, nil
			}

			// Attempt to verify readiness using an existing publisher key when available
			if client, err := msgManager.GetPublisher(ctx, ""); err == nil {
				if !client.IsReady() {
					return unhealthyStatus, stats, nil
				}
			}

			return healthyStatus, stats, nil
		},
	}
}
