package app

import (
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	unknownStatus = "unknown"
)

// HealthDebugInfo contains enhanced health information for debugging
type HealthDebugInfo struct {
	Components map[string]ComponentHealth `json:"components"`
	Summary    HealthSummary              `json:"summary"`
	App        Info                       `json:"app"`
}

// ComponentHealth contains detailed health information for a component
type ComponentHealth struct {
	Status   string         `json:"status"`
	Critical bool           `json:"critical"`
	Error    string         `json:"error,omitempty"`
	Details  map[string]any `json:"details"`
	LastRun  time.Time      `json:"last_run"`
	Duration string         `json:"duration"`
}

// HealthSummary provides overall health summary
type HealthSummary struct {
	OverallStatus string `json:"overall_status"`
	TotalProbes   int    `json:"total_probes"`
	HealthyCount  int    `json:"healthy_count"`
	CriticalCount int    `json:"critical_count"`
	ErrorCount    int    `json:"error_count"`
}

// Info contains application information
type Info struct {
	Name        string    `json:"name"`
	Environment string    `json:"environment"`
	Version     string    `json:"version"`
	StartTime   time.Time `json:"start_time,omitempty"`
	Uptime      string    `json:"uptime,omitempty"`
	PID         int       `json:"pid"`
	Goroutines  int       `json:"goroutines"`
	MemoryUsage uint64    `json:"memory_usage"`
}

// handleHealthDebug provides comprehensive health debugging information
func (d *DebugHandlers) handleHealthDebug(c echo.Context) error {
	start := time.Now()

	healthInfo := &HealthDebugInfo{
		Components: make(map[string]ComponentHealth),
		App:        d.getAppInfo(),
	}

	// Run all health probes and collect detailed information
	for _, probe := range d.app.healthProbes {
		probeStart := time.Now()
		result := probe.Run(c.Request().Context())
		duration := time.Since(probeStart)

		component := ComponentHealth{
			Status:   result.Status,
			Critical: result.Critical,
			Details:  result.Details,
			LastRun:  probeStart,
			Duration: duration.String(),
		}

		if result.Err != nil {
			component.Error = result.Err.Error()
		}

		if component.Details == nil {
			component.Details = make(map[string]any)
		}

		healthInfo.Components[result.Name] = component
	}

	// Add manager-specific health information if available
	d.addManagerHealth(healthInfo)

	// Calculate summary (incluyendo componentes añadidos)
	healthInfo.Summary = d.calculateHealthSummary(healthInfo.Components)
	resp := d.newDebugResponse(start, healthInfo, nil)
	return c.JSON(http.StatusOK, resp)
}

// getAppInfo collects application information
func (d *DebugHandlers) getAppInfo() Info {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	appInfo := Info{
		PID:         os.Getpid(),
		Goroutines:  runtime.NumGoroutine(),
		MemoryUsage: memStats.Alloc,
	}

	if d.app.cfg != nil {
		appInfo.Name = d.app.cfg.App.Name
		appInfo.Environment = d.app.cfg.App.Env
		appInfo.Version = d.app.cfg.App.Version
	}

	return appInfo
}

// calculateHealthSummary calculates overall health summary
func (d *DebugHandlers) calculateHealthSummary(components map[string]ComponentHealth) HealthSummary {
	summary := HealthSummary{
		TotalProbes: len(components),
	}

	for _, component := range components {
		switch component.Status {
		case "healthy", "ready":
			summary.HealthyCount++
		default:
			if component.Critical {
				summary.CriticalCount++
			}
			if component.Error != "" {
				summary.ErrorCount++
			}
		}
	}

	// Determine overall status
	if summary.CriticalCount > 0 {
		summary.OverallStatus = "critical"
	} else if summary.ErrorCount > 0 {
		summary.OverallStatus = "degraded"
	} else if summary.HealthyCount == summary.TotalProbes && summary.TotalProbes > 0 {
		summary.OverallStatus = "healthy"
	} else {
		summary.OverallStatus = unknownStatus
	}

	return summary
}

// addManagerHealth adds detailed information from database and messaging managers
func (d *DebugHandlers) addManagerHealth(healthInfo *HealthDebugInfo) {
	// Add database manager information
	if d.app.dbManager != nil {
		dbStart := time.Now()
		stats := d.app.dbManager.Stats()
		dbDuration := time.Since(dbStart)

		dbHealth := ComponentHealth{
			Status:   "healthy",
			Details:  make(map[string]any),
			LastRun:  dbStart,
			Duration: dbDuration.String(),
		}
		dbHealth.Details["stats"] = stats

		healthInfo.Components["database_manager"] = dbHealth
	}

	// Add messaging manager information
	if d.app.messagingManager != nil {
		msgStart := time.Now()
		stats := d.app.messagingManager.Stats()
		msgDuration := time.Since(msgStart)

		msgHealth := ComponentHealth{
			Status:   "healthy",
			Details:  make(map[string]any),
			LastRun:  msgStart,
			Duration: msgDuration.String(),
		}
		msgHealth.Details["stats"] = stats

		healthInfo.Components["messaging_manager"] = msgHealth
	}
}
