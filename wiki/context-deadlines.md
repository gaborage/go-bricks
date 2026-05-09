# Context Deadlines & Timeouts

> **Mental model:** GoBricks treats `context.Context` as the primary carrier of deadlines and cancellation. The framework configures timeouts at every external boundary — HTTP server, HTTP client, database pool, AMQP, Redis, observability exporter, startup — and lets those deadlines propagate through the call stack. Inside business logic, **the default is to use the inherited deadline**: do not introduce new timeouts unless you have a specific reason to *shorten* what's already in flight (or, rarely, to *detach* from the request lifecycle for fire-and-forget work).

## Where deadlines come from

Every operation that crosses an external boundary already has a configured timeout. Module authors do not need to wrap these themselves. The table below covers timeouts that shape **runtime, request-scoped behavior** — the budgets your handler context observes:

| Boundary | Config key | Default | Notes |
|---|---|---|---|
| HTTP request handler (deadline applied to `c.Request().Context()`) | `server.timeout.middleware` | **5s** | Set by the framework's `Timeout` middleware. This is the budget every handler inherits. |
| HTTP server read | `server.timeout.read` | 15s | Time to read the request body |
| HTTP server write | `server.timeout.write` | 30s | Time to write the response (must exceed `middleware`) |
| HTTP server idle | `server.timeout.idle` | 60s | Keep-alive idle |
| HTTP server graceful shutdown | `server.timeout.shutdown` | 10s | Drain inflight requests on SIGTERM |
| Outbound HTTP client | `httpclient.NewBuilder(...).WithTimeout(d)` | 30s | Per-request timeout on the underlying `http.Client` |
| Cache (Redis) dial / read / write | `cache.redis.{dialtimeout,readtimeout,writetimeout}` | 5s / 3s / 3s | Per-operation socket timeouts |
| AMQP connection establishment | `messaging.reconnect.connection_timeout` | 30s | Includes publish confirmation |
| Scheduler — slow job warning | `scheduler.timeout.slowjob` | 25s | Logs WARN if a job exceeds this; does not cancel |
| Scheduler — graceful shutdown | `scheduler.timeout.shutdown` | 30s | Wait for in-flight jobs on shutdown |
| Observability export | `observability.trace.export.timeout` | 10s (dev) / 60s (prod) | OTLP export RPC |

**Boundary maintenance / pool hygiene timeouts** — connection lifetime caps, idle eviction TTLs, and reconnect backoff caps don't propagate as deadlines on a request `ctx`. They live in the per-component reference docs: see [database.md](database.md) (`pool.idle.time`, `pool.lifetime.max`, `pool.keepalive.interval`), [cache.md](cache.md) (`manager.idle_ttl`), [messaging.md](messaging.md) (`reconnect.max_delay`, `publisher.idle_ttl`), [outbox.md](outbox.md), and [startup-defaults.md](startup-defaults.md).

## The default pattern: do nothing

Inside a handler, the request context already carries a 5-second deadline (the configured `server.timeout.middleware`). The deadline propagates through every operation that takes a `context.Context`:

```go
func (h *Handler) getOrder(req GetOrderReq, ctx server.HandlerContext) (server.Result[Order], server.IAPIError) {
    reqCtx := ctx.Echo.Request().Context()  // inherits the 5s deadline

    // Each call below observes the inherited deadline — no manual wrapping needed:
    _, _ = h.cache.Get(reqCtx, fmt.Sprintf("order:%d", req.ID))  // Redis dial/read/write
    order, err := h.svc.FindByID(reqCtx, req.ID)                 // DB query
    if err != nil {
        return server.Result[Order]{}, server.NewInternalServerError(err.Error())
    }
    _, _ = h.pricingClient.Get(reqCtx, order.SKU)                // outbound HTTP

    return server.NewResult(http.StatusOK, order), nil
}
```

When the deadline fires, every in-flight operation observes `ctx.Done()` and returns `context.DeadlineExceeded` — the framework's central error handler then surfaces a `503` envelope. Wrapping any of those calls with another `context.WithTimeout` would either be redundant (same duration) or would *shorten* the already-tight 5s budget, which is rarely what you want by default.

## When to shorten the inherited deadline

The legitimate use case is when one sub-operation should fail fast so the rest of the request budget can do something else. The canonical example is a cache-then-DB lookup, where the cache check should be capped tightly so a Redis hiccup doesn't burn the whole 5s budget:

```go
func (s *UserService) Get(ctx context.Context, id int64) (*User, error) {
    // Cap the cache lookup at 200ms; if Redis is slow we'd rather fall through to DB.
    cacheCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
    defer cancel()

    if data, err := s.cache.Get(cacheCtx, fmt.Sprintf("user:%d", id)); err == nil {
        return cache.Unmarshal[*User](data)
    }

    // DB query keeps the rest of the request budget (~4.8s).
    return s.queryDatabase(ctx, id)
}
```

Recommended budgets when shortening (these are *upper bounds*, not floors):

| Operation | Recommended cap | Rationale |
|---|---|---|
| Hot-path cache lookup before DB fallback | **200–500 ms** | If Redis is slow, fall through fast |
| Idempotency-key check / dedup lookup | **100–300 ms** | Short by design; fall through to actual work on miss |
| Optional enrichment (recommendations, A/B flags) | **500 ms – 1 s** | Best-effort augmentation; degrade on timeout |
| Internal HTTP RPC to a known-fast service | **1–2 s** | Tighter than the default 30s on `httpclient.WithTimeout` |
| External partner API (e.g. payments, KYC) | **5–10 s** | Use the default `httpclient` timeout; tune per partner SLA |
| Background job step inside a scheduler `Executor` | **per-job** | Scheduler doesn't cancel slow jobs — it logs at `slowjob` (25s) and reports duration. Apply your own deadline if the step has a known SLO. |

**Always pair `context.WithTimeout` with `defer cancel()`** — the linter (`govet`/`lostcancel`) will flag missing cancels, but the discipline matters because leaked cancel functions hold context references and can prevent goroutines from exiting.

## When to detach from the request lifecycle

For fire-and-forget background work that must outlive the request (e.g. sending a webhook from a handler that's returning 202 Accepted), use `context.WithoutCancel` (Go 1.21+) to inherit context *values* — trace IDs, tenant ID, logger — while *severing* the parent's cancellation/deadline:

```go
func (h *Handler) acceptJob(req JobReq, ctx server.HandlerContext) (server.Result[Receipt], server.IAPIError) {
    reqCtx := ctx.Echo.Request().Context()

    bgCtx := context.WithoutCancel(reqCtx)              // inherits values, sheds deadline
    bgCtx, cancel := context.WithTimeout(bgCtx, 30*time.Second)  // re-apply a budget

    go func() {
        defer cancel()
        if err := h.worker.Process(bgCtx, req); err != nil {
            h.logger.WithContext(bgCtx).Error().Err(err).Msg("background job failed")
        }
    }()

    return server.Accepted(Receipt{ID: req.ID}), nil
}
```

**Why not `context.Background()`?** It severs *everything* — trace ID, tenant ID, request ID, custom logger fields. The job's logs and spans become unattributable. `context.WithoutCancel` is almost always the right tool for "outlive the request, keep the context values."

For scheduler jobs, the `JobContext` passed to `Executor.Execute` carries a context that lives for the job's run and is cancelled on scheduler graceful shutdown (`scheduler.timeout.shutdown`). Jobs SHOULD honor `ctx.Done()` so a SIGTERM doesn't get blocked by a slow job.

## Common pitfalls

| Pitfall | Symptom | Fix |
|---|---|---|
| Forgetting `defer cancel()` on `context.WithTimeout` | `lostcancel` lint failure; goroutine leaks under load | Always pair `cancel` with `defer` on the next line |
| Calling `context.Background()` mid-handler | Logs and spans missing trace/tenant attribution; broken trace tree | Use the inherited `ctx` or `context.WithoutCancel(ctx)` |
| Wrapping with timeout *longer* than the inherited deadline | The longer timeout is silently ignored — the parent's earlier deadline still fires | Don't wrap; the inherited deadline is the cap. If you need longer, see "detach" above |
| Long loops without `ctx.Err()` checks | Handler keeps running after deadline fires; wasted CPU; eventual `503` from middleware | Check `ctx.Err()` (or `select { case <-ctx.Done(): return ctx.Err(); default: }`) at every iteration boundary in long loops |

## Why context-only timeouts (not `http.TimeoutHandler`)

GoBricks' [`server.Timeout`](../server/timeout.go) middleware applies a deadline by wrapping `c.Request().Context()` with `context.WithTimeout` — it does **not** swap the response writer the way Echo's stock `middleware.TimeoutWithConfig` (which wraps `net/http.TimeoutHandler`) would. The trade-off is deliberate: the response-writer swap invalidates `c.Response()` mid-handler, which in turn panics any logging/observability middleware that reads response headers or status. Context-only timeouts let the handler observe cancellation via `ctx.Done()` while keeping the response object valid; the framework's central error handler then renders a standardized 503 envelope.
