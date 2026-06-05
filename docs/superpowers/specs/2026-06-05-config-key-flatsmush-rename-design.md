# Design: Rename Underscored Config Keys to the Flat-Smushed Convention

- **Date:** 2026-06-05
- **Status:** Approved (design)
- **Area:** `config` (+ docs, ADR-024)
- **Follows:** PR #548 (comma-separated `[]string` env splitting) — fixes the sibling bug #548 surfaced (`LOG_SENSITIVE_FIELDS`).

## Problem

`config.Load()` loads env vars through koanf with this key transform (config/config.go:47-55):

```go
k = strings.ReplaceAll(strings.ToLower(k), "_", ".")
```

koanf nests on `.`, so **every** underscore becomes a nesting boundary. Any koanf leaf tag containing `_` is therefore unreachable from the environment: the env var lands at an orphan path no struct field claims, the value is silently dropped, and the default (or zero value) wins. No error.

### Empirical confirmation

| Env var | Lands at koanf key | Field result |
|---|---|---|
| `LOG_SENSITIVE_FIELDS=pan,cvv2,otp` | `log.sensitive.fields` (orphan) | `cfg.Log.SensitiveFields == nil` |
| `KEYSTORE_SECRET_MIN_LENGTH=64` | `keystore.secret.min.length` (orphan) | `cfg.KeyStore.SecretMinLength == 32` (default) |

Exactly **21** leaf keys are affected (underscore is the sole trigger — no koanf tag uses digits, hyphens, or uppercase).

## Root cause: the keys drifted from the framework's own convention

A 6-agent audit of `config/types.go` established the framework standard, with three independent proposal lenses converging:

- **Compound scalar leaves are flat-smushed**, never underscored: `connectionstring` (16 chars), `minretrybackoff`/`maxretrybackoff` (15), `poolsize`, `virtualhost`, `slowjob`, `pathprefix`, `cidrallowlist`, `dialtimeout`, and `QueryLogConfig.MaxLength → koanf:"max"`.
- **Sub-structs exist only to group multiple related leaves** under a shared namespace: `server.timeout.{read,write,idle,middleware,shutdown}`, `database.pool.{max,idle,lifetime,keepalive}`, `database.query.{slow,log}`.
- **The single-field nested structs that exist** (`PoolMaxConfig{connections}`, `LifetimeConfig{max}`, `PostgreSQLConfig{schema}`, `OracleConfig{service}`, `LimitsConfig{tenants}`) are namespace anchors within a sibling family or vendor/extension boundaries — **never lone wrappers of an atomic compound concept**. Even the audit lens that was *permitted* to use single-field structs chose zero.

Smushed leaves are env-reachable (no underscore → the `_`→`.` transform maps cleanly); snake_case leaves are not. The 21 keys are simply the ones that drifted into snake_case, and that drift *is* the bug.

## Decision: clean-break rename to flat-smushed keys

Rename all 21 snake_case koanf leaf tags to the underscore-free flat-smushed form. **No mapping layer, no new structs, no env-loading code change** — the existing transform works natively once keys are underscore-free. Only struct *tags* change; **Go field names stay the same**, so consumer code (`cfg.Outbox.BatchSize`, `cfg.KeyStore.SecretMinLength`) is untouched.

`messaging.reconnect` stays flat (grouping would collide with its pre-existing flat `reconnect.delay` sibling and contradicts the `RedisConfig` flat-timeout precedent). The one arguably-groupable pair (`table_name`+`auto_create_table`) is also smushed, for maximum uniformity with `RedisConfig`.

### The full mapping (all flat-smush)

| old koanf key | new koanf key | old koanf key | new koanf key |
|---|---|---|---|
| `cache.manager.max_size` | `cache.manager.maxsize` | `outbox.table_name` | `outbox.tablename` |
| `cache.manager.idle_ttl` | `cache.manager.idlettl` | `outbox.auto_create_table` | `outbox.autocreatetable` |
| `cache.manager.cleanup_interval` | `cache.manager.cleanupinterval` | `outbox.default_exchange` | `outbox.defaultexchange` |
| `log.sensitive_fields` | `log.sensitivefields` | `outbox.poll_interval` | `outbox.pollinterval` |
| `messaging.reconnect.reinit_delay` | `messaging.reconnect.reinitdelay` | `outbox.batch_size` | `outbox.batchsize` |
| `messaging.reconnect.resend_delay` | `messaging.reconnect.resenddelay` | `outbox.max_retries` | `outbox.maxretries` |
| `messaging.reconnect.connection_timeout` | `messaging.reconnect.connectiontimeout` | `outbox.retention_period` | `outbox.retentionperiod` |
| `messaging.reconnect.max_delay` | `messaging.reconnect.maxdelay` | `inbox.table_name` | `inbox.tablename` |
| `messaging.publisher.max_cached` | `messaging.publisher.maxcached` | `inbox.auto_create_table` | `inbox.autocreatetable` |
| `messaging.publisher.idle_ttl` | `messaging.publisher.idlettl` | `inbox.retention_period` | `inbox.retentionperiod` |
| `keystore.secret_min_length` | `keystore.secretminlength` | | |

### Why not the alternatives

- **Schema-aware alias map** (flatten→canonical reflection): permanently papers over a key set that violates the standard; rejected — fix the keys, not the transform.
- **Nested single-field wrapper structs** (`Cleanup{Interval}`, `Secret{MinLength}`): no framework precedent; the audit flagged this as drift.
- **`__` escape convention**: the natural env name still silently fails.

## Breaking change

Renaming koanf keys breaks existing YAML/env using the snake_case form — old keys become unknown and fall back to defaults (silently). Acceptable pre-1.0 and consistent with framework practice (ADR-016, S8179, S8196). Documented in **ADR-024** + a `wiki/migrations.md` before→after table. A **no-underscore invariant test** prevents the bug class from recurring.

## Components / files

**Core (behavioral):**
- `config/types.go` — rename `koanf`/`json`/`yaml`/`toml`/`mapstructure` tags on the 21 fields.
- `config/config.go` — `loadDefaults`: `keystore.secret_min_length` → `keystore.secretminlength` (the only underscored default key).
- `config.example.yaml` — the outbox/inbox/log keys.

**Consistency (strings that name the key, updated for accuracy):**
- `config/validation.go` — `NewValidationError("messaging.reconnect.reinit_delay", …)` and the other dotted key paths (8 sites).
- `outbox/config.go`, `inbox/config.go`, `cache/manager.go` — error-message strings (`poll_interval`, `batch_size`, `max_size`, …).
- `keystore/keystore.go` — doc comment.

**Docs:** `README.md`, `llms.txt`, `CLAUDE.md`, `wiki/{outbox,keystore,observability,cache,messaging,…}.md` — update documented keys.

**Governance:** `wiki/adr_024_*.md` (new) + `wiki/architecture_decisions.md` index + `wiki/migrations.md` entry.

**Ride-along doc fixes** (independently misleading, found in the earlier audit):
- `README.md:329-339` — wrong server-path env vars (`SERVER_BASE_PATH`→`SERVER_PATH_BASE`, `SERVER_HEALTH_ROUTE`→`SERVER_PATH_HEALTH`, `SERVER_READY_ROUTE`→`SERVER_PATH_READY`) + drifted YAML block shape; `:226` — soften the "auto-maps to dot notation" claim.
- `.env.example` — `DATABASE_TYPE=postgres`→`postgresql`; verify-then-fix the 3 orphan `MULTITENANT_*` entries.

**Explicit EXCLUSIONS** (false-positive tokens — do NOT rename):
- `messaging/manager.go` `idle_ttl_seconds` (a metric/log field name).
- `database/*` `table_name` etc. (SQL column names), and any other non-config use.
- Implementation classifies each reference; `make check` + targeted tests catch mistakes.

## Testing

- **New env-reachability tests** (`t.Setenv`), one per affected type: `[]string` (`LOG_SENSITIVEFIELDS`), int (`OUTBOX_BATCHSIZE`), bool (`OUTBOX_AUTOCREATETABLE`), duration (`MESSAGING_RECONNECT_CONNECTIONTIMEOUT`), and `KEYSTORE_SECRETMINLENGTH` — assert the field populates.
- **No-underscore invariant test** — reflect over `Config` (including nested structs and map/slice element struct types) and assert no `koanf` tag contains `_`. Permanent guard against the whole bug class.
- **Update** existing tests using the old keys (YAML/env values and error-message assertions).
- **Regression:** all existing `config` (and `outbox`/`inbox`/`cache`) tests green; full `make check`.

## Scope

- **In:** the rename (Class 1) + ride-along doc fixes (Class 3) + ADR-024.
- **Out (follow-up issue, needs user go-ahead to file):** InjectInto↔Unmarshal decode parity (Class 2 — duration-from-int = 30ns, integer base, empty-string coercion, whitespace trimming).

## Known limitations

- Operators must migrate snake_case YAML/env to the smushed keys (migration table provided); old keys silently fall back to defaults (clean break).
- The env var for a smushed key is itself smushed (`KEYSTORE_SECRETMINLENGTH`, not `…_SECRET_MIN_LENGTH`) — inherent to underscore-free keys; documented.
