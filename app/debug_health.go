package app

import (
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/gaborage/go-bricks/server"
)

const (
	unknownStatus = "unknown"
)

// healthDebugInfo contains enhanced health information for debugging
type healthDebugInfo struct {
	Components map[string]componentHealth `json:"components"`
	Summary    healthSummary              `json:"summary"`
	App        Info                       `json:"app"`
}

// componentHealth contains detailed health information for a component
type componentHealth struct {
	Status   string         `json:"status"`
	Critical bool           `json:"critical"`
	Error    string         `json:"error,omitempty"`
	Details  map[string]any `json:"details"`
	LastRun  time.Time      `json:"last_run"`
	Duration string         `json:"duration"`
}

// healthSummary provides overall health summary
type healthSummary struct {
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
	StartTime   time.Time `json:"start_time"`
	Uptime      string    `json:"uptime"`
	PID         int       `json:"pid"`
	Goroutines  int       `json:"goroutines"`
	MemoryUsage uint64    `json:"memory_usage"`
}

// handleHealthDebug renders the access-controlled debug view from one probe run: unlike
// /ready's gate it runs every registered kind, so the kinds behind an outage are still
// reported (ADR-066).
func (d *DebugHandlers) handleHealthDebug(c server.HandlerContext) error {
	start := time.Now()

	components := runReadinessProbes(c.RequestContext(), d.app.healthProbes).debugComponents()

	healthInfo := &healthDebugInfo{
		Components: components,
		Summary:    summarizeHealth(components),
		App:        d.getAppInfo(),
	}

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
