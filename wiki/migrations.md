# Breaking Change Migrations

Historical migration tables for upgrading existing GoBricks-based applications. Greenfield work can ignore this file — the new APIs are the only ones documented in CLAUDE.md.

## Graceful Shutdown Now Stops Inbound Work First (ADR-029)

Per [ADR-029](adr_029_graceful_shutdown_order.md), `App.Shutdown` reordered its phases. **Before:** modules were torn down first, while the HTTP server was still serving and AMQP consumers were still delivering — so in-flight handlers could run against already-shut-down modules (panics/errors during the shutdown window). **After:** `server → consumers → modules → observability → closers`.

**No code or config change is required** — this is an internal behavioral correction. Two things to be aware of:

- If a module's `Shutdown()` implicitly relied on the HTTP server still serving or on consumers still delivering, it will now see the corrected order (server drained and consumers stopped before module teardown). This was the buggy case the reorder fixes.
- `messaging.Manager` gains an additive `StopConsumers()` method (quiesce consumers without closing connections); existing code is unaffected.

## PostgreSQL `BuildUpsert` Now Binds Update Values (ADR-028)

Per [ADR-028](adr_028_pg_upsert_binds_update_values.md), `QueryBuilder.BuildUpsert` on PostgreSQL now binds the `updateColumns` values as parameters instead of emitting `col = EXCLUDED.col`.

**Before:** `ON CONFLICT (...) DO UPDATE SET "col" = EXCLUDED."col"` — the on-conflict update silently reused the *insert* value and ignored the `updateColumns` values entirely (diverging from Oracle's MERGE).

**After:** `ON CONFLICT (...) DO UPDATE SET "col" = $N` — the caller's update value is bound (Oracle parity).

**Action:**
- If you assert on the exact generated SQL, update expectations (`EXCLUDED.col` → `$N`).
- If you relied on the old behavior of updating to the *inserted* value, pass the same value in both `insertColumns` and `updateColumns` (the result is then identical).
- Calls that passed differing update values (or update columns absent from the insert set) were silently wrong before and now apply the intended value — no action needed beyond verifying the corrected behavior is what you want.

## Outbox/Inbox Multi-Tenant Relay & Cleanup

The outbox relay, outbox-cleanup, and inbox-cleanup jobs run from the scheduler's tenant-less context, so in multi-tenant mode they could not resolve any tenant's database — outbox events accumulated per tenant and were **never delivered**, and inbox ledgers were never pruned. These jobs now **fan out across the configured static tenants** (`multitenant.tenants`), resolving each tenant's database independently, so multi-tenant outbox delivery and cleanup now work.

For **dynamic** tenant sources (`source.type: dynamic`), the tenant set is not enumerable at job-registration time, so the framework now **fails fast** rather than silently never relaying:

- **Outbox** + `multitenant.enabled` + `source.type: dynamic` → module **Init fails** (the relay is the whole point of the outbox).
- **Inbox** + `multitenant.enabled` + `source.type: dynamic` → **`RegisterJobs` fails** when the cleanup job would register (i.e. when the scheduler is present). `ProcessOnce` is unaffected and still works — run without the scheduler to use the inbox without retention cleanup.

**Action:** a dynamic-multi-tenant deployment with outbox/inbox cleanup enabled now fails at startup instead of silently losing events. Use static `multitenant.tenants` config, or (for inbox) drop the scheduler to keep `ProcessOnce` without cleanup.

## `database.tls.cert/key/ca` Now Wired Into the Drivers (ADR-027)

Per [ADR-027](adr_027_database_tls_material.md), the `database.tls.cert`, `database.tls.key`, and `database.tls.ca` fields — previously advertised but never consumed — are now honored.

**PostgreSQL:** the DSN now includes `sslrootcert` (ca), `sslcert` (cert), and `sslkey` (key) when set, so pgx authenticates the server and presents a client certificate for mTLS. A config that set `mode: require` + `ca:` was previously **encrypted but unauthenticated** (MITM-able); it now performs CA verification.

**Action (PostgreSQL):** if you set `database.tls.ca` (or `cert`/`key`), the connection now verifies the server certificate against that CA. **A wrong, missing, or mismatched CA — or a server cert that doesn't match — will now make the connection fail** where it previously succeeded unauthenticated. Confirm the CA path and server certificate before upgrading. Configs without `cert/key/ca` are unaffected.

**Oracle:** TLS via tcps/wallet is not implemented, so `database.tls.cert/key/ca` are now **rejected at startup validation** (rather than silently ignored). `database.tls.mode` alone still passes (no-op).

**Action (Oracle):** remove `database.tls.cert`, `database.tls.key`, and `database.tls.ca` from Oracle configs — they never did anything and now fail validation.

## Migration `Config.DryRun` Is Now Honored

`Config.DryRun` ("Only validate, do not execute") was documented and stamped into the `migration.applied` audit event, but **never consumed** — a `DryRun=true` `Migrate`/`MigrateAll` ran a real migration (mutating the schema across the whole tenant fleet) while the audit falsely recorded `dry_run=true`.

`DryRun=true` now downgrades a `migrate` to the Flyway `validate` verb, so no schema is mutated. Because `validate` is not an application, it emits no `migration.applied` audit event (per ADR-019) — so the `migration.dry_run` attribute is now always `false` on emitted events, accurately asserting "this was a real application."

**Action:** if any pipeline set `DryRun=true` and relied on it actually applying migrations (contrary to the field's documentation), it now only validates. Remove `DryRun` (or set it `false`) for runs that must apply schema changes.

## `APP_ENV` Now Selects the `config.<env>.yaml` Overlay

Previously the environment-specific YAML overlay suffix was read from koanf **before** the environment provider loaded, so the `APP_ENV` environment variable could not select it — the suffix always came from `config.yaml`/defaults (typically `development`). A 12-factor deployment that set `APP_ENV=production` and shipped `config.production.yaml` silently ran the development overlay (or none), even though `cfg.App.Env` still ended up `production`.

`APP_ENV` now drives overlay selection, matching the documented precedence (env vars > `config.<env>.yaml` > `config.yaml` > defaults).

**Action:** if you set `APP_ENV` and ship a `config.<env>.yaml` that was previously being ignored, that overlay is now loaded — review those files to confirm their values are correct for the environment. A malformed `APP_ENV` (not matching `^[a-z][a-z0-9-]{0,31}$`) is now rejected at startup with a clear `app.env` error instead of being interpolated into a config filename.

## Request-Path Zero-Overhead Changes (ADR-026)

A set of perf changes that make the default-config request path allocate less. Several carry consumer-visible surface:

### DB span/metric emission gated on `observability.enabled`

Per-operation database OpenTelemetry spans and metrics are now emitted only when observability is enabled (the global `otel.Tracer`/`otel.Meter` return non-nil no-ops, so without an explicit gate the framework built and discarded span/metric attributes on every query with observability off). The app bootstrap sets the gate automatically from `observability.enabled`. **Consumers that use the `database` package directly — without the framework's `app` bootstrap — must call `database.SetObservabilityEnabled(true)` to emit DB spans/metrics**; otherwise DB telemetry is silently suppressed even when a global OTel provider is registered.

### `logger.LogEvent` gained `Enabled() bool`

The interface now has `Enabled() bool` so callers can skip building expensive fields when an event would be dropped (the request action-log short-circuits at disabled levels). The framework's `LogEventAdapter` implements it (delegating to zerolog's nil-safe `Event.Enabled()`). **Any external type implementing `logger.LogEvent`** (custom adapters, test doubles) must add the method:

```go
func (e *YourEvent) Enabled() bool { return true } // or delegate to the underlying event
```

This mirrors prior interface-evolution entries (S8179/S8196) — the fix is to add the method, never to abandon the interface.

### `server.SetupMiddlewares` signature

`SetupMiddlewares` now takes an explicit `observabilityEnabled bool` immediately after `cfg`:

```go
// before
SetupMiddlewares(e, log, cfg, healthPath, readyPath)
// after
SetupMiddlewares(e, log, cfg, cfg.Bool("observability.enabled", false), healthPath, readyPath)
```

The OTel HTTP middleware is registered only when the flag is true (zero per-request span/metric overhead when observability is off). Apps using the framework's normal `app`/server bootstrap are unaffected; only direct callers of `SetupMiddlewares` need to pass the flag.

### `server.gzip.minlength` default

`server.gzip.minlength` now defaults to **1024** bytes (previously gzip compressed everything). Responses smaller than this are sent uncompressed, avoiding gzip header overhead on tiny JSON. Set `server.gzip.minlength: 0` to restore always-compress behavior.

### `X-Response-Time` header is now opt-in (`server.responsetime.enabled`)

The diagnostic `X-Response-Time` response header (per-request processing time) is **no longer emitted by default**. The `Timing` middleware that set it on every response is now gated behind `server.responsetime.enabled` (default **false**). Each per-response `Header().Set` allocates a `[]string` in `net/textproto.MIMEHeader.Set` — measured at ~2.4% of total allocations on the default read workload — and OTel provides richer latency telemetry. `X-Request-ID` and `traceparent` are unaffected. To restore the header, set:

```yaml
server:
  responsetime:
    enabled: true   # or SERVER_RESPONSETIME_ENABLED=true
```

When disabled, CORS also stops advertising `X-Response-Time` in `Access-Control-Expose-Headers`, keeping the CORS contract aligned with what is on the wire. Direct callers of `server.CORS(...)` get a new leading `exposeResponseTime bool` parameter (`CORS(cfg.Server.ResponseTime.Enabled, cfg.App.Env)`); apps using the normal server bootstrap are unaffected.

## Connection Pool Idle Default — Tracks Max (ADR-025)

Per [ADR-025](adr_025_pool_idle_tracks_max.md), the default for `database.pool.idle.connections` changed from a fixed **2** to **tracking `database.pool.max.connections`** (default 25). The old default kept only 2 idle connections against a max of 25, so under sustained load the pool continuously opened and closed physical connections (TCP+TLS+auth) — measured as a 91% p95 latency reduction (16.25 → 1.46 ms) and connection-establishment errors dropping from 8.15% to 0% once idle tracked max.

**No code or config change is required.** This is a default-only change with `apiImpact: none`. But be aware of three behavioral consequences if you relied on the old default:

| Area | Before | After |
|---|---|---|
| Idle connections held (idle unset) | up to 2 | up to `pool.max.connections` (default 25), still reaped after `pool.idle.time` (5m) |
| Multi-tenant idle ceiling | 2 × tenants | up to ~12.5× higher per tenant — verify your PostgreSQL `max_connections` / Oracle session budget |
| Pool idle metrics | OTEL idle gauges & `Stats()` `max_idle_connections`/`configured_idle_connections` reported `2` | now report the effective max — update dashboards/alerts keyed to `idle == 2` |

**To keep the old behavior**, set the value explicitly — an explicit `database.pool.idle.connections` always wins over the default:

```yaml
database:
  pool:
    idle:
      connections: 2   # opt back into the old conservative fixed idle cap of 2
```

The effective `pool_max_connections` / `pool_idle_connections` / `pool_max_lifetime` / `pool_idle_time` are now logged at `Info` on every successful connection, so you can confirm what the pool actually uses.

## Config Keys — Flat-Smushed Rename (ADR-024)

Per [ADR-024](adr_024_config_key_flatsmush.md), 21 snake_case config keys were renamed to the framework's underscore-free flat-smushed convention so they become settable via environment variables (the env loader maps `_`→`.`, koanf's nesting delimiter, so underscored leaf keys were silently unreachable from env). Update both your YAML and any environment variables. Go field names are unchanged.

| Old key (YAML) | New key (YAML) | Old env var (broken) | New env var |
|---|---|---|---|
| `cache.manager.max_size` | `cache.manager.maxsize` | `CACHE_MANAGER_MAX_SIZE` | `CACHE_MANAGER_MAXSIZE` |
| `cache.manager.idle_ttl` | `cache.manager.idlettl` | `CACHE_MANAGER_IDLE_TTL` | `CACHE_MANAGER_IDLETTL` |
| `cache.manager.cleanup_interval` | `cache.manager.cleanupinterval` | `CACHE_MANAGER_CLEANUP_INTERVAL` | `CACHE_MANAGER_CLEANUPINTERVAL` |
| `log.sensitive_fields` | `log.sensitivefields` | `LOG_SENSITIVE_FIELDS` | `LOG_SENSITIVEFIELDS` |
| `messaging.reconnect.reinit_delay` | `messaging.reconnect.reinitdelay` | `MESSAGING_RECONNECT_REINIT_DELAY` | `MESSAGING_RECONNECT_REINITDELAY` |
| `messaging.reconnect.resend_delay` | `messaging.reconnect.resenddelay` | `MESSAGING_RECONNECT_RESEND_DELAY` | `MESSAGING_RECONNECT_RESENDDELAY` |
| `messaging.reconnect.connection_timeout` | `messaging.reconnect.connectiontimeout` | `MESSAGING_RECONNECT_CONNECTION_TIMEOUT` | `MESSAGING_RECONNECT_CONNECTIONTIMEOUT` |
| `messaging.reconnect.max_delay` | `messaging.reconnect.maxdelay` | `MESSAGING_RECONNECT_MAX_DELAY` | `MESSAGING_RECONNECT_MAXDELAY` |
| `messaging.publisher.max_cached` | `messaging.publisher.maxcached` | `MESSAGING_PUBLISHER_MAX_CACHED` | `MESSAGING_PUBLISHER_MAXCACHED` |
| `messaging.publisher.idle_ttl` | `messaging.publisher.idlettl` | `MESSAGING_PUBLISHER_IDLE_TTL` | `MESSAGING_PUBLISHER_IDLETTL` |
| `outbox.table_name` | `outbox.tablename` | `OUTBOX_TABLE_NAME` | `OUTBOX_TABLENAME` |
| `outbox.auto_create_table` | `outbox.autocreatetable` | `OUTBOX_AUTO_CREATE_TABLE` | `OUTBOX_AUTOCREATETABLE` |
| `outbox.default_exchange` | `outbox.defaultexchange` | `OUTBOX_DEFAULT_EXCHANGE` | `OUTBOX_DEFAULTEXCHANGE` |
| `outbox.poll_interval` | `outbox.pollinterval` | `OUTBOX_POLL_INTERVAL` | `OUTBOX_POLLINTERVAL` |
| `outbox.batch_size` | `outbox.batchsize` | `OUTBOX_BATCH_SIZE` | `OUTBOX_BATCHSIZE` |
| `outbox.max_retries` | `outbox.maxretries` | `OUTBOX_MAX_RETRIES` | `OUTBOX_MAXRETRIES` |
| `outbox.retention_period` | `outbox.retentionperiod` | `OUTBOX_RETENTION_PERIOD` | `OUTBOX_RETENTIONPERIOD` |
| `inbox.table_name` | `inbox.tablename` | `INBOX_TABLE_NAME` | `INBOX_TABLENAME` |
| `inbox.auto_create_table` | `inbox.autocreatetable` | `INBOX_AUTO_CREATE_TABLE` | `INBOX_AUTOCREATETABLE` |
| `inbox.retention_period` | `inbox.retentionperiod` | `INBOX_RETENTION_PERIOD` | `INBOX_RETENTIONPERIOD` |
| `keystore.secret_min_length` | `keystore.secretminlength` | `KEYSTORE_SECRET_MIN_LENGTH` | `KEYSTORE_SECRETMINLENGTH` |

> The "old env var" column never worked (that is the bug ADR-024 fixes); it is shown only to help locate occurrences in existing deployment manifests.

## Observability Config Keys — Flat-Smushed Rename (#554)

ADR-024 audited only the `koanf`-tagged keys in `config/types.go`. The `observability` config tree (`observability/config.go`) is tagged with `mapstructure` and loaded via a separate `config.Config.Unmarshal("observability", …)` path that binds by koanf tag or the case-insensitive Go field name and **never honors the `mapstructure` tag**. Four compound-word keys there carried underscores and so bound from neither YAML (the underscored key matched no field name) **nor** env (the loader maps `_`→`.`). [Issue #554](https://github.com/gaborage/go-bricks/issues/554) flat-smushed them to the same convention. Go field names are unchanged.

| Old key (YAML, broken) | New key (YAML) | Old env var (broken) | New env var |
|---|---|---|---|
| `observability.metrics.histogram_aggregation` | `observability.metrics.histogramaggregation` | `OBSERVABILITY_METRICS_HISTOGRAM_AGGREGATION` | `OBSERVABILITY_METRICS_HISTOGRAMAGGREGATION` |
| `observability.logs.disable_stdout` | `observability.logs.disablestdout` | `OBSERVABILITY_LOGS_DISABLE_STDOUT` | `OBSERVABILITY_LOGS_DISABLESTDOUT` |
| `observability.logs.slow_request_threshold` | `observability.logs.slowrequestthreshold` | `OBSERVABILITY_LOGS_SLOW_REQUEST_THRESHOLD` | `OBSERVABILITY_LOGS_SLOWREQUESTTHRESHOLD` |
| `observability.logs.sampling_rate` | `observability.logs.samplingrate` | `OBSERVABILITY_LOGS_SAMPLING_RATE` | `OBSERVABILITY_LOGS_SAMPLINGRATE` |

> Unlike the ADR-024 keys (which still bound from YAML and broke only from env), these four never bound from YAML either — a service setting `observability.logs.sampling_rate` silently got the framework default. The recurrence guard now also walks `mapstructure` tags (`config.TestConfigKoanfTagsHaveNoUnderscore`) and a sibling `observability.TestObservabilityConfigTagsHaveNoUnderscore` covers the observability tree.

## Go Naming Conventions (S8179) — Getter Methods

Per [SonarCloud rule S8179](https://rules.sonarsource.com/go/RSPEC-8179/), getter methods should NOT have the `Get` prefix.

| Package | Old Method | New Method |
|---------|------------|------------|
| `config.Config` | `GetString()`, `GetInt()`, `GetInt64()`, `GetFloat64()`, `GetBool()` | `String()`, `Int()`, `Int64()`, `Float64()`, `Bool()` |
| `config.Config` | `GetRequiredString()`, `GetRequiredInt()`, `GetRequiredInt64()`, `GetRequiredFloat64()`, `GetRequiredBool()` | `RequiredString()`, `RequiredInt()`, `RequiredInt64()`, `RequiredFloat64()`, `RequiredBool()` |
| `app.ResourceProvider` | `GetDB()`, `GetMessaging()`, `GetCache()` | `DB()`, `Messaging()`, `Cache()` |
| `app.ModuleDeps` | `GetDB`, `GetMessaging`, `GetCache` (fields) | `DB`, `Messaging`, `Cache` (fields) |
| `app.Builder` | `GetError()` | `Error()` |
| `messaging.Manager` | `GetPublisher()` | `Publisher()` |
| `server.Validator` | `GetValidator()` | `Validator()` |
| `migration.FlywayMigrator` | `GetDefaultMigrationConfig()` | `DefaultMigrationConfig()` |
| `config.TenantStore` | `GetTenants()` | `Tenants()` |
| `app.MetadataRegistry` | `GetModules()`, `GetModule()` | `Modules()`, `Module()` |
| `app.App` | `GetMessagingDeclarations()` | `MessagingDeclarations()` |
| `database.Interface` | `GetMigrationTable()` | `MigrationTable()` |
| `database/testing.TestDB` | `GetQueryLog()`, `GetExecLog()` | `QueryLog()`, `ExecLog()` |
| `database/testing.TenantDBMap` | `GetTenantDB()` | `TenantDB()` |
| `server.RouteRegistry` | `GetRoutes()` | `Routes()` |

**Example:**
```go
// OLD
host := cfg.GetString("server.host", "0.0.0.0")
db, err := deps.GetDB(ctx)

// NEW
host := cfg.String("server.host", "0.0.0.0")
db, err := deps.DB(ctx)
```

## Interface Naming Conventions (S8196)

Per [SonarCloud rule S8196](https://rules.sonarsource.com/go/RSPEC-8196/) and [ADR-013](adr_013_interface_naming_conventions.md).

| Package | Old Interface | New Interface |
|---------|---------------|---------------|
| `scheduler` | `Job` | `Executor` |
| `app` | `HealthProbe` | `Prober` |
| `database` | `TenantStore` | `DBConfigProvider` |
| `messaging` | `TenantMessagingResourceSource` | `BrokerURLProvider` |
| `server` | `ResultLike` | `ResultMetaProvider` |
| `cache` | `TenantCacheResourceSource` | `ConfigProvider` |

## Standardized `ToSQL()` Across Query Builders (S8179)

Per [ADR-017](adr_017_insert_query_builder.md), `qb.Insert*` constructors return `types.InsertQueryBuilder` (a go-bricks-owned interface) instead of `squirrel.InsertBuilder` directly. The render method is renamed from `ToSql()` to `ToSQL()` — matching `Select`/`Update`/`Delete`.

| Constructor | Old return | New return | Render method |
|---|---|---|---|
| `qb.Insert(table)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |
| `qb.InsertWithColumns(table, cols...)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |
| `qb.InsertStruct(table, instance)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |
| `qb.InsertFields(table, instance, fields...)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |

**Example:**
```go
// OLD
sql, args, err := qb.Insert("users").Columns("name").Values("Alice").ToSql()

// NEW
sql, args, err := qb.Insert("users").Columns("name").Values("Alice").ToSQL()
```

The new interface preserves all common chaining methods (`Columns`, `Values`, `SetMap`, `Options`, `Prefix`, `Suffix`, `Select`). For specialized squirrel-only methods (e.g., `RunWith`, `PlaceholderFormat`), keep the rendered SQL via `ToSQL()` and execute with `db.Exec(ctx, sql, args...)`.

## Scheduler Default Timezone → UTC (ADR-023)

Previously the scheduler ran jobs in the host's local time (`time.Local`). It now
defaults to **UTC**. Deployments that relied on host-local job times must set
`scheduler.timezone: "-"` to preserve the old behavior, or set an explicit IANA
zone.

```yaml
scheduler:
  timezone: "-"   # preserve pre-upgrade host-local behavior
```
