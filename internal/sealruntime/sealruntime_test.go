package sealruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type fakeCodec struct{ name string }

func (fakeCodec) ScanType(reflect.Type) (Spec, error)              { return nil, nil }
func (fakeCodec) NewSealer(Spec, string, *Runtime) (Sealer, error) { return nil, nil }

func TestRegisterIsSetOnce(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	assert.Nil(t, Registered())
	Register(fakeCodec{name: "a"})
	assert.Equal(t, fakeCodec{name: "a"}, Registered())
	assert.PanicsWithValue(t, "sealruntime: sealing codec already registered", func() { Register(fakeCodec{name: "b"}) })
	assert.Equal(t, fakeCodec{name: "a"}, Registered(), "second registration must not replace the first")
	assert.PanicsWithValue(t, "sealruntime: Register called with nil", func() { Register(nil) })
}

// boundCall is one call a capturing binder received.
type boundCall struct {
	decls any
	rt    *Runtime
}

// captureBinder installs a binder that records what Bind hands it and restores the
// previous binder at cleanup.
func captureBinder(t *testing.T) *[]boundCall {
	t.Helper()
	prev := binder.Swap(nil)
	t.Cleanup(func() { binder.Store(prev) })
	var calls []boundCall
	RegisterBinder(func(decls any, rt *Runtime) { calls = append(calls, boundCall{decls: decls, rt: rt}) })
	return &calls
}

func TestBindHandsTheBinderACopyWithItsOwnInstruments(t *testing.T) {
	calls := captureBinder(t)
	mp := sdkmetric.NewMeterProvider()
	preset := NewMetrics(nil)
	rt := &Runtime{Active: map[string]string{"fam": "v2"}, Tenancy: TenancyShared, Meter: mp, Metrics: preset}
	decls := &struct{ name string }{name: "a"}
	Bind(decls, rt)
	rt.Tenancy = TenancyPerTenant // caller mutation after Bind must not leak
	rt.Active["fam"] = "v9"       // nor a later write through the caller's map
	require.Len(t, *calls, 1)
	got := (*calls)[0]
	assert.Same(t, decls, got.decls)
	assert.Equal(t, TenancyShared, got.rt.Tenancy)
	assert.Equal(t, "v2", got.rt.Active["fam"])
	assert.Equal(t, mp, got.rt.Meter)
	require.NotNil(t, got.rt.Metrics)
	assert.NotSame(t, preset, got.rt.Metrics, "Bind builds the instruments from the meter")
	Bind(decls, rt)
	assert.NotSame(t, got.rt.Metrics, (*calls)[1].rt.Metrics, "every bind builds its own instruments")
	Bind(decls, &Runtime{})
	assert.Nil(t, (*calls)[2].rt.Active, "a nil selector stays nil")
	assert.PanicsWithValue(t, "sealruntime: Bind called with nil", func() { Bind(decls, nil) })
}

func TestBindWithoutABinderPanics(t *testing.T) {
	prev := binder.Swap(nil)
	t.Cleanup(func() { binder.Store(prev) })
	assert.PanicsWithValue(t, "sealruntime: no declarations binder registered (package messaging not linked)", func() { Bind(nil, &Runtime{}) })
}

func TestRegisterBinderIsSetOnce(t *testing.T) {
	captureBinder(t)
	assert.PanicsWithValue(t, "sealruntime: declarations binder already registered", func() { RegisterBinder(func(any, *Runtime) {}) })
	assert.PanicsWithValue(t, "sealruntime: RegisterBinder called with nil", func() { RegisterBinder(nil) })
}

func TestSnapshotOwnsItsSelector(t *testing.T) {
	rt := &Runtime{Active: map[string]string{"fam": "v2"}, Tenancy: TenancyShared}
	got := rt.Snapshot()
	got.Active["fam"] = "v8"
	got.Tenancy = TenancyDisabled
	assert.Equal(t, "v2", rt.Active["fam"])
	assert.Equal(t, TenancyShared, rt.Tenancy)
	assert.Nil(t, (&Runtime{}).Snapshot().Active)
}

func TestResetClearsTheCodecOnly(t *testing.T) {
	captureBinder(t)
	Register(fakeCodec{})
	Reset()
	assert.Nil(t, Registered())
	assert.NotNil(t, binder.Load(), "the binder outlives Reset, so a test that re-registers a codec can still bind")
}

func TestOpenRefusedErrorRendersCodeAndSortedDetailsOnly(t *testing.T) {
	cause := errors.New("codec detail")
	e := &OpenRefusedError{Code: "SEAL_HEADER_SLOT_INVALID", Details: map[string]string{"slot": "jti", "present": "false"}, Cause: cause}
	assert.Equal(t, "sealed open refused: SEAL_HEADER_SLOT_INVALID (present=false, slot=jti)", e.Error())
	assert.Equal(t, "sealed open refused: NOT_SEALED", (&OpenRefusedError{Code: "NOT_SEALED"}).Error())
	require.ErrorIs(t, e, cause)
	var nilErr *OpenRefusedError
	assert.Equal(t, "<nil>", nilErr.Error())
	assert.NoError(t, nilErr.Unwrap())
}

func TestTenancyString(t *testing.T) {
	assert.Equal(t, "disabled", TenancyDisabled.String())
	assert.Equal(t, "shared", TenancyShared.String())
	assert.Equal(t, "per-tenant", TenancyPerTenant.String())
	assert.Equal(t, "unknown", Tenancy(9).String())
}

func TestMetricsAreNoopWithoutAMeterAndNilSafe(t *testing.T) {
	m := NewMetrics(nil)
	require.NotNil(t, m)
	m.RecordOperation(context.Background(), OpSeal, time.Millisecond)
	m.RecordOpenFailure(context.Background(), "SEAL_X")
	var nilMetrics *Metrics
	nilMetrics.RecordOperation(context.Background(), OpOpen, time.Millisecond)
	nilMetrics.RecordOpenFailure(context.Background(), "SEAL_X")
}

func TestBoundMetricsRecordThroughTheBoundMeter(t *testing.T) {
	calls := captureBinder(t)
	reader := sdkmetric.NewManualReader()
	Bind(nil, &Runtime{Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))})
	require.Len(t, *calls, 1)
	m := (*calls)[0].rt.Metrics
	m.RecordOperation(context.Background(), OpSeal, 250*time.Millisecond)
	m.RecordOperation(context.Background(), OpOpen, 50*time.Millisecond)
	m.RecordOpenFailure(context.Background(), "SEAL_SIGNATURE_INVALID")
	m.RecordOpenFailure(context.Background(), "SEAL_SIGNATURE_INVALID")

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	byName := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		assert.Equal(t, MeterName, sm.Scope.Name)
		for _, met := range sm.Metrics {
			byName[met.Name] = met
		}
	}
	hist, ok := byName[MetricOperationDuration].Data.(metricdata.Histogram[float64])
	require.True(t, ok, "duration must be a float64 histogram")
	require.Len(t, hist.DataPoints, 2, "one series per op attribute")
	ops := map[string]uint64{}
	for _, dp := range hist.DataPoints {
		op, _ := dp.Attributes.Value(AttrOperation)
		ops[op.AsString()] = dp.Count
	}
	assert.Equal(t, map[string]uint64{OpSeal: 1, OpOpen: 1}, ops)
	sum, ok := byName[MetricOpenFailures].Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)
	code, _ := sum.DataPoints[0].Attributes.Value(AttrCode)
	assert.Equal(t, "SEAL_SIGNATURE_INVALID", code.AsString())
	assert.Equal(t, int64(2), sum.DataPoints[0].Value)
}
