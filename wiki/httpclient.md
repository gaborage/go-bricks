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

| Attribute | Notes |
|---|---|
| `peer.service` | Omitted when `WithPeerName` is not set. |
| `http.request.method` | Canonical HTTP method. |
| `server.address` | Omitted when not available. |

**`http.client.retries.total` attributes:**

| Attribute | Notes |
|---|---|
| `peer.service` | Omitted when `WithPeerName` is not set. |
| `http.request.method` | Canonical HTTP method. |
| `retry.reason` | One of `"timeout"`, `"network"`, `"5xx"`, `"build_response"`. |

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

The internal `tracking` package (at `httpclient/internal/tracking`) exposes `ResetMeterForTesting()` in `testing.go` for tests that need to swap meter providers between sub-tests. It resets all package-level instrument state to nil so the next `InitHTTPMeter()` call re-registers with whatever provider is active at that point. Because the package is under `internal/`, it is only importable by code within the `httpclient` package boundary.

## Payload Logging

> **Warning:** payload logging is a debug aid for development/staging only. Enabling it in production widens the audit-log surface and may expose sensitive data to log pipelines.
>
> **PCI/PII workloads must extend the default sensitive-field list.** The framework ships defaults like `password`, `token`, `api_key`, `authorization`, but workload-specific fields (`pan`, `cvv2`, `cvv`, `otp`, `ssn`, …) need to be added via `log.sensitive_fields` in YAML or `app.Options.LoggerFilterConfig` in code before enabling payload logging — see [observability.md](observability.md#sensitive-data-filtering) for the full field list and customization seams.

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
