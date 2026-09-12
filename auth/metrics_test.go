package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
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

func TestLogMetricErrorIgnoresASuccessfulInitialization(t *testing.T) {
	assert.NotPanics(t, func() {
		logMetricError(metricVerificationTotal, nil)
		logMetricError(metricVerificationTotal, errors.New("instrument unavailable"))
	})
}
