# ADR-132: HTTP server instrumentation moves to `echo-otel/v5`, sanitized inside it

**Status:** Accepted
**Date:** 2026-10-01

## Context

`github.com/labstack/echo-opentelemetry` is deprecated. Its v0.0.4 is v0.0.3 plus a `// Deprecated:`
marker pointing at `github.com/labstack/echo-otel/v5`, and it will get no further fixes. The
successor keeps the API: the same `Config` fields, `NewMiddlewareWithConfig` and `*Values`, so the
framework's code change is the import path. Its behavior differs in ways a consumer can see:

- The instrumentation scope name is `github.com/labstack/echo-otel/v5`, where it was
  `github.com/labstack/echo-opentelemetry`.
- `Values.MetricAttributes()` no longer includes `server.address`/`server.port`. Both are Opt-In
  for HTTP server metrics, and from `Request.Host` the client chooses them.
- `http.request.method_original` leaves metrics too (spans keep it), spans stop carrying
  `http.request.body.size`/`http.response.body.size` (the body-size histograms keep recording
  them), and HTTP/2 and HTTP/3 report `network.protocol.version` as `2`/`3` instead of `2.0`/`3.0`.
- It records `error.type` natively on spans and metrics, omits `http.response.status_code` when
  no status was sent, and takes `http.route` from the matched Echo route. It also moves to semconv
  v1.40.
- **It recovers panics.** A panic from the chain below it, or a `middleware.PanicStackError`
  returned by a `Recover` below it, is recorded as `span.SetStatus(codes.Error, "panic: %v")` of
  the panic value, and then re-panicked. v0.0.3 had no `recover()`.

The last point collides with ADR-081, which requires that a recovered panic value be reported by
type only. The framework's chain is outermost recover → request ID → instrumentation → request
enrich → CORS → IP pre-guard → tenant resolution → forwarded client cert → access log → `Recover`
→ `sanitizePanicValue` → … → handler. A handler panic is sanitized before `Recover` sees it, so
it reaches the span as its type. A panic in any middleware between the instrumentation and
`Recover` is not: echo-otel's own recover would put its value in the span status.

## Decision

1. Instrument incoming HTTP with `github.com/labstack/echo-otel/v5`, configured as before.
2. Register a second `sanitizePanicValue()` **immediately inside** the instrumentation
   (`useOTelMiddleware`). Every panic below it is re-panicked as `panicTypeError` before
   echo-otel's recover sees it, so the span status reads `panic: panic (type: <T>)`. The existing
   `Recover` + `sanitizePanicValue` pair stays where it is. Moving it outside the instrumentation,
   or wrapping the instrumentation from outside, would let echo-otel see raw handler panics first,
   because `Echo.Use` registers outermost first.
3. Accept the upstream metric attribute set. `server.address` is not re-added: it would only
   repeat `cfg.App.Name`, which every series already carries as the `service.name` resource
   attribute. The framework still appends the trusted-peer `url.scheme` and the status-code
   `error.type` for every 4xx/5xx.

## Consequences

- Breaking for telemetry consumers (`[C70.17]`). Queries, dashboards or alerts that filter on
  the instrumentation scope `github.com/labstack/echo-opentelemetry`, or that group HTTP server
  metrics by `server.address`/`server.port`, must be repointed, as must those reading
  `http.request.method_original` from metrics, body sizes from spans, or a
  `network.protocol.version` of `2.0`/`3.0`. The series identity of
  `http.server.request.duration` changes, so its history is discontinuous at the upgrade.
- Spans of 5xx responses gain `error.type`, and a request aborted before any status was sent has
  no `http.response.status_code`.
- No Go API changes for consumers: `echo.*` stays off the consumer surface (ADR-034).
- A panic between the instrumentation and `Recover` is still re-panicked to the outermost
  recover, as before. The only new effect is that the span now records it, by type.

## References

- [ADR-015](adr_015_echo_v5_migration.md): adopted `echo-opentelemetry`.
- [ADR-081](adr_081_recovered_panic_values_reported_by_type.md): panic values are reported by type.
- [ADR-034](adr_034_echo_boundary_types.md): no echo types on the consumer surface.
- [migrations.md](migrations.md) `[C70.17]`
- `server/middleware.go` (`useOTelMiddleware`, `sanitizePanicValue`) ·
  `server/otel_middleware_test.go` (`TestOTelMiddlewareNeverRecordsAPanicValue`)
