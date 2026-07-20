# Design spike: an operator surface for dead-lettered outbox events

Status: **design/spike** — the deliverable is this document. No production code is
proposed for this commit. A follow-on build plan (sketched at the end) executes it.

## Problem

When the outbox relay exhausts retries on a **poison** event (undecodable headers), it
parks the row at `status='failed'` via `MarkDeadLettered` and emits one WARN. After that
the event is invisible to an operator:

- The `Store` interface has **no read API** for failed rows — the only method that touches
  the failed state is `MarkDeadLettered`, and it is a *write* (`outbox/store.go:66-71`).
- The outbox module registers **no HTTP routes** — `grep -rn "RegisterRoutes\|_sys" outbox/*.go`
  returns nothing; `outbox/module.go` implements `RegisterJobs` (`outbox/module.go:200-244`)
  but not `RouteRegisterer`.
- There is **no backlog metric** — nothing counts parked rows.

So "how many events are parked right now?" and "retry this one" can only be answered with
direct SQL against the outbox table — the access most production deployments deliberately
deny the runtime DB role. The wiki even *promises* visibility that today only cashes out as
"visible to someone with SQL access" (`wiki/outbox.md:164-165`: failed rows "are
intentionally never auto-deleted so they stay visible").

**Scope honesty.** Only poison (undecodable headers) ever parks. Every connectivity failure
(broker down, NACK, confirmation timeout) advances `retry_count` but keeps retrying forever
and is *never* parked (`outbox/relay.go:299-317` and the `MaxRetries` doc at
`config/types.go:637-642`). So the real-world dead-letter backlog is **low-volume but
high-signal**: every parked row is a bug or a corruption artifact, not routine churn. That
shapes the design — a cheap standing count and a small list/retry surface, not a
high-throughput queue console.

The scheduler package already ships the precise pattern we need: a CIDR-restricted
`GET /_sys/job` list plus a `POST /_sys/job/:jobId` manual trigger. This spike specifies the
outbox equivalent so the build plan lands without design churn.

## Evidence

Every reference below was re-opened and re-confirmed in this worktree (HEAD `708216e`; the
`git diff --stat 0d71e99..HEAD` over the cited files is empty, so the code matches the plan's
verified leads).

### The gap (no read API, no routes, cleanup skips failed)

- `outbox/store.go:49-80` — the `Store` interface: `Insert` (:51), `FetchPending` (:57),
  `MarkPublished` (:60), `MarkFailed` (:64), `MarkDeadLettered` (:66-71), `DeletePublished`
  (:73-75), `CreateTable` (:79). **No** `Fetch*`/read method for `status='failed'`.
- `outbox/store.go:53-56` — `FetchPending` is documented as **status-gated only**: "Selection
  is status-gated ... parking is driven by the 'failed' status ... NOT by retry_count." This
  is the property that makes a parallel reader/retrier safe (see race analysis below).
- `outbox/store.go:41-43` — status constants `pending` / `published` / `failed`.
- `outbox/relay.go:299-317` — `deadLetterPoison`: at `RetryCount+1 >= MaxRetries` (:304) it
  calls `MarkDeadLettered` and logs `Warn(...).Msg("Outbox event dead-lettered after
  exhausting retries")` (:309-312). One log line at park time is the *entire* current
  visibility surface.
- `outbox/module.go:32-42` — the `Module` struct holds `logger`, `config`, `getDB`, `getMsg`,
  `publisher`, `cfg`, and `stores tenantstore.Cache[Store]` (one store per tenant, `""` =
  single-tenant). No routes, no metric instrument.
- `outbox/cleanup.go:26-28` — the cleanup job deletes via `DeletePublished` only, so failed
  rows accumulate (confirms `wiki/outbox.md:164-165`).

### The precedent to mirror (scheduler)

- `scheduler/api_handlers.go:36-68` — `listJobsHandler` (GET `/_sys/job`): typed
  `JobListResponse{Data, Meta}` envelope (:9-13), returns `server.NewResult(http.StatusOK,
  response)` (:67). Note it returns **all** jobs unpaginated — fine for a fixed, small job set,
  but not a model for unbounded outbox rows (cap REQUIRED, see API section).
- `scheduler/api_handlers.go:70-103` — `triggerJobHandler` (POST `/_sys/job/:jobId`):
  path param via `JobIDParam` (:31-34), 404 via `server.NewNotFoundError("job")` (:80),
  202-Accepted via `server.NewResult(http.StatusAccepted, response)` (:102). The param struct:

  ```go
  type JobIDParam struct {
      JobID string `param:"jobId" validate:"required"`
  }
  ```
- `scheduler/cidr_middleware.go:23-55` — `CIDRMiddleware(log, allowlist, trustedProxies)
  server.MiddlewareFunc`: empty allowlist → localhost-only (`parseCIDRAllowlist` :60-63),
  trusted-proxy-aware client IP via **`server.ClientIP`** (:85) and **`server.ParseCIDRs`**
  (:25). It lives in package `scheduler` — the cross-module coupling question (see below).
- `scheduler/module.go:139-162` — `RegisterRoutes`: builds the CIDR middleware from
  `m.config.Scheduler.Security.CIDRAllowlist` / `.TrustedProxies` (:151-152), creates the
  `/_sys` group and applies the middleware (`sysGroup.Use(cidrMiddleware)` :157-158), then
  registers `server.GET(hr, sysGroup, "/job", ...)` / `server.POST(...)` (:160-161).
- `server/clientip.go:14` (`ParseCIDRs`) and `server/clientip.go:43` (`ClientIP`) — the
  low-level, spoofing-resistant IP primitives **already live in `server`**. Only the
  allowlist-matching *wrapper* lives in `scheduler`. This materially strengthens the
  "lift the middleware to `server`" recommendation.

### Config precedent

- `config/types.go:611-655` — `OutboxConfig` field/tag style (`koanf`/`json`/`yaml`/`toml`/
  `mapstructure` on every field).
- `config/types.go:687-710` — `SchedulerConfig.Security SchedulerSecurityConfig`, whose
  `CIDRAllowlist []string` (:704) and `TrustedProxies []string` (:709) are the shapes to
  mirror.

### Multi-tenant / metric precedent

- `outbox/relay.go:46-56` — `Execute` iterates `for _, tenantID := range r.tenants` (:49),
  calling `multitenant.SetTenant(jobCtx, tenantID)` per tenant.
- `outbox/module.go:207` — `tenants := m.config.PerTenantJobKeys()` feeds `r.tenants`; the
  relay fans out over exactly the **statically configured** tenants (or `[""]` single-tenant).
- `config/config.go:198-210` — `PerTenantJobKeys()` is the static enumeration seam. Dynamic
  multi-tenant sources are **rejected at Init** (`outbox/module.go:102-111`), so there is no
  runtime "list every live tenant" API by design.
- `config/tenant_store.go:175` — `TenantStore.Tenants()` returns the *statically configured*
  map only; it is not a runtime discovery of dynamically-provisioned tenants.
- `observability/metrics.go:236/258/278` — `CreateCounter` / `CreateHistogram` /
  `CreateUpDownCounter` helpers.
- `observability/metrics.go:287-288` — a note that **observable gauges have no helper**: they
  are "created directly using `meter.Int64ObservableGauge` ... with callbacks."
- **No `tenant.id` metric-attribute convention exists.** `grep -rn "attribute.String" ...`
  finds tenant attributes on *no* metric; the closest tenant conventions are the relay's
  per-cycle log field `Str("tenant", tenantID)` (`outbox/relay.go:207`) and the AMQP
  `x-tenant-id` publish header (`messaging/tenant_publisher.go:11`). The metric section below
  resolves what to do given this absence.

## API surface

Two endpoints under a new CIDR-restricted `/_sys/outbox` group, mirroring
`scheduler/module.go:139-162`. Both use the standard `server.Result` / `{data, meta}`
envelope and typed request/response structs (`scheduler/api_handlers.go:9-34` pattern).

### `GET /_sys/outbox/dead-letter` — list parked rows

- **Selects** `status='failed'` rows, ordered `(created_at ASC, id ASC)` (oldest first; `id`
  is the deterministic tie-breaker the keyset cursor needs — the relay's own pending ordering is
  `created_at ASC` at `outbox/store_postgres.go:84`).
- **Row fields** (subset of `outbox.Record`, `outbox/store.go:24-37`): `id`, `event_type`,
  `aggregate_id`, `exchange`, `routing_key`, `retry_count`, `error`, `created_at`.
- **Pagination — keyset/cursor, NOT offset**: a `limit` query param (**hard server-side cap**;
  recommend **default 50, max 500**; clamp silently) plus an opaque `cursor` that encodes the
  last-seen `(created_at, id)`. **Why keyset over offset:** `OFFSET` is evaluated against the
  *live* failed-row set, so a concurrent retry that un-parks an earlier row shifts every
  subsequent offset and later pages silently SKIP rows — and `created_at ASC` alone ties
  nondeterministically; keyset over `(created_at, id)` is stable under concurrent retry and
  deterministically tie-broken by `id`. Unlike `/_sys/job` (which returns all jobs — a small,
  fixed set), outbox failed rows are unbounded, so the cap is REQUIRED. Surface in `meta`:
  `limit`, `nextCursor` (null when the page exhausts the set), and — cheaply, from the same
  count query the metric uses — `total`, so the operator sees "showing 50 of N".
- **Payload EXCLUDED by default.** The `payload` column is application data (potentially PII —
  the outbox carries whatever the producer wrote). The framework's `SensitiveDataFilter`
  applies to *log lines*, **not to HTTP response bodies**, so echoing `payload` here would be
  an unfiltered data-egress path. Exposing it is deferred to an explicit opt-in — see Open
  Questions.

Response envelope (illustrative):

```json
{
  "data": [
    {
      "id": "0f9c...",
      "eventType": "order.created",
      "aggregateId": "order-42",
      "exchange": "order.events",
      "routingKey": "order.created",
      "retryCount": 5,
      "error": "outbox relay: invalid headers JSON: ...",
      "createdAt": "2026-07-19T04:10:00Z"
    }
  ],
  "meta": { "total": 1, "limit": 50, "nextCursor": null, "timestamp": "...", "traceId": "..." }
}
```

### `POST /_sys/outbox/dead-letter/:eventID/retry` — un-park one row

- **Effect**: flip `status='failed'` → `status='pending'` for the addressed row, so the next
  relay `FetchPending` cycle picks it up.
- **`retry_count` semantics — RECOMMEND reset to 0.** The operator retries *after* fixing the
  poison cause (e.g., corrected a bad header producer), so the row deserves a fresh retry
  budget. A non-reset retry would flip to pending, get fetched once, fail the same header
  decode, and immediately re-park at `RetryCount+1 >= MaxRetries` (`outbox/relay.go:304`) —
  effectively a one-shot that hides whether the fix worked. Also clear `error` (PG) /
  `error_msg` (Oracle) so a subsequent failure records the *new* cause.
- **Path param** via an `EventIDParam` struct (mirrors `JobIDParam`,
  `scheduler/api_handlers.go:31-34`):

  ```go
  type EventIDParam struct {
      EventID string `param:"eventID" validate:"required"`
  }
  ```
- **Status codes**: 202-Accepted on a successful flip (the delivery is asynchronous, like the
  scheduler trigger at `scheduler/api_handlers.go:102`); 404 when no `status='failed'` row
  matches the id (via `server.NewNotFoundError`, `scheduler/api_handlers.go:80`).

**Race analysis** (the reason this is safe without locking):

1. *Retry vs. the relay.* A concurrent relay poll only ever fetches `status='pending'`
   (`FetchPending` is status-gated: `WHERE status='pending'`, `outbox/store_postgres.go:83`,
   `outbox/store_oracle.go:98`; documented at `outbox/store.go:53-56`). Until the retry commits
   the flip, the row is `failed` and invisible to the relay; after the flip it is a normal
   pending row that the relay delivers on its next cycle. There is no window where both act on
   it — benign.
2. *Retry vs. a concurrent retry.* The retry UPDATE is guarded `... WHERE id = ? AND
   status='failed'`. The first commit affects 1 row (→ 202); the second affects 0 rows
   (row is no longer `failed`) → 404. The affected-rows count is the idempotency signal —
   no extra locking needed.

### Store interface extension — the apidiff problem

`Store` is **exported** (`outbox/store.go:49`), so adding methods to it is a breaking change
for any external implementor (fail-closed apidiff gate blocks the PR; this repo has bitten on
exactly this before). Two options:

- **(a) A separate optional `DeadLetterStore` interface**, type-asserted at the call site; when
  the concrete store doesn't implement it the feature degrades gracefully (endpoint returns
  501/unavailable, metric is simply not registered). Additive — **no `!`**.
- **(b) Extend `Store`** with the new methods and take the breaking change (new ADR +
  migrations.md hop + `fix(...)!:` title).

**RECOMMEND (a).** It matches the framework's established duck-typing pattern —
`RouteRegisterer`, `MessagingDeclarer`, `GlobalMiddlewareRegisterer` are all optional
capabilities discovered by assertion, not forced onto every implementor. The framework's own
`postgresStore` / `oracleStore` implement it; a third-party `Store` keeps compiling untouched.

```go
// DeadLetterStore is an OPTIONAL capability a Store may implement to expose the
// operator surface for parked (status='failed') rows. Discovered by type assertion;
// a Store without it degrades the /_sys/outbox endpoints gracefully.
type DeadLetterStore interface {
    // FetchDeadLettered returns up to limit failed rows AFTER the keyset cursor
    // (nil cursor = first page), ordered (created_at ASC, id ASC). Keyset, not
    // offset: stable under a concurrent retry that un-parks an earlier row.
    FetchDeadLettered(ctx context.Context, db dbtypes.Interface, cursor *DeadLetterCursor, limit int) ([]Record, error)
    RetryDeadLettered(ctx context.Context, db dbtypes.Interface, eventID string) (bool, error) // (affected, err)
    CountDeadLettered(ctx context.Context, db dbtypes.Interface) (int64, error)                 // for the metric + list meta.total
}

// DeadLetterCursor is the keyset boundary (the last row of the previous page),
// encoded opaquely into the HTTP `cursor` query param. A nil cursor selects the
// first page.
type DeadLetterCursor struct {
    CreatedAt time.Time
    ID        string
}
```

### New store queries (PG and Oracle shapes)

Sketched from the existing vendor split (`outbox/store_postgres.go` / `outbox/store_oracle.go`).
**Watch the vendor column divergence**: PG names the error column `error` (`error TEXT`,
`outbox/store_postgres.go:25`); Oracle renames it to `error_msg` (`error_msg CLOB`,
`outbox/store_oracle.go:33`, because `error` is an Oracle reserved word — comment at
`outbox/store_oracle.go:18`). Every new query that touches the error column must respect this,
exactly as `MarkDeadLettered` already does (`$2`/`error` at `outbox/store_postgres.go:144` vs
`:2`/`error_msg` at `outbox/store_oracle.go:166`).

**`FetchDeadLettered`** (payload deliberately not selected; alias Oracle's `error_msg` to the
`Record.Error` field):

```sql
-- PostgreSQL — keyset over (created_at, id). $1,$2 = cursor (created_at, id); $3 = limit.
-- First page OMITS the "AND (created_at, id) > ($1, $2)" predicate. PG's row-value
-- comparison is the clean form for a composite keyset boundary.
SELECT id, event_type, aggregate_id, exchange, routing_key, retry_count, error, created_at
FROM <table>
WHERE status = 'failed' AND (created_at, id) > ($1, $2)
ORDER BY created_at ASC, id ASC
LIMIT $3;

-- Oracle — NO row-value comparison; expand the boundary portably. :1 = cursor created_at,
-- :2 = cursor id, :3 = limit. First page OMITS the "AND (...)" boundary predicate.
-- Mirror FetchPending's FETCH FIRST form (store_oracle.go:93-100).
SELECT id, event_type, aggregate_id, exchange, routing_key, retry_count, error_msg, created_at
FROM <table>
WHERE status = 'failed' AND (created_at > :1 OR (created_at = :1 AND id > :2))
ORDER BY created_at ASC, id ASC
FETCH NEXT :3 ROWS ONLY;
```

**`RetryDeadLettered`** — return affected-rows so the handler maps 202 vs 404:

```sql
-- PostgreSQL: reset retry budget + clear last error
UPDATE <table> SET status = 'pending', retry_count = 0, error = NULL
WHERE id = $1 AND status = 'failed';

-- Oracle
UPDATE <table> SET status = 'pending', retry_count = 0, error_msg = NULL
WHERE id = :1 AND status = 'failed';
```

**`CountDeadLettered`** — one cheap query, reused by both the list `meta.total` and the metric:

```sql
SELECT count(*) FROM <table> WHERE status = 'failed';   -- both vendors
```

> The relay's partial index is `WHERE status='pending'` (`outbox/store_postgres.go:33`), so
> `status='failed'` keyset scans are not index-backed today. Given the low expected cardinality
> of parked rows this is acceptable; if a deployment ever accumulates many, the build plan can
> add a partial index on `(created_at, id) WHERE status='failed'` to serve the keyset order.
> Flag, don't pre-optimize (YAGNI).

## CIDR middleware reuse — recommendation

**Options.**
- **(a)** `outbox` imports `scheduler.CIDRMiddleware` directly. Works today (it is exported at
  `scheduler/cidr_middleware.go:23`), but couples `outbox → scheduler` for an HTTP concern —
  the outbox already depends on `scheduler` for the *relay job* (`outbox/relay.go:15`), yet
  taking a second dependency for middleware is the wrong direction (a security primitive
  should not live in a job-scheduling package).
- **(b)** Lift the middleware into `server` (e.g. `server.CIDRRestrict(log, allowlist, proxies)
  server.MiddlewareFunc`) and have `scheduler.CIDRMiddleware` become a thin one-line wrapper
  that delegates. Both `outbox` and `scheduler` then depend only on `server`, where the IP
  primitives **already live**.

**RECOMMEND (b).** Evidence it is the natural home: the spoofing-resistant primitives
`server.ClientIP` (`server/clientip.go:43`) and `server.ParseCIDRs` (`server/clientip.go:14`)
are *already* in `server`; the scheduler middleware is just an allowlist-matching shell around
them (`scheduler/cidr_middleware.go:24-55`). apidiff trade-off: **adding** `server.CIDRRestrict`
is compatible (additive), and keeping `scheduler.CIDRMiddleware` as an exported thin wrapper is
also compatible (its signature is byte-identical) — so the lift breaks nothing. **This lift
belongs to the build plan's first, separately-verifiable commit** so the move is reviewed and
green before any outbox route rides on it.

## Multi-tenant addressing — recommendation

The route is process-global, but stores and DBs are **per-tenant**
(`stores tenantstore.Cache[Store]`, `outbox/module.go:41`; `getDB(ctx)` resolves by the tenant
in ctx). `/_sys/job` never faced this because scheduler jobs are process-global metadata, not
per-tenant DB rows (`listJobsHandler` reads `m.jobs`, `scheduler/api_handlers.go:38-56`).

**Options.**
- **(a) One tenant per call, addressed by a discriminator.** The tenant-resolution middleware
  already runs before handlers and puts the resolved tenant in ctx (the standard resolver
  header, e.g. `X-Tenant-ID`), so the handler just calls `deps.DB(ctx)` / the tenant's store
  exactly like every other request path. No new addressing machinery — the operator sends the
  same tenant header they'd send to any tenant-scoped endpoint.
- **(b) Fan out across all tenants in one response.** **Rejected**: there is no runtime
  "enumerate every live tenant" seam by design. `PerTenantJobKeys()` (`config/config.go:210`)
  and `TenantStore.Tenants()` (`config/tenant_store.go:175`) enumerate only *statically
  configured* tenants, and dynamic sources are explicitly rejected at outbox Init
  (`outbox/module.go:102-111`). A fan-out endpoint would silently miss dynamically-provisioned
  tenants and give a false "0 parked everywhere" — worse than no endpoint.

**RECOMMEND (a).** It reuses the existing tenant middleware and the per-tenant store cache with
zero new concepts; the operator scopes each call to one tenant via the resolver the deployment
already trusts. (Note: resolution is *identification, not authorization* — the deployment must
still gate who may hit `/_sys/outbox`; that is exactly what the CIDR allowlist is for here, the
same posture the scheduler endpoints rely on.) In single-tenant mode the resolver yields `""`
and it all collapses to the one store — no discriminator needed.

## Config sketch

Mirror `SchedulerSecurityConfig` (`config/types.go:699-710`) field-for-field, nested under a new
`OutboxConfig.API` block (all five tags on every field, per the `OutboxConfig` convention at
`config/types.go:611-655`). Default **disabled** (additive, opt-in — matches the framework's
fail-closed posture):

```go
// OutboxConfig gains:
//   API OutboxAPIConfig `koanf:"api" json:"api" yaml:"api" toml:"api" mapstructure:"api"`

// OutboxAPIConfig holds settings for the /_sys/outbox operator endpoints.
type OutboxAPIConfig struct {
    // Enabled activates the /_sys/outbox/dead-letter endpoints. Default: false (opt-in).
    Enabled bool `koanf:"enabled" json:"enabled" yaml:"enabled" toml:"enabled" mapstructure:"enabled"`

    // CIDRAllowlist holds CIDR ranges allowed to reach /_sys/outbox* endpoints.
    // Empty list = localhost-only (matches scheduler semantics).
    CIDRAllowlist []string `koanf:"cidrallowlist" json:"cidrallowlist" yaml:"cidrallowlist" toml:"cidrallowlist" mapstructure:"cidrallowlist"`

    // TrustedProxies holds CIDR ranges of trusted reverse proxies (X-Forwarded-For / X-Real-IP
    // honored only from these peers). Empty list = trust no proxy headers.
    TrustedProxies []string `koanf:"trustedproxies" json:"trustedproxies" yaml:"trustedproxies" toml:"trustedproxies" mapstructure:"trustedproxies"`
}
```

Config keys: `outbox.api.enabled` (default `false`), `outbox.api.cidrallowlist`,
`outbox.api.trustedproxies`. `RegisterRoutes` is a no-op when `outbox.api.enabled=false`, so
the endpoints simply don't exist unless opted in.

## Metric

- **Name**: `outbox.dlq.depth`
- **Instrument**: observable gauge (`meter.Int64ObservableGauge` with a callback — there is no
  framework helper; `observability/metrics.go:287-288` documents using the meter directly).
  Registered in `Module.Init` via `deps.MeterProvider` guarded by a nil check, exactly like the
  CLAUDE.md custom-metrics snippet. A gauge (not a counter): it reports a *current level* that
  rises on park and falls on retry/manual cleanup.
- **Sample point — RECOMMEND (a) per relay cycle.** The relay already runs per configured
  tenant every `PollInterval` (`outbox/relay.go:46-56`, iterating `r.tenants` at :49) with a DB
  handle in hand, so folding one `CountDeadLettered` (`SELECT count(*) WHERE status='failed'`)
  into that pass is a single cheap query per tenant per poll — no new schedule, no standing
  goroutine. The alternative (b), counting only on-demand inside the GET handler, yields no
  standing series to alert on, defeating the point (alerting on a gauge beats alerting on the
  park-time WARN log line at `outbox/relay.go:309-312`). The count query is the same
  `CountDeadLettered` added in the Store extension.
  - Observable-gauge nuance: OTel observable callbacks are pull-based, so rather than "push on
    each relay cycle," the cleaner shape is a registered callback that runs `CountDeadLettered`
    for each tenant when the SDK collects. Either way the query is the one cheap indexed-absent
    count; the build plan picks push-vs-callback against the meter API. (Recorded as a minor
    open detail, not a blocker.)
- **Tenant dimension — the honest answer.** There is **no existing `tenant.id` metric-attribute
  convention** in the framework (grep across `observability/` and `messaging/` finds none; the
  only tenant-tagging precedents are the relay's *log* field `Str("tenant", tenantID)`,
  `outbox/relay.go:207`, and the AMQP `x-tenant-id` *header*, `messaging/tenant_publisher.go:11`).
  So the build plan introduces the attribute deliberately: emit one gauge series **per tenant**
  with `attribute.String("tenant.id", tenantID)` when multi-tenant, and a single series with no
  tenant attribute in single-tenant mode (tenantID `""`). This follows the relay's own
  per-tenant log convention and keeps single-tenant dashboards attribute-free. Flag it in the
  build plan as a new-convention decision so a reviewer signs off on the attribute key.

## Open questions (for the operator)

1. **Payload opt-in.** Should `GET .../dead-letter` support an explicit
   `?includePayload=true` (or a separate `GET .../dead-letter/:eventID` detail route) that
   returns the raw `payload`? It is often exactly what an operator needs to decide whether to
   retry — but it is unfiltered application data over HTTP (the `SensitiveDataFilter` does not
   apply to response bodies). If yes, it should be a distinct, separately-gated capability
   (perhaps its own CIDR allowlist or an audit hook), not the list default.
2. **Audit event on retry.** Should `POST .../retry` emit an audit event (who un-parked which
   event, when)? Un-parking is an operator mutation of delivery state, which is the same class
   of action the migration subsystem already audits. Precedent for the seam:
   `migration.NewEmitter(log, sink)` + `Executor.WithAudit(...)` (see `wiki/migration_audit.md`).
   Reusing that shape would keep a single audit schema; the cost is wiring an emitter into the
   outbox module. Recommend deferring to the build plan but flagging it now.
3. **Should the metric ship independently of the endpoints?** The gauge is useful even to a
   deployment that never enables the HTTP surface (`outbox.api.enabled=false`). Recommend the
   metric registers whenever outbox is enabled, decoupled from `outbox.api.enabled` — but
   confirm the operator wants standing DLQ-depth telemetry on by default.

## Build-plan outline (the future executable plan)

Sequenced so each commit is independently verifiable; roughly **2–3 focused PRs, < ~600 LoC each**.

1. **Commit 1 — lift CIDR middleware to `server`.** Add `server.CIDRRestrict(...)`, reduce
   `scheduler.CIDRMiddleware` to a thin delegating wrapper (byte-identical signature). Pure
   refactor; unit tests move/duplicate; apidiff stays green (additive). ~150 LoC. *(Effort: S.)*
2. **Commit 2 — `DeadLetterStore` capability + vendor queries.** Add the optional interface
   (`outbox/store.go`), implement `FetchDeadLettered` / `RetryDeadLettered` / `CountDeadLettered`
   on `postgresStore` and `oracleStore` respecting the `error`/`error_msg` divergence. Unit +
   integration (testcontainers) coverage for both vendors, including the retry affected-rows
   404/202 path. Additive — no `!`. ~250 LoC. *(Effort: M.)*
3. **Commit 3 — `OutboxAPIConfig` + `RegisterRoutes`.** Add config block (default off), implement
   `RouteRegisterer` on the outbox module gated by `outbox.api.enabled`, wire the two handlers
   with the lifted CIDR middleware and per-tenant `deps.DB(ctx)`. Handler tests (httptest) for
   200/404/202, cap clamping, and CIDR rejection. ~200 LoC. *(Effort: M.)*
4. **Commit 4 — `outbox.dlq.depth` gauge.** Register the observable gauge in `Module.Init`
   (nil-guarded `deps.MeterProvider`), sample via `CountDeadLettered` per tenant with the
   `tenant.id` attribute decision. ~120 LoC. *(Effort: S.)*
5. **Docs**: extend `wiki/outbox.md` (the accumulate/prune note at :164-165 becomes "list &
   retry via `/_sys/outbox`"), CLAUDE.md outbox stub, `startup_defaults.md` for the new keys.
6. **Governance**: additive-only feature → `feat:` + adopt-only migrations note; the
   `tenant.id` metric attribute and the (a) `DeadLetterStore` duck-typing choice each warrant a
   line in the PR description; no new ADR strictly required unless the operator picks Store
   option (b).
7. Link this surface to **plan 030** (declarative messaging DLQ) — the two are complementary
   halves of "what happens to poison" (consumer-side broker DLQ vs. producer-side outbox
   dead-letter); cross-reference both docs in the build plan.

## Non-goals

- **No auto-retry policy.** Parked = poison = a human decides. The surface exposes *manual*
  retry only; no scheduled re-drain of failed rows.
- **No deletion endpoint.** Purging stays with the existing `outbox-cleanup` job
  (`outbox/cleanup.go`); this surface never deletes rows (that would erase forensic evidence of
  the bug that parked them).
- **No Tier-2 messaging redelivery.** Broker-side DLQ redelivery is out of scope here (already
  scoped out as plan 030's non-goal); this is strictly the *outbox table* operator surface.
- **No cross-tenant fan-out** (see Multi-tenant recommendation — no runtime enumeration seam
  exists, so it is deliberately excluded).
