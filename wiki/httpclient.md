# HTTP Client (Deep Dive)

The `httpclient` package provides a production-ready outbound HTTP client built around a fluent builder, with W3C trace propagation, retries with full-jitter exponential backoff, and an interceptor chain for cross-cutting concerns. It is the recommended client for any module making outbound HTTP calls within a GoBricks service.

## HTTP Client

The `httpclient` package provides a production-ready HTTP client with built-in observability and resilience.

**Key Features:**
- **Builder pattern**: Fluent configuration via `NewBuilder(logger).WithTimeout(...).Build()`
- **W3C trace propagation**: Automatic `traceparent`/`tracestate` header injection
- **Retry with backoff**: Exponential backoff with full jitter, configurable max retries
- **Interceptors**: Request/response interceptor chains for cross-cutting concerns
- **Structured logging**: Info-level metadata (no PII), optional debug payload logging

```go
// Builder pattern with trace propagation
client := httpclient.NewBuilder(logger).
    WithTimeout(10 * time.Second).
    WithRetries(3, 500 * time.Millisecond).
    WithDefaultHeader("Accept", "application/json").
    WithW3CTrace(true).
    Build()

resp, err := client.Get(ctx, &httpclient.Request{
    URL: "https://api.example.com/users",
})
```

**Interface:** `Get`, `Post`, `Put`, `Patch`, `Delete`, `Do` — all accept `context.Context` and `*Request`, return `*Response` and `error`.

## Metrics

### Overview

The `httpclient` package emits five OpenTelemetry instruments under the meter name `go-bricks/httpclient`. All instruments are initialized lazily on first use via `otel.GetMeterProvider()` and governed by `observability.enabled` — when observability is disabled a no-op provider is active and there is zero overhead.

**Meter scope:** `go-bricks/httpclient`

### Instrument Reference

| Name | Kind | Unit | Description |
|---|---|---|---|
| `http.client.request.duration` | `Float64Histogram` | `s` | Duration of HTTP client requests |
| `http.client.active_requests` | `Int64UpDownCounter` | `{request}` | Number of in-flight HTTP client requests |
| `http.client.request.body.size` | `Int64Histogram` | `By` | Size of HTTP client request bodies |
| `http.client.response.body.size` | `Int64Histogram` | `By` | Size of HTTP client response bodies |
| `http.client.retries.total` | `Int64Counter` | `{retry}` | Total number of HTTP client retry attempts |

> **Duration histogram bucket boundaries (explicit):** `0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10` (seconds). These follow the OTel HTTP client semconv recommendation.

### Attribute Reference

**Base attributes** (present on `http.client.request.duration`, `http.client.request.body.size`, and `http.client.response.body.size`):

| Attribute | Type | Notes |
|---|---|---|
| `peer.service` | string | Logical peer name set via `WithPeerName`. Omitted when not configured. |
| `server.address` | string | Hostname extracted from the request URL. |
| `server.port` | int | Port extracted from URL; defaults to 80 (http) or 443 (https) when absent. |
| `url.scheme` | string | URL scheme (e.g. `"https"`). |
| `http.request.method` | string | Uppercase canonical HTTP method. Non-standard methods emit `"_OTHER"`. |

**Duration-only additional attributes** (on `http.client.request.duration` only):

| Attribute | Notes |
|---|---|
| `http.response.status_code` | Integer status code. Omitted on transport errors (no response received). |
| `error.type` | OTel error type enum. Omitted on success and on 4xx/5xx responses — only set for transport-level failures. See `error.type` Enum below. |
| `http.request.resend_count` | Number of prior attempts. `0` on the first (non-retry) attempt. |

**`http.client.active_requests` attributes:**

| Attribute | Type | Notes |
|---|---|---|
| `peer.service` | string | Omitted when `WithPeerName` is not set. |
| `http.request.method` | string | Canonical HTTP method. |
| `server.address` | string | Omitted when URL parsing fails or the URL has no hostname. |

**`http.client.retries.total` attributes:**

| Attribute | Type | Notes |
|---|---|---|
| `peer.service` | string | Omitted when `WithPeerName` is not set. |
| `http.request.method` | string | Canonical HTTP method. |
| `retry.reason` | string | One of `"timeout"`, `"network"`, `"5xx"`, `"build_response"`. |

### `error.type` Enum

| Value | Condition |
|---|---|
| `"timeout"` | Framework `TimeoutError`, `context.DeadlineExceeded`, or any `net.Error` where `Timeout() == true` (after more-specific classifiers below are checked). |
| `"context_canceled"` | `context.Canceled` |
| `"name_resolution_error"` | `*net.DNSError` (DNS lookup failure, including DNS timeouts) |
| `"tls_error"` | `*tls.RecordHeaderError` or `*tls.CertificateVerificationError` |
| `"connection_error"` | `*net.OpError` with `Op == "dial"` (TCP connection refused / unreachable) |
| `"interceptor_failed"` | `InterceptorError` from a request or response interceptor |
| `"_OTHER"` | Any other `NetworkError` or unclassified error |

### `WithPeerName` Example

Set a low-cardinality logical service name at client construction time. It populates the `peer.service` attribute on all five instruments and is the recommended primary dimension for SLO dashboards.

```go
client := httpclient.NewBuilder(log).
    WithTimeout(10 * time.Second).
    WithPeerName("visa-vts").
    Build()
```

### Cardinality Guidance

Prefer `peer.service` over `server.address` when writing SLO queries and alerts. `peer.service` is a short, stable string set at builder construction time, so cardinality is bounded by the number of downstream services your application calls. `server.address` is the actual hostname resolved at request time and can explode for webhook-dispatcher or fan-out clients that call arbitrary external URLs. No URL path or query string is ever emitted as an attribute.

### JOSE Body-Size Caveat

For clients constructed with `WithJOSE(...)`, the `http.client.request.body.size` and `http.client.response.body.size` histograms measure the plaintext (application-level) body before encryption and after decryption respectively — not the encrypted wire size. A JOSE-aware variant that also records wire sizes is a possible follow-up.

### Test Utilities

**External module tests (the common case):** Use the `observability/testing` helpers to install an in-memory meter provider, exercise code that calls httpclient, then assert instruments were recorded:

```go
import (
    "github.com/gaborage/go-bricks/httpclient"
    obtest "github.com/gaborage/go-bricks/observability/testing"
    "go.opentelemetry.io/otel"
)

func TestMyService(t *testing.T) {
    mp := obtest.NewTestMeterProvider()
    prev := otel.GetMeterProvider()
    otel.SetMeterProvider(mp)
    t.Cleanup(func() { otel.SetMeterProvider(prev) })

    // ... exercise code that uses httpclient ...

    rm := mp.Collect(t)
    obtest.AssertMetricExists(t, rm, "http.client.request.duration")
}
```

**Internal httpclient tests only:** `tracking.ResetMeterForTesting()` (in `httpclient/internal/tracking/testing.go`) resets cached instrument state so the next `InitHTTPMeter()` re-registers against whichever provider is active. The `internal/` boundary limits this to code within the `httpclient/**` package tree.

## Tracing

### Overview

The `httpclient` package emits OpenTelemetry **CLIENT** spans for every outbound HTTP call under the tracer name `go-bricks/httpclient`. The tracer is initialized lazily on first use via `otel.GetTracerProvider()` and governed by `observability.enabled` — when observability is disabled the global tracer is a no-op and there is zero overhead per request.

**Tracer scope:** `go-bricks/httpclient`

### Span Tree Structure

Each call to `Client.Do` (and its method shortcuts `Get` / `Post` / etc.) emits:

1. **One parent "Do" span** — the logical request rollup. Opened in `Do` after request validation; closed when `Do` returns with the *final* attempt's status, error type, and response body size.
2. **One child attempt span per attempt** — opened in `executeAttempt` after `buildRequest` succeeds; closed at the same point the per-attempt metric is recorded, so span status and metric attribution stay in sync.

Every attempt span has the parent Do span as its direct parent in the trace tree. No span links are emitted between siblings — parent-child structure plus the `http.request.resend_count` attribute is sufficient to query the retry tree.

### Span Naming

| Condition | Span name |
|---|---|
| `PeerName` set via `WithPeerName` | `"{METHOD} {peer}"` (e.g. `"POST stripe"`) |
| `PeerName` unset | `"HTTP {METHOD}"` (e.g. `"HTTP GET"`) |
| Non-standard HTTP method | `METHOD` canonicalises to `"_OTHER"` |

URL paths are **never** in the span name. Without route templating (which the client does not have), path-in-name is a cardinality bomb.

### Attribute Reference

Set at span start (both Do span and attempt span unless noted):

| Attribute | Notes |
|---|---|
| `peer.service` | From `WithPeerName`. Omitted when empty. |
| `http.request.method` | Canonical uppercase. `"_OTHER"` for non-standard methods. |
| `server.address` | Hostname from the parsed URL. |
| `server.port` | Port from URL, defaulting to 80 (http) or 443 (https). |
| `url.scheme` | `"http"` or `"https"`. |
| `url.path` | Path component only — query string and userinfo are not included (`url.URL.Path` already excludes them). Omitted when empty. |
| `network.protocol.name` | Constant `"http"`. HTTP/1.1 vs HTTP/2 is not distinguished — the OTel-recommended low-cardinality default. |
| `http.request.resend_count` | **Attempt span only.** Omitted when `0` (first attempt) and always omitted on the parent Do span (which is the rollup, not any single attempt). |

Set at span end:

| Attribute | Notes |
|---|---|
| `http.response.status_code` | Set when a response is received. Omitted on transport error. |
| `http.response.body.size` | Set when response body bytes > 0. |
| `error.type` | Set only on transport error (status 0) and on response-build errors. Mirrors the duration histogram's `error.type` attribute. |

`url.full` is **never** emitted. Even with userinfo and query-string redaction, paths can still leak (`/users/{secret-token}/...`). Emitting `server.address` + `url.scheme` + `url.path` is sufficient for service-graph slicing without the leakage surface.

### Span Status Mapping

OTel HTTP **client** span status convention (different from server spans):

| Outcome | Span status | Exception event? |
|---|---|---|
| 2xx / 3xx response (no err) | `codes.Unset` (default OK) | no |
| 4xx response (no err) | `codes.Unset` | no |
| 5xx response (no err) | `codes.Error`, description `"HTTP {code}"` | no |
| Transport error (no response) | `codes.Error`, description = error.type | yes — sanitized event |
| Any response + non-nil err (interceptor/build failure on a 2xx, terminal HTTPError on a 5xx, …) | `codes.Error`, description = error.type or err message | yes — sanitized event |

Rationale for the 4xx-as-OK convention: client spans treat 4xx as a normal flow-control signal (the server told you something legitimate about the request). 5xx signals a server-side failure; transport errors signal a network-side failure on the path between us and the server. Any err alongside a response (e.g. a response interceptor failing on a 200) takes the error path so the span doesn't silently look like a success.

**Sanitized exception events.** Where the table says "yes — sanitized event," the framework emits the exception as an explicit `AddEvent("exception", ...)` rather than `span.RecordError(err)`. Go's stdlib `*url.Error.Error()` includes the full request URL with query string (Go redacts userinfo passwords but not query strings), and Go's default `RecordError` would export those bytes to every configured OTel backend. The framework walks the error chain via `errors.As(*url.Error)` and strips both `RawQuery` and `User` from any embedded URL before recording the exception message — so credentials passed as `?token=...` or in `user:pass@` userinfo never reach trace exporters. Defense-in-depth note: this is per-package, not framework-wide; until a global span-attribute SensitiveDataFilter ships, callers adding new span attributes carrying header/body bytes must run their own redaction.

### W3C `traceparent` Propagation

`httpclient` injects `traceparent` / `tracestate` headers per attempt with this precedence:

1. **OTel propagator path** — when a recording span is active on the request context (the attempt span this package opens, *or* a surrounding span from `server/` middleware), `otel.GetTextMapPropagator().Inject(ctx, headerCarrier)` writes the *real* traceparent matching that span. The framework registers `propagation.TraceContext{}` as the default global propagator.
2. **Legacy fallback** — when `c.config.EnableW3CTrace == true` AND no span is active on the context, the existing `TraceParentFromContext` / `GenerateTraceParent` path emits a synthetic traceparent. This preserves backward compatibility for callers wiring `httpclient` without an OTel tracer.
3. **Disabled** — `WithW3CTrace(false)` disables W3C injection entirely.

You don't need to change anything to benefit from the OTel propagator — leave `EnableW3CTrace` at its default `true` and register a tracer provider (`app.New(...)` does this automatically when `observability.enabled: true`). Downstream services receive a real traceparent that joins your trace.

### Zero-overhead when disabled

When `observability.enabled: false`, the framework installs `noop.NewTracerProvider()` as the global tracer. `StartHTTPClientSpan` returns a non-recording span; `EndHTTPClientSpan` is a no-op; every span method call is a no-op. The only per-request cost is one `otel.Tracer(...)` lookup (cached after the first call inside `sync.Once`).

### Testing Tracing

Use `observability/testing` helpers — they're exactly the same shape as the metrics test helpers documented above:

```go
import (
    obtest "github.com/gaborage/go-bricks/observability/testing"
    "github.com/gaborage/go-bricks/httpclient/internal/tracking"
    "go.opentelemetry.io/otel"
)

func TestModuleEmitsHTTPSpans(t *testing.T) {
    tp := obtest.NewTestTraceProvider()
    original := otel.GetTracerProvider()
    otel.SetTracerProvider(tp.TracerProvider)
    tracking.ResetTracerForTesting()
    defer func() {
        otel.SetTracerProvider(original)
        tracking.ResetTracerForTesting()
    }()

    // ... exercise code that performs an outbound HTTP call ...

    collector := obtest.NewSpanCollector(t, tp.Exporter)
    collector.AssertCount(2) // 1 parent Do span + 1 child attempt span
    span := collector.WithName("GET stripe").First()
    obtest.AssertSpanAttribute(t, &span, "peer.service", "stripe")
}
```

**Internal httpclient tests only:** `tracking.ResetTracerForTesting()` resets cached tracer state so the next `InitHTTPTracer()` re-registers against whichever provider is active. Same `internal/` boundary as `ResetMeterForTesting`.

## Payload Logging

> **Warning:** payload logging is a debug aid for development/staging only. Enabling it in production widens the audit-log surface and may expose sensitive data to log pipelines.
>
> **PCI/PII workloads must extend the default sensitive-field list.** The framework ships defaults like `password`, `token`, `api_key`, `authorization`, but workload-specific fields (`pan`, `cvv2`, `cvv`, `otp`, `ssn`, …) need to be added via `log.sensitivefields` in YAML or `app.Options.LoggerFilterConfig` in code before enabling payload logging — see [observability.md](observability.md#sensitive-data-filtering) for the full field list and customization seams.

By default the client logs only request/response metadata (method, URL, status, elapsed, body size). Debug-level payload logging can be enabled via the builder:

```go
client := httpclient.NewBuilder(logger).
    WithLogPayloads(true).
    WithMaxPayloadLogBytes(2048). // default 1024; must be > 0
    Build()
```

**Content-type-aware logging:** Request and response bodies are handled differently depending on the `Content-Type` header:

| Content-Type | Behaviour |
|---|---|
| `application/json` or `*+json` | Body is parsed with `json.Unmarshal`. If the root is a JSON object, it is logged as `body_preview` after `SensitiveDataFilter` walks it to mask sensitive keys (`password`, `token`, `api_key`, …); nested maps and arrays inside that object root are processed recursively. Primitive and array roots are dropped — the filter requires a top-level JSON object with keys to walk and mask, so root-level scalars (`"secret-token"`, `123456`) and bare arrays would land verbatim without one. |
| Everything else (form-urlencoded, binary, multipart, missing/unknown) | Bytes are **not** logged. Instead `body_content_type` and `body_preview_dropped` (byte count) appear in the log. Form-urlencoded bodies often carry credential pairs; multipart and binary blobs are not filterable. |

**JSON parse failure:** If the Content-Type is JSON but the body is malformed (e.g. truncated by `MaxPayloadLogBytes`), `body_content_type` and `body_preview_status: json_parse_failed` are logged instead of raw bytes.

**Recommendation:** Keep `WithLogPayloads` disabled in production configs. If you need body inspection in production, log only the specific fields you need at the application layer (before or after the HTTP call) rather than enabling blanket payload logging.
