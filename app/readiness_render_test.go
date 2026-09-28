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
func describe(name string, critical bool, liveErr error, stats map[string]any) probeDescription {
	return probeDescription{
		name:     name,
		critical: critical,
		live:     func(context.Context) error { return liveErr },
		stats:    func() map[string]any { return stats },
	}
}

// describeNotConfigured builds a description whose lease reports "not configured" — the
// shape a database-free deployment produces, and with perTenant the shape a multi-tenant
// one produces for the fixed "" key.
func describeNotConfigured(name string, critical, perTenant bool, stats map[string]any) probeDescription {
	return probeDescription{
		name:      name,
		critical:  critical,
		perTenant: perTenant,
		acquire: func(context.Context) (func(context.Context) error, func(), error) {
			return nil, nil, config.NewNotConfiguredError(name, "HOST", name+".host")
		},
		stats: func() map[string]any { return stats },
	}
}

// mustNotRunDescription fails the test if it is ever judged — the standing proof that
// /ready stops at the first failing critical kind rather than paying for the rest.
func mustNotRunDescription(t *testing.T) probeDescription {
	return probeDescription{
		name: "never",
		live: func(context.Context) error {
			t.Error("no probe after the first failing critical one may run")
			return nil
		},
	}
}

// wantComponent is a debug entry with the two varying fields (LastRun, Duration) left out.
type wantComponent struct {
	status   string
	critical bool
	errText  string
	details  map[string]any
}

// TestReadinessViews is the module's one table: each row is a probe set, and every row
// asserts the four things the two views must agree on — the /ready status code, how far
// /ready's run got, the exact /ready body, and the debug components plus summary. Since
// ADR-120 that body is the verdict alone, identical on every row, so asserting it per row is
// what pins the trim: a kind, a counter or an error text that starts rendering again fails
// every row at once. wantReadyRuns is the other half: /ready stops at the blocking probe,
// while the debug view runs them all.
func TestReadinessViews(t *testing.T) {
	const (
		streamKey   = "payments-ledger/fraud-scoring"
		redisAddr   = "10.0.0.9:6379"
		driverError = "failed to connect to `user=app database=payments`: 10.0.0.5:5432"
	)

	dbStats := map[string]any{
		"active_connections": 2,
		"max_connections":    25,
		"idle_ttl_seconds":   3600,
		"errors":             0,
		"connections":        []map[string]any{{"key": "tenant-alpha"}},
	}
	streamStats := map[string]any{
		"started": true, "consumers": 1, "publishers": 1, "ready": true,
		"stored_offsets":        map[string]int64{streamKey: 4242},
		"offset_store_count":    500,
		"offset_flush_interval": "5s",
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
		name           string
		descriptions   []probeDescription
		wantCode       int
		wantReadyRuns  int // probes /ready evaluates before it answers
		wantComponents map[string]wantComponent
		wantSummary    healthSummary
	}{
		{
			name: "every_registered_kind_is_judged",
			descriptions: []probeDescription{
				describe(componentDatabase, true, nil, dbStats),
				disabledProbe(componentMessaging),
				describeNotConfigured(componentCache, true, false, cacheStats),
				describe(componentStreams, false, nil, streamStats),
			},
			wantCode:      200,
			wantReadyRuns: 4,
			wantComponents: map[string]wantComponent{
				componentDatabase:  {status: healthyStatus, critical: true, details: withStatus(dbStats, healthyStatus)},
				componentMessaging: {status: disabledStatus, details: map[string]any{statusKey: disabledStatus}},
				componentCache:     {status: notConfiguredStatus, critical: true, details: withStatus(cacheStats, notConfiguredStatus)},
				componentStreams:   {status: healthyStatus, details: withStatus(streamStats, healthyStatus)},
			},
			wantSummary: healthSummary{OverallStatus: healthyStatus, TotalProbes: 4, HealthyCount: 4},
		},
		{
			name: "first_failing_critical_kind_gates",
			descriptions: []probeDescription{
				describe(componentDatabase, true, errors.New(driverError), dbStats),
				describe(componentCache, true, errors.New(redisAddr+": connection refused"), cacheStats),
			},
			wantCode:      503,
			wantReadyRuns: 1,
			wantComponents: map[string]wantComponent{
				componentDatabase: {status: unhealthyStatus, critical: true, errText: driverError, details: withStatus(dbStats, unhealthyStatus)},
				componentCache:    {status: unhealthyStatus, critical: true, errText: redisAddr + ": connection refused", details: withStatus(cacheStats, unhealthyStatus)},
			},
			wantSummary: healthSummary{OverallStatus: criticalStatus, TotalProbes: 2, CriticalCount: 2, ErrorCount: 2},
		},
		{
			name: "non_critical_failure_stays_ready_and_reads_degraded",
			descriptions: []probeDescription{
				describe(componentDatabase, true, nil, dbStats),
				describe(componentStreams, false, errStreamsNotOpen, streamStats),
			},
			wantCode:      200,
			wantReadyRuns: 2,
			wantComponents: map[string]wantComponent{
				componentDatabase: {status: healthyStatus, critical: true, details: withStatus(dbStats, healthyStatus)},
				componentStreams:  {status: unhealthyStatus, errText: errStreamsNotOpen.Error(), details: withStatus(streamStats, unhealthyStatus)},
			},
			// The drift decision 4 removes: this used to be `unknown`, because the debug
			// summary gated on a status list while /ready gated on Err && Critical.
			wantSummary: healthSummary{OverallStatus: degradedStatus, TotalProbes: 2, HealthyCount: 1, ErrorCount: 1},
		},
		{
			name: "absence_is_ready_equivalent_in_both_views",
			descriptions: []probeDescription{
				describeNotConfigured(componentDatabase, true, false, dbStats),
				describeNotConfigured(componentMessaging, false, true, nil),
				disabledProbe(componentCache),
			},
			wantCode:      200,
			wantReadyRuns: 3,
			wantComponents: map[string]wantComponent{
				componentDatabase:  {status: notConfiguredStatus, critical: true, details: withStatus(dbStats, notConfiguredStatus)},
				componentMessaging: {status: perTenantStatus, details: map[string]any{statusKey: perTenantStatus}},
				componentCache:     {status: disabledStatus, details: map[string]any{statusKey: disabledStatus}},
			},
			wantSummary: healthSummary{OverallStatus: healthyStatus, TotalProbes: 3, HealthyCount: 3},
		},
		{
			// A kind whose statistics carry an address: they reach the access-controlled
			// debug view and nowhere else.
			name: "statistics_reach_the_debug_view_alone",
			descriptions: []probeDescription{
				describe("vault", false, nil, map[string]any{"addr": "10.0.0.9:8200"}),
			},
			wantCode:      200,
			wantReadyRuns: 1,
			wantComponents: map[string]wantComponent{
				"vault": {status: healthyStatus, details: map[string]any{statusKey: healthyStatus, "addr": "10.0.0.9:8200"}},
			},
			wantSummary: healthSummary{OverallStatus: healthyStatus, TotalProbes: 1, HealthyCount: 1},
		},
		{
			// A kind with no statistics at all: its debug entry carries the status the judge
			// stamps on details and nothing else.
			name: "description_without_statistics_details_its_status_alone",
			descriptions: []probeDescription{
				{name: "vault", live: func(context.Context) error { return nil }},
			},
			wantCode:      200,
			wantReadyRuns: 1,
			wantComponents: map[string]wantComponent{
				"vault": {status: healthyStatus, details: map[string]any{statusKey: healthyStatus}},
			},
			wantSummary: healthSummary{OverallStatus: healthyStatus, TotalProbes: 1, HealthyCount: 1},
		},
		{
			// A started application where no kind renders: a normal 200. The before-start
			// 503 is a different fact entirely — see TestJudgeBeforeTheStartWalkFailsClosed.
			name:           "no_kind_renders_is_a_normal_ready",
			descriptions:   []probeDescription{},
			wantCode:       200,
			wantReadyRuns:  0,
			wantComponents: map[string]wantComponent{},
			wantSummary:    healthSummary{OverallStatus: unknownStatus},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// /ready's run: slot order, stopping at the first failing critical kind.
			report, _, found := judgeOf(tt.descriptions...).gate(context.Background())

			wantFound := tt.wantCode == 503
			assert.Equal(t, wantFound, found, "the gate decides the status code")
			assert.Len(t, report, tt.wantReadyRuns, "/ready must not evaluate past the blocking kind")

			// The verdict, and nothing this row's kinds reported, is the whole body.
			body, wantBody := readyBody(), readyBodyJSON
			if found {
				body, wantBody = notReadyBody(), notReadyBodyJSON
			}
			encoded, marshalErr := json.Marshal(body)
			require.NoError(t, marshalErr)
			assert.JSONEq(t, wantBody, string(encoded))

			// The debug view's run: every kind, whatever /ready decided.
			full := judgeOf(tt.descriptions...).full(context.Background())
			require.Len(t, full, len(tt.descriptions), "the debug view reports every kind that renders")

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

			assert.Equal(t, tt.wantSummary, summarizeHealth(components))
		})
	}
}

// TestReadinessProbeOrderIsRegistrationOrder pins that the report — and therefore the
// gate's "first failing critical" — follows registration order, which is what makes the
// 503 body name the database rather than whichever kind the map iteration happened to hit.
func TestReadinessProbeOrderIsRegistrationOrder(t *testing.T) {
	report := judgeOf(
		disabledProbe(componentDatabase),
		disabledProbe(componentMessaging),
		disabledProbe(componentCache),
		disabledProbe(componentStreams),
	).full(context.Background())

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

// TestJudgeStopsAtTheFirstBlockingKind pins both halves of /ready's traversal:
// a non-critical failure never gates, however early it is registered (the messaging and
// streams kinds depend on that), and nothing after the blocking probe runs at all — which
// is what keeps a database outage from adding a Redis PING and a publisher lease to every
// poll of an unauthenticated endpoint. The trailing probe fails the test if it is reached.
func TestJudgeStopsAtTheFirstBlockingKind(t *testing.T) {
	report, blocking, found := judgeOf(
		describe(componentStreams, false, errStreamsNotOpen, nil),
		describe(componentCache, true, errors.New("connection refused"), nil),
		mustNotRunDescription(t),
	).gate(context.Background())

	require.True(t, found)
	assert.Equal(t, componentCache, blocking.Name, "the non-critical failure ahead of it must not gate")
	assert.Len(t, report, 2, "evaluation stops at the blocking probe")
}

// TestJudgeRunsEveryKindWhenNothingBlocks is the other direction: with no
// blocking kind, /ready's run reaches every probe, so a healthy deployment's body still
// carries all of them.
func TestJudgeRunsEveryKindWhenNothingBlocks(t *testing.T) {
	report, _, found := judgeOf(
		describe(componentDatabase, true, nil, nil),
		describe(componentStreams, false, errStreamsNotOpen, nil),
		describe(componentCache, false, errors.New("connection refused"), nil),
	).gate(context.Background())

	assert.False(t, found)
	assert.Len(t, report, 3, "a non-critical failure must not truncate the body")
}

// TestReadyBodyPinsTheWireFormat is the one assertion whose expected side spells no
// production constant. Every other assertion builds its expectation from statusKey and
// friends, so renaming a key's value would keep them green while every consumer of /ready
// broke; this is what fails instead.
func TestReadyBodyPinsTheWireFormat(t *testing.T) {
	encoded, err := json.Marshal(readyBody())
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"ready"}`, string(encoded))
}

// TestNotReadyBodyPinsTheWireFormat is the 503 half of the same pin.
func TestNotReadyBodyPinsTheWireFormat(t *testing.T) {
	encoded, err := json.Marshal(notReadyBody())
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"not ready"}`, string(encoded))
}

// TestReadyBodiesNameNoKind is the disclosure half of the two pins above, stated as the
// property rather than as a literal: neither body may carry a component name, so a kind
// added tomorrow cannot reach an unauthenticated caller by being appended to a renderer.
// componentReadiness is in the list because the 503 a canceled waiter gets is one of these
// two bodies too (ADR-120).
func TestReadyBodiesNameNoKind(t *testing.T) {
	for _, body := range []map[string]string{readyBody(), notReadyBody()} {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		for _, kind := range []string{
			componentDatabase, componentMessaging, componentCache, componentStreams, componentReadiness,
		} {
			assert.NotContainsf(t, string(encoded), kind,
				"/ready is unauthenticated; %q must not reach its body", kind)
		}
	}
}
