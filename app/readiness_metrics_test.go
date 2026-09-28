package app

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/streams"
	"github.com/gaborage/go-bricks/observability"
	obtest "github.com/gaborage/go-bricks/observability/testing"
	"github.com/gaborage/go-bricks/server"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

// registerTestGauges registers the runtime gauges against a manual reader, so a test drives one
// collection at a time instead of waiting on an export interval.
func registerTestGauges(t *testing.T, sources gaugeSources) *obtest.TestMeterProvider {
	t.Helper()
	mp := obtest.NewTestMeterProvider()
	unregister, err := registerRuntimeGauges(mp.Meter(appMeterName), sources)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unregister()) })
	return mp
}

// statsSlot seals one kind's description carrying just the Stats() snapshot the gauges read, so
// a test drives the slot seam the callback walks without standing up a manager.
func statsSlot(kind string, stats func() map[string]any) resourceSlot {
	return &sealedSlot{sealedReadiness{kind: kind, description: &probeDescription{name: kind, stats: stats}}}
}

// disabledSlot seals the description an unconfigured kind produces: reported as disabled, and
// carrying no statistics at all.
func disabledSlot(kind string) resourceSlot {
	description := disabledProbe(kind)
	return &sealedSlot{sealedReadiness{kind: kind, description: &description}}
}

// unsealedSlot is a kind that sealed no description — the streams lane that never started.
func unsealedSlot(kind string) resourceSlot {
	return &sealedSlot{sealedReadiness{kind: kind}}
}

// gaugeDataPoints returns what a collection carries for one gauge. An instrument the callback
// observed nothing for is absent from the collection entirely — that disappearing series is the
// whole mechanism behind "a kind is reported only while its last verdict is healthy or
// unhealthy", so "no datapoints" is asserted here and never assumed.
func gaugeDataPoints(t *testing.T, rm metricdata.ResourceMetrics, name string) []metricdata.DataPoint[int64] {
	t.Helper()
	found := obtest.FindMetric(rm, name)
	if found == nil {
		return nil
	}
	gauge, ok := found.Data.(metricdata.Gauge[int64])
	require.True(t, ok, "%s must be an Int64 gauge", name)
	return gauge.DataPoints
}

// attrString and attrBool read one attribute off a series, failing the test when the series does
// not carry it at all rather than reading a zero value as an answer.
func attrString(t *testing.T, set attribute.Set, key string) string {
	t.Helper()
	value, ok := set.Value(attribute.Key(key))
	require.True(t, ok, "the series must carry %s", key)
	return value.AsString()
}

func attrBool(t *testing.T, set attribute.Set, key string) bool {
	t.Helper()
	value, ok := set.Value(attribute.Key(key))
	require.True(t, ok, "the series must carry %s", key)
	return value.AsBool()
}

// TestMetricNamesAreTheDocumentedWireNames pins the exported names as literals. Production and
// every other test here share the metric* constants, so renaming one would keep them all green
// while the dashboard contract documented in wiki/observability.md went stale — the names are
// what an operator's queries and alerts are written against.
func TestMetricNamesAreTheDocumentedWireNames(t *testing.T) {
	assert.Equal(t, "app.readiness.status", metricReadinessStatus)
	assert.Equal(t, "messaging.consumer.registries", metricConsumerRegistries)
	assert.Equal(t, "messaging.consumer.declared", metricConsumerDeclared)
	assert.Equal(t, "messaging.consumer.subscribed", metricConsumerSubscribed)
	assert.Equal(t, "messaging.consumer.resubscribes", metricConsumerResubscribes)
	assert.Equal(t, "messaging.consumer.max_fail_streak", metricConsumerMaxFailStreak)
	assert.Equal(t, "messaging.streams.consumers", metricStreamsConsumers)
	assert.Equal(t, "messaging.streams.publishers", metricStreamsPublishers)

	assert.Equal(t, "readiness.kind", attrReadinessKind)
	assert.Equal(t, "readiness.critical", attrReadinessCritical)

	// A gauge added to the table without a literal above would otherwise go unpinned.
	pinned := 0
	for _, specs := range statGaugeSpecs {
		pinned += len(specs)
	}
	assert.Equal(t, 7, pinned, "every stat gauge beside app.readiness.status needs a literal above")
}

// TestReadinessStatusGaugeReportsTheLastVerdict pins the value mapping and, for every status
// that means absence by design, the absence of any series at all: a dashboard must never read an
// unused kind as healthy, and a deploy must never start at 0 (ADR-120).
func TestReadinessStatusGaugeReportsTheLastVerdict(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		wantValue  int64
		wantSeries bool
	}{
		{name: "healthy", status: healthyStatus, wantValue: 1, wantSeries: true},
		{name: "unhealthy", status: unhealthyStatus, wantValue: 0, wantSeries: true},
		{name: "not_configured", status: notConfiguredStatus},
		{name: "disabled", status: disabledStatus},
		{name: "per_tenant", status: perTenantStatus},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _, _ := newTestVerdictStore()
			store.record(&HealthStatus{Name: componentDatabase, Status: tt.status, Critical: true})
			mp := registerTestGauges(t, gaugeSources{verdicts: store})

			points := gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus)

			if !tt.wantSeries {
				assert.Empty(t, points, "%s is absence by design, not a reading", tt.status)
				return
			}
			require.Len(t, points, 1)
			assert.Equal(t, tt.wantValue, points[0].Value)
		})
	}
}

// TestReadinessStatusGaugeDropsUnjudgedKinds pins the fourth silent case: a kind no judgment has
// reached yet has no verdict to report, so it has no series either.
func TestReadinessStatusGaugeDropsUnjudgedKinds(t *testing.T) {
	store, _, _ := newTestVerdictStore()
	mp := registerTestGauges(t, gaugeSources{verdicts: store})

	assert.Empty(t, gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus),
		"no judgment, no reading")
}

// TestReadinessStatusGaugeAttributesNameTheKindAndItsCriticality pins the two attributes an
// operator filters and alerts on, one series per judged kind.
func TestReadinessStatusGaugeAttributesNameTheKindAndItsCriticality(t *testing.T) {
	store, _, _ := newTestVerdictStore()
	store.record(&HealthStatus{Name: componentDatabase, Status: healthyStatus, Critical: true})
	store.record(&HealthStatus{Name: componentCache, Status: unhealthyStatus, Err: errors.New("connection refused")})
	mp := registerTestGauges(t, gaugeSources{verdicts: store})

	points := gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus)
	require.Len(t, points, 2)

	got := make(map[string]bool, len(points))
	values := make(map[string]int64, len(points))
	for _, point := range points {
		kind := attrString(t, point.Attributes, attrReadinessKind)
		got[kind] = attrBool(t, point.Attributes, attrReadinessCritical)
		values[kind] = point.Value
	}

	assert.Equal(t, map[string]bool{componentCache: false, componentDatabase: true}, got)
	assert.Equal(t, map[string]int64{componentCache: 0, componentDatabase: 1}, values)
}

// TestReadinessStatusGaugeOnlyNamesTheFrameworkKinds pins advisory item 18. Two blocking results
// name readiness itself rather than a kind — the judge asked before startSlots sealed, and a
// request canceled while it waited on the shared judgment (app/lifecycle.go) — and both are
// synthesized outside walk. Neither may reach the store, or the gauge would grow a `readiness`
// series no kind owns.
func TestReadinessStatusGaugeOnlyNamesTheFrameworkKinds(t *testing.T) {
	store, _, _ := newTestVerdictStore()
	notStarted := judgeOf(describe(componentDatabase, true, nil, nil, nil))
	notStarted.started = false

	report, blocking, found := notStarted.gate(context.Background())
	require.True(t, found)
	require.Equal(t, componentReadiness, blocking.Name)
	report.record(store)

	mp := registerTestGauges(t, gaugeSources{verdicts: store})
	require.Empty(t, gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus),
		"a result no kind produced has no series")

	started := judgeOf(
		describe(componentDatabase, true, nil, nil, nil),
		describe(componentMessaging, false, nil, nil, nil),
		describe(componentCache, false, errors.New("connection refused"), nil, nil),
		describe(componentStreams, false, nil, nil, nil),
	)
	started.full(context.Background()).record(store)

	kinds := make([]string, 0, 4)
	for _, point := range gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus) {
		kinds = append(kinds, attrString(t, point.Attributes, attrReadinessKind))
	}
	assert.ElementsMatch(t, []string{componentDatabase, componentMessaging, componentCache, componentStreams}, kinds)
}

// TestRuntimeGaugesReadTheRealManagersCounters pins the seven Stats() keys against the managers
// that spell them. The managers hardcode their map keys in their own packages, unreachable from
// here, so without this a rename would leave every other test green while the gauges silently
// reported nothing.
func TestRuntimeGaugesReadTheRealManagersCounters(t *testing.T) {
	mp := registerTestGauges(t, gaugeSources{slots: []resourceSlot{
		statsSlot(componentMessaging, (&messaging.Manager{}).Stats),
		statsSlot(componentStreams, (&streams.Manager{}).Stats),
	}})

	rm := mp.Collect(t)
	for kind, specs := range statGaugeSpecs {
		for _, spec := range specs {
			assert.Len(t, gaugeDataPoints(t, rm, spec.name), 1,
				"%s reads %q, which the %s manager must still publish", spec.name, spec.key, kind)
		}
	}
}

// TestRuntimeGaugesReadEachKeyOnce pins which counter feeds which gauge, so two keys cannot be
// crossed. consumer_resubscribes carries the value type hazard: it is a uint64 while its four
// neighbors are ints. The database slot pins the other half of the walk — a kind with no gauge
// group is never asked for its statistics, so the two kinds whose counters have their own
// instruments pay nothing per collection.
func TestRuntimeGaugesReadEachKeyOnce(t *testing.T) {
	mp := registerTestGauges(t, gaugeSources{slots: []resourceSlot{
		statsSlot(componentDatabase, func() map[string]any {
			t.Error("a kind with no gauge group must not be asked for its statistics")
			return nil
		}),
		statsSlot(componentMessaging, func() map[string]any {
			return map[string]any{
				gaugeConsumerRegistriesKey:   3,
				gaugeDeclaredConsumersKey:    7,
				gaugeSubscribedConsumersKey:  5,
				gaugeConsumerResubscribesKey: uint64(11),
				gaugeConsumerFailStreakKey:   int64(2),
			}
		}),
		statsSlot(componentStreams, func() map[string]any {
			return map[string]any{gaugeStreamsConsumersKey: 4, gaugeStreamsPublishersKey: 6}
		}),
	}})

	rm := mp.Collect(t)
	want := map[string]int64{
		metricConsumerRegistries:    3,
		metricConsumerDeclared:      7,
		metricConsumerSubscribed:    5,
		metricConsumerResubscribes:  11,
		metricConsumerMaxFailStreak: 2,
		metricStreamsConsumers:      4,
		metricStreamsPublishers:     6,
	}
	for name, value := range want {
		points := gaugeDataPoints(t, rm, name)
		require.Len(t, points, 1, name)
		assert.Equal(t, value, points[0].Value, name)
	}
}

// TestRuntimeGaugesSkipUnreadableStats pins the per-key skip: a counter that is missing, has been
// retyped, or is too large to carry reports nothing rather than a zero an operator would read as
// an idle consumer.
func TestRuntimeGaugesSkipUnreadableStats(t *testing.T) {
	mp := registerTestGauges(t, gaugeSources{slots: []resourceSlot{
		statsSlot(componentMessaging, func() map[string]any {
			return map[string]any{
				gaugeDeclaredConsumersKey:    "seven",
				gaugeConsumerResubscribesKey: uint64(math.MaxUint64),
				gaugeSubscribedConsumersKey:  5,
			}
		}),
	}})

	rm := mp.Collect(t)
	assert.Empty(t, gaugeDataPoints(t, rm, metricConsumerRegistries), "a missing key reports nothing")
	assert.Empty(t, gaugeDataPoints(t, rm, metricConsumerDeclared), "a retyped key reports nothing")
	assert.Empty(t, gaugeDataPoints(t, rm, metricConsumerResubscribes), "a value int64 cannot carry reports nothing")
	assert.Len(t, gaugeDataPoints(t, rm, metricConsumerSubscribed), 1, "its readable neighbor still reports")
}

// TestRuntimeGaugesCarryTheLargestConvertibleCounter pins the uint64 conversion at its exact
// boundary: MaxInt64 is carryable and must report, one more is not. A guard that rejected MaxInt64
// too would lose a legitimate reading, and the over-cap neighbor above only proves the far side.
func TestRuntimeGaugesCarryTheLargestConvertibleCounter(t *testing.T) {
	mp := registerTestGauges(t, gaugeSources{slots: []resourceSlot{
		statsSlot(componentMessaging, func() map[string]any {
			return map[string]any{
				gaugeConsumerResubscribesKey: uint64(math.MaxInt64),
				gaugeSubscribedConsumersKey:  uint64(math.MaxInt64) + 1,
			}
		}),
	}})

	rm := mp.Collect(t)
	points := gaugeDataPoints(t, rm, metricConsumerResubscribes)
	require.Len(t, points, 1, "a uint64 of exactly MaxInt64 is convertible and must report")
	assert.Equal(t, int64(math.MaxInt64), points[0].Value)
	assert.Empty(t, gaugeDataPoints(t, rm, metricConsumerSubscribed), "one past MaxInt64 reports nothing")
}

// TestRuntimeGaugesReportNothingWithoutSources pins the shape of a deployment that has none of
// this: no store, messaging unconfigured — which seals a disabled description carrying no
// statistics — and the native streams lane that never started, which seals no description at
// all. Neither absence needs a guard of its own in the callback.
func TestRuntimeGaugesReportNothingWithoutSources(t *testing.T) {
	mp := registerTestGauges(t, gaugeSources{slots: []resourceSlot{
		disabledSlot(componentMessaging),
		unsealedSlot(componentStreams),
	}})

	rm := mp.Collect(t)
	for _, name := range []string{metricReadinessStatus, metricConsumerRegistries, metricStreamsConsumers} {
		assert.Empty(t, gaugeDataPoints(t, rm, name), name)
	}
}

// meteredProvider is the observability.Provider shape registration meets: one handing back a
// usable meter provider. There is no second shape to defend against — both Provider
// implementations normalize the meter provider they return.
type meteredProvider struct {
	observability.Provider
	mp metric.MeterProvider
}

func (p meteredProvider) MeterProvider() metric.MeterProvider { return p.mp }

// TestStartRuntimeGaugesRegistersAgainstTheAppsProvider pins the wiring end to end: the gauges go
// through the App's own provider (never the otel global, which is unset with observability
// disabled), report from the store the judgment entry points wrote, read the manager counters
// through the slot list, and stop when Shutdown drops the callback. The slots here are the shape
// of a service that configures neither manager.
func TestStartRuntimeGaugesRegistersAgainstTheAppsProvider(t *testing.T) {
	mp := obtest.NewTestMeterProvider()
	store, _, _ := newTestVerdictStore()
	store.record(healthy(componentDatabase, true))
	app := &App{logger: logger.New("error", false), verdicts: store, observability: meteredProvider{mp: mp}}
	installSlotList(app, disabledSlot(componentMessaging), unsealedSlot(componentStreams))

	app.startRuntimeGauges()

	require.NotNil(t, app.unregisterGauges)
	assert.Len(t, gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus), 1)
	rm := mp.Collect(t)
	assert.Empty(t, gaugeDataPoints(t, rm, metricConsumerRegistries),
		"unconfigured messaging reports nothing")
	assert.Empty(t, gaugeDataPoints(t, rm, metricStreamsConsumers),
		"a streams lane that never started reports nothing")

	app.stopRuntimeGauges()

	assert.Empty(t, gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus),
		"the registration Shutdown drops stops reporting")
}

// TestStartRuntimeGaugesToleratesAProviderlessApp pins that registration is skipped, never fatal
// and never a panic, for the one shape that carries no meter provider: a hand-built App with no
// observability at all.
func TestStartRuntimeGaugesToleratesAProviderlessApp(t *testing.T) {
	noObservability := &App{logger: logger.New("error", false)}

	assert.NotPanics(t, noObservability.startRuntimeGauges)
	assert.Nil(t, noObservability.unregisterGauges, "no provider, no callback")
}

// TestPrepareRuntimeRegistersTheGaugesOnlyOnceStartupCannotFail pins where the registration sits
// in the startup order. Run hands a prepareRuntime failure straight back to its caller without a
// Shutdown, so nothing would ever drop a callback registered before a later step failed, and it
// would go on observing slots that never serve. The consumer veto is the last fallible step, so a
// registration anywhere earlier fails the rejecting arm; the accepting arm is the control that
// makes that absence mean something.
func TestPrepareRuntimeRegistersTheGaugesOnlyOnceStartupCannotFail(t *testing.T) {
	tests := []struct {
		name       string
		hookErr    error
		registered bool
	}{
		{name: "hook_rejects_the_route_table", hookErr: assert.AnError, registered: false},
		{name: "hook_accepts_the_route_table", hookErr: nil, registered: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
				Multitenant: config.MultitenantConfig{Enabled: false},
			}
			a := newLifecycleCheckAppWithLogger(t, cfg, logger.New("error", false))
			mp := obtest.NewTestMeterProvider()
			store, _, _ := newTestVerdictStore()
			store.record(healthy(componentDatabase, true))
			a.observability, a.verdicts = meteredProvider{mp: mp}, store
			a.postRegisterRoutes = func([]server.RouteDescriptor) error { return tt.hookErr }
			t.Cleanup(a.stopRuntimeGauges)

			err := a.prepareRuntime(context.Background())

			if tt.hookErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.hookErr)
			}
			assert.Equal(t, tt.registered, a.unregisterGauges != nil, "a callback outlives startup only when startup succeeded")
			series := gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus)
			if tt.registered {
				require.Len(t, series, 1, "the verdict recorded before startup is what a registered callback reports")
				return
			}
			assert.Empty(t, series, "a failed startup leaves nothing observing the slots")
		})
	}
}

// TestStopRuntimeGaugesUnregistersOnce pins the teardown Shutdown performs before the slots stop:
// the callback is dropped, and a second pass over an App already shut down does nothing.
func TestStopRuntimeGaugesUnregistersOnce(t *testing.T) {
	calls := 0
	app := &App{logger: logger.New("error", false), unregisterGauges: func() error {
		calls++
		return nil
	}}

	app.stopRuntimeGauges()
	app.stopRuntimeGauges()

	assert.Equal(t, 1, calls)
	assert.Nil(t, app.unregisterGauges)
}

// TestStopRuntimeGaugesReportsAFailedUnregister pins that a failing unregister is warned rather
// than returned: failing shutdown over a metrics callback would leave the rest of the teardown
// undone.
func TestStopRuntimeGaugesReportsAFailedUnregister(t *testing.T) {
	rec := &recLogger{}
	app := &App{logger: rec, unregisterGauges: func() error { return errors.New("already unregistered") }}

	app.stopRuntimeGauges()

	require.Equal(t, 1, loggedCount(rec, "Failed to unregister readiness gauges"))
	assert.Nil(t, app.unregisterGauges)
}

// builtReadinessApp is an App from the public constructor — the one path that builds the verdict
// store (Builder.CreateHealthProbes) — with every kind sealed by the real startSlots walk, so a
// judgment driven through it records exactly the way production does. Its database and messaging
// kinds are configured and healthy; the cache is unconfigured and the native streams lane never
// started, so neither has a verdict to report.
func builtReadinessApp(t *testing.T) (*App, *config.Config) {
	t.Helper()
	cfg := defaultTestConfig()
	cfg.Log.Level = "error"
	app := newConfiguredApp(t, cfg, &Options{
		Server: newMockServer(),
		DatabaseConnector: func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return dbtesting.NewTestDB(dbTypePostgres), nil
		},
		MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
	})
	require.NoError(t, app.startSlots(context.Background()))
	t.Cleanup(func() { app.stopSlots(context.Background()) })
	return app, cfg
}

// TestReadinessStatusGaugeReportsAJudgmentThroughTheBuiltApp pins the wiring no other test
// reaches: the store Builder.CreateHealthProbes installs on the App it built, written by the
// production record at each judgment entry point, read back through the gauge. Every other
// verdict test hands a store to an App by hand or drives the store directly, so a record call
// dropped from either entry point — or a store built and never assigned — would leave all of
// them green. One entry point per case, so each one's record is the only writer that could have
// produced its series. The meter provider is the single stand-in, since Options carries no
// observability seam; TestStartRuntimeGaugesRegistersAgainstTheAppsProvider pins the App's own
// provider onto the same gaugeSources() read here.
func TestReadinessStatusGaugeReportsAJudgmentThroughTheBuiltApp(t *testing.T) {
	tests := []struct {
		name  string
		drive func(t *testing.T, app *App, cfg *config.Config)
	}{
		{
			name: "ready_endpoint",
			drive: func(t *testing.T, app *App, cfg *config.Config) {
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, readyEndpoint, http.NoBody)
				rec := httptest.NewRecorder()
				require.NoError(t, app.readyCheck(server.NewHandlerContextForTest(rec, req, cfg)))
				require.Equal(t, http.StatusOK, rec.Code)
			},
		},
		{
			name: "health_debug",
			drive: func(t *testing.T, app *App, cfg *config.Config) {
				handlers := NewDebugHandlers(app, &config.DebugConfig{Enabled: true, PathPrefix: "/_debug"}, app.logger)
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health-debug", http.NoBody)
				rec := httptest.NewRecorder()
				require.NoError(t, handlers.handleHealthDebug(server.NewHandlerContextForTest(rec, req, cfg)))
				require.Equal(t, http.StatusOK, rec.Code)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, cfg := builtReadinessApp(t)
			mp := registerTestGauges(t, app.gaugeSources())
			require.Empty(t, gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus),
				"the built App has judged nothing yet, so every series below comes from the judgment")

			tt.drive(t, app, cfg)

			critical := map[string]bool{}
			values := map[string]int64{}
			for _, point := range gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus) {
				kind := attrString(t, point.Attributes, attrReadinessKind)
				critical[kind] = attrBool(t, point.Attributes, attrReadinessCritical)
				values[kind] = point.Value
			}

			assert.Equal(t, map[string]bool{componentDatabase: true, componentMessaging: false}, critical)
			assert.Equal(t, map[string]int64{componentDatabase: 1, componentMessaging: 1}, values)
		})
	}
}
