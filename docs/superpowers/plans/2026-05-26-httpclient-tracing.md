# httpclient OTel Tracing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OpenTelemetry tracing (spans) for outbound HTTP calls made via `httpclient.Client`, mirroring the metrics seam shipped in PR #470. Closes #471.

**Architecture:** New `httpclient/internal/tracking/tracing.go` exposes `StartHTTPClientSpan`/`EndHTTPClientSpan` + lazy `InitHTTPTracer`. `client.Do` opens a parent CLIENT-kind span; `executeAttempt` opens a child span per attempt. W3C `traceparent` injection routes through `otel.GetTextMapPropagator()` when an active span exists, with the existing `EnableW3CTrace` synthetic fallback for the no-tracer case. URL redaction: no `url.full`, only host/port/scheme/path.

**Tech Stack:** Go 1.25, `go.opentelemetry.io/otel` v1.43.0, `go.opentelemetry.io/otel/semconv/v1.32.0` (repo-pinned), `observability/testing` helpers (`NewTestTraceProvider`, `SpanCollector`), `testify`.

---

## File Structure

**Created:**
- `httpclient/internal/tracking/tracing.go` — `InitHTTPTracer`, `HTTPSpanInfo`, `StartHTTPClientSpan`, `EndHTTPClientSpan`, internal helpers.
- `httpclient/internal/tracking/tracing_test.go` — unit tests for span emission, attributes, status mapping, name template, no-op behavior.

**Modified:**
- `httpclient/internal/tracking/testing.go` — add `ResetTracerForTesting()` alongside the existing `ResetMeterForTesting()`.
- `httpclient/client.go` — wire parent + child spans in `Do` / `executeAttempt`; switch `ensureTraceContextHeaders` to propagator-based injection when a span is active.
- `httpclient/client_test.go` — add integration test for full retry sequence (`5xx → 5xx → 2xx`) verifying parent/child relationship.
- `wiki/httpclient.md` — new `## Tracing` section.
- `wiki/observability.md` — extend instrumented-meters paragraph to also list tracers.
- `CLAUDE.md` — one-line update to the `httpclient/` bullet referencing the new tracing section.
- `llms.txt` — brief tracing reference in the existing httpclient block.

---

### Task 1: Scaffold `tracing.go` with lazy init + failing first test

**Files:**
- Create: `httpclient/internal/tracking/tracing.go`
- Modify: `httpclient/internal/tracking/testing.go`
- Create: `httpclient/internal/tracking/tracing_test.go`

- [ ] **Step 1: Write the failing test** — verifies `StartHTTPClientSpan` returns a non-nil span and the span is recording when a real provider is set.

Append to a new file `httpclient/internal/tracking/tracing_test.go`:

```go
package tracking

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// setupTestTraceProvider installs a test trace provider as the global tracer
// provider, resets the package tracer cache, and returns the provider + a cleanup
// that restores the previous global and resets the cache again.
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

	u, err := url.Parse("https://api.example.com/v1/users?api_key=secret")
	require.NoError(t, err)

	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method: "GET",
		URL:    u,
	})
	require.NotNil(t, span)
	assert.True(t, span.IsRecording(), "span should be recording with a real tracer provider")
	span.End()
}
```

- [ ] **Step 2: Run test — expect compile failure**

```bash
go test ./httpclient/internal/tracking/ -run TestStartHTTPClientSpanReturnsRecordingSpan -v
```
Expected: build fails — `StartHTTPClientSpan`, `HTTPSpanInfo`, and `ResetTracerForTesting` are undefined.

- [ ] **Step 3: Implement the skeleton — minimum to compile and pass**

Create `httpclient/internal/tracking/tracing.go`:

```go
package tracking

import (
	"context"
	"net/url"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

const httpTracerName = "go-bricks/httpclient"

var (
	httpTracer   trace.Tracer
	tracerOnce   sync.Once
	tracerInitMu sync.Mutex
)

// HTTPSpanInfo carries the per-span fields needed to construct a CLIENT-kind
// span for an outbound HTTP call. It is the twin of HTTPClientMeasurement —
// every span-eligible field on the measurement also lives here.
type HTTPSpanInfo struct {
	PeerName    string
	Method      string
	URL         *url.URL
	ResendCount int
}

// initHTTPTracer performs the one-time tracer lookup. Caller MUST hold tracerInitMu.
func initHTTPTracer() {
	if httpTracer != nil {
		return
	}
	httpTracer = otel.Tracer(httpTracerName)
}

// InitHTTPTracer initializes the HTTP client tracer idempotently. Subsequent
// calls after the first are no-ops. Holds tracerInitMu so concurrent calls and
// ResetTracerForTesting cannot race on tracerOnce.
func InitHTTPTracer() {
	tracerInitMu.Lock()
	defer tracerInitMu.Unlock()
	tracerOnce.Do(initHTTPTracer)
}

// StartHTTPClientSpan opens a CLIENT-kind span as a child of any span on ctx.
// Returns the new context (with the span attached) and the span itself.
// When the global tracer provider is a no-op, the returned span is non-recording
// and every method call on it is a no-op — zero-cost.
func StartHTTPClientSpan(ctx context.Context, info *HTTPSpanInfo) (context.Context, trace.Span) {
	InitHTTPTracer()
	if httpTracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return httpTracer.Start(ctx, spanName(info), trace.WithSpanKind(trace.SpanKindClient))
}

// EndHTTPClientSpan ends the span — placeholder until Task 3 fills in attributes + status.
func EndHTTPClientSpan(span trace.Span, statusCode int, errType string, responseBytes int, err error) {
	if span == nil {
		return
	}
	span.End()
}

func spanName(info *HTTPSpanInfo) string {
	method := canonicalMethod(info.Method)
	if info.PeerName != "" {
		return method + " " + info.PeerName
	}
	return "HTTP " + method
}
```

Append to `httpclient/internal/tracking/testing.go`:

```go
// ResetTracerForTesting resets the package-level tracer state so each test starts
// with a fresh tracer. Mirrors ResetMeterForTesting. Safe to call concurrently
// with InitHTTPTracer — the mutex serializes both paths.
func ResetTracerForTesting() {
	tracerInitMu.Lock()
	defer tracerInitMu.Unlock()

	tracerOnce = sync.Once{}
	httpTracer = nil
}
```

- [ ] **Step 4: Run test — expect pass**

```bash
go test ./httpclient/internal/tracking/ -run TestStartHTTPClientSpanReturnsRecordingSpan -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add httpclient/internal/tracking/tracing.go \
        httpclient/internal/tracking/tracing_test.go \
        httpclient/internal/tracking/testing.go
git commit -m "feat(httpclient): scaffold OTel tracer + lazy init (#471)"
```

---

### Task 2: Span name template — method-only vs method+peer

**Files:**
- Modify: `httpclient/internal/tracking/tracing_test.go`
- Modify: `httpclient/internal/tracking/tracing.go` (already covered, just add edge-case test)

- [ ] **Step 1: Write failing tests for the name template**

Append to `tracing_test.go`:

```go
func TestSpanNameTemplateMethodOnly(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, err := url.Parse("https://api.example.com/users")
	require.NoError(t, err)
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method: "GET",
		URL:    u,
	})
	span.End()

	collector := obtest.NewSpanCollector(t, tp.Exporter)
	collector.AssertCount(1)
	got := collector.First()
	assert.Equal(t, "HTTP GET", got.Name)
}

func TestSpanNameTemplateMethodPeer(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, err := url.Parse("https://api.example.com/users")
	require.NoError(t, err)
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method:   "POST",
		URL:      u,
		PeerName: "stripe",
	})
	span.End()

	collector := obtest.NewSpanCollector(t, tp.Exporter)
	collector.AssertCount(1)
	got := collector.First()
	assert.Equal(t, "POST stripe", got.Name)
}

func TestSpanNameTemplateUnknownMethod(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, err := url.Parse("https://api.example.com/users")
	require.NoError(t, err)
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method: "WEIRD",
		URL:    u,
	})
	span.End()

	collector := obtest.NewSpanCollector(t, tp.Exporter)
	collector.AssertCount(1)
	got := collector.First()
	assert.Equal(t, "HTTP _OTHER", got.Name, "non-standard methods should be canonicalized to _OTHER")
}
```

- [ ] **Step 2: Run tests — expect pass (implementation from Task 1 covers all three)**

```bash
go test ./httpclient/internal/tracking/ -run TestSpanNameTemplate -v
```
Expected: all three pass.

- [ ] **Step 3: Commit**

```bash
git add httpclient/internal/tracking/tracing_test.go
git commit -m "test(httpclient): cover span name template variants (#471)"
```

---

### Task 3: Attribute set on StartHTTPClientSpan

**Files:**
- Modify: `httpclient/internal/tracking/tracing.go`
- Modify: `httpclient/internal/tracking/tracing_test.go`

- [ ] **Step 1: Write the failing test for the base attribute set**

Append to `tracing_test.go`:

```go
func TestStartHTTPClientSpanSetsBaseAttributes(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, err := url.Parse("https://api.example.com:8443/v1/users")
	require.NoError(t, err)
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method:      "POST",
		URL:         u,
		PeerName:    "stripe",
		ResendCount: 2,
	})
	span.End()

	collector := obtest.NewSpanCollector(t, tp.Exporter)
	collector.AssertCount(1)
	got := collector.First()
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

	u, err := url.Parse("http://api.example.com/foo")
	require.NoError(t, err)
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method: "GET",
		URL:    u,
	})
	span.End()

	collector := obtest.NewSpanCollector(t, tp.Exporter)
	got := collector.First()
	for _, kv := range got.Attributes {
		if string(kv.Key) == "peer.service" {
			t.Fatalf("peer.service should be omitted when PeerName is empty")
		}
	}
	// Default port for http should be 80.
	obtest.AssertSpanAttribute(t, &got, "server.port", int64(80))
}

func TestStartHTTPClientSpanOmitsResendCountWhenZero(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, err := url.Parse("https://api.example.com/foo")
	require.NoError(t, err)
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method: "GET",
		URL:    u,
	})
	span.End()

	collector := obtest.NewSpanCollector(t, tp.Exporter)
	got := collector.First()
	for _, kv := range got.Attributes {
		if string(kv.Key) == "http.request.resend_count" {
			t.Fatalf("http.request.resend_count should be omitted on first attempt (Do span or attempt 0)")
		}
	}
}

func TestStartHTTPClientSpanOmitsURLPathWhenEmpty(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, err := url.Parse("https://api.example.com")
	require.NoError(t, err)
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method: "GET",
		URL:    u,
	})
	span.End()

	collector := obtest.NewSpanCollector(t, tp.Exporter)
	got := collector.First()
	for _, kv := range got.Attributes {
		if string(kv.Key) == "url.path" {
			t.Fatalf("url.path should be omitted when the URL has no path")
		}
	}
}
```

- [ ] **Step 2: Run tests — expect fail (attributes not yet set)**

```bash
go test ./httpclient/internal/tracking/ -run TestStartHTTPClientSpan -v
```
Expected: the four new tests fail (attributes are not yet emitted).

- [ ] **Step 3: Replace `StartHTTPClientSpan` body to set attributes**

In `tracing.go`, replace the existing `StartHTTPClientSpan` with:

```go
import (
	// ... existing imports ...
	"go.opentelemetry.io/otel/attribute"
)

func StartHTTPClientSpan(ctx context.Context, info *HTTPSpanInfo) (context.Context, trace.Span) {
	InitHTTPTracer()
	if httpTracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	method := canonicalMethod(info.Method)
	ctx, span := httpTracer.Start(ctx, spanName(info), trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(spanAttributes(info, method)...)
	return ctx, span
}

// spanAttributes builds the attribute set applied at span start. resend_count is
// omitted when 0 (first attempt or Do-level rollup); peer.service is omitted when
// PeerName is empty; url.path is omitted when the URL path is empty.
func spanAttributes(info *HTTPSpanInfo, method string) []attribute.KeyValue {
	addr, port := serverAddressPort(info.URL)
	scheme := urlScheme(info.URL)
	attrs := make([]attribute.KeyValue, 0, 8)
	if info.PeerName != "" {
		attrs = append(attrs, attribute.String(attrPeerService, info.PeerName))
	}
	attrs = append(attrs,
		attribute.String(attrHTTPMethod, method),
		attribute.String(attrServerAddress, addr),
		attribute.Int(attrServerPort, port),
		attribute.String(attrURLScheme, scheme),
		attribute.String("network.protocol.name", "http"),
	)
	if p := urlPath(info.URL); p != "" {
		attrs = append(attrs, attribute.String("url.path", p))
	}
	if info.ResendCount > 0 {
		attrs = append(attrs, attribute.Int(attrHTTPResendCount, info.ResendCount))
	}
	return attrs
}

// urlPath returns the URL path without query string or userinfo. url.URL.Path
// already excludes both, so this is effectively a nil-safe accessor.
func urlPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Path
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./httpclient/internal/tracking/ -run TestStartHTTPClientSpan -v
```
Expected: all four pass.

- [ ] **Step 5: Commit**

```bash
git add httpclient/internal/tracking/tracing.go httpclient/internal/tracking/tracing_test.go
git commit -m "feat(httpclient): emit OTel HTTP client semconv attributes on spans (#471)"
```

---

### Task 4: Status mapping in EndHTTPClientSpan (2xx/3xx/4xx unset, 5xx Error, transport RecordError)

**Files:**
- Modify: `httpclient/internal/tracking/tracing.go`
- Modify: `httpclient/internal/tracking/tracing_test.go`

- [ ] **Step 1: Write failing tests for each status branch**

Append to `tracing_test.go`:

```go
import (
	// ... existing imports ...
	"errors"
	"go.opentelemetry.io/otel/codes"
)

func TestEndHTTPClientSpan2xxLeavesStatusUnset(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, _ := url.Parse("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	EndHTTPClientSpan(span, 200, "", 42, nil)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Unset)
}

func TestEndHTTPClientSpan4xxLeavesStatusUnset(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, _ := url.Parse("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	EndHTTPClientSpan(span, 404, "", 12, nil)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Unset)
	obtest.AssertSpanAttribute(t, &got, "http.response.status_code", int64(404))
}

func TestEndHTTPClientSpan5xxSetsErrorStatus(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, _ := url.Parse("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	EndHTTPClientSpan(span, 503, "", 0, nil)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Error)
	obtest.AssertSpanStatusDescription(t, &got, "HTTP 503")
	assert.Empty(t, got.Events, "no exception event should be recorded for 5xx (status code carries the signal)")
}

func TestEndHTTPClientSpanTransportErrorRecordsErrorAndAttr(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, _ := url.Parse("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	transportErr := errors.New("dial: connection refused")
	EndHTTPClientSpan(span, 0, "connection_error", 0, transportErr)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanStatus(t, &got, codes.Error)
	obtest.AssertSpanAttribute(t, &got, "error.type", "connection_error")
	require.NotEmpty(t, got.Events, "RecordError should add an exception event")
	for _, kv := range got.Attributes {
		if string(kv.Key) == "http.response.status_code" {
			t.Fatalf("http.response.status_code must be omitted when statusCode is 0 (transport error)")
		}
	}
}

func TestEndHTTPClientSpanResponseBytes(t *testing.T) {
	tp, cleanup := setupTestTraceProvider(t)
	defer cleanup()

	u, _ := url.Parse("https://api.example.com/foo")
	_, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{Method: "GET", URL: u})
	EndHTTPClientSpan(span, 200, "", 1024, nil)

	got := obtest.NewSpanCollector(t, tp.Exporter).First()
	obtest.AssertSpanAttribute(t, &got, "http.response.body.size", int64(1024))
}
```

- [ ] **Step 2: Run tests — expect fail (EndHTTPClientSpan is still a stub)**

```bash
go test ./httpclient/internal/tracking/ -run TestEndHTTPClientSpan -v
```

- [ ] **Step 3: Implement the status mapping**

In `tracing.go`, replace `EndHTTPClientSpan`:

```go
import (
	// ... existing imports ...
	"go.opentelemetry.io/otel/codes"
	"strconv"
)

// EndHTTPClientSpan finalizes status, error, and per-response attributes, then
// ends the span. statusCode == 0 signals a transport error. Status mapping follows
// the OTel HTTP client semantic conventions:
//
//   - 100-499 (incl. 4xx)  → status unset (default OK)
//   - 500-599               → codes.Error, message "HTTP {code}", NO RecordError
//   - transport error (0)   → codes.Error + RecordError(err) + error.type attribute
func EndHTTPClientSpan(span trace.Span, statusCode int, errType string, responseBytes int, err error) {
	if span == nil {
		return
	}
	if statusCode != 0 {
		span.SetAttributes(attribute.Int(attrHTTPStatusCode, statusCode))
	}
	if responseBytes > 0 {
		span.SetAttributes(attribute.Int("http.response.body.size", responseBytes))
	}
	switch {
	case statusCode == 0 && err != nil:
		// Transport error: no response received.
		if errType != "" {
			span.SetAttributes(attribute.String(attrErrorType, errType))
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, errType)
	case statusCode >= 500 && statusCode < 600:
		span.SetStatus(codes.Error, "HTTP "+strconv.Itoa(statusCode))
	default:
		// 100-499: leave status unset (default OK per OTel client-span convention).
	}
	span.End()
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./httpclient/internal/tracking/ -run TestEndHTTPClientSpan -v
```

- [ ] **Step 5: Commit**

```bash
git add httpclient/internal/tracking/tracing.go httpclient/internal/tracking/tracing_test.go
git commit -m "feat(httpclient): map HTTP status to OTel span status (#471)"
```

---

### Task 5: No-op behavior when no tracer provider

**Files:**
- Modify: `httpclient/internal/tracking/tracing_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tracing_test.go`:

```go
import (
	// ... existing imports ...
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestStartHTTPClientSpanNoopProviderReturnsNonRecording(t *testing.T) {
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	ResetTracerForTesting()
	defer func() {
		otel.SetTracerProvider(original)
		ResetTracerForTesting()
	}()

	u, _ := url.Parse("https://api.example.com/foo")
	ctx, span := StartHTTPClientSpan(context.Background(), &HTTPSpanInfo{
		Method: "GET",
		URL:    u,
	})
	require.NotNil(t, span)
	assert.False(t, span.IsRecording(), "no-op provider should yield a non-recording span")
	// EndHTTPClientSpan must not panic on a non-recording span.
	EndHTTPClientSpan(span, 500, "", 0, errors.New("boom"))
	// Span context should still be valid; ctx is not modified by the no-op tracer.
	_ = ctx
}
```

- [ ] **Step 2: Run test — expect pass (the existing code already handles no-op providers)**

```bash
go test ./httpclient/internal/tracking/ -run TestStartHTTPClientSpanNoop -v
```
Expected: PASS. This test locks in the no-op behavior against regressions.

- [ ] **Step 3: Commit**

```bash
git add httpclient/internal/tracking/tracing_test.go
git commit -m "test(httpclient): pin no-op tracer behavior for tracing seam (#471)"
```

---

### Task 6: Wire parent Do span into `client.Do`

**Files:**
- Modify: `httpclient/client.go`
- Modify: `httpclient/client_test.go`

- [ ] **Step 1: Write the failing integration test** — verifies that `Do` emits one parent span when no retries happen.

Append to `httpclient/client_test.go` (the imports + test infrastructure already exist; new test only):

```go
import (
	// ... existing test imports ...
	"go.opentelemetry.io/otel"
	obtest "github.com/gaborage/go-bricks/observability/testing"
	"github.com/gaborage/go-bricks/httpclient/internal/tracking"
)

func TestClientDoEmitsParentSpanOnSuccess(t *testing.T) {
	tp := obtest.NewTestTraceProvider()
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tp.TracerProvider)
	tracking.ResetTracerForTesting()
	defer func() {
		otel.SetTracerProvider(original)
		tracking.ResetTracerForTesting()
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := NewBuilder(logger.NewNop()).WithPeerName("test-peer").Build()
	resp, err := c.Get(context.Background(), &Request{URL: server.URL + "/foo"})
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	collector := obtest.NewSpanCollector(t, tp.Exporter)
	// One parent Do span + one child attempt span = 2.
	collector.AssertCount(2)
	doSpan := collector.WithName("GET test-peer").First()
	obtest.AssertSpanAttribute(t, &doSpan, "peer.service", "test-peer")
	obtest.AssertSpanStatus(t, &doSpan, codes.Unset)
}
```

- [ ] **Step 2: Run test — expect fail (no spans emitted yet)**

```bash
go test ./httpclient -run TestClientDoEmitsParentSpan -v
```

- [ ] **Step 3: Wire the parent span in `client.Do`**

In `httpclient/client.go`, modify `Do`. Replace the existing function body's block from after `validateRequest` through the return:

```go
func (c *client) Do(ctx context.Context, method string, req *Request) (*Response, error) {
	if err := c.validateRequest(req); err != nil {
		return nil, err
	}

	start := time.Now()
	callCount := atomic.AddInt64(&c.callCount, 1)
	maxRetries := c.config.MaxRetries

	// Pre-extract host once for active_requests attribute. URL parse failures
	// surface in buildRequest, not here — leave addr empty if parse fails so
	// the attribute is omitted rather than crashing the gauge.
	addr := ""
	var parsedURL *url.URL
	if parsed, err := url.Parse(req.URL); err == nil {
		addr = parsed.Hostname()
		parsedURL = parsed
	}

	tracking.IncActiveRequests(ctx, c.config.PeerName, method, addr)
	defer tracking.DecActiveRequests(ctx, c.config.PeerName, method, addr)

	// Open the parent "Do" span — ends in defer with the final attempt's outcome.
	ctx, doSpan := tracking.StartHTTPClientSpan(ctx, &tracking.HTTPSpanInfo{
		PeerName: c.config.PeerName,
		Method:   method,
		URL:      parsedURL,
	})
	var (
		finalStatus    int
		finalErrType   string
		finalRespBytes int
		finalErr       error
	)
	defer func() {
		tracking.EndHTTPClientSpan(doSpan, finalStatus, finalErrType, finalRespBytes, finalErr)
	}()

	for attempt := 0; ; attempt++ {
		result := c.executeAttempt(ctx, method, req, attempt, maxRetries, start, callCount)
		if result.retry {
			tracking.IncRetry(ctx, c.config.PeerName, method, result.retryReason)
			continue
		}
		// Capture final values for the parent span's End.
		if result.response != nil {
			finalStatus = result.response.StatusCode
			finalRespBytes = len(result.response.Body)
		}
		if result.err != nil {
			finalErr = result.err
			finalErrType = classifyError(result.err)
		}
		return result.response, result.err
	}
}
```

- [ ] **Step 4: Run test — the test will still fail because the child attempt span isn't wired yet (count is 1, not 2)**

```bash
go test ./httpclient -run TestClientDoEmitsParentSpan -v
```
Expected: fail with `expected 2 spans, got 1`. **Adjust the test assertion to `collector.AssertCount(1)` for now**, then re-run:

Update the test:
```go
	collector.AssertCount(1)
```

Re-run:
```bash
go test ./httpclient -run TestClientDoEmitsParentSpan -v
```
Expected: PASS — exactly one parent Do span emitted.

- [ ] **Step 5: Commit**

```bash
git add httpclient/client.go httpclient/client_test.go
git commit -m "feat(httpclient): emit parent OTel span per logical Do call (#471)"
```

> **Note:** The test temporarily asserts 1 span; Task 7 will add the child span and update the assertion to 2.

---

### Task 7: Wire child attempt span into `executeAttempt`

**Files:**
- Modify: `httpclient/client.go`
- Modify: `httpclient/client_test.go`

- [ ] **Step 1: Update the existing parent-span test to expect 2 spans (parent + child)**

In `client_test.go`, find `TestClientDoEmitsParentSpanOnSuccess` and update the assertion:

```go
	collector.AssertCount(2)
	doSpan := collector.WithName("GET test-peer").First()
	obtest.AssertSpanAttribute(t, &doSpan, "peer.service", "test-peer")
	obtest.AssertSpanStatus(t, &doSpan, codes.Unset)

	// Child attempt span exists and is a child of the Do span.
	allSpans := tp.Exporter.GetSpans()
	require.Len(t, allSpans, 2)
	// Find which is parent (no parent set) and which is child.
	var parent, child tracetest.SpanStub
	for i := range allSpans {
		if allSpans[i].Parent.IsValid() {
			child = allSpans[i]
		} else {
			parent = allSpans[i]
		}
	}
	assert.True(t, parent.SpanContext.IsValid(), "parent span context should be valid")
	assert.Equal(t, parent.SpanContext.SpanID(), child.Parent.SpanID(),
		"child attempt span must reference the Do span as its parent")
```

You'll need an additional import in the test file:
```go
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
```

- [ ] **Step 2: Run test — expect fail (still emits only the parent)**

```bash
go test ./httpclient -run TestClientDoEmitsParentSpan -v
```

- [ ] **Step 3: Wire the child span in `executeAttempt`**

In `httpclient/client.go`, replace `executeAttempt`:

```go
func (c *client) executeAttempt(
	ctx context.Context,
	method string,
	req *Request,
	attempt, maxRetries int,
	start time.Time,
	callCount int64,
) attemptResult {
	httpReq, err := c.buildRequest(ctx, method, req)
	if err != nil {
		// Pre-roundtrip build failure: do NOT record a metric or span observation.
		return attemptResult{err: err}
	}

	traceHeader := c.traceHeaderName()
	traceIDForLog := httpReq.Header.Get(traceHeader)

	// Open the child attempt span. ctx already carries the parent Do span.
	attemptCtx, attemptSpan := tracking.StartHTTPClientSpan(ctx, &tracking.HTTPSpanInfo{
		PeerName:    c.config.PeerName,
		Method:      method,
		URL:         httpReq.URL,
		ResendCount: attempt,
	})
	// Re-inject W3C trace context headers AFTER the attempt span starts so the
	// traceparent carries the attempt span's IDs. (See Task 8 for propagator wiring.)
	if c.config.EnableW3CTrace {
		c.injectW3CTraceContext(attemptCtx, httpReq)
	}

	c.logRequest(httpReq, req.Body, traceIDForLog)

	attemptStart := time.Now()
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if httpResp != nil && httpResp.Body != nil {
			httpResp.Body.Close()
		}
		// Transport error: record metric AND span with status 0 and classified error type.
		errType := classifyError(err)
		m := tracking.HTTPClientMeasurement{
			PeerName:     c.config.PeerName,
			Method:       method,
			URL:          httpReq.URL,
			StatusCode:   0,
			ErrorType:    errType,
			ResendCount:  attempt,
			Elapsed:      time.Since(attemptStart),
			RequestBytes: len(req.Body),
		}
		tracking.RecordHTTPClientMetrics(ctx, &m)
		tracking.EndHTTPClientSpan(attemptSpan, 0, errType, 0, err)
		return c.handleExecutionError(ctx, err, attempt, maxRetries)
	}

	respCtx := &responseProcessingContext{
		attempt:      attempt,
		maxRetries:   maxRetries,
		start:        start,
		callCount:    callCount,
		traceID:      traceIDForLog,
		attemptStart: attemptStart,
		method:       method,
		requestBytes: len(req.Body),
		httpReqURL:   httpReq.URL,
	}
	result := c.processHTTPResponse(ctx, httpReq, httpResp, respCtx)
	// End the attempt span using the result's outcome.
	endAttemptSpanFromResult(attemptSpan, result)
	return result
}

// endAttemptSpanFromResult ends the attempt span using the response or error
// captured by processHTTPResponse. Kept as a helper so executeAttempt stays
// readable.
func endAttemptSpanFromResult(span trace.Span, result attemptResult) {
	if result.response != nil {
		errType := ""
		if result.err != nil {
			errType = classifyError(result.err)
		}
		tracking.EndHTTPClientSpan(span, result.response.StatusCode, errType, len(result.response.Body), result.err)
		return
	}
	// Response build error path with no usable response.
	errType := classifyError(result.err)
	tracking.EndHTTPClientSpan(span, 0, errType, 0, result.err)
}
```

Add this import at the top of `client.go`:

```go
	"go.opentelemetry.io/otel/trace"
```

> **Note:** `injectW3CTraceContext` is added in Task 8. For now, leave `c.ensureTraceContextHeaders(httpReq)` inside `applyHeaders` as it is — this means the parent header injection still happens once at applyHeaders-time. Task 8 replaces it with a propagator-aware version.

Actually, to keep this task self-contained, **rename and adjust** the existing `ensureTraceContextHeaders` call:
- In `applyHeaders`, remove the `c.ensureTraceContextHeaders(httpReq)` call.
- Add `c.ensureTraceContextHeaders(httpReq)` (the existing function) immediately after the `c.applyHeaders(httpReq, req)` line inside `executeAttempt` is no longer needed — the call inside `applyHeaders` runs once per attempt anyway. Leave `applyHeaders` alone and DELETE the inline call I added in Step 3 above.

Revised Step 3 attempt-span block (replace the previous "Re-inject" comment + if branch with just the span start):

```go
	attemptCtx, attemptSpan := tracking.StartHTTPClientSpan(ctx, &tracking.HTTPSpanInfo{
		PeerName:    c.config.PeerName,
		Method:      method,
		URL:         httpReq.URL,
		ResendCount: attempt,
	})
	_ = attemptCtx // ctx for propagation is wired in Task 8.
```

Defer to Task 8 for propagator injection.

- [ ] **Step 4: Run test — expect pass**

```bash
go test ./httpclient -run TestClientDoEmitsParentSpan -v
```
Expected: PASS — 2 spans, parent SpanID matches child's Parent.SpanID().

- [ ] **Step 5: Run the full httpclient test suite to catch regressions**

```bash
go test ./httpclient/... -race
```
Expected: all existing tests still pass.

- [ ] **Step 6: Commit**

```bash
git add httpclient/client.go httpclient/client_test.go
git commit -m "feat(httpclient): emit child attempt spans under Do span (#471)"
```

---

### Task 8: Replace `ensureTraceContextHeaders` with propagator injection when a span is active

**Files:**
- Modify: `httpclient/client.go`
- Modify: `httpclient/client_test.go`

- [ ] **Step 1: Write the failing test** — verifies traceparent carries the attempt span's trace ID when EnableW3CTrace is on and a tracer is active.

Append to `client_test.go`:

```go
func TestClientDoInjectsRealTraceparentWhenSpanActive(t *testing.T) {
	tp := obtest.NewTestTraceProvider()
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tp.TracerProvider)
	tracking.ResetTracerForTesting()
	defer func() {
		otel.SetTracerProvider(original)
		tracking.ResetTracerForTesting()
	}()
	// Also set the global text-map propagator so otel.GetTextMapPropagator() returns one.
	originalProp := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(originalProp)

	var receivedTP string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTP = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewBuilder(logger.NewNop()).WithW3CTrace(true).Build()
	_, err := c.Get(context.Background(), &Request{URL: server.URL + "/foo"})
	require.NoError(t, err)

	// Find the attempt span in the exporter; its trace ID must appear in the
	// traceparent header sent on the wire.
	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 2)
	var attemptSpan tracetest.SpanStub
	for i := range spans {
		if spans[i].Parent.IsValid() {
			attemptSpan = spans[i]
			break
		}
	}
	require.True(t, attemptSpan.SpanContext.IsValid())
	require.NotEmpty(t, receivedTP, "server should have received a traceparent header")
	traceID := attemptSpan.SpanContext.TraceID().String()
	assert.Contains(t, receivedTP, traceID,
		"traceparent header should carry the attempt span's trace ID, got %q", receivedTP)
}
```

Add these imports to `client_test.go` if not already present:
```go
	"go.opentelemetry.io/otel/propagation"
```

- [ ] **Step 2: Run test — expect fail (current ensureTraceContextHeaders generates a synthetic traceparent that won't match)**

```bash
go test ./httpclient -run TestClientDoInjectsRealTraceparent -v
```

- [ ] **Step 3: Implement propagator-aware injection**

In `httpclient/client.go`, modify `ensureTraceContextHeaders` to prefer the OTel propagator when an active span exists. Replace:

```go
func (c *client) ensureTraceContextHeaders(httpReq *nethttp.Request) {
	// When a recording span is on the request context, use the OTel propagator —
	// this writes a traceparent that matches the active span's trace/span IDs and
	// joins the wider trace. Falls back to the legacy synthetic generator only
	// when no span is active in the context (no tracer wired upstream).
	ctx := httpReq.Context()
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(httpReq.Header))
		// Propagator handles both traceparent + tracestate; no further work needed.
		return
	}
	if httpReq.Header.Get(HeaderTraceParent) == "" {
		if tp, ok := TraceParentFromContext(ctx); ok {
			httpReq.Header.Set(HeaderTraceParent, tp)
		} else {
			httpReq.Header.Set(HeaderTraceParent, GenerateTraceParent())
		}
	}
	if httpReq.Header.Get(HeaderTraceState) == "" {
		if ts, ok := TraceStateFromContext(ctx); ok {
			httpReq.Header.Set(HeaderTraceState, ts)
		}
	}
}
```

Add imports:
```go
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
```

Now wire the per-attempt context through `applyHeaders`. The simplest path: `executeAttempt` already builds `httpReq` with `nethttp.NewRequestWithContext(ctx, ...)`, so `httpReq.Context()` returns the parent Do context — but NOT the attempt context. We need the attempt context to flow into the request context BEFORE `applyHeaders` runs.

In `executeAttempt`, after starting the attempt span (Task 7's modification), update httpReq's context:

```go
	attemptCtx, attemptSpan := tracking.StartHTTPClientSpan(ctx, &tracking.HTTPSpanInfo{
		PeerName:    c.config.PeerName,
		Method:      method,
		URL:         httpReq.URL,
		ResendCount: attempt,
	})
	// Bind the attempt span context onto the request so applyHeaders (called
	// earlier in buildRequest) can be re-run with the new context — or rather,
	// re-inject the propagator headers now that the attempt span exists.
	httpReq = httpReq.WithContext(attemptCtx)
	if c.config.EnableW3CTrace {
		c.ensureTraceContextHeaders(httpReq)
	}
```

Important: this means `applyHeaders → ensureTraceContextHeaders` inside `buildRequest` runs with the *parent Do* context (or empty), but the post-start re-injection overwrites with the attempt span's traceparent. To avoid duplicate logic, REMOVE the original `ensureTraceContextHeaders` call from `applyHeaders` and call it ONLY here (after the attempt span starts).

In `applyHeaders`, change:
```go
func (c *client) applyHeaders(httpReq *nethttp.Request, req *Request) {
	applyHeaderMap(httpReq.Header, c.config.DefaultHeaders)
	applyHeaderMap(httpReq.Header, req.Headers)
	c.ensureContentTypeHeader(httpReq, req.Body)
	c.ensureTraceIDHeader(httpReq)
	// W3C trace context headers are injected per-attempt in executeAttempt,
	// AFTER the attempt span starts — see executeAttempt for the injection site.
}
```

- [ ] **Step 4: Run test — expect pass**

```bash
go test ./httpclient -run TestClientDoInjectsRealTraceparent -v
```

- [ ] **Step 5: Run the full httpclient + tracking test suites to catch regressions**

```bash
go test ./httpclient/... -race
```

- [ ] **Step 6: Commit**

```bash
git add httpclient/client.go httpclient/client_test.go
git commit -m "feat(httpclient): inject real traceparent via OTel propagator when span active (#471)"
```

---

### Task 9: Integration test for retry sequence (5xx → 5xx → 2xx)

**Files:**
- Modify: `httpclient/client_test.go`

- [ ] **Step 1: Write the failing test**

Append to `client_test.go`:

```go
func TestClientDoRetrySequenceEmitsParentPlusThreeChildren(t *testing.T) {
	tp := obtest.NewTestTraceProvider()
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tp.TracerProvider)
	tracking.ResetTracerForTesting()
	defer func() {
		otel.SetTracerProvider(original)
		tracking.ResetTracerForTesting()
	}()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := NewBuilder(logger.NewNop()).
		WithRetries(2, time.Millisecond).
		WithPeerName("flaky-svc").
		Build()
	resp, err := c.Get(context.Background(), &Request{URL: server.URL + "/foo"})
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	spans := tp.Exporter.GetSpans()
	// 1 parent + 3 attempt children.
	require.Len(t, spans, 4)

	var parent tracetest.SpanStub
	children := make([]tracetest.SpanStub, 0, 3)
	for i := range spans {
		if spans[i].Parent.IsValid() {
			children = append(children, spans[i])
		} else {
			parent = spans[i]
		}
	}
	require.Len(t, children, 3)
	for i := range children {
		assert.Equal(t, parent.SpanContext.SpanID(), children[i].Parent.SpanID(),
			"every attempt span must reference the Do span as its parent")
	}

	// Resend counts on children should be 0, 1, 2 — count them.
	resendCounts := map[int64]int{}
	for i := range children {
		for _, kv := range children[i].Attributes {
			if string(kv.Key) == "http.request.resend_count" {
				resendCounts[kv.Value.AsInt64()]++
			}
		}
	}
	// First attempt omits resend_count (it's 0). Attempts 2 and 3 set 1 and 2.
	assert.Equal(t, 1, resendCounts[1], "exactly one attempt should have resend_count=1")
	assert.Equal(t, 1, resendCounts[2], "exactly one attempt should have resend_count=2")

	// Parent Do span ends with the final 2xx → status unset, status_code=200.
	obtest.AssertSpanStatus(t, &parent, codes.Unset)
	obtest.AssertSpanAttribute(t, &parent, "http.response.status_code", int64(200))

	// First two attempts (5xx) → status Error.
	errorCount := 0
	for i := range children {
		if children[i].Status.Code == codes.Error {
			errorCount++
		}
	}
	assert.Equal(t, 2, errorCount, "first two attempts (5xx) should have Error status")
}
```

- [ ] **Step 2: Run test — expect pass (all wiring is now in place)**

```bash
go test ./httpclient -run TestClientDoRetrySequence -v -race
```

- [ ] **Step 3: Commit**

```bash
git add httpclient/client_test.go
git commit -m "test(httpclient): cover full retry-sequence span tree (#471)"
```

---

### Task 10: Documentation — wiki/httpclient.md Tracing section

**Files:**
- Modify: `wiki/httpclient.md`

- [ ] **Step 1: Locate the "## Metrics" section in `wiki/httpclient.md` and add a new "## Tracing" section AFTER the metrics section ends.**

Find the end of the metrics section (look for the next `## ` heading), then insert the following block immediately before it:

````markdown
## Tracing

### Overview

The `httpclient` package emits OpenTelemetry **CLIENT** spans for every outbound HTTP call under the tracer name `go-bricks/httpclient`. The tracer is initialized lazily on first use via `otel.GetTracerProvider()` and governed by `observability.enabled` — when observability is disabled a no-op tracer is active and there is zero overhead per request.

**Tracer scope:** `go-bricks/httpclient`

### Span Tree Structure

Each call to `Client.Do` (and its method shortcuts `Get`/`Post`/etc.) emits:

1. **One parent "Do" span** — the logical request rollup. Opened in `Do` after request validation, closed when `Do` returns with the *final* attempt's status.
2. **One child "attempt" span per attempt** — opened in `executeAttempt` after `buildRequest` succeeds, closed after the metric for that attempt is recorded.

Every attempt span has the parent Do span as its direct parent in the trace tree. No span links are emitted between attempts — parent-child structure plus the `http.request.resend_count` attribute is sufficient.

### Span Naming

| Condition | Span name |
|---|---|
| `PeerName` set via `WithPeerName` | `"{METHOD} {peer}"` (e.g. `"POST stripe"`) |
| `PeerName` unset | `"HTTP {METHOD}"` (e.g. `"HTTP GET"`) |
| Non-standard HTTP method | `METHOD` is canonicalized to `"_OTHER"` |

URL paths are **never** in the span name — without route templating (which the client doesn't have), path-in-name is a cardinality bomb.

### Attribute Reference

Set at span start:

| Attribute | Notes |
|---|---|
| `peer.service` | From `WithPeerName`. Omitted when empty. |
| `http.request.method` | Canonical uppercase. `"_OTHER"` for non-standard methods. |
| `server.address` | Hostname from the parsed URL. |
| `server.port` | Port from URL, defaulting to 80 (http) or 443 (https). |
| `url.scheme` | `"http"` or `"https"`. |
| `url.path` | Path component only — query string and userinfo are not included (the parsed `url.URL.Path` already excludes them). Omitted when empty. |
| `network.protocol.name` | Constant `"http"`. HTTP/1.1 vs HTTP/2 is not distinguished — the OTel-recommended low-cardinality default. |
| `http.request.resend_count` | Attempt span only. Omitted when `0` (first attempt) and on the parent Do span (which represents the rollup, not any single attempt). |

Set at span end:

| Attribute | Notes |
|---|---|
| `http.response.status_code` | Set when a response is received. Omitted on transport error. |
| `http.response.body.size` | Set when response body bytes > 0. |
| `error.type` | Set only on transport error (status 0). Mirrors the value used by the duration histogram's `error.type` attribute. |

`url.full` is **never** emitted. The conservative-default rationale: even with userinfo and query-string redaction, paths can still leak (`/users/{secret-token}/...`). Emitting `server.address` + `url.scheme` + `url.path` is enough for service-graph and per-endpoint slicing without the leakage surface.

### Span Status Mapping

The OTel HTTP **client** span status convention (different from server spans):

| Outcome | Span status | RecordError? |
|---|---|---|
| 2xx / 3xx response | `codes.Unset` (default OK) | no |
| 4xx response | `codes.Unset` | no |
| 5xx response | `codes.Error`, description `"HTTP {code}"` | no |
| Transport error (no response) | `codes.Error`, description = error.type | yes (`span.RecordError(err)`) |

Rationale for the 4xx-as-OK convention: client spans treat 4xx as a normal flow-control signal (the server told you something legitimate about the request). 5xx signals a server-side failure; transport errors signal a network-side failure.

### W3C `traceparent` Propagation

`httpclient` injects `traceparent`/`tracestate` headers per attempt with this precedence:

1. **OTel propagator path** — when a recording span is active on the request context (the attempt span this package opens, *or* a surrounding span from `server/` middleware), `otel.GetTextMapPropagator().Inject(ctx, headerCarrier)` writes the *real* traceparent matching that span. This is the standard OTel mechanism — the framework registers `propagation.TraceContext{}` as the global propagator by default.
2. **Legacy fallback** — when `c.config.EnableW3CTrace == true` AND no span is active, the existing `TraceParentFromContext` / `GenerateTraceParent` path emits a synthetic traceparent. This keeps backward compatibility for callers that wire `httpclient` without an OTel tracer.
3. **Disabled** — `WithW3CTrace(false)` disables W3C injection entirely.

You don't need to change anything to benefit from the OTel propagator — leave `EnableW3CTrace` at its default `true` and register a tracer provider (the framework does this automatically via `app.New(...)` when `observability.enabled` is `true`). Downstream services receive a real traceparent that joins your trace.

### Zero-overhead when disabled

When `observability.enabled: false`, the framework installs `noop.NewTracerProvider()` as the global tracer provider. `StartHTTPClientSpan` returns a non-recording span; `EndHTTPClientSpan` is a no-op; attribute setting is a no-op. The only per-request cost is one `otel.Tracer(...)` lookup (cached after first call) and a few struct allocations the compiler can usually elide.
````

- [ ] **Step 2: Commit**

```bash
git add wiki/httpclient.md
git commit -m "docs(httpclient): add Tracing section to wiki (#471)"
```

---

### Task 11: Documentation — wiki/observability.md, CLAUDE.md, llms.txt

**Files:**
- Modify: `wiki/observability.md`
- Modify: `CLAUDE.md`
- Modify: `llms.txt`

- [ ] **Step 1: Extend `wiki/observability.md` to list tracers alongside meters**

Find the existing paragraph that mentions `go-bricks/database`, `go-bricks/messaging`, `go-bricks/httpclient` for meters, and append a similar tracer paragraph immediately after it:

```markdown
**Per-Subsystem Instrumented Tracers:** The framework ships three OTel tracers under matching scopes — `go-bricks/database` emits CLIENT-kind spans per query, `go-bricks/messaging` emits PRODUCER/CONSUMER spans per AMQP publish/consume, and `go-bricks/httpclient` emits CLIENT-kind spans per outbound HTTP call (one parent "Do" span + one child span per retry attempt). All three tracers are governed by `observability.enabled` and are no-ops when disabled. See [httpclient.md#tracing](httpclient.md#tracing) for the full attribute reference and status-mapping rules.
```

- [ ] **Step 2: Update the `httpclient/` bullet in `CLAUDE.md`**

Find the line in CLAUDE.md that reads:
```markdown
- **httpclient/** — HTTP client with retries, W3C trace propagation, and interceptors. OpenTelemetry metrics: see [wiki/httpclient.md#metrics](wiki/httpclient.md#metrics).
```

Replace with:
```markdown
- **httpclient/** — HTTP client with retries, W3C trace propagation, and interceptors. OpenTelemetry metrics: see [wiki/httpclient.md#metrics](wiki/httpclient.md#metrics). OpenTelemetry tracing: see [wiki/httpclient.md#tracing](wiki/httpclient.md#tracing).
```

- [ ] **Step 3: Update `llms.txt`**

Find the section that introduces httpclient (search for `httpclient.NewBuilder`). After the metrics example (if any), append:

```markdown
# OpenTelemetry tracing — automatic. Set observability.enabled: true in config;
# every outbound HTTP call emits a CLIENT span under the tracer name
# "go-bricks/httpclient". See wiki/httpclient.md#tracing for the attribute
# reference and the W3C traceparent precedence rules.
```

- [ ] **Step 4: Commit**

```bash
git add wiki/observability.md CLAUDE.md llms.txt
git commit -m "docs: reference httpclient tracing in observability/CLAUDE/llms.txt (#471)"
```

---

### Task 12: Run `make check-all` and fix any issues

**Files:**
- Possibly modify any of the above based on lint/test feedback.

- [ ] **Step 1: Run the comprehensive checks**

```bash
make check-all
```

- [ ] **Step 2: If anything fails, fix at the root cause (never bypass)**

Common possibilities:
- Lint may flag an unused `attemptCtx` in `executeAttempt` if propagator wiring uses `httpReq.WithContext(...)` only — handle by removing the local var.
- `go vet` may flag missing argument names in the new `EndHTTPClientSpan` — apply Go-idiomatic naming.
- Race detector may catch a tracerOnce race if `ResetTracerForTesting` is missing the mutex — verify it acquires `tracerInitMu`.

- [ ] **Step 3: Re-run until green**

```bash
make check-all
```
Expected: PASS.

- [ ] **Step 4: Commit if any fixes were needed**

```bash
git add -A
git commit -m "chore(httpclient): address make check-all feedback for tracing (#471)"
```

---

## Self-review notes

**Spec coverage:**
- Q1 (one span per Do or per attempt): ✅ Task 6 (parent), Task 7 (child).
- Q2 (span name template): ✅ Task 2.
- Q3 (URL redaction): ✅ Task 3 (`url.path` only; no `url.full`).
- Q4 (EnableW3CTrace coordination): ✅ Task 8.
- Q5 (tracer name `go-bricks/httpclient`): ✅ Task 1.
- Q6 (lifecycle wiring): ✅ Tasks 6+7.
- Q7 (error recording rules): ✅ Task 4.
- Q8 (init pattern): ✅ Task 1 (`sync.Once`+`sync.Mutex`, mirrors metrics).
- Tests cover 2xx/3xx/4xx/5xx/transport-error/retry-sequence/no-op/peer-set-unset/propagation. ≥80% coverage on `tracing.go` will follow.
- Documentation lands in Task 10–11 covering all three docs deliverables.

**Placeholder scan:** no TBDs, no "add appropriate" placeholders, every step shows the actual code or command.

**Type consistency:** `HTTPSpanInfo` used uniformly across tasks 1, 3, 6, 7; `StartHTTPClientSpan`/`EndHTTPClientSpan` signatures defined in task 1 and referenced verbatim everywhere; `attrPeerService`/`attrHTTPMethod`/`attrServerAddress`/`attrServerPort`/`attrURLScheme`/`attrHTTPResendCount`/`attrErrorType`/`attrHTTPStatusCode` are all existing constants in `metrics.go` (verified during exploration), reused unchanged.
