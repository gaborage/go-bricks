package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

func TestDebugHealthHandlers(t *testing.T) {
	tests := []struct {
		name           string
		setupApp       func() *App
		expectedStatus int
		checkResponse  func(t *testing.T, body string)
	}{
		{
			name: "health debug with probes",
			setupApp: func() *App {
				cfg := &config.Config{
					App: config.AppConfig{
						Name:    "test-app",
						Env:     "test",
						Version: "1.0.0",
					},
				}

				app := &App{
					cfg:    cfg,
					logger: logger.New("info", false),
					healthProbes: []HealthProbe{
						&testHealthProbe{
							name:   "database",
							status: "healthy",
							err:    nil,
						},
					},
				}
				return app
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, body string) {
				assert.Contains(t, body, "database")
				assert.Contains(t, body, "healthy")
				assert.Contains(t, body, "overall_status")
				assert.Contains(t, body, "app")
			},
		},
		{
			name: "health debug with failing probe",
			setupApp: func() *App {
				cfg := &config.Config{
					App: config.AppConfig{
						Name:    "test-app",
						Env:     "test",
						Version: "1.0.0",
					},
				}

				app := &App{
					cfg:    cfg,
					logger: logger.New("info", false),
					healthProbes: []HealthProbe{
						&testHealthProbe{
							name:     "database",
							status:   "unhealthy",
							err:      assert.AnError,
							critical: true,
						},
					},
				}
				return app
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, body string) {
				assert.Contains(t, body, "database")
				assert.Contains(t, body, "unhealthy")
				assert.Contains(t, body, "critical")
				assert.Contains(t, body, "error")
			},
		},
		{
			name: "health debug with nil config",
			setupApp: func() *App {
				app := &App{
					cfg:          nil,
					logger:       logger.New("info", false),
					healthProbes: []HealthProbe{},
				}
				return app
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, body string) {
				assert.Contains(t, body, "app")
				assert.Contains(t, body, "memory_usage")
				assert.Contains(t, body, "goroutines")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := tt.setupApp()
			debugConfig := &config.DebugConfig{
				Enabled:    true,
				PathPrefix: "/_debug",
			}

			debugHandlers := NewDebugHandlers(app, debugConfig, app.logger)

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/health-debug", http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := debugHandlers.handleHealthDebug(c)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, rec.Code)

			if tt.checkResponse != nil {
				tt.checkResponse(t, rec.Body.String())
			}
		})
	}
}

func TestGetAppInfo(t *testing.T) {
	tests := []struct {
		name     string
		setupApp func() *App
		check    func(t *testing.T, info Info)
	}{
		{
			name: "with valid config",
			setupApp: func() *App {
				cfg := &config.Config{
					App: config.AppConfig{
						Name:    "test-service",
						Env:     "production",
						Version: "2.1.0",
					},
				}

				return &App{
					cfg:    cfg,
					logger: logger.New("info", false),
				}
			},
			check: func(t *testing.T, info Info) {
				assert.Equal(t, "test-service", info.Name)
				assert.Equal(t, "production", info.Environment)
				assert.Equal(t, "2.1.0", info.Version)
				assert.Greater(t, info.PID, 0)
				assert.Greater(t, info.Goroutines, 0)
				assert.Greater(t, info.MemoryUsage, uint64(0))
			},
		},
		{
			name: "with nil config",
			setupApp: func() *App {
				return &App{
					cfg:    nil,
					logger: logger.New("info", false),
				}
			},
			check: func(t *testing.T, info Info) {
				assert.Empty(t, info.Name)
				assert.Empty(t, info.Environment)
				assert.Empty(t, info.Version)
				assert.Greater(t, info.PID, 0)
				assert.Greater(t, info.Goroutines, 0)
				assert.Greater(t, info.MemoryUsage, uint64(0))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := tt.setupApp()
			debugConfig := &config.DebugConfig{}
			debugHandlers := NewDebugHandlers(app, debugConfig, app.logger)

			info := debugHandlers.getAppInfo()
			tt.check(t, info)
		})
	}
}

func TestCalculateHealthSummary(t *testing.T) {
	debugHandlers := &DebugHandlers{
		logger: logger.New("info", false),
	}

	tests := []struct {
		name       string
		components map[string]ComponentHealth
		expected   HealthSummary
	}{
		{
			name: "all healthy components",
			components: map[string]ComponentHealth{
				"db":        {Status: "healthy"},
				"messaging": {Status: "ready"},
				"cache":     {Status: "healthy"},
			},
			expected: HealthSummary{
				OverallStatus: "healthy",
				TotalProbes:   3,
				HealthyCount:  3,
				CriticalCount: 0,
				ErrorCount:    0,
			},
		},
		{
			name: "mixed health states",
			components: map[string]ComponentHealth{
				"db":        {Status: "healthy"},
				"messaging": {Status: "unhealthy", Error: "connection failed"},
				"cache":     {Status: "degraded", Critical: false},
			},
			expected: HealthSummary{
				OverallStatus: "degraded",
				TotalProbes:   3,
				HealthyCount:  1,
				CriticalCount: 0,
				ErrorCount:    1,
			},
		},
		{
			name: "critical failure",
			components: map[string]ComponentHealth{
				"db": {Status: "failed", Critical: true, Error: "database down"},
			},
			expected: HealthSummary{
				OverallStatus: "critical",
				TotalProbes:   1,
				HealthyCount:  0,
				CriticalCount: 1,
				ErrorCount:    1,
			},
		},
		{
			name:       "no components",
			components: map[string]ComponentHealth{},
			expected: HealthSummary{
				OverallStatus: unknownStatus,
				TotalProbes:   0,
				HealthyCount:  0,
				CriticalCount: 0,
				ErrorCount:    0,
			},
		},
		{
			name: "unknown status",
			components: map[string]ComponentHealth{
				unknownStatus: {Status: unknownStatus},
			},
			expected: HealthSummary{
				OverallStatus: unknownStatus,
				TotalProbes:   1,
				HealthyCount:  0,
				CriticalCount: 0,
				ErrorCount:    0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := debugHandlers.calculateHealthSummary(tt.components)
			assert.Equal(t, tt.expected, summary)
		})
	}
}

func TestAddManagerHealth(t *testing.T) {
	tests := []struct {
		name          string
		setupApp      func() *App
		checkResponse func(t *testing.T, healthInfo *HealthDebugInfo)
	}{
		{
			name: "with database manager",
			setupApp: func() *App {
				mockDBManager := &database.DbManager{}
				return &App{
					dbManager: mockDBManager,
					logger:    logger.New("info", false),
				}
			},
			checkResponse: func(t *testing.T, healthInfo *HealthDebugInfo) {
				dbComponent, exists := healthInfo.Components["database_manager"]
				assert.True(t, exists)
				assert.Equal(t, "healthy", dbComponent.Status)
				assert.NotNil(t, dbComponent.Details["stats"])
				// Verify timestamp consistency
				assert.False(t, dbComponent.LastRun.IsZero(), "LastRun should not be zero time")
				assert.NotEmpty(t, dbComponent.Duration, "Duration should not be empty")
			},
		},
		{
			name: "with messaging manager",
			setupApp: func() *App {
				mockMsgManager := &messaging.Manager{}
				return &App{
					messagingManager: mockMsgManager,
					logger:           logger.New("info", false),
				}
			},
			checkResponse: func(t *testing.T, healthInfo *HealthDebugInfo) {
				msgComponent, exists := healthInfo.Components["messaging_manager"]
				assert.True(t, exists)
				assert.Equal(t, "healthy", msgComponent.Status)
				assert.NotNil(t, msgComponent.Details["stats"])
				// Verify timestamp consistency
				assert.False(t, msgComponent.LastRun.IsZero(), "LastRun should not be zero time")
				assert.NotEmpty(t, msgComponent.Duration, "Duration should not be empty")
			},
		},
		{
			name: "with both managers",
			setupApp: func() *App {
				mockDBManager := &database.DbManager{}
				mockMsgManager := &messaging.Manager{}
				return &App{
					dbManager:        mockDBManager,
					messagingManager: mockMsgManager,
					logger:           logger.New("info", false),
				}
			},
			checkResponse: func(t *testing.T, healthInfo *HealthDebugInfo) {
				dbComponent, hasDB := healthInfo.Components["database_manager"]
				msgComponent, hasMsg := healthInfo.Components["messaging_manager"]
				assert.True(t, hasDB)
				assert.True(t, hasMsg)
				// Verify timestamp consistency for both managers
				assert.False(t, dbComponent.LastRun.IsZero(), "Database manager LastRun should not be zero time")
				assert.NotEmpty(t, dbComponent.Duration, "Database manager Duration should not be empty")
				assert.False(t, msgComponent.LastRun.IsZero(), "Messaging manager LastRun should not be zero time")
				assert.NotEmpty(t, msgComponent.Duration, "Messaging manager Duration should not be empty")
			},
		},
		{
			name: "with nil managers",
			setupApp: func() *App {
				return &App{
					dbManager:        nil,
					messagingManager: nil,
					logger:           logger.New("info", false),
				}
			},
			checkResponse: func(t *testing.T, healthInfo *HealthDebugInfo) {
				_, hasDB := healthInfo.Components["database_manager"]
				_, hasMsg := healthInfo.Components["messaging_manager"]
				assert.False(t, hasDB)
				assert.False(t, hasMsg)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := tt.setupApp()
			debugConfig := &config.DebugConfig{}
			debugHandlers := NewDebugHandlers(app, debugConfig, app.logger)

			healthInfo := &HealthDebugInfo{
				Components: make(map[string]ComponentHealth),
			}

			debugHandlers.addManagerHealth(healthInfo)
			tt.checkResponse(t, healthInfo)
		})
	}
}

// Test utilities

type testHealthProbe struct {
	name     string
	status   string
	err      error
	critical bool
}

func (p *testHealthProbe) Run(_ context.Context) HealthStatus {
	details := make(map[string]any)
	if p.name == "database" {
		details["connection_count"] = 5
	}

	return HealthStatus{
		Name:     p.name,
		Status:   p.status,
		Err:      p.err,
		Critical: p.critical,
		Details:  details,
	}
}
