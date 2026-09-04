package inbox

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// TestModuleInitRegistersDedupCounterOnlyWithAMeter pins the two wirings: a
// meter provider yields the counter, its absence leaves the log line alone to
// carry a dedup hit rather than failing Init.
func TestModuleInitRegistersDedupCounterOnlyWithAMeter(t *testing.T) {
	withMeter := NewModule()
	deps := testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true}}
	deps.MeterProvider = obtest.NewTestMeterProvider()
	require.NoError(t, withMeter.Init(deps))
	assert.NotNil(t, withMeter.dedupHits)

	withoutMeter := NewModule()
	deps = testDeps()
	deps.Config = &config.Config{Inbox: config.InboxConfig{Enabled: true}}
	require.NoError(t, withoutMeter.Init(deps))
	assert.Nil(t, withoutMeter.dedupHits)
}

// TestProcessOnceDedupHitIsCountedAndLogged pins the short-circuit's two
// observables: the counter moves by exactly one per hit, labeled by tenant
// PRESENCE and sealed=false, and one log line carries the id's length — the id
// value itself appears nowhere.
func TestProcessOnceDedupHitIsCountedAndLogged(t *testing.T) {
	const replayed = "replayed-id-QZX7"
	for _, tc := range []struct {
		name          string
		ctx           context.Context
		tenantPresent bool
	}{
		{"single_tenant", t.Context(), false},
		{"tenant", multitenant.SetTenant(t.Context(), "acme"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
			db.ExpectTransaction().ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(0)
			in := newTestInbox(db)

			mp := obtest.NewTestMeterProvider()
			in.module.registerDedupCounter(&app.ModuleDeps{MeterProvider: mp})
			require.NotNil(t, in.module.dedupHits)
			// One hit before the measured one, so the assertion below is a delta.
			in.module.dedupHits.Add(t.Context(), 1)
			before := sumOfDedupHits(t, mp)

			var err error
			line := captureDrainLogs(t, func() {
				// The framework logger binds os.Stdout at construction, so it is built
				// inside the capture, after the swap.
				in.module.logger = logger.New("info", false)
				err = in.ProcessOnce(tc.ctx, replayed, func(context.Context, dbtypes.Tx) error {
					t.Error("fn must not run on a dedup hit")
					return nil
				})
			})
			require.NoError(t, err)

			assert.Equal(t, before+1, sumOfDedupHits(t, mp), "exactly one hit is counted")
			assertDedupHitAttributes(t, mp, tc.tenantPresent)

			assert.Contains(t, line, "Inbox dedup hit")
			assert.Contains(t, line, `"eventIdLength":16`)
			assert.Contains(t, line, `"tenantPresent":`+strconv.FormatBool(tc.tenantPresent))
			assert.Contains(t, line, `"sealed":false`)
			assert.NotContains(t, line, replayed, "the id value never reaches the log")
			assert.NotContains(t, line, "acme", "the tenant id never reaches the log")
		})
	}
}

// sumOfDedupHits reads the counter's total across every attribute set.
func sumOfDedupHits(t *testing.T, mp *obtest.TestMeterProvider) int64 {
	t.Helper()
	m := obtest.FindMetric(mp.Collect(t), metricDedupHits)
	require.NotNil(t, m, "the dedup-hit counter is exported")
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "the dedup-hit instrument is an int64 sum")
	var total int64
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	return total
}

// assertDedupHitAttributes pins the label set on the data point ProcessOnce
// wrote: tenant presence as a bool and sealed=false.
func assertDedupHitAttributes(t *testing.T, mp *obtest.TestMeterProvider, tenantPresent bool) {
	t.Helper()
	m := obtest.FindMetric(mp.Collect(t), metricDedupHits)
	require.NotNil(t, m)
	sum := m.Data.(metricdata.Sum[int64])
	for _, dp := range sum.DataPoints {
		tenant, hasTenant := dp.Attributes.Value(attribute.Key(attrTenantPresent))
		if !hasTenant {
			continue // the priming Add carried no attributes
		}
		assert.Equal(t, tenantPresent, tenant.AsBool())
		sealed, hasSealed := dp.Attributes.Value(attribute.Key(attrSealed))
		require.True(t, hasSealed)
		assert.False(t, sealed.AsBool())
		assert.Equal(t, int64(1), dp.Value)
		return
	}
	t.Fatal("no attributed dedup-hit data point was recorded")
}
