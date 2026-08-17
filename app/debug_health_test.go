package app

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
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
					healthProbes: []Prober{
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
					healthProbes: []Prober{
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
					healthProbes: []Prober{},
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

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health-debug", http.NoBody)
			rec := httptest.NewRecorder()
			c := server.NewHandlerContextForTest(rec, req, nil)

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

// TestHealthDebugKeepsFullCacheErrorWhileReadySanitizes pins both halves of the cache
// error-routing contract from a single probe set: /ready (no allowlist, no auth) discloses
// nothing about the backend, while the application log and the IP-allowlisted /health-debug
// keep the address an operator needs. Sanitizing at the probe would pass the /ready half
// alone, so the two are asserted together.
func TestHealthDebugKeepsFullCacheErrorWhileReadySanitizes(t *testing.T) {
	cacheManager := createTestCacheManagerWithGetError(t,
		cache.NewConnectionError("ping", redisProbeAddress, errors.New(errorRedisDown)))

	cfg := &config.Config{App: config.AppConfig{Name: appName, Env: testName, Version: appVersion}}
	require.Nil(t, cfg.Cache.Critical, "the strict default is what makes the /ready 503 reachable here")

	log := &recLogger{}
	app := &App{cfg: cfg, logger: log, cacheManager: cacheManager}
	app.healthProbes = app.createHealthProbes(probeInputs{})
	require.Len(t, app.healthProbes, 3)

	readyReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, readyEndpoint, http.NoBody)
	readyRec := httptest.NewRecorder()
	require.NoError(t, app.readyCheck(server.NewHandlerContextForTest(readyRec, readyReq, cfg)))

	debugHandlers := NewDebugHandlers(app, &config.DebugConfig{Enabled: true, PathPrefix: "/_debug"}, log)
	debugReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health-debug", http.NoBody)
	debugRec := httptest.NewRecorder()
	require.NoError(t, debugHandlers.handleHealthDebug(server.NewHandlerContextForTest(debugRec, debugReq, cfg)))

	var readyBody map[string]any
	require.NoError(t, json.Unmarshal(readyRec.Body.Bytes(), &readyBody))
	assert.Equal(t, http.StatusServiceUnavailable, readyRec.Code)
	assertCacheErrorSanitized(t, readyBody)

	assert.Equal(t, http.StatusOK, debugRec.Code)
	assert.Contains(t, debugRec.Body.String(), redisProbeAddress,
		"/health-debug must keep the address; sanitizing HealthStatus.Err itself would gut the diagnostic")
	assert.Contains(t, debugRec.Body.String(), errorRedisDown)

	logged, ok := loggedEvent(log, "Readiness check failed")
	require.True(t, ok, "readyCheck must log the failure the 503 body no longer describes")
	assert.Equal(t, "error", logged.level)
	assert.Contains(t, logged.err, redisProbeAddress,
		"readyCheck must log the full error so operators keep the diagnostic the 503 body drops")
	assert.Equal(t, componentCache, logged.str["component"],
		"the sanitized body no longer identifies the failure, so the log must name the component")
}

// TestHealthDebugKeepsPooledConnectionKeysWhileReadyOmitsThem pins both halves of the
// database_stats routing contract from a single DbManager. SECURITY: a pooled connection's
// key is the resourcepool key — the tenant ID in a multi-tenant deployment — so /ready's 200 body
// used to answer an unauthenticated, unthrottled caller with a live tenant enumeration plus
// per-tenant timing. It must now carry the scalar counters only, while the access-controlled
// /health-debug keeps the per-connection detail operators diagnose with. Redacting inside
// DbManager.Stats() or the probe would satisfy the /ready half alone, so the two are
// asserted together.
func TestHealthDebugKeepsPooledConnectionKeysWhileReadyOmitsThem(t *testing.T) {
	const (
		tenantAlpha = "tenant-alpha"
		tenantBeta  = "tenant-beta"
	)

	log := &recLogger{}
	connector := func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
		return dbtesting.NewTestDB(dbTypePostgres), nil
	}
	dbManager := database.NewDbManager(&stubTenantResource{}, log,
		database.DbManagerOptions{MaxSize: 5, IdleTTL: time.Hour}, connector)
	t.Cleanup(func() { assert.NoError(t, dbManager.Close()) })

	for _, key := range []string{tenantAlpha, tenantBeta} {
		_, release, err := dbManager.Get(context.Background(), key)
		require.NoError(t, err)
		release()
	}

	// The premise the /ready assertions rest on: two distinct keys really are pooled, so
	// their absence below is a redaction rather than an empty fixture asserting nothing.
	statsConns, err := json.Marshal(dbManager.Stats()[connectionsStatsKey])
	require.NoError(t, err)
	require.Contains(t, string(statsConns), tenantAlpha, "Stats() must still carry the pool keys")
	require.Contains(t, string(statsConns), tenantBeta, "Stats() must still carry the pool keys")

	cfg := &config.Config{App: config.AppConfig{Name: appName, Env: testName, Version: appVersion}}
	app := &App{cfg: cfg, logger: log, dbManager: dbManager}
	app.healthProbes = app.createHealthProbes(probeInputs{})
	require.Len(t, app.healthProbes, 3)

	readyReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, readyEndpoint, http.NoBody)
	readyRec := httptest.NewRecorder()
	require.NoError(t, app.readyCheck(server.NewHandlerContextForTest(readyRec, readyReq, cfg)))

	debugHandlers := NewDebugHandlers(app, &config.DebugConfig{Enabled: true, PathPrefix: "/_debug"}, log)
	debugReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health-debug", http.NoBody)
	debugRec := httptest.NewRecorder()
	require.NoError(t, debugHandlers.handleHealthDebug(server.NewHandlerContextForTest(debugRec, debugReq, cfg)))

	var readyBody map[string]any
	require.NoError(t, json.Unmarshal(readyRec.Body.Bytes(), &readyBody))
	assert.Equal(t, http.StatusOK, readyRec.Code)

	dbStats, ok := readyBody["database_stats"].(map[string]any)
	require.True(t, ok, "the 200 body must still carry database_stats")
	assert.Contains(t, dbStats, "active_connections")
	assert.Contains(t, dbStats, "max_connections")
	assert.Contains(t, dbStats, "idle_ttl_seconds")
	assert.Equal(t, healthyStatus, dbStats[statusKey])
	assert.NotContains(t, dbStats, connectionsStatsKey,
		"the per-connection array enumerates tenants on an unauthenticated endpoint")
	assertReadyBodyOmits(t, readyBody, tenantAlpha, tenantBeta)

	assert.Equal(t, http.StatusOK, debugRec.Code)
	var debugBody struct {
		Data struct {
			Components map[string]struct {
				Details map[string]any `json:"details"`
			} `json:"components"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(debugRec.Body.Bytes(), &debugBody))
	debugConns, err := json.Marshal(debugBody.Data.Components[componentDatabase].Details[connectionsStatsKey])
	require.NoError(t, err)
	assert.Contains(t, string(debugConns), tenantAlpha,
		"/health-debug renders the probe's details; redacting in Stats() or the probe would gut it")
	assert.Contains(t, string(debugConns), tenantBeta)
}

// TestHealthDebugRendersOneEntryPerKind pins the handler against the one-debug-view rule:
// the components map carries exactly the registered kinds, keyed by component name, and no
// separate *_manager entries — a manager's statistics are its kind's details. The summary
// comes from the same predicate /ready gates on, so a non-critical kind that is not live
// reads degraded rather than the unknown the two-model split produced.
func TestHealthDebugRendersOneEntryPerKind(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Name: appName, Env: testName, Version: appVersion}}
	app := &App{
		cfg:    cfg,
		logger: logger.New("error", false),
		healthProbes: []Prober{
			probeDescription{
				name:        componentDatabase,
				critical:    true,
				publicStats: databasePublicStats,
				live:        func(context.Context) error { return nil },
				stats:       func() map[string]any { return map[string]any{"active_connections": 1} },
			},
			probeDescription{
				name:        componentStreams,
				publicStats: streamsPublicStats,
				live:        func(context.Context) error { return errStreamsNotOpen },
				stats: func() map[string]any {
					return map[string]any{"stored_offsets": map[string]int64{"orders/projector": 7}}
				},
			},
		},
		dbManager:        &database.DbManager{},
		messagingManager: &messaging.Manager{},
	}

	handlers := NewDebugHandlers(app, &config.DebugConfig{Enabled: true, PathPrefix: "/_debug"}, app.logger)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health-debug", http.NoBody)
	rec := httptest.NewRecorder()
	require.NoError(t, handlers.handleHealthDebug(server.NewHandlerContextForTest(rec, req, cfg)))

	var decoded struct {
		Data struct {
			Components map[string]struct {
				Status  string         `json:"status"`
				Error   string         `json:"error"`
				Details map[string]any `json:"details"`
			} `json:"components"`
			Summary HealthSummary `json:"summary"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.ElementsMatch(t, []string{componentDatabase, componentStreams},
		slices.Collect(maps.Keys(decoded.Data.Components)), "one entry per registered kind, and no *_manager entries")
	assert.Equal(t, errStreamsNotOpen.Error(), decoded.Data.Components[componentStreams].Error)
	assert.Contains(t, decoded.Data.Components[componentStreams].Details, "stored_offsets",
		"the access-controlled view keeps what the /ready projection withholds")
	assert.Equal(t, HealthSummary{
		OverallStatus: degradedStatus,
		TotalProbes:   2,
		HealthyCount:  1,
		ErrorCount:    1,
	}, decoded.Data.Summary)
}

// TestHealthDebugRunsEveryProbeBehindAFailingCriticalKind pins the one place the two
// traversals must differ. /ready stops at the first failing critical kind so an outage costs
// the probes ahead of it and no more; the debug view is the access-controlled diagnostic, so
// it reports the kinds behind that outage — which is exactly when an operator needs them.
// Rendering the debug view from /ready's truncated report would drop them.
func TestHealthDebugRunsEveryProbeBehindAFailingCriticalKind(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Name: appName, Env: testName, Version: appVersion}}
	app := &App{
		cfg:    cfg,
		logger: logger.New("error", false),
		healthProbes: []Prober{
			describe(componentDatabase, true, errors.New("connection refused"), nil, databasePublicStats),
			describe(componentCache, true, nil, nil, cachePublicStats),
		},
	}

	handlers := NewDebugHandlers(app, &config.DebugConfig{Enabled: true, PathPrefix: "/_debug"}, app.logger)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health-debug", http.NoBody)
	rec := httptest.NewRecorder()
	require.NoError(t, handlers.handleHealthDebug(server.NewHandlerContextForTest(rec, req, cfg)))

	var decoded struct {
		Data struct {
			Components map[string]ComponentHealth `json:"components"`
			Summary    HealthSummary              `json:"summary"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.ElementsMatch(t, []string{componentDatabase, componentCache}, slices.Collect(maps.Keys(decoded.Data.Components)),
		"the kind behind the failing critical one must still be probed and reported")
	assert.Equal(t, healthyStatus, decoded.Data.Components[componentCache].Status)
	assert.Equal(t, HealthSummary{
		OverallStatus: criticalStatus,
		TotalProbes:   2,
		HealthyCount:  1,
		CriticalCount: 1,
		ErrorCount:    1,
	}, decoded.Data.Summary)
}
