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
