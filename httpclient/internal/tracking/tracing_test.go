package tracking

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// setupTestTraceProvider installs a test trace provider as the global tracer
// provider, resets the package tracer cache, and returns the provider + a
// cleanup that restores the previous global and resets the cache again.
func setupTestTraceProvider(t *testing.T) (tp *obtest.TestTraceProvider, cleanup func()) {
	t.Helper()
	tp = obtest.NewTestTraceProvider()
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tp.TracerProvider)
	ResetTracerForTesting()
	return tp, func() {
		otel.SetTracerProvider(original)
		ResetTracerForTesting()
	}
}

func TestStartHTTPClientSpanReturnsRecordingSpan(t *testing.T) {
	_, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/v1/users")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method: "GET",
		URL:    u,
	})
	require.NotNil(t, span)
	assert.True(t, span.IsRecording(), "span should be recording with a real tracer provider")
	span.End()
}

func TestSpanNameTemplateMethodOnly(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/users")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	span.End()

	got := obtest.NewSpanCollector(t, tp.Exporter).AssertCount(1).First()
	assert.Equal(t, "HTTP GET", got.Name)
}

func TestSpanNameTemplateMethodPeer(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/users")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method:   "POST",
		URL:      u,
		PeerName: "stripe",
	})
	span.End()

	got := obtest.NewSpanCollector(t, tp.Exporter).AssertCount(1).First()
	assert.Equal(t, "POST stripe", got.Name)
}

func TestSpanNameTemplateUnknownMethod(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/users")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "WEIRD", URL: u})
	span.End()

	got := obtest.NewSpanCollector(t, tp.Exporter).AssertCount(1).First()
	assert.Equal(t, "HTTP _OTHER", got.Name,
		"non-standard methods should be canonicalized to _OTHER")
}

func TestStartHTTPClientSpanSetsBaseAttributes(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com:8443/v1/users")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method:      "POST",
		URL:         u,
		PeerName:    "stripe",
		ResendCount: 2,
	})
	span.End()

	got := obtest.NewSpanCollector(t, tp.Exporter).AssertCount(1).First()
	obtest.AssertSpanAttribute(t, &got, "peer.service", "stripe")
	obtest.AssertSpanAttribute(t, &got, "http.request.method", "POST")
	obtest.AssertSpanAttribute(t, &got, "server.address", "api.example.com")
	obtest.AssertSpanAttribute(t, &got, "server.port", int64(8443))
	obtest.AssertSpanAttribute(t, &got, "url.scheme", "https")
	obtest.AssertSpanAttribute(t, &got, "url.path", "/v1/users")
	obtest.AssertSpanAttribute(t, &got, "network.protocol.name", "http")
	obtest.AssertSpanAttribute(t, &got, "http.request.resend_count", int64(2))
}

func TestStartHTTPClientSpanOmitsPeerWhenEmpty(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("http://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	span.End()

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	for _, kv := range got.Attributes {
		assert.NotEqualf(t, "peer.service", string(kv.Key),
			"peer.service should be omitted when PeerName is empty")
	}
	// Default port for http should be 80.
	obtest.AssertSpanAttribute(t, &got, "server.port", int64(80))
}

func TestStartHTTPClientSpanOmitsResendCountWhenZero(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	span.End()

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	for _, kv := range got.Attributes {
		assert.NotEqualf(t, "http.request.resend_count", string(kv.Key),
			"resend_count should be omitted on first attempt (Do span or attempt 0)")
	}
}

func TestStartHTTPClientSpanOmitsURLPathWhenEmpty(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	span.End()

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	for _, kv := range got.Attributes {
		assert.NotEqualf(t, "url.path", string(kv.Key),
			"url.path should be omitted when the URL has no path")
	}
}

func TestEndHTTPClientSpan2xxLeavesStatusUnset(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	EndHTTPClientSpan(span, 200, "", 42, nil)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Unset)
}

func TestEndHTTPClientSpan3xxLeavesStatusUnset(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	EndHTTPClientSpan(span, 301, "", 0, nil)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Unset)
}

func TestEndHTTPClientSpan4xxLeavesStatusUnset(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	EndHTTPClientSpan(span, 404, "", 12, nil)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Unset)
	obtest.AssertSpanAttribute(t, &got, "http.response.status_code", int64(404))
}

func TestEndHTTPClientSpan5xxSetsErrorStatusWithoutException(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	EndHTTPClientSpan(span, 503, "", 0, nil)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Error)
	obtest.AssertSpanStatusDescription(t, &got, "HTTP 503")
	for _, ev := range got.Events {
		assert.NotEqualf(t, "exception", ev.Name,
			"no exception event should be recorded for 5xx (status_code carries the signal)")
	}
}

func TestEndHTTPClientSpanTransportErrorRecordsErrorAndAttr(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	transportErr := errors.New("dial: connection refused")
	EndHTTPClientSpan(span, 0, "connection_error", 0, transportErr)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Error)
	obtest.AssertSpanAttribute(t, &got, "error.type", "connection_error")

	foundException := false
	for _, ev := range got.Events {
		if ev.Name == "exception" {
			foundException = true
			break
		}
	}
	assert.True(t, foundException, "RecordError should add an exception event for transport errors")

	for _, kv := range got.Attributes {
		assert.NotEqualf(t, "http.response.status_code", string(kv.Key),
			"http.response.status_code must be omitted when statusCode is 0 (transport error)")
	}
}

func TestEndHTTPClientSpanResponseBytes(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	EndHTTPClientSpan(span, 200, "", 1024, nil)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanAttribute(t, &got, "http.response.body.size", int64(1024))
}

func TestStartHTTPClientSpanNoopProviderReturnsNonRecording(t *testing.T) {
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	ResetTracerForTesting()
	defer func() {
		otel.SetTracerProvider(original)
		ResetTracerForTesting()
	}()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	require.NotNil(t, span)
	assert.False(t, span.IsRecording(), "no-op provider should yield a non-recording span")
	// Must not panic on a non-recording span.
	EndHTTPClientSpan(span, 500, "", 0, errors.New("boom"))
}

func TestEndHTTPClientSpanNilSpanIsNoop(t *testing.T) {
	// Verify the nil-guard short-circuit: passing nil must not panic.
	require.NotPanics(t, func() {
		EndHTTPClientSpan(nil, 200, "", 0, nil)
	})
}

func TestStartHTTPClientSpanNilURLEmitsEmptyAttrs(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	// Nil URL should not panic — serverAddressPort + urlScheme + urlPath are nil-safe.
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: nil})
	span.End()

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanAttribute(t, &got, "http.request.method", "GET")
	obtest.AssertSpanAttribute(t, &got, "server.address", "")
	obtest.AssertSpanAttribute(t, &got, "url.scheme", "")
}
