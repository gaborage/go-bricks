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

```text
v0.39.1 ─E40─ v0.40.0 ─E401─ v0.40.1 ─E41─ v0.41.0 ─E42─ v0.42.0 ─E43─ v0.43.0 ─E44─ v0.44.0 ─E45─ v0.45.0 ─E49─ v0.49.0 ─E50─ v0.50.0 ─E51─ v0.51.0 ─E52─ v0.52.0 ─E55─ v0.55.0 ─E56─ v0.56.0 ─E57─ v0.57.0 ─E58─ v0.58.0 ─E581─ v0.58.1
```

> v0.46.0–v0.48.0 shipped additive-only changes (route template/path-param accessors, raw-route descriptors, module-contributed global middleware — adopt-only, no migration atoms), so E49 is the next hop after v0.45.0 and applies when crossing from any of v0.45.0–v0.48.0 to v0.49.0.
>
> v0.52.0–v0.54.0 shipped additive-only changes (shared control-plane ledger tenancy for outbox/inbox — opt-in, `tenancy` defaults to `per-tenant`; clearer duplicate-route startup diagnostics — the echo router already rejected duplicate method+path registrations), so E55 is the next hop after v0.52.0 and applies when crossing from any of v0.52.0–v0.54.0 to v0.55.0.

| edge | hop | worst risk | atoms | compiler-caught | preflight (run BEFORE the bump) |
| ------ | ----- | ----------- | ------- | ----------------- | --------------------------------- |
| E40 | v0.39.1 → v0.40.0 | additive (safe) | 6 | none | none |
| E401 | v0.40.0 → v0.40.1 | silent-config | 2 | none | none |
| E41 | v0.40.1 → v0.41.0 | compile-break | 7 | C41.2 C41.3 C41.4 | DB connection budget |
| E42 | v0.41.0 → v0.42.0 | silent (fail-closed) | 9 | none | TLS CA / dynamic-multitenant |
| E43 | v0.42.0 → v0.43.0 | compile-break | 6 | C43.2 C43.3 | bare section-named env vars |
| E44 | v0.43.0 → v0.44.0 | noop | 2 | none | none |
| E45 | v0.44.0 → v0.45.0 | compile-break | 9 | C45.1 C45.2 C45.3 C45.4 C45.5 C45.6 | outbox re-delivery count |
| E49 | v0.45.0 → v0.49.0 | silent-config | 6 | none | multi-tenant outbox timeout guards / stale `messaging.*` + `database.manager.*` values / reconnect delay keys go live / mode-aware cache pool / unit-less duration guard |
| E50 | v0.49.0 → v0.50.0 | config-break | 4 | none | Flyway migrate surfaces unparseable/failure output as an error; non-empty DB passwords < 8 bytes rejected at config validation + migrate; dev CORS wildcard opt-in; `multitenant.resolver.order` now REQUIRED for `type: composite` (no default — composite deployments fail to start until they declare one) |
| E51 | v0.50.0 → v0.51.0 | silent-behavior (adopt-only) | 3 | none | none |
| E52 | v0.51.0 → v0.52.0 | compile-break | 4 | C52.1 | if you set `.Args` on any declaration in ≤v0.51.0, verify current broker state before upgrading |
| E55 | v0.52.0 → v0.55.0 | additive (safe) | 3 | none | none |
| E56 | v0.55.0 → v0.56.0 | compile-break (C56.6 only partially — see its gate) + silent-behavior default flip (C56.11) | 15 | C56.6 C56.9 | grep log-driven alerts, dashboards, and test assertions for card-data / `iban` / `otp` field names — their values render `***` after the bump (C56.3); expect a cold tenant's first messaging request to fail fast instead of blocking (C56.4); if you assemble config in Go, check for `forwardedclientcert.require` without `enabled` — that service was serving unauthenticated traffic and now returns 401 (C56.5); if one queue name is declared from two places, confirm the two shapes agree — a mismatch that used to be silently overwritten now fails startup (C56.7); if you call a JOSE-protected peer, check for payload-free requests of **any** method — `GET`/`HEAD`/`DELETE` as well as `POST`/`PUT`/`PATCH` — since none of them are sealed now and a peer demanding `application/jose` on every request will answer 415 (C56.8); `/ready`'s 200 body gains `cache` and `cache_stats` and, where a cache is configured, every poll now issues a Redis `PING`, so update any test, schema, or dashboard that pins its exact key set and expect a live cache outage to read `unhealthy` (C56.10); if you run with a top-level cache enabled (or supply an `Options.CacheConnector`, which is probed regardless of `cache.enabled`) and have never set `cache.critical`, that outage now also answers `503` and drains every replica from rotation at once — decide before the bump whether to keep the strict default (and set `readinessProbe.failureThreshold: 3`) or add `cache.critical: false` (C56.11); and if any alert or runbook parses the Redis address out of `/ready`'s `503` body, repoint it at the app log or `/_sys/health-debug` (C56.12); and a module that cannot work without a database can now declare `app.DatabaseRequirer` so an absent one aborts startup rather than booting green (C56.13); check every environment that sets any `database.*` identity field for a complete section, since a partial one now fails startup (C56.14); and re-point any alert asserting `/ready` returns 503 for a database-free or multi-tenant service — both now return 200 (C56.15) |
| E57 | v0.56.0 → v0.57.0 | silent-behavior + breaking (C57.4, C57.5, C57.6, C57.7, C57.8 abort startup; C57.9 fails `httpclient` construction) | 9 | C57.7 (only partially — a direct call still compiles; see its scope) | if any alert, runbook, synthetic check, or contract test parses the driver error out of `/ready`'s database `503` body, repoint it at the app log or `<debug.pathprefix>/health-debug` (default `/_sys`) — the field is now the fixed string `database unavailable` (C57.1); and if you implement your own critical `app.Prober`, its `503` body now reads `<Name> unavailable` instead of its raw error, since sanitization became the default rather than an opt-in (C57.2); and if anything reads `db_stats.connections` out of `/ready`'s **200** body — a per-tenant pool dashboard, an activity alert, a test pinning the key set — repoint it at `<debug.pathprefix>/health-debug` or the OTel database metrics, because that array is gone from `/ready`; a repo-local grep alone will not find an out-of-repo dashboard (C57.3); and check every environment with `outbox.enabled: true` or `inbox.enabled: true` (outside per-tenant fan-out or a dynamic source) for a configured, reachable database whose ledger table exists — or whose `autocreatetable` can create it — because Init now aborts startup instead of booting green (C57.4); and check every `database.connectionstring` (root, `databases.*`, `multitenant.tenants.*`) with no `database.type` set — a recognized scheme (`postgres://`, `postgresql://`, `oracle://`) now infers its type and actually dials instead of booting into a dead connection, and an unrecognized scheme on the built-in connector now fails startup instead of failing at first query (C57.5); and check every environment for a database identity key delivered as an empty string (an empty `secretKeyRef`, `envsubst` over an unset variable) — that shape used to load silently as database-free and now aborts startup naming the key (C57.6); and check every environment for `debug.enabled: true` (`DEBUG_ENABLED=true`) with at least one `debug.endpoints.*` flag on, an empty `debug.allowedips` and no `debug.bearertoken` — all four required, and that service refuses to start after the bump; and if you assemble config in Go, check for a `Debug` block with `Enabled: true` and any endpoint flag but no `AllowedIPs` — that path never received the loopback default, so the refusal is reachable there by omission; grep shortlists, booting in staging decides (C57.7); and check every **single-tenant** service that declares AMQP consumers for a broker that is reachable, accepts its credentials, and accepts its declarations at boot, because a consumer bootstrap failure now aborts startup instead of logging one WARN and serving HTTP while consuming nothing forever — publisher-only and messaging-free services are unaffected (C57.8); and read every `WithJOSE` policy and direct `jose.Seal` call for an explicitly-set algorithm outside the allowlist — `KeyAlg: RSA1_5` or a non-AEAD `Enc` sealed successfully before and now fails `Build()` at startup, while a policy that named no algorithms at all takes the package defaults and starts working instead of failing every request (C57.9) |
| E58 | v0.57.0 → v0.58.0 | compile-break + breaking (C58.3 aborts startup) + behavior (C58.4, C58.5) | 5 | C58.1 C58.2 C58.3 | check every environment for a **negative** `cache.manager.maxsize` or `cache.manager.idlettl`, and — in multi-tenant mode with `cache.manager.maxsize` unset — a negative `multitenant.limits.tenants`, which becomes the pool size. Under `cache.enabled: false` such a value used to be inert and now aborts startup (C58.3); and if any dashboard, alert, or saved query reads OTLP-exported log records by a `service.*`, `telemetry.sdk.*`, or `deployment.environment.name` **record** attribute your code sets as a log field, re-key it to the `app.`-prefixed name (C58.4); and audit the log backend for dashboards, alerts, or saved queries filtering log records by **any** record-level resource attribute — the framework's `service.*` / `telemetry.sdk.*` / `deployment.environment.name` plus every key your deployment injects via `OTEL_RESOURCE_ATTRIBUTES` (`k8s.pod.name`, …) — since all of them move to resource level only; no code grep finds these (C58.5) |
| E581 | v0.58.0 → v0.58.1 | silent-behavior | 2 | none | if you cache any type carrying a time.Time, decide before the bump whether a compare-and-set on a sub-second timestamp may fail during the rolling deploy (C581.1); and if `observability.logs.samplingrate` is set to any value strictly between 0.0 and 1.0, expect the exported INFO/DEBUG log volume AND the membership of the sampled set to change: a rate at or above 0.00005 and below 0.01 exported nothing before the bump and starts exporting its configured fraction after it, a rate that is not a whole percent stops flooring (0.999 was 99%, now 99.9%), and every fractional rate redraws which traces land in the sample; a rate below 0.00005, plus 0.0 and 1.0, are unaffected (C581.2) |

**4 — Read each atom's gate before acting.** Every atom carries `when: match | no-match | always`:

- **`when: match`** → act only if `detect` returns ≥1 line (an API/arity/interface change, or a config key you set).
- **`when: no-match`** → act only if `detect` returns 0 lines. These are **default flips**: you are affected *precisely because the key is unset*, so the new default now governs you. A naive agent that greps, finds nothing, and concludes "not affected" is **wrong** for these — the miss is the actionable case.
- Per class: **compile-break** atoms are build-driven — you may defer reading them and let `go build ./...` at the hop's `exit` enumerate them, then fix to green. **silent-behavior / silent-config** have no compiler safety net — you MUST run their `detect`. **additive-optional / no-consumer-action** — skip unless adopting the feature.

> **Writing a `detect`: never use a PCRE escape in `git grep -E`.** Git's POSIX-ERE engine has no `\b`/`\s`/`\d`/`\w`; it strips the backslash and matches the bare letter, so `'Allow\b'` matches `Allowb` and not `Allow,`. The pattern still compiles and still exits cleanly — it just silently matches nothing, and step 4's `when: match` gate then reports "not affected" to every consumer. Use `[[:space:]]`, `[[:digit:]]`, `[[:alnum:]_]`, and `([^A-Za-z0-9_]|$)` for a trailing word boundary; `git grep -P` works only where git was built with PCRE. This applies to `git grep` only — a plain `grep -E` further down a pipe is GNU/BSD grep and handles `\b` fine.

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

- detect: `git grep -nE 'logger\.LogEvent([^A-Za-z0-9_]|$)'`
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

- detect: `git grep -nE '(dbManager|cacheManager|messagingManager|DbManager|CacheManager)\.(Get|Publisher)\(|cache\.Manager([^A-Za-z0-9_]|$)'`
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

  inst, release, err := cacheManager.Get(ctx, tenantID) // cache.CacheManager.Get(ctx, key): (Cache, ReleaseFunc, error)
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

## E49 · v0.45.0 → v0.49.0 — messaging defaults in all modes + publisher lifecycle hardening + database.manager.* keys

- gist: `config.Validate` now applies `messaging.*` reconnect/publisher defaults **unconditionally**, even when the root `messaging.broker.url` is empty — previously every no-root-broker config (all multi-tenant static deployments, since `validateNoSingleTenantConflict` rejects a root broker URL there; plus single-tenant apps without messaging) skipped both zero→default coercion and negative-value rejection. Consequences: the outbox `publishtimeout` guards (against `connectiontimeout` AND `readytimeout`, C45.8) now actually fire in multi-tenant mode, and negative `messaging.*` values now fail startup everywhere. Defaulting is publisher-mode-aware: `maxcached` 50 single-tenant / preserved-zero multi-tenant (pool scales to `multitenant.limits.tenants`). This release also raises the single-tenant publisher `IdleTTL` default 10m → 1h, adds a bounded readiness wait on cold publishes (#655/#656/#660), and introduces `database.manager.*` pool keys (C49.3) — previously-inert `database.manager.*` YAML/env values become live on upgrade. Finally, the four `messaging.reconnect` delay/backoff keys (`delay`/`maxdelay`/`reinitdelay`/`resenddelay`) now reach the AMQP client instead of being validated-but-ignored (C49.4), and `cache.manager.maxsize` defaulting becomes deployment-mode-aware so a multi-tenant fleet's cache pool scales to `multitenant.limits.tenants` rather than capping at 100 (C49.5). And a unit-less numeric YAML/JSON/TOML value on a `time.Duration` key (e.g. `delay: 300` intending seconds) — previously coerced to that many nanoseconds by `WeaklyTypedInput` and booted — now fails config decode with an actionable error naming the value; an explicit `0` still means use-the-default (C49.6).
- build-caught: none
- preflight: run the C49.1/C49.3/C49.4/C49.6 detect greps BEFORE the bump — stale config values (inverted timeout pairs, negative or unit-less-numeric durations, misplaced manager blocks) abort startup post-upgrade, and the tenants file for `go-bricks-migrate --source-config` often lives in a separate ops/infra repo that needs the same sweep
- exit: `go get github.com/gaborage/go-bricks@v0.49.0 && go mod tidy && go build ./... && go test ./...`

### [C49.1] messaging.* defaults/validation now run without a root broker URL — outbox timeout guards armed in multi-tenant · silent-config · when: match

- detect: `git grep -nE '(^[[:space:]]*|\.)(publishtimeout|connectiontimeout|readytimeout)[[:space:]]*:' -- '*.yaml' '*.yml'` (leaf-anchored so it matches BOTH the nested `outbox:`→`publishtimeout:` form and flat-dotted keys; also grep the env forms `OUTBOX_PUBLISHTIMEOUT`, `MESSAGING_RECONNECT_CONNECTIONTIMEOUT`, `MESSAGING_RECONNECT_READYTIMEOUT` in deploy manifests) — and check `messaging.*` keys for negative values (`git grep -nE ':[[:space:]]*-[0-9]' -- '*.yaml' '*.yml'`)
- gate: match = (a) you run multi-tenant with the outbox module and set `outbox.publishtimeout` below the effective `messaging.reconnect.connectiontimeout` (default 30s) **or** `messaging.reconnect.readytimeout` (default 5s) — both guards were silently skipped in multi-tenant mode because those timeouts stayed 0 without a root broker URL, so the config booted and then risked the unbounded duplicate-delivery loop the checks exist to prevent; startup now rejects it by design (`outbox.publishtimeout` defaults to 60s, so leaving it unset = unaffected); or (b) any deployment mode without a root `messaging.broker.url` carries a negative `messaging.*` value — previously silently ignored, now a startup validation error naming the key.
- apply: (a) raise `outbox.publishtimeout` to `>= max(messaging.reconnect.connectiontimeout, messaging.reconnect.readytimeout)` (with defaults: `>= 30s`); (b) delete or correct negative `messaging.*` values. Single-tenant deployments WITH a broker URL already had defaults and guards applied — nothing changes for them.
- verify: `make run`  # a previously-booting config either boots identically or aborts with a `messaging config: ...` / `outbox: publishtimeout ...` error naming the offending key
- ref: #659 · config/validation.go: validateMessaging · wiki/outbox.md

### [C49.2] Single-tenant publisher IdleTTL default 10m → 1h; cold publishes wait for readiness · silent-behavior · when: no-match

- detect: `git grep -nE '(^[[:space:]]*|\.)idlettl[[:space:]]*:' -- '*.yaml' '*.yml'` (also `MESSAGING_PUBLISHER_IDLETTL` in deploy manifests)
- gate: no-match = you left `messaging.publisher.idlettl` unset in a single-tenant deployment, so the effective idle-eviction TTL rises from 10m to 1h (#660) — publishers for low-frequency publish cadences stay warm longer (the fix for the once-daily cold-publish failure class, #655). Multi-tenant default stays 10m; explicitly-set values are untouched. Independently, the first publish on a cold publisher now waits up to `messaging.reconnect.readytimeout` (default 5s) for client readiness instead of failing fast with `ErrNotConnected` (#656), and evictions/idle-cleanups now log at Info with `Stats()` counters (#657).
- apply: none required; set `messaging.publisher.idlettl` explicitly if you relied on the 10m eviction cadence.
- verify: start the app and let a publisher idle  # eviction now logs at Info at the new TTL; a publish right after eviction no longer fails with ErrNotConnected
- ref: #655 #656 #657 #660 · app/managers.go · messaging/amqp_client.go

### [C49.3] database.manager.* pool keys go live (maxsize/idlettl/cleanupinterval) · silent-config · when: match

- detect: `git grep -nE '(^[[:space:]]*|\.)(maxsize|idlettl|cleanupinterval)[[:space:]]*:' -- '*.yaml' '*.yml'` then keep only hits under a `database:`/`databases:`/tenant `database:` block (leaf-anchored: matches nested and flat-dotted forms); also grep `DATABASE_MANAGER_` in deploy manifests
- gate: match = you already carry `database.manager.*` keys or `DATABASE_MANAGER_*` env vars — inert (silently ignored) on v0.45–v0.48, they now bind: negative values abort startup naming the key, and a `manager` block under `databases.<name>` or `multitenant.tenants.<id>.database` is now rejected at startup (it was and remains non-functional — only the primary `database.manager.*` is honored). no-match = adopt-only: unset keys default to today's exact hardcoded behavior, byte-identical (single-tenant 10 / 1h / 5m; multi-tenant `maxsize` still scales to `multitenant.limits.tenants`, `idlettl` 30m, `cleanupinterval` 5m). The keys govern the single process-wide manager, which also caches named `databases.<name>` and per-tenant handles — count those when sizing `maxsize` (see wiki/database.md).
- apply: delete stale/negative `database.manager.*` values and any `manager` block under named/tenant database entries; set the primary keys only to tune the pool.
- verify: `make run`  # boots identically when unset; a negative value or misplaced manager block aborts with an error naming the key
- ref: #658 · config/validation.go: applyDatabaseManagerDefaults · app/managers.go: BuildDatabaseOptions · wiki/database.md

### [C49.4] messaging.reconnect delay/backoff keys go live (delay/maxdelay/reinitdelay/resenddelay) · silent-config · when: match

- detect: `git grep -nE '(^[[:space:]]*|\.)(delay|maxdelay|reinitdelay|resenddelay)[[:space:]]*:' -- '*.yaml' '*.yml'` then keep only hits under a `messaging:`→`reconnect:` block (leaf-anchored: matches nested and flat-dotted forms); also grep `MESSAGING_RECONNECT_DELAY`, `MESSAGING_RECONNECT_MAXDELAY`, `MESSAGING_RECONNECT_REINITDELAY`, `MESSAGING_RECONNECT_RESENDDELAY` in deploy manifests
- gate: match = you already set one of `messaging.reconnect.{delay,maxdelay,reinitdelay,resenddelay}` — validated and defaulted since forever but silently ignored (the AMQP client used its own hardcoded 5s/60s/2s/5s), these values now take effect on upgrade. Know each knob's exact scope: `delay` is the full-jitter backoff base (each wait is uniform-random in `[0, min(delay·2^attempt, maxdelay))` — an upper bound, NOT a minimum spacing); `maxdelay` caps the connection-reconnect loop only (the consumer re-subscribe loop keeps its fixed 60s cap); `resenddelay` spaces channel-publish-error retries only (broker NACKs retry on a fixed 100ms backoff, confirmation timeouts retry immediately). Startup now also rejects inconsistent pairs: `maxdelay < delay` fails validation, and `outbox.publishtimeout < resenddelay` fails outbox Init (same class as the C45.8 guards). no-match = adopt-only: unset keys default to 5s/60s/2s/5s — byte-identical to the old hardcoded behavior.
- apply: review any explicitly-set delay/backoff keys — a large previously-inert `delay`/`maxdelay` now stretches reconnect waits that interact with the publish retry budget (`readytimeout` 5s pre-flight × `maxpublishattempts` 5): confirm publish-side timeouts still cover your worst-case backoff. Fix any pair the new startup guards reject.
- verify: `make run`  # boots identically when unset; an inverted maxdelay/delay or resenddelay/publishtimeout pair now aborts startup naming the keys
- ref: #662 · config/validation.go: validateMessaging · messaging/amqp_client.go: WithReconnectDelay/WithReconnectMaxDelay/WithReinitDelay/WithResendDelay · outbox/module.go: validatePublishTimeout

### [C49.5] multi-tenant cache pool scales to tenant limit when maxsize unset · silent-behavior · when: no-match

- detect: `git grep -nE '(^[[:space:]]*|\.)maxsize[[:space:]]*:' -- '*.yaml' '*.yml'` then keep only hits under a `cache:`→`manager:` block (leaf-anchored: matches nested and flat-dotted forms); also grep `CACHE_MANAGER_MAXSIZE` in deploy manifests
- gate: no-match = you run multi-tenant (`multitenant.enabled: true`) with `cache.manager.maxsize` unset — the cache pool previously capped at a flat 100 and now scales to **`multitenant.limits.tenants`** (NOT your static tenant count; `limits.tenants` itself defaults to 100, so a >100-tenant fleet must also raise `multitenant.limits.tenants` or the cap — and the pool-below-tenant-count WARN — stays). match = your explicit positive value wins in both modes, but note the falsy-zero: an explicit `maxsize: 0` is indistinguishable from unset (previously coerced to 100, now scales in multi-tenant). Single-tenant unset still defaults to 100; negative is rejected in both modes.
- apply: multi-tenant fleets above 100 tenants: confirm `multitenant.limits.tenants` covers your fleet (that is the value the pool now scales to). Set `cache.manager.maxsize` explicitly to pin a specific pool size; replace any explicit `maxsize: 0` with the intended positive value.
- verify: `make run`  # with limits.tenants >= your tenant count and maxsize unset, the cache pool-below-tenant-count WARN no longer fires
- ref: #668 · config/validation.go: applyCacheManagerDefaults · app/managers.go: BuildCacheOptions

### [C49.6] unit-less numeric durations rejected at config decode (fail-fast) · silent-config · when: match

- detect: `git grep -nE ':[[:space:]]*-?[0-9]+(\.[0-9]+)?([[:space:]]|$|#)' -- '*.yaml' '*.yml'` (tolerates trailing comments) then check each hit's field type — **treat every numeric-valued key as suspect until you've confirmed it is not `time.Duration`**. The regex over-matches genuine numerics (`server.port`, pool/connection counts, `maxsize`, retry counts) which are untouched, but do NOT assume thresholds/intervals are safe: `database.query.slow.threshold`, `database.pool.keepalive.interval`, `database.pool.idle.time`, `database.pool.lifetime.max`, `database.manager.idlettl`/`cleanupinterval`, `observability.metrics.export.interval`, and `app.startup.{timeout,database,messaging,cache,observability}` are all durations, alongside the obvious `timeout`/`delay`/`backoff` leaves. Also scan JSON tenant secrets (pool duration fields) and the tenants file passed to `go-bricks-migrate --source-config`. Env forms (`MESSAGING_RECONNECT_DELAY=300`) already failed pre-upgrade via `time.ParseDuration`'s missing-unit error; only numeric YAML/JSON/TOML leaked through.
- gate: match = a config carries a non-zero bare number (or a boolean) on a `time.Duration` key (e.g. `delay: 300`) — previously coerced to that many **nanoseconds** by `WeaklyTypedInput` and silently booted (busy-loop tickers, microsecond TTLs, usually masked by runtime fallbacks), now aborts startup naming the value. Negative and float unit-less numerics are rejected the same way. An explicit `0` still means use-the-default (the framework `unset -> default` idiom, byte-identical to before). The guard covers all four decode seams: `config.Load`, the public `deps.Config.Unmarshal`, per-tenant JSON secrets (`migration.SecretsProvider`, now guarded), and the `go-bricks-migrate` CLI's `--source-config` tenants file — the last often lives in a separate ops/infra repo the detect grep never scans, so run it there too.
- apply: add the unit suffix — `300` → `300s` (or `5m`, `1h30m`).
- verify: `make run`  # a config with a bare numeric duration now aborts with `unit-less numeric duration <value> — use a duration string with an explicit unit (e.g. "300s", "5m", "1h30m")` naming the key path; a properly-suffixed value boots identically
- ref: #665 · internal/configdecode/configdecode.go: NumericToDurationGuardHookFunc · migration/secrets.go: decodeSecretConfig · tools/migration/internal/commands/common.go: numericToDurationGuardHookFunc

## E50 · v0.49.0 → v0.50.0 — Flyway migrate surfaces unparseable/failure output as an error

- gist: `migration.Migrate`/`MigrateFor` (and everything on top of them — `RunMigrationsAtStartup`, multi-tenant `MigrateAll`, the `go-bricks-migrate` CLI) previously returned a **nil error with a zero-valued Result** when the Flyway subprocess exited 0 but its `-outputType=json` output could not be parsed — the parse error was only Debug-logged — so a migration whose outcome was unobservable was reported as success, and the `migration.applied` audit event recorded `Outcome=success` with an empty version. It now returns a non-nil error (`errors.Is` `migration.ErrFlywayOutputUnparsed` for empty/malformed/redaction-suppressed output, or `migration.ErrFlywayReportedFailure` for a `success:false` envelope even at exit 0) and the audit event records `Outcome=failed`. No exported signatures change; `parseFlywayJSON`'s own contract is unchanged. Additionally, non-empty DB passwords shorter than 8 bytes are now rejected (config validation + migrate) rather than suppressed — see C50.2. The dev-permissive reflect-any-origin + credentials CORS posture (a development-alias, or koanf-defaulted, `APP_ENV` with `CORS_ORIGINS` unset) now additionally requires `CORS_DEV_WILDCARD=true` — without it, dev fails closed like every other env — see C50.3. This hop also adds one **additive, adopt-only** feature (no atom): the opt-in `server.logroutes` flag emits a `Route registered` Info line per HTTP route at startup — default dev-on/prod-off (tri-state, `SERVER_LOGROUTES`), so prod is unaffected; silence a dev boot with `server.logroutes: false`. Finally, `multitenant.resolver.order` is now **required** for `type: composite` — the hardcoded header → subdomain → path order is gone and there is no implicit replacement, so **every composite deployment fails to start until it declares an order**. All three tenant sources are caller-written (the URL path is authored by the caller; `Host` is itself a request header), so the framework refuses to guess a precedence it cannot verify: either default would silently harm someone — header-first lets a caller-supplied header override an explicitly-configured subdomain/path, and subdomain-first would silently escalate gateway-fronted deployments whose gateway owns `X-Tenant-ID` — see C50.4.
- build-caught: none
- preflight: run the C50.2 detect sweep BEFORE the bump — a non-empty DB password `< 8` bytes (static or per-tenant, including the ops/infra tenants file) now aborts startup or the migrate
- exit: `go get github.com/gaborage/go-bricks@v0.50.0 && go mod tidy && go build ./... && go test ./...`

### [C50.1] migrate now errors on unparseable/failure Flyway output · silent-behavior · when: no-match

- detect: `git grep -nE '\.(Migrate|MigrateFor)\(' -- '*.go'` then keep call sites that ignore the returned error, plus any custom `provisioning.Steps.Migrate` that wires `MigrateFor` and drops its error
- gate: no-match = well-behaved callers already consult the returned error and now correctly surface a previously-silent failure (a genuinely broken/unobservable migration that used to pass). Multi-tenant `MigrateAll` now lists such tenants in `Failed()`; under the default `ContinueOnError=false` the first one aborts the fan-out. Callers that discarded the error may newly observe failures — this is the fix, not a regression.
- apply: handle the returned error (`errors.Is` the two sentinels above); never read a zero-valued `Result` as proof of success.
- verify: `go test ./...`
- ref: #673 · migration/result.go: migrateOutcome · migration/flyway.go: runFor

### [C50.2] DB passwords shorter than 8 bytes are rejected (config validation + migrate) · config-break · when: match

- detect: inspect every DB password for a **non-empty** value shorter than 8 bytes — static (`database.password`, named `databases.*`) and per-tenant (tenant store / AWS secrets, the `go-bricks-migrate --source-config` tenants file)
- gate: match = any database config uses a non-empty password `len < 8`. Such passwords can't be safely redacted from Flyway output, so `config.Validate` now **rejects static configs at startup** (a `database.password` field error), and the migrate path **rejects per-tenant configs before running Flyway** (`errors.Is migration.ErrDatabasePasswordTooShort`). This replaces #674's suppress-then-fail behavior and closes the audit false-negative where a short-password migration was audited as `Outcome=failed` even on success. **Empty passwords (trust/IAM auth) are exempt.** `RunMigrationsAtStartup` under `APP_ENV=dev`/`local` fails startup for a short password.
- apply: use a DB password of at least 8 bytes for every database (or leave it empty for trust/IAM auth).
- verify: `make run` boots (or aborts with a `database.password` error naming the field); `go-bricks-migrate migrate` rejects a short-password tenant with `database password too short to safely redact Flyway output`
- ref: #673 #675 · ADR-037 · config/validation.go: validateDatabaseCoreFields · migration/flyway.go: ensurePasswordRedactable

### [C50.3] dev wildcard CORS requires explicit opt-in (`CORS_DEV_WILDCARD`) · silent-behavior · when: no-match

- detect: check every dev/test runtime environment (shell profiles, compose files, deployment manifests) for a `CORS_DEV_WILDCARD` value that parses true under `strconv.ParseBool` (`true`, `1`, `t`, …) or a non-empty `CORS_ORIGINS` allowlist — a `CORS_DEV_WILDCARD` that is present but false/empty/malformed is still a no-match (runtime treats it as no opt-in; dev will fail closed and remediation is still needed)
- gate: no-match = you relied on the old default where a development-alias `APP_ENV` (or an unset `APP_ENV`, which koanf defaults to `development`) received reflect-any-origin + `AllowCredentials=true` CORS with no explicit setting. That posture now requires `CORS_DEV_WILDCARD=true`; without it, dev fails closed exactly like neutral/production envs (no `Access-Control-Allow-Origin` header is emitted). The flag is ignored outside development aliases, and unparseable values are treated as false with a WARN. Production/staging behavior is unchanged.
- apply: for browser-based local dev set `CORS_DEV_WILDCARD=true`; or set `CORS_ORIGINS=<comma-separated origins>` for a strict allowlist (works in any env).
- verify: boot with `APP_ENV=development` and no flag — startup logs `WARN [server.cors] … CORS_DEV_WILDCARD is not enabled` and a cross-origin browser request gets no `Access-Control-Allow-Origin`; set `CORS_DEV_WILDCARD=true` and the wildcard-echo WARN appears instead.
- ref: ADR-038 · server/cors.go: corsEcho / devWildcardOptIn

### [C50.4] `multitenant.resolver.order` is REQUIRED for `type: composite` (startup failure) · config-break · when: match

- detect: find every composite resolver across ALL config sources — a composite resolver **anywhere** is a match. The `multitenant.resolver.order` key **does not exist at v0.49.0**; it is introduced by this hop, so no config you are upgrading *from* can already pin one and every composite is actionable by definition. (If you added an order while trialing this release, you are already compliant and `apply` is a no-op.) YAML: `git grep -nE "type: *composite" -- '*.yaml' '*.yml'`. Environment (env vars outrank YAML): `MULTITENANT_RESOLVER_TYPE=composite` — sweep shell profiles, `.env` files, compose files, and deployment manifests (`git grep -rn "MULTITENANT_RESOLVER_"` ; `grep -rn "MULTITENANT_RESOLVER_" k8s/ deploy/ .env* 2>/dev/null`). Match = you have a composite resolver. (Non-composite types are unaffected — except that `order` set on one is now a startup error instead of a silent no-op, so drop it if present.)
- gate: match = **your app will FAIL TO START** until `multitenant.resolver.order` is set. There is no implicit default any more (the old hardcoded order was header → subdomain → path); `config.Validate` now rejects a composite config with an empty order, naming the key and both candidate values. Pick by what your **edge** enforces, because all three sources are caller-written (the URL path is authored by the caller; `Host` is itself a request header, constrained only if your ingress pins it): **(a)** a trusted gateway authenticates the caller and **owns `X-Tenant-ID`** (strips the inbound header, sets its own) → you need **header-first**, `[header, subdomain, path]` — adopting the recommended order instead would let a caller-controlled `Host`/path outrank your gateway's assertion; **(b)** per-tenant DNS (each tenant has its own hostname) → the recommended `[subdomain, path, header]`; **(c)** path-scoped contracts with **no** per-tenant DNS → `[path, header]` — omit `subdomain` and you need no `domain` at all (listing `subdomain` without per-tenant DNS just forces you to invent a `domain` the resolver never matches, and validation now requires a real one); **(d)** no legacy header clients left → drop `header` from the order entirely (e.g. `[subdomain, path]`), so an unmatched request fails closed instead of falling through to the header. A sub-resolver named in the order must also be configured: `path` requires `path.segment > 0`, `subdomain` requires a real `domain` (a `domain` of `"."` is now rejected); a composite whose order omits `subdomain` no longer needs a `domain` at all. Unknown entries and duplicates are rejected. **Ordering is identification, not authorization** — regardless of order, authorize the resolved tenant (`multitenant.Tenant(ctx)`) against the authenticated principal, and note that a request matching *no* higher-precedence source still falls through to the header (apex host + unprefixed path + `X-Tenant-ID` ⇒ header wins), so the edge must force every tenant-scoped request to carry a resolvable subdomain/path.
- apply: add the order to the composite resolver block —

  ```yaml
  multitenant:
    resolver:
      type: composite
      order: [subdomain, path, header]   # or [header, subdomain, path] if your gateway owns X-Tenant-ID
                                         # or [path, header] if you have no per-tenant DNS
      domain: api.example.com            # required when order contains `subdomain`
      path:
        segment: 2                       # required (> 0) when order contains `path`
  ```

  Env form is a comma-separated list: `MULTITENANT_RESOLVER_ORDER=header,subdomain,path`. Also delete any `multitenant.resolver.order` set on a **non-composite** type — it is now rejected at startup.
- verify: `make run` — a composite config with no order aborts naming `multitenant.resolver.order` (`required when multitenant.resolver.type is 'composite' — no implicit default`); once set, startup succeeds. Then send a request carrying both a valid subdomain/path tenant and a conflicting `X-Tenant-ID`: the resolved tenant is whichever source you put first. `go test ./config/ ./server/ ./multitenant/`
- ref: ADR-039 · config/validation.go: validateResolverOrder · server/middleware.go: compositeSubResolvers · config/types.go: DefaultResolverOrder

## E51 · v0.50.0 → v0.51.0 — echo/v5 v5.3.0 (group implicit-404 revert + stricter JSON bind + configurable body limit)

- gist: The `github.com/labstack/echo/v5` bump v5.2.1 → v5.3.0 is behavior-affecting, not a pure version bump — adopt-only for consumers (no code migration required, no exported go-bricks signature changes). Three observable shifts: (1) echo restored v4's behavior where a middleware-bearing group auto-registers an implicit `/*` catch-all, so group middleware (the scheduler `/_sys` CIDR gate, the debug auth gate, any app sub-group with middleware) now ALSO runs on unmatched sub-paths and wrong-method requests under its prefix — a defense-in-depth win — and a wrong-method request under such a group returns 404 (no `Allow` header) instead of 405; go-bricks intentionally KEEPS echo's new default (does NOT set `NoGroupAutoRegister404Routes`) to preserve the gate-coverage win and hardens `HandlerContext.PathParams()`/`RouteTemplate()` to still report "unmatched" for the catch-all. (2) JSON binding is stricter — a request body with trailing NON-whitespace after the top-level JSON value (a second value or stray bytes) is now rejected (400) where v5.2.1 silently accepted it; trailing whitespace still binds (echo switched `Deserialize` from `json.Decoder` to `json.Unmarshal` + a pooled buffer, also a small per-bind allocation win). (3) A new `server.bodylimit` config (int64 bytes, default 10 MB) makes the request body cap configurable.
- build-caught: none
- preflight: none
- exit: `go get github.com/gaborage/go-bricks@v0.51.0 && go mod tidy && go build ./... && go test ./...`

### [C51.1] Middleware-bearing groups auto-register an implicit `/*` catch-all (405 → 404 under a group; gate now covers unmatched sub-paths) · silent-behavior · when: match

- detect: `git grep -nE 'StatusMethodNotAllowed|MethodNotAllowed|405|Allow([^A-Za-z0-9_]|$)|/_sys' -- '*_test.go'` then keep hits that assert a wrong-method response (or an `Allow` header) for a path UNDER a middleware-bearing group prefix
- gate: match = you have a test/client/monitor that expects 405 + an `Allow` header for a wrong-method request under a group prefix (e.g. `/_sys/*`, the debug group, or any app sub-group with middleware), OR you relied on that group's middleware NOT running for unmatched sub-paths. On echo v5.3.0 the group's implicit `/*` catch-all shadows echo's automatic 405 for the WHOLE prefix: both an unmatched sub-path AND a wrong-method request to an existing route under the group now return 404 (no `Allow`), with the group middleware (CIDR gate, auth gate) running first — so an unmatched sub-path under a gated prefix is now denied by the gate instead of falling through. Scope: this is limited to routes under a middleware-bearing group. no-match = TOP-LEVEL routes (not under such a group), real matched routes, and the global 404/405 fallbacks are unaffected — a wrong-method request to a top-level route still returns 405 + `Allow`.
- apply: none required — this is a security-positive default the framework keeps deliberately. Update any test/monitor that asserted 405 + `Allow` under a group prefix to expect 404, and confirm nothing depended on group middleware being skipped for unmatched sub-paths.
- verify: `go test ./...`  # a wrong-method request under `/_sys/...` returns 404 through the CIDR gate; tests asserting 405/`Allow` under a middleware group now expect 404
- ref: echo/v5 v5.3.0 · labstack/echo#530 · CHANGELOG 0.51.0

### [C51.2] JSON bind rejects trailing bytes after the top-level value · silent-behavior · when: match

- detect: audit any client/producer that POSTs to this service and appends content after the JSON document (concatenated objects, a trailing newline-delimited record, stray bytes) — not reliably greppable in this repo
- gate: match = a caller sends a request body with extra NON-whitespace after the top-level JSON value (a second JSON value or stray bytes) — v5.2.1's `json.Decoder`-based bind silently accepted (and ignored) the trailing content; v5.3.0's `json.Unmarshal`-based bind rejects the whole body with 400. Trailing whitespace (a newline, spaces) is still accepted, and well-formed single-document bodies are unaffected and gain a small per-bind allocation win. no-match = your callers send exactly one JSON value per body, unaffected.
- apply: fix the offending client to send exactly one JSON value per request body; there is no opt-out.
- verify: `go test ./...`  # then POST a body with trailing non-whitespace (e.g. a second JSON value) and confirm a 400 (previously 200)
- ref: echo/v5 v5.3.0 · CHANGELOG 0.51.0

### [C51.3] New `server.bodylimit` config caps request body size (default 10 MB) · silent-behavior · when: no-match

- detect: `git grep -nEi '(^[[:space:]]*|\.)bodylimit[[:space:]]*:|SERVER_BODYLIMIT'`
- gate: no-match = you leave `server.bodylimit` unset, so the new default governs — the accepted request body is capped at 10 MB (10485760 bytes) and a larger body is rejected with 413 before the handler runs. match = you set an explicit **positive** byte count, which then governs (raises or lowers the cap); an explicit `0` resolves to the 10 MB default and a negative value is rejected at config validation.
- apply: leave unset for the 10 MB default OR set `server.bodylimit` (int64 bytes, env `SERVER_BODYLIMIT`) to a positive value to raise it for large-upload/bulk-import endpoints or lower it to tighten the boundary.
- verify: `make run` then POST a body larger than the configured cap  # rejected with 413; a body under the cap is accepted
- ref: echo/v5 v5.3.0 · server config · CHANGELOG 0.51.0

## E52 · v0.51.0 → v0.52.0 — messaging declaration Args reach the broker (AMQPClient signature change)

- gist: `AMQPClient.DeclareQueue` / `DeclareExchange` / `BindQueue` (and the testify `MockAMQPClient`'s matching `Expect*` helpers) changed shape: they now take `(ctx context.Context, decl *QueueDeclaration / *ExchangeDeclaration / *BindingDeclaration)` instead of positional fields. The declaration's `Args` is now forwarded to RabbitMQ instead of being silently dropped, and ctx is checked before each broker operation (amqp091 declares are not context-aware on the wire, so ctx is a pre-flight cancellation check aligning these methods with the Context-First rule the rest of the interface already follows). `QueueDeclaration.Args` / `ExchangeDeclaration.Args` / `BindingDeclaration.Args` were already deep-copied on registration and folded into the topology hash — only the broker-facing boundary (`AMQPClientImpl`, hardcoded `nil`) discarded them. This enables broker-side dead-lettering for nacked-without-requeue deliveries — parked only when the full DLX route (exchange + queue + binding) is provisioned; `x-dead-letter-exchange` alone routes, it does not retain — and unblocks attaching to ops-provisioned queues declared with arguments (e.g. `x-queue-type=quorum`). See [ADR-040](adr_040_declaration_args_passthrough.md).
- build-caught: any direct call to `AMQPClient.DeclareQueue`/`DeclareExchange`/`BindQueue`, and any hand-rolled fake/mock implementing `messaging.AMQPClient`
- preflight: if you set `.Args` on any declaration in ≤v0.51.0, verify current broker state (management UI) before upgrading — see C52.2
- exit: `go get github.com/gaborage/go-bricks@v0.52.0 && go mod tidy && go build ./... && go test -race ./...`

### [C52.1] AMQPClient.DeclareQueue/DeclareExchange/BindQueue (+ mock Expect helpers) now take (ctx, declaration struct) · compile-break · when: match

- detect: `git grep -nE '\.(DeclareQueue|DeclareExchange|BindQueue)\(|Expect(DeclareQueue|DeclareExchange|BindQueue)(Any)?\(|messaging\.AMQPClient' -- '*.go' | grep -vE '\bdecls?\.'` — call sites, `Expect*` helper usage, AND any type implementing or asserting `messaging.AMQPClient` (hand-rolled fakes declare the three methods; find them with `git grep -nE 'func \([^)]+\) (DeclareQueue|DeclareExchange|BindQueue)\('`). The trailing exclusion drops the `Declarations` helper API, which shares these method names (`decls.DeclareQueue(name)` etc.) but is unaffected — adjust it to your local receiver name if it isn't `decls`
- gate: match = you call these methods directly, or maintain a hand-rolled fake/mock implementing `messaging.AMQPClient` (including a `testing/mocks.MockAMQPClient`-style testify mock using `ExpectDeclareQueue`/`ExpectDeclareExchange`/`ExpectBindQueue`/their `...Any` variants). no-match = you only use the `Declarations` helpers (`decls.DeclareQueue`, `decls.DeclareTopicExchange`, `decls.DeclareBinding`, ...) — recompile only, no source change.
- apply: at each call site, wrap the positional values in the declaration struct and prepend ctx — `client.DeclareQueue(ctx, &messaging.QueueDeclaration{Name: n, Durable: true, Args: args})` (the `messaging.NewQueue` / `NewTopicExchange` / `NewBinding` helpers build declarations with production-safe defaults; set remaining fields and `Args` before the call). Fake/mock method signatures become `(ctx context.Context, decl *messaging.QueueDeclaration)` (and exchange/binding equivalents), reading fields from the declaration. The testify `Expect*` helpers now take the declaration itself (`ExpectDeclareQueue(queue *messaging.QueueDeclaration, err error)`; nil declaration = match any), and each `...Any` helper matches exactly two `mock.Anything` parameters.
- verify: `go build ./... && go test -race ./...`  # compiler enumerates every remaining call site until green
- ref: ADR-040 · `messaging/messaging.go` · `testing/mocks/amqp.go`

### [C52.2] Args set on declarations are now sent to the broker (previously silently dropped) · silent-behavior · when: match

- detect: `git grep -nE '\.Args\[|\.Args[[:space:]]*=|(^|[^A-Za-z0-9_])Args:' -- '*.go'` then keep hits that populate a `QueueDeclaration`/`ExchangeDeclaration`/`BindingDeclaration`'s `Args` field — indexed writes (`q.Args[k] = v`), whole-map assignment (`q.Args = map[string]any{...}`), or struct-literal fields, directly or via `decls.Queues[...]`/`decls.Exchanges[...]`/`decls.Bindings[...]`. The struct-literal branch matches the `Args:` key itself rather than requiring a value on the same line, so `Args: args`, `Args: nil`, `Args: make(map[string]any)`, and a value wrapped onto the next line are all caught; the leading `(^|[^A-Za-z0-9_])` keeps it from firing on unrelated identifiers ending in `Args:` (e.g. `expectedArgs:` in tests). **Limits of the probe:** it is line-oriented, so it cannot see an `Args` map assembled elsewhere and passed in by variable, a field set through reflection or a helper, or a declaration built in a package you only consume. It also matches `Args` fields on unrelated types — that is what the "keep hits that populate" filter above is for. Treat a clean result as a strong hint, not proof; if you declare any messaging topology, read your declaration sites once by hand
- gate: match = you populated `.Args` on any declaration in ≤v0.51.0. Those args now participate in the actual broker calls. Queues/exchanges: args join RabbitMQ's declare-equivalence check — a pre-existing queue/exchange whose server-side arguments differ from what you now declare fails startup with `406 PRECONDITION_FAILED` (previously your `Args` were silently ignored and the mismatch never surfaced). Bindings: args do NOT trigger 406 — a binding's identity is `(source, destination, routing key, args)`, so redeclaring with different args creates an ADDITIONAL binding while the old argless one persists (both route; for headers exchanges the args change matching semantics), silently altering delivery behavior instead of failing. no-match = you never set `.Args` — no behavior change; declares still send no arguments table, byte-identical to pre-v0.52.0.
- apply: before upgrading, verify the broker's current arguments match what your `Args` maps now declare — queues/exchanges via the management UI or `rabbitmqctl list_queues name arguments` / `rabbitmqctl list_exchanges name arguments`, bindings via `rabbitmqctl list_bindings source_name destination_name routing_key arguments`; reconcile queues/exchanges by updating your `Args` to match the broker or deleting/recreating the broker object (data-loss risk on delete — plan accordingly for durable queues with messages in flight); for bindings, delete the stale argless binding (unbind requires the SAME args the binding was created with) so only the args-bearing one remains.
- verify: `make run` (or equivalent staging deploy) against the real broker — startup succeeds with no `406 PRECONDITION_FAILED`; for a queue carrying `x-dead-letter-exchange`, provision the complete dead-letter route first (DLX exchange + DLQ queue + binding), then confirm a nacked-without-requeue message is parked in the DLQ instead of dropped; for bindings that carry args, `rabbitmqctl list_bindings` shows exactly one binding per intended route (no stale argless duplicate)
- ref: ADR-040 · `messaging/amqp_client.go` (`toTable`) · `messaging/registry.go` (`DeclareInfrastructure`)

### [C52.3] `database.postgresql.schema` now targets migrations · silent-config · when: match

- detect: your config sets `database.postgresql.schema` (YAML `postgresql: { schema: ... }`, env `DATABASE_POSTGRESQL_SCHEMA`, or a tenant secret's `"postgresql": {"schema": ...}`)
- gate: none — the key previously fed only observability namespaces; it now also appends `-schemas=<schema> -defaultSchema=<schema>` to every Flyway invocation
- apply: if you set the key expecting schema-targeted migrations, nothing to do — it now works. If you set it for observability labeling while migrations deliberately land in `public`, unset it (or move the label elsewhere) before upgrading
- verify: run `migrate info` against a staging target and confirm `flyway_schema_history` resolves in the intended schema
- ref: #716

### [C52.4] DeclareQueueWithDLQ declarative dead-letter opt-in · additive-optional

- note: `decls.DeclareQueueWithDLQ(name, spec)` declares the fanout DLX + parking queue + binding and sets `x-dead-letter-exchange` (and optionally `x-dead-letter-routing-key`) on the primary queue in one call — failed deliveries park in the default `<queue>.dlq` (or the configured parking queue) instead of dropping. Raw `Args["x-dead-letter-exchange"]` (see "Dead-Lettering" in wiki/messaging.md) remains valid for custom topologies. Purely additive; existing hand-rolled DLX declarations are untouched.
- ref: #721 · messaging/helpers.go

## E55 · v0.52.0 → v0.55.0 — database Execute* query/exec helpers + httpclient client TLS + server TLS listener

- gist: Adds `database.ExecuteQuerySingle` / `ExecuteQueryMany` / `ExecuteUpdate` / `ExecuteUpdateOne` / `ExecuteInsert`, a 2-method `database.Executor` interface (satisfied by both `database.Interface` and `database.Tx`), and `database.Raw(sql, args...)` for hand-written SQL — collapsing the repeated `ToSQL()` → `Query`/`Exec` → scan/`RowsAffected` → error-wrap glue every SQL repository re-implements. Also adds `httpclient.NewClientTLSConfig` and `httpclient.Builder.WithTLSConfig` for config-driven client certificates / mutual TLS, and `server.tls.*` for a config-driven HTTPS listener (ADR-042; client-certificate verification deferred). No exported go-bricks symbol changes outside these new surfaces; purely additive.
- build-caught: none
- preflight: none
- exit: `go get github.com/gaborage/go-bricks@v0.55.0 && go mod tidy && go build ./... && go test ./...`

### [C55.1] database Execute* helpers (Executor + Raw + typed errors) · additive-optional

- note: `database.ExecuteQuerySingle/ExecuteQueryMany/ExecuteUpdate/ExecuteUpdateOne/ExecuteInsert`
  run builder output or `database.Raw(sql, args...)` against either a connection
  or a `Tx` (both satisfy the new 2-method `database.Executor`), replacing
  hand-rolled ToSQL→Query/Exec→scan/RowsAffected glue. `ExecuteUpdate` returns
  the raw affected-row count and does not interpret it at all (any count,
  including zero, is legitimate for bulk or idempotent writes); `ExecuteUpdateOne`
  wraps it and enforces an exactly-one-row contract — zero rows maps to
  `ErrNoRows`, and **more than one** row affected is rejected as `*ExecError` at
  `StageRowsAffected` instead of silently reported as success (a
  broader-than-intended `WHERE` predicate updating several rows is a
  data-integrity failure, not a "found it" outcome). Errors: `errors.Is(err,
  database.ErrNoRows)` for zero-row outcomes (wraps `sql.ErrNoRows`, so
  `IsNotFound` matches too); `var execErr *database.ExecError;
  errors.As(err, &execErr)` for build/exec/scan/iterate/close/rows_affected
  infrastructure failures (`Stage` is `database.ExecStage`) — `close` covers a
  `Rows.Close` failure surfaced after a successful `ExecuteQuerySingle` scan;
  `rows_affected` also covers `ExecuteUpdateOne`'s multi-row rejection.
  `ExecuteQueryMany` also rejects a nil `scan` callback at `StageBuild` before
  dispatching the query. Purely additive; existing repository code is untouched.
- security: every `database.Raw(sql, args...)` call site requires an adjacent
  `// SECURITY: Manual SQL review completed - <what was verified>` comment, exactly as
  `f.Raw()`/`jf.Raw()` do — `Raw` replaces the whole statement and so bypasses the
  builder's identifier validation. Enforced by `.claude/hooks/check-raw-sql.sh`; audit
  grep `git grep -E 'f\.Raw\(|jf\.Raw\(|database\.Raw\('`.
- ref: #772 · database/execute.go

### [C55.2] httpclient client TLS / client certificates · additive-optional

- note: `httpclient.NewClientTLSConfig(*httpclient.ClientTLSConfig)` loads client
  certificate, key and CA material — each from a PEM file path (`CertFile`/`KeyFile`/
  `CAFile`) or a base64-encoded PEM value (`CertValue`/`KeyValue`/`CAValue`) — into a
  `*tls.Config` with a TLS 1.2 floor (`MinVersion: "1.3"` opts up), and the new
  `Builder.WithTLSConfig(*tls.Config)` installs it as the client's base transport —
  a clone of `http.DefaultTransport`, or an equivalently-configured transport when
  that global has been replaced. It shares the base-transport slot with
  `WithTransport` — last call wins — and wrapper layers such as `WithJOSE` still
  stack on top regardless of call order. Purely additive; no existing signature or
  default changes, so mTLS toward partners no longer needs a hand-built
  `http.Transport` per consumer.
- security: the loader never sets `InsecureSkipVerify`; the escape hatch is an
  explicit hand-built `*tls.Config` passed to `WithTLSConfig`. Two silent-weakening
  shapes to check when adopting: a configured CA **replaces** the system roots (a
  client pinned to a private partner CA can no longer verify public-CA endpoints —
  build your own pool from `x509.SystemCertPool()` if you need both), and a CA-only
  config authenticates the **server** only, presenting no client certificate — set
  `RequireClientCert: true` so a missing cert/key pair fails at load instead of
  silently downgrading intended mTLS to one-way TLS.
- ref: #767 · httpclient/tls.go

### [C55.3] server TLS listener (HTTPS) · additive-optional

- note: `server.tls.*` (`enabled`, `certfile`/`certvalue`, `keyfile`/`keyvalue`,
  `minversion`) enables HTTPS on the go-bricks HTTP listener via Echo's
  `StartConfig.TLSConfig`. Cert and key each come from a file path or a
  base64-encoded value — exactly one source per piece — loaded through the
  same `internal/secretfile` guards the httpclient TLS loader (C55.2) uses.
  `minversion` floors at TLS 1.2 (`""`/`"1.2"`); `"1.3"` opts up. The all-zero
  default leaves the listener plaintext, byte-for-byte unchanged from prior
  behavior. Client-certificate verification is a separate, gated follow-up
  (ADR-042) — not part of this atom.
- security: bad or unreadable material fails `Start()` fast rather than
  silently falling back to plaintext; staged-but-disabled material (fields
  set while `server.tls.enabled` is false) emits one WARN naming
  `server.tls.enabled` so a mistyped flag is never silent. HTTP/1.1-only —
  `NextProtos` is deliberately left unset (see ADR-042).
- ref: #767 · ADR-042 · server/tls.go

## E56 · v0.55.0 → v0.56.0 — ALB forwarded-client-cert identity middleware + seal-payload CLI + widened logger mask list + httpclient Build fail-closed

- gist: Adds `server.forwardedclientcert.*` (`enabled`, `require`) for a config-gated middleware that parses ALB verify-mode `X-Amzn-Mtls-Clientcert-*` identity headers into a typed `server.ForwardedClientCert` (ADR-043; identification, not authorization). Also adds the installable `cmd/seal-payload` CLI for curl-testing jose-tagged endpoints. `logger.DefaultFilterConfig()` gains eleven card-data/PII field names, which silently changes log output for anyone on the defaults (C56.3). Multi-tenant lazy consumer setup also stops blocking over-budget callers: `messaging.Manager.EnsureConsumers` now returns as soon as the caller's own context ends, while the setup itself still completes (C56.4). `server.forwardedclientcert.require: true` now registers the middleware even when `enabled` was left false, so a programmatically-assembled config that skipped `config.Validate` stops serving unauthenticated traffic and starts returning 401 (C56.5). And `httpclient.Builder.Build()` now returns `(Client, error)` instead of `Client` — it fails construction, rather than warning, when a `WithTransport`/`WithTLSConfig`/`WithHTTPClient` composition would silently discard a client certificate, pinned roots, or a caller's transport (C56.6, ADR-044). Finally, re-declaring one queue name now merges compatible shapes instead of letting the last declaration overwrite the earlier one — so `DeclareQueueWithDLQ` and `DeclareQueue` on a single name compose rather than one silently dropping the other's dead-letter args — while incompatible shapes keep the first declaration and fail startup with one aggregate error naming every conflict (C56.7). Lastly, a JOSE-configured `httpclient` stops sealing requests that carry no body — it had been stamping a JWE over an empty payload plus `Content-Type: application/jose` onto every bodyless `GET`/`HEAD`/`DELETE`, which gateways drop — so a payload-free `POST`/`PUT`/`PATCH` now goes out unsealed too (C56.8). Separately, the never-implemented `cache.Manager` interface is deleted — nothing in go-bricks implemented or consumed it and `*CacheManager` never satisfied it, but a type in a consumer's own module could, so it is a compile-break for anyone who named the type (C56.9, ADR-045). And `GET /ready` stops discarding the cache probe's result: the 200 body now always carries `cache` and `cache_stats`, and the probe stops answering from the pool — it calls `Cache.Health(ctx)` on the leased instance, so a configured cache costs one Redis `PING` per poll and a live outage is finally visible (C56.10). That visibility is **strict by default**: the new `cache.critical` key (env `CACHE_CRITICAL`) is deliberately unregistered, so an absent key means the cache probe is critical and a cache-enabled service starts answering `503` on a Redis outage with no config change — `cache.critical: false` opts out and emits a startup WARN on every boot (C56.11, ADR-046). The cache `503`'s `error` field is sanitized to the fixed string `cache unavailable` rather than the connector error, which named the Redis host, port and resolved IP on an unauthenticated endpoint; the full error still reaches the app log and the debug health endpoint at `<debug.pathprefix>/health-debug` (default `/_sys`); the database body was left alone in this hop and is sanitized in turn by C57.1 (C56.12). Finally, a module that cannot work without a database can now say so by implementing `app.DatabaseRequirer`, which turns a required-but-absent database into a startup abort instead of a service that boots green — and a database-free service emits one advisory startup `WARN` as a backstop (C56.13). Lastly, the framework stops conflating an absent database with a misconfigured one: a partially delivered `database:` section now fails startup instead of loading silently and failing at first query (C56.14), while a genuinely database-free service — and every static multi-tenant deployment, which was also permanently 503 — reports `not_configured` / `per_tenant` and `/ready` returns 200 (C56.15, ADR-047).
- build-caught: C56.6 C56.9
- preflight: if a top-level cache is enabled **or** you pass an `app.Options.CacheConnector` — that connector never consults `cache.enabled`, so its probe is live and critical even with the cache disabled in config — and you have never set `cache.critical`, decide the readiness posture BEFORE the bump: keep the strict default and size `readinessProbe.failureThreshold`, or add `cache.critical: false` (C56.11)
- exit: `go get github.com/gaborage/go-bricks@v0.56.0 && go mod tidy && go build ./... && go test ./...`

### [C56.1] ALB forwarded-client-cert identity middleware · additive-optional

- note: `server.forwardedclientcert.*` (`enabled`, `require`) wires a config-gated
  middleware that parses ALB verify-mode `X-Amzn-Mtls-Clientcert-*` headers
  (`-Subject`, `-Issuer`, `-Serial-Number`, `-Leaf`) into a typed
  `server.ForwardedClientCert`, retrievable via
  `server.ForwardedClientCertFromContext`. Identification only, not
  authorization (ADR-039's stance). `require: true` rejects (401) when both
  `-Subject` and `-Serial-Number` are absent, and when any one of the four
  headers carries more than one value (that check runs first, so a duplicated
  `-Issuer` rejects even with a valid Subject and Serial-Number); a present
  Subject whose `-Leaf` fails to decode still passes (`Leaf == nil` + WARN).
  A duplicate is treated as absent identity without `require` too — fail open,
  never first-value-wins. Health/ready
  probes are always exempt. The all-zero default leaves the middleware
  unwired, byte-for-byte unchanged from prior behavior.
- security: `-Leaf` is percent-encoded by AWS with `+=/` left literal, so
  `url.PathUnescape` (not `url.QueryUnescape`, which corrupts a literal `+`
  into a space) is the decoder; the encoded value is capped at 64 KiB before
  any decode attempt. AWS does not publicly document that the ALB strips or
  overwrites client-supplied copies of these headers (verified 2026-07-27) —
  trust rests entirely on the deployment posture (mTLS-verify listener +
  closed security groups + single ingress path), never on an AWS
  sanitization guarantee; per-subject authorization on these headers is safe
  only where the trust store scopes a single partner CA. No in-app source-IP
  or `X-Forwarded-For` trust (F23 precedent).
- ref: ADR-043 · server/forwardedcert.go · wiki/forwarded_client_cert.md

### [C56.2] seal-payload CLI · additive-optional

- note: New installable `cmd/seal-payload` CLI (`go install
  github.com/gaborage/go-bricks/cmd/seal-payload@v0.56.0`) seals a JSON
  payload as a compact JWE-of-JWS token via the production `jose.Seal` path,
  for curl-testing jose-tagged endpoints. No config keys, no framework API
  change — `package main` contributes no importable symbols and is inert
  unless separately installed and invoked.
- ref: #776 · cmd/seal-payload/main.go · wiki/jose.md#sealing-test-payloads-with-curl-seal-payload-cli

### [C56.3] `logger.DefaultFilterConfig()` gains card-data + PII names — matching log values now emit `***` · silent-behavior · when: match

- detect: eleven needles are new — `cardholder`, `card_number`, `cardnumber`, `primary_account_number`, `cvv`, `cvc`, `track1`, `track2`, `track_data`, `iban`, `otp`. Find affected log fields with `git grep -nEi '"[^"]*(cardholder|card_?number|primary_account_number|cvv|cvc|track1|track2|track_data|iban|otp)[^"]*"' -- '*.go'`, then the same names in log-driven alerts, dashboard queries, and test assertions. Matching is case-insensitive substring, so `otp` also hits camelCase `…otP…`: `git grep -nE '"[A-Za-z]*[oO]t[pP][A-Za-z]*"' -- '*.go'` surfaces `snapshotPath`-style false positives.
- gate: match = you log a field whose name contains one of the needles AND you leave `app.Options.LoggerFilterConfig` unset — including YAML `log.sensitivefields` extenders, which merge *into* the widened defaults (`resolveLoggerFilterConfig`, `app/app_builder.go`). Those values now render as `***` with no compile error and no startup signal. no-match = you set a non-nil `Options.LoggerFilterConfig`; it replaces the list wholesale, so your masking is byte-identical and you do **not** pick up the new names.
- apply: nothing for the masking itself — that is the intended PCI posture. Repoint the downstream consumers of the old plaintext: alerts, dashboards, and tests that assert on those values. Rename any field caught as a false positive (`snapshotPath` under `otp`). If you replace the config via `Options.LoggerFilterConfig` and want the new coverage, build from `logger.DefaultFilterConfig()` and append rather than enumerating fields by hand — a bare `&logger.FilterConfig{SensitiveFields: ...}` also drops `password`, `token`, and every other default.
- verify: emit one log line per affected field in staging and confirm `***`; re-run the alert and dashboard queries against the new stream and confirm they still match what they are meant to match.
- ref: #827 · `logger/filter.go` (`DefaultFilterConfig`) · `app/app_builder.go` (`resolveLoggerFilterConfig`) · wiki/observability.md#sensitive-data-filtering

### [C56.4] `EnsureConsumers` returns on the caller's own context — a cold tenant's first messaging request fails fast instead of blocking · silent-behavior · when: match

- detect: no symbol changes to grep for — the signature is identical and `apidiff` is green. Find the exposure instead: `git grep -n 'deps.Messaging(' -- '*.go'` for handler-path resolution, and confirm any module implements `DeclareMessaging` at all — consumers are not required, since `EnsureConsumers` runs whenever declarations are non-nil, so a publisher-only module matches on its first request too. Then check whether any caller of a messaging-touching handler treats a `context.DeadlineExceeded` response as terminal rather than retrying.
- gate: match = multi-tenant deployment (`multitenant.enabled: true`) whose request handler resolves `deps.Messaging(ctx)` for a module with non-nil `DeclareMessaging` declarations — **consumers are not required**, publisher-only counts. Per-tenant messaging setup is lazy and has no pre-warm path, so it is first triggered by a real request under `server.timeout.middleware` (default 5s) while the setup itself is budgeted at 45s (`infraSetupTimeout`) — and config validation rejects `server.timeout.middleware >= server.timeout.write` (default 30s), so your request budget is structurally below the setup budget unless you deliberately raised `write` past 45s first. Assume you match. no-match = you never resolve messaging from a request handler — messaging touched only at boot and from scheduled jobs, which run on non-expiring contexts. Note single-tenant is NOT automatically no-match: `SingleTenantResourceProvider.Messaging` calls `EnsureConsumers` with the request context on every resolution. What spares it is that boot-time setup already installed the entry, so the call takes the warm fast path and never consults the caller's context. Publisher-only declarations are not no-match either during a cold start: neither provider gates on consumer presence, only on declarations being non-nil, so a publisher-only first touch still runs the same setup pass.
- apply: nothing in your code, and **the status code does not change**. Before this bump a cold tenant's first messaging request held the connection for up to 45s and then returned 503 anyway — the request-timeout middleware does not preempt the handler, it overwrites the handler's result with `ctx.Err()` once the deadline has passed, and `context.DeadlineExceeded` maps to 503. After the bump you get the same 503 in roughly `server.timeout.middleware` instead, the request handler's goroutine and its connection are freed — the shared setup goroutine keeps running in the background until the pass finishes, so a cold tenant still occupies one goroutine for up to `infraSetupTimeout`, and the setup still completes in the background so the tenant is warm for whoever asks next. `errors.Is(err, context.DeadlineExceeded)` matches through the new wrap, so existing deadline handling keeps working. The one thing to re-tune is **retry policy**: because failures now return fast while the background setup is still running, an aggressive retry sees several quick 503s over the same warm-up window where it previously saw one slow one. Cap retries or add backoff on the first per-tenant call if that count matters to your alerting.
- verify: cold-start a tenant and issue one request that resolves `deps.Messaging(ctx)`. Confirm it still returns 503 (`Request processing timed out`) but now at roughly `server.timeout.middleware` rather than tens of seconds later — latency is the only thing that should move. Then, BEFORE issuing a second request, confirm on the broker that every resource the tenant's declarations name is already present — exchanges, queues and consumers, each only when the declarations include them. That is the direct proof the abandoned setup ran to completion. A later request succeeding does not show this on its own: if the setup had been aborted, a fresh request would start its own and also succeed. As a second signal, that later request should return immediately rather than taking another setup's worth of latency, which is the warm fast path.
- ref: #835 · `messaging/manager.go` (`EnsureConsumers`) · `messaging/constants.go` (`infraSetupTimeout`) · `app/resource_provider.go`

### [C56.5] `forwardedclientcert.require: true` now registers the middleware even when `enabled` is false — a config-assembled service stops serving unauthenticated traffic · silent-behavior · when: match

- detect: `git grep -n 'app.NewWithConfig' -- '*.go'` to find programmatic config assembly, then in each config literal check the `ForwardedClientCert` block: `git grep -nA3 'ForwardedClientCert:' -- '*.go'` and look for `Require: true` without `Enabled: true` (an omitted `Enabled` is the zero value `false`, so absence counts). Match = at least one such literal reaches `app.NewWithConfig`.
- gate: match = you build a `*config.Config` in Go and hand it to `app.NewWithConfig`, with `ForwardedClientCert{Require: true}` and `Enabled` false or omitted. That path never calls `cfg.Validate()`, so nothing rejected the combination and the middleware was silently never registered — every request was served without a client-certificate check. no-match = you configure via YAML/env through `config.Load`, where `config.Validate` already rejects `require` without `enabled` at startup; you cannot have shipped this combination.
- apply: set `enabled: true` explicitly (`Enabled: true` in the Go literal) — your service was accepting unauthenticated requests and will now reject them with 401, which is the fix. The framework registers the middleware regardless and emits one startup WARN naming both keys, so the flip is never silent; setting `Enabled` clears the WARN. If you did **not** intend to require client-certificate identity, drop `Require: true` instead.
- verify: start the service and check startup output — the WARN is present only while `enabled` is still false. Then issue one request carrying no `X-Amzn-Mtls-Clientcert-*` headers and confirm 401 (before this bump it returned the handler's normal response). Health/ready probes stay exempt: confirm they still return 200, because ALB health checks present no client certificate.
- ref: ADR-043 · `server/middleware.go` (`setupIdentityMiddlewares`) · `config/validation.go` (`validateServerForwardedClientCert`) · wiki/forwarded_client_cert.md

### [C56.6] `Builder.Build` now returns `(Client, error)` — displacing compositions fail construction · compile-break · when: match

- detect: `git grep -nE 'httpclient\.NewBuilder\(' -- '*.go'` and `git grep -n 'httpclient\.Builder' -- '*.go'` as best-effort probes for the common import path. Both miss an import alias — `hc "github.com/gaborage/go-bricks/httpclient"` makes call sites read `hc.NewBuilder(...)`, matching neither grep — so a clean probe result is not proof you're unaffected. This is a compile-break atom: `go build ./...` at the hop's `exit` below is the authoritative check regardless of what these probes find.
- gate: match = you call `httpclient.NewBuilder(...).Build()` anywhere (under any import alias) — the return type changed from `Client` to `(Client, error)`. A call site that captures the result via single-value assignment (`client := builder.Build()`, or `client = builder.Build()` reassigning an existing single variable) fails to compile until updated, regardless of whether that builder chain would ever actually report an error. **Compiler detection covers single-value captures only.** Four shapes discard both return values, compile identically before and after this change, and are not flagged by `go vet` either — so they need manual review, not a build: a **bare** `builder.Build()` expression statement; a blank-identifier assignment `client, _ := builder.Build()`; `defer builder.Build()`; and `go builder.Build()`. (Verified by compiling all four against a two-return `Build`.) Treat `compile-break` on this atom as "mostly compiler-caught" — see verify below for the probes.
- apply: change `client := builder.Build()` to `client, err := builder.Build()` and **always handle `err`** — return it, wrap it, or `log.Fatal` at startup (treat it like any other fail-fast construction error per the manifesto). Do not discard it with `client, _ := builder.Build()`: that suppresses the fail-closed signal this entire hop exists to add, and leaves a nil `client` on the error path that panics on first use. (The framework's own `NewClient` discards it internally — that is a pinned, deliberately-unreachable special case for a bare `NewBuilder(log).Build()` chain with no `WithTransport`/`WithTLSConfig`/`WithHTTPClient` calls; it is not a pattern to copy into your own code.) If your builder chain combines `WithTransport`, `WithTLSConfig`, and/or `WithHTTPClient`, the new error is not just a formality: `Build` now returns it instead of logging a WARN when one of those calls would have silently discarded a client certificate, pinned roots, or a caller-supplied transport — fix the composition (see [wiki/httpclient.md](httpclient.md#transport-composition)) rather than only satisfying the compiler.
- verify: `go build ./... && go test -race ./...` enumerates every call site where the result is captured by assignment — but **not** a bare `builder.Build()` statement with no assignment, which compiles silently both before and after this change and simply discards a construction error that may now be non-nil. The same is true of a blank-identifier assignment: `client, _ := builder.Build()` compiles, and is the tempting way to silence the compile error, but it discards exactly the signal this hop adds and leaves a nil `client` that panics on first use. `defer builder.Build()` and `go builder.Build()` behave the same way — both are legal statements that drop both results. `go build ./...` alone is therefore not sufficient to find every affected call site. Three probes cover the four silent shapes — a bare statement (including a multi-line fluent chain whose last line is just `Build()`, which a `\.Build\(\)`-anchored pattern misses), a discarded error, and the `defer`/`go` forms:

```bash
git grep -nE '^[[:space:]]*[A-Za-z0-9_.]*\.?Build\(\)[[:space:]]*$' -- '*.go'
git grep -nE ',[[:space:]]*_[[:space:]]*:?=.*\.Build\(\)' -- '*.go'
git grep -nE '^[[:space:]]*(defer|go)[[:space:]]+.*\.Build\(\)' -- '*.go'
```

None of them is exhaustive — all three are line-oriented and blind to an import alias or an unusual layout — so treat them as probes, not proof; a `go/analysis`-based unused-result check (or `errcheck` configured for this symbol) is the only complete answer. Expect false positives in both directions: the first probe matches the terminating `Build()` line of *any* multi-line fluent chain, including one whose result is fully captured on the opening line and including builders that have nothing to do with `httpclient` (go-bricks' own `app.NewAppBuilder()` chain matches it). Read each hit rather than acting on the count: confirm it is an `httpclient` builder **and** that its result is genuinely discarded before touching it. Add an assignment plus real error handling only to the hits that clear both checks — editing a captured chain or an unrelated builder is a regression, not a migration. Once assignment-captured sites compile, a chain that previously logged a WARN now either builds successfully (composed) or returns a non-nil error (genuine discard) — confirm which one your chain hits.

- ref: ADR-044 · `httpclient/client.go` (`Build`) · `httpclient/tls.go` (`WithTLSConfig`)

### [C56.7] Re-declaring one queue name merges instead of overwriting — an incompatible pair now fails startup · silent-behavior · when: match

- detect: `git grep -nE 'DeclareQueue\(|DeclareQueueWithDLQ\(|RegisterQueue\(|DeclareConsumer\(' -- '*.go'` in your app, then read the hits for one queue **name** reached from two places. `DeclareConsumer(opts, queue)` is a declaration site too — its non-nil `queue` argument now always reaches `RegisterQueue` instead of being skipped when the name already existed. Two different modules is the usual shape, and it is exactly the case no single call site can see — so grep the names, do not trust a per-module reading. A name that appears once is not a match; a name that appears twice is a match only if the two shapes disagree (see gate). Nothing changes for a name declared once, which is the overwhelming majority.
- gate: match = one queue name declared twice where the two declarations disagree on any of the four flags (`Durable`, `AutoDelete`, `Exclusive`, `NoWait`), or where both set the same `Args` key to different values. no-match = every queue name is declared once, or the repeat declarations agree — including the common case where one call adds `Args` the other never sets, which merges cleanly. Note that a repeat declaration is no longer a silent no-op you can ignore: before this bump the later call replaced the earlier one wholesale, so an app could be *relying* on the overwrite without knowing it.
- apply: align the call sites so the two declarations agree — pick the intended flags and the intended value for the contested `Args` key, and make both call sites say it. The good news first: `DeclareQueueWithDLQ("orders.events.queue", nil)` and `DeclareQueue("orders.events.queue")` on one name now **compose** — their `Args` union reaches the broker — instead of whichever ran last silently dropping the other's dead-letter args. That silent drop is the bug this hop fixes: it reverted a queue to dropping failed deliveries with no error, no WARN, and an unchanged-looking topology. If your two declarations genuinely need different shapes, they are different queues — give them different names. Exchanges are unchanged and still last-write-wins. Bindings are unchanged too, but they were never last-write-wins: `RegisterBinding` appends, so two declarations of the same queue/exchange/routing-key have always both survived and both replayed to the broker.
- verify: boot the app and confirm startup does not abort with `declaration validation failed: conflicting queue declarations` — the error names every conflicting queue, the field or `Args["<key>"]` at fault, and both values labeled by which one is in effect (`Durable kept "true" vs rejected "false"`, `Args["x-dead-letter-exchange"] kept "orders.dlx" vs rejected ""`), so one boot enumerates all of them rather than one per restart. Then confirm on the broker that the queue carries the `x-dead-letter-exchange` you expect (`rabbitmqctl list_queues name arguments`, or the management UI's queue detail): a queue that used to lose its dead-letter args to an overwrite now keeps them, and that argument reaching the broker is the direct proof the merge ran. A queue whose arguments are unchanged from before the bump was never affected.
- ref: `messaging/declarations.go` (`RegisterQueue`, `Validate`) · [wiki/messaging.md](messaging.md#helper-functions-for-simplified-declarations)

### [C56.8] A JOSE-configured httpclient no longer seals requests that carry no body · silent-behavior · when: match

- detect: `git grep -n 'WithJOSE(' -- '*.go'` to find the clients, then read every request built from one that carries **no payload**, whatever the method — `client.Get(ctx, url)`, `client.Post(ctx, url, nil)`, a `http.NewRequest(m, url, nil)`, or any call passing `http.NoBody`. `git grep -nE '\.(Get|Head|Delete|Post|Put|Patch|Do)\(|http\.NewRequest|http\.NoBody' -- '*.go'` enumerates the send paths — the convenience verbs are not the only way in, so it also catches `Do`, both `NewRequest` constructors, and a directly-passed `http.NoBody`. Treat it as best-effort: a request built by your own wrapper, or handed over as a struct literal, will not match, so trace those by hand. Do not skip `GET`/`HEAD`/`DELETE`: those were the shapes this hop stopped sealing, so a peer that accepted a sealed bodyless `GET` is affected exactly like a mutating one — it was simply also the shape gateways were already dropping, which is why the bug surfaced there first.
- gate: match = you issue a payload-free request of any method through a `WithJOSE` client to an endpoint that requires `application/jose` on that request. Decide it per bodyless call site, not per client: a peer can accept a bare `GET` on a status endpoint and still demand JOSE on a payload-free `POST`, or demand it only on its protected routes. A payload-free `POST`/`PUT`/`PATCH` is the likeliest match, because a sealed bodyless `GET`/`HEAD`/`DELETE` was already being dropped by most gateways. no-match = the client has no bodyless call site at all, or every bodyless call site targets an endpoint that accepts a bare request. A client whose `POST`s all carry payloads is not automatically clear — one payload-free `GET` is enough to match. Note the rule is **body presence, not method**: a `POST` with a body is sealed exactly as before, and nothing about the sealed shape changed.
- apply: for a `POST`/`PUT`/`PATCH`, give the request an explicit body so the seal still runs and the `application/jose` Content-Type is still set. Use the smallest payload the peer's schema accepts — `{}` works only where the peer tolerates an empty object, and sending it to an endpoint that validates a schema trades a 415 for a 400. Do **not** do that for a `GET`/`HEAD`/`DELETE`: a body on those is what gateways, CDNs and ALBs drop, so adding one to satisfy the peer reintroduces the defect this hop fixes — that combination is a contradictory contract and needs the peer to accept a bare request. Either way, do not work around it by re-sealing empty payloads at the call site. A partner that genuinely requires sealed bodyless requests needs an opt-in `SealBodyless` flag on `JOSETransport` — file that rather than reverting the guard. **Security note:** giving the request a body restores the Content-Type, not an authentication guarantee. `jose.Seal` signs the payload verbatim and injects no `iat`, `jti`, or request binding, so a JWS over `{}` attests only that someone holding the signing key signed two bytes — it is replayable against any endpoint. If your peer relies on the JWS to authenticate the caller, put `iat` and `jti` in that body and pair them with the peer's replay window.
- verify: point the client at the peer's action endpoint and confirm the payload-free call is not answered `415`. On the wire (or in an interceptor) a request you intend to be bodyless should carry no `Content-Type: application/jose` and either no `Content-Length` header at all or `Content-Length: 0` — which of the two net/http puts on the wire varies with the method and protocol version, so check the actual request rather than assuming one form — while a request you have given a body should carry the sealed compact JWE. The response direction needs no check: a JOSE-typed reply that carries a body is still decrypted and verified, and a reply that RFC 9110 says carries none (`1xx`, `204`, `304`, any answer to `HEAD`) now passes through instead of failing `JOSE_MALFORMED` — strictly more permissive, so nothing that worked before can break.
- ref: `httpclient/jose_transport.go` (`wrapRequest`, `unwrapResponse`) · [wiki/jose.md](jose.md)

### [C56.9] The `cache.Manager` interface is removed · compile-break · when: match

- detect: `git grep -nE 'cache\.Manager([^A-Za-z0-9_]|$)' -- '*.go'` — the explicit character class is deliberate: **do not write `\b` in a `git grep -E` pattern.** Git's POSIX-ERE engine has no word-boundary escape, so it strips the backslash and matches a literal `b` — `'Allow\b'` matches `Allowb` and *not* `Allow,`. A `\b` detect therefore reports "not affected" for essentially every consumer, silently. See the PCRE-escape note under step 4 of the runbook protocol above
- scope: only code that *names the interface type* — a parameter, field, local, type assertion, or a `var _ cache.Manager = ...` assertion. Callers of the concrete `*cache.CacheManager` are unaffected, as are apps on `deps.Cache(ctx)` / `ResourceProvider`. Nothing inside go-bricks ever implemented or consumed it, and `*CacheManager` never satisfied it either (its `Stats()` returns `ManagerStats`, not `map[string]any` — see `after`), so no in-tree assertion existed to break. A type in **your** module could still satisfy it, which is why this is a `match` gate and not a no-op
- gate: match = the detect returns ≥1 line. no-match = no Go file names the type. Treat the grep as a probe, not proof — it is line-oriented and blind to an import alias or a dot-import (`import c ".../cache"` then `c.Manager`). the compiler is the authoritative answer for a compile-break atom, since it resolves references regardless of how the package was imported. Use `go build ./... && go test ./...` — `go build` alone skips `_test.go` files, so a test-only reference survives it; and either way the answer covers only the files your active build tags select
- before:

  ```go
  func NewService(m cache.Manager) *Service { ... }
  ```

- after:

  ```go
  // Depend on the concrete manager…
  func NewService(m *cache.CacheManager) *Service { ... }

  // …or declare the narrow interface you actually need, on the consumer side
  // (the Go convention: interfaces belong to the consumer, not the producer).
  type cacheGetter interface {
      Get(ctx context.Context, key string) (cache.Cache, cache.ReleaseFunc, error)
  }

  func NewService(m cacheGetter) *Service { ... }
  ```

  Exactly one difference made `*CacheManager` unable to satisfy the deleted interface: `Stats()` returns `cache.ManagerStats`, where the interface demanded `map[string]any`. So if you wrote your own narrow interface, do not copy the old `Stats()` signature. Two further differences do **not** affect satisfaction but are worth knowing when you write that interface: the concrete `Get`'s second parameter is named `key`, not `tenantID` (parameter names never participate in satisfaction — it is the cache key, which is the tenant ID only in multi-tenant deployments), and `*CacheManager` also offers `Remove(key string) error`, which the interface never declared (extra methods are always fine)
- verify: `go build ./... && go test ./...`
- ref: ADR-045 · `cache/types.go` · `cache/manager.go` (`CacheManager`) · #862

### [C56.10] `/ready` reports cache health — two new body keys, and a Redis `PING` per poll where a cache is configured · silent-behavior · when: always

- detect: `git grep -n '"/ready"' -- '*_test.go'` and `git grep -rn '/ready' --` across
  dashboards, synthetic checks, and contract fixtures. You are looking for two things:
  (a) anything that pins the *exact* key set of the 200 body rather than reading individual
  keys — `assert.Len(body, N)`, a golden JSON file, a schema with
  `additionalProperties: false`, or a dashboard widget that enumerates the response object;
  (b) the poll period of every readiness probe definition (Kubernetes `readinessProbe`,
  ALB/NLB target-group health checks, synthetic monitors), tight enough that one added
  Redis round trip per poll is worth accounting for.
- gate: always. The two new keys appear whether or not you set `cache.critical`, and
  whether or not you configure a cache at all — with `cache.enabled: false` the body
  reports `"cache": "not_configured"` and `cache_stats` still carries the manager
  counters; `"cache": "disabled"` with `"cache_stats": {}` appears only when the cache
  manager failed to construct at startup. Nothing here is opt-in. The added round trip
  needs `cache.enabled: true`: a not-configured lease fails before any ping, so that
  deployment sees no extra traffic and no status change.
- apply: add `cache` and `cache_stats` to any pinned key set, then know what the probe now
  means. `GET /ready` used to enumerate only `database` and `messaging`: the cache probe
  ran, its result was stored, then discarded — so a dead cache still read `ready`, and only
  the IP-allowlisted `GET /_sys/health-debug` endpoint surfaced the probe's result at all
  (#860) — and only where `debug.enabled: true` (default `false`); the path follows
  `debug.pathprefix`, default `/_sys`.
  That probe only leased an instance from the manager, and the manager returns a pooled
  instance without any network traffic, so a Redis outage that began after the instance was
  built read `healthy` forever — `/health-debug` exposed a closed manager, never an outage.
  The 200 body now carries `cache` (a status string) and `cache_stats` (the manager
  counters) alongside the existing `database`/`db_stats` and `messaging`/`messaging_stats`
  pairs, and the probe calls `Cache.Health(ctx)` on the leased instance, matching what the
  database probe has always done — under the default Redis connector that is one `PING` per
  `/ready` call, documented on the `Cache` interface as fast (<100ms) and safe to call
  frequently. The cost is conditional, not flat: a `disabled` or `not_configured` deployment
  pays none of it (the first registers no probe, the second fails the lease before any ping),
  and a custom `Options.CacheConnector` supplies its own `Health`, so it need not issue a
  Redis `PING` and need not emit the `db.client.operation.duration` sample below. That `PING`
  runs under the
  request's own deadline, capped at 500ms independent of `server.timeout.middleware` and
  `cache.redis.readtimeout`, so a hung Redis is reported (a connection error wrapping
  `context deadline exceeded`) rather than draining the request budget. The cap covers the
  warm poll only: a cold poll — the first after boot, or after an idle eviction — first builds
  the instance, paying that instance's own 5s construction `PING` plus an `INFO` version check
  before spending the 500ms, so it can run several seconds and issue two `PING`s. Size
  `initialDelaySeconds`/`timeoutSeconds` for that, not for 500ms. Status codes change too,
  and by default: unless you set `cache.critical: false` (C56.11), that live outage answers
  `503` rather than showing as `"cache": "unhealthy"` inside a `200`. Under the default
  connector a warm poll also emits one
  `db.client.operation.duration` sample from inside the Redis client (tagged `error.type`
  during an outage), so warm-pool outages now reach cache dashboards — adjust those before the
  bump. A failed lease emits no cache metric at all (the construction `PING` is untracked), so
  do not build the boot-time alert on one; see [wiki/cache.md](cache.md#readiness).
- verify: `curl -s localhost:8080/ready | jq 'keys'` and confirm `cache` and `cache_stats`
  are present — on a `200`; a critical database failure short-circuits `/ready` before the
  cache probe's result is rendered, so check this against a healthy database. Then re-run
  whatever test or dashboard your `detect` surfaced. Then, with `cache.critical: false` set
  (the C56.11 opt-out), stop Redis against a running pod (or block its port) and
  `curl -s localhost:8080/ready | jq '.cache, .cache_stats.status'` — both read `unhealthy`
  within one poll, where before the bump they stayed `healthy` for as long as the instance
  stayed pooled. Under the strict default the same outage answers `503` with a
  `{status, cache, error}` body that carries no `cache_stats`, so check `.cache` alone there.
- ref: #860 · `app/health.go` (`cacheManagerHealthProbe`) · `app/lifecycle.go` (`readyCheck`) · `cache/redis/client.go` (`Health`) · [wiki/cache.md](cache.md#readiness)

### [C56.11] a failing cache now fails `/ready` with `503` by default (`cache.critical`) · silent-behavior · when: no-match

- detect: two questions, in order, both answered from the **top-level** `cache:` block —
  print it once with `git grep -n -A10 '^cache:' -- '*.yaml' '*.yml'` and read the two keys
  out of what it prints. The column-0 anchor is the whole point: a bare `enabled:`/`critical:`
  grep also hits `server:`, `observability:` and the per-tenant
  `multitenant.tenants.<id>.cache:` blocks, and only the top-level connection is what the
  probe leases. (Use `[[:space:]]`, never `\s`, if you write your own pattern — `git grep -E`
  is POSIX ERE and silently matches a literal `s` instead, so the command returns nothing
  and a clean result is indistinguishable from a real one.) **(a) Is a top-level cache
  enabled?** `enabled: true` in that block, plus `CACHE_ENABLED` across shell profiles,
  `.env` files, compose files, and deployment manifests (`git grep -rn 'CACHE_ENABLED'` ;
  `grep -rn 'CACHE_ENABLED' k8s/ deploy/ .env* 2>/dev/null`), plus any Go-assembled
  `config.Config` setting `Cache.Enabled` (`git grep -n 'Cache\.Enabled' -- '*.go'`). A
  custom `Options.CacheConnector` (`git grep -n 'CacheConnector' -- '*.go'`) is *also* a
  match regardless of `cache.enabled` — it never consults that key, so its probe is live.
  **(b) Is `cache.critical` explicitly set?** `critical:` in that same block,
  `git grep -rn 'CACHE_CRITICAL'` across the same env sources, and `git grep -n
  'Cache\.Critical' -- '*.go'`. The greps shortlist files, not the effective configuration:
  environment variables override `config.<env>.yaml`, which overrides `config.yaml`, so
  where the sources disagree the answer is the parsed config the service actually boots with.
  **You are affected when (a) matches and (b) returns nothing.** Finding nothing in (b) is
  the actionable result, not the all-clear.
- gate: no-match on (b) = you are on the pre-bump behaviour, where the cache probe's result
  never reached the response at all (C56.10), so a failing cache could not change the status
  code. `cache.critical` does not exist in v0.55.0 — it arrives in this hop alongside the
  probe change, which is why finding nothing in (b) is the actionable result rather than the
  all-clear. The key is registered as no default at all: absent means **critical**, so a live
  Redis outage answers `/ready` with `503 {"status":"not ready","cache":"unhealthy","error":"cache
  unavailable"}` (no `cache_stats`, no other component's status — see C56.12 for the body)
  instead of a `200` carrying `"cache": "unhealthy"`. Combined with C56.10 — the probe now
  issues a real `PING` per poll rather than answering from the pool — this means an outage
  that begins after boot is detected *and* acted on, where before the bump it was neither.
  Every replica sharing one Redis fails readiness at the same moment, so a blip can drain
  the whole Deployment from rotation and stall a rollout; size
  `readinessProbe.failureThreshold` (see `apply`). Paths that report no error still never
  `503` under either setting: with the default Redis connector `cache.enabled: false`
  reports `not_configured` (the probe ran, the lease declined, the error is nil), and a
  manager that failed to construct registers no probe at all and reports `disabled`. A
  custom `Options.CacheConnector` ignores `cache.enabled` entirely, so it is probed — and
  critical — even with the cache disabled in config. The flag remains process-global and observes
  only the top-level `cache.*` connection, so it is inert for caches living under
  `multitenant.tenants.<id>.cache`. Database criticality (always) and messaging criticality
  (never, still no knob) are unchanged. A failing readiness probe never restarts a
  container — only liveness does, and `/health` (the correct liveness target) is unchanged.
- apply: **keep the strict default** if the service cannot serve correct results without the
  cache — a rate limiter, session store, or idempotency ledger — and set
  `readinessProbe.failureThreshold: 3` (with `periodSeconds: 10`) so a transient blip has to
  persist across three polls before the pod leaves the Service endpoints. **Opt out** if the
  cache is an optimisation in front of a database that can absorb the miss: add one line —

  ```yaml
  cache:
    critical: false
  ```

  or `CACHE_CRITICAL=false` — and note that this is now **loud**: an enabled cache with an
  explicit `false` emits a startup WARN on every boot naming the key, the consequence
  (`/ready` keeps answering `200` while the cache is down, so a dead cache still reports
  ready) and the remedy. That WARN is intentional and is not suppressible; it is the visible
  marker of a deliberately weakened readiness posture. Do **not** work around the flip by
  repointing `readinessProbe` at `/health` — `/health` checks no dependency at all, so that
  silences the database probe too and hides the change from config review.
- verify: boot with an enabled cache and no `cache.critical`, stop Redis (or block its
  port), then `curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/ready` — it reads
  `503` within one poll, and `curl -s localhost:8080/ready | jq '.status, .cache, .error'`
  reads `"not ready"`, `"unhealthy"`, `"cache unavailable"`. Restart Redis and confirm it
  returns to `200`. If you opted out, the same outage keeps answering `200` with
  `.cache == "unhealthy"`, and startup logs the `cache.critical is explicitly false` WARN —
  grep the boot log for it to confirm the opt-out was actually parsed (a typo'd key leaves
  you on the strict default with no WARN). Check this against a healthy database either
  way: a critical database failure short-circuits `/ready` before the cache probe runs.
- ref: #860 · ADR-046 · `config/types.go` (`CacheConfig.Critical`) · `config/config.go`
  (`Config.IsCacheCritical`) · `app/app_builder.go` (`warnIfCacheCriticalityOptOut`) ·
  [wiki/cache.md](cache.md#readiness)

### [C56.12] the cache `503` body no longer carries the connector error · silent-behavior · when: match

- detect: `git grep -rn '/ready' --` across alert rules, runbooks, synthetic checks, log
  pipelines, and contract tests, looking for anything that reads the `error` field of a
  `503` response and expects a Redis address or a dial error in it — a regex
  over the body, an alert annotation templating it, or a test asserting
  `assert.Contains(body["error"], "<host>")`. Match = one of those consumers exists. Only
  the **cache** probe's `503` is affected.
- gate: match = your consumer now reads the fixed string `cache unavailable`. `/ready`
  carries no IP allowlist and no authentication, and the cache probe's error renders the
  Redis host and port and the resolved dial IP (no tenant identity: the probe leases the
  empty top-level key). Since C56.11 makes the `503` path default-on for
  every cache-enabled service, that disclosure would otherwise become shipped default
  behavior, so the cache probe now declares a sanitized public string that `readyCheck`
  emits in its place. The **database** and **messaging** `503` bodies are byte-identical to
  before *in this hop* — the sanitization is per-probe, not a rewrite of the shared branch.
  If you are landing on v0.57.0 or later, the database body changes too: see `[C57.1]`.
- apply: repoint the consumer at one of the two channels that still carry the full error.
  `readyCheck` logs it at ERROR on every `503` with a `component` field
  (`Readiness check failed`), which is the channel to alert on. The debug health endpoint
  still renders it verbatim in `data.components.cache.error` — but only where
  `debug.enabled: true` (default `false`) and `debug.endpoints.health: true` (default
  `true`), and it lives at `<debug.pathprefix>/health-debug` (`debug.pathprefix` defaults to
  `/_sys`; the debug group registers at the URL root, so `server.path.base` does **not**
  prefix it, unlike `/ready`). Reaching it means satisfying `debug.allowedips` — default
  loopback (`127.0.0.1`, `::1`) — and sending `Authorization: Bearer <debug.bearertoken>`
  when that key is set. Clearing `allowedips` turns the middleware into a pass-through,
  which the framework WARNs about separately when no `debug.bearertoken` is set either. Do
  not reconstruct the address by parsing the log line if the config already tells you which
  Redis the service dials.
- verify: with an enabled cache and Redis stopped, `curl -s localhost:8080/ready | jq -r
  '.error'` prints exactly `cache unavailable` and contains no host, port, or IP. The
  application log for the same request carries the full
  `cache connection error: ping failed for <host>:<port>: …` string. For the debug channel,
  build the URL from your own settings rather than assuming the defaults — with
  `debug.enabled: true`, `debug.endpoints.health` left at `true`, the request coming from an
  address inside `debug.allowedips`, and `PREFIX` set to `debug.pathprefix` (`/_sys` unless
  you changed it):
  `curl -s -H "Authorization: Bearer $DEBUG_TOKEN" "localhost:8080$PREFIX/health-debug" | jq
  -r '.data.components.cache.error'` prints it too (drop the header when
  `debug.bearertoken` is empty).
- ref: #860 · ADR-046 · `app/health.go` (`cacheUnavailableMessage`, `HealthStatus.PublicErr`) ·
  `app/lifecycle.go` (`publicProbeError`, `readyCheck`) · [wiki/cache.md](cache.md#readiness)

### [C56.13] Modules can declare `DatabaseRequirer` — a required-but-absent database aborts startup · additive-optional

- note: A module that cannot function without a database may now implement
  `app.DatabaseRequirer` (`RequiresDatabase() bool`). `ModuleRegistry.Register`
  evaluates the declaration **before** calling `Init` and returns an error — which the
  framework treats as fatal — when no database is configured. This closes a gap that
  no amount of config inspection can: an empty `database.*` section is byte-identical
  whether the service is deliberately database-free or its configuration failed to
  reach the process (a dropped secret mount), so only the module can supply the
  missing intent. Implementing the interface is not itself the declaration —
  `RequiresDatabase` may return `false`, so a module can gate the requirement on its own
  construction-time config. Three deployment modes are exempt, because they resolve
  database config at runtime rather than from the root block: multi-tenant (config
  validation rejects a root block alongside static tenants), a dynamic config source
  (`source.type: dynamic`), and a caller-supplied `Options.ResourceSource` that reports
  `IsDynamic()`. Declaring nothing leaves behavior byte-for-byte unchanged, so this is
  adopt-only. The abort is a `*config.ConfigError` in the `missing` category — not
  `not_configured`, so the framework's own skip-and-degrade idiom
  (`config.IsNotConfigured`) cannot swallow it.
- note: A database-free service now also emits one startup `WARN`
  (`No database configured - …`) as a backstop for modules that declare nothing. It is
  advisory: silence it by configuring a database, or ignore it on a service that is
  intentionally database-free. The three exempt modes above never emit it.
- after:

  ```go
  // In the module that owns database-backed work:
  func (m *Module) RequiresDatabase() bool { return true }
  ```

- verify: `go build ./... && go test ./...`
- ref: `app/module.go` (`DatabaseRequirer`) · `app/module_registry.go` (`checkDatabaseRequirement`) · `app/bootstrap.go` (`rootDatabaseAbsent`, `warnIfDatabaseAbsent`) · #872

### [C56.14] A partially configured database now fails startup · breaking · when: match

- detect: `git grep -nE 'DATABASE_(TYPE|HOST|PORT|DATABASE|USERNAME|PASSWORD|CONNECTIONSTRING|ORACLE_SERVICE_(NAME|SID))' -- '*.yaml' '*.yml' '*.env' 'Dockerfile*' 'deploy/'` and the equivalent for your deployment manifests, then check whether any environment that sets one of those carries a COMPLETE section. `type` + `host` is not enough: `validateDatabaseCoreFields` also requires `port` and `username`, plus a target — `database`, or for Oracle `oracle.service.name` / `oracle.service.sid`. A `connectionstring` still needs a `type` to dispatch on (#877). The explicit character class habit applies: **never write `\b` in a `git grep -E` pattern** — Git's POSIX-ERE engine strips the backslash and matches a literal `b`, so the detect silently reports "not affected"
- scope: `config.IsDatabaseConfigured` widened from three fields (`connectionstring`, `host`, `type`) to every connection-identity field, adding `port`, `database`, `username`, `password`, and Oracle's `oracle.service.name` / `oracle.service.sid` (Oracle names its target with those rather than a database name). Any one of them now marks the section as intended, which routes it into `validateDatabase`. A config that set, say, `database.database` + `database.username` with no type or host used to load silently and then fail at first query; it now fails `config.Load` with `config_invalid: database.type '' is not supported`. Fields filled in by `applyDatabasePoolDefaults` (`timezone`, `pool.*`, `query.*`) are deliberately excluded, so the verdict is identical before and after defaulting — a defaulted config never reads as intent
- gate: match = some environment sets a database identity field without completing the section (see the detect for what complete means). no-match = every environment either configures the database fully or sets no `database.*` identity field at all. Caveat: a field delivered as an *empty string* (an empty `secretKeyRef`, `envsubst` over an unset variable) still reads as absence — the predicate cannot distinguish it from an unset field
- before:

  ```yaml
  database:
    database: appdb
    username: app        # loaded fine; failed at first query
  ```

- after:

  ```yaml
  database:
    type: postgresql     # complete the section: type + host + port + username + target
    host: db.internal
    port: 5432
    database: appdb      # Oracle instead: oracle.service.name or oracle.service.sid
    username: app
  ```

  …or remove the `database:` block entirely if the service genuinely has no database. Partial is the one thing that is no longer accepted
- verify: `config.Load()` succeeds for every environment you deploy
- ref: ADR-047 · `config/validation.go` (`IsDatabaseConfigured`) · #872

### [C56.15] An unconfigured database reports not_configured — `/ready` flips 503 → 200 · silent-behavior · when: match

- detect: `git grep -nE 'database.*(not ready|503)' -- '*.go' '*.yaml'` over your own probes and alerts, plus any alert rule or synthetic check that asserts `/ready` returns 503 for a database-free or multi-tenant service
- scope: three deployments change readiness. (1) A service with **no** `database:` block at all: `/ready` returned a permanent 503 with `unsupported database type: ` and now returns 200 with `database: "not_configured"`. (2) Every **static multi-tenant** deployment: the probe resolves the fixed `""` key, and validation rejects a root block alongside static tenants, so that key could never resolve and these were *also* permanently 503 even when every tenant was healthy — they now report a new `per_tenant` status. Note the consequence: where the `""` key does not resolve, multi-tenant `/ready` carries no database signal at all — no critical probe, no startup gate, no WARN. A multi-tenant deployment that *does* configure a root block (a shared-ledger control plane, `outbox.tenancy: shared`) is still probed and still gates readiness. (3) `/_debug/health` `overall_status` counts `not_configured`, `disabled`, and `per_tenant` as healthy, so it stops reporting `critical` for a service `/ready` calls ready. A configured-but-unreachable database is unchanged: still `critical: true`, still 503. `deps.DB(ctx)` on a database-free service now returns an error satisfying `config.IsNotConfigured`, and the spurious pre-warm WARN pair disappears
- gate: match = you deploy a database-free or multi-tenant service, or you alert on `/ready` status codes. no-match = every service configures a root database
- before:

  ```json
  {"database":"unhealthy","error":"failed to create database connection for key : unsupported database type:  (supported: postgresql, oracle)","status":"not ready"}
  ```

- after:

  ```json
  {"status":"ready","database":"not_configured","db_stats":{"status":"not_configured"}}
  ```

  (database keys only — the body also carries `messaging`, `cache` and their stats per C56.10.)
  A rollout that was previously held back by a failing readiness gate will now proceed. If that 503 was load-bearing for you — i.e. the service genuinely needs a database — implement `app.DatabaseRequirer` (C56.13) so the absence aborts startup instead
- verify: `curl -s -o /dev/null -w '%{http_code}' localhost:8080/ready`
- ref: ADR-047 · `config/tenant_store.go` (`DBConfig`) · `app/health.go` · `app/debug_health.go` · #872

## E57 · v0.56.0 → v0.57.0 — `/ready` stops disclosing internals in either body + outbox/inbox startup database verification

- gist: The database readiness probe now declares a sanitized public string, so `/ready`'s
  `503` body reports `database unavailable` instead of the driver's error. That error named
  the connection identity — pgconn renders ``failed to connect to `user=<username>
  database=<dbname>`: <host>:<port>`` and redacts only the password — on an endpoint that
  is unauthenticated and carries no IP allowlist by design, so any caller who could reach
  the port harvested it during any database outage. The full error is unchanged on both
  channels operators own: the application log and — where debug is enabled and
  access-controlled (see `apply:`, that endpoint's protection is conditional) —
  `<debug.pathprefix>/health-debug`. This reuses the seam ADR-046 built for the cache probe
  (`HealthStatus.PublicErr`, C56.12), so it requires no new configuration — but it is not
  action-free: any alert, runbook, synthetic check, or contract test that reads that
  `error` field must stop parsing the driver error and adopt the sanitized response
  (C57.1). The same hop then inverts the seam itself: sanitization is the default for
  **every** critical probe rather than something each one opts into, so a critical
  `Prober` you wrote yourself now serves `<Name> unavailable` instead of its raw error
  (C57.2). For the framework's own probes that flip emits identical bytes. The hop then
  closes the same disclosure on the **200** body, which is the one actually polled:
  `db_stats.connections` carried one entry per live pooled connection, keyed by the
  resourcepool key — the tenant ID in a multi-tenant deployment — with `last_used` and
  `idle_duration` alongside it, so an unauthenticated caller read a live tenant enumeration
  and per-tenant timing. That array is now withheld from `/ready`; the scalar counters stay,
  and the per-key detail is unchanged on `<debug.pathprefix>/health-debug` (C57.3). Separately,
  an enabled outbox or inbox now verifies at `Init` that its ledger database and table are
  actually usable — previously `outbox.enabled: true`/`inbox.enabled: true` with no database
  configured (or an unreachable one, or a missing ledger table) booted green and only failed
  once per poll interval, forever, with a relay log line that misleadingly read
  `database (optional)`. The check runs the read/write the relay or cleanup job already performs
  every cycle (the outbox's `FetchPending(…, 1)`; the inbox's `DeleteProcessed` before the Unix
  epoch, which matches no row) against the same database and table, so `Init` now fails fast
  with an actionable error instead. It is a reachability-and-table check, not a full capability
  check: the outbox probe covers exactly what the relay reads, but the inbox probe is the
  *cleanup* job's `DELETE`, so it does not prove `ProcessOnce`'s `INSERT` — a role holding
  `DELETE` but not `INSERT` still passes `Init` and fails at the first processed event. Per-tenant fan-out and `source.type: dynamic` deployments are
  unaffected — those resolve their database at runtime, not at `Init`. A custom dynamic
  `Options.ResourceSource` behind a *static* `source.type` is not among them: a module cannot see
  that resource source, so such a deployment is probed (C57.4). Separately, a
  `database.connectionstring` with no `database.type` passed `config.Validate` and then
  could never connect — `database.NewConnection` dispatches solely on `type` and errored
  only at first query. `config.Validate` now infers `type` from a recognized DSN scheme
  (`postgres://`/`postgresql://` → `postgresql`, `oracle://` → `oracle`) when `type` is
  empty, and rejects an explicit `type` that conflicts with the inferred scheme. For the
  built-in connector, any other scheme left untyped now fails startup instead of booting
  into a dead database; a caller-supplied `Options.DatabaseConnector` is exempt (C57.5).
  Finally, debug endpoints refuse to register without access control: `debug.enabled: true` exposing at least one endpoint with neither `debug.allowedips` nor `debug.bearertoken` set now aborts startup instead of registering behind a pass-through middleware and a WARN (C57.7, ADR-049).
  And the hop closes the messaging counterpart of the same fail-fast argument: a
  single-tenant service whose declared consumers could not be started logged one WARN and
  carried on, and since nothing retried and the messaging readiness probe is not critical,
  it passed `/ready` with 200 and consumed zero messages for the life of the pod — a silent
  total outage for a consumer service. That failure is now fatal. The grade is scoped to
  services that actually declared consumers: consumer setup still runs unconditionally
  (it is what declares exchanges, queues, and bindings), so publisher-only services — and
  every service with no messaging configured, which reaches the same call with an empty
  declaration set and an unresolvable broker URL — keep warn-and-continue (C57.8).
  Lastly, `httpclient.Builder.Build()` extends its ADR-044 fail-closed posture to JOSE:
  it fills a policy's unset algorithms from the `jose` defaults and runs
  `jose.Policy.Validate` — the check the server's tag scanner has always run at
  registration — so a disallowed algorithm, direction-mismatched kids, or a nil
  `Resolver` fails construction rather than every request. `jose.Seal` now runs the
  allowlist itself as well, closing the gap where an httpclient-built policy could emit
  a JWE wrapped with `RSA1_5` or a non-AEAD content encryption — shapes go-jose sealed
  happily, below the framework's own floor. Kid *resolution* stays per-request, so a
  lazily-loaded keystore is not forced open at construction (C57.9).
- build-caught: none
- exit: `go get github.com/gaborage/go-bricks@v0.57.0 && go mod tidy && go build ./... && go test ./...`

### [C57.1] the database `503` body no longer carries the driver error · silent-behavior · when: match

- detect: `git grep -rn '/ready' --` across alert rules, runbooks, synthetic checks, log
  pipelines, and contract tests, looking for anything that reads the `error` field of a
  `503` response and expects driver text in it — a regex over the body, an alert annotation
  templating it, or a test asserting `assert.Contains(body["error"], "<dbname>")`. Match =
  one of those consumers exists. Only the **database** probe's `503` is affected; the cache
  probe was already sanitized in C56.12, and the messaging probe is never critical, so its
  errors never render into a `503` body at all.
- gate: match = your consumer now reads the fixed string `database unavailable`. `/ready`
  has no authentication and no IP allowlist — it must stay reachable by load balancers —
  and the database probe's error carries the connection identity the driver puts in it.
  For PostgreSQL that is pgconn's ``failed to connect to `user=<username>
  database=<dbname>`: <host>:<port> (<resolved-ip>)`` prefix: the password is redacted,
  the username, database name and resolved internal address are not. A fixed public string
  is emitted in its place, exactly as for the cache probe since C56.12 — as of C57.2, later
  in this same hop, neither probe declares that string itself; `publicProbeError`
  synthesizes it. no-match = nothing parses that field; the change is invisible.
- before:

  ```json
  {"status":"not ready","database":"unhealthy","error":"failed to connect to `user=app database=payments`: 10.0.0.5:5432 (10.0.0.5): dial error"}
  ```

- after:

  ```json
  {"status":"not ready","database":"unhealthy","error":"database unavailable"}
  ```

- apply: repoint the consumer at one of the two channels that still carry the full error.
  `readyCheck` logs it on every `503` with a `component` field
  (`Readiness check failed`) — that is the channel to alert on, and it is where the
  identity still lives. The debug health endpoint renders it verbatim in
  `data.components.database.error`, but only where `debug.enabled: true` (default `false`)
  and `debug.endpoints.health: true` (default `true`), at `<debug.pathprefix>/health-debug`
  (`debug.pathprefix` defaults to `/_sys`; the debug group registers at the URL root, so
  `server.path.base` does **not** prefix it, unlike `/ready`). **That endpoint is
  access-controlled only conditionally**: the IP check is a pass-through when
  `debug.allowedips` is empty, and the bearer check is registered only when
  `debug.bearertoken` is set — so a deployment that enables debug, clears the allowlist,
  and sets no token serves the full driver error, the very string this atom removed from
  `/ready`, to anyone who can reach the port. Before repointing a consumer here, confirm
  `debug.allowedips` is non-empty (it defaults to loopback: `127.0.0.1`, `::1`) or
  `debug.bearertoken` is set; prefer the application log, which needs neither. And do not
  reconstruct the DSN by parsing the log line if your own config already names the database
  the service dials.
- verify: with a configured but unreachable database, `curl -s localhost:8080/ready | jq -r
  '.error'` prints exactly `database unavailable` and contains no `user=`, database name,
  host, port, or IP. The application log for the same request still carries the full driver
  error under `component=database`.
- ref: ADR-046 (the seam, reused unchanged) · `app/health.go`
  (`databaseManagerHealthProbe`) · `app/lifecycle.go` (`publicProbeError`) ·
  `app/debug_health.go` · #879

### [C57.2] every critical probe's `503` body is sanitized by default · silent-behavior · when: match

- detect: `git grep -rn 'app.Prober\|HealthStatus{' -- '*.go'` for your own readiness probe
  implementations, then check each one for `Critical: true` — and, among those, for a
  `PublicErr` that is either never set or set from anything other than a fixed literal.
  `git grep -n 'PublicErr' -- '*.go'` finds every assignment; flag one built from
  `Err.Error()`, a `fmt.Sprintf(...)` call, a config field, or a host/DSN/tenant-identifier
  variable — anything that isn't a bare quoted string. Match = you implement a critical
  `Prober` that returns an error and either leaves `PublicErr` empty or assigns it from
  dynamic data — per ADR-048, reviewing a `PublicErr` override is the only disclosure review
  left once this atom lands. **Expect no match at this version:** probe registration is
  framework-internal (`App.healthProbes` is unexported, its only writer `createHealthProbes`
  takes no argument, and `app.Options` carries no probe field), so nothing consumer-written
  reaches `readyCheck` today. `Prober` is exported, so this atom is written for the release
  where a registration API lands. The framework's own probes are **not** a match either: the
  database and cache `503` bodies are byte-identical across this atom (`database
  unavailable` / `cache unavailable`, exactly what they served in C57.1 and C56.12) — both
  fixed literals — and messaging is never critical. **Limits of the probe:** it only catches
  a `Prober` that names `app.Prober` explicitly or builds its status from an inline
  `HealthStatus{` literal, so it misses an implicit `Prober` (a `Run(context.Context)
  HealthStatus` method that never names the interface), a `Run` whose status comes from a
  helper function instead of an inline literal, and a `PublicErr` set by field access on a
  variable built elsewhere. Treat a clean result as a strong hint, not proof; inspect each
  `Run` implementation and the probe registration path by hand for helper-generated statuses
  and implicit `Prober` implementations.
- gate: match = your probe's `503` body stops carrying `Err` and now reads
  `<Name> unavailable`, synthesized from `HealthStatus.Name`. `publicProbeError` no longer
  falls back to the raw error for an empty `PublicErr` — `/ready` is unauthenticated and
  carries no IP allowlist, so the sanitized string is the default and `PublicErr` is the
  override. `HealthStatus.Err` is unchanged: it still reaches the application log on every
  critical `503` and `<debug.pathprefix>/health-debug`. no-match = nothing to do; this atom
  emits no byte difference for a consumer running only framework probes.
- before:

  ```json
  {"status":"not ready","vault":"unhealthy","error":"dial tcp 10.0.0.9:8200: connect: connection refused"}
  ```

- after:

  ```json
  {"status":"not ready","vault":"unhealthy","error":"vault unavailable"}
  ```

- apply: nothing, if the synthesized `"<Name> unavailable"` reads correctly for your probe —
  that is the intended outcome, and the detail your alerting needs is on the log line
  `Readiness check failed` (with a `component` field) — prefer that channel, since it needs
  no configuration — or on the debug health endpoint, which carries the full error only
  where debug is enabled and access-controlled: configure `debug.allowedips` (non-empty) or
  `debug.bearertoken` before pointing anything at `<debug.pathprefix>/health-debug` (see
  `[C57.1]`'s `apply:`). To choose different wording, set `HealthStatus.PublicErr` to a
  **fixed** string: a value derived from config — a host, a DSN, a tenant key —
  reintroduces on an unauthenticated endpoint exactly the disclosure this default removes.
- verify: with your dependency down, `curl -s localhost:8080/ready | jq -r '.error'` prints
  `<Name> unavailable` (using your probe's `Name`, or your `PublicErr` if you set one), and
  contains no host, port, IP, username, or database name. The same request's application
  log line still carries the full error.
- ref: ADR-048 · `app/lifecycle.go` (`publicProbeError`) · `app/health.go` (`Prober`,
  `HealthStatus.PublicErr`)

### [C57.3] `/ready`'s 200 body drops `db_stats.connections` · silent-behavior · when: match

- detect: `git grep -rn 'db_stats' --` finds the in-repo consumers — a contract test pinning
  the key set, a checked-in log-pipeline config. **A clean grep is not sufficient on its
  own**, and treating it as one is how a dashboard breaks on deploy. Most consumers of
  `/ready` live outside the Go repo, and some are invisible to a literal grep even inside it:
  Grafana panels, Prometheus / Datadog alert rules, synthetic checks, k6 or Postman scripts,
  and any code assembling the path dynamically (`body["db_stats"]["connections"][i]["key"]`,
  a JSONPath expression, a field name held in a variable). Search those by hand for
  `connections` and for the fields under it — `key`, `last_used`, `idle_duration`. Match =
  any consumer, inside the repo or outside it, reads the array. Only `db_stats` changes:
  `messaging_stats` and `cache_stats` were already counters-only and carry no per-key
  entries, so nothing reading those needs checking.
- gate: match = your consumer now finds `db_stats` without its array. `/ready` has no
  authentication and no IP allowlist — load balancers must reach it — and its only throttle is
  the `app.rate.ippreguard` abuse ceiling (enabled by default at 2000 rps/IP), which is no
  barrier to enumeration. Each entry's `key` is the resourcepool key, which is the **tenant
  ID** in a multi-tenant deployment and the named-database key in a multi-DB one. Polling the
  endpoint therefore returned a live enumeration of which tenants were active, plus
  `last_used` and `idle_duration` showing when each was last served. This is the 200-body
  counterpart of C57.1: unlike a `503`, it answers on the healthy path, which is the one actually
  polled. no-match = nothing reads the array; the change is invisible. The scalar counters
  (`active_connections`, `max_connections`, `idle_ttl_seconds`, `status`) are untouched, so
  a consumer reading only those sees no difference.
- before:

  ```json
  {"status":"ready","database":"healthy","db_stats":{"active_connections":3,"max_connections":25,"idle_ttl_seconds":3600,"status":"healthy","connections":[{"key":"acme","last_used":"2026-08-05T10:00:00Z","idle_duration":4},{"key":"globex","last_used":"2026-08-05T09:58:12Z","idle_duration":112}]}}
  ```

- after:

  ```json
  {"status":"ready","database":"healthy","db_stats":{"active_connections":3,"max_connections":25,"idle_ttl_seconds":3600,"status":"healthy"}}
  ```

  (database keys only — the body also carries `messaging`, `cache`, their stats, `time` and
  `app`.)
- apply: repoint anything alerting on per-tenant pool activity. Two channels still carry it.
  The debug health endpoint renders the full array verbatim under
  `data.components.database.details.connections` at `<debug.pathprefix>/health-debug`
  (`debug.pathprefix` defaults to `/_sys`), but only where `debug.enabled: true` (default
  `false`) and `debug.endpoints.health: true` (default `true`) — and its access control is
  conditional, so confirm `debug.allowedips` is non-empty (it defaults to loopback) or
  `debug.bearertoken` is set before pointing a scraper at it. The durable answer for a
  dashboard is the OpenTelemetry database metrics, which carry pool state without publishing
  it on an unauthenticated port. `database.DbManager.Stats()` is unchanged for code calling
  it directly — the redaction happens where `/ready` renders, not in the manager — but note
  the same rule now applies to any surface you render it on: a pool key is tenant identity.
- verify: with at least two tenants warm, `curl -s localhost:8080/ready | jq '.db_stats'`
  shows the four scalar keys and no `connections`, and `curl -s localhost:8080/ready` grepped
  for a known tenant ID returns nothing. For the debug channel, build the URL from your own
  settings rather than assuming the defaults — with `debug.enabled: true`,
  `debug.endpoints.health` left at `true`, the request coming from an address inside
  `debug.allowedips`, and `PREFIX` set to `debug.pathprefix` (`/_sys` unless you changed it):
  `curl -s -H "Authorization: Bearer $DEBUG_TOKEN" "localhost:8080$PREFIX/health-debug" | jq
  '.data.components.database.details.connections'` still lists every key (drop the header when
  `debug.bearertoken` is empty).
- ref: `app/lifecycle.go` (`publicDBStats`) · `database/manager.go` (`DbManager.Stats`) ·
  `app/debug_health.go`

### [C57.4] An enabled outbox/inbox now fails startup without a usable database · breaking · when: match

- detect: `git grep -n 'outbox:\|inbox:' -- '*.yaml' '*.yml' 'config*.yaml'` and
  `git grep -nE 'OUTBOX_ENABLED|INBOX_ENABLED'` across your deployment manifests and env
  files for every environment that sets `outbox.enabled: true` or `inbox.enabled: true`, then
  check each one against the exempt-mode list in `scope` below — anything not exempt is a
  match if its database is absent, unreachable, its outbox/inbox table has not been migrated
  yet and `autocreatetable` is off, or its runtime role lacks the privilege the probe needs
  (outbox `SELECT`, inbox `DELETE`, plus table DDL wherever `autocreatetable` is on).
- scope: `outbox.Module.Init` / `inbox.Module.Init` now run a startup probe
  (`verifyStartupDatabase`) whenever the `""` key is statically resolvable at `Init` time —
  single-tenant, or `tenancy: shared` with a static `source.type` (the same set
  `checkTenancyFanOutGuards` already gated). The probe is the exact read/write the relay or
  cleanup job already performs every cycle (outbox: `FetchPending(…, 1)`; inbox:
  `DeleteProcessed` before the Unix epoch, a write that matches no row), so it fails for the
  same reasons the job would have — just at startup instead of once per `pollinterval`/day,
  forever. The inbox probe therefore needs `DELETE` on the inbox table even where nothing else
  deletes from it: `ProcessOnce` only issues an `INSERT`, and the retention cleanup job — the
  sole runtime `DELETE` — is registered only when a scheduler module is present, so a
  least-privilege runtime role for a `ProcessOnce`-only deployment must be granted `DELETE`
  before the bump. With `autocreatetable` enabled the table DDL moves to `Init` as well, since
  the probe initializes the store. **Exempt** (unaffected, still runtime-resolved): per-tenant
  fan-out (`multitenant.enabled: true` with the default tenancy) and a dynamic source
  (`source.type: dynamic`). **Not exempt, unlike elsewhere in the framework:** a custom
  dynamic `Options.ResourceSource` behind a static `source.type` — the app builder's
  pre-init and `/ready` skip that mode, but a module sees only `*config.Config` and cannot
  detect it, so such a deployment is probed at startup where it previously wasn't. Set
  `source.type: dynamic` to opt out.
- gate: match = an enabled outbox/inbox in a non-exempt mode has no database configured, an
  unreachable database, or a table the probe cannot use. `Init` now returns one of:
  `outbox.enabled=true requires a database, but none is configured` (or `inbox`, same
  wording) for an unconfigured database; `database unreachable at startup` for a
  network/auth/DNS failure; or `table %q is not usable (missing table or insufficient
  privileges)` when the table is missing, or the runtime role cannot run the probe's statement
  (outbox: `SELECT`; inbox: `DELETE`). no-match = the database is configured and reachable and
  the table exists — or `autocreatetable` is on and its DDL succeeds — or the deployment is
  exempt.
- before:

  ```yaml
  outbox:
    enabled: true
  # no database: block — the service booted green; the relay then logged a
  # poll-interval failure forever, captioned "database (optional)"
  ```

- after:

  ```yaml
  database:
    type: postgresql
    host: db.internal
    port: 5432
    database: appdb
    username: app
  outbox:
    enabled: true
  # …and the outbox table must already exist (run migrations), or set
  # outbox.autocreatetable: true
  ```

- verify: `go build ./... && go test ./...` exercises the probe logic itself, but no test can
  see your environment's config — start the service against each non-exempt environment and
  confirm it now exits non-zero (instead of logging a relay/cleanup failure once per interval)
  when the database is absent or unreachable, or its table is missing with `autocreatetable`
  off (or on, but the DDL fails); confirm a healthy environment still starts cleanly.
- ref: wiki/outbox.md#startup-verification · `outbox/module.go` (`verifyStartupDatabase`) ·
  `inbox/module.go` (`verifyStartupDatabase`) · #876

### [C57.5] `database.type` is inferred from a recognized connection-string scheme; an unrecognized one now fails startup on the built-in connector · breaking · when: match

- detect: `git grep -n 'connectionstring' -- '*.yaml' '*.yml'` and
  `git grep -nE 'DATABASE_CONNECTIONSTRING|DATABASES_.*_CONNECTIONSTRING|MULTITENANT_TENANTS_.*_CONNECTIONSTRING'`
  across deployment manifests and env files for every `database.connectionstring` /
  `databases.<name>.connectionstring` / `multitenant.tenants.<id>.database.connectionstring`
  — **with or without** a sibling `type`. Match = one exists; check its scheme against the
  recognized list (`postgres://`, `postgresql://`, `oracle://`), whether a sibling `type` is
  set and agrees with that scheme, and whether the deployment uses the built-in connector (no
  `Options.DatabaseConnector`). An entry with no `type` matters only on the built-in connector;
  a `type` that contradicts the scheme matters on every connector.
- scope: `config.validateDatabaseWithConnectionString` now infers `Type` from the DSN scheme
  when `Type` is empty, and errors on an explicit `Type` that conflicts with the inferred
  one. This applies everywhere `validateDatabase` runs: the root `database:` block, every
  `databases.*` entry (write-back persists the inference the same way it already persists
  pool/session defaults), and every static `multitenant.tenants.*.database` entry. Separately,
  `app.Builder.ConfigureRuntimeHelpers` now fails startup when the root `database:` block, a
  `databases.*` entry, or — **only under `multitenant.enabled: true`** — a
  `multitenant.tenants.*.database` entry still carries a connection string with no resolved
  type, but **only** when the built-in connector (`database.NewConnection`) would be used. A
  tenants block left behind under `multitenant.enabled: false` is inert (koanf loads it from
  YAML regardless of the flag, but neither validation nor `config.NewTenantStore` reads it)
  and is deliberately not policed. A caller-supplied `Options.DatabaseConnector` parses the DSN
  itself and is exempt from that startup guard — but from the guard only: `config.Validate` is
  connector-blind, so a `type` that contradicts the DSN scheme fails on a custom connector too.
  The quiesce CLI's tolerated-empty-`Type` PostgreSQL path
  (`tools/migration/internal/commands/quiesce.go`) now arrives with `Type` already
  inferred to `postgresql` and so is unaffected either way.
  **Oracle only:** inference makes `validateOracleFields` run on a DSN-only config for the
  first time (an empty `Type` used to skip vendor validation entirely), so that check is
  relaxed in the same change — a connection string waives the "exactly one of
  `oracle.service.name` / `oracle.service.sid` / `database`" requirement, because
  `buildOracleDSN` returns the connection string verbatim and never reads those fields.
  `oracle://user:pw@host:1521/XE` alone is therefore a complete config. Two Oracle checks are
  **not** waived alongside a connection string: setting more than one identifier is still
  "multiple identifiers configured", and `database.tls.cert`/`key`/`ca` are still rejected
  (tcps/wallet is not implemented, so accepting them would imply an encryption that does not
  exist).
- gate: match = one of three outcomes. (1) A connstring with a recognized scheme and no
  explicit `type` — this used to pass validation and then fail at first query with
  `unsupported database type: ""`; it now infers the type and **actually connects**, which is
  the surprising direction: a deployment that "worked" only because its database layer was
  never exercised now dials a real database at startup pre-init. (2) A connstring with an
  unrecognized scheme, no `type`, on the built-in connector — this also used to boot and fail
  at first query; it now fails startup with an error naming every affected config path. (3) An
  explicit `type` that conflicts with the DSN's scheme — this used to pass validation with the
  explicit value taken as-is; it now fails validation, on every connector. A fourth outcome
  needs no action, only awareness: an `oracle://` connection string with no identifier field
  now validates, where `type: oracle` alongside the same DSN used to be rejected. That is a
  relaxation — nothing that validated before stops validating. no-match = every
  connection string either already carries a matching explicit `type`, or carries no `type`
  while the deployment supplies its own `Options.DatabaseConnector`, or is one of the untyped
  ones sitting under `multitenant.tenants` in a `multitenant.enabled: false` deployment, where
  the whole block is inert.
- before:

  ```yaml
  database:
    connectionstring: postgres://app:pass@db.internal:5432/appdb
  # no type — passed config.Validate, then every query hit
  # "unsupported database type: \"\""
  ```

- after:

  ```yaml
  database:
    connectionstring: postgres://app:pass@db.internal:5432/appdb
  # type inferred to postgresql — the service now actually connects
  ```

  and, for an unrecognized scheme with no explicit `type`:

  ```yaml
  database:
    connectionstring: sqlserver://app:pass@db.internal:1433/appdb
  # built-in connector: Init now fails with
  # "database configuration at [database]: connectionstring has no resolved database type; ..."
  ```

  and, for Oracle, where the DSN alone is now enough:

  ```yaml
  database:
    connectionstring: oracle://app:pass@db.internal:1521/XEPDB1
  # type inferred to oracle; no oracle.service.name / .sid / database needed —
  # the DSN carries the identifier. Previously "type: oracle" plus this DSN failed
  # with "oracle connection identifier exactly one required".
  ```

- apply: for outcome (1), confirm the target database is reachable and reviewed as a startup
  dependency — it was previously inert, so pre-init now dials it for the first time. For
  outcome (2), set `<path>.type` to `postgresql` or `oracle` explicitly (the schemes
  `postgres://`, `postgresql://`, `oracle://` infer automatically and need no `type`), or
  switch to a custom `Options.DatabaseConnector` if the scheme is neither vendor. For outcome
  (3), fix the conflicting `type` or connection-string scheme — one of them is wrong.
- verify: `go build ./... && go test ./...` exercises the inference and guard logic itself,
  but no test can see your environment's config — start each affected environment and confirm
  it now connects (outcome 1) or fails startup with a clear error naming the path (outcome 2)
  instead of booting green.
- ref: [ADR-050](adr_050_connectionstring_type_inference.md) ·
  `config/validation.go` (`inferDatabaseTypeFromConnectionString`,
  `validateDatabaseWithConnectionString`) · `app/app_builder.go`
  (`untypedConnectionStringPaths`, `ConfigureRuntimeHelpers`) · #877

---

### [C57.6] A database identity field delivered as an empty string now fails startup · breaking · when: match

- detect: `git grep -nE "(MULTITENANT_TENANTS_[A-Z0-9_]+_)?DATABASE(S_[A-Z0-9_]+)?_(TYPE|HOST|PORT|DATABASE|USERNAME|PASSWORD|CONNECTIONSTRING|ORACLE_SERVICE_(NAME|SID))=(\"\"|'')?$"`
  across env files and deployment manifests for any identity var set to an
  empty value — the optional prefix covers the per-tenant namespace, which
  `config.Load` maps to `multitenant.tenants.<id>.database.*` like any other
  key — and
  `git grep -nE "(host|type|port|database|username|password|connectionstring):[[:space:]]*(\"\"|''|null|~)?[[:space:]]*$"`
  under `database:`, `databases.*`, and `multitenant.tenants.*.database` blocks
  in YAML for a key that is bare **or** set to an explicit empty scalar: `""`,
  `''`, `null`, and `~` all decode to the empty string and all abort startup, so
  a bare-key-only pattern would miss the very shape the `before:` block below
  shows. Use `[[:space:]]`, not `\s`: `git grep -E`
  is POSIX ERE and silently drops the backslash, so `\s*` degrades to "zero or
  more literal `s`" — it would miss a key with trailing whitespace and match
  `weird:sss`. C56.14 named this exact shape as its one
  blind spot: a field delivered empty (an empty `secretKeyRef`, `envsubst` over
  an unset variable) reads as absence and the predicate cannot tell it from a
  field never set. Both greps are repo-local and `config.Load` ingests **every**
  process environment variable, so also check the rendered environment your
  deployment actually runs with — a CI runner's exported vars, a base image
  `ENV`, a `docker run -e DATABASE_HOST` with no value, an operator's shell
  profile. None of those live in the tree, and each one now aborts startup.
  Both greps are also line-oriented, which bounds them to flat YAML keys and
  shell-style assignments; two shapes need a manual pass. The YAML pattern omits
  `oracle.service.name` and `oracle.service.sid` deliberately — the env pattern
  covers them, but in YAML they nest four deep under `database.oracle.service`
  and their leaves, `name:` and `sid:`, are among the commonest keys in the
  format, so folding them into the alternation above matches every unrelated
  `name:` in reach. Locate the block instead, with
  `git grep -nE "^[[:space:]]*oracle:[[:space:]]*$"`, and read its two leaves.
  And a structured manifest carries the key and its value on separate lines
  (`- name: DATABASE_HOST` then `value: ""`), which no line-oriented pattern can
  correlate — nor can any repo grep see a `secretKeyRef` / `configMapKeyRef`
  whose referenced secret is the empty thing. Check those by eye, the same way
  `[C57.3]` sends you outside the repo for its dashboards.
- scope: `config.Validate` gains `validateNoDeliveredEmptyDatabase`, which runs
  before `validateMultitenant` and consults the koanf instance `config.Load`
  already stores on `cfg.k`, not just the decoded `DatabaseConfig` values. For
  the root `database` section, each `databases.<name>`, and each static
  `multitenant.tenants.<id>.database`, a section that `IsDatabaseConfigured`
  reports as unconfigured but that has ANY identity key present in the loaded
  configuration now fails startup, naming every offending key path so one boot
  surfaces the whole set.
  Tenant sections are walked only when `multitenant.enabled: true`, so a
  leftover `tenants:` block in a single-tenant deployment stays inert.
  `IsDatabaseConfigured`'s signature and its six existing call sites are
  unchanged; hand-built `Config` values (no koanf instance) and dynamic-source
  tenant configs (never routed through koanf) are unaffected.
- gate: match = some environment sets a database identity key to an empty
  value with no other identity field completing the section. no-match = every
  environment either configures the section fully, sets no identity key at
  all, the empty key sits under `multitenant.tenants.*` with
  `multitenant.enabled: false`, or the empty key belongs to a config source
  that bypasses `config.Load` (dynamic-source tenants, hand-built `Config`
  literals).
- before:

  ```yaml
  database:
    host: ""   # e.g. from an empty secretKeyRef
  # loaded as database-free; /ready reported not_configured; first query failed
  ```

- after:

  ```yaml
  database:
    host: ""   # config.Load now fails: "database identity field(s) delivered
               # empty: [database.host]"
  ```

  Fix by supplying a real value, or removing the key entirely if the service
  genuinely has no database — an absent section is unaffected.
- verify: `config.Load()` succeeds for every environment you deploy; an
  environment relying on an empty `DATABASE_*` var as its "no database" signal
  must switch to leaving the var unset.
- ref: [ADR-051](adr_051_delivered_empty_database_identity.md) ·
  `config/validation.go` (`validateNoDeliveredEmptyDatabase`,
  `deliveredEmptyDatabaseKeys`, `databaseIdentityKeys`) · C56.14 · #880

### [C57.7] Debug endpoints with neither an allowlist nor a bearer token now abort startup · breaking · when: match

- detect: three steps. The greps **locate candidates**; they do not decide the outcome, because they match key and symbol *names* while the hazard is decided by their *values*.
  **(1) Locate.** `git grep -nE 'DEBUG_(ENABLED|ALLOWEDIPS|BEARERTOKEN)' -- '*.yaml' '*.yml' '*.env' 'Dockerfile*' 'deploy/'` and `git grep -nE '^[[:space:]]*(enabled|allowedips|bearertoken|pathprefix):' -- '*.yaml' '*.yml'` scoped to each file's `debug:` block, over every environment you deploy; then, for config assembled in Go, `git grep -n 'app.NewWithConfig' -- '*.go'`, `git grep -n 'ConfigLoader:' -- '*.go'` and `git grep -nA6 'Debug:' -- '*.go'`. The explicit character class habit applies: **never write `\b` in a `git grep -E` pattern** — Git's POSIX-ERE engine strips the backslash and matches a literal `b`, so the detect silently reports "not affected".
  **(2) Inspect each candidate's effective values** — in the merged YAML/env for that environment, or in the Go literal — and confirm all **four** hold at once: `debug.enabled` is true; **at least one `debug.endpoints.*` flag is true** (all four default to `true` under `config.Load`, and all four are `false` in a bare Go literal); `debug.allowedips` resolves to an empty list (`[]`, `DEBUG_ALLOWEDIPS=`, or a nil `AllowedIPs` field); and `debug.bearertoken` is unset, empty, **or whitespace-only** (it is trimmed before the check, since `Authorization: Bearer  ` would authenticate against a blank one). Any one of the four missing means not affected — a hit in step 1 on its own means nothing.
  **(3) A clean grep is not sufficient on its own** where config is assembled in Go: the `Debug` fields can be set anywhere the struct is reachable — a helper constructor, a `switch` on environment, a merge over a base config, a field assigned from a variable — so no fixed pattern is guaranteed to see them, and reading a miss as safety is how this one gets through. The decisive check, and the only one that cannot produce a false negative: **start the service in a staging environment against its real config and see whether it boots.** The refusal fires during startup, before the listener opens, and prints the full error naming the exposed endpoints and both keys. Match = at least one environment, or one `*config.Config` reaching `app.NewWithConfig`, satisfies all four conditions — or fails to boot in step 3
- scope: `app.DebugHandlers.RegisterDebugEndpoints` gained an `error` return and refuses that state instead of warning. `ipWhitelistMiddleware` lost its empty-allowlist pass-through, and each control is now applied only when its key is configured — so where both are set a request must come from an allowlisted IP AND carry the token, and where only the token is set the allowlist no longer silently admits everyone. The refusal is gated on at least one endpoint being enabled: `debug.enabled: true` with every `debug.endpoints.*` flag off registers an empty group and still starts. A whitespace-only `debug.bearertoken` no longer counts as configured: it neither satisfies the gate nor wires `authMiddleware`, so a config relying on one now aborts instead of installing a credential that `Bearer  ` matches. The signature change is source-compatible **only for a direct call written as an expression statement**, where Go permits discarding the return value; a consumer that names the old signature — a method value assigned to a `func(server.RouteRegistrar)` variable, field, or argument, or an interface declaring the method without an `error` result — stops compiling and must adopt `if err := h.RegisterDebugEndpoints(r); err != nil { return fmt.Errorf("register debug handlers: %w", err) }`. `apidiff` reports the change either way and `errcheck` will newly flag a direct call that ignores the error; the framework itself calls it from `App.registerDebugHandlers`, and `App.prepareRuntime` returns that error to startup untouched, so the message you see at boot is the one quoted under `verify` below
- gate: match = some environment has `debug.enabled: true` **and** at least one `debug.endpoints.*` flag on **and** an empty `debug.allowedips` **and** a `debug.bearertoken` that is unset, empty, or whitespace-only; **or** you build a `*config.Config` in Go and hand it to `app.NewWithConfig` (directly, or via `Options.ConfigLoader`) with `Debug{Enabled: true}`, at least one `Endpoints` flag set and `AllowedIPs`/`BearerToken` left at their zero values. That path never receives the koanf default map, so the loopback `allowedips` default that protects every YAML/env deployment was never there — the refusal is reachable by omitting one field rather than by overriding two. no-match = every environment breaks at least one of the four conjuncts: it leaves `debug.enabled` at `false`, or has every `debug.endpoints.*` flag off (nothing is exposed, so nothing is refused), or keeps a non-empty `debug.allowedips` (the `["127.0.0.1", "::1"]` default counts, but only where `config.Load` supplied it), or sets `debug.bearertoken` to a value that is non-empty after trimming whitespace
- before:

  ```yaml
  debug:
    enabled: true
    allowedips: []        # started, WARNed once, served /_sys to every peer
  ```

- after:

  ```yaml
  debug:
    enabled: true
    allowedips: ["10.0.0.0/8"]       # …and/or bearertoken below
    bearertoken: ${DEBUG_BEARERTOKEN}
  ```

  If the empty list was deliberate — reaching the endpoint from a container or a LAN peer during development — say so explicitly with `allowedips: ["0.0.0.0/0"]` (plus `["::/0"]` for IPv6), which is greppable in a way the empty list was not. Setting `debug.enabled: false` is the other exit. In a Go-assembled config, set the field explicitly — `Debug: config.DebugConfig{Enabled: true, AllowedIPs: []string{"127.0.0.1", "::1"}, …}` — since nothing back-fills it there
- verify: the service starts. On the refused config it now exits during startup with `debug endpoints are enabled and would expose <list> at <prefix> with NO access control: set debug.allowedips (env DEBUG_ALLOWEDIPS) and/or debug.bearertoken (env DEBUG_BEARERTOKEN), or set debug.enabled to false`
- ref: ADR-049 · `app/debug_handlers.go` (`RegisterDebugEndpoints`, `exposedEndpoints`, `bearerTokenConfigured`, `ipWhitelistMiddleware`, `authMiddleware`) · `app/lifecycle.go` (`registerDebugHandlers`, `prepareRuntime`)

### [C57.8] A single-tenant service that declared consumers now fails startup when they cannot start · breaking · when: match

- detect: `git grep -nE 'DeclareConsumer|RegisterConsumer'` for modules that declare
  AMQP consumers, then confirm the deployment is single-tenant
  (`multitenant.enabled` absent or `false`). Only that intersection is affected. A
  repo grep cannot tell you whether the broker those consumers need is reachable
  from each environment at boot, which is the other half of the match — check the
  broker's availability and credentials per environment, and whether any queue or
  exchange in the declaration set is ops-provisioned with arguments that differ
  from what the module declares (that mismatch surfaces as `406
  PRECONDITION_FAILED` — see [ADR-040](adr_040_declaration_args_passthrough.md)).
- scope: `MessagingInitializer.PrepareRuntimeConsumers` previously logged one WARN
  (`Failed to start single-tenant consumers`) and returned nil when
  `Manager.EnsureConsumers` failed. Nothing ever retried it, and the messaging
  readiness probe is not critical, so the pod passed `/ready` with 200 and served
  HTTP while consuming **zero** messages — permanently. That failure is now
  returned, and `prepareRuntime` already propagates it, so startup aborts. The
  fatal grade applies **only when the declaration set contains at least one
  consumer**. `EnsureConsumers` itself still runs unconditionally — it is what
  declares exchanges, queues, and bindings — so a **publisher-only** service, or one
  with no messaging configured at all, keeps the historical warn-and-continue and is
  unaffected. Multi-tenant is unaffected: consumers start lazily per tenant, so
  nothing is attempted at startup. The publisher pre-warm pass that runs immediately
  after remains WARN-only by design. This complements the existing
  `assertMessagingConfiguredIfDeclared` check, which already aborted when
  declarations existed but no broker URL was configured; the new gate covers the
  case where a broker **is** configured but unusable.
- gate: match = a single-tenant service with at least one consumer declaration whose
  broker is unreachable, rejects its credentials, or rejects one of its declarations
  at boot. Startup now exits non-zero with `failed to start single-tenant consumers:
  <cause>`. no-match = multi-tenant, no consumer declarations (publisher-only or
  messaging-free), or a healthy reachable broker.
- before:

  ```text
  WARN  Failed to start single-tenant consumers error="..."
  INFO  Starting HTTP server on :8080        # serves traffic, consumes nothing, forever
  ```

- after:

  ```text
  FATAL failed to start single-tenant consumers: <cause>   # process exits non-zero
  ```

- verify: `go build ./... && go test ./...` covers the grading logic, but no test can
  see your broker. Start each single-tenant consumer service against its real broker
  and confirm it still boots; then confirm that with the broker stopped it now exits
  non-zero instead of logging the WARN and serving. Container orchestrators restart
  a service that exits, so an environment whose broker comes up *after* the service
  will now crash-loop until the broker is ready rather than idling deaf — make sure
  restart backoff is configured, or start the broker first.
- ref: `app/messaging_setup.go` (`PrepareRuntimeConsumers`) · `app/lifecycle.go`
  (`prepareMessagingConsumers`, `assertMessagingConfiguredIfDeclared`)

### [C57.9] `httpclient.Build` validates JOSE policies; `jose.Seal` enforces the algorithm allowlist · breaking · when: match

- detect: `git grep -n 'WithJOSE(' -- '*.go'` for the builder call sites and
  `git grep -n 'jose\.Seal(' -- '*.go'` for direct sealing, then read the
  `*jose.Policy` each one is handed. You are looking for three shapes:
  an explicitly-set `SigAlg`, `KeyAlg`, or `Enc` outside the allowlist
  (`RS256`/`PS256`; `RSA-OAEP-256`; `A256GCM`) — `git grep -nE
  'SigAlg:|KeyAlg:|Enc:' -- '*.go'` enumerates the assignments; kids that do
  not match the policy's `Direction` (an outbound policy carrying
  `DecryptKid`/`VerifyKid`, or missing `SignKid`/`EncryptKid`); and a
  `JOSEConfig` whose `Resolver` is nil while `Outbound` or `Inbound` is set.
  All three greps are line-oriented and blind to an import alias or a policy
  built field-by-field across statements, so treat them as probes: the
  authoritative answer is that `Build()` now returns the error, at startup,
  for every affected client.
- scope: `Build` normalizes each non-nil policy — unset `SigAlg`/`KeyAlg`/`Enc`/`Cty`
  take the `jose` package defaults — and then runs `jose.Policy.Validate`, the same
  check the server's struct-tag scanner has always run at route registration.
  Normalization happens on **copies**, so a `jose.Policy` shared across builders is
  never mutated. **The two call-site classes are not affected alike.** Through
  `WithJOSE` you get both halves — unset algorithms take the package defaults, and only
  a genuinely invalid policy fails, at `Build()`, before the process serves traffic. A
  **direct** `jose.Seal` caller gets the check without the defaults: `Seal` now runs the
  allowlist itself before touching the crypto adapter, so an explicitly-set disallowed
  algorithm that used to seal successfully returns `JOSE_ALGORITHM_DISALLOWED` at
  runtime, and a policy that left its algorithms unset now fails the same way rather
  than on whatever go-jose made of an empty `alg`. **Kid *resolution* is
  deliberately not part of this**: a `KeyResolver` may be backed by lazily-loaded key
  material, so an unknown kid still fails per request as `JOSE_KID_UNKNOWN`.
- gate: match = a policy you hand to `WithJOSE` or `jose.Seal` explicitly names an
  algorithm outside the allowlist, or sets kids that contradict its `Direction`, or
  you call `WithJOSE` with a policy and no `Resolver`. The consequential case is
  `KeyAlg: RSA1_5` or a non-AEAD `Enc`: those **worked** before — go-jose sealed them
  and the peer accepted them — so the client is emitting tokens below the framework's
  floor today and stops building after the bump. no-match = your policies come from
  the defaults or `jose/testing`'s fixtures, or they set no algorithms at all *and*
  reach the wire through `WithJOSE`. A policy that named **no** algorithms is strictly
  better off on that path — it used to reach the crypto adapter with an empty `alg` and
  fail every request with `JOSE_OUTBOUND_FAILED`, and now takes the package defaults and
  works — but the same policy handed to `jose.Seal` **directly** is a match, because
  nothing defaults it there and it now fails as `JOSE_ALGORITHM_DISALLOWED`.
  A disallowed *signature* algorithm (`HS256`) is likewise not a regression in
  outcome — it already failed every request — only in timing.
- before:

  ```go
  // Built fine; every request failed, or (RSA1_5) succeeded below the algorithm floor.
  client, err := httpclient.NewBuilder(logger).
      WithTransport(base).
      WithJOSE(httpclient.JOSEConfig{
          Outbound: &jose.Policy{
              Direction: jose.DirectionOutbound,
              SignKid:   "our-signing", EncryptKid: "visa-vts-encrypt",
              KeyAlg:    "RSA1_5",
          },
          Resolver: resolver,
      }).Build()
  ```

- after:

  ```go
  // err: "httpclient: invalid JOSE policy: JOSE_ALGORITHM_DISALLOWED:
  //       key-wrapping algorithm not in allowlist"
  //
  // Drop the field to take the default (RSA-OAEP-256):
  Outbound: &jose.Policy{
      Direction: jose.DirectionOutbound,
      SignKid:   "our-signing", EncryptKid: "visa-vts-encrypt",
  },
  ```

- apply: remove the disallowed algorithm and let the default apply, or fix the kids
  for the policy's direction, or pass the `Resolver` you were relying on the transport
  to have. Do not work around it by constructing the `JOSETransport` struct directly —
  `jose.Seal` enforces the allowlist too, so that path fails at the same point without
  the startup signal. A peer that genuinely requires `RSA1_5` needs the allowlist
  widened in `jose/algorithms.go` with the padding-oracle risk argued in an ADR, not a
  per-client escape hatch. To distinguish this failure from a transport-slot
  displacement, match the error rather than its text: it wraps `*jose.Error`
  (`errors.As`) and its sentinel (`errors.Is(err, jose.ErrAlgorithmDisallowed)`,
  `jose.ErrPolicyMismatch`, or `jose.ErrKeyResolution` for the nil `Resolver`), and it
  is **not** `httpclient.ErrUnsafeTransportComposition`.
- verify: every `WithJOSE` client constructs at startup — `Build()` returns a nil
  error — and a request through it still round-trips against the peer. A client that
  previously sealed with `RSA1_5` will fail construction until the algorithm is
  changed; confirm with the peer that it accepts `RSA-OAEP-256` before deploying.
- ref: ADR-044 (the fail-closed `Build` posture this extends) ·
  `httpclient/client.go` (`WithJOSE`, `normalizeJOSE`, `normalizedJOSEPolicy`) ·
  `jose/sealer.go` (`Seal`) · `jose/policy.go` (`validateAlgorithms`) ·
  [wiki/httpclient.md](httpclient.md#jose-policy-validation-at-build)

## E58 · v0.57.0 → v0.58.0 — dead exported surface removed + a cache that cannot be constructed aborts startup + reserved log attribute namespaces + log records stop duplicating resource identity

- gist: Two clusters of dead exported symbols leave the public API, both carrying a comment
  that asserted a use they never had. `jose.PolicyRegistry` goes with its constructor and its
  two methods: it cached scanned-and-resolved JOSE policies keyed by
  `(reflect.Type, Direction)` and justified itself, in its own doc comment, with a per-request
  hot path that does not exist — `jose:` tag scanning happens once per route at
  `RegisterHandler` time and the resolved policies live on the route descriptor that the
  request path reads, so there was never a re-scan for the cache to prevent, and nothing in
  the framework ever called it. A consumer with a hand-rolled registration path scans with
  `jose.ScanType` + `jose.ResolvePolicy` directly and memoizes the resolved `*jose.Policy`
  itself — keyed on the type **and** the direction, since one struct used as both request and
  response resolves to two different policies (C58.1). Alongside it,
  `server.TestShortTimeout`, `TestMediumTimeout` and `TestLongTimeout` go too. Those three exported `time.Duration` constants were declared under a header asserting
  they are "used exclusively in test files" and were referenced by nothing in the framework,
  production or test, for their whole life. No framework code ever read them, so removing
  them changes no behaviour — only whether code naming them compiles (C58.2).
  Separately, a cache the framework was told to build and could not build stops being
  survivable. `ResourceManagerFactory.CreateCacheManager` logged one WARN and returned a bare
  `nil` when `cache.NewCacheManager` rejected its options, defeating the intent
  `BuildCacheOptions` documents one function away — that a negative `cache.manager.*` value must
  "fail loudly there instead of being silently swallowed into a live pool". The `nil` then
  bypassed ADR-046's critical-by-default cache probe outright: `createHealthProbes` registers a
  cache probe only when the manager is non-nil, so with no manager there was no probe, `/ready`
  reported the cache `disabled`, and the pod answered `200` — a service that asked for a cache,
  got none, and joined the rotation. It now returns `(*cache.CacheManager, error)`,
  `dependencies` propagates it, and `Builder.ResolveDependencies` records it, so startup aborts.
  Reaching the cache is unaffected: an unreachable Redis at boot still only WARNs
  (C58.3, ADR-054). Separately, the OTel log bridge now reserves the resource-identity
  attribute namespaces: a top-level log field keyed `service.*`, `telemetry.sdk.*`, or
  `deployment.environment.name` is remapped under the `app.` prefix in OTLP-exported log
  records (value preserved, one-time WARN), so a log call can no longer shadow the service's
  identity at record level (C58.4). Finally, the per-processor log enricher stops copying the
  service identity onto every record: it was handed the merged resource rather than the
  `log.type` delta it exists for, so `service.name`, `service.version`,
  `deployment.environment.name` and the `telemetry.sdk.*` triplet — plus every key injected via
  `OTEL_RESOURCE_ATTRIBUTES`, since the whole resource attribute set was copied — rode every OTLP
  log record as record-level duplicates of what the `ResourceLogs.resource` block already ships
  once per batch. `log.type` is now the only record attribute the framework adds; those resource
  attributes are unchanged where they always were, in the resource block. Log fields your own code
  sets are not affected — nothing caller-supplied is removed (C58.5, ADR-056).
- build-caught: C58.1 C58.2 C58.3
- preflight: check every environment for a **negative** `cache.manager.maxsize` or
  `cache.manager.idlettl`, and — in multi-tenant mode where `cache.manager.maxsize` is unset —
  a negative `multitenant.limits.tenants`, which `BuildCacheOptions` substitutes as the pool
  size. Under `cache.enabled: false` such a value used to be inert and now aborts startup
  (C58.3). For C58.4, grep for reserved-key literals (see its detect) and hand-review any code
  path that ranges a map into log fields — dynamic keys escape every grep, and the bridge's
  runtime WARN only exists after the bump, so it is post-upgrade confirmation, not preflight
  evidence; when such paths exist, smoke-test in staging before relying on the old key names.
  For C58.5 the search moves off the codebase entirely: audit log-backend dashboards, alerts and
  saved queries for filters on **record-level** resource attributes (see its detect) — no grep over
  your Go sources can find them. Read `OTEL_RESOURCE_ATTRIBUTES` and `OTEL_SERVICE_NAME` in each
  environment first: every key they inject was duplicated onto records too, so checking only the
  framework's own identity keys under-counts what moves
- exit: `go get github.com/gaborage/go-bricks@v0.58.0 && go mod tidy && go build ./... && go test ./...`

### [C58.1] `jose.PolicyRegistry` and its constructor and methods are removed · compile-break · when: match

- detect: `git grep -nE 'PolicyRegistry|LoadOrScan' -- '*.go'`. Keep the pattern plain — **do not write `\b` in a `git grep -E` pattern**; Git's POSIX-ERE engine strips the backslash and matches a literal `b`, so the detect silently reports "not affected" (see step 4 of the runbook protocol). Four exported symbols go: the type `jose.PolicyRegistry`, the constructor `jose.NewPolicyRegistry`, and the methods `LoadOrScan` and `Store`. Grep for the type and the constructor, not for `.Store(` on its own — `Store` is a common method name and searching it directly buries the real hits; find the registry *values* first, then their call sites
- scope: only code that names the type or calls the constructor. The registry was never reachable from any framework entry point — `ModuleDeps` never carried one, `server.HandlerRegistry` never accepted one, no `HandlerRegistryOption` took one — so the only way to hold one is to have called `jose.NewPolicyRegistry()` yourself, in a hand-rolled registration path. Everything else in package `jose` is untouched: `ScanType`, `ResolvePolicy`, `Policy`, `Direction`, and the `jose:` struct-tag surface are unchanged
- gate: match = the detect returns ≥1 line outside a vendor directory. no-match = no Go file names either symbol. `go build ./...` is the authoritative answer here, since a compile-break atom is resolved by the compiler regardless of how the package was imported (an alias or dot-import defeats a line-oriented grep)
- before:

  ```go
  reg := jose.NewPolicyRegistry()
  p, err := reg.LoadOrScan(reflect.TypeOf(req), jose.DirectionInbound)
  ```

- after:

  ```go
  p, err := jose.ScanType(reflect.TypeOf(req), jose.DirectionInbound)
  if err != nil {
      return err
  }
  if p != nil {
      if err := jose.ResolvePolicy(resolver, p); err != nil {
          return err
      }
  }
  ```

  If you were memoizing deliberately, hold the **resolved** `*jose.Policy` rather than re-deriving it — that is what the framework does: `scanRouteJOSE` scans and resolves once per route at `RegisterHandler` time and writes the result onto the route descriptor, and the request path reads that field. Your own `map[joseKey]*jose.Policy` behind a mutex — where `joseKey` is a `struct{ t reflect.Type; dir jose.Direction }` — or a `sync.Map` under that same composite key, is the whole of what `PolicyRegistry` provided. The direction must stay in the key: one struct used as both a request and a response scans to two different policies with different required keys, and a type-only key returns the wrong one on the second lookup
- why: the type's own doc comment justified it with a per-request hot path that does not exist. `jose:` tag scanning happens once per route at registration and never per request, so there was nothing for the cache to serve — and nothing in the framework ever called it
- verify: `go build ./... && go test ./...`
- ref: [ADR-052](adr_052_remove_jose_policy_registry.md) · #817 · `jose/registry.go` (deleted) · `jose/scanner.go` (`ScanType`) · `jose/resolver.go` (`ResolvePolicy`) · `server/jose.go` (`scanRouteJOSE`)

### [C58.2] `server.TestShortTimeout`, `TestMediumTimeout` and `TestLongTimeout` are removed · compile-break · when: match

- detect: `git grep -nE 'Test(Short|Medium|Long)Timeout' -- '*.go'`. As everywhere in this runbook, keep the pattern free of `\b`/`\s`/`\w` — `git grep -E` has no PCRE escapes, so a pattern carrying one silently matches nothing and the gate reports "not affected"
- scope: three exported `time.Duration` constants in `server/constants.go` — `TestShortTimeout` (100ms), `TestMediumTimeout` (1s), `TestLongTimeout` (5s). No framework code ever read them, so nothing changes behaviourally; only code that names them stops compiling. The `Default*Timeout` constants in the same file (`DefaultReadTimeout`, `DefaultWriteTimeout`, `DefaultIdleTimeout`, `DefaultShutdownTimeout`, `DefaultAPITimeout`) are untouched
- gate: match = the detect returns ≥1 line. no-match = no Go file names any of the three
- before:

  ```go
  ctx, cancel := context.WithTimeout(context.Background(), server.TestShortTimeout)
  ```

- after:

  ```go
  ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
  ```

  Or keep the vocabulary as constants in your own test package — `const testShortTimeout = 100 * time.Millisecond` — which is where test-timing values belong. The three removed values were 100ms, 1s and 5s respectively
- verify: `go build ./... && go test ./...`
- ref: [ADR-053](adr_053_remove_server_test_timeout_constants.md) · #818 · `server/constants.go`

### [C58.3] `CreateCacheManager` returns an error, and a cache that cannot be constructed now aborts startup · breaking · when: match

- detect: two steps, because this atom has two independent audiences and the compiler sees only one.
  **(1) Compile side.** `git grep -nE 'CreateCacheManager|NewResourceManagerFactory' -- '*.go'` — a direct caller of the exported factory must adopt the two-value form. Few consumers have one: the framework calls it from `appBootstrap.dependencies`, which apps reach through `app.New`/`app.NewWithConfig`, not directly.
  **(2) Config side.** Look for a **negative** `cache.manager.maxsize` or `cache.manager.idlettl`: `git grep -nE '^[[:space:]]*(maxsize|idlettl):[[:space:]]*-' -- '*.yaml' '*.yml'` scoped to each file's `cache.manager:` block, and `git grep -nE 'CACHE_MANAGER_(MAXSIZE|IDLETTL)=-'` across env files and deployment manifests.
  **(3) The derived source.** A negative pool size can arrive without either key being set: in multi-tenant mode `BuildCacheOptions` substitutes `multitenant.limits.tenants` for an unset `cache.manager.maxsize`, so search that too — `git grep -nE '^[[:space:]]*tenants:[[:space:]]*-' -- '*.yaml' '*.yml'` scoped to each file's `multitenant.limits:` block, and `git grep -nE 'MULTITENANT_LIMITS_TENANTS=-'` — and read it together with whether `cache.manager.maxsize` is left unset in the same environment. `validateMultitenantLimits` clamps a non-positive tenant limit for `config.Load`, so this one is reachable only on the unvalidated path in `scope`(b), but a grep that omits it reports "not affected" for a config that does abort.
  Keep every pattern free of `\b`/`\s`/`\d` — `git grep -E` has no PCRE escapes and a pattern carrying one silently matches nothing. Then narrow by **how that config is loaded**, which is what decides whether you were already protected — see `scope`
- scope: `CreateCacheManager` returns `(*cache.CacheManager, error)` instead of a bare `*cache.CacheManager`; `appBootstrap.dependencies` propagates it; `Builder.ResolveDependencies` records it in the builder error. The WARN line `Failed to create cache manager, cache will be disabled` is gone. **Two config shapes actually reach this, and the obvious one is not among them.** A deployment loaded through `config.Load` with `cache.enabled: true` was never affected: `config.Validate` runs `applyCacheManagerDefaults`, which has always rejected a negative `maxsize`/`idlettl`/`cleanupinterval`, so it failed at validation and never reached the factory. What reaches it is (a) **`cache.enabled: false` carrying a leftover negative** — `validateCache` returns early for a disabled cache, so the defaults applier never runs, the value passed through untouched, and the resulting nil manager was harmless because nothing wanted a cache; that shape now aborts startup, and it is the one real upgrade hazard in this atom — and (b) **a hand-assembled `*config.Config` handed to `app.NewWithConfig`**, directly or via `Options.ConfigLoader`, which never calls `config.Validate` at all, so nothing checks the pool values. Not in scope: reaching the cache. An unreachable Redis at boot still logs a WARN in `preInitCache` and continues, and a service with no cache configured never fails here
- gate: match = either detect fires — you name the factory in Go, **or** an environment resolves a negative value for those two keys under one of the two shapes in `scope`. no-match = you never call the factory directly AND every environment leaves `cache.manager.maxsize` and `cache.manager.idlettl` absent, zero, or positive **and**, wherever multi-tenant mode leaves `cache.manager.maxsize` unset, leaves `multitenant.limits.tenants` positive. Read the asymmetry carefully, because it inverts the usual config-atom instinct: an **absent** key is safe (it takes the mode default), a **zero** is safe (zero means unset), and only a **negative** is fatal. Finding the keys set is not a match — the sign is what decides
- before:

  ```go
  cacheManager := factory.CreateCacheManager(resourceSource)
  ```

  ```yaml
  cache:
    enabled: false
    manager:
      maxsize: -1        # inert: the cache was off, so the nil manager cost nothing
  ```

- after:

  ```go
  cacheManager, err := factory.CreateCacheManager(resourceSource)
  if err != nil {
      return fmt.Errorf("create cache manager: %w", err)
  }
  ```

  ```yaml
  cache:
    enabled: false       # drop the stale manager block, or give it positive values
  ```

  If a negative was reaching for "unbounded", it never meant that — `cache.NewCacheManager` has always rejected it. Omit the key instead: an unset `maxsize` takes the mode default (`multitenant.limits.tenants` in multi-tenant mode, the built-in single-tenant default otherwise). Running without a cache is still supported and still silent — that is `cache.enabled: false` with no stale tuning values under it
- verify: the service starts. On the refused config it now exits during startup with `create cache manager with maxsize=-1 idlettl=15m0s (from cache.manager.*, or multitenant.limits.tenants where maxsize is unset in multi-tenant mode): maxsize cannot be negative` rather than logging `Failed to create cache manager, cache will be disabled` and serving traffic. The error reports the **resolved** values rather than only the key names, because a third input can produce them: in multi-tenant mode an unset `cache.manager.maxsize` takes `multitenant.limits.tenants`, so a negative there lands here too — `validateMultitenantLimits` clamps it for `config.Load`, leaving that shape reachable on the same `app.NewWithConfig` path as the rest of this atom
- ref: [ADR-054](adr_054_cache_construction_fails_startup.md) · #861 · `app/managers.go` (`CreateCacheManager`, `BuildCacheOptions`) · `app/bootstrap.go` (`dependencies`) · `app/app_builder.go` (`ResolveDependencies`) · `config/validation.go` (`validateCache`, `applyCacheManagerDefaults`) · `cache/manager.go` (`NewCacheManager`)

### [C58.4] Log fields in resource-identity namespaces are remapped under `app.` in OTLP log records · behavior · when: match

- detect: `` git grep -nE '["`](service|telemetry[.]sdk)[.][^"`[:space:]]*["`]' -- '*.go' `` and `` git grep -nE '["`]deployment[.]environment[.]name["`]' -- '*.go' `` — key-literal based across both Go string-literal forms (quoted and raw) with any non-space suffix, so it catches every zerolog constructor (`Str`, `Uint`, `Dur`, `Time`, `RawJSON`, …) and keys like `service.1` at the cost of some non-log noise. Fields built dynamically (ranging a caller- or tenant-influenced map into log fields) escape any literal grep — hand-review those call sites; the runtime WARN record confirms only after the bump
- scope: the OTel log bridge only — the boundary where zerolog field names become OTLP record attributes. A top-level field keyed with the `service.` or `telemetry.sdk.` prefix, or exactly `deployment.environment.name`, reaches the backend as `app.<original key>` with its value preserved; the first remap per bridge instance (one bridge per process in practice) also emits a WARN record (`reserved.keys` names the offending keys, never values). The raw zerolog stream (stdout/file JSON, console output) is **unchanged** — only OTLP-exported records are affected. `log.type` stays caller-settable; nested map values are untouched (they flatten under their parent key); the resource-level identity was never spoofable and does not change
- gate: match = a detect hit, or any code path that ranges caller/tenant-influenced keys into log fields. no-match = nothing to do
- before:

  ```text
  record attribute: service.name = "downstream-svc"   (shadows identity on flattening backends)
  ```

- after:

  ```text
  record attribute: app.service.name = "downstream-svc"   (+ one-time WARN naming service.name)
  ```

  Re-key dashboards, alerts, and saved queries reading the old record attribute to the `app.`-prefixed name — or rename the field at the call site out of the reserved namespace (for a downstream peer, semconv's `peer.service` is the conventional home). Never treat `app.*` as service identity: it is caller-supplied and unauthenticated
- verify: with `observability.logs.enabled: true`, log a field keyed `service.name` and confirm the backend shows `app.service.name` plus the WARN record; `go test ./...`
- ref: [ADR-055](adr_055_reserved_log_attribute_namespaces.md) · #915 · `logger/otel_bridge.go`

### [C58.5] Log records stop carrying record-level copies of resource identity attributes · behavior · when: match

- detect: backend-side only — search the log backend (dashboards, alerts, saved queries) for filters on record-level `service.name`, `service.version`, `deployment.environment.name`, or `telemetry.sdk.*` in LOG records. **Those four are the floor, not the whole set.** The old enricher was handed the resource's *entire* attribute set, so the same applies to every other key the resource carries — including anything folded in by `OTEL_RESOURCE_ATTRIBUTES` (and `OTEL_SERVICE_NAME`), which `resource.Default()`'s env detector merges. Under the Kubernetes OTel operator that routinely means `k8s.pod.name`, `k8s.namespace.name`, `deployment.region` and friends. Read those env vars **in each environment** and add whatever they set to the search. Filters on resource attributes are unaffected; no code grep can find any of this
- scope: OTLP log export only. The per-processor enricher now stamps only `log.type` on records that don't already carry one. Every attribute the resource carries stops appearing as a record-level attribute: the framework's own `service.name`, `service.version`, `deployment.environment.name` and `telemetry.sdk.{name,language,version}`, **plus any key injected through `OTEL_RESOURCE_ATTRIBUTES`/`OTEL_SERVICE_NAME`** (`OTEL_SERVICE_NAME`'s `service.name` is the one exception that behaved identically before — the framework's own value already overrode it on merge). They all remain, unchanged, in the OTLP `ResourceLogs.resource` block the logger provider has always attached. The raw zerolog stream (stdout/file JSON, console output) is unchanged, and so are traces and metrics
- gate: match = a log-backend query filtering any of the keys in `detect` at record level. no-match = nothing to do
- before:

  ```text
  FRAMEWORK-ADDED record attributes: log.type + service.name, service.version,
  deployment.environment.name, telemetry.sdk.{name,language,version}
  + every OTEL_RESOURCE_ATTRIBUTES key (k8s.pod.name, …)            (per record)
  ```

- after:

  ```text
  FRAMEWORK-ADDED record attributes: log.type only    (every resource attribute
                                     now once per batch, in ResourceLogs.resource)
  ```

  Both blocks list only what the **framework** puts on a record. Your own log fields are untouched — an HTTP action log still carries its `request_id`, `http.route`, `http.response.status_code` and the rest exactly as before; this change removes nothing a caller set. Repoint affected queries at the resource attribute of the same name. Backends that flatten record attributes over resource attributes show the same values as before; backends that index the two levels separately stop matching record-level identity filters until repointed
- verify: with `observability.logs.enabled: true`, emit a log and confirm the backend shows the resource attributes at resource level only, `log.type` still at record level, and your own log fields still at record level unchanged; `go test ./observability/...`
- ref: [ADR-056](adr_056_log_enricher_delta_attributes.md) · #914 · `observability/processor_attribute_exporter.go`

## E581 · v0.58.0 → v0.58.1 — cached time.Time values keep sub-second precision + log sampling gains 0.01% resolution

- gist: The cache's CBOR encoder moves from `cbor.TimeRFC3339` to
  `cbor.TimeRFC3339Nano`, so a `time.Time` marshaled through `cache.Marshal`
  no longer loses its sub-second digits on the wire. Both read directions
  still work — the decoder was already mode-independent, so an old
  whole-second entry still parses under the new binary and a new
  sub-second entry still parses under an old one — and a whole-second time
  still encodes to byte-identical output. The one thing that shifts is a
  raw-byte compare-and-set across a mixed-version fleet on a sub-second
  timestamp. Separately, `observability.logs.samplingrate`'s INFO/DEBUG
  sampling comparison scales from a whole-percent modulus (`%100`) to a
  0.01%-resolution one (`%10000`), so a rate at or above 0.00005 that used
  to truncate to a zero threshold and export nothing now rounds to the
  nearest 0.01% bucket and exports that fraction; a rate below 0.00005
  still rounds to zero and still exports nothing. The same wider modulus
  also stops flooring non-whole-percent rates (`0.999` was 99%, now
  99.9%) and redraws which traces land in the sample for every rate at or
  above 0.00005 and below 1.0, including whole percents.
- build-caught: none
- preflight: if any cached type carries a `time.Time` and you use
  `CompareAndSet`/`GetOrSet` on it, decide before the bump whether a
  failed swap during the rolling window is acceptable — persistent
  (`ttl == 0`) entries keep the old encoding until overwritten; and if
  `observability.logs.samplingrate` is set to any value strictly between
  0.0 and 1.0, expect the exported INFO/DEBUG log volume AND the
  membership of the sampled set to change: a rate at or above 0.00005 and
  below 0.01 exported nothing before the bump and starts exporting its
  configured fraction after it, a rate that is not a whole percent stops
  flooring (0.999 was 99%, now 99.9%), and every rate at or above 0.00005
  and below 1.0 redraws which traces land in the sample; a rate below
  0.00005, plus 0.0 and 1.0, are unaffected (C581.2)
- exit: `go get github.com/gaborage/go-bricks@v0.58.1 && go mod tidy && go build ./... && go test ./...`

### [C581.1] Cached `time.Time` values round-trip with sub-second precision instead of being truncated to whole seconds · silent-behavior · when: match

- detect: `git grep -nE 'cache[.](Must)?(Marshal|Unmarshal)' -- '*.go'` — this
  only shortlists call sites into the cache's CBOR encoder/decoder. It does
  **not** chase the type graph: you must hand-review each type passed to
  `Marshal`/`Unmarshal` (and each type nested inside it) for a `time.Time`
  field. No grep can do that walk for you.
- scope: one option in `cache/serialization.go` (`encMode`'s `Time` field).
  Decoding is unchanged and was always mode-independent — `time.Parse` with
  the `RFC3339` layout accepts an optional fractional-second field even
  though the layout does not name one — so **both** directions of a
  mixed-version fleet hold: an old (whole-second) binary reading a new
  (sub-second) entry keeps the nanoseconds, and a new binary reading an old
  entry sees nanosecond `0`. A whole-second `time.Time` encodes to
  byte-identical bytes under either option, so whole-second cache entries
  are unaffected either way. Zone offsets render exactly as before —
  `cbor.TimeRFC3339NanoUTC` was deliberately not chosen because it
  additionally rewrites every non-UTC time to `Z`.
- gate: match = you cache a type carrying a `time.Time` (directly or nested)
  that can hold a non-zero nanosecond. no-match = no cached times, or
  whole-second-only values (e.g. already truncated before caching).
- after: two consequences. (i) code that relied on the old truncation —
  `==` comparisons against a `time.Time` read from the cache, using such a
  time as a map key, a golden-file assertion pinning the encoded bytes —
  now sees the sub-second digits it previously lost. (ii)
  `CompareAndSet`/`GetOrSet` compare **raw stored bytes**, not decoded
  values (`cache/redis/client.go`, `casScript`: `if current == expected
  then`). In a rolling deploy sharing one Redis, an `expectedValue`
  marshaled by one binary version will not byte-match a sub-second-timestamp
  value written by the other version — the CAS returns `false` (no data
  loss; the swap simply does not apply) until the entry is next rewritten.
  TTL bounds that window only where a TTL is set: `Set` treats `ttl == 0`
  as no expiration, so a persistent entry keeps its old encoding
  indefinitely rather than aging out on its own.
- verify: `go test ./cache/...`; in a consuming application, round-trip a
  `time.Time` with a non-zero nanosecond through
  `cache.Marshal`/`cache.Unmarshal` and assert `Nanosecond()` survives.
- ref: `cache/serialization.go` (`encMode`) · `cache/redis/client.go`
  (`casScript`, `Set`)

### [C581.2] Log sampling gains 0.01% resolution — sub-1% rates stop exporting nothing · silent-behavior · when: match

- detect: `git grep -nE 'samplingrate|SAMPLINGRATE|SamplingRate' -- '*.yaml' '*.yml' '*.go'` — plus any `OBSERVABILITY_LOGS_SAMPLINGRATE` set outside the repo (a Helm value, a task definition, a secret); no repo grep finds those.
- scope: one constant (`samplingDenominator = 10000`) and one constructor-computed field (`sampleThreshold`) in `observability/dual_processor.go`. The sampling comparison scaled from `%100 < uint64(rate*100)` to `%samplingDenominator < sampleThreshold`, where `sampleThreshold = round(rate * samplingDenominator)` is computed once in `NewDualModeLogProcessor` and reused on every call. Config validation is unchanged — `[0.0, 1.0]` is still the accepted range, and no previously-valid value is rejected.
- gate: match = `observability.logs.samplingrate` is set to a value strictly between 0.0 and 1.0. no-match = unset, `0.0`, or `1.0` — those three paths take the unchanged fast paths and are byte-for-byte unaffected.
- after: three consequences. (i) A rate at or above 0.00005 (0.005%) and below 0.01 used to truncate to a zero threshold and export **nothing**; it now rounds to the nearest 0.01%-resolution bucket and exports that fraction — `0.005` goes from 0% to 0.5% of INFO/DEBUG trace logs, a real increase in export volume and cost. A rate below 0.00005 still rounds to a zero threshold and still exports nothing — the floor moved, it did not disappear. (ii) Rates that are not whole percents stop flooring — `0.999` was 99% and is now 99.9%, `0.155` was 15% and is now 15.5%. (iii) The **membership** of the sample changes for every rate at or above 0.00005 and below 1.0, including whole-percent ones: the same expected fraction is kept, but the modulus moved from 100 to 10 000, so which traces land in the sample is redrawn. Sampling stays deterministic per trace after the bump; it is simply not the same set as before. Nothing to do for (iii) — but do not read a trace disappearing from the sample as a regression.
- verify: `go test ./observability/...`; then set `observability.logs.samplingrate: 0.005` and confirm INFO/DEBUG trace logs now reach the backend at roughly 0.5%.
- ref: `observability/dual_processor.go` (`samplingDenominator`, `sampleThreshold`) · [ADR-006](adr_006_otlp_log_export.md)

---

*The sections below are reference material: the two config-key rename lookup tables (linked from atoms C401.1 and C41.7), followed by pre-v0.39 changes retained for consumers upgrading from older releases.*

## Config Keys — Flat-Smushed Rename (ADR-024)

Per [ADR-024](adr_024_config_key_flatsmush.md), 21 snake_case config keys were renamed to the framework's underscore-free flat-smushed convention so they become settable via environment variables (the env loader maps `_`→`.`, koanf's nesting delimiter, so underscored leaf keys were silently unreachable from env). Update both your YAML and any environment variables. Go field names are unchanged.

| Old key (YAML) | New key (YAML) | Old env var (broken) | New env var |
| --- | --- | --- | --- |
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
| --- | --- | --- | --- |
| `observability.metrics.histogram_aggregation` | `observability.metrics.histogramaggregation` | `OBSERVABILITY_METRICS_HISTOGRAM_AGGREGATION` | `OBSERVABILITY_METRICS_HISTOGRAMAGGREGATION` |
| `observability.logs.disable_stdout` | `observability.logs.disablestdout` | `OBSERVABILITY_LOGS_DISABLE_STDOUT` | `OBSERVABILITY_LOGS_DISABLESTDOUT` |
| `observability.logs.slow_request_threshold` | `observability.logs.slowrequestthreshold` | `OBSERVABILITY_LOGS_SLOW_REQUEST_THRESHOLD` | `OBSERVABILITY_LOGS_SLOWREQUESTTHRESHOLD` |
| `observability.logs.sampling_rate` | `observability.logs.samplingrate` | `OBSERVABILITY_LOGS_SAMPLING_RATE` | `OBSERVABILITY_LOGS_SAMPLINGRATE` |

> Unlike the ADR-024 keys (which still bound from YAML and broke only from env), these four never bound from YAML either — a service setting `observability.logs.sampling_rate` silently got the framework default. The recurrence guard now also walks `mapstructure` tags (`config.TestConfigKoanfTagsHaveNoUnderscore`) and a sibling `observability.TestObservabilityConfigTagsHaveNoUnderscore` covers the observability tree.

## Go Naming Conventions (S8179) — Getter Methods

Per [SonarCloud rule S8179](https://rules.sonarsource.com/go/RSPEC-8179/), getter methods should NOT have the `Get` prefix.

| Package | Old Method | New Method |
| --------- | ------------ | ------------ |
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
| --------- | --------------- | --------------- |
| `scheduler` | `Job` | `Executor` |
| `app` | `HealthProbe` | `Prober` |
| `database` | `TenantStore` | `DBConfigProvider` |
| `messaging` | `TenantMessagingResourceSource` | `BrokerURLProvider` |
| `server` | `ResultLike` | `ResultMetaProvider` |
| `cache` | `TenantCacheResourceSource` | `ConfigProvider` |

## Standardized `ToSQL()` Across Query Builders (S8179)

Per [ADR-017](adr_017_insert_query_builder.md), `qb.Insert*` constructors return `types.InsertQueryBuilder` (a go-bricks-owned interface) instead of `squirrel.InsertBuilder` directly. The render method is renamed from `ToSql()` to `ToSQL()` — matching `Select`/`Update`/`Delete`.

| Constructor | Old return | New return | Render method |
| --- | --- | --- | --- |
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
