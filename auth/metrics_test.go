package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	authtesting "github.com/gaborage/go-bricks/auth/testing"
	obstesting "github.com/gaborage/go-bricks/observability/testing"
)

// newMeteredVerifier builds a JWKS-backed verifier over a manual-reader meter
// provider, so every instrument this package declares is collected in-process.
func newMeteredVerifier(t *testing.T, srv *authtesting.JWKSServer) (*Verifier, *obstesting.TestMeterProvider) {
	t.Helper()
	mp := obstesting.NewTestMeterProvider()
	v, err := NewVerifier(jwksConfig(srv), nil, mp.MeterProvider, jwksClient(t, srv))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, v.Close()) })
	v.now = fixedClock
	return v, mp
}

// counterValue reads one data point of an Int64 counter, selected by its
// attributes. It fails the test when the metric or the point is absent, so a
// renamed metric cannot pass as a zero.
func counterValue(t *testing.T, rm metricdata.ResourceMetrics, name string, attrs ...attribute.KeyValue) int64 {
	t.Helper()
	found := obstesting.FindMetric(rm, name)
	require.NotNil(t, found, "metric %s was not recorded", name)
	sum, ok := found.Data.(metricdata.Sum[int64])
	require.True(t, ok, "metric %s is not an int64 sum", name)
	want := attribute.NewSet(attrs...)
	for _, point := range sum.DataPoints {
		if point.Attributes.Equals(&want) {
			return point.Value
		}
	}
	t.Fatalf("metric %s carries no data point for %v", name, attrs)
	return 0
}

// gaugeValue reads the single data point of a gauge.
func gaugeValue[N int64 | float64](t *testing.T, rm metricdata.ResourceMetrics, name string) N {
	t.Helper()
	found := obstesting.FindMetric(rm, name)
	require.NotNil(t, found, "metric %s was not recorded", name)
	gauge, ok := found.Data.(metricdata.Gauge[N])
	require.True(t, ok, "metric %s is not a gauge of the expected numeric type", name)
	require.Len(t, gauge.DataPoints, 1)
	return gauge.DataPoints[0].Value
}

func TestVerifierRecordsOneVerificationPerOutcome(t *testing.T) {
	srv := newJWKSFixture(t)
	v, mp := newMeteredVerifier(t, srv)
	ctx := context.Background()

	_, err := v.Verify(ctx, srv.Issuer().Mint(authtesting.Claims{}))
	require.NoError(t, err)
	_, err = v.Verify(ctx, srv.Issuer().MintExpired())
	require.Error(t, err)
	_, err = v.Verify(ctx, "")
	require.ErrorIs(t, err, ErrMissingCredential)

	rm := mp.Collect(t)

	assert.Equal(t, int64(1), counterValue(t, rm, metricVerificationTotal, attribute.String(attrAuthResult, resultSuccess)))
	assert.Equal(t, int64(1), counterValue(t, rm, metricVerificationTotal, attribute.String(attrAuthResult, string(ClassExpired))))
	assert.Equal(t, int64(1), counterValue(t, rm, metricVerificationTotal, attribute.String(attrAuthResult, resultMissingCredential)))
}

func TestVerifierRecordsTheKeySetRefreshOutcome(t *testing.T) {
	srv := newJWKSFixture(t)
	v, mp := newMeteredVerifier(t, srv)
	clock := newFakeClock()
	installResolverClock(t, v, clock)

	// The construction fetch is the success; a lookup past the stale ceiling
	// against a dead issuer is the failure.
	srv.SetMode(authtesting.JWKSServerError)
	clock.Advance(testStaleCeiling + time.Second)
	_, err := v.Verify(context.Background(), srv.Issuer().Mint(authtesting.Claims{}))
	require.ErrorIs(t, err, ErrKeySetUnavailable)

	rm := mp.Collect(t)

	assert.Equal(t, int64(1), counterValue(t, rm, metricKeySetRefreshTotal, attribute.String(attrAuthResult, resultSuccess)))
	assert.Equal(t, int64(1), counterValue(t, rm, metricKeySetRefreshTotal,
		attribute.String(attrAuthResult, resultFailure),
		attribute.String(attrErrorType, refreshErrorStatus),
	))
	assert.Equal(t, int64(1), counterValue(t, rm, metricVerificationTotal,
		attribute.String(attrAuthResult, string(ClassKeySetUnavailable)),
	))
}

func TestVerifierObservesTheKeySetGauges(t *testing.T) {
	srv := newJWKSFixture(t)
	v, mp := newMeteredVerifier(t, srv)
	clock := newFakeClock()
	installResolverClock(t, v, clock)
	clock.Advance(90 * time.Second)

	rm := mp.Collect(t)

	assert.Equal(t, int64(1), gaugeValue[int64](t, rm, metricKeySetKeyCount))
	assert.InDelta(t, 90.0, gaugeValue[float64](t, rm, metricKeySetAge), 0.001)
}

func TestVerifierGaugesReportNothingBeforeTheFirstFetch(t *testing.T) {
	mp := obstesting.NewTestMeterProvider()
	m := newAuthMetrics(mp.MeterProvider)
	unregister := m.registerKeySetGauges(&jwksResolver{now: time.Now})
	t.Cleanup(unregister)

	rm := mp.Collect(t)

	assert.Nil(t, obstesting.FindMetric(rm, metricKeySetKeyCount))
	assert.Nil(t, obstesting.FindMetric(rm, metricKeySetAge))
}

func TestAuthMetricsToleratesANilReceiver(t *testing.T) {
	var m *authMetrics

	assert.NotPanics(t, func() {
		m.recordVerification(context.Background(), nil)
		m.recordRefresh(context.Background(), refreshErrorParse)
		m.registerKeySetGauges(&jwksResolver{now: time.Now})()
	})
}

func TestVerifierWithoutAMeterProviderRecordsNothing(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)

	assert.Nil(t, v.metrics, "a caller-supplied resolver carries no instruments")
	assert.NotPanics(t, func() {
		_, err := v.Verify(context.Background(), iss.Mint(authtesting.Claims{}))
		require.NoError(t, err)
	})
}

func TestVerificationResultLabelsEveryOutcome(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "no_error", err: nil, want: resultSuccess},
		{name: "missing_credential", err: ErrMissingCredential, want: resultMissingCredential},
		{name: "key_set_unavailable", err: ErrKeySetUnavailable, want: string(ClassKeySetUnavailable)},
		{name: "wrapped_key_set_unavailable", err: errors.Join(errors.New("lookup"), ErrKeySetUnavailable), want: string(ClassKeySetUnavailable)},
		{name: "verification_error", err: NewVerificationError(ClassSignature, nil), want: string(ClassSignature)},
		{name: "unclassified_error", err: errors.New("something else"), want: resultFailure},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, verificationResult(tc.err))
		})
	}
}

func TestRecordRefreshLabelsSuccessWithoutAnErrorType(t *testing.T) {
	mp := obstesting.NewTestMeterProvider()
	m := newAuthMetrics(mp.MeterProvider)

	m.recordRefresh(context.Background(), "")

	rm := mp.Collect(t)
	found := obstesting.FindMetric(rm, metricKeySetRefreshTotal)
	require.NotNil(t, found)
	sum, ok := found.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)
	_, present := sum.DataPoints[0].Attributes.Value(attrErrorType)
	assert.False(t, present, "a successful refresh must not carry an error type")
}

func TestNewAuthMetricsFallsBackToTheGlobalMeterProvider(t *testing.T) {
	m := newAuthMetrics(nil)

	require.NotNil(t, m)
	assert.NotNil(t, m.meter)
	assert.NotPanics(t, func() { m.recordVerification(context.Background(), nil) })
}

func TestRegisterKeySetGaugesDegradesWithoutAMeter(t *testing.T) {
	m := &authMetrics{}

	unregister := m.registerKeySetGauges(&jwksResolver{now: time.Now})

	require.NotNil(t, unregister)
	assert.NotPanics(t, unregister)
}

// captureStderr redirects os.Stderr for the duration of fn and returns what was
// written to it. Instrument initialization failures go there, so this is how the
// package reads back what degraded telemetry actually reported.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { os.Stderr = original }()
	defer r.Close()
	os.Stderr = w

	var buf bytes.Buffer
	copied := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&buf, r)
		copied <- copyErr
	}()

	fn()

	require.NoError(t, w.Close())
	require.NoError(t, <-copied)
	return buf.String()
}

func TestLogMetricErrorReportsOnlyAFailedInitialization(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReport bool
	}{
		{name: "successful_initialization", err: nil},
		{name: "failed_initialization", err: errors.New("instrument unavailable"), wantReport: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStderr(t, func() { logMetricError(metricVerificationTotal, tc.err) })

			if !tc.wantReport {
				assert.Empty(t, out, "a successful initialization must report nothing")
				return
			}
			assert.Contains(t, out, metricVerificationTotal)
			assert.Contains(t, out, "instrument unavailable")
		})
	}
}

// failingRegistration is a metric.Registration whose Unregister always fails,
// which is the arm a real SDK registration never takes.
type failingRegistration struct {
	embedded.Registration
	err error
}

func (r failingRegistration) Unregister() error { return r.err }

// callbackFailingMeter embeds a real meter so every instrument constructor
// behaves normally, and overrides RegisterCallback alone: it returns registerErr
// when that is set, otherwise a registration whose Unregister returns
// unregisterErr.
type callbackFailingMeter struct {
	metric.Meter
	registerErr   error
	unregisterErr error
}

func (m callbackFailingMeter) RegisterCallback(
	_ metric.Callback,
	_ ...metric.Observable,
) (metric.Registration, error) {
	if m.registerErr != nil {
		return nil, m.registerErr
	}
	return failingRegistration{err: m.unregisterErr}, nil
}

// callbackFailingProvider hands out callbackFailingMeter, so newAuthMetrics
// picks it up through the MeterProvider it already takes.
type callbackFailingProvider struct {
	embedded.MeterProvider
	real          metric.MeterProvider
	registerErr   error
	unregisterErr error
}

func (p callbackFailingProvider) Meter(name string, opts ...metric.MeterOption) metric.Meter {
	return callbackFailingMeter{
		Meter:         p.real.Meter(name, opts...),
		registerErr:   p.registerErr,
		unregisterErr: p.unregisterErr,
	}
}

func TestRegisterKeySetGaugesReportsAFailedCallbackRegistration(t *testing.T) {
	mp := obstesting.NewTestMeterProvider()
	m := newAuthMetrics(callbackFailingProvider{real: mp.MeterProvider, registerErr: errors.New("callback rejected")})

	var unregister func()
	out := captureStderr(t, func() {
		unregister = m.registerKeySetGauges(&jwksResolver{now: time.Now})
	})

	require.NotNil(t, unregister)
	assert.Contains(t, out, opKeySetGaugeRegister)
	assert.NotContains(t, out, "initialize metric", "a registration failure must not read as a failed instrument initialization")
	assert.Contains(t, out, "callback rejected")
	assert.NotPanics(t, unregister)
	assert.Empty(t, captureStderr(t, unregister), "a degraded cleanup must stay silent")
}

func TestRegisterKeySetGaugesReportsAFailedUnregister(t *testing.T) {
	mp := obstesting.NewTestMeterProvider()
	m := newAuthMetrics(callbackFailingProvider{real: mp.MeterProvider, unregisterErr: errors.New("already gone")})

	var unregister func()
	registerOut := captureStderr(t, func() {
		unregister = m.registerKeySetGauges(&jwksResolver{now: time.Now})
	})
	require.NotNil(t, unregister)
	assert.Empty(t, registerOut, "a successful registration must report nothing")

	out := captureStderr(t, func() { assert.NotPanics(t, unregister) })

	assert.Contains(t, out, opKeySetGaugeUnregister)
	assert.NotContains(t, out, "initialize metric", "an unregister failure must not read as a failed instrument initialization")
	assert.Contains(t, out, "already gone")
}
