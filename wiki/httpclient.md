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
