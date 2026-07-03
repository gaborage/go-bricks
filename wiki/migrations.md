# Breaking Change Migrations — per-hop upgrade runbook

Executable upgrade path for existing GoBricks apps, structured as one **hop per release** so an agent can walk from any version to any target. Optimized for LLM coding agents: every change is a gated **atom** with a `detect` command, an `apply` step (inline before/after for compiler-caught API changes; a one-line decision for silent ones), and a `verify` command. The deep "why" for each change lives in its ADR (`wiki/adr_*.md`) and `CHANGELOG.md` — this file is the *what to do*. Greenfield work can ignore it; the new APIs are documented in CLAUDE.md.

> Two rename lookup tables (ADR-024, #554) and pre-v0.39 changes are retained at the end as reference.

## How to use this runbook (agent protocol)

**1 — Detect CURRENT version** from the consuming app's `go.mod`:
```sh
go list -m -f '{{.Version}}{{if .Replace}} =>{{.Replace.Path}}{{end}}' github.com/gaborage/go-bricks
```
A plain `vX.Y.Z` is your current node. `=>` a local path (dev `replace`) means the require line is not the running code — resolve the real version from that checkout: `git -C <path> describe --tags --abbrev=0`.

**2 — Pick TARGET** = the version you're moving to (usually the newest node).

**3 — Select the hop chain** on the Ladder: every edge strictly to the right of CURRENT, up to and including TARGET. Never apply an edge at/left of CURRENT.

```
v0.39.1 ─E40─ v0.40.0 ─E401─ v0.40.1 ─E41─ v0.41.0 ─E42─ v0.42.0 ─E43─ v0.43.0 ─E44─ v0.44.0 ─E45─ v0.45.0
```

| edge | hop | worst risk | atoms | compiler-caught | preflight (run BEFORE the bump) |
|------|-----|-----------|-------|-----------------|---------------------------------|
| E40  | v0.39.1 → v0.40.0 | additive (safe) | 6 | none | none |
| E401 | v0.40.0 → v0.40.1 | silent-config | 2 | none | none |
| E41  | v0.40.1 → v0.41.0 | compile-break | 7 | C41.2 C41.3 C41.4 | DB connection budget |
| E42  | v0.41.0 → v0.42.0 | silent (fail-closed) | 9 | none | TLS CA / dynamic-multitenant |
| E43  | v0.42.0 → v0.43.0 | compile-break | 6 | C43.2 C43.3 | bare section-named env vars |
| E44  | v0.43.0 → v0.44.0 | noop | 2 | none | none |
| E45  | v0.44.0 → v0.45.0 | compile-break | 9 | C45.1 C45.2 C45.3 C45.4 C45.5 C45.6 | outbox re-delivery count |

**4 — Read each atom's gate before acting.** Every atom carries `when: match | no-match | always`:
- **`when: match`** → act only if `detect` returns ≥1 line (an API/arity/interface change, or a config key you set).
- **`when: no-match`** → act only if `detect` returns 0 lines. These are **default flips**: you are affected *precisely because the key is unset*, so the new default now governs you. A naive agent that greps, finds nothing, and concludes "not affected" is **wrong** for these — the miss is the actionable case.
- Per class: **compile-break** atoms are build-driven — you may defer reading them and let `go build ./...` at the hop's `exit` enumerate them, then fix to green. **silent-behavior / silent-config** have no compiler safety net — you MUST run their `detect`. **additive-optional / no-consumer-action** — skip unless adopting the feature.

**5 — Execute, one of two modes:**
- **WALK (default, safest):** for each edge left→right — run each atom's gate and apply/verify the actionable ones, then run the edge's `exit` line (build + test, then `go get @<node> && go mod tidy`). Don't advance until green. `go.mod` sits at a real released tag after every hop, so a failure bisects to one edge.
- **DIRECT-JUMP (token-efficient over a wide range):** run every selected hop's `preflight` FIRST (these guard data hazards that are unrecoverable after the bump), then a single `go get @<TARGET> && go mod tidy`, let one `go build ./...` batch all compiler-caught edits, then run every silent atom's gate once. When two atoms touch the same config key/symbol across hops, apply only the later one. Fall back to WALK if a build break is hard to localize.

## E40 · v0.39.1 → v0.40.0 — database ergonomics + inbox/outbox helpers + config list-split

- gist: Adds vendor-aware DB error classifiers, `WithTx` helpers, a consumer-side inbox (`ProcessOnce`), and exported outbox event-id header; two silent runtime shifts — comma env vars now split into `[]string`, and schema-qualified outbox tables derive index names from the last segment.
- build-caught: none
- preflight: none
- exit: `go get github.com/gaborage/go-bricks@v0.40.0 && go mod tidy && go build ./... && go test ./...`

### [C40.1] Vendor-aware unique/FK/not-found error classifiers · additive-optional
- note: New `database.IsUniqueViolation(err)`, `IsForeignKeyViolation(err)`, `IsNotFound(err)`, `ConstraintName(err)` classify pgx (SQLSTATE 23505/23503) and go-ora (ORA-00001/02291) errors via `errors.As` over the wrap chain — adopt to replace hand-rolled driver error-string matching (wrap driver errors with `%w`, not `%v`, for the chain to traverse). Purely additive; existing error handling is untouched.
- ref: CHANGELOG 0.40.0 · #542 · database/errors.go

### [C40.2] database.WithTx / WithTxOptions transaction helpers · additive-optional
- note: `database.WithTx(ctx, db, func(ctx, tx) error {...})` commits on nil, rolls back on error, and rolls back + re-panics on panic (committed-flag guard suppresses post-commit rollback noise); `WithTxOptions(ctx, db, *sql.TxOptions, fn)` adds isolation/read-only. Optional cleanup for the demo's manual `db.Begin`/`Commit`/`Rollback` blocks in `internal/modules/products/service/service.go`; manual tx code still compiles and runs unchanged.
- ref: CHANGELOG 0.40.0 · #543 · database/transaction.go

### [C40.3] inbox ProcessOnce durable idempotency ledger · additive-optional
- note: New `inbox` module — consumer-side complement to the outbox. Register `inbox.NewModule()` to get `deps.Inbox.ProcessOnce(ctx, eventID, func(ctx, tx) error {...})`, which records the id and runs the handler exactly once per id in one transaction (redelivery of a known id short-circuits, returns nil); the ledger table auto-creates on first use only when `inbox.autocreatetable: true` (opt-in; default false — otherwise you must provision the table yourself). No existing consumer breaks; adopt only for exactly-once handling.
- ref: CHANGELOG 0.40.0 · #545 · inbox/inbox.go

### [C40.4] outbox exports x-outbox-event-id header + EventIDFromHeaders · additive-optional
- note: `outbox.HeaderEventID` (`"x-outbox-event-id"`) and `outbox.EventIDFromHeaders(amqp.Table) (string, bool)` are now exported so consumers can pull the event id off AMQP delivery headers (normalizes string vs `[]byte`) to feed `inbox.ProcessOnce` or custom dedup. Optional correlation helper; nothing to change if you don't dedup consumer-side.
- ref: CHANGELOG 0.40.0 · #544 · outbox/headers.go

### [C40.5] Comma-separated env vars now split into []string fields · silent-behavior · when: match
- detect: `grep -rnE '^[A-Z][A-Z0-9_]*=[^=]*,' .env .env.example deploy k8s 2>/dev/null`
- gate: match = you set an env var whose value contains a comma AND it binds to a `[]string` config field (e.g. `SCHEDULER_SECURITY_CIDRALLOWLIST`, `SCHEDULER_SECURITY_TRUSTEDPROXIES`) — it now decodes to multiple trimmed slice elements instead of one literal string; also, a non-empty `scheduler.security.cidrallowlist`/`trustedproxies` that resolves to zero valid CIDRs now FAILS startup instead of silently degrading to localhost-only. (This demo sets no such env vars → not affected.)
- apply: Confirm each comma-containing env var was intended as a list; if a literal comma was meant for a scalar field, restructure the value so it no longer binds to a `[]string`.
- verify: `go run ./cmd/api 2>&1 | head -40`  # app starts; parsed slice field has the intended element count (no zero-CIDR startup failure)
- ref: CHANGELOG 0.40.0 · #548 · config/converters.go

### [C40.6] outbox index names derived from table's last segment · silent-behavior · when: match
- detect: `grep -rniE 'outbox:' -A6 config*.yaml | grep -iE 'tablename\s*:\s*\S+\.\S+'`
- gate: match = your `outbox.tablename` is schema-qualified (e.g. `myschema.outbox`); generated index names now use only the last segment (`outbox`) so they are valid, un-dotted identifiers. (This demo uses `tablename: gobricks_outbox` — no dot → not affected.)
- apply: None required; if migrating from an older run that created dotted/invalid index names, drop any stale duplicate index left by the old naming.
- verify: `grep -iE 'tablename' config.development.yaml` then run with `outbox.autocreatetable: true`  # indexes create with no SQL syntax error
- ref: CHANGELOG 0.40.0 · #547 · outbox/store_postgres.go

## E401 · v0.40.0 → v0.40.1 — config keys go flat-smushed

- gist: 21 underscored config leaf keys were renamed to the framework's underscore-free convention so they finally bind from env vars; the old underscored keys silently fall back to defaults after the bump.
- build-caught: none
- preflight: none
- exit: `go get github.com/gaborage/go-bricks@v0.40.1 && go mod tidy && go build ./... && go test ./...`

### [C401.1] ADR-024: 21 snake_case config keys renamed to flat-smushed · silent-config · when: match
- detect: `git grep -nE '(^[[:space:]]*|\.)(max_size|idle_ttl|cleanup_interval|sensitive_fields|reinit_delay|resend_delay|connection_timeout|max_delay|max_cached|table_name|auto_create_table|default_exchange|poll_interval|batch_size|max_retries|retention_period|secret_min_length)[[:space:]]*:' -- '*.yaml' '*.yml'` (leaf-anchored so it matches BOTH nested and flat-dotted YAML — a `outbox\.table_name` dotted grep silently misses the nested `outbox:`→`table_name:` form the framework actually uses; also grep the UPPER_SNAKE env forms — `OUTBOX_TABLE_NAME`, `KEYSTORE_SECRET_MIN_LENGTH`, … — in deploy manifests)
- gate: match = you set one of the 21 underscored keys, so on upgrade it stops binding and silently falls back to its default with NO error (e.g. `outbox.auto_create_table` → `false`, table never created; `outbox.default_exchange` → `""`, events never route; note keys with framework defaults like `outbox.batch_size` fall back to `100`, not zero). no-match = you use none of them (or already flat-smushed), unaffected.
- apply: rename each YAML leaf AND env var to the underscore-free form per the 21-row table below (§ Config Keys — Flat-Smushed Rename (ADR-024)) — `outbox.table_name`→`outbox.tablename`, `OUTBOX_TABLE_NAME`→`OUTBOX_TABLENAME`, `keystore.secret_min_length`→`keystore.secretminlength`, etc. Go struct field names are unchanged.
- verify: `git grep -nE '(^[[:space:]]*|\.)(max_size|idle_ttl|cleanup_interval|sensitive_fields|reinit_delay|resend_delay|connection_timeout|max_delay|max_cached|table_name|auto_create_table|default_exchange|poll_interval|batch_size|max_retries|retention_period|secret_min_length)[[:space:]]*:' -- '*.yaml' '*.yml'`  # expect zero matches; then start the app and confirm the setting takes effect (e.g. set `OUTBOX_BATCHSIZE=7`, observe batch size 7 in relay logs)
- ref: ADR-024 · #549 · wiki/adr_024_config_key_flatsmush.md

### [C401.2] Docs: corrected server-path env var names and .env.example orphans · no-consumer-action
- note: Docs-only correction of the documented `server.path.*` env-var names and stale `.env.example` entries; no runtime or API change. Optionally cross-check that any `SERVER_PATH_*` vars in your `.env`/manifests match the corrected names (`grep -rniE 'server\.path|SERVER_PATH' .env* config*.yaml`).
- ref: CHANGELOG 0.40.1 · #551

## E41 · v0.40.1 → v0.41.0 — perf iteration 2: zero-overhead request path (ADR-026) + pool idle tracks max (ADR-025)

- gist: request-path allocations trimmed — gzip skips tiny bodies, `X-Response-Time` goes opt-in (CORS arity change), DB spans/`SetupMiddlewares`/OTel HTTP middleware gate on `observability.enabled`, `LogEvent` gains `Enabled()`, pool idle default now tracks max, and four observability keys finally bind from YAML.
- build-caught: C41.2 C41.3 C41.4
- preflight: Before bumping, verify server-side connection budget — the idle default jumps 2→pool.max (25): `psql -h <host> -U <user> -c 'SHOW max_connections;'` and confirm max_connections ≥ pool.max.connections × active-tenant count (Oracle: check session budget).
- exit: `go get github.com/gaborage/go-bricks@v0.41.0 && go mod tidy && go build ./... && go test ./...`

### [C41.1] server.gzip.minlength now defaults to 1024 bytes · silent-behavior · when: no-match
- detect: `grep -rniE 'gzip\.minlength|SERVER_GZIP_MINLENGTH' config*.yaml etc/ deploy/`
- gate: no-match = the new default now governs you, because the key is unset — responses smaller than 1024 bytes are now sent uncompressed (previously gzip compressed everything).
- apply: Leave unset to keep the new (faster, less header overhead) behavior OR set `server.gzip.minlength: 0` to restore always-compress.
- verify: `curl -s -H 'Accept-Encoding: gzip' http://localhost:8080/health -D - | grep -i content-encoding`  # small bodies show NO gzip by default
- ref: ADR-026 · #559

### [C41.2] X-Response-Time header now opt-in; server.CORS() gains leading exposeResponseTime bool · compile-break · when: match
- detect: `git grep -nE 'X-Response-Time|responsetime\.enabled|SERVER_RESPONSETIME_ENABLED|server\.CORS\('`
- scope: The header is now OFF by default for ALL apps (silent): the `Timing` middleware is gated behind `server.responsetime.enabled` (default false), and CORS stops advertising it in `Access-Control-Expose-Headers`. Only DIRECT callers of `server.CORS(...)` hit the compile break — the standard `app.New()` bootstrap does not call it directly, but still loses the header. If a client/test reads `X-Response-Time`, set `server.responsetime.enabled: true` (or `SERVER_RESPONSETIME_ENABLED=true`). `X-Request-ID` / `traceparent` are unaffected.
- before:
  ```go
  func CORS(envOverride ...string) echo.MiddlewareFunc
  // call site:
  e.Use(server.CORS(cfg.App.Env))
  ```
- after:
  ```go
  func CORS(exposeResponseTime bool, envOverride ...string) echo.MiddlewareFunc
  // call site:
  e.Use(server.CORS(cfg.Server.ResponseTime.Enabled, cfg.App.Env))
  ```
- verify: `go build ./...`
- ref: ADR-026 · #563

### [C41.3] logger.LogEvent interface gained Enabled() bool · compile-break · when: match
- detect: `git grep -nE 'logger\.LogEvent\b'`
- scope: Any external type implementing `logger.LogEvent` (custom adapters, test doubles). The framework's own `LogEventAdapter` already implements it (delegating to zerolog's nil-safe `Event.Enabled()`); apps that only consume `deps.Logger` are unaffected.
- before:
  ```go
  type stubEvent struct{ /* ... */ }
  func (e *stubEvent) Msg(string) {}
  // no Enabled() method
  ```
- after:
  ```go
  type stubEvent struct{ /* ... */ }
  func (e *stubEvent) Msg(string) {}
  func (e *stubEvent) Enabled() bool { return true } // or delegate to the underlying event
  ```
- verify: `go build ./...`
- ref: ADR-026 · #559

### [C41.4] server.SetupMiddlewares gained explicit observabilityEnabled bool param · compile-break · when: match
- detect: `git grep -nE 'SetupMiddlewares\('`
- scope: Only direct callers of `server.SetupMiddlewares`. Apps using the normal `app`/server bootstrap are unaffected. The OTel HTTP middleware is now registered only when the flag is true (zero per-request span/metric overhead when observability is off).
- before:
  ```go
  server.SetupMiddlewares(e, log, cfg, healthPath, readyPath)
  ```
- after:
  ```go
  server.SetupMiddlewares(e, log, cfg, cfg.Bool("observability.enabled", false), healthPath, readyPath)
  ```
- verify: `go build ./...`
- ref: ADR-026 · #559

### [C41.5] DB spans/metrics gated on observability.enabled · silent-behavior · when: match
- detect: `git grep -nE 'database\.NewQueryBuilder|database\.Open|SetObservabilityEnabled'`
- gate: match = you use the `database` package. If you reach it via the `app.New()` bootstrap (this demo does), the gate is set AUTOMATICALLY from `observability.enabled` — no action. Only DIRECT-use apps (no framework bootstrap) now silently suppress DB spans/metrics until they opt in.
- apply: For framework-bootstrapped apps do nothing OR, in a direct-use app that wants DB telemetry, call `database.SetObservabilityEnabled(true)` at startup.
- verify: `curl -s http://localhost:8889/metrics | grep -i 'db\|database'`  # with observability on, DB metrics present; absent in a direct-use app without the opt-in call
- ref: ADR-026 · #559

### [C41.6] Pool idle-connections default now tracks pool.max.connections (was fixed 2) · silent-behavior · when: no-match
- detect: `grep -rniE 'pool\.idle\.connections|POOL_IDLE' config*.yaml etc/`
- gate: no-match = the new default now governs you, because `database.pool.idle.connections` is unset — the pool now holds up to `pool.max.connections` (default 25) idle instead of 2 (still reaped after `pool.idle.time`, 5m). Fixed a 91% p95 latency regression from constant connection churn, but raises the steady-state server-side connection count ~12.5×.
- apply: Do nothing to keep the new behavior (run the preflight budget check first) OR set `database.pool.idle.connections: 2` to restore the old cap; update any dashboard/alert keyed to idle==2.
- verify: `make run 2>&1 | grep -iE 'pool_idle_connections|pool_max_connections'`  # Info startup log now reports the effective pool sizes
- ref: ADR-025 · #558

### [C41.7] Observability config keys flat-smushed (#554) — never bound from YAML before · silent-config · when: match
- detect: `git grep -nE 'observability\.metrics\.histogram_aggregation|observability\.logs\.(disable_stdout|slow_request_threshold|sampling_rate)' -- '*.yaml' '*.yml'`
- gate: match = you set one of the underscored keys. Unlike the ADR-024 rename, these keys NEVER bound from YAML, so your prior setting was silently the default all along — re-verify you actually want the value once it starts taking effect.
- apply: Rename in YAML and env — `histogram_aggregation`→`histogramaggregation`, `disable_stdout`→`disablestdout`, `slow_request_threshold`→`slowrequestthreshold`, `sampling_rate`→`samplingrate` (env: `OBSERVABILITY_LOGS_SAMPLING_RATE`→`OBSERVABILITY_LOGS_SAMPLINGRATE`, etc.).
- verify: set `observability.logs.samplingrate` and confirm log sampling actually changes  # old underscored key had no effect
- ref: #554/#556

## E42 · v0.41.0 → v0.42.0 — config-wiring correctness & fail-closed DB/TLS defaults

- gist: Previously-advertised-but-inert config (`database.tls.*`, `APP_ENV` overlay, migration `DryRun`) now actually takes effect; PG upsert binds real update values; multi-tenant outbox/inbox delivers per tenant (dynamic sources fail fast); shutdown drains inbound work first.
- build-caught: none
- preflight: `grep -rniE 'database\.tls\.(cert|key|ca)|source\.type\s*:\s*dynamic|multitenant\.enabled\s*:\s*true' config*.yaml deploy/` — any hit is a fail-closed hazard (PG CA verification now enforced, Oracle TLS material rejected at startup, dynamic-multitenant outbox Init fails); resolve BEFORE bumping.
- exit: `go get github.com/gaborage/go-bricks@v0.42.0 && go mod tidy && go build ./... && go test ./...`

### [C42.1] `database.tls.cert/key/ca` now wired into drivers (PG verifies CA; Oracle rejects at startup) · silent-config · when: match
- detect: `grep -rniE 'database\.tls\.(cert|key|ca)|DATABASE_TLS_(CERT|KEY|CA)' config*.yaml deploy/`
- gate: match = you set one of these keys, so the driver now consumes it — PostgreSQL adds `sslrootcert`/`sslcert`/`sslkey` to the DSN and authenticates the server (a former `mode: require` + `ca:` connection was encrypted-but-unauthenticated and is now CA-verified); Oracle never implemented tcps/wallet so these keys are now rejected at startup validation.
- apply: PostgreSQL — confirm the CA path and server certificate chain match, or the connection now fails where it previously succeeded unauthenticated. OR Oracle — remove `database.tls.cert`, `database.tls.key`, `database.tls.ca` (they were always no-ops and now fail validation; `database.tls.mode` alone still passes).
- verify: `go build ./...` then start the app against the DB  # PG connects only with a valid CA/cert chain; Oracle with cert/key/ca fails validation with a clear error
- ref: ADR-027 · #582

### [C42.2] PostgreSQL `BuildUpsert` now binds update values (Oracle MERGE parity) · silent-behavior · when: match
- detect: `git grep -nE 'BuildUpsert|EXCLUDED\.' -- '*.go'`
- gate: match = you call `QueryBuilder.BuildUpsert` on PostgreSQL or assert on its SQL — the on-conflict clause changed from `SET "col" = EXCLUDED."col"` (which silently reused the *insert* value and ignored `updateColumns`) to `SET "col" = $N` (the caller's update value is bound, matching Oracle).
- apply: update SQL-string assertions (`EXCLUDED."col"` → `$N`) OR, if you relied on updating to the inserted value, pass the same value in both `insertColumns` and `updateColumns` (result is then identical); calls that passed differing update values were silently wrong before and now apply the intended value — verify intent.
- verify: `go test ./...`  # SQL-assertion tests fail until updated; inspect the generated `DO UPDATE SET "col" = $N`
- ref: ADR-028 · #583

### [C42.3] Graceful shutdown stops inbound work (server, consumers) before module teardown · no-consumer-action
- note: `App.Shutdown` reordered to `server → consumers → modules → observability → manager cleanup → closers`, so in-flight handlers no longer run against already-torn-down modules; no code/config change. `messaging.Manager` gains an additive `StopConsumers()` (quiesce without closing). Only adjust if a module's `Shutdown()` implicitly relied on the HTTP server still serving or consumers still delivering — that was the buggy case the reorder fixes.
- ref: ADR-029 · #585

### [C42.4] `APP_ENV` now selects the `config.<env>.yaml` overlay · silent-behavior · when: match
- detect: `ls config.*.yaml 2>/dev/null; grep -rniE 'APP_ENV' deploy/`
- gate: match = you set `APP_ENV` and ship a `config.<env>.yaml` — the overlay suffix was previously read from koanf before the env provider loaded, so the file was ignored (the suffix always came from `config.yaml`/defaults, usually `development`); `APP_ENV` now drives selection, so a formerly-ignored overlay is now loaded.
- apply: review the now-active `config.<env>.yaml` for correctness in that environment; a malformed `APP_ENV` (not `^[a-z][a-z0-9-]{0,31}$`) is now rejected at startup with an `app.env` error instead of being interpolated into a filename.
- verify: `APP_ENV=production` with a `config.production.yaml` present, start the app  # its overlay values are applied
- ref: #578

### [C42.5] Migration `Config.DryRun` is now honored (downgrades migrate to Flyway validate) · silent-behavior · when: match
- detect: `git grep -nE 'DryRun' -- '*.go'`
- gate: match = a migration pipeline sets `DryRun=true` — it was documented and stamped into the `migration.applied` audit event but never consumed, so it actually ran a real schema-mutating migrate; it now downgrades to the Flyway `validate` verb (no schema change) and emits no `migration.applied` event.
- apply: if a pipeline set `DryRun=true` but relied on it actually applying schema (contrary to the field's docs), remove `DryRun` or set it `false` for runs that must apply changes.
- verify: run a `DryRun=true` migration  # schema is unchanged and Flyway `validate` ran, no `migration.applied` event
- ref: #580

### [C42.6] Outbox/Inbox multi-tenant relay & cleanup fan-out; fail-fast on dynamic tenant source · silent-behavior · when: match
- detect: `grep -rniE 'multitenant\.enabled\s*:\s*true|source\.type\s*:\s*dynamic|multitenant\.tenants' config*.yaml`
- gate: match = you run multi-tenant with outbox/inbox — the relay/cleanup jobs ran from the scheduler's tenant-less context and could not resolve any tenant DB, so events accumulated and were never delivered and inbox ledgers were never pruned; jobs now fan out across static `multitenant.tenants`. For `source.type: dynamic`: outbox module Init fails, and inbox `RegisterJobs` fails when the scheduler is present.
- apply: use static `multitenant.tenants` to get per-tenant delivery/cleanup OR, for a dynamic inbox, drop the scheduler to keep `ProcessOnce` without retention cleanup; a dynamic-multitenant app with outbox enabled now fails at startup instead of silently losing events.
- verify: start a static-multitenant app  # outbox events deliver per tenant; a dynamic-multitenant app with outbox enabled fails fast at startup
- ref: #581

### [C42.7] Debug/system endpoint IP allowlist now requires a trusted proxy (blocks XFF spoofing) · silent-config · when: match
- detect: `grep -rniE 'debug\.(enabled|trustedproxies|ipwhitelist|allowlist)' config*.yaml`
- gate: match = you gate debug/`_sys` endpoints by IP behind a proxy — the denial path now derives the client IP via the trusted-proxy-aware `server.ClientIP(...)` instead of echo's spoofable `c.RealIP()`, so an unconfigured proxy chain no longer honors a spoofable `X-Forwarded-For`.
- apply: set `debug.trustedproxies` to your proxy CIDR(s) so the real client IP is derived from a trusted hop; an invalid CIDR entry now logs a startup WARN whenever debug endpoints are enabled.
- verify: hit a debug endpoint through your proxy  # allow/deny uses the trusted-proxy-derived client IP, not raw XFF
- ref: #576 · CHANGELOG 0.42.0

### [C42.8] httpclient redacts credentials/secrets from logged request URLs · silent-behavior · when: match
- detect: `git grep -nE 'httpclient\.New|WithJOSE' -- '*.go'`
- gate: match = you make outbound requests whose logs are scraped by a parser keyed on the full URL — logged request URLs now have userinfo credentials and secret query params redacted.
- apply: none for behavior; adjust any log parser that expected the raw URL with credentials/secrets intact.
- verify: make an outbound request with credentials in the URL  # logs show the redacted form
- ref: #575 · CHANGELOG 0.42.0

### [C42.9] messaging publish-confirmation timeout applied; lazy consumers detached from request ctx; contiguous subquery placeholders · silent-behavior · when: match
- detect: `grep -rniE 'messaging\.reconnect\.connectiontimeout' config*.yaml; git grep -niE 'Subquery' -- '*.go'`
- gate: match = you set `messaging.reconnect.connectiontimeout`, start consumers lazily, or build subquery filters — three bug fixes: the configured `reconnect.connectiontimeout` now actually governs the AMQP client's per-publish broker ACK/NACK confirmation wait (the timeout waiting for a publish-confirm; it is NOT the connection-establishment timeout) (#571), lazily-started consumers no longer inherit/cancel with the triggering request context (#577), and subquery filter placeholders are now numbered contiguously (#579).
- apply: none required; verify any tests asserting exact subquery placeholder indices.
- verify: `go test ./...`  # subquery SQL placeholders are sequential; the AMQP per-publish confirmation wait honors the configured timeout
- ref: #571/#577/#579 · CHANGELOG 0.42.0

## E43 · v0.42.0 → v0.43.0 — query-builder SQLi close, leased tenant handles, hardened env/keep-alive config

- gist: direct-string identifiers in the query builder are now validated (M9 SQLi fix), raw per-tenant managers hand back a `ReleaseFunc`, `PoolKeepAliveConfig.Enabled` becomes `*bool`, and bare section-named env vars (e.g. `DEBUG`, `CACHE`, docker-link `SERVER_PORT=tcp://…`) are dropped before koanf unflatten (sub-keyed and `custom.*` vars still bind).
- build-caught: C43.2 C43.3
- preflight: `env | grep -iE '^(DEBUG|CACHE|DATABASE|DATABASES|SERVER|APP|LOG|MESSAGING|MULTITENANT|SOURCE|SCHEDULER|OUTBOX|INBOX|KEYSTORE|OBSERVABILITY)=|=tcp://'` — bare section-named or docker-link env vars (e.g. `SERVER_PORT=tcp://...`) that clobbered a config section before must be removed or moved under `CUSTOM_` before/at bump
- exit: `go get github.com/gaborage/go-bricks@v0.43.0 && go mod tidy && go build ./... && go test ./...`

### [C43.1] Query builder validates direct-string identifiers on all vendors · silent-behavior · when: match
- detect: `git grep -nE '\.(From|OrderBy|GroupBy|JoinOn|LeftJoinOn|RightJoinOn|InnerJoinOn|CrossJoinOn|Set|SetMap)\('`
- gate: match = you pass an SQL function/expression or dynamic identifier (e.g. `OrderBy("COUNT(*) DESC")`) as a plain string to one of these methods, so `ToSQL()` now returns an error instead of interpolating it. Bare/qualified columns, aliases (`"users u"`), `Table().As()`, and trailing `ASC`/`DESC`/`NULLS FIRST|LAST` still pass; user **values** through the Filter API were never affected.
- apply: wrap function/expression identifiers in `qb.Expr(...)`/`qb.MustExpr(...)` — e.g. `OrderBy(qb.MustExpr("COUNT(*) DESC"))` — and keep bare column/table identifiers as-is
- verify: `go test ./...`  # raw-expression OrderBy/GroupBy errors from ToSQL() until wrapped in Expr()
- ref: ADR-031 · #604

### [C43.2] Per-tenant resource managers return a third ReleaseFunc value · compile-break · when: match
- detect: `git grep -nE '(dbManager|cacheManager|messagingManager|DbManager|CacheManager)\.(Get|Publisher)\(|cache\.Manager\b'`
- scope: only DIRECT callers of the raw managers (`database.DbManager.Get`, `messaging.Manager.Publisher`, `cache.CacheManager.Get`); standard apps on `deps.DB(ctx)`/`deps.Cache(ctx)`/`deps.Messaging(ctx)`/`deps.DBByName` and the `ResourceProvider` interface are unaffected — the framework leases/releases for you
- before:
  ```go
  conn, err := dbManager.Get(ctx, tenantID)
  client, err := messagingManager.Publisher(ctx, tenantID)
  inst, err := cacheManager.Get(ctx, tenantID)
  ```
- after:
  ```go
  conn, release, err := dbManager.Get(ctx, tenantID) // Get(ctx, key) (Interface, ReleaseFunc, error)
  if err != nil {
      return err
  }
  defer release() // return the lease; release() is idempotent and does NOT close the shared pool

  client, release, err := messagingManager.Publisher(ctx, tenantID) // (AMQPClient, ReleaseFunc, error)
  // ... defer release()

  inst, release, err := cacheManager.Get(ctx, tenantID) // cache.Manager.Get: (Cache, ReleaseFunc, error)
  // ... defer release()  // on error the returned ReleaseFunc is nil — check err first
  ```
- verify: `go build ./...`
- ref: ADR-032 · #606/#607

### [C43.3] PoolKeepAliveConfig.Enabled changed from bool to *bool · compile-break · when: match
- detect: `git grep -nE 'PoolKeepAliveConfig|KeepAlive\.Enabled'`
- scope: code that constructs `config.PoolKeepAliveConfig` directly or reads `.Enabled` as a `bool` (e.g. tests, custom config wiring); YAML/env `database.pool.keepalive.enabled` binding is unchanged
- before:
  ```go
  PoolKeepAliveConfig{Enabled: true}
  PoolKeepAliveConfig{Enabled: false}
  if cfg.Pool.KeepAlive.Enabled { ... }
  ```
- after:
  ```go
  PoolKeepAliveConfig{Enabled: observability.BoolPtr(true)}
  PoolKeepAliveConfig{Enabled: observability.BoolPtr(false)}
  PoolKeepAliveConfig{}                          // nil → defaulted to enabled at validation
  if cfg.Pool.KeepAlive.IsEnabled() { ... }      // nil-safe reader (nil treated as disabled)
  ```
- verify: `go build ./...`
- ref: ADR-030 · #601

### [C43.4] Bare section-named env vars dropped before koanf unflatten; scalar-over-map merge guard · silent-behavior · when: match
- detect: `env | grep -iE '^(DEBUG|CACHE|DATABASE|DATABASES|SERVER|APP|LOG|MESSAGING|MULTITENANT|SOURCE|SCHEDULER|OUTBOX|INBOX|KEYSTORE|OBSERVABILITY)=|=tcp://'`
- gate: match = you set a bare section-named var (`DEBUG=1`, `CACHE=…`, `MULTITENANT=…`) or a K8s docker-link var (`SERVER_PORT=tcp://10.96.0.1:80`). The env loader has NO prefix filter — it still ingests EVERY process env var; what changed is that a bare var whose full key exactly equals a top-level section name is now dropped before koanf unflattens (previously it clobbered that section's map and crashed startup with `expected a map or struct, got string`), plus a scalar-over-map merge guard. Sub-keyed vars (`DEBUG_ENABLED`, `CACHE_REDIS_HOST`) AND all unrelated/app-specific vars still bind exactly as before; `custom` is deliberately NOT in the dropped set.
- apply: none required for the common case — sub-keyed and app-specific vars are unaffected. Only remove or rename any BARE section-named var (or scalar that would overwrite a section map) that you relied on; `custom.*` settings continue to bind.
- verify: `DEBUG=1 make run`  # app starts instead of crashing; confirm `CUSTOM_*` vars land in `custom.*`
- ref: #601

### [C43.5] App consumes validated startup-budget & manager-tuning keys; warns on under-provisioned pools · additive-optional
- note: new startup-budget/manager-tuning config keys are now honored (#600) and evicted handles close outside the manager lock, emitting an under-provisioned-pool WARN at startup (#605); no action — optionally tune the new keys and heed the pool WARN.
- ref: #600/#605 · CHANGELOG 0.43.0

### [C43.6] Oracle identifier quoting correction in query builder · silent-behavior · when: match
- detect: `git grep -nE 'NewQueryBuilder\(database\.Oracle|database\.Oracle'`
- gate: match = you build queries with the Oracle dialect and assert on the generated SQL string — Oracle identifier quoting is corrected (#603), so quoted-identifier expectations may shift. Runtime behavior needs no change.
- apply: update any Oracle SQL-string assertions to the corrected quoting
- verify: `go test ./...`  # Oracle query-builder tests pass
- ref: #603 · CHANGELOG 0.43.0

## E44 · v0.43.0 → v0.44.0 — dependency & CI housekeeping (no public API change)

- gist: A maintenance release: CI's `actions/checkout` goes v7 (framework-internal only) and four transitive Go deps (amqp091-go, go-redis, echo/v5, testcontainers) bump. No exported go-bricks symbol changes.
- build-caught: none
- preflight: none
- exit: `go get github.com/gaborage/go-bricks@v0.44.0 && go mod tidy && go build ./... && go test ./...`

### [C44.1] CI: actions/checkout bumped to v7 (framework-internal workflows only) · no-consumer-action
- note: The BREAKING label on #609 is scoped to go-bricks' own GitHub Actions workflows, not to any Go symbol. Your app's `.github/workflows/*.yml` are independent — this demo's `ci.yml`/`security.yml` are unaffected. Only act if you literally copied go-bricks' workflow files: then bump `actions/checkout@v6`→`v7` yourself (`grep -rniE 'actions/checkout@v[0-9]' .github/workflows/`).
- ref: #609 · CHANGELOG 0.44.0

### [C44.2] Transitive dependency bumps (amqp091-go v1.12.0, go-redis v9.21.0, echo/v5 v5.2.1, testcontainers v0.43.0) · no-consumer-action
- note: These are pulled in automatically when you bump go-bricks; `go mod tidy` reconciles your `go.mod` (this demo already resolves `labstack/echo/v5 v5.2.1`, `amqp091-go v1.12.0`, `redis/go-redis/v9 v9.21.0`). No public API impact — verify only with `go build ./... && go test ./...`. Confirm with `go list -m all | grep -E 'amqp091-go|go-redis|labstack/echo|testcontainers'`.
- ref: #616 · #598 · #612 · CHANGELOG 0.44.0

## E45 · v0.44.0 → v0.45.0 — echo-free boundary + bounded outbox/publish retries

- gist: ADR-034 removes every `echo.*` type from the public surface (flat `server.MiddlewareFunc`, typed `HandlerContext` accessors, `RootGroup()`/`ModuleGroup()` instead of `runner.Echo()`); ADR-033 bounds AMQP publish retries and drives outbox parking by `status='failed'` instead of `retry_count`.
- build-caught: C45.1 C45.2 C45.3 C45.4 C45.5 C45.6
- preflight: Before bumping (ADR-033 re-delivery surge): `psql ... -c "SELECT count(*) FROM gobricks_outbox WHERE status='pending' AND retry_count >= <outbox.maxretries>;"` — size the burst of soft-parked rows that will re-publish on the first post-upgrade relay cycle, and delete any you mean to abandon.
- exit: `go get github.com/gaborage/go-bricks@v0.45.0 && go mod tidy && go build ./... && go test ./...`

### [C45.1] Custom middleware: Echo nested closure → flat `server.MiddlewareFunc` · compile-break · when: match
- detect: `git grep -nE 'echo\.(HandlerFunc|MiddlewareFunc)|func\([a-z]* \*?echo\.Context\)|next\(c\)'`
- scope: apps that write their own middleware; standard `app.New()` bootstrap and typed handlers are unaffected.
- before:
  ```go
  func Auth(next echo.HandlerFunc) echo.HandlerFunc {
      return func(c *echo.Context) error {
          token := c.Request().Header.Get("Authorization")
          if token == "" {
              return server.NewUnauthorizedError("missing authorization header")
          }
          ctx := withUser(c.Request().Context(), token)
          c.SetRequest(c.Request().WithContext(ctx)) // context propagation
          return next(c)
      }
  }
  ```
- after:
  ```go
  func Auth() server.MiddlewareFunc {
      return func(c server.HandlerContext, next func() error) error {
          token := c.RequestHeader("Authorization")
          if token == "" {
              return server.NewUnauthorizedError("missing authorization header")
          }
          c.SetRequestContext(withUser(c.RequestContext(), token)) // context propagation
          return next() // continue the chain; to ABORT, return an IAPIError instead of calling next()
      }
  }
  ```
- verify: `go build ./...`
- ref: ADR-034 · #627 · wiki/adr_034_echo_boundary_types.md

### [C45.2] `HandlerContext.Echo` removed → typed accessors · compile-break · when: match
- detect: `git grep -nE 'ctx\.Echo|hctx\.Echo|\.Echo\.(Request|Response)'`
- scope: handlers/middleware that reached through the removed `.Echo` field; use `RequestContext()`, `Request()`/`ResponseWriter()`, `Param`/`Query`/`RequestHeader`/`Get`/`Set`.
- before:
  ```go
  func (h *Handler) getUser(req GetReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
      reqCtx := ctx.Echo.Request().Context()
      user, err := h.svc.Find(reqCtx, req.ID)
      // ...
  }
  ```
- after:
  ```go
  func (h *Handler) getUser(req GetReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
      reqCtx := ctx.RequestContext()
      user, err := h.svc.Find(reqCtx, req.ID)
      // ...
  }
  ```
- verify: `go build ./...`
- note (restored in v0.46.0): three v0.44 capabilities had NO v0.45 substitute and are restored as typed accessors in v0.46.0 — `ctx.Echo.Path()` → `ctx.RouteTemplate()`, `ctx.Echo.PathValues()` → `ctx.PathParams()` (neutral `[]server.PathParam`, route-template order, defensive copy), `ctx.Echo.SetPathValues(...)` → `ctx.SetPathParams(...)`. Projects landing on v0.45 with any of these call sites should proceed to v0.46 rather than work around. SEMANTIC CHANGE for PathValues() migrants: the v0.44 slice was a LIVE view — in-place element writes reached `Param()` and `param:` binding; the v0.46 `PathParams()` slice is a defensive copy, so in-place mutation silently does nothing. Rewrite mutation sites to read → modify → `ctx.SetPathParams(modified)`. WARNING: do not substitute stdlib `ctx.Request().PathValue(name)` — under echo v5 it is ALWAYS empty (echo deliberately never populates stdlib path values); and `ctx.Request().Pattern`, while it currently carries the template, is unpromised engine behavior — use `RouteTemplate()`.
- ref: ADR-034 · #627 · wiki/adr_034_echo_boundary_types.md

### [C45.3] `ServerRunner.Echo()` removed → `RootGroup()`/`ModuleGroup()`; `RegisterReadyHandler` retyped · compile-break · when: match
- detect: `git grep -nE 'runner\.Echo\(\)|RegisterReadyHandler|scheduler\.CIDRMiddleware|e\.(GET|POST|Use)\('`
- scope: apps that grabbed the raw `*echo.Echo` for `_sys`/debug routes or overrode readiness; `scheduler.CIDRMiddleware` now returns `server.MiddlewareFunc` (call site unchanged — only explicit `var` types).
- before:
  ```go
  e := runner.Echo()
  e.Use(server.LoggerWithConfig(appLogger, cfg))
  e.GET("/_sys/ping", pingHandler)
  runner.RegisterReadyHandler(func(c *echo.Context) error {
      return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
  })
  ```
- after:
  ```go
  root := runner.RootGroup() // no basePath; ModuleGroup() applies basePath for app routes
  root.Use(server.LoggerWithConfig(appLogger, cfg))
  root.Add(http.MethodGet, "/_sys/ping", pingHandler) // pingHandler is a server.Handler
  runner.RegisterReadyHandler(func(c server.HandlerContext) error {
      return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
  }) // pass nil to restore the built-in readiness check
  ```
- verify: `go build ./...`
- ref: ADR-034 · #627 · wiki/adr_034_echo_boundary_types.md

### [C45.4] Framework middleware constructors return `server.MiddlewareFunc`; `SkipperFunc` takes `*http.Request`; `EscalateSeverity` is a method · compile-break · when: match
- detect: `git grep -nE 'echo\.MiddlewareFunc|server\.EscalateSeverity\(|SkipperFunc'`
- scope: only code with explicit `echo.MiddlewareFunc` var types, a `SkipperFunc`, or the removed package-level `server.EscalateSeverity(c, level)`; plain `r.Use(server.CORS(...))` call sites are unchanged.
- before:
  ```go
  var cors echo.MiddlewareFunc = server.CORS(exposeResponseTime, env)
  func skip(c *echo.Context) bool { return c.Path() == "/health" }
  // ... in a middleware:
  server.EscalateSeverity(c, zerolog.WarnLevel)
  ```
- after:
  ```go
  var cors server.MiddlewareFunc = server.CORS(exposeResponseTime, env)
  func skip(r *http.Request) bool { return r.URL.Path == "/health" }
  // ... in a middleware (c is a server.HandlerContext):
  c.EscalateSeverity(zerolog.WarnLevel)
  ```
- note: the skipper rewrite `c.Path() == "/health"` → `r.URL.Path == "/health"` swaps a route-template check for a concrete-URL check — equivalent ONLY for static routes. For parameterized routes (`/users/:id` matches `/users/42`, `/users/43`, …) the faithful migration target is the route template via `ctx.RouteTemplate()` (v0.46.0+, see the C45.2 note) — i.e. compare inside the middleware body, where the `server.HandlerContext` is available, instead of in the `*http.Request` skipper.
- verify: `go build ./...`
- ref: ADR-034 · #627 · wiki/adr_034_echo_boundary_types.md

### [C45.5] Tests build the context via `server.NewHandlerContextForTest` · compile-break · when: match
- detect: `git grep -nE 'echo\.New\(\)\.NewContext|e\.NewContext\('`
- scope: unit tests that hand-built an echo context to drive a handler; handlers are now `server.Handler`. `NewHandlerContextForTest` returns a `server.HandlerContext` (no `Bind` method) — replace any echo-context calls the test made, e.g. `echoCtx.Bind(&req)` → `json.NewDecoder(req.Body).Decode(&req)`. Echo remains a `go.mod` dependency (server/ uses it internally); it drops to `// indirect` only if you remove ALL direct echo references, including test-only constants like `echo.HeaderContentType`/`echo.MIMEApplicationJSON` — otherwise it stays a direct dep, which is fine.
- before:
  ```go
  e := echo.New()
  c := e.NewContext(req, rec)
  err := handler.GetUser(c) // raw echo handler
  ```
- after:
  ```go
  ctx := server.NewHandlerContextForTest(rec, req, cfg)
  err := handler.GetUser(ctx) // handler is now a server.Handler
  ```
- verify: `go test ./...`
- ref: ADR-034 · #627 · wiki/adr_034_echo_boundary_types.md

### [C45.6] Custom `outbox.Store`: `FetchPending` loses `maxRetries`; new `MarkDeadLettered` · compile-break · when: match
- detect: `git grep -nE 'FetchPending|outbox\.Store|MarkDeadLettered'`
- scope: only apps that implement a custom `outbox.Store`; apps using `deps.Outbox` / the built-in PostgreSQL & Oracle stores need no change.
- before:
  ```go
  // FetchPending was retry_count-gated and took maxRetries:
  FetchPending(ctx context.Context, db dbtypes.Interface, batchSize, maxRetries int) ([]Record, error)
  // (no MarkDeadLettered method)
  ```
- after:
  ```go
  // FetchPending is status-gated only (drops maxRetries):
  FetchPending(ctx context.Context, db dbtypes.Interface, batchSize int) ([]Record, error)
  // new terminal-parking method for poison events:
  MarkDeadLettered(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error
  ```
- verify: `go build ./...`
- ref: ADR-033 · #626 · wiki/adr_033_outbox_retry_count_status_parking.md

### [C45.7] `messaging.Publish`/`PublishToExchange` are now bounded — return `ErrPublishRetriesExhausted` · silent-behavior · when: match
- detect: `git grep -nE 'PublishToExchange|\.Publish\(ctx|== context\.(Canceled|DeadlineExceeded)|ErrPublish'`
- gate: match = you call `Publish`/`PublishToExchange` directly, so the old "blocks forever until ACK/shutdown" assumption is now wrong — after `messaging.reconnect.maxpublishattempts` (default 5) it returns `ErrPublishRetriesExhausted` wrapping the cause, and once at least one publish attempt has failed, cancel/deadline errors are *wrapped*, so an `err == context.Canceled` / `== context.DeadlineExceeded` comparison can silently stop matching (use `errors.Is`).
- apply: Handle the returned error instead of assuming an infinite block (the durable path is the outbox, which retries next cycle) AND switch publish-error comparisons from `==` to `errors.Is(err, ...)`.
- verify: `go test ./...`  # then manually force a broker outage and confirm the direct publish returns rather than hangs, with `errors.Is(err, messaging.ErrPublishRetriesExhausted)` true (returns, does not hang)
- ref: ADR-033 · #626 · wiki/adr_033_outbox_retry_count_status_parking.md

### [C45.8] New config keys + startup validation (`maxpublishattempts`, `publishtimeout ≥ connectiontimeout`) · silent-config · when: no-match
- detect: `grep -rniE 'messaging\.reconnect\.maxpublishattempts|outbox\.publishtimeout|messaging\.reconnect\.connectiontimeout' config*.yaml`
- gate: no-match = the new defaults now govern you because the keys are unset — `messaging.reconnect.maxpublishattempts=5` and `outbox.publishtimeout=60s` apply automatically; harmless unless you later set `outbox.publishtimeout` below `messaging.reconnect.connectiontimeout`, which the outbox module now rejects at startup.
- apply: Leave unset to accept the safe defaults, OR if you set `outbox.publishtimeout` keep it `>= messaging.reconnect.connectiontimeout`.
- verify: `make run`  # a publishtimeout below connectiontimeout aborts startup with a clear error; otherwise boots normally
- ref: ADR-033 · #626 · wiki/adr_033_outbox_retry_count_status_parking.md

### [C45.9] Status-driven parking → re-delivery surge of previously soft-parked outbox rows · silent-behavior · when: match
- detect: `psql ... -c "SELECT count(*) FROM gobricks_outbox WHERE status='pending' AND retry_count >= <outbox.maxretries>;"` (run BEFORE upgrading)
- gate: match = that count is > 0 — before ADR-033 those rows were retry_count-gated out and left silently `pending`; the new status-gated `FetchPending` fetches them, so on the first post-upgrade relay cycle they all re-publish in a burst (correct at-least-once un-sticking, but a surprising surge). No DB migration is needed (the `status` column / `'failed'` value already exist).
- apply: Run the count query before upgrading, then either let idempotent consumers absorb the re-delivery OR delete the rows you intend to abandon; note `'failed'` rows now accumulate (`DeletePublished` purges only `'published'`) — monitor and prune.
- verify: `psql ... -c "SELECT count(*) FROM gobricks_outbox WHERE status='pending' AND retry_count >= <outbox.maxretries>;"`  # after the first relay cycle re-delivered volume matches the pre-upgrade count and consumers dedupe
- ref: ADR-033 · #626 · wiki/adr_033_outbox_retry_count_status_parking.md


---

_The sections below are reference material: the two config-key rename lookup tables (linked from atoms C401.1 and C41.7), followed by pre-v0.39 changes retained for consumers upgrading from older releases._

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
