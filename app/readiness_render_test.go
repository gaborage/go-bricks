package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

// describe builds a lease-less description whose liveness result and statistics are fixed,
// so a row can name the shape both views must render without standing up a manager.
func describe(name string, critical bool, liveErr error, stats map[string]any, allow []string) probeDescription {
	return probeDescription{
		name:        name,
		critical:    critical,
		publicStats: allow,
		live:        func(context.Context) error { return liveErr },
		stats:       func() map[string]any { return stats },
	}
}

// describeNotConfigured builds a description whose lease reports "not configured" — the
// shape a database-free deployment produces, and with perTenant the shape a multi-tenant
// one produces for the fixed "" key.
func describeNotConfigured(name string, critical, perTenant bool, stats map[string]any, allow []string) probeDescription {
	return probeDescription{
		name:        name,
		critical:    critical,
		perTenant:   perTenant,
		publicStats: allow,
		acquire: func(context.Context) (func(context.Context) error, func(), error) {
			return nil, nil, config.NewNotConfiguredError(name, "HOST", name+".host")
		},
		stats: func() map[string]any { return stats },
	}
}

// foreignProbe is a Prober from outside the framework: it declares no allowlist, so the
// unauthenticated body may carry its status and nothing else.
type foreignProbe struct {
	result HealthStatus
}

func (p *foreignProbe) Run(context.Context) HealthStatus { return p.result }

// mustNotRunProbe fails the test if it is ever invoked — the standing proof that /ready
// stops at the first failing critical probe rather than paying for the rest.
type mustNotRunProbe struct {
	t *testing.T
}

func (p mustNotRunProbe) Run(context.Context) HealthStatus {
	p.t.Error("no probe after the first failing critical one may run")
	return HealthStatus{Name: "never"}
}

// jsonKey spells a map key the way encoding/json renders it. A forbidden key that is also
// the tail of a published one — "connections" inside "active_connections", "error" inside
// "errors" — would otherwise match the published key and assert nothing.
func jsonKey(name string) string { return `"` + name + `":` }

// wantComponent is a debug entry with the two varying fields (LastRun, Duration) left out.
type wantComponent struct {
	status   string
	critical bool
	errText  string
	details  map[string]any
}

// TestReadinessViews is the module's one table: each row is a probe set, and every row
// asserts the four things the two views must agree on — the /ready status code, how far
// /ready's run got, the exact /ready body, and the debug components plus summary.
// Asserting the whole body map (rather than key-by-key) is what pins the one-body rule: a
// kind that stops rendering, or a stats key that starts, fails the row. wantReadyRuns is
// the other half: /ready stops at the blocking probe, while the debug view runs them all.
func TestReadinessViews(t *testing.T) {
	const (
		fixedUnix   = int64(1755300000)
		streamKey   = "payments-ledger/fraud-scoring"
		redisAddr   = "10.0.0.9:6379"
		driverError = "failed to connect to `user=app database=payments`: 10.0.0.5:5432"
	)
	now := time.Unix(fixedUnix, 0)
	appCfg := &config.AppConfig{Name: "svc", Env: "test", Version: "1.0.0"}
	appBody := map[string]any{appNameKey: "svc", appEnvKey: "test", appVersionKey: "1.0.0"}

	dbStats := map[string]any{
		"active_connections": 2,
		"max_connections":    25,
		"idle_ttl_seconds":   3600,
		"errors":             0,
		"connections":        []map[string]any{{"key": "tenant-alpha"}},
	}
	dbPublic := map[string]any{
		"active_connections": 2, "max_connections": 25, "idle_ttl_seconds": 3600, "errors": 0,
	}
	streamStats := map[string]any{
		"started": true, "consumers": 1, "publishers": 1, "ready": true,
		"stored_offsets":        map[string]int64{streamKey: 4242},
		"offset_store_count":    500,
		"offset_flush_interval": "5s",
	}
	streamPublic := map[string]any{
		"started": true, "consumers": 1, "publishers": 1, "ready": true,
		"offset_store_count": 500, "offset_flush_interval": "5s",
	}
	cacheStats := map[string]any{"active_caches": 3, "errors": 0}

	// withStatus returns stats plus the status key the judge stamps on details.
	withStatus := func(stats map[string]any, status string) map[string]any {
		out := map[string]any{statusKey: status}
		for k, v := range stats {
			out[k] = v
		}
		return out
	}

	tests := []struct {
		name          string
		probes        []Prober
		wantCode      int
		wantReadyRuns int // probes /ready evaluates before it answers
		wantBody      map[string]any
		// forbidden entries are matched as raw substrings of the encoded body, so a key
		// that is the tail of a published one is spelled with jsonKey.
		forbidden      []string
		wantComponents map[string]wantComponent
		wantSummary    HealthSummary
	}{
		{
			name: "every_registered_kind_renders_status_and_stats",
			probes: []Prober{
				describe(componentDatabase, true, nil, dbStats, databasePublicStats),
				disabledProbe(componentMessaging),
				describeNotConfigured(componentCache, true, false, cacheStats, cachePublicStats),
				describe(componentStreams, false, nil, streamStats, streamsPublicStats),
			},
			wantCode:      200,
			wantReadyRuns: 4,
			wantBody: map[string]any{
				statusKey:                        readyStatus,
				timeKey:                          fixedUnix,
				"app":                            appBody,
				componentDatabase:                healthyStatus,
				componentDatabase + statsSuffix:  withStatus(dbPublic, healthyStatus),
				componentMessaging:               disabledStatus,
				componentMessaging + statsSuffix: map[string]any{statusKey: disabledStatus},
				componentCache:                   notConfiguredStatus,
				componentCache + statsSuffix:     withStatus(cacheStats, notConfiguredStatus),
				componentStreams:                 healthyStatus,
				componentStreams + statsSuffix:   withStatus(streamPublic, healthyStatus),
			},
			forbidden: []string{"tenant-alpha", jsonKey("connections"), streamKey, "stored_offsets", "4242"},
			wantComponents: map[string]wantComponent{
				componentDatabase:  {status: healthyStatus, critical: true, details: withStatus(dbStats, healthyStatus)},
				componentMessaging: {status: disabledStatus, details: map[string]any{statusKey: disabledStatus}},
				componentCache:     {status: notConfiguredStatus, critical: true, details: withStatus(cacheStats, notConfiguredStatus)},
				componentStreams:   {status: healthyStatus, details: withStatus(streamStats, healthyStatus)},
			},
			wantSummary: HealthSummary{OverallStatus: healthyStatus, TotalProbes: 4, HealthyCount: 4},
		},
		{
			name: "first_failing_critical_kind_gates_and_sanitizes",
			probes: []Prober{
				describe(componentDatabase, true, errors.New(driverError), dbStats, databasePublicStats),
				describe(componentCache, true, errors.New(redisAddr+": connection refused"), cacheStats, cachePublicStats),
			},
			wantCode:      503,
			wantReadyRuns: 1,
			wantBody: map[string]any{
				statusKey:         "not ready",
				componentDatabase: unhealthyStatus,
				errorKey:          "database unavailable",
			},
			forbidden: []string{driverError, "user=app", "10.0.0.5", redisAddr, "active_connections"},
			wantComponents: map[string]wantComponent{
				componentDatabase: {status: unhealthyStatus, critical: true, errText: driverError, details: withStatus(dbStats, unhealthyStatus)},
				componentCache:    {status: unhealthyStatus, critical: true, errText: redisAddr + ": connection refused", details: withStatus(cacheStats, unhealthyStatus)},
			},
			wantSummary: HealthSummary{OverallStatus: criticalStatus, TotalProbes: 2, CriticalCount: 2, ErrorCount: 2},
		},
		{
			name: "non_critical_failure_stays_ready_and_reads_degraded",
			probes: []Prober{
				describe(componentDatabase, true, nil, dbStats, databasePublicStats),
				describe(componentStreams, false, errStreamsNotOpen, streamStats, streamsPublicStats),
			},
			wantCode:      200,
			wantReadyRuns: 2,
			wantBody: map[string]any{
				statusKey:                       readyStatus,
				timeKey:                         fixedUnix,
				"app":                           appBody,
				componentDatabase:               healthyStatus,
				componentDatabase + statsSuffix: withStatus(dbPublic, healthyStatus),
				componentStreams:                unhealthyStatus,
				componentStreams + statsSuffix:  withStatus(streamPublic, unhealthyStatus),
			},
			forbidden: []string{streamKey, "4242", jsonKey(errorKey)},
			wantComponents: map[string]wantComponent{
				componentDatabase: {status: healthyStatus, critical: true, details: withStatus(dbStats, healthyStatus)},
				componentStreams:  {status: unhealthyStatus, errText: errStreamsNotOpen.Error(), details: withStatus(streamStats, unhealthyStatus)},
			},
			// The drift decision 4 removes: this used to be `unknown`, because the debug
			// summary gated on a status list while /ready gated on Err && Critical.
			wantSummary: HealthSummary{OverallStatus: degradedStatus, TotalProbes: 2, HealthyCount: 1, ErrorCount: 1},
		},
		{
			name: "absence_is_ready_equivalent_in_both_views",
			probes: []Prober{
				describeNotConfigured(componentDatabase, true, false, dbStats, databasePublicStats),
				describeNotConfigured(componentMessaging, false, true, nil, messagingPublicStats),
				disabledProbe(componentCache),
			},
			wantCode:      200,
			wantReadyRuns: 3,
			wantBody: map[string]any{
				statusKey:                        readyStatus,
				timeKey:                          fixedUnix,
				"app":                            appBody,
				componentDatabase:                notConfiguredStatus,
				componentDatabase + statsSuffix:  withStatus(dbPublic, notConfiguredStatus),
				componentMessaging:               perTenantStatus,
				componentMessaging + statsSuffix: map[string]any{statusKey: perTenantStatus},
				componentCache:                   disabledStatus,
				componentCache + statsSuffix:     map[string]any{statusKey: disabledStatus},
			},
			forbidden: []string{"tenant-alpha", jsonKey(errorKey)},
			wantComponents: map[string]wantComponent{
				componentDatabase:  {status: notConfiguredStatus, critical: true, details: withStatus(dbStats, notConfiguredStatus)},
				componentMessaging: {status: perTenantStatus, details: map[string]any{statusKey: perTenantStatus}},
				componentCache:     {status: disabledStatus, details: map[string]any{statusKey: disabledStatus}},
			},
			wantSummary: HealthSummary{OverallStatus: healthyStatus, TotalProbes: 3, HealthyCount: 3},
		},
		{
			name: "foreign_probe_publishes_only_its_status",
			probes: []Prober{
				&foreignProbe{result: HealthStatus{
					Name:    "vault",
					Status:  healthyStatus,
					Details: map[string]any{statusKey: healthyStatus, "addr": "10.0.0.9:8200"},
				}},
			},
			wantCode:      200,
			wantReadyRuns: 1,
			wantBody: map[string]any{
				statusKey:             readyStatus,
				timeKey:               fixedUnix,
				"app":                 appBody,
				"vault":               healthyStatus,
				"vault" + statsSuffix: map[string]any{statusKey: healthyStatus},
			},
			forbidden: []string{"10.0.0.9:8200", "addr"},
			wantComponents: map[string]wantComponent{
				"vault": {status: healthyStatus, details: map[string]any{statusKey: healthyStatus, "addr": "10.0.0.9:8200"}},
			},
			wantSummary: HealthSummary{OverallStatus: healthyStatus, TotalProbes: 1, HealthyCount: 1},
		},
		{
			// A Prober may report no Details at all. /ready still mirrors its status under
			// <name>_stats, while the debug entry's nil guard renders an empty object.
			name: "prober_without_details_mirrors_its_status",
			probes: []Prober{
				&foreignProbe{result: HealthStatus{Name: "vault", Status: healthyStatus}},
			},
			wantCode:      200,
			wantReadyRuns: 1,
			wantBody: map[string]any{
				statusKey:             readyStatus,
				timeKey:               fixedUnix,
				"app":                 appBody,
				"vault":               healthyStatus,
				"vault" + statsSuffix: map[string]any{statusKey: healthyStatus},
			},
			wantComponents: map[string]wantComponent{
				"vault": {status: healthyStatus, details: map[string]any{}},
			},
			wantSummary: HealthSummary{OverallStatus: healthyStatus, TotalProbes: 1, HealthyCount: 1},
		},
		{
			name: "status_outside_the_vocabulary_reads_unknown",
			probes: []Prober{
				&foreignProbe{result: HealthStatus{Name: "vault", Status: "starting", Details: map[string]any{statusKey: "starting"}}},
			},
			wantCode:      200,
			wantReadyRuns: 1,
			wantBody: map[string]any{
				statusKey:             readyStatus,
				timeKey:               fixedUnix,
				"app":                 appBody,
				"vault":               "starting",
				"vault" + statsSuffix: map[string]any{statusKey: "starting"},
			},
			wantComponents: map[string]wantComponent{
				"vault": {status: "starting", details: map[string]any{statusKey: "starting"}},
			},
			wantSummary: HealthSummary{OverallStatus: unknownStatus, TotalProbes: 1},
		},
		{
			name:          "no_probes_renders_the_envelope_alone",
			probes:        []Prober{},
			wantCode:      200,
			wantReadyRuns: 0,
			wantBody: map[string]any{
				statusKey: readyStatus,
				timeKey:   fixedUnix,
				"app":     appBody,
			},
			wantComponents: map[string]wantComponent{},
			wantSummary:    HealthSummary{OverallStatus: unknownStatus},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// /ready's run: registration order, stopping at the first failing critical kind.
			report, blocking, found := runUntilBlocking(context.Background(), tt.probes)

			var body map[string]any
			if found {
				body = notReadyBody(&blocking)
			} else {
				body = report.readyBody(appCfg, now)
			}

			wantFound := tt.wantCode == 503
			assert.Equal(t, wantFound, found, "the gate decides the status code")
			assert.Len(t, report, tt.wantReadyRuns, "/ready must not evaluate past the blocking probe")
			assert.Equal(t, tt.wantBody, body)

			assertReadyBodyOmits(t, body, tt.forbidden...)

			// The debug view's run: every probe, whatever /ready decided.
			full := runReadinessProbes(context.Background(), tt.probes)
			require.Len(t, full, len(tt.probes), "the debug view reports every registered kind")

			components := full.debugComponents()
			require.Len(t, components, len(tt.wantComponents))
			for name, want := range tt.wantComponents {
				got, ok := components[name]
				require.Truef(t, ok, "the debug view must carry %q", name)
				assert.Equal(t, want.status, got.Status)
				assert.Equal(t, want.critical, got.Critical)
				assert.Equal(t, want.errText, got.Error)
				assert.Equal(t, want.details, got.Details, "the debug view carries the full unredacted details")
				assert.False(t, got.LastRun.IsZero())
				probeDuration, parseErr := time.ParseDuration(got.Duration)
				require.NoError(t, parseErr)
				assert.GreaterOrEqual(t, probeDuration, time.Duration(0))
			}

			assert.Equal(t, tt.wantSummary, healthSummary(components))
		})
	}
}

// TestReadinessProbeOrderIsRegistrationOrder pins that the report — and therefore the
// gate's "first failing critical" — follows registration order, which is what makes the
// 503 body name the database rather than whichever kind the map iteration happened to hit.
func TestReadinessProbeOrderIsRegistrationOrder(t *testing.T) {
	report := runReadinessProbes(context.Background(), []Prober{
		disabledProbe(componentDatabase),
		disabledProbe(componentMessaging),
		disabledProbe(componentCache),
		disabledProbe(componentStreams),
	})

	names := make([]string, 0, len(report))
	for i := range report {
		names = append(names, report[i].status.Name)
	}
	assert.Equal(t, []string{componentDatabase, componentMessaging, componentCache, componentStreams}, names)
}

// TestIsFailingAndIsReadyEquivalentPartitionTheVocabulary pins the one predicate both views
// share. Widening isReadyEquivalent until it swallows unhealthy is the mistake that would
// make the debug summary report healthy while /ready answers 503.
func TestIsFailingAndIsReadyEquivalentPartitionTheVocabulary(t *testing.T) {
	for _, status := range []string{healthyStatus, notConfiguredStatus, disabledStatus, perTenantStatus} {
		assert.Truef(t, isReadyEquivalent(status), "%q is ready-equivalent", status)
		assert.Falsef(t, isFailing(status), "%q is not failing", status)
	}
	assert.True(t, isFailing(unhealthyStatus))
	assert.False(t, isReadyEquivalent(unhealthyStatus))
	assert.False(t, isFailing("starting"))
	assert.False(t, isReadyEquivalent("starting"))
}

// TestRunUntilBlockingStopsAtTheFirstBlockingProbe pins both halves of /ready's traversal:
// a non-critical failure never gates, however early it is registered (the messaging and
// streams kinds depend on that), and nothing after the blocking probe runs at all — which
// is what keeps a database outage from adding a Redis PING and a publisher lease to every
// poll of an unauthenticated endpoint. The trailing probe fails the test if it is reached.
func TestRunUntilBlockingStopsAtTheFirstBlockingProbe(t *testing.T) {
	report, blocking, found := runUntilBlocking(context.Background(), []Prober{
		describe(componentStreams, false, errStreamsNotOpen, nil, streamsPublicStats),
		describe(componentCache, true, errors.New("connection refused"), nil, cachePublicStats),
		mustNotRunProbe{t: t},
	})

	require.True(t, found)
	assert.Equal(t, componentCache, blocking.Name, "the non-critical failure ahead of it must not gate")
	assert.Len(t, report, 2, "evaluation stops at the blocking probe")
}

// TestRunUntilBlockingRunsEveryProbeWhenNothingBlocks is the other direction: with no
// blocking kind, /ready's run reaches every probe, so a healthy deployment's body still
// carries all of them.
func TestRunUntilBlockingRunsEveryProbeWhenNothingBlocks(t *testing.T) {
	report, _, found := runUntilBlocking(context.Background(), []Prober{
		describe(componentDatabase, true, nil, nil, databasePublicStats),
		describe(componentStreams, false, errStreamsNotOpen, nil, streamsPublicStats),
		describe(componentCache, false, errors.New("connection refused"), nil, cachePublicStats),
	})

	assert.False(t, found)
	assert.Len(t, report, 3, "a non-critical failure must not truncate the body")
}

// TestPublicProbeError pins both branches of the /ready sanitization switch. A negated
// condition here would either leak every probe's raw error or force the default onto a
// probe that declared its own wording, and neither shows up as a compile or type failure.
func TestPublicProbeError(t *testing.T) {
	t.Run("public_error_set_overrides_the_default", func(t *testing.T) {
		result := HealthStatus{
			Name:      componentDatabase,
			PublicErr: "database temporarily unavailable",
			Err:       errors.New(pgconnIdentityError),
		}

		assert.Equal(t, "database temporarily unavailable", publicProbeError(&result))
	})

	t.Run("public_error_empty_synthesizes_a_safe_default", func(t *testing.T) {
		result := HealthStatus{Name: componentDatabase, Err: errors.New(pgconnIdentityError)}

		assert.Equal(t, databaseUnavailableBody, publicProbeError(&result))
	})

	t.Run("nil_error_renders_without_panicking", func(t *testing.T) {
		result := HealthStatus{Name: componentCache}

		assert.Equal(t, cacheUnavailableBody, publicProbeError(&result))
	})
}

// TestPublicProjectionKeepsOnlyAllowlistedKeys pins both halves of the render-site filter:
// the /ready view carries the allowlisted counters plus the mirrored status and nothing
// else, and the map it was built from is untouched — the access-controlled debug view
// renders that same map and operators need the withheld keys there.
func TestPublicProjectionKeepsOnlyAllowlistedKeys(t *testing.T) {
	details := map[string]any{
		"active_connections": 2,
		"max_connections":    25,
		"idle_ttl_seconds":   3600,
		"errors":             0,
		statusKey:            healthyStatus,
		connectionsStatsKey: []map[string]any{
			{"key": "tenant-alpha", "last_used": "2026-08-05T10:00:00Z", "idle_duration": 4},
		},
	}

	public := publicProjection(&HealthStatus{Status: healthyStatus, Details: details}, databasePublicStats)

	assert.Equal(t, map[string]any{
		"active_connections": 2,
		"max_connections":    25,
		"idle_ttl_seconds":   3600,
		"errors":             0,
		statusKey:            healthyStatus,
	}, public, "every allowlisted counter survives; the keyed array is withheld")
	assert.Contains(t, details, connectionsStatsKey,
		"filtering in place would strip the array from the debug endpoint's view too")
}

// TestPublicProjectionWithoutAnAllowlist covers the two shapes with no counters to publish:
// a disabled kind, whose only detail is its own status, and a Prober from outside the
// framework, which declares no allowlist and therefore publishes its status alone.
func TestPublicProjectionWithoutAnAllowlist(t *testing.T) {
	assert.Equal(t, map[string]any{statusKey: disabledStatus},
		publicProjection(&HealthStatus{Status: disabledStatus, Details: map[string]any{statusKey: disabledStatus}}, nil))

	assert.Equal(t, map[string]any{statusKey: healthyStatus},
		publicProjection(&HealthStatus{
			Status:  healthyStatus,
			Details: map[string]any{statusKey: healthyStatus, "vault_addr": "10.0.0.9:8200"},
		}, nil))

	assert.Equal(t, map[string]any{statusKey: healthyStatus},
		publicProjection(&HealthStatus{Status: healthyStatus}, databasePublicStats),
		"a nil details map must still render the mirrored status — never JSON null")
}

// TestReadyBodyPinsTheWireFormat is the one assertion whose expected side spells no
// production constant. The table above builds its expectations from statusKey, timeKey and
// friends, so renaming a key's value would keep that table green while every consumer of
// /ready broke; this row is what fails instead.
func TestReadyBodyPinsTheWireFormat(t *testing.T) {
	body := runReadinessProbes(context.Background(), []Prober{disabledProbe("cache")}).
		readyBody(&config.AppConfig{Name: "svc", Env: "test", Version: "1.0.0"}, time.Unix(1755300000, 0))

	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"ready","time":1755300000,"app":{"name":"svc","environment":"test","version":"1.0.0"},"cache":"disabled","cache_stats":{"status":"disabled"}}`, string(encoded))
}

// TestNotReadyBodyPinsTheWireFormat is the 503 half of the same pin, and doubles as the
// wire-level proof that the driver's connection identity never reaches the body.
func TestNotReadyBodyPinsTheWireFormat(t *testing.T) {
	result := HealthStatus{
		Name:     componentDatabase,
		Status:   unhealthyStatus,
		Critical: true,
		Err:      errors.New(pgconnIdentityError),
	}

	encoded, err := json.Marshal(notReadyBody(&result))
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"not ready","database":"unhealthy","error":"database unavailable"}`, string(encoded))
}
