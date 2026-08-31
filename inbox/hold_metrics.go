package inbox

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gaborage/go-bricks/messaging/streams"
)

// The hold's gauges. One reading per consumer, attributed by name, because a
// deployment's consumers hold independently and an operator acts on one of them.
const (
	metricHoldTenants   = "inbox.hold.tenants"
	metricHoldRows      = "inbox.hold.rows"
	metricHoldOldestAge = "inbox.hold.oldest_age"

	attrHoldConsumer = streams.AttrConsumerName
)

// registerHoldGauges publishes what the drain last saw. The readings come from
// the snapshot each pass stores, never from the ledger: an observable callback
// fires on the exporter's own schedule, and a database read on that path would
// make the metrics pipeline a source of load on the control-plane database.
//
// registerHoldGauges registers observable gauges for held tenants, held message
// rows, and the age of the oldest hold, labeled by consumer. It returns a
// function that unregisters the observation callback and an error if gauge
// creation or callback registration fails.
func registerHoldGauges(meter metric.Meter, drain *HoldDrain) (func() error, error) {
	tenants, err := meter.Int64ObservableGauge(metricHoldTenants,
		metric.WithDescription("Tenants currently held on this consumer"))
	if err != nil {
		return nil, fmt.Errorf("inbox hold: create %s failed: %w", metricHoldTenants, err)
	}

	rows, err := meter.Int64ObservableGauge(metricHoldRows,
		metric.WithDescription("Held messages waiting to be replayed on this consumer"))
	if err != nil {
		return nil, fmt.Errorf("inbox hold: create %s failed: %w", metricHoldRows, err)
	}

	oldest, err := meter.Float64ObservableGauge(metricHoldOldestAge,
		metric.WithDescription("Age of the oldest hold on this consumer"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, fmt.Errorf("inbox hold: create %s failed: %w", metricHoldOldestAge, err)
	}

	observe := func(_ context.Context, observer metric.Observer) error {
		for consumer, stats := range drain.snapshots() {
			if stats == nil {
				// No pass has visited this consumer yet. Reporting zero would claim
				// nothing is held, which no pass has established.
				continue
			}

			attrs := metric.WithAttributes(attribute.String(attrHoldConsumer, consumer))
			observer.ObserveInt64(tenants, stats.Tenants, attrs)
			observer.ObserveInt64(rows, stats.Rows, attrs)
			observer.ObserveFloat64(oldest, holdAgeSeconds(drain.now, stats.OldestHeldSince), attrs)
		}
		return nil
	}

	registration, err := meter.RegisterCallback(observe, tenants, rows, oldest)
	if err != nil {
		return nil, fmt.Errorf("inbox hold: register gauges failed: %w", err)
	}
	return registration.Unregister, nil
}

// holdAgeSeconds renders the oldest hold's age. An empty hold has no oldest
// holdAgeSeconds calculates the age of the oldest held item in seconds.
// It returns zero when no oldest hold is recorded or when the timestamp is
// slightly in the future.
func holdAgeSeconds(now func() time.Time, oldest time.Time) float64 {
	if oldest.IsZero() {
		return 0
	}
	// The row's timestamp is the ledger's clock and now() is this process's, so a
	// held_since slightly in the future is a clock difference, not a negative age.
	return max(now().Sub(oldest).Seconds(), 0)
}
