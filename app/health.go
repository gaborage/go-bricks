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

func createHealthProbes(db database.Interface, msg messaging.Client, log logger.Logger) []HealthProbe {
	probes := []HealthProbe{databaseHealthProbe(db, log)}
	probes = append(probes, messagingHealthProbe(msg))
	return probes
}

func databaseHealthProbe(db database.Interface, log logger.Logger) HealthProbe {
	if db == nil {
		return healthProbeFunc{
			name: "database",
			fn: func(context.Context) (string, map[string]any, error) {
				return "disabled", map[string]any{"status": "disabled"}, nil
			},
		}
	}

	return healthProbeFunc{
		name:     "database",
		critical: true,
		fn: func(ctx context.Context) (string, map[string]any, error) {
			if err := db.Health(ctx); err != nil {
				return "unhealthy", map[string]any{}, err
			}

			stats, err := db.Stats()
			if err != nil {
				log.Error().Err(err).Msg("Failed to get database stats")
				stats = map[string]any{"error": err.Error()}
			}

			if stats == nil {
				stats = map[string]any{}
			}

			return "healthy", stats, nil
		},
	}
}

func messagingHealthProbe(msg messaging.Client) HealthProbe {
	if msg == nil {
		return healthProbeFunc{
			name: "messaging",
			fn: func(context.Context) (string, map[string]any, error) {
				return "disabled", map[string]any{}, nil
			},
		}
	}

	return healthProbeFunc{
		name: "messaging",
		fn: func(context.Context) (string, map[string]any, error) {
			if msg.IsReady() {
				return "healthy", map[string]any{}, nil
			}
			return "unhealthy", map[string]any{}, nil
		},
	}
}
