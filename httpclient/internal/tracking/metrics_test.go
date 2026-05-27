package tracking

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// setupTestMeterProvider creates a test meter provider, sets it as the global provider,
// resets meter state, and initialises the instruments. Returns the provider for metric
// collection and a cleanup function.
func setupTestMeterProvider(t *testing.T) (mp *obtest.TestMeterProvider, cleanup func()) {
	t.Helper()
	mp = obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)
	ResetMeterForTesting()
	InitHTTPMeter()
	cleanup = func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}
	return mp, cleanup
}

// mustParseURL is a test helper that panics if the URL string is invalid.
func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

// assertHasAttribute asserts that a string attribute with the given key exists and
// equals the expected value in the provided attribute slice.
func assertHasAttribute(t *testing.T, attrs []attribute.KeyValue, key, expected string) {
	t.Helper()
	for _, a := range attrs {
		if string(a.Key) == key {
			assert.Equal(t, expected, a.Value.AsString(), "attribute %s value mismatch", key)
			return
		}
	}
	t.Errorf("attribute %s not found in attribute set", key)
}

// assertHasIntAttribute asserts that an integer attribute with the given key exists and
// equals the expected value in the provided attribute slice.
func assertHasIntAttribute(t *testing.T, attrs []attribute.KeyValue, key string, expected int64) {
	t.Helper()
	for _, a := range attrs {
		if string(a.Key) == key {
			assert.Equal(t, expected, a.Value.AsInt64(), "attribute %s value mismatch", key)
			return
		}
	}
	t.Errorf("attribute %s not found in attribute set", key)
}

// assertNoAttribute asserts that no attribute with the given key appears in the slice.
func assertNoAttribute(t *testing.T, attrs []attribute.KeyValue, key string) {
	t.Helper()
	for _, a := range attrs {
		if string(a.Key) == key {
			t.Errorf("attribute %s should be absent but found value %v", key, a.Value)
			return
		}
	}
}

// TestRecordHTTPClientMetricsSuccessPath verifies that a successful request emits the
// duration histogram with all expected attributes and no error.type attribute.
func TestRecordHTTPClientMetricsSuccessPath(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	m := HTTPClientMeasurement{
		PeerName:    "payments-api",
		Method:      "GET",
		URL:         mustParseURL("https://api.example.com/v1/users"),
		StatusCode:  200,
		ErrorType:   "",
		ResendCount: 0,
		Elapsed:     150 * time.Millisecond,
	}

	RecordHTTPClientMetrics(context.Background(), &m)

	rm := mp.Collect(t)
	obtest.AssertMetricExists(t, rm, metricRequestDuration)

	durationMetric := obtest.FindMetric(rm, metricRequestDuration)
	require.NotNil(t, durationMetric)

	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64]")
	require.NotEmpty(t, histData.DataPoints)

	dp := histData.DataPoints[0]
	assert.InDelta(t, 0.15, dp.Sum, 0.01, "duration should be ~0.15 seconds")
	assert.Equal(t, uint64(1), dp.Count)

	attrs := dp.Attributes.ToSlice()
	assertHasAttribute(t, attrs, attrPeerService, "payments-api")
	assertHasAttribute(t, attrs, attrHTTPMethod, "GET")
	assertHasAttribute(t, attrs, attrServerAddress, "api.example.com")
	assertHasAttribute(t, attrs, attrURLScheme, "https")
	assertHasIntAttribute(t, attrs, attrHTTPStatusCode, 200)
	assertHasIntAttribute(t, attrs, attrHTTPResendCount, 0)
	assertNoAttribute(t, attrs, attrErrorType)
}

// TestRecordHTTPClientMetricsPerAttemptSemantics verifies that three sequential calls with
// resend_count 0, 1, 2 each emit a separate OTel datapoint (distinct attribute sets),
// for a total of 3 recorded measurements across all datapoints.
func TestRecordHTTPClientMetricsPerAttemptSemantics(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	u := mustParseURL("https://api.example.com/orders")
	ctx := context.Background()

	for i := range 3 {
		RecordHTTPClientMetrics(ctx, &HTTPClientMeasurement{
			Method:      "POST",
			URL:         u,
			StatusCode:  200,
			ResendCount: i,
			Elapsed:     10 * time.Millisecond,
		})
	}

	rm := mp.Collect(t)

	durationMetric := obtest.FindMetric(rm, metricRequestDuration)
	require.NotNil(t, durationMetric)

	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64]")

	// Each resend_count value (0, 1, 2) produces a distinct attribute set → distinct datapoint.
	// Sum their counts to get the total number of recorded measurements.
	var totalCount uint64
	for _, dp := range histData.DataPoints {
		totalCount += dp.Count
	}
	assert.Equal(t, uint64(3), totalCount, "expected 3 total measurements, one per attempt")
}

// TestRecordHTTPClientMetricsTransportError verifies that when StatusCode is 0 (transport
// error) the http.response.status_code attribute is omitted from all instruments.
func TestRecordHTTPClientMetricsTransportError(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	RecordHTTPClientMetrics(context.Background(), &HTTPClientMeasurement{
		Method:      "GET",
		URL:         mustParseURL("https://api.example.com"),
		StatusCode:  0, // transport error — no response received
		ErrorType:   "network_error",
		ResendCount: 0,
		Elapsed:     5 * time.Millisecond,
	})

	rm := mp.Collect(t)

	durationMetric := obtest.FindMetric(rm, metricRequestDuration)
	require.NotNil(t, durationMetric)
	histData := durationMetric.Data.(metricdata.Histogram[float64])
	require.NotEmpty(t, histData.DataPoints)

	attrs := histData.DataPoints[0].Attributes.ToSlice()
	assertNoAttribute(t, attrs, attrHTTPStatusCode)
}

// TestRecordHTTPClientMetricsWithTimeout verifies that a timeout error sets the
// error.type attribute and omits http.response.status_code when StatusCode is 0.
func TestRecordHTTPClientMetricsWithTimeout(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	RecordHTTPClientMetrics(context.Background(), &HTTPClientMeasurement{
		Method:      "GET",
		URL:         mustParseURL("https://api.example.com"),
		StatusCode:  0,
		ErrorType:   "timeout",
		ResendCount: 0,
		Elapsed:     30 * time.Second,
	})

	rm := mp.Collect(t)

	durationMetric := obtest.FindMetric(rm, metricRequestDuration)
	require.NotNil(t, durationMetric)
	histData := durationMetric.Data.(metricdata.Histogram[float64])
	require.NotEmpty(t, histData.DataPoints)

	attrs := histData.DataPoints[0].Attributes.ToSlice()
	assertHasAttribute(t, attrs, attrErrorType, "timeout")
	assertNoAttribute(t, attrs, attrHTTPStatusCode)
}

// TestRecordHTTPClientMetricsWithNonZeroBodySizes verifies that non-zero request and
// response body sizes emit the corresponding body-size histograms.
func TestRecordHTTPClientMetricsWithNonZeroBodySizes(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	RecordHTTPClientMetrics(context.Background(), &HTTPClientMeasurement{
		Method:        "POST",
		URL:           mustParseURL("https://api.example.com/data"),
		StatusCode:    201,
		Elapsed:       20 * time.Millisecond,
		RequestBytes:  512,
		ResponseBytes: 1024,
	})

	rm := mp.Collect(t)

	obtest.AssertMetricExists(t, rm, metricRequestBodySize)
	obtest.AssertMetricExists(t, rm, metricResponseBodySize)

	reqCount, err := obtest.GetMetricHistogramCount(rm, metricRequestBodySize)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), reqCount)

	respCount, err := obtest.GetMetricHistogramCount(rm, metricResponseBodySize)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), respCount)
}

// TestRecordHTTPClientMetricsWithZeroBodySizes verifies that body-size histograms are
// not emitted when RequestBytes and ResponseBytes are both 0.
func TestRecordHTTPClientMetricsWithZeroBodySizes(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	RecordHTTPClientMetrics(context.Background(), &HTTPClientMeasurement{
		Method:        "GET",
		URL:           mustParseURL("https://api.example.com/status"),
		StatusCode:    200,
		Elapsed:       5 * time.Millisecond,
		RequestBytes:  0,
		ResponseBytes: 0,
	})

	rm := mp.Collect(t)

	// Body-size metrics must not appear when sizes are 0.
	assert.Nil(t, obtest.FindMetric(rm, metricRequestBodySize), "request body size metric should not be emitted when 0")
	assert.Nil(t, obtest.FindMetric(rm, metricResponseBodySize), "response body size metric should not be emitted when 0")
}

// TestIncDecActiveRequestsBalanced verifies that a paired Inc/Dec results in a net zero
// active-request count and that the server.address attribute is present on the datapoint.
func TestIncDecActiveRequestsBalanced(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	ctx := context.Background()
	IncActiveRequests(ctx, "svc", "GET", "api.example.com")
	DecActiveRequests(ctx, "svc", "GET", "api.example.com")

	rm := mp.Collect(t)
	obtest.AssertMetricExists(t, rm, metricActiveRequests)

	activeMetric := obtest.FindMetric(rm, metricActiveRequests)
	require.NotNil(t, activeMetric)

	sumData, ok := activeMetric.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] for active_requests")
	// Paired inc/dec → net 0, meaning the sum across datapoints is 0.
	var total int64
	for _, dp := range sumData.DataPoints {
		total += dp.Value
	}
	assert.Equal(t, int64(0), total, "net active requests should be 0 after balanced inc/dec")

	// server.address must be present on the datapoint.
	require.NotEmpty(t, sumData.DataPoints)
	assertHasAttribute(t, sumData.DataPoints[0].Attributes.ToSlice(), attrServerAddress, "api.example.com")
}

// TestIncRetryWithVariousReasons verifies that IncRetry increments the retry counter
// with the correct reason and http.request.method attributes for each call.
func TestIncRetryWithVariousReasons(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	ctx := context.Background()
	reasons := []string{"timeout", "5xx", "network", "build_response"}
	for _, r := range reasons {
		IncRetry(ctx, "svc", "POST", r)
	}

	rm := mp.Collect(t)
	obtest.AssertMetricExists(t, rm, metricRetriesTotal)

	retryMetric := obtest.FindMetric(rm, metricRetriesTotal)
	require.NotNil(t, retryMetric)

	sumData, ok := retryMetric.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] for retries.total")

	// Count total and collect distinct reasons seen.
	var total int64
	reasonsFound := make(map[string]bool)
	for _, dp := range sumData.DataPoints {
		total += dp.Value
		attrs := dp.Attributes.ToSlice()
		for _, a := range attrs {
			if string(a.Key) == attrRetryReason {
				reasonsFound[a.Value.AsString()] = true
			}
		}
		// Every datapoint must carry http.request.method canonicalized to "POST".
		assertHasAttribute(t, attrs, attrHTTPMethod, "POST")
	}

	assert.Equal(t, int64(4), total, "expected 4 total retries (one per reason)")
	for _, r := range reasons {
		assert.True(t, reasonsFound[r], "reason %q not found in retry attributes", r)
	}
}

// TestIncRetryMethodCanonicalization verifies that IncRetry canonicalizes the HTTP method:
// lowercase "get" → "GET", unknown "WEIRD" → "_OTHER".
func TestIncRetryMethodCanonicalization(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	ctx := context.Background()

	// Lowercase method must be uppercased.
	IncRetry(ctx, "svc", "get", "timeout")
	// Unknown method must become _OTHER.
	IncRetry(ctx, "svc", "WEIRD", "5xx")

	rm := mp.Collect(t)

	retryMetric := obtest.FindMetric(rm, metricRetriesTotal)
	require.NotNil(t, retryMetric)

	sumData, ok := retryMetric.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] for retries.total")

	methodsFound := make(map[string]bool)
	for _, dp := range sumData.DataPoints {
		for _, a := range dp.Attributes.ToSlice() {
			if string(a.Key) == attrHTTPMethod {
				methodsFound[a.Value.AsString()] = true
			}
		}
	}

	assert.True(t, methodsFound["GET"], "lowercase 'get' should be canonicalized to 'GET'")
	assert.True(t, methodsFound[methodOther], "unknown 'WEIRD' should be canonicalized to '_OTHER'")
}

// TestEmptyPeerNameOmitsPeerServiceAttribute verifies that when PeerName is empty the
// peer.service attribute is omitted across all instruments (duration, body size, active
// requests, retries).
func TestEmptyPeerNameOmitsPeerServiceAttribute(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	ctx := context.Background()

	RecordHTTPClientMetrics(ctx, &HTTPClientMeasurement{
		PeerName:      "", // must be omitted
		Method:        "GET",
		URL:           mustParseURL("https://api.example.com/ping"),
		StatusCode:    200,
		Elapsed:       10 * time.Millisecond,
		RequestBytes:  100,
		ResponseBytes: 50,
	})
	IncActiveRequests(ctx, "", "GET", "")
	DecActiveRequests(ctx, "", "GET", "")
	IncRetry(ctx, "", "GET", "timeout")

	rm := mp.Collect(t)

	// Duration histogram — no peer.service.
	durationMetric := obtest.FindMetric(rm, metricRequestDuration)
	require.NotNil(t, durationMetric)
	histData := durationMetric.Data.(metricdata.Histogram[float64])
	require.NotEmpty(t, histData.DataPoints)
	assertNoAttribute(t, histData.DataPoints[0].Attributes.ToSlice(), attrPeerService)

	// Request body size — no peer.service.
	reqBodyMetric := obtest.FindMetric(rm, metricRequestBodySize)
	require.NotNil(t, reqBodyMetric)
	reqHistData := reqBodyMetric.Data.(metricdata.Histogram[int64])
	require.NotEmpty(t, reqHistData.DataPoints)
	assertNoAttribute(t, reqHistData.DataPoints[0].Attributes.ToSlice(), attrPeerService)

	// Response body size — no peer.service.
	respBodyMetric := obtest.FindMetric(rm, metricResponseBodySize)
	require.NotNil(t, respBodyMetric)
	respHistData := respBodyMetric.Data.(metricdata.Histogram[int64])
	require.NotEmpty(t, respHistData.DataPoints)
	assertNoAttribute(t, respHistData.DataPoints[0].Attributes.ToSlice(), attrPeerService)

	// Active requests — no peer.service.
	activeMetric := obtest.FindMetric(rm, metricActiveRequests)
	require.NotNil(t, activeMetric)
	sumData := activeMetric.Data.(metricdata.Sum[int64])
	if len(sumData.DataPoints) > 0 {
		assertNoAttribute(t, sumData.DataPoints[0].Attributes.ToSlice(), attrPeerService)
	}

	// Retries — no peer.service.
	retryMetric := obtest.FindMetric(rm, metricRetriesTotal)
	require.NotNil(t, retryMetric)
	retrySumData := retryMetric.Data.(metricdata.Sum[int64])
	require.NotEmpty(t, retrySumData.DataPoints)
	assertNoAttribute(t, retrySumData.DataPoints[0].Attributes.ToSlice(), attrPeerService)
}

// TestUnknownMethodNormalizedToOther verifies that an unrecognised HTTP method is
// normalised to "_OTHER" in the http.request.method attribute.
func TestUnknownMethodNormalizedToOther(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	RecordHTTPClientMetrics(context.Background(), &HTTPClientMeasurement{
		Method:     "WEIRD",
		URL:        mustParseURL("https://api.example.com/op"),
		StatusCode: 200,
		Elapsed:    10 * time.Millisecond,
	})

	rm := mp.Collect(t)

	durationMetric := obtest.FindMetric(rm, metricRequestDuration)
	require.NotNil(t, durationMetric)
	histData := durationMetric.Data.(metricdata.Histogram[float64])
	require.NotEmpty(t, histData.DataPoints)

	assertHasAttribute(t, histData.DataPoints[0].Attributes.ToSlice(), attrHTTPMethod, methodOther)
}

// TestNoopMeterProviderPath verifies that when the global meter provider is a noop provider
// all tracking functions execute without panicking and no instruments are registered.
func TestNoopMeterProviderPath(t *testing.T) {
	// Save original provider so we can restore it.
	original := otel.GetMeterProvider()
	t.Cleanup(func() {
		otel.SetMeterProvider(original)
		ResetMeterForTesting()
		// Re-initialize with real provider so other tests are unaffected.
	})

	// Install noop provider and reset cached meter.
	otel.SetMeterProvider(metricnoop.NewMeterProvider())
	ResetMeterForTesting()

	ctx := context.Background()

	// All functions must complete without panicking.
	assert.NotPanics(t, func() {
		RecordHTTPClientMetrics(ctx, &HTTPClientMeasurement{
			Method:     "GET",
			URL:        mustParseURL("https://api.example.com/ping"),
			StatusCode: 200,
			Elapsed:    1 * time.Millisecond,
		})
	})
	assert.NotPanics(t, func() { IncActiveRequests(ctx, "svc", "GET", "api.example.com") })
	assert.NotPanics(t, func() { DecActiveRequests(ctx, "svc", "GET", "api.example.com") })
	assert.NotPanics(t, func() { IncRetry(ctx, "svc", "GET", "timeout") })
}

// TestRecordHTTPClientMetricsHTTPErrorStatusOmitsErrorType verifies that a 503 response
// does not automatically set error.type — 4xx/5xx status codes alone do not imply an error type.
func TestRecordHTTPClientMetricsHTTPErrorStatusOmitsErrorType(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	RecordHTTPClientMetrics(context.Background(), &HTTPClientMeasurement{
		Method:     "GET",
		URL:        mustParseURL("https://api.example.com/health"),
		StatusCode: 503,
		ErrorType:  "", // explicitly empty — no transport-level error
		Elapsed:    8 * time.Millisecond,
	})

	rm := mp.Collect(t)

	durationMetric := obtest.FindMetric(rm, metricRequestDuration)
	require.NotNil(t, durationMetric, "duration histogram must be emitted for 503 responses")

	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64]")
	require.NotEmpty(t, histData.DataPoints, "duration histogram must have datapoints")

	attrs := histData.DataPoints[0].Attributes.ToSlice()
	// 503 must produce a status code attribute.
	assertHasIntAttribute(t, attrs, attrHTTPStatusCode, 503)
	// 5xx without an explicit ErrorType must NOT produce an error.type attribute.
	assertNoAttribute(t, attrs, attrErrorType)
}

// TestServerAddressPort exercises the defensive branches of serverAddressPort directly.
func TestServerAddressPort(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		nilURL   bool
		wantAddr string
		wantPort int
	}{
		{
			name:     "nil_url",
			nilURL:   true,
			wantAddr: "",
			wantPort: 0,
		},
		{
			name:     "http_no_explicit_port",
			rawURL:   "http://example.com/path",
			wantAddr: "example.com",
			wantPort: 80,
		},
		{
			name:     "https_no_explicit_port",
			rawURL:   "https://example.com/path",
			wantAddr: "example.com",
			wantPort: 443,
		},
		{
			name:     "explicit_port_overrides_scheme_default",
			rawURL:   "https://example.com:8443/path",
			wantAddr: "example.com",
			wantPort: 8443,
		},
		{
			name:     "http_explicit_port",
			rawURL:   "http://example.com:9090/path",
			wantAddr: "example.com",
			wantPort: 9090,
		},
		{
			name:     "ipv6_with_explicit_port",
			rawURL:   "http://[::1]:8080/path",
			wantAddr: "::1", // url.URL.Hostname() strips brackets
			wantPort: 8080,
		},
		{
			name:     "unknown_scheme_falls_back_to_80",
			rawURL:   "ftp://example.com/files",
			wantAddr: "example.com",
			wantPort: 80,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var u *url.URL
			if !tc.nilURL {
				u = mustParseURL(tc.rawURL)
			}
			addr, port := serverAddressPort(u)
			assert.Equal(t, tc.wantAddr, addr, "addr mismatch")
			assert.Equal(t, tc.wantPort, port, "port mismatch")
		})
	}
}
