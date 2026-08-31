package inbox

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// TestHoldGaugesReportTheDrainsSnapshot pins what an operator sees: the tenants
// held, the rows behind them, and how old the oldest hold is — read from the
// snapshot the drain wrote, never from the database, because a gauge callback
// fires on the exporter's schedule and must not put a query on that path.
func TestHoldGaugesReportTheDrainsSnapshot(t *testing.T) {
	mp := obtest.NewTestMeterProvider()
	drain := &HoldDrain{now: time.Now}
	unregister, err := registerHoldGauges(mp.Meter("test"), drain)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unregister()) })

	drain.setSnapshot(testHoldConsumer, &HoldStats{
		Tenants: 2, Rows: 5, OldestHeldSince: time.Now().Add(-90 * time.Second),
	})

	rm := mp.Collect(t)

	obtest.AssertMetricValue(t, rm, metricHoldTenants, int64(2))
	obtest.AssertMetricValue(t, rm, metricHoldRows, int64(5))
	age := obtest.FindMetric(rm, metricHoldOldestAge)
	require.NotNil(t, age, "the oldest-age gauge is reported")
}

// TestHoldGaugesReportNothingBeforeAPass pins that a consumer the drain has not
// visited yet publishes no reading at all — a zero would read as "nothing held",
// which is a claim no pass has made.
func TestHoldGaugesReportNothingBeforeAPass(t *testing.T) {
	mp := obtest.NewTestMeterProvider()
	drain := &HoldDrain{now: time.Now}
	unregister, err := registerHoldGauges(mp.Meter("test"), drain)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unregister()) })

	rm := mp.Collect(t)

	assert.Nil(t, obtest.FindMetric(rm, metricHoldTenants),
		"no pass, no reading")
}

// TestHoldGaugesStopAfterUnregister pins the module's shutdown: the callback is
// unregistered, so a later collection reports nothing.
func TestHoldGaugesStopAfterUnregister(t *testing.T) {
	mp := obtest.NewTestMeterProvider()
	drain := &HoldDrain{now: time.Now}
	unregister, err := registerHoldGauges(mp.Meter("test"), drain)
	require.NoError(t, err)
	drain.setSnapshot(testHoldConsumer, &HoldStats{Tenants: 1, Rows: 1})

	require.NoError(t, unregister())
	rm := mp.Collect(t)

	assert.Nil(t, obtest.FindMetric(rm, metricHoldTenants),
		"an unregistered callback reports nothing")
}
