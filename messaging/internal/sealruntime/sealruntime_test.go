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

func TestConfigureCopiesAndReplaces(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	assert.Nil(t, Configured())
	rt := &Runtime{Active: map[string]string{"fam": "v2"}, Tenancy: TenancyShared}
	Configure(rt)
	rt.Tenancy = TenancyPerTenant // caller mutation after Configure must not leak
	rt.Active["fam"] = "v9"       // nor a later write through the caller's map
	got := Configured()
	require.NotNil(t, got)
	assert.Equal(t, TenancyShared, got.Tenancy)
	assert.Equal(t, "v2", got.Active["fam"])
	got.Active["fam"] = "v8" // nor a write through a returned snapshot
	assert.Equal(t, "v2", Configured().Active["fam"])
	Configure(&Runtime{})
	assert.Nil(t, Configured().Active, "a nil selector stays nil")
	Configure(&Runtime{Tenancy: TenancyPerTenant})
	assert.Equal(t, TenancyPerTenant, Configured().Tenancy, "the app is the single writer and may replace")
	assert.PanicsWithValue(t, "sealruntime: Configure called with nil", func() { Configure(nil) })
}

func TestResetClearsEverything(t *testing.T) {
	Register(fakeCodec{})
	Configure(&Runtime{})
	Reset()
	assert.Nil(t, Registered())
	assert.Nil(t, Configured())
}

func TestOpenRefusedErrorRendersCodeAndSortedDetailsOnly(t *testing.T) {
	cause := errors.New("codec detail")
	e := &OpenRefusedError{Code: "SEAL_HEADER_SLOT_INVALID", Details: map[string]string{"slot": "jti", "present": "false"}, Cause: cause}
	assert.Equal(t, "sealed open refused: SEAL_HEADER_SLOT_INVALID (present=false, slot=jti)", e.Error())
	assert.ErrorIs(t, e, cause)
	assert.Equal(t, "sealed open refused: NOT_SEALED", (&OpenRefusedError{Code: "NOT_SEALED"}).Error())
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

func TestInstrumentsAreNoopUntilConfiguredAndNilSafe(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	m := Instruments()
	require.NotNil(t, m)
	m.RecordOperation(context.Background(), OpSeal, time.Millisecond)
	m.RecordOpenFailure(context.Background(), "SEAL_X")
	var nilMetrics *Metrics
	nilMetrics.RecordOperation(context.Background(), OpOpen, time.Millisecond)
	nilMetrics.RecordOpenFailure(context.Background(), "SEAL_X")
}

func TestInstrumentsRecordThroughTheConfiguredMeter(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	reader := sdkmetric.NewManualReader()
	Configure(&Runtime{Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))})
	m := Instruments()
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
