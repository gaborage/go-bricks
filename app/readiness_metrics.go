package app

import (
	"context"
	"fmt"
	"math"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// appMeterName scopes app's own instruments. app carried no meter before ADR-120's replacement
// signal; the gauges below are the first.
const appMeterName = "go-bricks/app"

const (
	metricReadinessStatus = "app.readiness.status"

	metricConsumerRegistries    = "messaging.consumer.registries"
	metricConsumerDeclared      = "messaging.consumer.declared"
	metricConsumerSubscribed    = "messaging.consumer.subscribed"
	metricConsumerResubscribes  = "messaging.consumer.resubscribes"
	metricConsumerMaxFailStreak = "messaging.consumer.max_fail_streak"

	metricStreamsConsumers  = "messaging.streams.consumers"
	metricStreamsPublishers = "messaging.streams.publishers"

	attrReadinessKind     = "readiness.kind"
	attrReadinessCritical = "readiness.critical"
)

// The manager Stats() keys the gauges read. Each manager spells its own keys in its own package,
// unreachable from here, so TestRuntimeGaugesReadTheRealManagersCounters pins these spellings
// against messaging.Manager.Stats and streams.Manager.Stats — a renamed key must fail a test
// rather than silently report nothing.
const (
	gaugeConsumerRegistriesKey   = "consumer_registries"
	gaugeDeclaredConsumersKey    = "declared_consumers"
	gaugeSubscribedConsumersKey  = "subscribed_consumers"
	gaugeConsumerResubscribesKey = "consumer_resubscribes"
	gaugeConsumerFailStreakKey   = "consumer_max_fail_streak"
	gaugeStreamsConsumersKey     = "consumers"
	gaugeStreamsPublishersKey    = "publishers"
)

// unitConsumer is the unit the three gauges that count consumers share.
const unitConsumer = "{consumer}"

// statGauge is one gauge: the metric it exports, the Stats() key that feeds it, and — once
// registerRuntimeGauges has built it — the instrument itself. One type, so a gauge is never
// described in one shape and observed through a second projection of it.
type statGauge struct {
	name        string
	key         string
	unit        string
	description string
	gauge       metric.Int64ObservableGauge
}

// statGaugeSpecs are the manager counters the /ready body stopped carrying (ADR-120) that no
// other instrument covers: the database pool and the cache manager already have their own
// (db.client.connection.*, cache.manager.*), and the rest stay on the access-controlled debug
// view.
//
// Keyed by the kind whose sealed probe description carries the Stats() snapshot the group reads,
// so the callback resolves a group by walking the slot list instead of holding a manager.
var statGaugeSpecs = map[string][]statGauge{
	componentMessaging: {
		{name: metricConsumerRegistries, key: gaugeConsumerRegistriesKey, unit: "{registry}", description: "Tenant keys holding a consumer registry"},
		{name: metricConsumerDeclared, key: gaugeDeclaredConsumersKey, unit: unitConsumer, description: "Consumers declared across those registries"},
		{name: metricConsumerSubscribed, key: gaugeSubscribedConsumersKey, unit: unitConsumer, description: "Declared consumers currently subscribed to their queue"},
		{name: metricConsumerResubscribes, key: gaugeConsumerResubscribesKey, unit: "{resubscribe}", description: "Re-subscribe attempts across those consumers since start"},
		{name: metricConsumerMaxFailStreak, key: gaugeConsumerFailStreakKey, unit: "{failure}", description: "Largest current re-subscribe failure streak across those consumers"},
	},
	componentStreams: {
		{name: metricStreamsConsumers, key: gaugeStreamsConsumersKey, unit: unitConsumer, description: "Native stream consumers running on this process"},
		{name: metricStreamsPublishers, key: gaugeStreamsPublishersKey, unit: "{publisher}", description: "Native stream publishers open on this process"},
	},
}

// gaugeSources are the in-memory reads the gauge callback makes: the last verdict per kind, and
// the slot list, through which each kind's own counters are reached at collection time. The slot
// list and not the managers, so every absence the callback must survive is already spelled at the
// slot — an unconfigured kind seals a disabled description carrying no statistics, and the native
// streams lane that never started (no runtime and no URI, no declarations, a failed start) seals
// no description at all.
type gaugeSources struct {
	verdicts *verdictStore
	slots    []resourceSlot
}

// registerRuntimeGauges publishes the signal that replaces the statistics the /ready body
// carried before ADR-120 trimmed it: each kind's last readiness verdict, and the manager
// counters OTel lacks.
//
// The callback runs no probe and makes no I/O — it reads what a judgment already decided and
// what the managers already hold in memory. One read is not free: streams' Stats() takes that
// manager's exclusive mutex and builds a stored_offsets map this gauge discards
// (messaging/streams/manager.go:869-891). At a collection interval that is acceptable; on a
// request path it would not be.
//
// The returned func unregisters the callback; Shutdown calls it before the slots stop.
func registerRuntimeGauges(meter metric.Meter, sources gaugeSources) (func() error, error) {
	status, err := meter.Int64ObservableGauge(metricReadinessStatus,
		metric.WithDescription("Last readiness verdict per component: 1 healthy, 0 unhealthy"),
		metric.WithUnit("{status}"))
	if err != nil {
		return nil, fmt.Errorf("app: create %s failed: %w", metricReadinessStatus, err)
	}

	observables := []metric.Observable{status}
	gauges := make(map[string][]statGauge, len(statGaugeSpecs))
	for kind, specs := range statGaugeSpecs {
		built, buildErr := buildStatGauges(meter, specs)
		if buildErr != nil {
			return nil, buildErr
		}
		gauges[kind] = built
		for _, g := range built {
			observables = append(observables, g.gauge)
		}
	}

	observe := func(_ context.Context, observer metric.Observer) error {
		observeVerdicts(observer, status, sources.verdicts)
		observeSlotStats(observer, sources.slots, gauges)
		return nil
	}

	registration, err := meter.RegisterCallback(observe, observables...)
	if err != nil {
		// RegisterCallback hands back a LIVE registration alongside its error when it accepted
		// some observables and rejected others: the accepted ones are already firing while the
		// rejections are joined into err (sdk/metric/meter.go). This path returns no cleanup, so
		// a registration left standing would go on observing the slots for the rest of the
		// process — past Shutdown, which would have nothing to stop it with.
		// Unregister's own error is dropped: both SDK registrations (unregisterFuncs and
		// noopRegister) always return nil, and this App only ever holds an SDK or noop provider.
		if registration != nil {
			_ = registration.Unregister()
		}
		return nil, fmt.Errorf("app: register runtime gauges failed: %w", err)
	}
	return registration.Unregister, nil
}

// buildStatGauges constructs one instrument per gauge, carrying the rest of each description
// through unchanged. A failed constructor aborts the whole set: the SDK may hand back a usable
// instrument together with an error, and a callback registered over a half-built set would
// observe an instrument whose construction failed.
func buildStatGauges(meter metric.Meter, specs []statGauge) ([]statGauge, error) {
	built := make([]statGauge, 0, len(specs))
	for _, spec := range specs {
		gauge, err := meter.Int64ObservableGauge(spec.name,
			metric.WithDescription(spec.description),
			metric.WithUnit(spec.unit))
		if err != nil {
			return nil, fmt.Errorf("app: create %s failed: %w", spec.name, err)
		}
		spec.gauge = gauge
		built = append(built, spec)
	}
	return built, nil
}

// observeVerdicts reports one series per kind whose last verdict was healthy or unhealthy. Every
// other status — disabled, not_configured, per_tenant — and a kind no judgment has reached yet
// are observed as nothing at all, which drops the series entirely: a dashboard never shows an
// unused kind as healthy, and a deploy never starts at 0 (ADR-120).
func observeVerdicts(observer metric.Observer, gauge metric.Int64ObservableGauge, verdicts *verdictStore) {
	for _, reading := range verdicts.readings() {
		value, ok := readinessGaugeValue(reading.status)
		if !ok {
			continue
		}
		observer.ObserveInt64(gauge, value, metric.WithAttributes(
			attribute.String(attrReadinessKind, reading.kind),
			attribute.Bool(attrReadinessCritical, reading.critical),
		))
	}
}

// readinessGaugeValue maps a verdict onto the gauge and reports whether the kind has a series at
// all. isFailing is the one predicate the /ready gate and the debug summary share
// (readiness_render.go), and the gauge is the third view of the same verdict.
func readinessGaugeValue(status string) (value int64, ok bool) {
	switch {
	case status == healthyStatus:
		return 1, true
	case isFailing(status):
		return 0, true
	default:
		return 0, false
	}
}

// observeSlotStats reports the manager counters through the slot list: a kind with a gauge group
// hands over the same Stats() snapshot /ready renders. A kind that sealed no description — the
// native streams lane that never started — contributes nothing, and so does a kind whose manager
// was never built, whose disabled description carries no statistics at all. The group is
// resolved before the snapshot is taken, so the kinds with no gauges (database, cache) never pay
// for a Stats() call this callback would discard.
func observeSlotStats(observer metric.Observer, slots []resourceSlot, gauges map[string][]statGauge) {
	for _, slot := range slots {
		description := slot.readiness()
		if description == nil {
			continue
		}
		group, ok := gauges[description.name]
		if !ok {
			continue
		}
		observeStats(observer, group, description.snapshot())
	}
}

// observeStats reports one kind's counters, skipping a single key the manager no longer
// publishes under that name — and, for a kind whose snapshot is nil, every key it would feed.
func observeStats(observer metric.Observer, gauges []statGauge, stats map[string]any) {
	for _, g := range gauges {
		value, ok := int64Stat(stats, g.key)
		if !ok {
			continue
		}
		observer.ObserveInt64(g.gauge, value)
	}
}

// int64Stat reads one counter out of a manager's Stats() map. The maps are map[string]any and
// each manager chooses its own numeric type — consumer_resubscribes is a uint64 while its four
// neighbors are ints — so the type is asserted per key. A key that is missing, retyped, or too
// large to carry reports nothing rather than a zero, which would read as an idle consumer.
func int64Stat(stats map[string]any, key string) (int64, bool) {
	switch value := stats[key].(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case uint64:
		if value > math.MaxInt64 {
			return 0, false
		}
		return int64(value), true
	default:
		return 0, false
	}
}

// startRuntimeGauges registers the gauges once every kind has started — the point at which the
// streams manager exists if it exists at all (app/streams_setup.go) — and once no startup step
// can still fail, which is why prepareRuntime calls it last. Reported and never fatal: a service
// that runs without publishing its readiness gauge is worse observed, not broken.
//
// The absent provider is the only guard: it is a hand-built App, which the framework never
// produces. Both observability.Provider implementations normalize their meter provider
// (observability/provider.go, noop.go), and bootstrap hands that same value straight into
// ModuleDeps.MeterProvider, where inbox calls Meter on it unguarded.
func (a *App) startRuntimeGauges() {
	if a.observability == nil {
		return
	}

	meter := a.observability.MeterProvider().Meter(appMeterName)
	unregister, err := registerRuntimeGauges(meter, a.gaugeSources())
	if err != nil {
		a.logger.Warn().Err(err).Msg("Readiness gauges unavailable")
		return
	}
	a.unregisterGauges = unregister
}

// gaugeSources binds the callback to this App's store and slot list. Each slot is asked for its
// sealed description at collection time rather than at registration, so the callback reads
// whatever the start walk sealed.
func (a *App) gaugeSources() gaugeSources {
	return gaugeSources{verdicts: a.verdicts, slots: a.slots}
}

// stopRuntimeGauges unregisters the callback. Shutdown calls it before the slots stop, so no
// collection can land inside a manager's own teardown — see Shutdown's step 2.
func (a *App) stopRuntimeGauges() {
	if a.unregisterGauges == nil {
		return
	}
	if err := a.unregisterGauges(); err != nil {
		// Reported, not returned: a gauge that outlives its managers is a metrics problem, and
		// failing shutdown over it would leave the rest of the teardown undone.
		a.logger.Warn().Err(err).Msg("Failed to unregister readiness gauges")
	}
	a.unregisterGauges = nil
}
