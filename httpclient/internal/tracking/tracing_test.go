package tracking

import (
	"context"
	"errors"
	"net/url"
	"strings"
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

// TestEndHTTPClientSpanInterceptorErrorOn200RecordsError covers the response-build
// failure path: the wire returned a 2xx but assembly failed (interceptor error
// or body-read failure). The span must carry error.type, an exception event,
// and codes.Error so callers don't see this as a successful 200. Symmetric
// with how RecordHTTPClientMetrics records error.type in this same path.
func TestEndHTTPClientSpanInterceptorErrorOn200RecordsError(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	buildErr := errors.New("response interceptor failed: bad signature")
	EndHTTPClientSpan(span, 200, "interceptor_failed", 0, buildErr)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanAttribute(t, &got, "http.response.status_code", int64(200))
	obtest.AssertSpanAttribute(t, &got, "error.type", "interceptor_failed")
	obtest.AssertSpanStatus(t, &got, codes.Error)
	obtest.AssertSpanStatusDescription(t, &got, "interceptor_failed")

	foundException := false
	for _, ev := range got.Events {
		if ev.Name == "exception" {
			foundException = true
			break
		}
	}
	assert.True(t, foundException,
		"response-build failures must produce an exception event so the span doesn't look like a clean 200")
}

// TestEndHTTPClientSpan5xxWithErrorRecordsErrorTypeAndException covers the
// parent Do-span path when the final result is a 5xx HTTPError after retries
// are exhausted: finalErr carries the wrapped HTTPError, finalErrType is set.
// The span must surface both alongside the "HTTP 5xx" status description.
func TestEndHTTPClientSpan5xxWithErrorRecordsErrorTypeAndException(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	terminalErr := errors.New("HTTP request failed with status 503")
	EndHTTPClientSpan(span, 503, "_OTHER", 0, terminalErr)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Error)
	obtest.AssertSpanStatusDescription(t, &got, "HTTP 503")
	obtest.AssertSpanAttribute(t, &got, "error.type", "_OTHER")
	foundException := false
	for _, ev := range got.Events {
		if ev.Name == "exception" {
			foundException = true
			break
		}
	}
	assert.True(t, foundException,
		"terminal 5xx with HTTPError must carry an exception event so Jaeger/Tempo surface the failure detail")
}

// TestEndHTTPClientSpan4xxWithErrorMarksError covers a 4xx response that
// produced an error (e.g. terminal 404 after retries — though the framework
// doesn't currently retry 4xx, this defends against future config drift and
// matches the metric semantics: error.type is recorded when non-empty).
func TestEndHTTPClientSpan4xxWithErrorMarksError(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	terminalErr := errors.New("HTTP request failed with status 404")
	EndHTTPClientSpan(span, 404, "_OTHER", 0, terminalErr)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Error)
	obtest.AssertSpanAttribute(t, &got, "error.type", "_OTHER")
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

// TestStartHTTPClientSpanNilURLOmitsUnknownAttrs verifies OTel's "omit unknown
// values" guidance: when info.URL is nil we cannot derive server.address /
// server.port / url.scheme / url.path, so those attributes must be absent from
// the span (not emitted as "" / 0). Trace queries can then distinguish
// "missing" from "empty".
func TestStartHTTPClientSpanNilURLOmitsUnknownAttrs(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	// Nil URL should not panic — serverAddressPort + urlScheme + urlPath are nil-safe.
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: nil})
	span.End()

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	// http.request.method is always present (canonicalMethod returns _OTHER
	// for unknown rather than empty). network.protocol.name is a constant.
	obtest.AssertSpanAttribute(t, &got, "http.request.method", "GET")
	obtest.AssertSpanAttribute(t, &got, "network.protocol.name", "http")
	// All URL-derived attrs must be omitted.
	omitted := []string{"server.address", "server.port", "url.scheme", "url.path"}
	for _, kv := range got.Attributes {
		key := string(kv.Key)
		for _, o := range omitted {
			assert.NotEqualf(t, o, key,
				"attribute %q should be omitted when URL is nil (OTel: prefer missing over empty)", o)
		}
	}
}

// TestStartHTTPClientSpanNilInfoIsSafe locks in the nil-info guard.
// CodeRabbit flagged this as a potential panic on nil dereference; the
// guard at the top of StartHTTPClientSpan treats nil as an empty value.
func TestStartHTTPClientSpanNilInfoIsSafe(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	require.NotPanics(t, func() {
		_, span := StartHTTPClientSpan(context.Background(), nil)
		require.NotNil(t, span)
		span.End()
	})

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	// Empty method canonicalises to _OTHER per the package's existing behavior;
	// the span name therefore uses the HTTP-METHOD template since PeerName is
	// also empty.
	assert.Equal(t, "HTTP _OTHER", got.Name)
}

// TestEndHTTPClientSpanStripQueryStringFromTransportError locks in F-SEC-1
// from the pre-push security audit: when a transport error's underlying
// *url.Error carries a query string with credentials, the span's exception
// event must NOT contain those credentials. Pre-fix, span.RecordError(err)
// would export Go's default stringification including the full URL.
func TestEndHTTPClientSpanStripQueryStringFromTransportError(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	// Simulate a transport error with a *url.Error carrying a sensitive
	// query string and a userinfo password — mirrors what net/http surfaces.
	const secretToken = "sk_live_super_secret_value"
	urlErr := &url.Error{
		Op:  "Get",
		URL: "https://user:pw@api.example.com/foo?token=" + secretToken + "&api_key=anothersecret",
		Err: errors.New("dial tcp: connection refused"),
	}
	EndHTTPClientSpan(span, 0, "connection_error", 0, urlErr)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	require.NotEmpty(t, got.Events, "transport error should produce an exception event")
	var exMsg string
	for _, ev := range got.Events {
		if ev.Name == "exception" {
			for _, kv := range ev.Attributes {
				if string(kv.Key) == "exception.message" {
					exMsg = kv.Value.AsString()
				}
			}
		}
	}
	require.NotEmpty(t, exMsg, "exception event should have an exception.message attribute")
	assert.NotContainsf(t, exMsg, secretToken,
		"exception.message must not contain query-string secret token, got %q", exMsg)
	assert.NotContainsf(t, exMsg, "anothersecret",
		"exception.message must not contain query-string api_key value, got %q", exMsg)
	assert.NotContainsf(t, exMsg, "user:pw",
		"exception.message must not contain userinfo credentials, got %q", exMsg)
	// The operation + redacted URL + inner error should still be present so
	// the message remains debuggable.
	assert.Truef(t, strings.Contains(exMsg, "Get"),
		"exception.message should retain the operation verb for debuggability, got %q", exMsg)
	assert.Truef(t, strings.Contains(exMsg, "api.example.com"),
		"exception.message should retain the host for debuggability, got %q", exMsg)
}

// TestRedactErrorMessageNonTransportErrorIsUnchanged confirms that errors
// without an embedded *url.Error pass through unchanged — interceptor
// failures, framework HTTPErrors, etc. don't carry raw URLs and don't need
// query-stripping.
func TestRedactErrorMessageNonTransportErrorIsUnchanged(t *testing.T) {
	plain := errors.New("interceptor failed: bad signature")
	assert.Equal(t, plain.Error(), redactErrorMessage(plain))
	assert.Equal(t, "", redactErrorMessage(nil))
}
