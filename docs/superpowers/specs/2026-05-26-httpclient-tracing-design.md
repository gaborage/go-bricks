# httpclient OpenTelemetry Tracing — Design Spec

**Issue:** #471
**Author:** generated 2026-05-26
**Status:** Approved — ready for implementation plan

## Goal

When `observability.enabled: true`, every outbound HTTP call made via `httpclient.Client` emits a child span of the surrounding context's active span, with OTel HTTP client semantic-convention attributes, correct status mapping, and W3C trace-context injection that interoperates with the framework's existing `EnableW3CTrace` propagation.

## Non-goals

- JOSE-aware request/response body-size attributes (separate follow-up; #469 out-of-scope list).
- A per-request `OperationName` attribute (separate follow-up).
- Server-side HTTP tracing — `server/` already has it.
- Distributed-tracing interaction with the JOSE response unwrap (note in docs, defer investigation).

## Architecture

### New file: `httpclient/internal/tracking/tracing.go`

Public surface (full list):

```go
const httpTracerName = "go-bricks/httpclient"

// InitHTTPTracer initializes the package-level tracer idempotently. Subsequent
// calls are no-ops. Mirrors InitHTTPMeter exactly: sync.Once + sync.Mutex.
func InitHTTPTracer()

// HTTPSpanInfo carries per-span data — first-class twin of HTTPClientMeasurement.
type HTTPSpanInfo struct {
    PeerName    string   // peer.service attribute; "" omits it
    Method      string   // canonical (uppercase, "_OTHER" for non-standard methods)
    URL         *url.URL // server.address, server.port, url.scheme, url.path
    ResendCount int      // http.request.resend_count; 0 for first attempt
}

// StartHTTPClientSpan opens a CLIENT-kind span as a child of any active span on ctx.
// Returns the context with the new span attached + the span itself.
//
// Used for BOTH the logical "Do" span (resend_count omitted) and per-attempt spans
// (resend_count set). Caller chooses by passing the appropriate context and info.
//
// Span name: "{METHOD} {peer.service}" when PeerName set, else "HTTP {METHOD}".
func StartHTTPClientSpan(ctx context.Context, info *HTTPSpanInfo) (context.Context, trace.Span)

// EndHTTPClientSpan applies the final attributes and status mapping, then ends the span.
// statusCode = 0 means transport error (no response received).
//
// Status mapping (OTel HTTP client semconv):
//   - 2xx, 3xx, 4xx        → status unset (default OK)
//   - 5xx                   → codes.Error, no RecordError (no exception)
//   - transport error       → codes.Error + RecordError(err) + error.type attr
//
// On nil-or-no-op span, this is a zero-cost no-op.
func EndHTTPClientSpan(span trace.Span, statusCode int, errType string, responseBytes int, err error)

// ResetTracerForTesting clears the cached tracer so tests can swap providers.
// Test-only — never call from production code.
func ResetTracerForTesting()
```

### Wiring inside `client.go`

`Do`:
- After `validateRequest`, open the **parent "Do" span** via `StartHTTPClientSpan(ctx, &HTTPSpanInfo{Method, URL: parsedURL, PeerName})`.
- `defer EndHTTPClientSpan(doSpan, finalStatus, finalErrType, finalRespBytes, finalErr)` — final values are captured by closure (or a small `final` struct updated by the loop).
- The retry loop runs against the context that has the Do span attached, so each attempt span is its child.

`executeAttempt`:
- After `buildRequest` succeeds (so `httpReq.URL` is available), open the **child attempt span** via `StartHTTPClientSpan(attemptCtx, &HTTPSpanInfo{Method, URL: httpReq.URL, PeerName, ResendCount: attempt})`.
- Move `applyHeaders → ensureTraceContextHeaders` to use `otel.GetTextMapPropagator().Inject(...)` *after* the attempt span starts, so the injected traceparent matches the attempt span's IDs.
- Close the attempt span via `EndHTTPClientSpan(attemptSpan, statusCode, errType, respBytes, err)` after recording metrics — same lifecycle as the metric observation.
- On `buildRequest` failure: **no** attempt span is opened (no roundtrip = no observation).

### Attribute table

Per the design's URL-redaction decision: `url.full` is **never** emitted. `url.path` is emitted with query string + userinfo stripped.

| Attribute | Source | Notes |
|---|---|---|
| `peer.service` | `info.PeerName` | Omitted when `""`. |
| `http.request.method` | `canonicalMethod(info.Method)` | Canonical uppercase; `"_OTHER"` fallback. |
| `server.address` | `info.URL.Hostname()` | Omitted when URL has no host. |
| `server.port` | `info.URL.Port()` or scheme default | 80 (http) / 443 (https) when port absent. |
| `url.scheme` | `info.URL.Scheme` | Lowercased. |
| `url.path` | `info.URL.Path` | Emitted when non-empty (the parsed URL already excludes query string and userinfo — `url.URL.Path` is the raw path). |
| `network.protocol.name` | `"http"` | Constant — HTTP/1 vs HTTP/2 not surfaced; OTel-recommended low-cardinality default. |
| `http.request.resend_count` | `info.ResendCount` | Attempt span only. Omitted on Do span. |
| `http.response.status_code` | EndHTTPClientSpan `statusCode` | Omitted when `0` (transport error). |
| `error.type` | EndHTTPClientSpan `errType` | Set only for transport errors. |

### Status mapping (verbatim from OTel HTTP client semconv v1.32.0+)

| Condition | Span status |
|---|---|
| `statusCode == 0 && err != nil` | `codes.Error`, message = `errType`, plus `RecordError(err)` |
| `500 <= statusCode < 600` | `codes.Error`, message = `"HTTP {code}"`, no `RecordError` |
| `100 <= statusCode < 500` | unset (default OK) |

The "leave 4xx unset" rule is the OTel HTTP client convention — client treats 4xx as a normal flow-control signal, not an error. Server-side spans differ; this is the *client* file.

### W3C traceparent coordination

Order of operations inside `applyHeaders`:

1. The attempt span has already been started — `attemptCtx` carries it.
2. If `c.config.EnableW3CTrace`:
   - If `attemptCtx` has an active recording span: use `otel.GetTextMapPropagator().Inject(attemptCtx, propagation.HeaderCarrier(httpReq.Header))`. This writes the *real* traceparent matching the attempt span.
   - Else, fall back to current behavior: `TraceParentFromContext(attemptCtx)` (inherited) or `GenerateTraceParent()` (synthetic).
3. If `c.config.EnableW3CTrace == false`: no W3C injection (backward compat).

This means with `EnableW3CTrace=true` *and* an active tracer (the new one or the surrounding server tracer), every outbound request carries a traceparent that downstream services can extract and join. The synthetic fallback only fires when nothing in the call chain has a real tracer.

No deprecation of `EnableW3CTrace` in this PR — documented in wiki/httpclient.md.

### Initialization pattern

```go
var (
    httpTracer    trace.Tracer
    tracerOnce    sync.Once
    tracerInitMu  sync.Mutex
)

func initHTTPTracer() {
    if httpTracer != nil {
        return
    }
    httpTracer = otel.Tracer(httpTracerName)
}

func InitHTTPTracer() {
    tracerInitMu.Lock()
    defer tracerInitMu.Unlock()
    tracerOnce.Do(initHTTPTracer)
}

// ResetTracerForTesting clears the singleton + sync.Once so tests can switch providers.
func ResetTracerForTesting() {
    tracerInitMu.Lock()
    defer tracerInitMu.Unlock()
    httpTracer = nil
    tracerOnce = sync.Once{}
}
```

`StartHTTPClientSpan` calls `InitHTTPTracer()` on entry. Nil-checks guard against init failure: if `httpTracer == nil`, return `ctx, trace.SpanFromContext(ctx)` (non-recording span — every method call is a no-op).

### Testing strategy

- Add `httpclient/internal/tracking/tracing_test.go` with these scenarios:
  - **2xx**: status unset, attributes present, no `RecordError`.
  - **3xx**: same as 2xx.
  - **4xx**: status unset (the OTel client convention test).
  - **5xx**: `codes.Error`, no `RecordError`, no `error.type` attr.
  - **Transport error**: `codes.Error` + `RecordError` + `error.type` attr set.
  - **Retry sequence** (5xx → 5xx → 2xx with `MaxRetries=2`): 3 attempt spans, all children of Do span, `http.request.resend_count` = 0,1,2; Do span status = unset (final 2xx).
  - **Parent-child**: assert each attempt span's parent SpanContext equals the Do span's SpanContext.
  - **Peer set vs unset**: span name flips between `"GET stripe"` and `"HTTP GET"`.
  - **URL with query string + userinfo**: assert `url.path` attribute is path-only.
  - **No-op when no tracer provider**: assert calls don't panic; uses `noop.NewTracerProvider()`.
  - **Propagation header**: assert the outgoing request's `traceparent` carries the attempt span's trace ID.
- Reuse the existing `observability/testing/helpers.go` helpers where possible; otherwise use the OTel SDK `tracetest.SpanRecorder` directly.
- Coverage target: ≥80% on `tracing.go`. (Mirror metrics_test.go's coverage approach.)

### Documentation deliverables

- `wiki/httpclient.md`: new "## Tracing" section adjacent to the existing "## Metrics" section. Same shape — scope, span naming, attribute reference, status mapping, propagation precedence, init pattern.
- `wiki/observability.md`: extend "Per-Subsystem Instrumented Meters" paragraph to also list tracers (`go-bricks/httpclient`, `go-bricks/database`, `go-bricks/messaging`).
- `CLAUDE.md`: update the httpclient stub bullet to reference the new tracing section alongside metrics.
- `llms.txt`: add a brief tracing example in the existing httpclient block (one or two lines — keep it terse).

## Open question deferred to implementation

None — all 8 issue questions are answered in this spec.

## Acceptance criteria (from #471)

1. ✅ One span per attempt + one logical Do span (parent-child).
2. ✅ Attributes match OTel HTTP client semconv v1.32.0 (the version pinned across the repo).
3. ✅ URL redaction implemented: no `url.full`, `url.path` only with query/userinfo stripped.
4. ✅ `EnableW3CTrace` coordination documented.
5. ✅ `observability.enabled: false` → no-op tracer + zero overhead.
6. ✅ Existing tests continue to pass; new tests achieve ≥80% on `tracing.go`.
7. ✅ `make check` + `make check-all` pass.
8. ✅ CodeRabbit + SonarCloud + project-conventions-reviewer findings addressed before merge.
9. ✅ wiki/httpclient.md "Tracing" section + wiki/observability.md tracer list + CLAUDE.md stub update.
