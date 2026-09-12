package auth

import (
	"context"
	"errors"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meterName follows the go-bricks/<module> convention every other package's
// meter uses.
const meterName = "go-bricks/auth"

// Metric names. There is no OTel semantic convention for credential
// verification, so the names follow the house shape instead: a dotted namespace
// per subject, ".total" on a monotonic counter, as in job.execution.total.
const (
	metricVerificationTotal  = "auth.verification.total"
	metricKeySetRefreshTotal = "auth.keyset.refresh.total"
	metricKeySetAge          = "auth.keyset.age"
	metricKeySetKeyCount     = "auth.keyset.key.count"
)

// Attribute keys. error.type is the OTel-conventional spelling; auth.result
// mirrors the scheduler's job.status — one low-cardinality outcome dimension.
const (
	attrAuthResult = "auth.result"
	attrErrorType  = "error.type"
)

// auth.result values that are not a Class. Every other value is a Class
// constant rendered as a string, so the verification counter's dimension is the
// same vocabulary the rejection log carries.
const (
	resultSuccess           = "success"
	resultFailure           = "failure"
	resultMissingCredential = "missing_credential"
)

// error.type values on a failed key-set refresh. The set is closed so the
// attribute stays low-cardinality: it names the STAGE that failed, never the
// underlying error text.
const (
	refreshErrorTransport = "transport"
	refreshErrorStatus    = "status"
	refreshErrorOversized = "oversized"
	refreshErrorParse     = "parse"
	refreshErrorEmpty     = "empty_key_set"
)

// authMetrics holds the package's OTel instruments.
//
// Unlike httpclient's tracking package, which memoizes one meter off the global
// otel.Meter, the instruments here hang off the MeterProvider the caller passed
// to NewVerifier and are per-verifier. A nil provider falls back to
// otel.GetMeterProvider(), which is exactly what otel.Meter resolves to, so the
// precedent's behavior is the default and an explicit provider simply wins over
// it. Nothing is package-global, so a test needs no reset hook.
//
// Every method tolerates a nil receiver: a verifier built through
// NewVerifierWithResolver records nothing.
type authMetrics struct {
	meter         metric.Meter
	verifications metric.Int64Counter
	refreshes     metric.Int64Counter
}

// newAuthMetrics builds the counters. Instrument creation failures are logged
// to stderr and leave the instrument nil: telemetry must never fail a
// verification, and a nil instrument is simply not recorded.
func newAuthMetrics(mp metric.MeterProvider) *authMetrics {
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	meter := mp.Meter(meterName)
	m := &authMetrics{meter: meter}

	var err error
	m.verifications, err = meter.Int64Counter(
		metricVerificationTotal,
		metric.WithDescription("Total bearer credential verifications by outcome"),
		metric.WithUnit("{verification}"),
	)
	logMetricError(metricVerificationTotal, err)

	m.refreshes, err = meter.Int64Counter(
		metricKeySetRefreshTotal,
		metric.WithDescription("Total issuer key set refresh attempts by outcome"),
		metric.WithUnit("{refresh}"),
	)
	logMetricError(metricKeySetRefreshTotal, err)

	return m
}

// logMetricError reports an instrument initialization failure on stderr, the
// same best-effort channel the httpclient and database trackers use. Metric
// wiring must never crash a service.
func logMetricError(name string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Failed to initialize metric %s: %v\n", name, err)
	}
}

// recordVerification counts one verification outcome. err is the value Verify
// is about to return; nil counts as a success.
//
// SECURITY: only the outcome label is recorded. err itself is never rendered
// into an attribute — a *VerificationError's Cause routinely embeds the
// credential it failed on.
func (m *authMetrics) recordVerification(ctx context.Context, err error) {
	if m == nil || m.verifications == nil {
		return
	}
	m.verifications.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attrAuthResult, verificationResult(err)),
	))
}

// verificationResult maps a Verify outcome onto its low-cardinality label. Every
// rejection class reaches it through *VerificationError; the two sentinels
// outside that chain get their own labels.
func verificationResult(err error) string {
	if err == nil {
		return resultSuccess
	}
	var verr *VerificationError
	if errors.As(err, &verr) {
		return string(verr.Class)
	}
	switch {
	case errors.Is(err, ErrMissingCredential):
		return resultMissingCredential
	case errors.Is(err, ErrKeySetUnavailable):
		return string(ClassKeySetUnavailable)
	default:
		return resultFailure
	}
}

// recordRefresh counts one key-set refresh attempt. errType is empty on
// success and one of the refreshError* constants otherwise.
func (m *authMetrics) recordRefresh(ctx context.Context, errType string) {
	if m == nil || m.refreshes == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String(attrAuthResult, resultSuccess)}
	if errType != "" {
		attrs = []attribute.KeyValue{
			attribute.String(attrAuthResult, resultFailure),
			attribute.String(attrErrorType, errType),
		}
	}
	m.refreshes.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// keySetState is what the observable gauges read: the current key count and the
// age of the key set, in seconds. ok is false before the first successful fetch,
// which suppresses both observations rather than reporting a zero age.
type keySetState interface {
	keySetObservation() (keys int64, ageSeconds float64, ok bool)
}

// registerKeySetGauges wires the key-count and key-set-age gauges to state and
// returns the cleanup that unregisters them. Registration failures degrade to a
// no-op cleanup, matching the database tracker's graceful-degradation contract.
func (m *authMetrics) registerKeySetGauges(state keySetState) func() {
	if m == nil || m.meter == nil {
		return func() {}
	}

	keyCount, err := m.meter.Int64ObservableGauge(
		metricKeySetKeyCount,
		metric.WithDescription("Number of usable RSA keys in the cached issuer key set"),
		metric.WithUnit("{key}"),
	)
	logMetricError(metricKeySetKeyCount, err)

	age, err := m.meter.Float64ObservableGauge(
		metricKeySetAge,
		metric.WithDescription("Age of the cached issuer key set since its last successful fetch"),
		metric.WithUnit("s"),
	)
	logMetricError(metricKeySetAge, err)

	if keyCount == nil || age == nil {
		return func() {}
	}

	registration, err := m.meter.RegisterCallback(
		func(_ context.Context, observer metric.Observer) error {
			keys, ageSeconds, ok := state.keySetObservation()
			if !ok {
				return nil
			}
			observer.ObserveInt64(keyCount, keys)
			observer.ObserveFloat64(age, ageSeconds)
			return nil
		},
		keyCount, age,
	)
	if err != nil {
		logMetricError("auth_keyset_callback", err)
		return func() {}
	}
	return func() {
		if err := registration.Unregister(); err != nil {
			logMetricError("auth_keyset_unregister", err)
		}
	}
}
