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
v0.39.1 ─E40─ v0.40.0 ─E401─ v0.40.1 ─E41─ v0.41.0 ─E42─ v0.42.0 ─E43─ v0.43.0 ─E44─ v0.44.0 ─E45─ v0.45.0 ─E49─ v0.49.0 ─E50─ v0.50.0 ─E51─ v0.51.0 ─E52─ v0.52.0 ─E55─ v0.55.0 ─E56─ v0.56.0 ─E57─ v0.57.0 ─E58─ v0.58.0 ─E581─ v0.58.1 ─E59─ v0.59.0 ─E60─ v0.60.0 ─E61─ v0.61.0 ─E62─ v0.62.0
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
| E581 | v0.58.0 → v0.58.1 | silent-behavior | 3 | none | if you cache any type carrying a time.Time, decide before the bump whether a compare-and-set on a sub-second timestamp may fail during the rolling deploy (C581.1); and if `observability.logs.samplingrate` is set to any value strictly between 0.0 and 1.0, expect the exported INFO/DEBUG log volume AND the membership of the sampled set to change: a rate at or above 0.00005 and below 0.01 exported nothing before the bump and starts exporting its configured fraction after it, a rate that is not a whole percent stops flooring (0.999 was 99%, now 99.9%), and every fractional rate redraws which traces land in the sample; a rate below 0.00005, plus 0.0 and 1.0, are unaffected (C581.2); and if you call `Close()` directly on a `DbManager`/`CacheManager`/`messaging.Manager`, know that a handle still borrowed by in-flight work now stays open until its final release instead of closing immediately (C581.3) |
| E59 | v0.58.1 → v0.59.0 | compile-break (C59.2, C59.3, C59.13) + silent-behavior (C59.1, C59.4, C59.14 turns a hand-built config's silently-off secret floor into a startup abort, C59.9 lets a valid Oracle case or whitespace variant build and write, while other newly-accepted spellings still fail later) + breaking (C59.5 rejects a password, C59.6 rejects a dynamic config's TLS material, C59.7 rejects an upsert call, C59.8 rejects another, C59.10 rejects a duplicated conflict column, C59.11 rejects a `database.tls` shape at startup, C59.12 rejects a hand-built config at construction) | 14 | C59.2 C59.3 C59.13 (only partially — a hand-built config that never set the field still compiles; see C59.14) | if your service sits behind a proxy on a **public** address (CloudFront, a partner edge), set `server.trustedproxies` to its CIDR range before the bump — otherwise that proxy is itself returned as the client and every caller behind it collapses into a single rate-limit bucket; and check the load balancer for any mode that writes a non-IP `X-Forwarded-For` entry — on AWS ALB that is `routing.http.xff_client_port.enabled` (appends `client_ip:port`) and, separately, `routing.http.xff_header_processing.mode = remove` — since either keys the entire fleet on the load balancer's own address after the bump and `server.trustedproxies` cannot fix it; the remedy is deployment-side (C59.1); and if any of your code — **including test files**, which `go build` does not compile — implements `cache.Cache`, add the new `CompareAndDelete` method, and before swapping a lock's `Delete` release for it make sure the lock is acquired with a **positive** TTL, since a `ttl == 0` lock that a token-verified release declines to remove is held forever (C59.3); and grep your **test** files for a `cache/testing.MockCache` handed a context that is already canceled or expired while the call is expected to succeed — the mock's cancellation check no longer depends on a configured `WithDelay`, so that call now returns the context's error (C59.4); and if you call `ProvisionPGRoles` or `PGRoleProvisioningSQL` with a `PGRoleSpec` whose `MigratorPassword` or `RuntimePassword` is read from a file, a mounted secret, an environment read, or a command substitution, `strings.TrimSpace` it before the bump — a password containing CR, LF, or NUL is now rejected by `Validate` instead of provisioning, and any credential whose provisioning failure was logged while its password contained a newline should be rotated, since the first line of that secret reached the error string (C59.5); and hand-read every `BuildUpsert` call for a column key present in BOTH `conflictColumns` and `updateColumns` — grep finds the calls but not the overlap, since both maps are usually built dynamically — because on PostgreSQL such a call built and ran before the bump and now returns an error from the builder, while on Oracle it already failed and now fails earlier, at build time with the builder's message rather than at execution with ORA-38104; match keys the way each vendor does, since Oracle folds the unquoted identifiers it emits to upper case (so `id` and `ID` are one column there, and are now rejected) while PostgreSQL quotes every identifier and keeps them distinct; then compare that column's update value against its insert value before remediating — equal means dropping it from `updateColumns` changes no column value — though if it is the column's **only** entry the set empties, which builds `DO NOTHING` on PostgreSQL and drops Oracle's `WHEN MATCHED` arm, so a matched row stops being updated at all, its UPDATE triggers stop firing and `RETURNING` yields no row; keep a real non-conflict column or issue an explicit `UPDATE` — under the same transaction and locking rule as below — where that matters — but differing means the call was rewriting the conflict column on a matched row, which no vendor-portable upsert can express, so those need a separate `UPDATE` rather than a dropped column — run it in the same transaction as the insert, keyed on the conflict columns, and holding the row lock the single statement took for you (`SELECT … FOR UPDATE` or equivalent), because splitting one atomic upsert into two statements lets a concurrent writer interleave and under READ COMMITTED a shared transaction alone does not stop it (C59.7); and if a dynamic multi-tenant `DBConfigProvider` returns a `database.connectionstring` with no `type`, that tenant now dials a real database at first use instead of failing every request with `unsupported database type: ""`, and if you supply `Options.DatabaseConnector` it now receives an inferred `type` for a recognized scheme instead of an empty one — inference is unconditional, since that option's exemption only ever covered the startup guard; then enumerate the same source for any tenant carrying `database.tls.cert`/`database.tls.key`/`database.tls.ca` next to an Oracle type or `oracle://` DSN, or exactly one of `database.tls.cert`/`database.tls.key` on a PostgreSQL one, since those connect today with the TLS material silently dropped and stop connecting after the bump, typed configs included (C59.6); and hand-read the `BuildUpsert` shortlist again for a conflict column that names no column of `insertColumns` by vendor identity — Oracle already rejected those, PostgreSQL let the column fall to its table default, which was inert where the default was absent but is a working pattern where it is a sequence, a `current_setting(...)` or a generated column, and after the bump both vendors refuse it, so a sequence or `current_setting(...)` default means passing the value in `insertColumns` while a generated column — which PostgreSQL forbids writing directly — means `database.Raw` or a schema change; drop any match on the precondition texts too, since `conflict columns required for Oracle MERGE` and `conflict columns required for PostgreSQL upsert` both become `conflict columns required for upsert` (C59.8); and check whether any `BuildUpsert` call can pass the same column twice in `conflictColumns` — duplicates are judged by vendor identity, so an exact repeat on either vendor, or on Oracle a case variant of a non-reserved identifier, which Oracle emits unquoted and folds (its reserved words are quoted and stay case-sensitive, so `["level", "LEVEL"]` is still accepted; on PostgreSQL no case variant is a duplicate) — because both vendors now refuse it where PostgreSQL previously failed only at execution (`42P10`) and Oracle accepted it outright, so de-duplicate at the call site by the vendor's own rules rather than by lower-casing, which is wrong on PostgreSQL (C59.10); and read that shortlist once more for an Oracle call whose conflict column and insert key spell the same column differently — a case variant or a whitespace-padded key — since those were refused at build time and now build and write, with no signal after the bump, so any validation you were getting from that rejection has to move into your own code before it (C59.9); and grep every environment for a `database.tls` block, because four shapes that booted green now abort startup — static configuration at boot, while a dynamic `DBConfigProvider` record with the same shape boots green and fails at first connection acquisition, so check dynamic tenants by acquiring a connection: a PostgreSQL `mode` outside the sslmode allowlist, `cert`/`key`/`ca` under an empty/`disable`/`allow`/`prefer` mode (pgx was discarding the material, allowing a plaintext downgrade, or — for `ca: system` — silently upgrading the mode to `verify-full`; an unset mode defaults to `prefer`, so this is the common case), any `database.tls.*` alongside a `connectionstring`, and — extending C42.1, which said mode alone still passed — `database.tls.mode` on Oracle; setting `verify-ca`/`verify-full` — or `require` plus a `ca` — also makes that connection verify the server for the first time, so confirm the CA chain before the bump, while bare `require` encrypts without authenticating the server (C59.11); and if any code hands `app.NewWithConfig` or a direct `app.Builder` chain a hand-built config, run it against `config.Validate`'s rules before the bump — missing `app.name`/`app.version`, zero server timeouts and invalid vendors now fail construction (C59.12); and if any Go code sets `config.KeyStoreConfig.SecretMinLength`, write `new(0)` / `new(N)` — the field is now `*int` (C59.13); and if a hand-built config with symmetric secrets never set it, it relied on the Go zero silently disabling the floor and now gets 32 bytes, so any secret shorter than that fails startup — grep finds nothing for this shape, read C59.14 |
| E60 | v0.59.0 → v0.60.0 | compile-break (C60.4 — internal helpers nothing outside app/ used; C60.14 — the 33 `config.TestKey*` constants are deleted) + breaking (C60.1 fails a migrate run; C60.15 fails configuration resolution on a numeric key delivered empty; C60.20 does the same for `debug.allowedips`, where an empty value used to disable the IP whitelist while a bearer token kept the deployment booting; C60.18 does the same for a bool key, where an empty value used to decode as `false` — silently turning `database.pool.keepalive.enabled` off and making a failing `cache.critical` probe non-fatal; the documented defaults return only once the operator unsets the key; C60.16 re-addresses a non-root database section's ConfigError.Field, and C60.19 finishes that job at the runtime door — additive `config.ApplyDatabasePoolDefaultsForKey`, no call-site change forced; C60.11 rejects an upsert call whose column keys name one Oracle column twice, that names an Oracle conflict column it also updates under another spelling, or that Oracle's MERGE cannot name — and, on BOTH vendors, one whose key carries an unescaped interior quote; C60.24 rejects an INSERT or upsert call whose table argument is not a bare or qualified name with at most one alias, closing the last five doors that interpolated a table unchecked; C60.26 rejects a SELECT or INSERT COLUMN that is not an identifier or a wildcard, so a function or constant string moves to qb.Expr(); C60.27 finishes the sweep at the Filter and JoinFilter columns, where PostgreSQL previously interpolated the caller's column verbatim; C60.28 validates the table alias handed to `Columns.As`, the one identifier door that PANICS rather than deferring to `ToSQL()`, and moves the refusal from whichever door the rendered column reached to the door that owns the alias; C60.29 finishes it at the RawExpression escape hatch itself, whose alias denylist was skippable by writing a struct literal instead of calling qb.Expr(); C60.12 rejects a negative scheduler timeout and a module Init with no config; C60.22 rejects a trust list covering an ENTIRE ADDRESS FAMILY — a literal default route, a set covering one between its entries, or the v4-mapped `::ffff:0.0.0.0/96` — on all three of `debug.trustedproxies`, `scheduler.security.trustedproxies` and `server.trustedproxies`, where the first two previously accepted even the literal form the third already refused; and rejects a `debug.allowedips` entry that is neither an IP address nor a CIDR range, as well as one that parses as a CIDR but has HOST BITS SET (`192.168.1.55/16`), which used to silently widen the allowed range to `192.168.0.0/16`); C60.23 reports a recovered panic value's TYPE instead of the value on every framework sink — log fields and span text alike; C60.23's `scope` carries the surface-by-surface table, and the rule, not any total, is the contract. **The HTTP lane reaches every service**: a panicking handler's value used to land in the action log line's `error` field and in the server span's status description, both in production posture and ungated by `app.debug`. The error handler's own `Panic recovered` line is narrower — production posture emitted a TYPE there (`error_type`, the value's own error type) and still does, now the constant `*server.panicTypeError`; only `app.debug: true` put the VALUE on that line, in its `error` field, where it now reads `panic (type: T)` — while the audit sink-failure line, the scheduler job-panic line, the settle line, and the shared delivery outcome line on BOTH messaging lanes cover the messaging, scheduler and audit paths — relying on the sensitive-data filter there was never protection, since it masks by FIELD name and the field is `panic`, so a bare `panic("secret")` and any unlisted map key were emitted in clear; C60.21 stops the log filter panicking on a JSON array of objects — any body shaped `{"…":[{…}]}`, which used to crash the log path at BOTH doors and, from the audit emitter's panic-reporting defer, the process — normalizes the needle list at every construction door, so an empty needle no longer masks the entire log stream, changes the SCHEDULER's job-panic summary from `panic: <value>` to `panic (type: T)` — that one breaks silently, so repoint any alert or saved query matching the old rendering BEFORE upgrading or it simply stops firing; makes a panicking audit SINK survivable, where a map-bearing panic value used to kill the process, at the cost of a new `panic_type` field; and makes the masking-disabled WARN judge the EFFECTIVE needle list, so a Go-built list whose entries all normalize away now warns where it used to be silent) + silent-behavior (C60.25 — both identifier renderers double an interior quote, so a name carrying one renders as that name instead of ending the identifier early; C60.2 forwards `database.tls` to Flyway, where nothing reached it before; C60.3 changes strings on `/ready`'s 200 body — including the `db_stats` → `database_stats` rename — and folds two entries out of the debug health view; no status code moves; C60.5 — idle-cleanup sweeps start at manager construction; five startup/shutdown log lines retire; C60.7 — the AMQP failure and panic lines' second `correlation_id` stamp becomes `amqp_correlation_id`; C60.8 — inbound trace identifiers failing validation are discarded and regenerated; C60.9 — the streams lane's consume telemetry and log lines change shape; C60.10 — a published message's `CorrelationId` carries the same id as its own `X-Request-ID` header, or nothing at all where that id fails validation; C60.13 — the default log filter stops masking a field matched only by the removed bare `key` needle; the named ones, `api_key`, `private_key`, `signing_key`, `encryption_key`, still mask; C60.17 — the HTTP ingress `traceparent`/`tracestate`, the reflected response `traceparent` and the classic AMQP lane's `CorrelationId`/`MessageId`/`RoutingKey`/`Exchange` are validated, a value that fails is omitted from every log field, span attribute and metric attribute, and `tracestate` gains a control-byte refusal at every door; the consume lines gain `identity_rejected` and `delivery_tag` so the omission stays searchable; C60.23 — an unwinding panic no longer produces an SDK `exception` event on the span (`WithoutPanicRecording`), so exception-event alerts and counts on panicking spans see a silent drop; the span's error status and the framework's `panic_type` log line remain; C60.22 — `server.ClientIP` answers with an identified untrusted hop or the observed peer and never a caller-written value, so every `X-Forwarded-For` line is read, brackets are stripped, an unparseable hop stops the walk, an all-trusted chain yields the peer, and `X-Real-IP` is not consulted at all; C60.30 — the framework's own `details.error` on an unhandled 5xx or recovered-panic response now requires `app.debug: true` AS WELL AS a development `app.env`, so a development environment running with debug off stops shipping raw error text to the caller, the same way its logs already withheld it) + compile-break (C60.6 — `messaging.StartConsumeSpan` removed; the consumed-messages counter now counts at completion with `error.type`) | 30 | C60.4; C60.14; C60.6 (only partially — the code door is compiler-caught; the telemetry door is silent, see its gate) | if you run the `go-bricks-migrate` CLI, resolve every tenant it reads before the bump — `go-bricks-migrate info` per tenant is the cheap check — because configs it accepted are now validated the way the framework validates them before dialing, which covers the `database.tls` shapes of [C59.11] plus a missing Oracle connection identifier and an unloadable `database.timezone`; the per-tenant AWS Secrets Manager payloads cannot be grepped from your repo, so enumerate the prefix in each account rather than trusting a config-file sweep (C60.1); and grep dashboards, alerts, synthetic checks and contract tests for `db_stats`, `not_ready`, `connection_failed`, `no_active_connections`, `database_manager`/`messaging_manager`, for `messaging_stats`/`cache_stats` pinned to `{}` on a service without that kind, and for `overall_status == unknown` on `/_sys/health-debug` — every kind now speaks one vocabulary (`healthy`, `unhealthy`, `not_configured`, `disabled`, `per_tenant`), a disabled kind's stats read `{"status":"disabled"}`, messaging and cache read `per_tenant` in multi-tenant deployments where they read `not_configured`, and the debug view lists every classic kind; the 200 body's `db_stats` key is now `database_stats`, the debug view's `database_manager`/`messaging_manager` entries are gone (their statistics are the `database`/`messaging` entries' details), and `overall_status` reads `degraded` where it read `unknown` for a non-critical kind that is not live; a critical kind that is down still answers 503 exactly as before (C60.3); and if you resolve per-section database configs yourself, move to the additive `config.ApplyDatabasePoolDefaultsForKey` (the old function still compiles and still answers for the root) — and re-point any `ConfigError.Field` matcher at the key families rather than a literal, since the RUNTIME door now addresses a dynamic tenant exactly as a static one and three sibling tenant-tree spellings (per-tenant cache, the messaging-consistency field, `NewMultiTenantError`'s prose field) join the same rule; the `failed to apply pool defaults for key` wrapper is gone from rendered text, and a non-root section's `Action` now names its OWN variable (`DATABASES_REPORTING_PORT`) — or no variable at all where one cannot reach the key (C60.19). And grep every deployment surface for `DEBUG_ALLOWEDIPS` set to nothing, plus any YAML `allowedips: ""` — that shape used to wipe the loopback default and, with `debug.bearertoken` set, skip the IP whitelist entirely while the service still booted; it now fails startup, and `allowedips: []` is the spelling for a deliberate token-only posture (C60.20). And grep your Go code — test files included — for the sixteen removed app helpers and the eight unexported debug response types (C60.4). And grep log-based alerts and saved queries for the five retired cleanup-loop lines (`Starting/Stopping database manager cleanup loop`, `Starting/Stopping messaging manager cleanup loop`, `Manager cleanup loops stopped`) — they have no renamed equivalent (C60.5). And grep your Go code for `StartConsumeSpan` and your dashboards for `messaging.client.consumed.messages` — the call is a build break, the counter now increments at completion and carries `error.type` on failure (C60.6). And grep log-based alerts, saved queries and log-parsing tests for `correlation_id` on the AMQP consumer's failure and panic lines — the delivery's own AMQP `CorrelationId` is stamped as `amqp_correlation_id` there now, and `correlation_id` carries only the framework trace ID (C60.7). And search your log backend for `correlation_id` values that are empty, longer than 128 characters, or contain anything outside `A-Za-z0-9_-` — those identifiers are now discarded at ingress and replaced by a framework-minted one, so correlation with the upstream that emitted them breaks; that query covers the X-Request-ID half only, so also check the gateway emitting your `traceparent`/`tracestate`, which are now validated too and show up in no log field (C60.8). And if you consume native streams, grep dashboards and log queries for the streams consumer's failure and panic lines and for its span attributes — the lane now runs on the shared delivery pipeline, so its lines gain the spine, its panic line's wording changes, and its span gains the four shared attributes (C60.9). And if anything downstream reads a message's AMQP `CorrelationId` property rather than its `X-Request-ID` header, that property now carries the same aligned trace id the header carries — a 32-hex traceparent trace-id where an HTTP-originated publish used to put the request's UUID (C60.10). And if you build `config.Config` in Go or call `scheduler.Module.Init` / `app.NewModuleRegistry` yourself, check both `scheduler.timeout.*` keys: zero now normalizes (30s/25s, where the module used to fall back to 30s for slowjob), a negative value fails validation, and a module handed no config fails Init (C60.12). And if you build Oracle upserts, grep `BuildUpsert` call sites for column maps assembled dynamically — two keys that fold to one Oracle column, a conflict column spelled differently from the insert or update key naming that same column, and dotted or function-shaped keys, are now build-time errors rather than SQL Oracle rejects at parse or at execution (C60.11); on PostgreSQL the only new rejection is a key carrying a quote that is not doubled, which used to leave the identifier and become SQL, and no identity rule changes there. And grep your Go code for `ConfigError.Field` compared to a literal `database.*` key: a non-root database section now reports `databases.<name>.host` / `multitenant.tenants.<id>.database.host` there instead of the root spelling, and its message loses the `databases.<name>: ` prefix (C60.16). And read C60.13 before upgrading if you log any field whose name contains `key`: the bare `key` needle is gone from the default log filter, so a field matched only by it — an identifier like `tenant_key`, but also a secret the new list does not name, such as `license_key` — logs in clear until you add a needle via `log.sensitivefields`; the named needles (`api_key`, `private_key`, `signing_key`, `encryption_key`, each in three spellings) still mask. And grep your Go code for the `TestKey` identifier itself — not for a `config.` qualifier, which an aliased or dot-imported reference does not carry; C60.14 has the exact pattern, whose alternation cannot live in this cell. Those 33 constants are deleted, and five of them named keys the loader never read, so C60.14's table tells you which value to inline instead of the one they held. And grep every deployment surface — Helm values, Kustomize overlays, `.env` files, rendered manifests — for a go-bricks variable set to nothing (`FOO=`, `FOO=""`, a structured `value: ""`, a `secretKeyRef` whose stored value is empty, an `envsubst` over an unset variable): a numeric key delivered empty used to decode as `0` and now fails naming the key, which is how `KEYSTORE_SECRETMINLENGTH=` silently disabled the secret-length floor; then resolve the two seams no environment sweep reaches — the CLI's `tenants.yaml` and every stored `DBConfigProvider` payload — because those decode at first use, so a clean environment and a green startup say nothing about them (C60.15). And read that same sweep's hits a second time for BOOL keys, which now fail identically: `DATABASE_POOL_KEEPALIVE_ENABLED=` was a default-true → false flip that turned TCP keep-alive off, `CACHE_CRITICAL=` disabled ADR-046's strict readiness so `/ready` answered 200 through a cache outage, and `SERVER_LOGROUTES=` is the third pointer key; the non-pointer bools were landing on `false`, their own default, so those fail loudly where the behaviour was benign — decide per key whether you meant unset (restores the default) or an explicit `false` (C60.18). And search the log backend once more for a `traceparent` field that is not spec-exact and for `amqp_correlation_id`/`message_id`/`routing_key`/`exchange` values a foreign publisher or a non-ASCII exchange name put there, and re-check dashboards filtering `messaging.rabbitmq.exchange` or grouping by `messaging.destination.name`, which is built from the exchange and the routing key — those are validated at the HTTP door and across the AMQP delivery identity now, and a value that fails is omitted from the log field, the span attribute and the metric attribute rather than emitted; a field the delivery never carried is absent rather than empty, so a saved query asserting one of them is always present needs repairing (C60.17). And if you type-assert the result of the public `logger.FilterValue` to a concrete slice type, or build a needle list in Go through `app.Options.LoggerFilterConfig` / `logger.NewSensitiveDataFilter` — or in YAML through `log.sensitivefields` — read C60.21: a slice whose elements the filter rewrites now emits as `[]any` (serialized output unchanged), and an empty or whitespace-only needle in a Go-built list is normalized away rather than taken literally — YAML `log.sensitivefields` is NOT affected, it was already normalized before this hop, and a duplicate needle never mattered on either side, so a deployment that carried a stray empty entry has been masking EVERY field of every log line and will start emitting them again — re-read your effective needle list against what the service actually logs before deciding that is fine, and note a list that normalizes away ENTIRELY now WARNs at startup — the only signal it masks nothing, and suppressed at `log.level: error`. Two more from C60.21 need action before the bump. **Grep for `json.RawMessage` and named `[]byte` types reaching `Interface()`/`WithFields()`**: that shape used to PANIC the log path and now renders in clear, the one place this hop leaves you worse off — C60.21 detect step (4) has the two greps and apply (e) the three remedies. And if you register an `AuditRecorder`, the sink-failure line gains a `panic_type` field and a second message for an unrenderable value, so repoint anything matching it. When you audit `FilterValue` consumers, remember a TYPE SWITCH is affected as much as an assertion and fails silently into `default` rather than loudly. And repoint anything reading the `panic` field on the audit sink-failure, scheduler job-panic, delivery settle and shared delivery outcome lines — all now carry `panic_type` (the Go type) and no value, and the audit line's `(value unrenderable)` message is retired with no successor. **And the HTTP surfaces are the ones to check even if you use no messaging at all** — work C60.23's `scope` table, which lists them: the action log line's `error`, the error handler's `Panic recovered` line, the `unhandled error` line under `app.debug`, and the server span's status description all change for any panicking handler, so a service with no consumers, no scheduled jobs and no `AuditRecorder` is still affected, and clearing one of them says nothing about the rest. **The messaging pair is the one to check even if you register no `AuditRecorder` and run no scheduled jobs** — it fires whenever a message handler panics, and the delivery spine's key set is a contract your own lane-shape tests may pin. **Then repoint the SPAN side separately, because no log grep reaches it**: both messaging lanes' `exception.message` and span status description now read `panic in message handler (type: T)` instead of carrying the value, and the scheduler's cleanup-job error does the same — a shop that alerts on traces rather than logs matches nothing in the log sweep and is still fully affected. That span text also means a handler panicking with a credential has been shipping it to your tracing vendor in every prior release; upgrading closes it and does not clean up what already went. Every one of these breaks SILENTLY — the query stops matching and the alert never fires again (C60.23). And hand-read every `qb.Select(...)` taking MORE THAN ONE string argument before the bump — a grep over `Select("…")` only sees the first argument, so a function string in any later position (`Select("department", "COUNT(*)")`) is invisible to it and is exactly the shape that now fails; the same applies to `InsertWithColumns`, `.Columns(...)` and `.SetMap(...)` column lists, and the remedy is `qb.Expr(...)` in every case, with a function-plus-alias splitting into `Expr(sql, alias)` (C60.26). `BuildUpsert` calls whose TABLE argument is not a literal — a parameter, a config value, a struct field or a concatenation — because those are now refused unless the value is a bare or qualified name with at most one alias, reported as `BuildUpsert`'s own third return value rather than from `ToSQL()`, which this door never reaches; a dynamic table needs an allowlist your own code owns, since the builder will not accept a computed one (C60.24). And grep every deployment surface for `DEBUG_TRUSTEDPROXIES` / `SCHEDULER_SECURITY_TRUSTEDPROXIES` / `SERVER_TRUSTEDPROXIES` (and their YAML spellings) — all three, and read the values rather than only the keys. Carrying `0.0.0.0/0` or `::/0`: those two keys accepted a default route that `server.trustedproxies` already refused, and a default route makes every peer a trusted proxy, so a DIRECT caller's forwarding headers were believed by the debug allowlist and by `/_sys/job` — no proxy transit required. They now fail startup. Check the VALUES on all three keys while you are there, `server.trustedproxies` included: a list whose entries together cover a family (`["0.0.0.0/1","128.0.0.0/1"]`) and the v4-mapped `::ffff:0.0.0.0/96` are newly refused everywhere, so setting the three consistently is not the exemption it was for a literal default route. Check `debug.allowedips` on BOTH new rules in the same pass (each newly a startup error rather than a silent deny-all): an entry that is neither an IP address nor a CIDR range, and one that parses cleanly as a CIDR but has host bits set (`192.168.1.55/16`) — clearing the first does not clear the second. Note the allowlist keys are deliberately still allowed to hold `0.0.0.0/0` (C60.22). And if any environment runs a development `app.env` alias (`development`, `dev`, `local`) with `app.debug` false or unset, grep contract tests, frontends and curl scripts for a reader of `details.error` on a 5xx body — that entry now vanishes there; setting `app.debug: true` restores it, and production is unchanged (C60.30). And `git grep -nE 'RawExpression\{' -- '*.go'` — every struct literal is now validated where it is consumed, so one with an empty SQL body or an alias carrying `;`, `'`, `"`, `--`, `/*` or `*/` fails from `ToSQL()` where it used to render; values from `qb.Expr`/`qb.MustExpr` are unaffected (C60.29). And `git grep -nE '\.As\(' -- '*.go'` — every `Columns.As` argument whose RUNTIME VALUE is not a single bare identifier, or the framework's own quoted form, now panics at the `As` call with `*dbtypes.InvalidAliasError` where it used to render. A literal `"u"` still passes, and so does a parameter that carries `"u"`; what fails is the value, not the fact that it is computed — a concatenation, a config value or an argument carrying surrounding whitespace fails only when it actually produces one. So read each NON-LITERAL call site for the values it can carry, and each literal against the grammar; a request-derived alias needs an allowlist your own code owns (C60.28). |
| E61 | v0.60.0 → v0.61.0 | compile-break (C61.1 — `server.FieldError.Value` removed) + silent-behavior (C61.2 — every response `error.details` map now requires `app.debug` as well as a development `app.env`, at every status and on both the enveloped and raw renderers; a bind failure's detail becomes a payload-free summary) + silent-behavior (C61.6 — Oracle reserved-word columns are quoted on `Insert().Columns` and `.SetMap`, which used to emit SQL Oracle rejected) + silent-behavior (C61.3 — every framework span sink drops `exception.message` and reports the error's Go type) + silent-behavior (C61.5 — `InsertStruct` and `SetStruct` render columns in sorted order) + silent-behavior (C61.7 — a caller-quoted identifier containing a dot renders as one name instead of being split into segments) + silent-behavior (C61.10 — the three identifier validators return the normalized identifier, so a padded one renders trimmed) + compile-break + breaking (C61.9 — `ErrDangerousAlias` is removed, which the compiler catches; a `RawExpression` alias must now be an unquoted identifier, which it does NOT) + silent-behavior (C61.8 — `Having` accepts a `qb.Expr()` RawExpression, and a STRING predicate passed to `Having` joins the `// SECURITY:` annotation rule) + silent-behavior (C61.11 — `jf.Eq` renders nil as `IS NULL` and a slice or array as `IN (…)`, as `f.Eq` always has; ordering refuses nil and list operands at `ToSQL` while a scalar is bound whatever its Go type; and the six `jf` compare doors bind a `driver.Valuer`'s resolved value rather than the wrapper) + silent-behavior (C61.16 — `f`'s nine value doors (`Eq`, `NotEq`, `Lt`, `Lte`, `Gt`, `Gte`, `In`, `NotIn`, `Between` — excluding `Like`) and both families' `In`/`NotIn`/`Between` resolve the operand nil-first too, so a nil pointer to a `driver.Valuer` type stops panicking, `f` ordering and both `Between` refuse nil and list operands with the shared sentinel, every one of those doors binds the resolved value, and a `[]byte` renders `IN (?)` at `In` and `NOT IN (?)` at `NotIn` rather than `= ?` and `<> ?`) + silent-behavior (C61.13 — `LogEvent.Err` masks the error field when the operator's `log.sensitivefields` contains a needle that substring-matches `error`, which zerolog's own `Err` never did) + breaking (C61.4 — the framework builds the PostgreSQL Flyway JDBC URL and passes `-url=`, outranking any `flyway.url` in your `flyway.conf`, and FAILS a migrate run on a config v0.60.0 accepted: mTLS material, a host outside the identifier grammar, a partially filled block, a `port` below 0 or above 65535 (`0` stays unset), a `tls.mode` outside the libpq six, a `tls.ca` without a verifying mode or spelled `system`, or `tls.*` beside a `connectionstring`) + silent-behavior (C61.12 — a panic in a middleware registered outside `Recover`, a consumer-supplied tenant resolver being the reachable one, is answered with the standard 500 envelope and one ERROR line naming the panic's TYPE, where it used to unwind into net/http, which printed the VALUE to stderr and dropped the connection) + silent-behavior (C61.14 — a caller-set `traceparent` is validated before `trace.InjectIntoHeaders` re-emits it, so a malformed one — planted in `PublishOptions.Headers` or persisted in an outbox row — is replaced by the context's parent or a generated one, its `tracestate` is dropped with it, the `tracestate` beside an ACCEPTED pre-set traceparent is no longer overwritten by the context's, and the outbound `X-Request-ID` is derived only from a hex trace-id) + breaking (C61.15 — `BuildUpsert`'s conflict, insert and update column keys are trimmed before they are judged, rendered and compared, and one acceptance rule replaces the per-vendor pair: PostgreSQL refuses a qualified, function-shaped or empty key it used to render, and both vendors refuse a key carrying a doubled quote) + silent-behavior (C61.17 — a publish whose exchange, routing key or any header key exceeds the 255-byte AMQP shortstr limit is refused with `ErrInvalidPublishDestination` before any channel attempt, where it used to reach the frame writer and take down the connection every publisher shares; the same rule fails an over-long declared name at startup) + silent-behavior (C61.18 — the log filter parses an opaque JSON payload and masks inside it, byte-exact when nothing matched, with JWK private members and PEM private-key blocks masked by shape and an unreadable or over-cap payload masked whole) + silent-behavior (C61.19 — an outbox event whose exchange, routing key or header key exceeds 255 bytes is refused before the INSERT, an over-long `outbox.defaultexchange` fails startup, and a row the broker can never accept is dead-lettered at `outbox.maxretries` instead of being retried forever) + silent-behavior (C61.20 — the observability phase of `App.Shutdown` no longer folds its error into what `App.Run()` returns: a provider `Shutdown` failure is one WARN line and shutdown continues, where a downed collector used to make a graceful shutdown exit non-zero) + silent-behavior (C61.21 — a panic in a consumer-supplied factory behind any pool-backed manager is recovered in the resource pool's singleflight closure and fails that acquisition with a type-only error, where it used to re-panic on a singleflight goroutine no recover could catch and take the process down, printing the VALUE to stderr) + breaking (C61.22 — a key under `databases`, `multitenant.tenants` or `keystore.keys` that is not `^[a-z0-9-]+$` fails startup, because the env transform cannot address it and its variable silently reaches a different key) + silent-behavior + compile-break (C61.23 — the outbox ledger gains a per-ledger `seq` it is drained in order of, a `lane` column with a native super-stream lane behind `outbox.superstreams`, and a `<table>_leader` row taken `FOR UPDATE NOWAIT`, so ONE relay instance per ledger drains at a time and a failed row parks its key's later rows for the cycle without advancing their retry_count; `config.OutboxConfig` gains a `[]string` and stops being comparable, `outbox.Store` gains `Lead`, and `outbox.tablename` is bounded at 49 bytes for its last segment) + breaking (C61.24 — the per-key cache factory addresses its four config errors to the resource key that produced them, so a tenant's runtime cache failure reads `multitenant.tenants.<id>.cache[.<leaf>]` with the tenant's own env hint, as the startup door already did; the empty key is the root and is byte-identical) + compile-break + breaking (C61.25 — `app.StreamDeclarer` and `ModuleRegistry.DeclareStreams` are removed; `messaging.streams.uri` set without importing `messaging/streams` fails startup naming the import) | 25 | C61.1, C61.23, C61.25 (only the compile half — an outside `outbox.Store` missing `Lead`, and code COMPARING two `config.OutboxConfig` values, both stop compiling; the schema, the single-drainer change and the parking are RUNTIME and the compiler sees none of them), C61.9 (only partially — the deleted `ErrDangerousAlias` is compiler-caught; the alias grammar itself fails at `Expr()` or `ToSQL()` at RUNTIME, so the alias sweep cannot be skipped) | if you read `FieldError.Value` — a client rendering the rejected value back to a user, a contract test pinning the key — grep your Go code AND your response fixtures for it, because the field is gone from the wire shape as well as the struct and the value has no replacement (C61.1); and if any environment runs a development `app.env` alias with `app.debug` false or unset, expect `error.details` to vanish from EVERY error status there, not only the 5xx that `[C60.30]` covered — validation error lists and captured stack frames included; set `app.debug: true` in that environment to get them back (C61.2); and re-read any smoke test or debugging script that matched a bind failure's `details.error` against decoder text, since it now reads `json: type mismatch (want int, offset N)` or `failed to bind query param "ratio"` (C61.2); and if you run Oracle, grep for `qb.Insert(` chains whose `.Columns`/`.SetMap` name a reserved word such as `level` or `comment` — those statements failed with ORA-00904 before and now render quoted and run, so drop any pre-quoting workaround you added (C61.6); and grep your TRACING backend's monitors, dashboards and saved queries for `exception.message` or a message-matched span status description on the job, delivery, HTTP-client and publish spans, since those attributes are gone and such a query silently stops matching (C61.3); and if any golden file or contract test pins the SQL emitted by `InsertStruct` or a field-list-free `SetStruct`, expect a ONE-TIME diff to sorted column order, after which the text stops moving between processes (C61.5); and grep for caller-quoted identifiers carrying a dot (`"my.col"`), which stop being torn into separately-quoted halves (C61.7); and if any identifier you hand the query builder is COMPUTED rather than typed — read from config, a CSV header, an HTTP parameter — a padded one now renders trimmed, so anything reading the emitted SQL back sees the trimmed spelling (C61.10); and grep your `Expr(`, `MustExpr(` and `RawExpression{` sites, reading every ALIAS argument, because one carrying a space, a parenthesis, a newline or the quoted form now fails at `Expr()` and at `ToSQL()` where the six-substring denylist tolerated it — and `git grep -n 'ErrDangerousAlias'`, which is deleted in favour of `errors.Is(err, dbtypes.ErrInvalidAlias)` (C61.9); and grep your own `Having(` call sites, because a STRING predicate there now owes the same `// SECURITY: Manual SQL review completed` annotation `f.Raw`/`jf.Raw`/`database.Raw` do — nothing breaks, but the convention gate and any reviewer checklist now count four doors (C61.8); and grep `jf.Eq`/`jf.NotEq` for operands that can be NON-SCALAR — nil, a slice, an ARRAY, a typed nil pointer, or a `driver.Valuer` reporting NULL — which start working, and `jf.Lt`/`Lte`/`Gt`/`Gte` for the same five forms, which start erroring; the C61.11 gate lists them with the reason the last two count — and separately, at ALL six of those doors a `driver.Valuer` HOLDING a value now binds the value it resolves to rather than the wrapper, which changes nothing the driver sees but does move `ToSQL`'s argument list, so re-record any golden file or contract test that pins those args; and read every nil guard around a `jf` call before deleting it — one written to dodge the old `col = ?`-bound-to-NULL rendering is now redundant, but one that deliberately OMITS the predicate for an absent operand must STAY, since an unguarded nil now MEANS `IS NULL` and removing the guard narrows the query instead of preserving it; and a nil POINTER to a Valuer type (`(*sql.NullString)(nil)`) now renders `IS NULL` at `jf.Eq` where it used to PANIC inside `ToSQL`, so drop any nil guard you wrote around such a `jf` call — and C61.16 extends the same resolution to `f`, `jf.In`, `jf.NotIn` and `jf.Between` on this same hop, so read C61.11 and C61.16 together and take the end state from C61.16 (C61.11); and grep those same call sites for the four shapes C61.16 moves — a nil POINTER to a `driver.Valuer` type at ANY `f` door or at `In`/`NotIn`/`Between`, which stopped crashing (at `ToSQL` for the compare and `Between` doors, at EXEC for the list doors), and a typed nil pointer, untyped nil, a slice, an array or a NULL-reporting `driver.Valuer` at `f.Lt`/`Lte`/`Gt`/`Gte` or either family's `Between`, which now return `dbtypes.ErrOrderingOperandNotComparable` where they rendered `col < ?` or squirrel's own message — repoint any match on squirrel's text at the sentinel, and re-record any golden file or contract test pinning `ToSQL`'s args at those doors, which now hold the RESOLVED value, or pinning the SQL of a `[]byte` passed to `In`/`NotIn`, which becomes `IN (?)` / `NOT IN (?)` (C61.16but keep it where the same operand reaches `f`, `jf.In`, `jf.NotIn` or `jf.Between`, which still panic (C61.11); and if you migrate PostgreSQL with go-bricks, `grep -n 'flyway.url' <your flyway.conf(s)>` — a URL there is now silently outranked by the framework's `-url=`, so delete it or confirm it matches `database.*`, and check your database config for `tls.cert`/`tls.key`, for any `tls.*` set beside a `connectionstring`, for a `port` below 0 or above 65535 (a NEGATIVE one used to migrate silently against the driver default; `0` still means unset), for a `tls.mode` that is not one of the libpq six once trimmed (case-sensitive), and for a `tls.ca` set without `verify-ca`/`verify-full` or spelled `system` (this one binds validated configs too, since `require` + `ca` passes `config.Validate`), all of which now fail the migrate outright — the latter only where the config bypasses `config.Validate`, i.e. a dynamic `DBConfigProvider` or the CLI's `tenants.yaml` (C61.4); and read your `log.sensitivefields` for an entry that substring-matches `error` — if one is there, every framework `Err` line starts rendering the mask value instead of the message, which is the masking those lines should always have had and which `Str("error", …)` already applied (C61.13); and grep your stdout/stderr stream — not your structured log stream — for `http: panic serving`, and your clients for an EOF on a route whose access log shows no line at all: both are the same event, and those requests now answer 500 with the standard envelope and an ERROR line carrying `panic_type`, so repoint any alert keyed on the connection symptom and fix the panics the search surfaces (C61.12); query your outbox table for persisted traceparents outside the version-00 hex grammar while you are there, since those rows re-emitted their value on every relay cycle until this hop and now republish under a regenerated parent (C61.14) and grep your `BuildUpsert(` call sites, reading the conflict slice and both maps' KEYS: a qualified (`t.name`), function-shaped (`COUNT(*)`) or empty key that built on PostgreSQL now errors there as it always did on Oracle, a key spelling an interior quote as a doubled one (`a""b`) now errors on Oracle too, and a key that can arrive PADDED because it is computed rather than typed now names the trimmed column on both vendors — which matches a padded conflict key to its insert key, rejects a map holding both spellings, and moves the PostgreSQL SQL any golden file pins (C61.15); grep your logs for `exceeds 255 bytes` to find the calls that already hit it (C61.17); and log pre-encoded bodies — a `json.RawMessage`, `[]byte`, `[]json.RawMessage` or a JSON-looking string through `.Interface()`, `.Bytes()` or `WithFields` — expect the needle list to reach INSIDE them now, so a `{"password":…}` body starts rendering masked where it logged in clear, a JWK's `d`/`p`/`q`/`dp`/`dq`/`qi`/`k`/`oth` and a PEM `PRIVATE KEY` block mask by shape under any field name, and a JSON-looking payload that will not parse or exceeds the new 64 KiB `FilterConfig.MaxPayloadBytes` masks WHOLE; a payload with nothing to mask is byte-identical, but a MASKED one is re-encoded, so re-record any fixture pinning its bytes (C61.18); and if you use the outbox, read every `OutboxEvent` whose `Exchange`, `RoutingKey`, `EventType` or `Headers` keys are computed rather than literal, and check `outbox.defaultexchange` in every environment — a value over 255 bytes now fails `Publish` before the INSERT (and, for the default exchange, fails startup) where it used to be persisted, and a row already in the ledger with such a destination now parks as `status = 'failed'` at `outbox.maxretries` instead of staying `pending` forever, so re-drive those rows the way you re-drive other failed ones (C61.19); and if anything keys on `Run()` returning an error, or on a non-zero exit, when the collector is down at shutdown — a deploy health check, a CI smoke test, an alert on the `Failed to shutdown observability` ERROR line — it stops firing: that failure is now a WARN line reading `Observability shutdown failed; telemetry may have been lost` and the shutdown succeeds (C61.20); and grep that same stdout/stderr stream for a bare `panic: ` line whose stack runs through `singleflight`, and your orchestrator for a restart with no shutdown line before it, since a panic in a factory you supply to a pool-backed manager — a dynamic `DBConfigProvider` is the common one — now fails that acquisition with a type-only error instead of taking the process down, so repoint any alert keyed on the restart and fix the panics the search surfaces (C61.21); and read the KEYS you chose under `databases`, `multitenant.tenants` and `keystore.keys` in every config file and overlay — one carrying an underscore, an uppercase letter or anything else outside `[a-z0-9-]` now FAILS startup naming that key, and renaming it moves its environment variables and every `deps.DBByName`/keystore/tenant lookup with it; a hand-built `config.Config` is judged too, dynamic tenant providers are not, and if you rename to a hyphen check that whatever sets the variable allows `-` (Docker and Kubernetes do, POSIX `export` does not) (C61.22); and if `outbox.enabled` is true ANYWHERE, read C61.23 before deploying: with managed migrations the `ALTER`, the EXPLICIT `seq` backfill and the index must run in that order BEFORE the new relay starts, or the pending backlog drains once in an arbitrary order and nothing reports it; grant the relay role `SELECT … FOR UPDATE` on the new `<table>_leader` table; expect exactly ONE replica per ledger to drain from now on, with the others logging `another instance leads this ledger` at DEBUG, so an alert keyed on every replica reporting a cycle now fires falsely; expect a failing row to HOLD its key's later rows without advancing their `retry_count`, which a dashboard reading that count as liveness will read as a stall; and `git grep -n 'outbox[.]Store'` for an outside implementation plus any comparison of two `config.OutboxConfig` values, both of which stop compiling (C61.23); and if you are multi-tenant or run a dynamic `ResourceSource`, read every matcher your code has on a cache `ConfigError.Field` — an equality or `cache.`-prefix test now misses a tenant's failure, which reads `multitenant.tenants.<id>.cache[.<leaf>]` — and repoint any runbook naming `CACHE_REDIS_HOST` or `CACHE_ENABLED` as the fix for ONE tenant, which configures the root cache and leaves that tenant broken (C61.24); and if you consume native streams or set `messaging.streams.uri`, import `github.com/gaborage/go-bricks/messaging/streams` (a blank import is enough) — a leftover URI without that import now fails startup, and `app.StreamDeclarer` / `ModuleRegistry.DeclareStreams` stop compiling (C61.25) |
| E62 | v0.61.0 → v0.62.0 | breaking (C62.1 — the exact spelling `Local` fails `config.Validate` on `scheduler.timezone`, `database.timezone` and every named or per-tenant database timezone key, and at the runtime door a dynamic `DBConfigProvider` and the `go-bricks-migrate` CLI go through; `"-"` is the only documented opt-out spelling — host-local on the scheduler, the server's default zone on a database session — and `local`/`LOCAL` were already refused) + silent-behavior default flip (C62.2 — an absent `cache.critical` now leaves the cache probe NON-critical, so `/ready` stays `200` through a Redis outage that answered `503` on every replica under v0.61.0; the explicit-`false` startup WARN is gone) + breaking (C62.3 — a `keystore.secretminlength` below 32, the former `0` opt-out included, fails startup) | 3 | none | grep every deployment surface — YAML, `.env`, Helm values, Vault and AWS Secrets Manager payloads, the CLI's `tenants.yaml` — for a timezone key set to `Local` and rewrite it to `"-"` where host-local was meant or to the IANA zone where it was not; on a database key note `"-"` means the SERVER's default zone, not the application host's (C62.1); and if any cache-enabled deployment leaves `cache.critical` unset and relies on the v0.61.0 default to take a replica out of rotation during a Redis outage — a rate limiter that must fail closed, a session store, an idempotency ledger — set `cache.critical: true` BEFORE the bump (the key is a no-op on v0.61.0, so it can ship ahead); and grep alerting for the `cache.critical is explicitly false` WARN line, which stops firing (C62.2); and read every environment's `keystore.secretminlength` — YAML, `KEYSTORE_SECRETMINLENGTH`, and any Go literal setting `SecretMinLength` — for `0` or any value below 32: those now fail startup, and a symmetric secret shorter than 32 bytes has no keystore path any more, so a partner key that short must be loaded outside `keystore` before the bump (C62.3) |

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
- gate: no-match on (b) = you are on the pre-bump behavior, where the cache probe's result
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
- scope: single-tenant consumer bootstrap (`MessagingInitializer.PrepareRuntimeConsumers`
  at v0.57.0; the unexported `App.prepareRuntimeConsumers` since v0.60.0, [C60.4])
  previously logged one WARN
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
- ref: `app/messaging_setup.go` (`prepareRuntimeConsumers`) · `app/lifecycle.go`
  (`prepareRuntime`, `assertMessagingConfiguredIfDeclared`)

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
  them changes no behavior — only whether code naming them compiles (C58.2).
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
- scope: three exported `time.Duration` constants in `server/constants.go` — `TestShortTimeout` (100ms), `TestMediumTimeout` (1s), `TestLongTimeout` (5s). No framework code ever read them, so nothing changes behaviorally; only code that names them stops compiling. The `Default*Timeout` constants in the same file (`DefaultReadTimeout`, `DefaultWriteTimeout`, `DefaultIdleTimeout`, `DefaultShutdownTimeout`, `DefaultAPITimeout`) are untouched
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
  nearest 0.01%-resolution bucket after it, a rate that is not a whole percent stops
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
- after: three consequences. (i) A rate at or above 0.00005 (0.005%) and below 0.01 used to truncate to a zero threshold and export **nothing**; it now rounds to the nearest 0.01%-resolution bucket; the exported rate is the rounded bucket, not always the configured fraction — `0.005` goes from 0% to 0.5% of INFO/DEBUG trace logs, a real increase in export volume and cost. A rate below 0.00005 still rounds to a zero threshold and still exports nothing — the floor moved, it did not disappear. (ii) Rates that are not whole percents stop flooring — `0.999` was 99% and is now 99.9%, `0.155` was 15% and is now 15.5%. (iii) The **membership** of the sample changes for every rate at or above 0.00005 and below 1.0, including whole-percent ones: the same expected fraction is kept, but the modulus moved from 100 to 10 000, so which traces land in the sample is redrawn. Sampling stays deterministic per trace after the bump; it is simply not the same set as before. Nothing to do for (iii) — but do not read a trace disappearing from the sample as a regression.
- verify: `go test ./observability/...`; then set `observability.logs.samplingrate: 0.005` and confirm INFO/DEBUG trace logs now reach the backend at roughly 0.5%.
- ref: `observability/dual_processor.go` (`samplingDenominator`, `sampleThreshold`) · [ADR-006](adr_006_otlp_log_export.md)

### [C581.3] Manager `Close()` defers a still-borrowed handle to its final release instead of force-closing it · silent-behavior · when: always

- detect: no grep locates this from a consumer repo — the call sites this atom
  governs (`database/manager.go`, `cache/manager.go`, `messaging/manager.go`) live
  inside the go-bricks module, not your application. In your own application,
  audit direct callers of `DbManager.Close` / `CacheManager.Close` /
  `messaging.Manager.Close` (most apps never call these — `deps.DB/Cache/Messaging`
  callers are unaffected, since lifecycle `Close()` is framework-invoked at
  shutdown), then hand-audit any shutdown test that asserts every tracked handle is
  closed *immediately* after `Close()` returns, with no intervening release. This
  is a manual audit, not a search-and-fix.
- scope: `internal/resourcepool.Pool.Close`'s drain loop now splits by live borrowers
  instead of closing every entry unconditionally (Plan 115); all three managers reach it
  through `Close()` unchanged, and `Close()` itself needed no code changes beyond its doc
  comment. `Stats()` did change: `DbManager` and `messaging.Manager` gained an `"errors"`
  key surfacing `PoolStats.Errors` (`CacheManager.Stats().Errors` already exposed it), so
  the deferred-close failure in consequence (ii) below is observable, not merely counted.
- gate: always — every deployment that shuts down gracefully is affected. Whether the
  deferred branch actually fires depends on whether a handler is still mid-operation at
  the instant `Close()` runs, but the contract change (`Close()` may return before every
  handle is closed) applies to 100% of deployments, not a conditional subset.
- after: three consequences. (i) an in-flight AMQP/scheduler handler holding a leased
  handle no longer gets `sql: database is closed` (or an equivalent closed-client error)
  mid-work — the handle stays open until the handler's `ReleaseFunc` runs, which is the
  reason for the change (see ADR-032's 2026-08-09 amendment and issue #606). (ii) a close
  failure on such a still-borrowed handle is no longer part of `Close()`'s returned
  `error`; it surfaces later in `cacheManager.Stats().Errors`,
  `dbManager.Stats()["errors"]`, or `messagingManager.Stats()["errors"]`. Code
  that treats `Close()`'s return as the complete error set for that shutdown must read
  stats after the last lease releases, not immediately after `Close()` returns. (iii)
  `Close()` does not join in-flight work — it does not wait for outstanding leases, it
  only stops accepting new borrows and closes what is already idle. Callers that used
  `Close()` itself as the shutdown barrier must now run every outstanding `ReleaseFunc` —
  or, on framework-managed call paths, wait for the automatic per-unit-of-work scope
  release (ADR-032) — before treating shutdown and the final manager-specific
  error-statistic read as complete.
- verify: `go test -race ./database/... ./cache/... ./messaging/... ./internal/resourcepool/...`;
  in a consuming application, hold a leased handle open across a `Close()` call and
  assert the handle stays usable until released, then closes exactly once on release.
- ref: `internal/resourcepool/resourcepool.go` (`liveLeases`, `Close`) ·
  `database/manager.go` (`Close`, `Stats`) · `cache/manager.go` (`Close`) ·
  `messaging/manager.go` (`Close`, `Stats`) ·
  `wiki/adr_032_lease_refcount_tenant_handles.md` (2026-08-09 amendment)

## E59 · v0.58.1 → v0.59.0 — the client IP is derived through trusted proxies, not raw `X-Forwarded-For` + consumers carry per-consumer AMQP arguments, so three messaging structs stop being comparable + `cache.Cache` gains `CompareAndDelete` + five new rejections at the boundary: control characters in a role password, a dynamic config whose vendor rules were never enforced, and three `BuildUpsert` preconditions

- gist: `server.New` swaps `echo.LegacyIPExtractor()` — which returned the
  left-most, unvalidated, caller-authored `X-Forwarded-For` entry — for
  `echo.ExtractIPFromXFFHeader()`, which walks the chain right-to-left and
  returns the first hop it does not trust. Echo's loopback / link-local /
  RFC1918 trust defaults are kept, so a service behind an in-VPC load
  balancer is correct with **no configuration**. A new `server.trustedproxies`
  CIDR list adds trust for a proxy sitting on a public address. Nothing
  fails to compile and most deployments simply get better-keyed limits, but
  every value `RealIP()` produces can change: both rate-limit bucket keys
  and the client address written to three log lines. Separately, consumers
  gain per-consumer AMQP arguments. `ConsumeFromQueue` passed a hardcoded
  `nil` args table, so `x-stream-offset` — which rides `basic.consume`, not
  the queue declare — had nowhere to go, and a RabbitMQ stream queue that
  [ADR-040](adr_040_declaration_args_passthrough.md) already let you
  *declare* could never be correctly *consumed*: every consumer silently
  attached at the broker default `next`, so a stream declared for replay
  delivered only what was published after the consumer connected. An
  `Args map[string]any` on `ConsumerOptions`, `ConsumerDeclaration` and
  `ConsumeOptions` closes that, and `DeclareStreamQueue` declares the queue
  with its retention args. The cost is comparability: a struct holding a map
  is not comparable in Go, so `==` and map-key use on those three types stop
  compiling (C59.2). Separately, `cache.Cache` gains a `CompareAndDelete`
  method — the safe, token-verified release the interface never had, since
  its only release was unconditional `Delete` — so every implementer must
  add it (C59.3). Separately again, `migration`'s `summarizeStmt` now redacts
  the `PASSWORD '…'` clause **before** splitting a failing statement at its
  first newline — the old order left the first-line fragment ending
  mid-literal, so the closing-quote-anchored pattern matched nothing and the
  first line of a newline-bearing password reached the logged error verbatim
  — and `PGRoleSpec.Validate` now rejects CR, LF, or NUL in either password
  field, so a spec that used to provision successfully returns an error
  (C59.5). Separately again, `BuildUpsert` now rejects a column present in
  both `conflictColumns` and `updateColumns` on **both** vendors. Oracle's
  MERGE cannot update a column referenced in its ON clause and fails at
  execution with ORA-38104, while PostgreSQL accepted the identical call, so
  the same code diverged by deployment — typically discovered in the Oracle
  environment, far from where it was written. Both builders now refuse it at
  build time, so one call means one thing everywhere (C59.7). Finally, ADR-050's `database.type` inference from the
  connection-string scheme, which ran only inside `config.Validate`, now also
  runs in `config.ApplyDatabasePoolDefaults` — the seam
  `database.DbManager.createConnection` applies to every config a
  `DBConfigProvider` returns — so a dynamic multi-tenant source returning a
  DSN-only config dials a real database instead of failing that tenant's
  every request with `unsupported database type: ""`, and a caller-supplied
  `Options.DatabaseConnector` now receives an inferred type where it used to
  receive `""` — inference is unconditional, since that option's exemption only
  ever covered the startup guard. Turning a fail-closed config into a working
  one means every consumer of that shape must agree, and one did not: the seam
  ran no vendor-specific validation, so a dynamic Oracle tenant carrying
  `database.tls.cert` / `database.tls.key` / `database.tls.ca` dialed with the material silently dropped.
  The seam now runs the same vendor validation `config.Validate` runs, so that
  tenant — and a PostgreSQL one carrying a lone `database.tls.cert` — fails at connection
  acquisition instead, typed configs included (C59.6). Separately again,
  `BuildUpsert` now also requires every conflict column to name a column of
  `insertColumns`, by the vendor's own identifier rules. Oracle enforced that already — its MERGE reads each conflict
  column from the USING SELECT that `insertColumns` builds — while PostgreSQL
  accepted the call and let the conflict column fall to its table default, so the
  same code diverged by deployment a second time. Both vendors now refuse it at
  build time, and each upsert precondition reports one message instead of one per
  vendor (C59.8). Separately again, a `conflictColumns` list naming one
  column twice is now refused on both vendors. PostgreSQL already failed at
  execution on it; Oracle accepted it, emitting a redundant ON-clause
  tautology that ran correctly, so the same list diverged by deployment a
  third time. Duplicates are judged by the vendor's identifier rules, so on
  Oracle a case variant of an identifier it emits **unquoted** — the
  non-reserved ones — is one column named twice, while the reserved words it
  quotes stay case-sensitive and are not; on PostgreSQL, which quotes
  everything, no case variant is a duplicate at all (C59.10). Separately
  again, `database.tls` reached pgx unvalidated apart from the cert/key
  pairing check, so a block configuring mTLS under `mode: disable` — or a
  `ca:` with no mode at all — booted green while connecting with no client
  certificate and no server verification, and a `tls:` block alongside a
  `connectionstring` was ignored outright. Startup validation now enforces
  an sslmode allowlist, requires `require`/`verify-ca`/`verify-full`
  wherever cert/key/ca are set, rejects the block alongside a connection
  string, and rejects it wholesale on Oracle (C59.11).
- build-caught: C59.2 C59.3 (via `go vet` — `go build` does not compile test files)
- preflight: **ten** actions. (i) If a proxy in front of the service sits on
  a **public** address, set `server.trustedproxies` to its CIDR range before
  the bump — without an entry it is untrusted, so it is returned as the
  client and every caller behind it collapses into one bucket. (ii) Check
  the load balancer for any mode that writes a **non-IP** `X-Forwarded-For`
  entry. On AWS ALB that is `routing.http.xff_client_port.enabled`, which
  appends `client_ip:port`: `net.ParseIP` rejects it, and echo then abandons
  the whole chain and returns the direct peer, so **every request** keys on
  the load balancer's own address — one bucket for the entire fleet, and
  `client_ip` reading as the LB in every access log. The same shape reaches
  any proxy writing RFC 7239 `for=` syntax, an obfuscated identifier, or a
  hostname. Separately, `routing.http.xff_header_processing.mode = remove`
  leaves no XFF to walk at all, with the same fleet-wide result — the shim's
  `X-Real-IP` fallback is deliberately gone. **`server.trustedproxies` does
  not rescue either case**: the chain is abandoned before trust is
  consulted. The only remedy is deployment-side (turn the attribute off, or
  normalize the header). (iii) If you provision PostgreSQL roles through
  `ProvisionPGRoles` or `PGRoleProvisioningSQL`, trace where each
  `MigratorPassword` / `RuntimePassword` comes from and `strings.TrimSpace`
  any value read from a file, a mounted secret, an environment read, a
  command substitution, or a secret-manager payload — a trailing newline is
  what those producers routinely append, and after the bump `Validate`
  rejects it instead of provisioning. Nothing is compiler-caught here, so a
  staging provisioning run against the real secret source is the decisive
  check. Rotate any credential whose provisioning failure was logged while
  its password contained a newline. (iv) Hand-read every `BuildUpsert` call
  for a column key present in **both** `conflictColumns` and
  `updateColumns`. `git grep -n 'BuildUpsert' -- '*.go'` finds the call
  sites but not the overlap — both maps are usually built dynamically — so
  grep shortlists and reading decides. On Oracle, match keys the way the
  database does: unquoted identifiers fold to upper case there, so
  `conflictColumns: ["id"]` with `updateColumns: {"ID": …}` is one column and
  is now rejected, while a **reserved word** — which Oracle quotes — stays
  case-sensitive. PostgreSQL quotes every identifier, so `"id"` and `"ID"`
  remain two distinct columns and that pairing is still accepted there.
  Only PostgreSQL turns success into failure; Oracle still fails, but at
  build time with the builder's message instead of at execution with
  ORA-38104 — earlier, and a different error to match on. Compare the column's update value against
  its insert value before choosing a remedy. If they are equal, drop it from
  `updateColumns` and no column value changes: the conflict match pins it
  on a matched row and the INSERT supplies it on an unmatched one. But if
  it is the **only** entry in `updateColumns`, dropping it empties the set,
  which builds `DO NOTHING` on PostgreSQL and drops Oracle's `WHEN MATCHED`
  arm — a matched row then gets no UPDATE at all, so row-level UPDATE
  triggers stop firing and `RETURNING` yields no row. Where that update is
  load-bearing, keep a real non-conflict column in the set or pair the
  DO NOTHING insert with an explicit `UPDATE` under the same transaction
  and locking rule as below. If they
  differ, that call was **rewriting the conflict column** on a matched row —
  legal PostgreSQL, impossible on Oracle, and not expressible through
  `BuildUpsert` on either vendor after the bump. Issue a separate `UPDATE`
  for those — in the **same transaction** as the insert or DO NOTHING path,
  keyed on the conflict columns, and holding the row lock the single
  statement took for you (`SELECT … FOR UPDATE` or equivalent), since one
  atomic statement becomes two and under READ COMMITTED a shared
  transaction alone does not stop a concurrent writer interleaving between
  them — and do not silently drop the column: that would keep the old key
  value and lose a write. (v) For every dynamic
  `DBConfigProvider`, list the tenants whose config carries a `postgres://` /
  `postgresql://` / `oracle://` connection string and no `type` — after the
  bump each of those opens a real connection at first use, so confirm the
  database exists, the credentials are current, and the connection budget
  absorbs the tenants that were previously failing closed. If you supply
  `Options.DatabaseConnector`, read it for any branch on an empty `cfg.Type`
  first — inference is unconditional, so that branch now takes the wrong arm.
  Then enumerate the same source again for every tenant carrying
  `database.tls.cert` / `database.tls.key` / `database.tls.ca` next to an Oracle type or `oracle://` DSN,
  and every PostgreSQL tenant carrying exactly one of `database.tls.cert` / `database.tls.key`.
  Those connect **today**, with the TLS material silently dropped, and stop
  connecting after the bump. Nothing here is compiler-caught and a repo grep
  finds nothing when the records live in Vault or a control-plane table, so
  enumerating the store is the decisive check — do it before the bump, since
  the remedy (drop material that was never in force, or move the tenant to a
  transport the driver implements) is a data change, not a code change. (vi) Hand-read the `BuildUpsert` shortlist from (iv) again, now for a
  conflict column that names **no** column of
  `insertColumns` under the vendor's identifier rules. Oracle already rejected those; PostgreSQL did not, and
  instead let the conflict column take its table default. That was harmless
  where the column was NOT NULL with no default (the insert already failed with
  `23502`) or nullable with no default (the inserted NULL is distinct under a
  plain unique index, so the conflict never fired), but it is a **working
  pattern** where the default is a sequence, a `current_setting(...)`, or a
  generated column — and after the bump both vendors refuse it. For a sequence
  or a `current_setting(...)` default, compute the value caller-side and pass it
  in `insertColumns`; prefer that to `database.Raw`, which replaces the whole
  statement and takes the builder's identifier validation and vendor quoting
  with it. A **generated** column cannot be passed at all — PostgreSQL forbids
  writing one directly — so conflicting on one needs `database.Raw` or a schema
  change. Separately, drop any match on the upsert
  precondition message texts: `conflict columns required for Oracle MERGE` and
  `conflict columns required for PostgreSQL upsert` both become `conflict
  columns required for upsert`, and the membership error no longer names Oracle
  MERGE.
  (vii) Check whether any `BuildUpsert` call can pass the same column
  twice in `conflictColumns` — an exact repeat on either vendor, or on Oracle
  a case variant of a **non-reserved** identifier, which Oracle emits unquoted
  and folds to one column. A reserved word is quoted and stays
  case-sensitive, so `["level", "LEVEL"]` is two columns and is still
  accepted; on PostgreSQL no case variant is a duplicate. Dynamically
  assembled lists are where this happens. Both vendors now refuse it, and the two started from different
  places: on PostgreSQL such a call already failed at execution with `42P10`,
  but **on Oracle it worked**, because the duplicate only produced a redundant
  `AND` in the ON clause. De-duplicate at the call site — by the vendor's own
  rules, since lower-casing before comparison is wrong on PostgreSQL, where
  case distinguishes columns. (viii) Hand-read the `BuildUpsert` shortlist once more for an
  **Oracle** call whose conflict column and insert key name the same column in
  different spellings — a case variant (`["id"]` against `{"ID": …}`) or a
  whitespace-padded key (`[" id "]` against `{"id": …}`). Those were refused at
  build time and now build, and on execution they **write**. This is the one
  item here with no after-the-bump signal: a test pinning the old rejection
  fails loudly, but a call site where that rejection was quietly doing
  validation work for you — catching a wrong column name that only surfaced
  because its spelling did not match — simply proceeds and upserts. If you were
  relying on it, move that check into your own code before the bump.
  (ix) Grep every environment — including the ones
  not deployed from this repo — for a `database.tls` block, and decide each
  one before the bump. Four shapes now abort startup for static
  configuration — a dynamic `DBConfigProvider` record with the same shape
  boots green and fails at first connection acquisition instead, so check
  dynamic tenants by acquiring a connection, not by booting: a PostgreSQL `mode`
  outside `disable`/`allow`/`prefer`/`require`/`verify-ca`/`verify-full`;
  `cert`/`key`/`ca` with the mode empty, `disable`, `allow` or `prefer`; any
  `database.tls.*` alongside a `connectionstring`; and `database.tls.mode`
  on Oracle. The unset-mode case is the one to look for hardest — it reads
  as "no TLS configured" but pgx defaults to `prefer`, so a path-valued
  `ca:` sitting alone was never verifying anything (`ca: system` was the
  lone exception — pgx force-upgraded it to `verify-full`). Remediating the
  second shape is not
  purely editorial: `verify-ca`/`verify-full` — or `require` with a `ca`,
  which pgx upgrades to verify-ca semantics — makes that connection verify
  the server for the first time, so confirm the CA path and the server's
  certificate chain first, or a connection that succeeded unverified will
  now fail; bare `require` (no `ca`) encrypts without authenticating the
  server, so it satisfies the validator yet buys no verification. Nothing
  here is compiler-caught; booting each environment decides for static
  configs — a first connection per dynamic tenant decides for the rest.
  (x) Grep your service for `app.NewWithConfig`, a direct `app.Builder`
  chain, or an `Options.ConfigLoader` handed to `app.NewWithOptions` —
  `git grep -n 'NewWithConfig\|NewAppBuilder\|NewWithOptions'` — and check
  every hit handing over a hand-built `*config.Config`, not `config.Load`
  output; most services construct via `app.New()` and are unaffected. Run that config
  through `config.Validate` before the bump: missing `app.name` or
  `app.version`, zero `server.timeout.*` values, and an invalid
  `database.type` now fail construction instead of booting on whatever the
  bypass previously let through. Test fixtures are the common hit — give
  them a name, a version, and positive server timeouts.
- exit: `go get github.com/gaborage/go-bricks@v0.59.0 && go mod tidy && go build ./... && go test ./...`

### [C59.1] Rate-limit buckets and logged client addresses key on the trusted-proxy-derived IP · silent-behavior · when: match

- detect: `git grep -nE 'client_ip|ClientAddr' -- '*.go'` inside your own
  repo finds code that reads the framework's logged client address, and
  `git grep -n 'trustedproxies' -- '*.yaml' '*.yml'` shows whether you
  already configure one. **Neither reaches what actually matters.** The
  three framework sites whose emitted values move are
  `server/logger.go` (`ClientAddr` on every access-log record — the
  `client_ip` field), `server/ip_preguard.go` (the pre-guard's `429`
  rejection line) and `server/tenant_middleware.go` (the `400`
  tenant-resolution-failure line). A dashboard, alert, saved query, or
  synthetic check keyed on any of those three lives **outside this repo**
  and no repo-local grep will find it — audit the log backend by hand.
- scope: one line in `server/server.go` (the `IPExtractor` assignment) plus
  the new `server.trustedproxies` key. The two rate limiters
  (`server/ratelimit.go`, `server/ip_preguard.go`) are unchanged — they
  still key on `ctx.RealIP()`; only what `RealIP()` resolves to moves.
  Middleware order is unchanged. Access-control paths were never affected:
  the debug allowlist and the scheduler's CIDR middleware already used
  `server.ClientIP(r, trustedNets)`.
- gate: match = you are behind any proxy or load balancer, **or** anything
  outside the repo reads `client_ip` from the access log. no-match = the
  service is reached directly with no proxy and nothing consumes the logged
  client address.
- after: three consequences. (i) Both limiters now bucket on an address that
  only a caller **already inside** loopback, link-local, RFC1918, or IPv6
  unique-local (`fc00::/7`) space can still choose — the trust boundary moved
  from "anyone who can reach the service" to those ranges, it did not
  disappear — so a client on the public internet that evaded the IP pre-guard
  by rotating `X-Forwarded-For` is throttled, and traffic that previously
  spread across forged keys now concentrates on real ones — expect `429`
  rates to move in both directions. (ii) `client_ip` values change wherever a proxy is in
  play; a chart grouped by it will show a different population. (iii) A
  malformed `server.trustedproxies` entry now **aborts startup** on every
  `app` construction path — `app.NewWithConfig` runs `config.Validate` too
  since ADR-064 — except when `server.New` is called directly, outside the
  `app` package's `config.Validate`, where it is skipped with an ERROR log
  instead — rather than being dropped with a warning:
  `net.ParseCIDR` must accept it (a bare
  `10.0.0.5` is rejected — write `10.0.0.5/32`), host bits must be clear
  (`10.1.2.3/8` is rejected because it silently widens to `10.0.0.0/8`), and
  a default route (`0.0.0.0/0`, `::/0`) is rejected because trusting
  everyone restores the spoofable behavior this change removes.
- verify: `go test ./server/... ./config/...`; in a consuming application,
  send a request carrying `X-Forwarded-For: 1.2.3.4` from a peer that is not
  in a trusted range and assert the access log's `client_ip` reads the real
  peer, not `1.2.3.4`.
- ref: `server/server.go` (`trustedProxyOptions`, the `IPExtractor`
  assignment) · `config/validation.go` (`validateServerTrustedProxies`) ·
  [ADR-057](adr_057_trusted_proxy_ip_extraction.md) ·
  [ADR-015](adr_015_echo_v5_migration.md) (recorded this follow-up)

### [C59.2] `ConsumeOptions`, `ConsumerOptions` and `ConsumerDeclaration` stop being comparable · compile-break · when: match

- detect: `git grep -nE 'messaging[.](Consume|Consumer)(Options|Declaration)' -- '*.go'` shortlists every
  file naming one of the three types; the hits that matter are the ones where such a value is an operand of
  `==` or `!=`, or the key type of a `map[...]`. Keep the pattern free of `\b`/`\s`/`\w` — `git grep -E`
  has no PCRE escapes, so a pattern carrying one silently matches nothing and the gate reports "not
  affected". `go vet ./...` is the authoritative answer regardless, since the compiler resolves this one
  even through an alias or dot-import that a line-oriented grep misses — and unlike `go build ./...` it
  type-checks `_test.go` files, where a `==` on one of these structs is at least as likely as in
  production code and would otherwise pass a green build untouched
- scope: one added field, `Args map[string]any`, on each of `messaging.ConsumeOptions`
  (`messaging/messaging.go`), `messaging.ConsumerOptions` (`messaging/helpers.go`) and
  `messaging.ConsumerDeclaration` (`messaging/registry.go`). A struct containing a map is not comparable in
  Go, so the three types lose `==`, `!=` and map-key use. **Unchanged**: assignment, copying, passing by
  value, struct literals and field access. A consumer that sets no `Args` also produces byte-identical wire
  traffic — `toTable` normalizes a nil or empty map to a nil `amqp.Table`. `reflect.DeepEqual` still
  *compiles*, but it is **not** unchanged: it now walks the `Args` contents, and it distinguishes a nil map
  from an empty one. That last point bites in one specific place — `RegisterConsumer` and `Clone` allocate
  an empty `Args` map even when the caller supplied nil, so a declaration read back out of `Declarations`
  will not `DeepEqual` a hand-built literal that left `Args` nil, even though the two are equivalent
  everywhere else. Compare the fields you care about, or set `Args: map[string]any{}` on the literal
- gate: match = the detect's hits include a `==`/`!=` between two such values, or a map keyed on one.
  no-match = you only construct, pass and read these types, which is the overwhelmingly common case — the
  types are declaration inputs, not value objects, so most consumers never compared them
- before:

  ```go
  if got == messaging.ConsumeOptions{Queue: "orders", Consumer: "worker"} { /* ... */ }

  seen := map[messaging.ConsumerDeclaration]bool{}
  seen[decl] = true
  ```

- after:

  ```go
  if got.Queue == "orders" && got.Consumer == "worker" { /* ... */ }

  // Key on the identity the framework itself uses: queue + consumer tag
  // (+ event type for a ConsumerDeclaration), not the whole struct.
  type consumerKey struct{ queue, consumer string }

  seen := map[consumerKey]bool{}
  seen[consumerKey{decl.Queue, decl.Consumer}] = true
  ```

  Compare the fields you actually care about rather than the whole value. If you genuinely need
  whole-struct equality — most often in a test assertion — `reflect.DeepEqual` (or
  `require.Equal`/`assert.Equal`, which use it) still compiles, and now compares the `Args` contents too,
  which `==` could never have done. Read the scope bullet's nil-versus-empty-map caveat before relying on
  it against a declaration that came back out of `Declarations`
- why: `x-stream-offset` is a **per-consumer** argument on `basic.consume`, not a queue argument, because
  two consumers on one stream legitimately start at different offsets. Reaching the wire with it means
  carrying a variable-length argument set through all three hops, and a type carrying one is not a
  comparable value. A pointer-to-map would restore `==` as pointer identity — two consumers with identical
  arguments comparing unequal, and code that kept compiling silently changing meaning — which is worse than
  a break the compiler finds for you
- verify: `go build ./... && go test ./...`
- ref: [ADR-058](adr_058_consumer_scoped_amqp_arguments.md) · [ADR-040](adr_040_declaration_args_passthrough.md)
  (the queue-side precedent this completes) · `messaging/messaging.go` · `messaging/helpers.go` ·
  `messaging/registry.go` · [messaging.md](messaging.md#stream-queues-amqp-lane)

### [C59.3] `cache.Cache` gains `CompareAndDelete`, so every implementer must add it · compile-break · when: match

- detect: `git grep -nE 'cache[.]Cache([^A-Za-z0-9_]|$)' -- '*.go'` shortlists every file naming the
  interface; the hits that matter are the ones **implementing** it rather than consuming it. Keep the
  pattern free of `\b`/`\s`/`\w` — `git grep -E` has no PCRE escapes, so a pattern carrying one
  silently matches nothing and the gate reports "not affected". `go vet ./...` is the authoritative
  answer: **do not use `go build ./...` for this one.** `go build` does not type-check `_test.go`
  files, and a hand-rolled `cache.Cache` double is far likelier to live in a test file than in
  production code — two of the four in-repo implementers do. A consumer following a `go build`
  instruction sees it pass and then hits the break in CI
- scope: one added method on the exported `cache.Cache` interface —
  `CompareAndDelete(ctx context.Context, key string, expectedValue []byte) (deleted bool, err error)`.
  Every implementer must supply it. The break usually surfaces in **test doubles**: a fake cache in a
  `_test.go` file, or a hand-written mock in a testing package. Consumers who only *call* the
  interface are unaffected and need no change. Nothing else on the interface moved, and `Delete`
  keeps its name and its unconditional semantics. **Also carries an upgrade hazard that is not
  compiler-caught**: swapping `Delete` for `CompareAndDelete` to release a lock acquired with
  `ttl == 0` converts a recoverable mistake into a permanent one. `CompareAndSet` accepts ttl 0 and
  stores the key **without expiration**; an unconditional `Delete` always freed it, but a
  token-verified release returns `false` on any token drift and then nothing ever removes the key.
  Acquire with a bounded, positive TTL before switching the release, and make the stored value
  unique per **acquisition** — a mechanical swap that keeps a reusable worker identity as the token
  keeps the hazard, because a release landing after the TTL lapsed matches the next acquisition's
  identical value and deletes its lock. Two further contract rules:
  `false` and any error are both **terminal** — never fall back to `Delete` and never retry the
  release with it, or the unconditional-release hazard returns behind an API that reads as safe — and
  `false` does not prove another holder's value is present, because it also covers "the key was
  already gone"
- gate: match = your code (including test files) declares a type implementing `cache.Cache`.
  no-match = you only obtain a cache via `deps.Cache(ctx)` and call methods on it
- before:

  ```go
  type fakeCache struct{ data map[string][]byte }

  func (f *fakeCache) CompareAndSet(_ context.Context, key string, expected, new []byte, _ time.Duration) (bool, error) {
      // ...
  }
  ```

- after:

  ```go
  func (f *fakeCache) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
      if expected == nil {
          return false, cache.ErrNilExpectedValue
      }
      if stored, ok := f.data[key]; !ok || !bytes.Equal(stored, expected) {
          return false, nil
      }
      delete(f.data, key)
      return true, nil
  }
  ```

  Reject a nil `expectedValue` rather than treating it as a mode: `CompareAndSet` reads nil as
  acquire-if-absent, but a delete has no such counterpart and unconditional removal is already `Delete`.
  A real cache must reject it *before* the round trip — go-redis renders a nil `[]byte` as a
  zero-length bulk string, which would silently match a key holding the empty string. An empty slice
  `[]byte{}` is a genuine comparison against the empty string. If your double has no test that
  exercises the method, the minimal body is `return false, cache.ErrNilExpectedValue` guarded as
  above plus a compare-and-delete over your backing map
- why: the interface has advertised `CompareAndSet` as the distributed-locking primitive since
  [ADR-011](adr_011_redis_cache.md) while offering no safe release. The only release was
  unconditional `Delete`, so a worker whose work outran the TTL cleared the **next** holder's lock and
  two workers ran concurrently under a lock that reported success to both. It could not be emulated:
  `casScript` has no `DEL` branch, and `CompareAndSet(…, workerID, nil, 0)` writes an empty string
  that still occupies the key, with no expiry. An optional interface discovered by type assertion was
  rejected because the fallback *is* the hazard — a cache not implementing it would send callers back
  to unconditional `Delete`, reached silently instead of visibly. A compile break is the correct,
  loud failure
- verify: `go vet ./... && go test ./...` — **not** `go build ./...`, which does not compile the test
  files where hand-rolled doubles usually live
- ref: [ADR-060](adr_060_cache_compare_and_delete.md) · [ADR-011](adr_011_redis_cache.md) (the
  superseded lock example) · `cache/types.go` · `cache/errors.go` (`ErrNilExpectedValue`) ·
  `cache/redis/client.go` · [cache.md](cache.md#key-operations)

### [C59.4] `MockCache` reports a canceled context even when no delay is configured · silent-behavior · when: match

- detect: `git grep -ln 'NewMockCache' -- '*_test.go'` lists every file using the mock; intersect it
  with `git grep -lnE 'context[.]With(Cancel|Timeout|Deadline)' -- '*_test.go'` and read the overlap.
  The tests at risk hand an **already canceled or already expired** context to one of the mock's seven
  context-taking methods — `Get`, `Set`, `GetOrSet`, `CompareAndSet`, `CompareAndDelete`, `Delete`,
  `Health` — and rely on the operation completing anyway. Include tests that reuse a context after
  their own `cancel()`, and ones whose `WithTimeout` budget is short enough to have elapsed by the time
  the mock is reached. Keep every pattern free of `\b`/`\s`/`\w` — `git grep -E` has no PCRE escapes, so
  a pattern carrying one matches nothing and the gate falsely reports "not affected". `go test ./...`
  is the authoritative answer: nothing here is compiler-caught.
- scope: `cache/testing/mock_cache.go` only. Those seven methods each carried their own
  `if m.delay > 0 { select { … } }` block, so the context was consulted **only while a configured delay
  was being waited out**: a canceled context on a mock with **no** delay — the default from
  `NewMockCache()` — was ignored and the operation ran to completion. All seven now route through one
  `waitDelay` helper that checks `ctx.Err()` before arming its timer, so a dead context short-circuits
  with `context.Canceled` / `context.DeadlineExceeded` whatever the delay is. `waitDelay` runs first in
  every method — **before** the `m.closed` check and **before** the configured-`With*Failure` check — so
  under a dead context the context's error now outranks both: a closed mock returns `context.Canceled`
  where it returned `cache.ErrClosed`, and a `WithGetFailure(errBoom)` mock returns `context.Canceled`
  where it returned `errBoom`. A **positive** delay already behaved exactly this way (its guard sat in
  the same first position), and that path is unchanged — only the no-delay case moves. The
  remedy is normally to stop threading a dead context into the mock, or to assert the cancellation
  error the call now returns. Nothing on the real `cache/redis` client moved, no assertion helper
  changed, and `Stats()` / `Close()` take no context and are untouched.
- gate: match = a test hands a `MockCache` method a context that is already canceled or expired and
  asserts any specific **non-context** outcome for that call — success, `cache.ErrNotFound`,
  `cache.ErrClosed`, or a `With*Failure` error — since the context's error now replaces all of them.
  no-match = your tests pass live contexts, already assert the context's own error, or already pair
  cancellation with a configured `WithDelay` (that combination behaved this way before and still does).
- after: a `MockCache` call under a dead context returns the context's error instead of its normal
  result, so a test asserting `assert.NoError` (or `cache.ErrNotFound`) on that call now sees
  `context.Canceled`. Fix the context, not the assertion, unless the test's real subject *is* the
  cancellation path — in which case assert `context.Canceled` and delete the `WithDelay` that used to
  be needed to make cancellation observable.
- verify: `go test ./...`
- ref: [ADR-060](adr_060_cache_compare_and_delete.md) (Consequences — why the guard was restructured) ·
  `cache/testing/mock_cache.go` (`waitDelay`) · [testing.md](testing.md#cache-testing)

### [C59.5] A `PGRoleSpec` password containing CR, LF, or NUL now fails `Validate` · breaking · when: match

- detect: `git grep -nE '(Migrator|Runtime)Password[[:space:]]*[:=]' -- '*.go'` lists every site that
  populates a `PGRoleSpec` password. For each, trace where the value comes from: a literal or an
  already-trimmed string is safe, while `os.ReadFile`, a mounted-secret read, a command substitution,
  or a secret-manager payload can all carry a trailing newline. Keep every pattern free of the PCRE
  escapes — `git grep -E` is POSIX ERE and silently ignores them, so a pattern carrying one matches
  nothing and the gate falsely reports "not affected". Nothing here is compiler-caught: the failure is
  a returned error at run time, so a staging provisioning run is the authoritative answer.
- scope: `migration/roles.go` only — `PGRoleSpec.Validate`, reached by both `ProvisionPGRoles` and
  `PGRoleProvisioningSQL` (`buildPGRoleStatements` is unexported and reachable only through those
  two, so no path bypasses the check). `Validate` now rejects `MigratorPassword` or `RuntimePassword`
  containing CR, LF, or NUL, wrapping the new exported sentinel `ErrPGRolePasswordHasControlChar`
  with the offending **field name** — never the value. The check runs after the existing identifier
  and role-differ checks, so all previous error precedence is preserved, and an **empty** password
  stays valid (it deliberately emits no `ALTER ROLE … PASSWORD` statement at all). The character set
  is CR/LF/NUL, not "all control characters", matching `flyway.go`'s `ErrEnvFieldHasControlChar` so
  the two boundaries agree. PostgreSQL itself accepts such passwords — the restriction is this API's,
  because the provisioning path cannot carry them log-safely. Shipped alongside the fix that motivated
  it: `summarizeStmt` now redacts the `PASSWORD '…'` clause **before** splitting the statement at its
  first newline, which is not itself breaking.
- gate: match = you build a `PGRoleSpec` whose `MigratorPassword` or `RuntimePassword` is sourced from
  a file, a mounted secret, an environment read, a command substitution, or a secret-manager payload —
  any producer that can append a newline. no-match = both password fields are always empty (credentials
  managed out-of-band), or every value is a literal or already passed through `strings.TrimSpace`.
- before: `ProvisionPGRoles` / `PGRoleProvisioningSQL` accepted the password and emitted a multi-line
  `ALTER ROLE … PASSWORD 'line1\nline2'`. Worse, if that statement then failed, `summarizeStmt` split
  the statement before redacting it, so the closing-quote-anchored pattern could not match the
  first-line fragment and the first line of the secret was interpolated verbatim into the returned
  error — which callers log.
- after: `Validate` returns an error wrapping `ErrPGRolePasswordHasControlChar`, naming the field, and
  provisioning does not run. Match it with `errors.Is`, never `==`. The remedy is to trim at the
  source — `strings.TrimSpace(string(b))` on the file or secret read — not to strip inside the
  framework, which would silently provision a credential different from the one you passed. **Rotate
  any credential** whose provisioning failure was logged while its password contained a newline: this
  change stops future leaks, not past ones.
- verify: `go build ./... && go test ./...`, then confirm no provisioning password is read from a file
  or a command substitution without `strings.TrimSpace`, and run one provisioning pass in staging with
  the real secret source — a trailing newline that survived to production would now abort the run.
- ref: [ADR-061](adr_061_role_password_control_chars.md) · `migration/roles.go`

### [C59.6] `ApplyDatabasePoolDefaults` infers `database.type` and enforces vendor rules on every dynamic config · breaking · when: match

- detect: `git grep -nE 'DBConfig\(ctx context.Context|ConnectionString:' -- '*.go'` over your own
  `DBConfigProvider` / `app.TenantStore` implementations, plus whatever your provider reads (Vault
  path, Secrets Manager key, control-plane table) for a stored connection string with no sibling
  type. Then `git grep -nE 'CertFile|KeyFile|CAFile|sslcert|sslkey|tls' -- '*.go'` over the same
  implementations, and read the records themselves for TLS material stored next to an Oracle tenant,
  or exactly one of cert/key next to a PostgreSQL one. Also
  `git grep -n 'DatabaseConnector' -- '*.go'`. Keep every pattern free of the PCRE escapes — `git
  grep -E` is POSIX ERE and silently ignores them, so a pattern carrying one matches nothing and the
  gate falsely reports "not affected". Match = a dynamic provider can return a `DatabaseConfig` at
  all, or you supply `Options.DatabaseConnector`.
- scope: two changes to `config.ApplyDatabasePoolDefaults` — the seam
  `database.DbManager.createConnection` applies to every config a dynamic provider returns, and
  which never reaches `config.Validate`. **(1) Inference.** It infers `Type` from a recognized DSN
  scheme (`postgres://`, `postgresql://` → `postgresql`; `oracle://` → `oracle`) when `Type` is
  empty, matching what `config.validateDatabaseWithConnectionString` has done for static config
  since `[C57.5]` ([ADR-050](adr_050_connectionstring_type_inference.md)). Surrounding whitespace no
  longer defeats the scheme match at either site — a DSN read from a mounted secret routinely
  carries a trailing newline — and the stored DSN is not rewritten. That tolerance also reaches the
  **static** path, with one consequence worth knowing. `ConfigureRuntimeHelpers`' untyped-DSN startup
  guard walks only static config — the root `database:` block, `databases.<name>`, and (under
  `multitenant.enabled: true`) `multitenant.tenants.<id>` — so this affects those entries only. Such
  an entry whose scheme went unmatched *because* of surrounding whitespace now resolves a `Type`, and
  the guard no longer fires for it. Under static single-tenant that is strictly better:
  pre-initialization still dials at boot, so startup aborts there with the driver's own message
  instead of a misleading "no resolved database type". Where pre-initialization is skipped —
  `multitenant.enabled: true`, `source.type: dynamic`, or a dynamic resource store — that **static**
  entry instead boots green and fails at first use, where it used to abort at startup. A dynamic
  provider's own records are unaffected in either direction: the guard never walked them, so they had
  no startup signal to lose and failed at first use before and after (see `before:` below).
  Inference is unconditional:
  `Options.DatabaseConnector`'s exemption covers the **startup guard** only and never covered
  inference, so a custom connector now receives `Type` populated where it used to receive `""`.
  **(2) Vendor validation.** The seam now runs the same vendor-specific validation
  `config.Validate` runs, so Oracle's TLS rejection and PostgreSQL's
  `database.tls.cert`/`database.tls.key` pairing rule — those config keys reach pgx as the DSN
  parameters `sslcert`/`sslkey`, which is the wording its error uses —
  reach dynamic configs for the first time — including configs whose `Type` was set **explicitly**,
  not only DSN-only ones. Unchanged: an unrecognized scheme still leaves `Type` empty and still
  fails at the built-in connector; an explicit `Type` contradicting the DSN is still **not** an
  error on this seam — only `config.Validate` rejects that — so a dynamic source with a wrong
  explicit type keeps failing at dial with the vendor error.
- gate: match = a dynamic provider (`source.type: dynamic`, a custom `DBConfigProvider`, a
  multi-tenant resource source) can emit a `DatabaseConfig` that is untyped-with-a-DSN, **or** that
  carries `database.tls.cert` / `database.tls.key` / `database.tls.ca` at all, or `Options.DatabaseConnector` is set. no-match =
  every database config your app produces is static YAML (those already went through `[C57.5]` and
  have always had vendor validation) and you use the built-in connector.
- before: a dynamic multi-tenant source returning `{ConnectionString: "postgres://…"}` with no
  `Type` reached `database.NewConnection`'s `switch cfg.Type` and failed that tenant's every request
  with `unsupported database type: "" (supported: postgresql, oracle)`, forever and with no startup
  signal — dynamic tenants are resolved lazily, so nothing enumerates them at boot. Separately and
  more quietly, a dynamic tenant resolving to `oracle` — inferred or spelled out — **connected**
  with its `database.tls.cert` / `database.tls.key` / `database.tls.ca` silently dropped, since go-ora implements neither tcps
  nor wallet; and a PostgreSQL tenant carrying only `database.tls.cert` connected with the certificate
  ignored under `sslmode=disable`. Both are exactly the states the vendor validators exist to
  prevent, and neither validator was reachable on this path.
- after: the untyped-DSN tenant comes up, which is the fix, but it also means a real dial against a
  real database that nothing in your deployment has exercised. The TLS shapes above stop connecting
  instead: `deps.DB(ctx)` returns a config error at connection acquisition (`TLS cert/key/ca are not
  supported for Oracle`, or `sslcert and sslkey must be configured together`) rather than a handle
  whose transport was never what the record claimed. Decide per affected tenant. For the untyped
  DSNs: whether connecting is what you want — if a tenant's DSN was untyped because that tenant is
  meant to be database-free, stop returning a connection string for it. For the TLS ones: drop the
  material that was never in force, or move that tenant to a transport the driver implements; do not
  read the new error as a regression, because the encryption it names was never applied. A rejected
  config goes back to the caller **completely untouched** — normalization happens on a clone that is
  committed only after every step succeeds — so a provider that reuses the struct sees no partial
  mutation, neither a reclassified `Type` nor half-applied pool defaults. If you supply
  `Options.DatabaseConnector`, replace any `cfg.Type == ""` branch with one that parses the DSN
  unconditionally, or that accepts both an empty and an inferred type.
- verify: start the service and resolve a previously-failing tenant — the
  `Created new database connection` log line now carries a non-empty `db_type`
  (`database/manager.go`), and `deps.DB(ctx)` returns a usable handle instead of
  `unsupported database type: ""`. Then resolve a tenant whose record carries TLS material and
  confirm it now fails with the vendor message instead of connecting.
- ref: [ADR-050](adr_050_connectionstring_type_inference.md) · `[C57.5]` ·
  `config/validation.go` (`ApplyDatabasePoolDefaults`, `validateVendorSpecificFields`) ·
  `database/manager.go` (`createConnection`)

### [C59.7] `BuildUpsert` rejects a conflict column that also appears in the update set · breaking · when: match

- detect: `git grep -n 'BuildUpsert' -- '*.go'` lists every call site. Keep the pattern plain — `git
  grep -E` is POSIX ERE and silently ignores `\b`, `\s`, `\d` and `\w`, so a pattern carrying one
  matches nothing and the gate falsely reports "not affected". Nothing here is compiler-caught: the
  `BuildUpsert` signature is unchanged, so the failure is a returned error at run time.
- scope: `database/internal/builder/` only — a shared helper `rejectConflictColumnUpdates`
  (`helpers.go`) called from `BuildUpsert` (`oracle.go`), which holds every upsert precondition.
  It rejects any call passing the same column key in **both** `conflictColumns` and
  `updateColumns`, naming the offending column. Grep alone cannot decide whether a given hit is
  affected — the two maps are usually built dynamically — so hand-read each one. Calls whose column
  sets are disjoint are unaffected, and so are DO NOTHING calls (an empty or nil `updateColumns`,
  where every lookup misses). Column identity follows the **active vendor's** identifier rules, so the
  check sees columns the way the database will: PostgreSQL quotes every identifier and matches exactly
  (`"id"` and `"ID"` stay two columns, and that pairing is still accepted), while Oracle folds the
  unquoted, non-reserved identifiers it emits to upper case (`id` and `ID` are one column, now
  rejected) and keeps the reserved words it quotes case-sensitive. The conflict ⊆ insert precondition
  runs first, so a call violating both still gets that message; every upsert precondition keys on the
  same vendor identity, so a case-variant pairing reads the same way to all of them.
- gate: match = a `BuildUpsert` call whose `conflictColumns` and `updateColumns` share a key. no-match
  = every call's two column sets are disjoint, or `updateColumns` is always empty.
- before: on PostgreSQL the call built and executed — `ON CONFLICT ("id") DO UPDATE SET "id" = $3` is
  legal SQL, so the divergence stayed invisible until the same call ran against Oracle, where MERGE
  fails at execution with **ORA-38104: Columns referenced in the ON Clause cannot be updated** —
  typically in the Oracle deployment, far from where the code was written.
- after: both vendors return an error from the builder before any SQL is produced. Only PostgreSQL
  turns a working call into a failing one; on Oracle the call already failed, and what changes is when
  and how — build time with the builder's message, not execution time with ORA-38104. The remedy depends
  on the column's update value. If it equals the insert value, drop the column from `updateColumns` —
  the conflict match pins it on a matched row and the INSERT supplies it on an unmatched one, so no
  resulting row changes. **Check first whether it is the only entry in `updateColumns`**: dropping the
  last one leaves an empty set, and an empty update set builds `DO NOTHING` on PostgreSQL and omits
  Oracle's `WHEN MATCHED` arm, so a matched row is no longer updated at all — row-level UPDATE
  triggers stop firing, a trigger-maintained `updated_at` stops advancing, and `RETURNING` yields no
  row. That is a behavior change beyond column values. Where the update itself is load-bearing, either
  keep a genuine non-conflict column in `updateColumns`, or pair the DO NOTHING insert with an
  explicit `UPDATE` under the same transaction, predicate and locking rule stated below. If it
  differs, the call was **rewriting the conflict column** on a matched
  row (`ON CONFLICT ("id") DO UPDATE SET "id" = $3` moves the row's key): legal on PostgreSQL, always
  impossible on Oracle, and no longer expressible through `BuildUpsert` on either. Issue a separate
  `UPDATE` for that case — dropping the column instead would keep the old key value and silently lose
  a write. Run that `UPDATE` **in the same transaction** as the insert or DO NOTHING path, keyed on
  the conflict columns so it targets the row the conflict would have matched, and take the locking
  the upsert used to give you for free: one atomic statement became two, so without a surrounding
  transaction a concurrent writer can interleave and the row you update need not be the row you
  inserted against.
- verify: `go build ./... && go test ./...`
- ref: [ADR-028](adr_028_pg_upsert_binds_update_values.md) — context only: it decides that the
  PostgreSQL upsert binds update values rather than reusing `EXCLUDED`, and states no vendor-parity
  policy. The policy this atom applies — `BuildUpsert` expresses what **both** vendors can express,
  so Oracle's constraints are the floor — is stated here and in C59.8, not in an ADR. ·
  `database/internal/builder/helpers.go` (`rejectConflictColumnUpdates`) ·
  `database/internal/builder/oracle.go` (`BuildUpsert`)

### [C59.8] `BuildUpsert` requires every conflict column in the insert columns on both vendors · breaking · when: match

- detect: `git grep -n 'BuildUpsert' -- '*.go'` lists every call site. Keep the pattern plain — `git
  grep -E` is POSIX ERE and silently ignores `\b`, `\s`, `\d` and `\w`, so a pattern carrying one
  matches nothing and the gate falsely reports "not affected". Nothing here is compiler-caught: the
  `BuildUpsert` signature is unchanged, so the failure is a returned error at run time.
- scope: `database/internal/builder/` only — a shared helper `requireConflictColumnsInInsertSet`
  (`helpers.go`) called from `BuildUpsert` itself (`oracle.go`), which now holds all three upsert
  preconditions rather than repeating them in `buildOracleMerge` and `buildPostgreSQLUpsert`, so the
  two vendor builders cannot drift apart on a precondition again. It rejects any call whose
  `conflictColumns` holds an entry that names no column of `insertColumns`, naming the offending one.
  An unsupported vendor is still reported as unsupported, ahead of any precondition. Oracle enforced
  this already; **PostgreSQL is the new
  rejection**, so this atom changes only PostgreSQL call sites — those whose conflict column matches
  no inserted column *by identity*. Membership keys on vendor identity, the same rule the checks
  around it use; C59.9 is what brought them into line, and it separately widens what Oracle accepts,
  so read the two together. Grep alone
  cannot decide whether a given hit is affected — the two collections are usually built dynamically —
  so hand-read each one. The same change unifies the wording of both upsert preconditions: the empty
  `conflictColumns` error read `conflict columns required for Oracle MERGE` on one vendor and
  `conflict columns required for PostgreSQL upsert` on the other, and the membership error named
  Oracle MERGE. Each precondition now emits one message regardless of vendor.
- gate: match = a `BuildUpsert` call reaching PostgreSQL whose `conflictColumns` can hold a column
  absent from `insertColumns`, **or** any code matching on either precondition's message text.
  no-match = every call's conflict columns are also insert keys, and nothing matches the message text.
- before: PostgreSQL built and executed the call. `ON CONFLICT (c)` names a column of the *target
  table* and only picks the arbiter index — it has no relation to the insert column list — so the
  proposed row took `c`'s `DEFAULT` (or NULL). Three outcomes followed: `c` NOT NULL with no default
  failed at execution with `23502 not_null_violation`; `c` nullable with no default inserted NULL,
  which a plain unique index treats as distinct, so the conflict never fired and `DO UPDATE` was dead
  weight; and `c` carrying a real default (a sequence, `current_setting(...)`, a generated column)
  worked as intended. Oracle rejected all three at build time, because its MERGE reads each conflict
  column from the USING SELECT that `insertColumns` builds, so an absent one produced
  `source.<column>` against an inline view that never declared it.
- after: both vendors return `conflict column "c" must be present in insert columns for upsert` before
  any SQL is produced, and an empty `conflictColumns` returns `conflict columns required for upsert`
  on both. Oracle rejected all three shapes before and still does, so **this atom** changes no Oracle
  call — but C59.9 does, by accepting spelling variants Oracle folds to one column, so Oracle behavior
  across the hop is not unchanged. On PostgreSQL the remedy is to put the
  conflict column in `insertColumns` with the value the conflict is matching on — for the first two
  outcomes above that value was never meaningful, and stating it is what the call always meant. The
  third outcome is the one that costs something: a conflict column populated by a column default is a
  working PostgreSQL pattern that `BuildUpsert` no longer expresses, because Oracle cannot express it.
  Where the default is a sequence or a `current_setting(...)`, compute the value caller-side and pass
  it in `insertColumns` — that keeps the statement inside the builder, so prefer it. A **generated**
  column has no such remedy: PostgreSQL forbids writing one directly (an INSERT may name it only as
  `DEFAULT`), so `insertColumns` cannot carry it at all, and conflicting on a generated column now
  needs `database.Raw` or a schema change. `database.Raw` is a real step down: it replaces the whole
  statement, so the builder's identifier validation and vendor-specific quoting no longer apply to any
  part of it, and the SQL becomes yours to review and keep portable.
- verify: `go build ./... && go test ./...`
- ref: [ADR-028](adr_028_pg_upsert_binds_update_values.md) — context only: it decides that the
  PostgreSQL upsert binds update values rather than reusing `EXCLUDED`, and states no vendor-parity
  policy. The policy this atom applies — `BuildUpsert` expresses what **both** vendors can express,
  so Oracle's constraints are the floor, and the cost is named in `after:` — is stated here and in
  C59.7, not in an ADR. · `database/internal/builder/helpers.go`
  (`requireConflictColumnsInInsertSet`) · `database/internal/builder/oracle.go` (`BuildUpsert`) ·
  `database/internal/builder/postgres.go`

### [C59.9] `BuildUpsert` accepts an Oracle conflict column whose spelling differs from the insert key · silent-behavior · when: match

- detect: `git grep -n 'BuildUpsert' -- '*.go'` — **both** production and test call sites, not tests
  alone. A test pinning the old rejection fails loudly; a production call site that *branched* on that
  error is the quiet one. Keep the pattern plain; `git grep -E` is POSIX ERE and silently ignores
  `\b`, `\s`, `\d` and `\w`, so a pattern carrying one matches nothing and the gate falsely reports
  "not affected".
- scope: `database/internal/builder/helpers.go` only — `requireConflictColumnsInInsertSet` compares by
  `columnIdentity` instead of by raw map key, so it sees columns the way the active vendor does and
  agrees with the two preconditions around it. **Strictly more calls are accepted and none newly
  rejected**, because two identical strings always have identical identities. Only Oracle changes, and
  the newly-accepted calls fall into two groups that must not be read as one. **Valid, and they now
  execute:** case variants (`["id"]` against `{"ID": …}`) and whitespace-padded keys (`[" id "]`
  against `{"id": …}`, since the renderer trims before folding) — both render to the same identifier,
  so the MERGE is correct. **Accepted by the precondition but still broken downstream:**
  function-shaped keys (`["count(*)"]` against `{"COUNT(*)": …}`), which `buildOracleMerge` renders
  into `SELECT :1 AS COUNT(*)` — not a legal Oracle alias. Those pass this check and fail later; the
  relaxation does not fix them and is not an endorsement of them. PostgreSQL quotes every identifier,
  so its case and whitespace variants stay distinct and those pairings stay rejected; Oracle's quoted
  reserved words stay case-sensitive, so `level` against `LEVEL` stays rejected too. This is the same
  `columnIdentity` mechanism C59.7 applied to the overlap check, at the opposite polarity — there
  identity matching widened the *rejected* set, which made it breaking; here it widens the *accepted*
  set. Residuals deliberately left: a caller-quoted conflict column still will not match an unquoted
  insert key (`"ID"` vs `id`), which is exactly the residual C59.7 carries, so the two checks now agree
  rather than either being complete; and dotted and whitespace-only keys join function-shaped ones in
  reaching paths that were already invalid — read that as the state of this hop only: `[C60.11]`
  rejects those keys outright, so the acceptance described here no longer stands.
- gate: match = an Oracle `BuildUpsert` call whose conflict column can differ in spelling from the
  insert key it names — **whether or not a test pins it**. Include any call site that treats a builder
  error as control flow (a fallback, a retry, an alert), and any assertion naming *which* column the
  error reports: with several conflict columns the reported one can change, because a now-matching
  column is skipped and a later one is named instead. no-match = every conflict column is spelled
  exactly as its insert key, which is the normal case.
- before: the check was a raw map-key lookup, so `["id"]` against `{"ID": 1}` was rejected on Oracle
  even though the generated MERGE would have been valid: the USING clause aliases the insert key
  (`:1 AS ID`) while the ON clause keeps the conflict column's spelling (`target.id = source.id`), and
  Oracle folds both unquoted forms to `ID`.
- after: for the valid group the call builds and, once executed, **writes** — a MERGE that previously
  never ran because the builder refused it. No previously-*succeeding* call changed, so nothing
  silently produces different SQL; what changed is that a fail-closed path became a fail-open one.
  Read it that way when auditing: a case or whitespace mismatch that used to surface as a build-time
  error is now upserted on the column Oracle folds it to, which is the intended column — but if that
  mismatch was masking a genuinely wrong column name in your call, the error that would have caught it
  is gone. A test pinning the old rejection fails at test time and should be inverted or deleted.
- verify: `go build ./... && go test ./...`
- ref: `database/internal/builder/helpers.go` (`requireConflictColumnsInInsertSet`, `columnIdentity`) ·
  #992 · #997, closed by `[C60.11]` (the already-invalid paths this hop did not fix)

### [C59.10] `BuildUpsert` rejects a conflict column list that names one column twice · breaking · when: match

- detect: `git grep -n 'BuildUpsert' -- '*.go'` lists every call site. Keep the pattern plain — `git
  grep -E` is POSIX ERE and silently ignores `\b`, `\s`, `\d` and `\w`. Nothing is compiler-caught;
  the failure is a returned error at run time. Grep finds the calls but not the duplicate, since
  `conflictColumns` is usually assembled dynamically — a list built by appending a tenant key and a
  business key is the shape that produces one twice.
- scope: `database/internal/builder/` only — a helper `requireUniqueConflictColumns` (`helpers.go`)
  called from `BuildUpsert` (`oracle.go`) ahead of the other preconditions — generalized to
  `requireDistinctColumnIdentities`, which serves all three column groups, in `[C60.11]`. Duplicates are keyed by
  **vendor identity**, so the same list can be legal on one vendor and rejected on the other: Oracle
  folds the unquoted identifiers it emits, making `["id", "ID"]` one column named twice, while
  PostgreSQL quotes every identifier, so there `"id"` and `"ID"` are two columns and a legitimate
  composite conflict target that still builds. Oracle's quoted reserved words stay case-sensitive, so
  `["level", "LEVEL"]` is two columns there and is also still accepted. **Scope limit, stated because
  the check does not close the whole class:** only `conflictColumns` is deduplicated. `insertColumns`
  and `updateColumns` are maps, so they cannot hold an *exact* duplicate, but two keys can still fold
  to one Oracle column (`{"id": 1, "ID": 2}`), which builds a MERGE with a duplicate alias in the
  USING clause and an ambiguous `source` reference. That path is unchanged here and closed by `[C60.11]`.
- gate: match = a `BuildUpsert` call whose `conflictColumns` can hold the same column twice — an exact
  repeat on either vendor, or on Oracle a case variant of a non-reserved identifier (its reserved
  words are quoted and stay case-sensitive). no-match = every list is built from distinct columns.
- before: neither vendor complained, and the two behaved differently. PostgreSQL emitted
  `ON CONFLICT ("id", "id")`, which no unique index can match, so the statement failed at execution
  with `42P10 there is no unique or exclusion constraint matching the ON CONFLICT specification`.
  Oracle emitted `ON (target.id = source.id AND target.id = source.id)` — a redundant tautology, but
  **valid SQL that executed correctly**, so a duplicate there was silently harmless.
- after: both vendors return `conflict columns must be distinct: "id" and "id" name the same column
  for upsert` before
  any SQL is produced. On PostgreSQL this only moves an existing failure earlier, from execution to
  build. **On Oracle it turns a working call into a failing one** — that is the breaking half, and it
  is deliberate: a duplicate conflict target expresses nothing the single column does not, and
  accepting it on one vendor while the other fails is the divergence this whole hop is closing. The
  remedy is to de-duplicate the list at the call site, which changes no generated semantics on either
  vendor. If the list is assembled dynamically, de-duplicate by the vendor's own rules — lower-casing
  before comparison is wrong on PostgreSQL, where case distinguishes columns.
- verify: `go build ./... && go test ./...`
- ref: `database/internal/builder/helpers.go` (`requireUniqueConflictColumns`, `columnIdentity`) ·
  `database/internal/builder/oracle.go` (`BuildUpsert`) · #992

### [C59.11] `database.tls` fails closed: PG mode allowlist + material requires a TLS-mandatory mode; Oracle rejects the whole block · silent-config · when: match

- detect: two greps, one per config shape. `grep -rniE '^[[:space:]]*tls:' config*.yaml deploy/`
  catches the standard nested mapping (`database:` → `tls:` → `mode:` — the form
  `config.example.yaml` shipped live before this change, which a dotted-key grep cannot see; it
  also matches any other nested `tls:` block, such as the server's, so inspect the hits). `grep -rniE 'database\.tls\.|DATABASE_TLS_'
  config*.yaml deploy/` catches dotted keys and environment-variable overrides, which the nested
  grep cannot see. Run both across every environment, including the ones you do not deploy from
  this repo. Add `git grep -n 'TLS' -- '*.go'` if you assemble a `config.DatabaseConfig` in Go.
  Nothing here is compiler-caught — the struct is unchanged — so the failure is a validation
  error: at startup for static configuration, at first connection acquisition for dynamic
  `DBConfigProvider` records. The decisive check is booting each environment — plus, for
  dynamic tenants, acquiring a first connection per tenant, since their boot stays green.
- scope: `config/validation.go` only (`validateVendorSpecificFields` and the two vendor functions it
  dispatches to), so the rules run wherever `config.Validate` does: the root `database:` block, every
  `databases.*` entry, and every static `multitenant.tenants.*` entry — and, since #1002 (C59.6),
  the dynamic seam too: `ApplyDatabasePoolDefaults` runs the same vendor validation on every
  `DBConfigProvider` record, at connection acquisition. All four TLS fields are
  `TrimSpace`d once at the dispatch seam, and the trim persists into the DSN. **Not** covered: a
  `connectionstring` whose scheme is unrecognized (the dispatch's `default` arm returns nil, so the
  block stays inert there). The `tools/migration` CLI is the other gap, and **at this hop it
  is still open**: it never calls `config.Validate`, so every shape below reaches its
  `quiesce` DSN builder, which forwards `mode`/`ca` as `sslmode`/`sslrootcert` unvalidated.
  `database.tls` does not reach Flyway at all at this version — only
  host/port/user/password/database are passed, and the JDBC URL comes from the operator's own
  `flyway.conf`. Both halves are closed in the next hop by [C60.1] and [C60.2]; on v0.59.0,
  treat the CLI as unvalidated.
- gate: match = validation now rejects four shapes that previously booted — at startup for
  static configuration, while a dynamic `DBConfigProvider` record carrying the same shape
  still boots green and is rejected at first connection acquisition (#1002 seam). (1) PG
  `database.tls.mode` outside `disable`/`allow`/`prefer`/`require`/`verify-ca`/`verify-full` — a typo
  such as `Require` or `verify_full` used to fail at first *connect* with a parse error go-bricks
  deliberately redacts, and in multi-tenant deployments connections are lazy, so it surfaced at first
  request rather than at boot. (2) PG `cert`/`key`/`ca` with `mode` empty, `disable`, `allow` or
  `prefer` — pgx returns a nil TLS config under `disable` before it ever reads the cert files, and
  sets `InsecureSkipVerify` plus a plaintext fallback under the other three, so the material was
  silently discarded or the connection silently downgradeable. An unset mode is the common case: it
  defaults to `prefer`. The one inversion: `ca: system` under any of these modes was silently
  *upgraded* — pgx rewrites the sslmode to `verify-full` before its mode switch — so that shape did
  verify, just not the way the config read. (3) PG any `database.tls.*` alongside `connectionstring` — `NewConnection`
  uses the DSN verbatim and never consults the block. (4) Oracle `database.tls.mode` — always a no-op,
  and `cert`/`key`/`ca` were already rejected by [C42.1], which stated that "`database.tls.mode` alone
  still passes". It no longer does. no-match = you set no `database.tls.*` key anywhere.
- apply: PostgreSQL — set `database.tls.mode` to `require`, `verify-ca` or `verify-full` wherever
  `cert`/`key`/`ca` are configured, and know what each buys: `verify-ca`/`verify-full` — or `require`
  with a `ca`, which pgx upgrades to verify-ca semantics — verify the server for the first time
  (confirm the CA chain, or the connection now fails where it previously succeeded unverified), while
  bare `require` encrypts **without authenticating the server**: it satisfies the validator, and any
  host that answers still gets the client cert, the password, and every query. Fix any
  misspelled mode against the allowlist; the framework does not case-fold, since `Require` is a typo
  and pgx is case-sensitive. With `connectionstring`, move the settings into the DSN
  (`sslmode`/`sslrootcert`/`sslcert`/`sslkey`) and delete the block — that DSN is also the escape
  hatch if you deliberately want pgx-native semantics these rules refuse, such as `prefer` with a
  client certificate. The likeliest R4 hit is historical: the pre-change `config.example.yaml` shipped
  `mode: disable` filled in, so a copied example plus a later `connectionstring` reproduces the
  rejected pairing verbatim. Oracle — delete the `database.tls` block entirely. A valid mode with **no**
  material still passes on PostgreSQL, `disable` included, so a plain `mode: disable` needs no change.
- verify: `go build ./...`, then start the app against the database in each environment — a rejected
  static shape fails startup with `database.tls` (or `database.tls.mode`) named in the error and the
  remedy in its action line. For dynamic `DBConfigProvider` tenants a clean boot proves nothing:
  acquire a first connection per tenant (hit a per-tenant endpoint or its readiness probe) and
  expect the same error at acquisition.
- ref: [ADR-062](adr_062_database_tls_fail_closed.md) · [C42.1] (the historical record this
  supersedes) · `config/validation.go`

### [C59.12] `app.NewWithConfig` and `app.Builder.WithConfig` validate the config · breaking · when: match

- detect: `git grep -n 'NewWithConfig\|NewAppBuilder\|NewWithOptions'` in your service. Every hit
  handing over a hand-built `*config.Config` (not `config.Load` output) is in scope —
  `NewWithOptions` counts when its `Options.ConfigLoader` returns one — most services construct via
  `app.New()` and are unaffected.
- scope: `app.Builder.WithConfig` now runs `config.Validate`. The rules are the ones `config.Load`
  always applied; only the bypass is gone (ADR-050 documented the obligation, nothing enforced it).
- gate: match = a hand-built config violating any `config.Validate` rule — empty `app.name` or
  `app.version`, zero `server.timeout.*`, an invalid `database.type`, a negative pool value — now
  fails construction with `invalid configuration: …` naming the field. no-match = you construct via
  `app.New`, or `NewWithOptions` with the default loader, or you already ran `config.Validate` first.
- apply: fix the config, not the call site — each rejection's `ConfigError` carries an action line.
  Test fixtures are the common hit: give them `app.name`, `app.version`, positive server timeouts.
- verify: run your service's tests; construction-time failures name the field.
- ref: [ADR-064](adr_064_app_validates_every_config.md) · [ADR-050](adr_050_connectionstring_type_inference.md)

### [C59.13] `keystore.secretminlength` becomes `*int` (nil = 32, 0 = off, deprecated) · compile-break · when: match

> **Superseded on the E62 hop by `[C62.3]`** (ADR-095). The `0` this atom keeps meaning "floor
> off" is rejected there: `keystore.secretminlength` becomes mandatory at 32 and can only be
> raised, so `new(0)` fails startup rather than disabling the floor.

- detect: `git grep -n 'SecretMinLength' -- '*.go'` in your service shortlists the files; the hits that
  assign the field (a literal or a `cfg.KeyStore.SecretMinLength = …`) are in scope, reads and helper
  code are not. YAML/env is out of scope for this atom — `keystore.secretminlength: 0`
  keeps meaning off (it now WARNs at startup, deprecated per #1036) and `N` keeps meaning a floor of `N`;
  a config that never set the field is [C59.14].
- scope: the field is now a tri-state pointer: nil applies the default (32), `0` keeps the floor off
  (deprecated), `N > 0` sets the floor. `SecretMinLength: 0` and `SecretMinLength: N` no longer compile.
- gate: match = at least one Go literal sets the field. no-match = no Go code sets it; then read [C59.14].
- apply: `SecretMinLength: new(0)` / `new(N)` — Go 1.26's `new(expr)` — same as `cache.critical`'s
  `Critical: new(false)`. Expect the deprecation WARN wherever `0` stays.
- verify: `go build ./...`.
- ref: [ADR-065](adr_065_keystore_secretminlength_tristate.md) · [ADR-046](adr_046_cache_readiness_strict_default.md)

---

### [C59.14] a hand-built config that never set `SecretMinLength` now enforces the 32-byte floor · silent-behavior · when: no-match

- detect: `git grep -n 'NewWithConfig\|NewAppBuilder\|NewWithOptions\|WithConfig(' -- '*.go'`
  shortlists every hand-built `*config.Config` (not `config.Load` output — `NewWithOptions` counts when
  its `Options.ConfigLoader` returns one, as in [C59.12]). Then read each one: you are in scope when its
  `KeyStore.Keys` carries a `Secret` entry and nothing assigns `SecretMinLength` for that config. The grep
  only shortlists — before the bump the Go zero `0` disabled the floor silently and nothing in the file
  says so, so a grep that finds nothing does not clear you; only reading the config does.
- scope: `config.Validate` (which every construction path runs, [C59.12]) now fills the unset field with
  32, and `keystore.Module.Init` rejects any symmetric secret shorter than that at startup — the same
  behavior a YAML config always had. Only the literal-config door changes.
- gate: match = a hand-built config with symmetric secrets shorter than 32 bytes and no `SecretMinLength`
  set. no-match = every symmetric secret is at least 32 bytes, or the field is set (then [C59.13]).
- apply: raise the secret to 32 bytes or more; if a partner-mandated short key makes that impossible,
  set `SecretMinLength: new(0)` deliberately (deprecated, WARNs, #1036) or `new(N)` for a floor you can meet.
- verify: the service starts; on the affected shape it exits during startup with
  `keystore: key "<name>": secret is <n> bytes, minimum is 32`.
- ref: [ADR-065](adr_065_keystore_secretminlength_tristate.md)

---

## E60 · v0.59.0 → v0.60.0 — `go-bricks-migrate` validates every resolved database config and forwards `database.tls` to Flyway + readiness speaks one status vocabulary + the AMQP lane moves onto the shared delivery pipeline

- gist: The `go-bricks-migrate` CLI now validates every tenant's resolved database config the
  way the framework does before dialing (C60.1) and forwards `database.tls` to Flyway as
  `DB_SSLMODE`/`DB_SSLROOTCERT`/`DB_SSLCERT`/`DB_SSLKEY` (C60.2). Readiness: `app/readiness.go` judges every readiness kind (database, messaging,
  cache, streams) from one probe description with one lease → liveness →
  status machine, and renders both readiness views from one probe run
  (ADR-066). The strings each kind used to invent are gone: `details.status`
  mirrors the component status, `unhealthy` always carries an error, streams
  reports `unhealthy` where it reported `not_ready`, a disabled kind's stats
  render `{"status":"disabled"}` for every kind, and messaging and cache read
  `per_tenant` in a multi-tenant deployment where they read `not_configured`
  for the fixed `""` key. The 200 body now renders `<kind>` and `<kind>_stats`
  for every registered kind, so `db_stats` becomes `database_stats`; the debug
  health view lists every classic kind and drops its separate
  `database_manager` / `messaging_manager` entries, whose statistics are now
  the `database` and `messaging` entries' details; and both views count from
  one predicate, so a non-critical kind that is not live reads `degraded`
  instead of `unknown` on `overall_status`. No status code changes for any
  deployment: the kinds that answered 200 still answer 200, and only a
  critical kind whose status is `unhealthy` answers 503 — the same kinds as
  before. Sixteen unused exported app symbols are also removed and eight
  debug response types unexported, with no change to any emitted JSON
  (C60.4).
  Idle-cleanup maintenance also moves manager-side: `database.DbManager` and
  `messaging.Manager` start their idle sweep at construction and stop it in
  `Close()`, as the cache manager already did, so five app-level cleanup-loop
  log lines retire and the cleanup-interval advisory fires earlier; sweep
  frequency, shutdown order and every emitted body are unchanged (C60.5).
  The AMQP lane now runs on the shared delivery pipeline
  (`messaging/internal/delivery`, ADR-068; the streams lane follows in ADR-068's
  named follow-up): `messaging.StartConsumeSpan` is removed
  with no replacement export, and the classic lane's
  `messaging.client.consumed.messages` is recorded at completion with `error.type`
  instead of at receive without it (C60.6). On the same lane, the failure and panic
  lines' second `correlation_id` stamp — the delivery's own AMQP `CorrelationId` —
  is renamed `amqp_correlation_id`, so `correlation_id` carries the framework trace
  ID and nothing else on every line of both lanes; both values are still emitted
  (C60.7). Inbound trace identifiers are also validated at the `trace` seam now:
  an `X-Request-ID` outside `^[A-Za-z0-9_-]{1,128}$`, a `traceparent` that is not
  spec-exact, and a `tracestate` over 512 bytes are DISCARDED rather than stored,
  and the delivery continues with a traceparent-derived id or a fresh UUID
  (ADR-070, C60.8). On the publish side, a prepared message's `CorrelationId` is read back
  from the `X-Request-ID` header the same injection just wrote instead of being derived a
  second time from the context, so the two carry the same id — or, where that id fails the
  ADR-070 charset check, the header carries it and the property carries nothing — where they
  disagreed on every publish out of an HTTP-originated context (C60.10). Finally, `BuildUpsert`
  finishes the precondition class C59.10 left open: `insertColumns` and `updateColumns` must each
  name every column at most once by the column each key NAMES — two keys folding to one Oracle
  column built a MERGE with a duplicate alias and no error — and the membership and overlap checks
  now key on that same rule, so an Oracle conflict column spelled `"ID"` is no longer updatable as
  `id` (ORA-38104 at execution before). On Oracle every key must also be a single column name,
  since the USING clause names each one as a column alias; on BOTH vendors a key may not carry an
  unescaped interior quote, because neither escaper doubles it. PostgreSQL is otherwise untouched
  (ADR-071, C60.11). Separately,
  `config/testkeys.go` is deleted: all 33 `TestKey*` constants had no call site anywhere, and
  five of them named keys the loader does not read — `database.connection_string` for
  `connectionstring`, and four `messaging.broker.*` keys that have never existed (ADR-073,
  C60.14). Last, the three doors C60.8 did not reach are closed on its terms: the HTTP
  ingress `traceparent`/`tracestate`, which `enrichTraceContext` read straight off
  `req.Header` (invalid ⇒ treated as absent and minted afresh, never a rejected request);
  the response header `ensureTraceParentHeader` reflected raw at six call sites, plus the
  access-log field; and the classic AMQP lane's four delivery identity fields —
  `CorrelationId` and `MessageId` in the content-header properties, `RoutingKey` and
  `Exchange` in the `basic.deliver` envelope — none of which a header extractor reaches and landed verbatim in log
  fields, span attributes and metric attributes. Those four are
  judged once per delivery and OMITTED where they fail — so a field the delivery did not
  carry is now absent from the line rather than present and empty (ADR-070 amended, C60.17).
  Last, `server.ClientIP` — the derivation behind the debug-endpoint allowlist and the
  `/_sys/job` guard — answers only from an identified untrusted hop or the peer it actually
  observed, never a caller-written value, and the two trusted-proxy keys that accepted a
  default route where `server.trustedproxies` refused one now refuse it too. A default
  route made every peer trusted, so a DIRECT caller's `X-Real-IP` or `X-Forwarded-For` was
  believed by both access-control checks with no proxy transit required (ADR-080, C60.22).
  And the 5xx response body joins the log sinks' gate: the framework's own `details.error`
  entry — raw error text on an unhandled 500 or a recovered panic — used to attach under
  `app.env` alone while both log lines withheld it under `app.debug`, so a development
  environment with debug off silenced the copy the operator reads and kept shipping the copy
  the caller reads; it now requires both keys, and the stricter one wins (ADR-081 addendum,
  C60.30).
  And the `RawExpression` escape hatch is validated where it is consumed: `Expr()` refused an
  empty SQL body and an alias carrying a SQL metacharacter, but the type is a plain struct, so
  a literal skipped the check and `Select` rendered its alias verbatim; every consuming door —
  `Select`, `GroupBy`, `OrderBy`, the `JoinFilter` value comparisons and both `Between` bounds
  — now calls the same `Validate()` funnel, so a literal fails from `ToSQL()` with the sentinel
  `Expr()` would have returned (ADR-082 addendum, C60.29).
  And the table alias handed to `Columns.As` is validated against the same bare-identifier
  grammar the table argument applies to the alias half of `"users u"`: `As` refused only an
  empty alias, then every `Col`/`Cols`/`All` rendering emitted `alias + "." + column`
  verbatim, and `Having` still carries that string into the statement unexamined. `As` has
  no error channel, so a refused alias PANICS with a typed `*dbtypes.InvalidAliasError`; the
  grammar itself moves to `database/internal/sqllex` so the builder, the columns package and
  the db-tag parser source one copy (ADR-082 addendum, C60.28).
- build-caught: C60.4; C60.6; C60.14
- preflight: if you run `go-bricks-migrate`, `go-bricks-migrate info` per tenant before the bump
  and grep `flyway.conf`/the migration environment for `DB_SSLMODE`/`DB_SSLROOTCERT`/`DB_SSLCERT`/`DB_SSLKEY`
  (C60.1, C60.2); grep dashboards, alerts, synthetic checks and contract fixtures
  for the retired strings and for `*_stats` objects pinned to `{}` (C60.3); and grep
  log-based alerts and saved queries for the five retired cleanup-loop lines
  (`Starting/Stopping database manager cleanup loop`, `Starting/Stopping messaging
  manager cleanup loop`, `Manager cleanup loops stopped`) — they have no renamed
  equivalent (C60.5); and grep the same places, plus any log-parsing test, for
  `correlation_id` read off the AMQP consumer's failure and panic lines (C60.7);
  and search the same backend for `correlation_id` values outside 1-128 characters
  of `A-Za-z0-9_-`, which are now discarded at ingress (C60.8); and if you consume
  native streams, re-read the streams consumer's failure and panic log lines and its
  consume span, both of which change shape (C60.9); and if a consumer or a dashboard reads a
  message's AMQP `CorrelationId` property rather than its `X-Request-ID` header, re-read what
  that property now holds (C60.10); and if you build `config.Config` in
  code, note that `scheduler.timeout.slowjob` now normalizes to 25s where the module used
  to fall back to 30s (C60.12); and if you build Oracle upserts, re-read the `BuildUpsert` call
  sites whose column maps are assembled dynamically, including any conflict column spelled
  differently from the insert or update key naming the same column; and on PostgreSQL, where
  nothing else changes, re-read those same call sites for a key containing a quote that is not
  doubled (C60.11); and if any code compares `config.ConfigError.Field` to a
  literal `database.*` key, switch it to a database-scoped predicate — a non-root section now
  names itself in that field, and a bare suffix match would also catch `cache.redis.host`
  (C60.16); and if you resolve per-section database configs yourself, move to the additive
  `config.ApplyDatabasePoolDefaultsForKey` (the old function still compiles and still answers
  for the root), then re-point those same `Field` matchers once more — the runtime door
  now addresses a dynamic tenant the way a static one is addressed, the MANAGER stops wrapping
  the key back in — the CLI keeps its `tenant "<id>":` wrap until the pin bump — and a non-root
  `Action` names its own env var or none (C60.19);
  and grep your log calls for field names containing `key`,
  since the default filter stops masking them (C60.13); and `git grep -nE '(^|[^[:alnum:]_])TestKey[A-Za-z0-9_]*([^[:alnum:]_]|$)' -- '*.go'`, since those
  constants are gone — match the identifier, not the `config.` qualifier, so an aliased or
  dot-imported reference cannot hide from the sweep (C60.14); and grep every deployment surface for a go-bricks variable
  set to nothing (`FOO=`, `FOO=""`, a structured `value: ""`, a `secretKeyRef` whose stored value
  is empty, an `envsubst` over an unset variable), because a numeric key delivered empty now fails
  configuration resolution instead of decoding as `0` — at startup for the service's own config,
  at first use for the CLI's `tenants.yaml` and a dynamic `DBConfigProvider` payload (C60.15);
  and read the same sweep's hits again for BOOL keys, which now fail the same way — the three
  that changed behaviour rather than restating `false` are `DATABASE_POOL_KEEPALIVE_ENABLED`
  (a default-true → false flip that turned TCP keep-alive off), `CACHE_CRITICAL` (strict
  readiness disabled, so `/ready` answered 200 through a cache outage) and `SERVER_LOGROUTES`
  (C60.18);
  and search the same log backend for a non-spec-exact `traceparent` field and for
  `amqp_correlation_id`/`message_id`/`routing_key`/`exchange` values a foreign publisher or a
  non-ASCII exchange name put there — and note this preflight runs BEFORE the bump, so on a
  pre-C60.7 version the delivery's own AMQP CorrelationId is still logged as `correlation_id`
  on the failure and panic lines; search that name too, restricted to those two lines so the
  framework trace id on every other line does not swamp the result — plus any
  saved query or alert that treats one of those four as always present — each is now omitted
  where it fails validation, and absent rather than empty where the delivery carried none
  (C60.17); and grep every deployment surface for `DEBUG_TRUSTEDPROXIES`,
  `SCHEDULER_SECURITY_TRUSTEDPROXIES` and `SERVER_TRUSTEDPROXIES` — read the VALUES on all
  three. A literal `0.0.0.0/0` or `::/0` is newly refused on the first two, which
  `server.trustedproxies` already rejected; but that key is NOT unaffected, because C60.22
  also newly rejects a list whose entries TOGETHER cover a family
  (`["0.0.0.0/1","128.0.0.0/1"]`) and the v4-mapped `::ffff:0.0.0.0/96` — on all three keys.
  Then check `debug.allowedips` twice: for an entry that is
  neither an IP address nor a CIDR range, AND for one that parses cleanly as a CIDR but has
  HOST BITS SET (`192.168.1.55/16`). The second form is newly refused too, so an entry that
  clears the first check can still fail startup — write the canonical network address
  (`192.168.0.0/16`) or the single host without a prefix. Note the ALLOWLIST keys may still
  legitimately hold a default route (C60.22); and if any environment runs a development
  `app.env` alias with `app.debug` false or unset, grep contract tests, frontends and curl
  scripts for a reader of `details.error` on a 5xx body — that entry now needs both keys
  (C60.30); and `git grep -nE 'RawExpression\{' -- '*.go'`
  — every struct literal is now validated at consumption, so read each one for an empty SQL
  body or an alias carrying a SQL metacharacter (C60.29)
  ; and `git grep -nE '\.As\(' -- '*.go'` — every `Columns.As` argument whose RUNTIME
  VALUE is not a single bare identifier — or the framework's own quoted form — now panics at
  the call, so a literal `"u"` and a parameter that carries `"u"` both still pass; read each
  NON-LITERAL call site for the values it can actually carry, and each literal against the
  grammar (C60.28)
- exit: `go get github.com/gaborage/go-bricks@v0.60.0 && go mod tidy && go build ./... && go test ./...`

### [C60.1] `go-bricks-migrate` validates every resolved database config · breaking · when: match

- detect: only the `tools/migration` CLI (`go-bricks-migrate`) is affected, not services —
  services already validated at startup. Sweep the configs the CLI reads: the
  `--source-config` YAML for `--credentials-from config-file`, and the per-tenant secret
  payloads under `--secrets-prefix` for `aws-secrets-manager`. The secrets path cannot be
  grepped from your repo; enumerate the prefix in each account. The decisive check is
  `go-bricks-migrate info --tenant <id>` per tenant, which resolves a config without
  touching the schema.
- scope: `tools/migration/internal/commands/dbtls.go`, wired at `buildConfigProvider`, so
  it applies to every subcommand (`migrate`, `info`, `validate`, `list`, `quiesce`) and both
  credentials sources. It calls the exported `config.ApplyDatabasePoolDefaults`, the same
  seam the framework runs before dialing, so there is no second copy of the rules to drift.
  Rejection is per resolved tenant at resolution time, with the tenant named in the error, so
  a fleet run fails on the offending tenant and `--continue-on-error` skips past it like any
  other per-tenant failure. Validation runs on a copy: `config.TenantStore` returns a cached
  shared pointer for the single-tenant and `named:` keys, and the seam normalizes in place.
- gate: match = the CLI previously ran no validation at all, so this is wider than TLS.
  Rejected now: every `database.tls` shape of [C59.11]; an Oracle config with no connection
  identifier (`oracle.service.name`, `oracle.service.sid`, or `database`); and a
  `database.timezone` that `time.LoadLocation` cannot load. A missing `database.type` is no
  longer fatal where the `connectionstring` scheme identifies the vendor — it is inferred.
- apply: fix the config, not the CLI. There is no bypass flag, deliberately: accepting a
  config the service will refuse to boot on is the trap this closes. Note the rules arrive
  with the CLI's pin bump, so pinning an older `go-bricks-migrate` defers them.
- verify: `go-bricks-migrate info` against every tenant in every environment. A clean exit
  means every resolved config passes; there is no partial mode.
- ref: [ADR-062](adr_062_database_tls_fail_closed.md) · [C59.11] · [C60.2] ·
  `tools/migration/internal/commands/dbtls.go`

### [C60.2] Flyway receives `DB_SSLMODE`/`DB_SSLROOTCERT`/`DB_SSLCERT`/`DB_SSLKEY` · silent-behavior · when: match

> **Superseded on the E61 hop by `[C61.4]`** (ADR-085). The residual gap this atom closes with a
> WARN — "confirm from the database side that the connection is actually encrypted, since the WARN
> cannot" — is closed outright there: the framework builds the JDBC URL and passes `-url=`, so the
> conf can no longer drop the TLS parameters. The `DB_SSL*` variables below are **no longer
> exported** as of v0.61.0, and a PostgreSQL `cert`/`key` migration now fails rather than
> needing the PKCS-8 conversion this atom suggests. Read this atom only if you are landing on
> v0.60.x; going straight to v0.61.0, apply `[C61.4]` instead.

- detect: `grep -rn 'DB_SSLMODE\|DB_SSLROOTCERT\|DB_SSLCERT\|DB_SSLKEY' <your flyway.conf(s)>`
  and the environment that launches migrations. Two independent questions: whether your
  `flyway.conf` interpolates these (if not, nothing changes for you), and whether anything in
  your deployment already exports a variable of the same name (if so, read the precedence
  note below).
- scope: `migration/flyway.go` (`buildEnvironmentVariables`, `validateEnvFields`), so it
  applies to the framework's Flyway runner generally — `go-bricks-migrate` and any consumer
  calling `MigrateFor`/`MigrateAll` alike. PostgreSQL only; Oracle has no arm, because
  `database.tls` is rejected outright for Oracle. Unset fields are omitted rather than
  exported empty, so a conf interpolating them unconditionally never receives a bare
  `sslmode=`. The four fields also join the existing CR/LF/NUL env-field guard.
- gate: match = additive for almost everyone — before this, `database.tls` never reached
  Flyway in any form, so a `verify-full` block validated cleanly while the migration ran with
  whatever TLS the JDBC URL specified, possibly none. **The one regression-shaped case:**
  framework variables are appended after `os.Environ()`, so if your deployment exports
  `DB_SSLMODE` (or any of the other three) itself *and* the resolved config carries
  `database.tls`, the config's value now wins where the environment's used to. A deployment
  that sets one but not the other is unaffected.
- apply: if you want migrations encrypted, add the variables to your JDBC URL using Flyway's
  `${env.NAME}` substitution — `?sslmode=${env.DB_SSLMODE}&sslrootcert=${env.DB_SSLROOTCERT}`.
  A bare `${DB_SSLMODE}` is Flyway's *placeholder* syntax, resolved from
  `flyway.placeholders.*`, and never reads the environment. Reference only the variables your
  config actually sets: unset fields are not exported, so naming `sslrootcert` while
  `database.tls.ca` is empty leaves the placeholder unresolved. Note that `database.tls.key`
  is validated against libpq semantics but pgjdbc's `sslkey` wants PKCS-8 DER, not PEM —
  convert it, or use a mode needing no client certificate. **Correction (2026-08-24):** that
  is not right. pgjdbc's `LibPQFactory` dispatches on the file name — a `.key` or `.pem` path
  goes to `PEMKeyManager`, which reads an unencrypted PKCS#8 PEM; only other names go to
  `LazyKeyManager`, which wants DER. Converting is unnecessary for an unencrypted PKCS#8 PEM
  at such a path. A PKCS#1 (`BEGIN RSA PRIVATE KEY`) or encrypted PEM is still unreadable by
  either manager.
- verify: run `migrate` and read the WARN. The framework does not parse your conf, so it warns
  once per migrator whenever `database.tls` is set, naming the conf path; confirm from the
  database side (`pg_stat_ssl`) that the connection is actually encrypted, since the WARN
  cannot. Closing that last gap — so a TLS-configured migration cannot run in cleartext at
  all — is tracked as #1047.
- ref: [ADR-062](adr_062_database_tls_fail_closed.md) · [C60.1] · [C61.4] (supersedes) · `migration/flyway.go`

### [C60.3] `/ready` and `/_sys/health-debug` speak one status vocabulary · silent-behavior · when: always

- detect: `git grep -n '"/ready"' -- '*_test.go'` and, across dashboards, alerts, synthetic
  checks and contract fixtures,
  `git grep -rn 'db_stats\|not_ready\|connection_failed\|no_active_connections\|messaging_stats\|cache_stats\|overall_status\|unknown\|database_manager\|messaging_manager' --`.
  You are looking for anything that reads `db_stats`, matches one of the retired sub-status
  strings, pins a disabled kind's `messaging_stats`/`cache_stats` to `{}`, alerts on the debug
  summary's `overall_status == unknown`, or reads the debug view's `database_manager` /
  `messaging_manager` entries.
- scope: this atom now covers both readiness slices (ADR-066 §Delivery) — the shared status
  vocabulary from the first, the `db_stats` → `database_stats` rename and the debug-summary
  changes from the second. Readiness is one module now (ADR-066): every kind is judged by the
  same lease → liveness → status machine from a probe description, so the strings each kind
  used to invent are gone. Status codes do not change for any kind that was ready before —
  `disabled`, `not_configured`, `per_tenant` and `healthy` still answer 200, and only a
  *critical* kind whose status is `unhealthy` answers 503, exactly the kinds that answered
  503 before.
- gate: always. Every service serves the unified strings whether or not it configures any of
  the four kinds.
- apply: eight changes, all in body strings or the debug view — (1) `<name>_stats.status`
  mirrors the component's status: `unhealthy` where it used to say `no_active_connections`
  (database lease failure), `connection_failed` (messaging or cache lease failure) or
  `not_ready` (messaging leased but not ready); (2) `streams` reports `unhealthy` where it
  reported `not_ready`; (3) a `disabled` kind's `<name>_stats` is `{"status":"disabled"}` for
  every kind (messaging and cache used to render `{}`); (4) in a multi-tenant deployment
  (`multitenant.enabled: true`) messaging and cache report `per_tenant` where they reported
  `not_configured` for the fixed `""` key — the database already did; (5)
  `/_sys/health-debug`'s `components` map now carries every classic kind (a nil manager
  appears as `disabled`, which counts as healthy in the summary), and a non-critical kind
  that is not live now reads `degraded`, not `unknown`, on `overall_status`; (6) the 200 body
  renders `<kind>` and `<kind>_stats` for every registered kind, so the database's stats key
  is now `database_stats` — `db_stats` was the one key that did not match its component
  name; (7) `/_sys/health-debug`'s `components` map no longer carries the separate
  `database_manager` and `messaging_manager` entries — their manager statistics are now the
  `database` and `messaging` entries' `details`; and (8) the debug summary's `error_count`
  and `critical_count` are derived from the exact predicate `/ready` gates on (`error_count`
  = kinds whose status is `unhealthy`, `critical_count` = those that are also critical), so
  the two views cannot disagree. Match `unhealthy` instead of the retired strings, read
  `database_stats` instead of `db_stats`, read the `database`/`messaging` entries instead of
  the `*_manager` ones, and drop any `overall_status == unknown` alert in favor of
  `degraded`/`critical`.
- verify: capture one 200 response first — `curl -s -o /tmp/ready.json -w '%{http_code}\n' localhost:8080/ready`
  prints `200` (a `503` carries only `status`/`<name>`/`error`, so the checks below only
  apply to a 200 body) — then `jq '.messaging_stats.status, .cache_stats.status' /tmp/ready.json`
  never prints a retired string, and
  `jq 'has("database_stats"), has("db_stats")' /tmp/ready.json` prints `true`
  then `false`; with the debug endpoint enabled,
  `curl -s localhost:8080/_sys/health-debug | jq '.data.components | keys'` lists `database`,
  `messaging`, `cache` (and `streams` once declared) and no `*_manager` entry.
- ref: [ADR-066](adr_066_readiness_one_module.md)

---

### [C60.4] sixteen unused `app` symbols removed; eight debug response types unexported · compile-break · when: match

- detect: three greps, all against your own Go code —
  `git grep -nE 'app\.(MessagingInitializer|NewMessagingInitializer|ConnectionPreWarmer|NewConnectionPreWarmer)([^A-Za-z0-9_]|$)' -- '*.go'` (the removed methods are only reachable through these types and constructors, so the type names catch every receiver),
  `git grep -nE 'app\.(HealthDebugInfo|ComponentHealth|HealthSummary|DebugResponse|GCInfo|GoroutineInfo|GoroutineStack|PotentialLeak)([^A-Za-z0-9_]|$)' -- '*.go'`, and
  `git grep -nE 'app\.Options\{' -- '*.go'` — then read each `Options` literal for a
  `Database:` or `MessagingClient:` field. Include test files: `go build ./...` does not
  compile them, so a hit there surfaces only under `go vet ./...` or `go test`.
- scope: none of these were ever called by the framework's own consumers — they were
  internal helpers that happened to be exported. `MessagingInitializer` and
  `ConnectionPreWarmer` held a logger plus manager pointers `app.App` already holds and
  were each driven from a single startup line; they are now unexported `App` methods
  (`prepareRuntimeConsumers`, the slots' `start` phase and friends). `Options.Database` and
  `Options.MessagingClient` were read by no code path at all. The eight debug response
  types describe the JSON of `/_sys/health-debug`, `/_sys/goroutines` and `/_sys/gc`; they
  are unexported with their `json:` tags byte-identical. **No emitted JSON, status code or
  startup behavior changes on the shipped `NewWithConfig` chain** — including the #907
  fail-vs-warn consumer grading ([C57.8])
  and the pre-warm publisher-readiness wait, both of which keep their exact semantics at
  their new unexported home. Three log lines are the exception: `LogDeploymentMode`'s INFO
  (`Messaging initialized for {single,multi}-tenant deployment`) and `LogAvailability`'s
  four DEBUG lines (`{Database,Messaging} manager {available,not available} for
  pre-warming`) and `SetupLazyConsumerInit`'s `Unknown resource provider type` WARN (never
  reachable from a shipped `Options`) retire outright with no renamed equivalent — the deployment mode and
  manager availability both remain visible from the surviving `prepareRuntimeConsumers`
  and pre-warm INFO/DEBUG lines. A hand-composed `Builder` chain that skips `ConfigureRuntimeHelpers` — outside
  supported use — used to get neither consumer bootstrap nor pre-warm and now gets both
  whenever the managers exist (ADR-067 §Consequences).
- gate: match = at least one grep names one of these symbols on the `app` package, or an
  `app.Options` literal sets `Database:` or `MessagingClient:`. no-match = the common case;
  a service that only registers modules and calls `app.New*` never touched any of them.
- apply: there is no replacement for the removed helpers — the framework drives every one
  of those paths itself, so delete the call. Concretely: an `app.NewConnectionPreWarmer`
  built to warm a connection yourself is redundant with `app.App`'s own single-tenant
  pre-warm; an `app.NewMessagingInitializer` is redundant with `prepareRuntime`; a
  `Database:` or `MessagingClient:` field in an `app.Options` literal was inert and should
  be dropped (inject through `DatabaseConnector`, `MessagingClientFactory` or
  `ResourceSource` instead); a variable typed `app.GCInfo`/`app.ComponentHealth`/… to
  decode a debug endpoint should decode into your own struct with the same `json` tags —
  the wire shape is unchanged, so copying the tags is a mechanical move. A log-based alert
  or saved query matching the retired `LogDeploymentMode`/`LogAvailability` text (see
  scope) has no drop-in replacement string — re-point it at the surviving
  `prepareRuntimeConsumers`/pre-warm lines that already report deployment mode and manager
  availability.
- verify: `go build ./... && go vet ./...` — vet is the load-bearing half, since a
  reference in a `_test.go` file is invisible to `go build`.
- ref: [ADR-067](adr_067_lifecycle_slots.md)

### [C60.5] idle cleanup starts at manager construction, not at `prepareRuntime` · silent-behavior · when: always

- detect: nothing in your Go code changes, so the grep is for operations —
  search your log backend for the five retired lines `Starting database manager cleanup loop`,
  `Starting messaging manager cleanup loop`, `Stopping database manager cleanup loop`,
  `Stopping messaging manager cleanup loop` and `Manager cleanup loops stopped`. If you also
  construct a manager yourself, `git grep -nE '(database\.NewDbManager|messaging\.NewMessagingManager)\(' -- '*.go'`
  finds the call sites the second half of `apply` covers.
- scope: `database.DbManager` and `messaging.Manager` now start their idle-eviction sweep inside
  their constructor and stop it inside `Close()`, exactly as `cache.NewCacheManager` has always
  done (ADR-067 decision 4). On the framework's own boot path that moves the start earlier —
  before module `Init()` runs, rather than after (the end of `prepareRuntime` shifts to the
  Builder's manager-construction step) — and moves the stop one shutdown phase later, from a
  dedicated phase to the closers that already ran last. **The wall-clock shutdown order is
  unchanged**: the closers run after modules and observability either way (ADR-029). Sweep
  frequency, idle TTL and eviction semantics are untouched, and the HTTP response schema and
  status codes are stable; manager statistics and counters can move with the sweep's earlier
  start. The sweep now runs during module `Init()` and can, in theory, evict the
  pre-init `""` lease before its first use inside a module's `Init` — inert at the default TTLs
  (30m database, 1h messaging) against any startup that finishes in that window, but a real
  effect if `IdleTTL` is tuned low enough. Three things do change for an
  operator: (1) the five INFO lines above retire with no renamed equivalent — the sweep is no
  longer an app-level phase, so there is no phase to announce; (2) the
  `<prefix>.cleanupinterval is >= <prefix>.idlettl` advisory now fires at manager construction
  instead of at `prepareRuntime`, so it appears earlier in the startup log, with its message and
  its `resource`/`cleanupinterval`/`idlettl` fields byte-identical; and (3) `DbManagerOptions`
  and `messaging.ManagerOptions` gain an additive `CleanupInterval` field — adding a field to an
  exported struct is `apidiff`-compatible but not source-compatible for unkeyed composite
  literals, which stop compiling with `too few values in struct literal` until the field is
  added or the literal switches to keyed fields (the form to use); an unset field takes the
  same 5m/2m defaults `StartCleanup` has always applied.
- gate: always — every service that configures a database or messaging manager starts its sweep
  at a different moment, whether or not it sets `cleanupinterval`.
- apply: for the common case (you call `app.New`/`app.NewWithConfig` and let the framework build
  the managers) there is nothing to do — repoint any alert or saved query that matched one of the
  five retired lines at the manager's own lifecycle, or drop it. If you construct
  `database.NewDbManager` or `messaging.NewMessagingManager` yourself and call `StartCleanup`
  afterwards, that call is now redundant: it is a **no-op** while the constructor's loop is
  running, and it leaks no second goroutine (`StartCleanup` is idempotent). Delete it, or pass
  the interval through the new `CleanupInterval` option instead. The one shape that needs a real
  edit is a caller that used a second `StartCleanup` to *change* the interval on a live manager:
  that no longer takes effect, so call `StopCleanup()` first and then `StartCleanup(newInterval)`.
- verify: start the service and confirm the log carries no `Starting database manager cleanup
  loop` / `Starting messaging manager cleanup loop` line, and that idle eviction still happens —
  `curl -s localhost:8080/ready | jq '.messaging_stats.idle_cleanups'` climbs over a window
  longer than `messaging.publisher.idlettl` on a service with idle tenants. On a deliberately
  misconfigured `cleanupinterval >= idlettl`, the same advisory text appears, now among the
  earliest startup lines.
- ref: [ADR-067](adr_067_lifecycle_slots.md) · `database/manager.go` (`NewDbManager`,
  `StartCleanup`) · `messaging/manager.go` (`NewMessagingManager`, `StartCleanup`) ·
  `internal/resourcepool/cleanup_warning.go` (`WarnIfCleanupIntervalTooLate`) ·
  `app/lifecycle.go` (the retired `startMaintenanceLoops` / `shutdownManagers`)

### [C60.6] `messaging.StartConsumeSpan` is removed and the AMQP consumed counter moves to completion with `error.type` · breaking · when: match

- detect: two doors, and you may be behind either. Code:
  `git grep -n 'StartConsumeSpan' -- '*.go'` in your service — hits mean you drive your own
  AMQP consume loop rather than declaring consumers through `DeclareConsumer`. Telemetry:
  search dashboards, alerts and saved queries for `messaging.client.consumed.messages`; no
  repo-local grep finds those.
- scope: `messaging.StartConsumeSpan` is deleted (no deprecated stub, no replacement export).
  Separately, the classic lane's consumed counter is recorded when a delivery finishes rather
  than when it arrives, and carries `error.type` when the handler returned an error or panicked.
  The count per delivery is unchanged and the duration histogram is unchanged. Framework-declared
  consumers need no code change: `Registry.processMessage` does all of this for you.
- gate: match = at least one `StartConsumeSpan` call, or at least one query over
  `messaging.client.consumed.messages`. no-match = neither; nothing to do.
- apply: for the code door, start the span yourself — the framework no longer offers one:

  ```go
  ctx, span := otel.Tracer("your-service").Start(ctx, queue+" receive",
      trace.WithSpanKind(trace.SpanKindConsumer))
  defer span.End()
  ```

  A loop that also wants the consumed counter records
  `messaging.client.consumed.messages` itself at handler completion (with
  `error.type` on failure) — the framework's recorder is internal and this
  release exports no replacement.
  For the telemetry door, split by `error.type` where a query used to assume the counter had none,
  and expect a counter sample to land at handler completion — a query correlating counter and
  histogram timestamps now sees them together instead of a handler-duration apart.
- verify: `go build ./...` for the code door. For the telemetry door: consume one message that
  succeeds and one whose handler fails — each lands exactly one counter sample after the handler
  completes, and only the failing one carries `error.type`.
- ref: [ADR-068](adr_068_delivery_pipeline.md) · `messaging/internal/delivery/delivery.go`

### [C60.7] the AMQP lane's failure and panic lines stamp `amqp_correlation_id` instead of a second `correlation_id` · silent-behavior · when: always

- detect: nothing in your Go code changes, so the grep is for operations — search your log
  backend, dashboards, alerts and saved queries for `correlation_id` read off the AMQP
  consumer's two failure lines, `Message processing failed - discarding without requeue`
  and `Panic recovered in message handler - discarding without requeue`. If you also parse
  those lines in a contract test or a log shipper's config,
  `git grep -nE '(^|[^A-Za-z0-9_])correlation_id([^A-Za-z0-9_]|$)' -- '*.go' '*.yaml' '*.yml' '*.json'`
  finds those sites.
- scope: those two lines stamped `correlation_id` **twice** — the framework trace ID first,
  then the delivery's own AMQP `CorrelationId` — so a JSON parser keeping the last value read
  the AMQP field under that key. The second stamp is now `amqp_correlation_id`: both values
  are still emitted, but a parser sees a new key appear and the duplicate `correlation_id`
  key disappear. The level and the message texts are byte-identical; what changes under
  `correlation_id` is the value it holds on a failure line. Where
  the publisher is also GoBricks the two were usually the same string anyway — `messaging`
  defaults an unset AMQP `CorrelationId` to the trace id it stamps on the delivery's `X-Request-ID` (C60.10) — so the split bites where a foreign
  publisher set its own. Field **order** on all three outcome lines also changes: the two
  fields both lanes share, `correlation_id` and `processing_time`, now lead the line and the
  lane's own fields follow.
- gate: always — every service that declares AMQP consumers emits these lines.
- apply: a query or alert that reads `correlation_id` off those lines for trace correlation
  needs no edit and gets more than it had: the trace ID, always, instead of whichever value
  the publisher happened to set. Re-key to `amqp_correlation_id` only where the query was
  deliberately matching the publisher's own AMQP `CorrelationId`. Anything pinning the field
  *order* of an outcome line — a golden file, a positional parser — needs its expectation
  regenerated.
- verify: consume one message whose handler returns an error, and one whose handler panics;
  each line carries exactly one `correlation_id`, holding the trace-id the delivery's
  `X-Request-ID`/`traceparent` header carried, plus `amqp_correlation_id` when the publisher
  set one.
- ref: [ADR-068](adr_068_delivery_pipeline.md) · `messaging/registry.go` (`logOutcome`,
  `buildFailureLogEvent`) · `messaging/internal/delivery/delivery.go` (`AppendOutcome`)

### [C60.8] inbound trace identifiers failing validation are discarded, not stored · silent-behavior · when: always

- detect: your Go code cannot tell you this — the offending value comes from an upstream
  gateway or publisher, not from your source — so search your LOG BACKEND for
  `correlation_id` values that are empty, longer than 128 characters, or contain any
  character outside `A-Za-z0-9_-`. Any such value was being accepted and is now discarded.
  **That query finds the X-Request-ID half ONLY.** A malformed `traceparent` and an
  oversized `tracestate` are also discarded now, and neither reaches `correlation_id`, so
  neither shows up in that search: for those, check the gateway or sidecar that emits them
  and — where you can — a broker message capture, since a publisher sending a
  non-spec-exact traceparent silently loses its trace linkage rather than logging anything.
  If you also set the header yourself, `git grep -nE '(X-Request-ID|traceparent|tracestate)' -- '*.go'`
  finds the call sites; and if you plant ids through the exported API,
  `git grep -nE '(trace\.WithTraceID|trace\.EnsureTraceID)\(' -- '*.go'` finds those,
  which the seam cannot guard.
- scope: `trace.ExtractFromHeaders` stored every inbound identifier verbatim, testing only
  for non-emptiness. It now validates: `X-Request-ID` against `^[A-Za-z0-9_-]{1,128}$` (the
  bound the HTTP door has always applied, moved down so every door shares it), `traceparent`
  against the spec grammar with a non-zero trace-id and parent-id (version `00` exactly 55
  characters; versions `01`–`fe` may carry additional dash-delimited fields of printable
  non-space ASCII up to 255 bytes total, which are otherwise ignored; version `ff`
  rejected), and
  `tracestate` against a 512-byte cap AND against the carrier that brought its parent —
  tracestate is kept only when the same carrier supplied a valid `traceparent`, so an
  orphan one is discarded rather than attached to an inherited parent, and a carrier
  bringing a parent without a tracestate clears any inherited one. A value that fails is DISCARDED — never truncated,
  because truncation maps distinct upstream ids onto one and silently forges correlation —
  and the delivery continues with the traceparent-derived id, or a fresh UUID. The delivery
  itself is never rejected. `messaging` additionally refuses to copy an invalid id into an
  AMQP `CorrelationId`, since `trace.WithTraceID` and `trace.EnsureTraceID` are exported and
  bypass the seam. Validation runs AFTER AMQP field coercion, so a `[]byte`, an int or a
  nested table is rendered first and then judged on the rendering; an int still passes,
  because digits are in the charset.
- gate: always — every service consuming AMQP messages, running the outbox relay, or calling
  `trace.ExtractFromHeaders` directly now validates what it previously stored.
- apply: nothing to change in your code. Decide what to do about any upstream the detect
  finds: an emitter of long or punctuated request ids loses correlation, because the
  framework substitutes its own id rather than carrying theirs. Fix the emitter, or accept
  that those hops correlate by traceparent instead. If you relied on a `tracestate` larger
  than 512 bytes surviving a hop, it no longer does.
- verify: run all three against a real broker, with a consumer whose handler performs ONE
  downstream publish, so you exercise both the ingress bound and the re-emission it protects.
  Publish each message with headers set explicitly (the framework's own publisher injects a
  valid `traceparent`, so hand-set headers are the only way to feed the seam bad input):

  1. **Oversized `X-Request-ID`.** Header `X-Request-ID` = 300 characters (the bound is
     `^[A-Za-z0-9_-]{1,128}$`), no `traceparent`. Expect the consumer's `correlation_id` to be
     a fresh UUID, NOT the sent value, and the outbound publish to carry a short valid id in
     `X-Request-ID` rather than the 300-character string. Do not expect the logged UUID there
     verbatim: `InjectIntoHeaders` aligns the outbound id to the outbound `traceparent`, so it
     emits that traceparent's 32-hex trace-id. The assertion is that the oversized value is
     gone, not which valid id replaced it. Before this change the oversized value reached
     `amqp.Publishing.CorrelationId`, which amqp091 refuses over 255 bytes, tearing down the
     shared connection on the next publish.
  2. **Malformed `traceparent`.** Header `traceparent` =
     `00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01` (non-hex trace-id), plus a
     valid `X-Request-ID` such as `probe-2`. Expect `correlation_id` = `probe-2`, and the
     outbound `traceparent` to be a freshly generated one — the malformed value must appear in
     no outbound header.
  3. **Valid `traceparent`, oversized `tracestate`.** A well-formed `traceparent`, plus
     `tracestate` of 600 characters (the cap is 512). Expect the outbound `traceparent` to
     match the inbound one and the outbound `tracestate` to be ABSENT — not truncated.

  Across all three: no discarded inbound value appears in any outbound header, every delivery
  still carries some valid `correlation_id`, and the shared AMQP connection survives — check
  the broker's connection list, or simply that publishes after the probe still succeed. The
  substitute follows a precedence, so do not assert a fresh UUID where a valid `traceparent`
  is present: it correlates on the traceparent-derived 32-hex id instead.
- ref: [ADR-070](adr_070_inbound_trace_identifier_validation.md) · `trace/validate.go` ·
  `trace/trace.go` (`extractRequestID`, `extractTraceParent`, `extractTraceState`) ·
  `messaging/amqp_client.go` (`preparePublishing`) · `server/request_utils.go`

### [C60.9] the streams lane runs on the shared delivery pipeline · silent-behavior · when: match

- detect: `git grep -nE 'app\.StreamDeclarer|messaging/streams' -- '*.go'` — hits mean you
  consume native streams and this applies. Then grep dashboards, saved queries and
  log-based alerts for the streams consumer's two failure lines,
  `Stream message handling failed - offset not committed` and the panic line, and for
  any query over the consume span's attributes.
- scope: `consumerRunner.deliver` now builds a `delivery.Request` instead of doing its own
  span, timing, recover and telemetry, so the lane gains what it never had and its output
  changes shape in four ways. Its failure and panic lines now carry the shared spine —
  `correlation_id` and `processing_time`, plus `panic` and `stack` on a panic — on top of
  the `stream`/`consumer`/`offset` fields they already had. The panic line's wording moves
  from the AMQP-lane-style `panic in stream handler` to the pipeline's
  `panic in message handler` in the error it produces, and the line itself is now written
  once from the outcome tail rather than from inside the recover. The consume span gains the
  four attributes both lanes share (`messaging.system`, `messaging.operation.name`,
  `messaging.destination.name`, `messaging.message.body.size`) alongside the consumer name
  and offset it already set. A successful delivery still logs NOTHING.
  Two things the lane gains rather than changes: trace context is now extracted from the
  message's application properties, so a handler reads the originating id via
  `trace.IDFromContext(ctx)` and log lines correlate across the hop for the first time; and
  a per-message lease scope is installed, so a per-tenant handle a handler borrows is
  released when the message is done rather than immediately (ADR-032). Offset semantics are
  untouched: commit-or-skip through the same batching tracker, still committing only after
  success (ADR-059).
- gate: match = you consume native streams. no-match = nothing to do; the AMQP lane's own
  changes are C60.6 and C60.7.
- apply: repoint any log query that matched the old panic wording. Add `correlation_id` to
  streams log queries that previously had no cross-service id to join on — that is the
  capability this adds. If a dashboard pinned the consume span's exact attribute set, widen
  it by the four shared keys.
- verify: consume a message whose publisher set `traceparent`, from a handler you have made
  RETURN AN ERROR — a successful streams delivery logs nothing, so there is no line to read
  otherwise. Confirm the resulting failure line carries a `correlation_id` equal to the
  publisher's trace id; before this change the streams lane extracted nothing and the field
  was absent entirely.
- ref: [ADR-068](adr_068_delivery_pipeline.md) · [ADR-069](adr_069_pipeline_owns_settlement_timing.md) ·
  [ADR-070](adr_070_inbound_trace_identifier_validation.md) · `messaging/streams/runner.go` (`deliver`,
  `logOutcome`, `commitOffset`)

### [C60.10] a published `CorrelationId` is the message's own `X-Request-ID`, or empty · silent-behavior · when: always

- detect: your own code cannot tell you this — the value changes on the wire, not in your
  source. Grep consumer code for reads of the property,
  `git grep -nE '(^|[^A-Za-z0-9_])CorrelationId([^A-Za-z0-9_]|$)' -- '*.go'`, and grep
  dashboards and saved queries for `amqp_correlation_id` (C60.7), which is where a GoBricks
  consumer surfaces it. A consumer that correlates on the delivery's `X-Request-ID` header or
  on `correlation_id` needs no query: neither value changes.
- scope: `preparePublishing` derived the id twice, one call apart. `trace.InjectIntoHeaders`
  wrote `X-Request-ID` as the id ALIGNED with the traceparent it emits, and the next line
  wrote `CorrelationId` from a second `trace.EnsureTraceID(ctx)`, which does not align. The
  two therefore disagreed whenever the context's trace id was not already the traceparent's
  32-hex — which is every publish out of an HTTP handler, since the server middleware's id is
  a UUID. The property is now read back from the header the injection wrote, so the two are
  byte-equal by construction. The header, the `traceparent` and the `MessageId` are all
  unchanged. Two consequences: a `CorrelationId` that read as the originating request's UUID
  now reads as that request's traceparent trace-id, and a context trace id the validation
  seam refuses (ADR-070) no longer leaves the property EMPTY — the aligned id takes its place,
  and the cap still applies to it. The reverse case exists too and is new: a caller-supplied
  `traceparent` whose trace-id field is 32 characters but not hex aligns onto that value, which
  the guard then refuses, so `CorrelationId` is EMPTY where the HTTP path always populated it.
  Nothing loses correlation — the header, the `traceparent` and the framework's own
  `correlation_id` log field are unaffected — but a query over `amqp_correlation_id` can see
  blanks it never saw.
- gate: always — every AMQP publish the framework prepares, the outbox relay's included. The
  native streams publisher sets no `CorrelationId` and is untouched.
- apply: nothing in your code. Repoint any consumer-side join, dedup key or saved query that
  pinned the AMQP `CorrelationId` to the originating HTTP request's `X-Request-ID` value —
  join on the message's own `X-Request-ID` header, which is now both values, and do not
  assume the property is populated — the empty case in `scope` is reachable from any caller
  that can set the request's `traceparent`.
- verify: publish from an HTTP handler serving a request that carried a well-formed
  `traceparent`, and capture the message at the broker: `CorrelationId`, the `X-Request-ID`
  header and the `traceparent`'s trace-id are one string. Before this change the property held
  the request's UUID while the header held the trace-id. Then repeat with a `traceparent`
  whose trace-id field is 32 non-hex characters — `00-` plus 32 `!` — and expect the header and
  the `traceparent` to carry it while `CorrelationId` is EMPTY.
- ref: [ADR-070](adr_070_inbound_trace_identifier_validation.md) ·
  `messaging/amqp_client.go` (`preparePublishing`) · `trace/trace.go` (`InjectIntoHeaders`)

### [C60.11] `BuildUpsert` rejects colliding and unnameable column keys · breaking · when: match

- detect: `git grep -n 'BuildUpsert' -- '*.go'` lists every call site. Keep the pattern plain —
  `git grep -E` is POSIX ERE and silently ignores `\b`, `\s`, `\d` and `\w`. Nothing is
  compiler-caught; the failure is a returned error at run time. The grep finds the calls but not
  the collision, because `insertColumns` and `updateColumns` are usually assembled dynamically —
  a map built from struct tags and then merged with a caller's override map is the shape that
  lands `id` and `ID` in one call. Only Oracle can fold at all.
- scope: `database/internal/builder/` only, as two preconditions at `BuildUpsert` (`helpers.go`:
  `requireDistinctColumnIdentities`, `requireSingleColumnNames`). **First**, `insertColumns` and
  `updateColumns` must each name every column at most once, keyed by **the column each key
  actually names** on the vendor: Oracle folds the unquoted identifiers it emits and reads a
  quoted one verbatim, so `{"id": 1, "ID": 2}` and `{"id": 1, "\"ID\"": 2}` are each one column
  written twice and both are refused, while PostgreSQL quotes every identifier and sees two
  columns that still build. Comparing the RENDERINGS would miss the second pair — `id` renders
  unquoted and `"ID"` renders quoted, but Oracle folds the first onto the second. A caller-quoted
  key keeps its case, so `{"id": 1, "\"id\"": 2}` is two columns on Oracle and still builds, and
  so does `{"level": 1, "LEVEL": 2}`, which renders quoted on both sides. **Second**, every conflict, insert and update key must be a
  single column name. On Oracle that means no qualifier, no function call and no empty name. **On
  both vendors** it also means no quote that ends the identifier early — a quote inside a name
  must be doubled. That half was written when the two escapers shared a rendering defect; `[C60.25]`
  later in this same hop fixed both, so the rule now stands as builder VALIDATION of the key rather
  than as compensation for a renderer that could not escape it. Conflict and insert keys have no choice: the MERGE names them as column aliases in
  its USING clause and in the INSERT list. Update keys become UPDATE SET targets, where Oracle
  would accept an alias-qualified one; refusing those is the API's own restriction.
  That last rule refuses a key that, before `[C60.25]`, rendered as SQL rather than as a name:
  `role" = 'admin', "name` came out as `"role" = 'admin', "name"`, a second assignment rather than
  a column. `[C60.25]` fixes both renderers so that key now renders as one (absurd) column, and
  `[C60.24]` closes the `table` argument the same hop — but the key is still refused here, because
  rendering it correctly is not the same as it being a column name the caller can have meant. Read
  the two together: `[C60.11]` is what the upsert ACCEPTS, `[C60.25]` is what the renderer EMITS. Within
  it the two halves have different reach. The undoubled interior-quote rule applies to upsert keys
  on **both** vendors, because that clause is not Oracle grammar but a defect both escapers share.
  The name-shape rules — no qualifier, no function call, no empty name — are **Oracle-only**. So a
  dotted or function-shaped key is still *rendered* on PostgreSQL, which is not the same as being
  accepted: `{"t.name": 1}` renders `"t"."name"`, a qualified reference, and as an
  `ON CONFLICT ("t"."name")` target PostgreSQL's own grammar rejects it at execution. The builder
  does not refuse it because doing so is a second break on a second vendor, which this change has
  no evidence for — not because the statement works.
  **Third**, the two checks that already shipped — every conflict column must name an inserted
  column (`[C59.7]`), and no conflict column may also be updated (`[C59.9]`) — now key on that same
  named column instead of on the rendering, and both change outcome on Oracle. A conflict column
  spelled `"ID"` against an insert key spelled `id` was refused and now builds, because the value
  IS supplied. The same conflict column against an *update* key spelled `id` was accepted and now
  errors — that call was reaching Oracle and failing with ORA-38104, since the ON clause and the
  SET clause named one column under two spellings, so what moves is where it fails. On PostgreSQL
  both checks compare the keys themselves, exactly as before.
  The name check runs before the identity check, which is what keeps the identity rule's
  first-and-last-byte quote test correct — every key that reaches it renders as one whole token.
- gate: match = an Oracle deployment whose upsert column maps can carry two spellings of one
  column — including a conflict column spelled differently from the insert or update key naming
  the same column — or, still on Oracle, any key that is dotted, function-shaped or blank.
  Match as well for any call site that treats a builder error as control flow (a fallback, a
  retry, an alert), and any test pinning the old outcome for one of those shapes, in either
  direction: `[C59.7]` accepts a cross-spelling pairing it used to refuse, `[C59.9]` refuses one
  it used to let through.
  no-match = every key is already a distinct plain column name, which is the normal case and
  unchanged; a PostgreSQL-only deployment is exposed to the interior-quote rule alone — no
  identity rule here changes anything on PostgreSQL, where every spelling stays its own column. **Not covered by
  this hop:** the `table` argument, which nothing validates — it is tracked with the rest of the
  renderer in issue #1104.
- apply: nothing to change for a call whose keys are distinct single column names. For a folding
  pair, decide which spelling is the column and drop the other; the builder refuses rather than
  deduplicating on your behalf, because the key it discarded would take its **value** with it and
  the upsert would silently write the wrong one. For a dotted or function-shaped key, name the
  column by itself; a quote inside a quoted name must be doubled (`a""b`). **The one shape that
  did work:** an update key qualified with the MERGE's own
  `target` alias — `{"target.name": …}` — rendered `UPDATE SET target.name = :3`, which Oracle
  accepts; it is refused now, and naming the column alone (`{"name": …}`) builds the same
  statement. Every other rejected shape was already producing SQL Oracle refuses at parse.
  For the newly-refused overlap — an Oracle conflict column and an update key spelling one column
  differently — the remedy is `[C59.9]`'s: drop that column from `updateColumns` when its update
  value equals its insert value, and otherwise issue a separate `UPDATE` in the same transaction,
  keyed on the conflict columns and holding the row lock.
- verify: exercise your upsert paths and read the errors, since nothing surfaces at build time.
  Three Oracle probes and one PostgreSQL probe cover the hop. On Oracle: `BuildUpsert("t",
  []string{"k"}, map[string]any{"k": 0, "id": 1, "ID": 2}, nil)` now returns `insert columns must
  be distinct: "ID" and "id" name the same column for upsert` where it used to return a MERGE that
  Oracle answers with ORA-00957; a key like `"t.name"` or `"count(*)"` now returns `... is not a
  single column name for upsert` where it used to return a MERGE with an alias Oracle cannot
  parse; and `BuildUpsert("t", []string{"\"ID\""}, map[string]any{"\"ID\"": 1},
  map[string]any{"id": 3})` now returns `update column "id" collides with conflict column
  "\"ID\"" (…ORA-38104…)` where it used to return a MERGE that Oracle answers with that same
  ORA-38104 at execution. On PostgreSQL, where nothing else changes, the one probe is the quote
  rule: a key spelled `role" = 'admin', "name` now returns `... is not a single column name for
  upsert` where it used to render into the statement as a second assignment. All four produce no
  SQL.
- ref: [ADR-071](adr_071_upsert_column_sets_name_each_column_once.md) ·
  `database/internal/builder/helpers.go` · `database/types/interfaces.go` (the stated contract) ·
  #997 · `[C59.10]` (the scope limit this closes)

---

### [C60.12] `scheduler.timeout.*` is normalized in config, not defaulted at use time · breaking · when: match

- detect: `git grep -nE 'config\.Config\{|scheduler\.NewModule|NewModuleRegistry|\.Init\(' -- '*.go'`
  — hits mean you assemble config or module dependencies in code rather than letting `app`
  construction do it, and this applies. Also grep your YAML/env — and any other configuration
  source you load from, including ones a repo grep cannot see — for a negative
  `scheduler.timeout.shutdown` or `scheduler.timeout.slowjob`; the v0.59 godoc for `SlowJob`
  read "Zero or negative = disabled", so a `-1` written on that advice is a realistic hit.
  **This grep is a shortlist, not a verdict**: it cannot see a config returned by a helper or
  factory, an aliased import, or a `Module.Init` reached through an interface. A clean grep
  means "nothing obvious" — the `verify` step below is what actually clears you, because it
  tests the behaviour rather than the spelling.
- scope: the config normalize phase now fills `scheduler.timeout.shutdown` with 30s and
  `scheduler.timeout.slowjob` with 25s when the decoded value is zero, and REJECTS a
  negative value for either, naming the key. The scheduler module's own
  `defaultShutdownTimeout` (30s) and `defaultSlowJobThreshold` (**30s**) and their `> 0`
  guards are gone — it reads the normalized config. Three consequences. (1) A hand-built
  config passed to `app.NewWithConfig`/`Builder.WithConfig` ran a 30s slow-job threshold
  and now runs 25s, the value every YAML deployment already had (`config.Validate` runs on
  those paths per ADR-064). (2) `scheduler.Module.Init` now REQUIRES a normalized
  `deps.Config`: it returns `scheduler: deps.Config is required` for a nil one, and
  `scheduler: scheduler.timeout.<key> must be positive; run the config through
  config.Validate` — naming whichever of `shutdown`/`slowjob` is unset — for a config
  that never went through `config.Validate`. A `*config.Config` handed
  straight to `Module.Init` or `app.NewModuleRegistry` is never normalized, and with both
  keys zero the module would otherwise abandon in-flight jobs at shutdown and log every
  SUCCESSFUL job at WARN. (3) The slow-job WARN can no longer be switched
  off: zero takes the default and negative is rejected. YAML- and env-loaded deployments
  are unchanged — koanf already supplied 30s/25s.
- gate: match = you build `config.Config` in code, call `Module.Init`/`NewModuleRegistry`
  yourself, or set either key negative. no-match = nothing to do **only once the verify step
  passes** — the detect grep alone cannot clear you, since a config reaching `Init` through a
  helper matches none of its patterns.
- apply: nothing for YAML deployments. If you depended on the 30s hand-built slow-job
  threshold, set `Scheduler.Timeout.SlowJob` explicitly. Replace any negative value with a
  positive duration. If you drive `Module.Init` directly (a consumer test, a custom
  registry), hand it a config that went through `config.Validate` — or set both
  `Scheduler.Timeout` fields explicitly — and give it a non-nil `Config` at all.
- verify: run your config through `config.Validate` and print `cfg.Scheduler.Timeout` —
  zeros come back as 30s and 25s, and both fields must be POSITIVE before the config reaches
  a module (`Init` demands positive, not merely non-zero, so a negative value fails there
  too); a negative value in the config itself returns a validation error naming
  `scheduler.timeout.shutdown` or `scheduler.timeout.slowjob`. For a direct-`Init` harness
  assert both directions: an error when `deps.Config` is nil or either timeout is
  non-positive — the message names the unmet precondition — and nil only once you pass a
  non-nil config with both timeouts positive.
- ref: [ADR-075](adr_075_scheduler_timeout_single_default.md) ·
  [ADR-064](adr_064_app_validates_every_config.md) · `config/validation.go`
  (`normalizeScheduler`) · `scheduler/module.go` (`Init`, `Shutdown`,
  `determineJobSeverity`)

### [C60.15] a numeric config key delivered empty fails configuration resolution · breaking · when: match

> **Partly superseded on the E62 hop by `[C62.3]`** (ADR-095). `KEYSTORE_SECRETMINLENGTH=0` is
> still a well-formed zero for this atom's delivered-empty rule, but it stops disabling the
> keystore floor there and fails startup instead. The rest of this atom is unchanged.

- detect: grep every deployment surface — Helm values, Kustomize overlays, `.env` files,
  Task/Compose definitions, CI secrets — for a go-bricks variable **set to nothing**:
  `grep -rnE "^[[:space:]]*[A-Z0-9_]+=[[:space:]]*(\"[[:space:]]*\"|'[[:space:]]*')?[[:space:]]*(#.*)?$"`
  over env files — written to catch the QUOTED spellings too, since `FOO=""`, `FOO=''` and
  `FOO="   "` all deliver what this rule rejects: the guard TRIMS, so quoted whitespace is as
  empty as nothing at all — plus
  `grep -rnE ":[[:space:]]*(\"[[:space:]]*\"|'[[:space:]]*')[[:space:]]*(#.*)?$"` over Helm
  values, Kustomize overlays and Compose files, where the empty value is structured
  (`value: ""`, `value: "   "`, `FOO: ''`) and no `=` appears at all. Both tolerate a trailing
  comment, which is where a deliberately-blanked value tends to be documented
  (`value: "" # intentionally blank`). Neither matches a bare `key:` with nothing after it —
  that is YAML **null**, which is absence and still takes the default. Plus any
  `secretKeyRef`/`configMapKeyRef` whose source key holds an EMPTY value, and any `envsubst`
  template over a variable that can be unset. Note which secret shape actually bites: a
  MISSING key does not produce `FOO=` — with `optional: true` Kubernetes leaves the variable
  unset, and with `optional: false` the container will not start — so only a present key with
  an empty payload reaches this rule. A repo grep will not find those last two: read the
  rendered manifest, or `kubectl exec … env | grep -E '=[[:space:]]*$'` in a running pod
  (whitespace-aware, because a whitespace-only value is rejected too).
- scope: a set-but-empty string bound to a NUMERIC config key used to decode as `0`
  (koanf keeps the key; mapstructure's `WeaklyTypedInput` rewrote `""` to `0`). It now
  fails `config.Load` — and the public `Config.Unmarshal` — with an error naming the key
  and reporting it was `delivered empty`. Pointer targets are included, which is the
  damaging case: `KEYSTORE_SECRETMINLENGTH=` decoded as `*0` and DISABLED the secret-length
  floor rather than taking ADR-065's 32-byte default, because a non-nil pointer to zero
  reads as an operator choice. `SERVER_BODYLIMIT=` decoded as `0` and was rescued only by a
  downstream fallback. Unchanged: an unset variable, an omitted YAML key, a YAML **null**
  (different plumbing — still absence, still takes the default), an explicit value
  including an explicit `0`, and every non-numeric key — `DATABASE_HOST=` still produces
  ADR-051's database-identity error, not this one — with ONE exception: `database.port` is
  both an identity key and numeric, so `DATABASE_PORT=` now fails at decode with this
  message instead of ADR-051's, which means it no longer lists every other offending
  identity key alongside it. `time.Duration` keys are exempt: an empty string already
  failed loudly there. A whitespace-only value is rejected too (it already failed to parse;
  the message is now the same one). Through the public `Config.Unmarshal`, slice and map
  ELEMENTS are judged as well — `["1","","3"]` into a `[]int` used to decode `[1 0 3]` and
  now fails naming the index. Two other seams carry the rule: the `go-bricks-migrate` CLI
  applies it to `tenants.yaml`, and a dynamic `DBConfigProvider` payload read through
  `migration.SecretsProvider` applies it to the secret's own numeric fields, so a rotated
  secret rendering `{"port": ""}` fails instead of dialing port 0.
- gate: match = an empty value can reach a numeric key through ANY of the four seams — an
  environment variable or YAML key in the service's own configuration, the public
  `Config.Unmarshal`, the CLI's `tenants.yaml`, or a stored `DBConfigProvider` payload. Read
  that wider than it sounds: for most numeric keys `0` means "use the default", so an empty
  value was BENIGN before this change (`CACHE_REDIS_PORT=` resolved to 6379,
  `OUTBOX_BATCHSIZE=` to its default) and those deployments were healthy. They now fail —
  at startup for the first two seams, at first use for the last two. no-match = every one of
  those sources is either absent or carries a real value; a clean environment ALONE is not
  no-match, since the CLI and dynamic seams resolve payloads no env sweep sees.
- apply: unset the variable, or give it an explicit value. For a `secretKeyRef`, fix the
  stored VALUE rather than the reference: an absent key leaves the variable unset (or blocks
  the pod), so what reaches this rule is a key whose payload is empty. For a dynamic
  `DBConfigProvider`, check the stored secret payload itself: a rotation that writes `""`
  for a numeric field now fails that tenant's config resolution. In a multi-tenant
  deployment sweep EVERY tenant record (`multitenant.tenants.<key>.*`, `databases.<key>.*`),
  not just top-level keys: one bad record now fails `config.Load` for the whole process,
  where before it produced a zero for that tenant alone. `FOO=0` is
  still the way to say zero where zero is legal (`KEYSTORE_SECRETMINLENGTH=0` keeps
  disabling the floor, deprecated but unchanged).
- verify: boot the service in staging with its real manifest. A startup failure naming a
  key with `delivered empty` is this rule; fix the source and re-boot. Two naming
  caveats: the message carries the koanf key (`keystore.secretminlength`), not the
  environment variable, and on the `Config.Unmarshal` seam it carries the Go field path
  of the target struct instead (`Trace.Batch.Size`), so grep your manifest for the key's
  env spelling rather than the string in the error. `observability.*` decodes through
  that seam, and a delivered-empty value there now ABORTS startup where it previously
  fell back to a no-op provider — telemetry silently off — so that section is worth
  checking first. To confirm the
  posture before deploying, run `kubectl exec <pod> -- env | grep -E '=[[:space:]]*$'`
  against the current release — every line it prints is a candidate, whitespace-only values
  included, since those are rejected too.

  Booting the service exercises only the two seams that resolve at startup — `config.Load`
  and the `Config.Unmarshal` calls the framework makes for you. Check the other two: run `go-bricks-migrate info` per tenant to put the CLI's
  `tenants.yaml` through the same guard, and for a dynamic `DBConfigProvider`, resolve every
  stored secret payload (acquire a connection per tenant, or call your provider's resolve
  path directly) — those decode at first use rather than at boot, so a green startup says
  nothing about them.
- ref: [ADR-074](adr_074_delivered_empty_numeric_config.md) ·
  [ADR-051](adr_051_delivered_empty_database_identity.md) ·
  [ADR-065](adr_065_keystore_secretminlength_tristate.md) ·
  `internal/configdecode/configdecode.go` (`EmptyStringToScalarGuardHookFunc`, named
  `EmptyStringToNumericGuardHookFunc` when this atom was written — [C60.18] renamed it
  when the same rule reached bool) ·
  `config/config.go` (`buildDecoderConfig`, `unmarshalDecoderConfig`)

### [C60.18] a bool config key delivered empty fails configuration resolution · breaking · when: match

- detect: the same sweep as [C60.15], run against the **bool** keys — it is one grep, not
  two, so if you already ran C60.15's you have the candidate list; what changes is which
  hits matter. Over env files:
  `grep -rnE "^[[:space:]]*[A-Z0-9_]+=[[:space:]]*(\"[[:space:]]*\"|'[[:space:]]*')?[[:space:]]*(#.*)?$"`,
  and over Helm values, Kustomize overlays and Compose files, where the empty value is
  structured: `grep -rnE ":[[:space:]]*(\"[[:space:]]*\"|'[[:space:]]*')[[:space:]]*(#.*)?$"`.
  Both TRIM, so `FOO="   "` is as empty as `FOO=`. A bare `key:` with nothing after it is
  YAML **null**, which is absence and still takes the default. Then read the hits for a
  boolean key — `*_ENABLED`, `*_CRITICAL`, `*_LOGROUTES`, `*_REQUIRE`, `*_PROXIES`,
  `*_AUTOCREATETABLE`, `*_PRETTY`, `*_DEBUG`, and the four `DEBUG_ENDPOINTS_*` — plus the
  same `secretKeyRef`/`configMapKeyRef` and `envsubst` sources C60.15 names, which no repo
  grep reaches. THREE of those keys are the ones that actually changed behaviour rather
  than merely restating the default `false`: `DATABASE_POOL_KEEPALIVE_ENABLED`, `CACHE_CRITICAL`,
  `SERVER_LOGROUTES` — grep those three by name first
  (`grep -rnE "(DATABASE_POOL_KEEPALIVE_ENABLED|CACHE_CRITICAL|SERVER_LOGROUTES)"`), and in
  YAML their key paths `database.pool.keepalive.enabled`, `cache.critical`,
  `server.logroutes`.
- scope: a set-but-empty string bound to a BOOL config key used to decode as `false`
  (koanf keeps the key; mapstructure's `WeaklyTypedInput` answers an empty string with
  `SetBool(false)` by an explicit branch, not a parse failure). It now fails `config.Load`
  — and the public `Config.Unmarshal` — with an error naming the key and reporting
  `boolean value delivered empty`. The damage this ends is the pointer tri-states, where a
  non-nil `*false` reads as an operator choice: `DATABASE_POOL_KEEPALIVE_ENABLED=` was a
  genuine **default-true → false flip** that turned TCP keep-alive off, and `CACHE_CRITICAL=`
  disabled ADR-046's strict readiness so `/ready` answered 200 through a Redis outage.
  `SERVER_LOGROUTES=` is the third pointer key. The non-pointer bools (`cache.enabled`,
  `server.ratelimit.enabled`, `debug.enabled`, …) are guarded on the same rule but were
  landing on `false`, which is also their default — for those the change is a loud failure
  where the behaviour was benign. Unchanged: an unset variable, an omitted YAML key, a YAML
  **null** (different plumbing — still absence, still the default), and every explicit
  spelling `true`/`false`/`1`/`0`, including the deliberate `CACHE_CRITICAL=false` opt-out.
  A whitespace-only value is rejected too. Through the public `Config.Unmarshal`, slice and
  map ELEMENTS are judged as well. The same four seams as [C60.15] carry it, because all
  four compose one hook: `config.Load`, `Config.Unmarshal`, the `go-bricks-migrate` CLI's
  `tenants.yaml`, and a dynamic `DBConfigProvider` payload read through
  `migration.SecretsProvider` — where a rotation rendering
  `{"pool":{"keepalive":{"enabled":""}}}` now fails instead of turning keep-alive off for
  that tenant.
- gate: match = an empty value can reach a bool key through ANY of the four seams. Read it
  the way [C60.15]'s gate asks you to: for the non-pointer bools an empty value was BENIGN
  before this change, and those deployments were healthy — they now fail, at startup for
  the first two seams and at first use for the last two. no-match = every one of those
  sources is either absent or carries a real value; a clean environment ALONE is not
  no-match, since the CLI and dynamic seams resolve payloads no env sweep sees.
- apply: unset the variable, or give it an explicit `true`/`false`. Decide which you meant
  before you type it — for `database.pool.keepalive.enabled` the two answers are not
  equivalent, since unsetting restores the default **true** while `false` keeps the posture
  the empty value produced. For `cache.critical`, unsetting restores STRICT readiness and
  `false` keeps the outage-tolerant one. For a `secretKeyRef`, fix the stored VALUE rather
  than the reference. In a multi-tenant deployment sweep every tenant record
  (`multitenant.tenants.<key>.*`, `databases.<key>.*`), not just top-level keys: one bad
  record now fails `config.Load` for the whole process.
- verify: boot the service in staging with its real manifest. A startup failure naming a
  key with `boolean value delivered empty` is this rule (the numeric half of the same guard
  says `numeric value delivered empty` — that one is [C60.15]). Same two naming caveats:
  the message carries the koanf key (`cache.critical`), not the environment variable, and
  on the `Config.Unmarshal` seam it carries the Go field path instead. Then confirm the two
  keys whose DEFAULT you may have been overriding without knowing: after the fix,
  `database.pool.keepalive.enabled` should read `true` unless you set it false on purpose,
  and — on a deployment that actually configures a cache — `/ready` should answer 503 while
  that backend is down, unless `cache.critical` is explicitly `false`. A cache-free
  deployment has no such probe, so it has nothing to confirm here. Booting exercises only the two startup seams — run
  `go-bricks-migrate info` per tenant for the CLI's `tenants.yaml`, and resolve every stored
  `DBConfigProvider` payload for the fourth, since those decode at first use.
- ref: [ADR-077](adr_077_delivered_empty_bool_config.md) ·
  [ADR-074](adr_074_delivered_empty_numeric_config.md) ·
  [ADR-046](adr_046_cache_readiness_strict_default.md) ·
  `internal/configdecode/configdecode.go` (`EmptyStringToScalarGuardHookFunc`) ·
  `config/config.go` (`buildDecoderConfig`, `unmarshalDecoderConfig`)

### [C60.16] a database section's `ConfigError.Field` names that section · breaking · when: match

- detect: `git grep -nE 'ConfigError|errors\.As' -- '*.go'` in your own code, then read every
  hit that compares `.Field` to a literal starting `database.` — those are the matchers this
  changes. Log-side: grep dashboards and saved queries for the message prefix
  `databases.<name>: ` or `multitenant.tenants.<id>.database: `.
- scope: normalization errors from a NON-ROOT database section used to carry the section path
  in the wrapping message while the `*ConfigError` behind `errors.As` kept the root spelling —
  message `databases.reporting: config_missing: database.database required`, but
  `Field == "database.database"`. The path now lives in `Field` and only there:
  `databases.reporting.database`, `databases.reporting.host`,
  `multitenant.tenants.acme.database.host`. That spelling is byte-identical to the keys ADR-051's
  delivered-empty check already emits for the same section, so the package now has one
  spelling per key. A field that is not key-shaped is PREFIXED rather than
  rewritten, keeping the name: `databases.reporting.oracle connection identifier`. Unchanged:
  the root section (`database.host`, same `Field`, same text), the connect door — a
  `DBConfigProvider` resolving a tenant at RUNTIME still reports the root spelling
  (`database.tls`), with the tenant key only in the wrapping message — and `Action`, which
  still names the ROOT env var (`set DATABASE_DATABASE env var …`) even for a named section,
  whose real variable is `DATABASES_REPORTING_DATABASE`.
- gate: match = your code reads `ConfigError.Field`, or a log query pins the old message
  prefix. no-match = you only read the rendered error as text and grep for the key, which is
  still in it.
- apply: replace `field == "database.host"` with a predicate scoped to the DATABASE field
  families — a bare suffix match is wrong, because `ConfigError.Field` is not a
  database-only namespace and `cache.redis.host` ends in `.host` too:

  ```go
  func isDatabaseField(field, key string) bool {
      return field == "database."+key || // root
          (strings.HasPrefix(field, "databases.") && strings.HasSuffix(field, "."+key)) ||
          (strings.HasPrefix(field, "multitenant.tenants.") &&
              strings.HasSuffix(field, ".database."+key))
  }
  ```

  Then `errors.As(err, &cfgErr) && isDatabaseField(cfgErr.Field, "host")` matches root and
  section spellings and nothing else; the two prefixes also tell you WHICH family it was. Repoint log queries from the `databases.<name>: `
  prefix to the qualified key. If you route per-tenant failures on that prefix, know it
  matches STATIC tenants only: a dynamic `DBConfigProvider` tenant still reports the root
  spelling, so keep whatever handling you have for that path.
- verify: give a `databases.<name>` entry a real host and no database name, boot, and read the
  startup error: it names `databases.<name>.database` once. Before this change the same boot
  printed the path in the prefix and `database.database` in the field.
- ref: [ADR-076](adr_076_section_qualified_config_error_field.md) ·
  [ADR-051](adr_051_delivered_empty_database_identity.md) ·
  `config/database_section.go` (`dbSection.qualify`, `qualifyField`)

---

### [C60.19] a tenant-tree error names its section, and its hint names a reachable variable · breaking · when: match

- detect: `git grep -nE 'ConfigError|errors\.As' -- '*.go'` in your own code, then read every
  hit that reads `.Field` or `.Action`. Three families change. For `Field`: a comparison to a
  literal `database.*` on the RUNTIME path (a `DBConfigProvider` tenant resolved through
  `database.DbManager`, or the `go-bricks-migrate` CLI), which [C60.16] already changed for
  the startup path only; a comparison to the prose spelling `NewMultiTenantError` emitted,
  `git grep -nE "tenant '" -- '*.go'`; and the literal `multitenant.tenants messaging`
  (a space, not a dot). For `Action`: anything parsing the env-var name out of the hint,
  `git grep -nE '\.Action' -- '*.go'`. Log-side: grep dashboards and saved queries for the
  ONE retired wrapping prefix, `failed to apply pool defaults for key`, and for
  `tenant '<id>' ` inside a rendered config error. The CLI's `tenant "<id>":` prefix is NOT
  retired — `go-bricks-migrate` keeps it until the pin bump — so leave queries matching it
  alone for now.
- scope: [C60.16] addressed a database section's errors to that section at the STARTUP doors
  and said plainly that the runtime door stayed root-spelled. It no longer does.
  A NEW exported function, `config.ApplyDatabasePoolDefaultsForKey`, takes the
  `DBConfigProvider` resource key the config was resolved for (`""` root,
  `config.NamedDatabasePrefix + name`, otherwise a tenant id) and addresses its errors from
  it, so a dynamically-resolved tenant reports `multitenant.tenants.acme.database.tls`
  exactly as a statically-declared one does. `ApplyDatabasePoolDefaults` is UNCHANGED — same
  signature, still root-addressed — so nothing you call stops compiling; it now delegates
  with an empty key. It is a second function rather than a second parameter because
  `tools/migration` is a separate module pinned to a RELEASED go-bricks, where an arity
  change cannot compile until the next tag. `database.DbManager` moves to the new door and
  correspondingly STOPS wrapping: `failed to apply pool defaults for key acme:` is gone from
  the rendered text, because the key now lives in `Field`. The CLI's TLS-validating provider
  still uses the root door and still wraps `tenant "acme":`; it adopts the new one with its
  pin bump. `Action` is re-pointed with `Field`:
  a `databases.reporting` failure now reads `set DATABASES_REPORTING_PORT env var …` where
  it read `set DATABASE_PORT env var …`, which was not merely cosmetic — following the old
  hint on a multitenant config writes a partial root `database` block, which ADR-047 rejects
  as an incomplete section, so the hint manufactured a second failure. The env half is
  emitted ONLY when the variable round-trips back to the same key; a section or tenant whose
  name carries `_` (or an uppercase letter) gets the YAML half alone, because
  `DATABASES_REPORT_DB_PORT` reaches `databases.report.db.port`, not
  `databases.report_db.port` (that those names are unreachable at all is #1124, fixed on the
  E61 hop by `[C61.22]`, which rejects such a name at startup — on THIS hop they still boot
  and still get the YAML half alone). Three sibling spellings in the tenant tree join the same rule: a per-tenant CACHE
  failure moves from `cache.redis.host` plus a `tenant <id> cache:` wrapper to
  `multitenant.tenants.<id>.cache.redis.host`; the messaging-consistency error's field
  `multitenant.tenants messaging` becomes `multitenant.tenants.*.messaging` — a WILDCARD
  segment, because that error is a whole-map invariant and a literal `.messaging` there
  would be indistinguishable from a tenant actually named `messaging`; and
  `config.NewMultiTenantError`'s field stops being the prose `tenant 'acme' database` and
  becomes `multitenant.tenants.acme.database`. Unchanged: every ROOT-section error, in
  `Field`, `Action` and rendered text alike. The per-key CACHE factory was deliberately left
  out here — a runtime door still reporting `cache.redis.host` with a root `CACHE_REDIS_HOST`
  hint for a dynamically-resolved tenant — and that asymmetry is CLOSED on the next hop by
  `[C61.24]` (#1125): if you route on the cache family rather than the database one, read that
  atom and take the end state from it.
- gate: match = your code reads `ConfigError.Field` / `.Action`, OR a dashboard, alert or
  log-parsing test pins one of the retired strings. Calling `config.ApplyDatabasePoolDefaults`
  is NOT a match on its own — it still compiles and still behaves as it did; you need the new
  door only if you want your own resolved section named. Note which door the `go-bricks-migrate`
  CLI is on: it still calls the ROOT one and still wraps `tenant "<id>":`, because that module
  pins a released go-bricks and cannot see `ApplyDatabasePoolDefaultsForKey` until the next
  tag. The CLI's own errors therefore stay root-spelled for one release; the pin bump adopts
  the new door. no-match = you never
  touch the typed error and never parse those messages; the new spellings are then a strictly
  better read for a human and need no action.
- apply: if you resolve per-section configs yourself, switch to
  `ApplyDatabasePoolDefaultsForKey` and pass the key you already hold —
  `config.NamedDatabasePrefix + name` for a named database, the tenant id for a tenant;
  `ApplyDatabasePoolDefaults` stays correct for a single root database. Then replace `Field`
  matchers with a predicate scoped to the key families,
  exactly as [C60.16] teaches — `database.<key>` for the root, and a `databases.` /
  `multitenant.tenants.` prefix before accepting a suffix, since `cache.redis.host` ends in
  `.host` too. That predicate now works for BOTH doors, which is the point of this atom: a
  consumer can stop caring whether a tenant was declared statically or resolved dynamically.
  Drop any code that re-derived the tenant from a wrapping prefix. If you parse the env-var
  name out of `Action`, handle a hint that names no variable — that is now a legitimate
  shape, not a bug.
- verify: resolve one dynamic tenant with a deliberately invalid database section (a
  negative `pool.idle.time` is enough) and read the typed error: `Field` must be
  `multitenant.tenants.<id>.database.pool.idle.time`, and the message must NOT contain
  `failed to apply pool defaults for key`. Then take a real failure's `Action` at its word,
  branching on which half it offers: when it names an env var, set THAT variable and re-boot;
  when it names none — a section or tenant whose name carries `_` or an uppercase letter, where
  no variable reaches the key — add the YAML key it names instead. Either way the failure must
  RESOLVE rather than move, which is the property the old root-spelled hint failed: it named a
  variable that configured a different section. A hint offering no env var is a legitimate
  shape, so "there was nothing to set" is not a failed verify.
- ref: [ADR-076](adr_076_section_qualified_config_error_field.md) (addendum) ·
  [ADR-047](adr_047_database_absence_vs_misconfiguration.md) ·
  `config/database_section.go` (`sectionForResourceKey`, `normalizeDatabaseValues`,
  `dbSection.qualify`) · `config/validation.go` (`ApplyDatabasePoolDefaultsForKey`) ·
  `config/errors.go` (`tenantField`) · `database/manager.go` ·
  `tools/migration/internal/commands/dbtls.go`

### [C60.20] a delivered-empty `debug.allowedips` fails configuration resolution · breaking · when: match

- detect: grep every deployment surface for `DEBUG_ALLOWEDIPS` **set to nothing** —
  `grep -rnE "^[[:space:]]*DEBUG_ALLOWEDIPS=[[:space:]]*(\"[[:space:]]*\"|'[[:space:]]*')?[[:space:]]*(#.*)?$"`
  over env files, and `grep -rnE "DEBUG_ALLOWEDIPS" -A1` over Helm values, Kustomize
  overlays and Compose files where the empty value is structured (`value: ""`). In YAML,
  `git grep -nE "allowedips:[[:space:]]*(\"\"|''|~|null)?[[:space:]]*$"` finds the
  empty-STRING and the NULL spellings (a bare `allowedips:` is what an unset template value
  renders); `allowedips: []` is the empty-LIST spelling and is NOT affected. A
  SEPARATOR-ONLY value is caught too and no grep for emptiness will show it — check any
  variable built by joining a list (`{{ join "," … }}`, `"${A},${B}"`) whose elements can all
  be unset, since `,` decodes to zero entries. Also check any
  `secretKeyRef`/`configMapKeyRef` whose stored value is empty and any `envsubst` template
  over a variable that can be unset — a repo grep reaches neither; read the rendered manifest
  or `kubectl exec <pod> -- env | grep -E '^DEBUG_ALLOWEDIPS=[[:space:]]*$'`.
- scope: `debug.allowedips` is the only list key whose default is a CONTROL
  (`["127.0.0.1", "::1"]`), so an empty value there removes protection rather than relaxing
  it. It used to boot: the env layer overwrote the default with an empty list, ADR-049's
  registration gate is a CONJUNCTION and was satisfied by `debug.bearertoken` alone, and
  registration then skipped `ipWhitelistMiddleware` entirely — a deployment that asked for
  allowlist AND token ran with token only, silently. It now fails `config.Load` naming the
  key. The discriminator is the SHAPE the source delivered, not the decoded value: an empty
  **string** (`DEBUG_ALLOWEDIPS=`, `allowedips: ""`) is rejected; an empty **sequence**
  (`allowedips: []`) is the deliberate, still-sanctioned token-only clear and is unchanged.
  Unset keeps the loopback default; explicit lists are unchanged. The no-token case still
  aborts, now at `config.Load` rather than at registration — same refusal, earlier seam, and
  it names the key. The other five `[]string` keys are byte-unchanged: clearing
  `scheduler.security.cidrallowlist` still fails closed to localhost, the two trusted-proxy
  lists and `log.sensitivefields` still treat empty as the stricter posture, and
  `multitenant.resolver.order` still raises ITS own ADR-039 error rather than this one.
- gate: match = `debug.allowedips` can receive an empty value from ANY source — env file,
  rendered manifest, `secretKeyRef`, `envsubst`, or a YAML `allowedips: ""`. no-match = the
  key is unset, carries real entries, or is written `[]`. Note which side of the line you are
  on before assuming safety: a deployment relying on `DEBUG_ALLOWEDIPS=` to mean "token only"
  is a MATCH and will now fail to start. The check does NOT consult `debug.enabled`, so a
  deployment that templates this variable fleet-wide is a match even on services that expose
  no debug endpoint — the value has no security effect there, but it still aborts startup, so
  fix the template rather than relying on the endpoint being off.
- apply: decide what the empty value was meant to say. If it meant "no IP restriction, the
  token is the control", write `debug.allowedips: []` in config.yaml — that is the spelling
  ADR-049 sanctions and the one no rendering accident produces. If it meant "the loopback
  default", remove the key or the variable entirely. If the empty value was an accident — an
  unset Helm value, an `envsubst` over an undefined variable, an empty `secretKeyRef` payload
  — put the intended CIDRs back; that is the case this atom exists to surface, and your
  deployment has been running without the IP dimension.
- verify: boot the service in staging with its real manifest. A startup failure naming
  `debug.allowedips` with `delivered empty` is this rule; the error names both remedies. Then
  confirm the posture you actually want: with entries configured, a request to
  `<debug.pathprefix>` from a non-allowlisted peer must be refused even WITH a correct bearer
  token — that is the dimension that was missing. If you deliberately chose `[]`, the same
  request must succeed with the token and fail without it (ADR-049's token-only posture,
  unchanged).
- ref: [ADR-078](adr_078_delivered_empty_allowedips.md) ·
  [ADR-049](adr_049_debug_endpoints_fail_closed.md) (addendum: two premises amended) ·
  [ADR-051](adr_051_delivered_empty_database_identity.md) ·
  `config/validation.go` (`validateNoDeliveredEmptyList`, `deliveredEmptyRejectingKeys`) ·
  `config/phases.go` · `app/debug_handlers.go`

### [C60.21] the log filter walks JSON arrays without panicking, normalizes its needle list, and reports recovered panic values by type · breaking · when: match

- detect: four independent checks, run all four — (4) is a confidentiality check and is
  the one most easily skipped.
  (1) **The panic.** You do not need to grep for it — it fires on any logged document
  shaped `{"…":[{…}]}`, which is what `encoding/json` produces for every JSON list of
  objects. Search your log backend and crash reports for
  `comparing uncomparable type map[string]interface {}` and for
  `comparing uncomparable type []interface {}`. Absence proves nothing: the crash needs a
  slice-of-objects to actually reach a log call, so a service that has never logged a
  response body has never been hit and will be the moment it does.
  (2) **The emitted type.** `git grep -nE "FilterValue\(" -- '*.go'`, then follow each
  result to every place its CONCRETE TYPE is consumed — not just to a `.(T)` assertion.
  Four forms, all affected: a type assertion (`.([]string)`, `.([]MyStruct)`); a **type
  switch** (`switch v := x.(type) { case []string: … }`), whose arm silently stops being
  selected and falls to `default` rather than failing; **reflection**
  (`reflect.TypeOf(x)`, `reflect.ValueOf(x).Kind()`, and anything keyed on the type name);
  and any branch keyed on the concrete type by other means — a map of `reflect.Type`, a
  `fmt.Sprintf("%T")` compared against a literal, a generic constraint. The type switch is
  the one to look for hardest: an assertion panics or returns `ok=false` where a switch
  just quietly takes a different arm.
  (3) **The needle list.** `git grep -nE "LoggerFilterConfig|NewSensitiveDataFilter|NewWithFilter"`
  over your own code, then read each `SensitiveFields` literal for an entry that is empty,
  whitespace-only, or a duplicate — including any list built at runtime from a secret
  manager, a database or a `strings.Split`, where an empty element is the normal accident.
  Go only — do NOT sweep your YAML. `log.sensitivefields` was already normalized before this
  hop and behaves identically after it, so a YAML grep here produces nothing but false hits
  and the wasted audit teaches operators to distrust the rest of this atom.
  (4) **Pre-encoded payloads — the one shape where this hop trades a crash for a
  leak.** A `json.RawMessage` (or any named `[]byte`) is opaque to the filter, so a
  secret spelled INSIDE it is not masked. `[]json.RawMessage` used to PANIC the log
  path; it now renders in clear. Two steps, because a value is usually logged by an
  identifier rather than inline. First list the candidates:
  `git grep -nE "json\.RawMessage|^[[:space:]]*(type[[:space:]]+)?[A-Za-z_][A-Za-z0-9_]*[[:space:]]+(=[[:space:]]*)?\[\]byte" -- '*.go'`.
  The leading `[[:space:]]*` catches a member of a grouped `type ( … )` block and the
  `(=[[:space:]]*)?` catches a `type X = []byte` alias; both escape a `^type … []byte`
  anchor. It also matches `[]byte` STRUCT FIELDS, which is deliberate — a byte field can hold
  the same pre-encoded payload.
  Then list the log-field calls:
  `git grep -nE "(^|[^A-Za-z0-9_])(Interface|WithFields)\(" -- '*.go'`.
  The leading alternation is load-bearing: a chained log call puts `Interface(` at the START
  of a continuation line with no dot before it, so a `\.` pattern silently misses it. Two of
  the files this hop changes are exactly that shape.
  Cross-check the two by identifier: a value of one of those types reaching one of
  those calls, whose bytes can contain a secret, now logs that secret in clear where
  it previously crashed. `Str`/`Int`/`Bytes` fields are not affected — only the two
  calls that walk an arbitrary value. A TOP-LEVEL `json.RawMessage` is NOT part of
  this hop: it leaked before and leaks identically after.
- scope: five consumer-visible changes. The first two are in the sensitive-data filter; the
  last three are NOT — the panic summary is `scheduler/module.go`, the WARN is
  `logger/logger.go`, and the sink recovery is `migration/audit_emitter.go`. The scheduler
  one is the change most likely to be missed, because nothing about it looks like a
  log-filter concern.
  **The walk.** `filterSliceOrArrayWithProtection` compared each filtered element with the
  original to decide whether to preserve the slice's concrete type. Both sides are `any`, so
  the comparison panicked on any element whose dynamic type is uncomparable — a
  `map[string]interface{}` or a `[]interface{}`, i.e. every decoded JSON object or array
  inside a list. The struct branch that would have avoided it sees `interface{}` for a
  `[]any` element and never fired. The decision now reads the ELEMENT TYPE before descending:
  a slice the walker cannot rewrite is returned untouched, anything else is rebuilt as
  `[]any`. Both doors are covered — `.Interface()` on a log event, and
  `Logger.WithFields`. Slices of scalars and `[]byte` (still base64, not an array of
  numbers) keep their concrete type. A non-nil slice of typed STRUCTS does NOT — struct
  elements are rewritten, so `[]MyStruct` comes back as `[]any`. Its serialized output is
  identical, which is why it is easy to miss: the change is visible only to code reading the
  concrete type. Serialized output is identical either way,
  including a typed NIL slice, which stays `null` rather than becoming `[]`.
  **The needle list.** Trim, drop-empty and de-duplicate moved from
  `app.resolveLoggerFilterConfig` into `logger.NewSensitiveDataFilter`, so they now apply to
  EVERY construction door. `app.Options.LoggerFilterConfig` replaces the whole config and
  bypassed the old normalizer: a single empty needle there made
  `strings.Contains(field, "")` true for every field and replaced the entire log stream with
  the mask value — framework identity fields (`app`, `env`, `version`) included. That list is
  now normalized, so the same config masks only what it names.
  **The scheduler's panic summary.** A recovered job panic used to render its VALUE
  into the summary line's `error` field and into the span's recorded error, neither
  of which the filter touches — so a job panicking with a secret emitted it in clear
  one line after the filtered report masked it. Both now name the panic's TYPE:
  `"error":"panic (type: mypkg.Payload)"` where it read `"error":"panic: {…}"`, and
  the span's recorded error changes with it. **Repoint any alert or saved query that
  matches the old rendering before upgrading**: this breaks SILENTLY — the query stops
  matching and the alert simply never fires again, which reads as "no job panics"
  rather than as a failure. That is worse than a config break, which at least stops
  the service.

  **The audit sink's panic recovery.** `deliverToSink` recovered a panicking
  `AuditRecorder` and then reported it with a log call — inside a defer that had already
  spent its `recover()`. Rendering a slice-bearing panic value hit the walk defect above,
  so the second panic escaped into `consumeSink`'s bare goroutine and killed the process,
  defeating the guarantee #686 shipped. The reporting call is now guarded, and reports the
  panic's TYPE rather than its value — see [C60.23], which corrected this: relying on the
  sensitive-data filter here was not protection, because that filter masks by FIELD name and
  the field is `panic`. The line names `audit_type` and `target`, so a dropped event stays
  attributable.

  **The masking-disabled WARN.** It judged the RAW `SensitiveFields` length, so a list whose
  entries all normalize away (`[""]`, `["  "]`) would have masked nothing while staying
  silent. It now judges the EFFECTIVE needle list.
- gate: match on ANY of — (a) you log decoded JSON, request/response bodies, broker payloads
  or JWKS documents through `Interface()`/`WithFields()`, including via `httpclient`'s
  `LogPayloads`; (b) you call the public `FilterValue` and anything downstream depends on the
  CONCRETE TYPE of its result — a type assertion, a type switch, reflection, or a branch
  keyed on the type by any other route (see detect (2)); (c) you build a needle list IN GO via
  `app.Options.LoggerFilterConfig` or `logger.NewSensitiveDataFilter`, and it can contain an
  empty or whitespace-only entry. NOT a duplicate: de-duplication moved between the two
  commits but was present in both, and a duplicate needle never changed what is masked
  either way, so it is not a symptom to look for. YAML `log.sensitivefields` is NOT this case:
  that door was already normalized before this hop, by a loop in `app_builder` that this
  change deletes and replaces with the same rule one level down, so its behaviour is
  unchanged and there is nothing to audit there;
  (d) you run scheduler jobs AND any alert, saved query or log-parsing test matches the job
  panic summary's `error` field or the span's recorded error — the rendering changes from
  `panic: <value>` to `panic (type: T)`, and a query on the old text stops matching SILENTLY;
  (e) a `json.RawMessage`, or any named `[]byte`, can reach `Interface()`/`WithFields()` with
  a secret inside it — see detect (4); (f) you register an `AuditRecorder` via
  `FlywayMigrator.WithAuditRecorder` AND anything keys on the audit sink-failure log line,
  whose shape changes (a new `panic_type` field, and a second possible message,
  `audit sink panicked; event dropped (value unrenderable)`, when the value cannot be
  rendered).
  no-match = ALL of the following hold. (1) Nothing you log contains a map, slice, array,
  pointer or `any` — at the top level or nested inside a struct, at any depth. **A typed
  struct is not automatically outside this atom**: the walk descends into its fields, so one
  non-scalar field anywhere inside puts that value in scope. In scope means CHECK, not
  "will change": what the walk emits for a non-scalar depends on its element type and on
  nesting depth — a `[]byte` stays base64 and a typed nil slice stays `null`, but the same
  `[]byte` seven levels of nesting down comes back as `["***", …]`, because past the depth budget
  everything is masked regardless of type. So there is no shortcut from "it is a `[]byte`"
  or "it is a struct" to "unaffected"; the shortcut runs the other way, from "everything I
  log is a scalar" to no-match. (2) Nothing downstream depends on the concrete type of
  `FilterValue`'s result, by assertion, type switch, reflection or any other route.
  (3) Your needle lists are literal and clean. (4) You have no scheduler alert on the panic
  summary. (5) You register no `AuditRecorder`. (6) You log no pre-encoded JSON. Note (a) is the common case and its old behaviour was a
  crash, so matching it alone is good news, not work — but check (e) before concluding that:
  one shape inside (a) went the other way, from crash to silent leak, and it is the only
  branch here that leaves you worse off than before the upgrade.
- apply: nothing to write for (a) — the crash stops. For (b), change the assertion ONLY where the element type is
  one the walker rewrites — maps, slices, arrays, structs, pointers, and `any`. A slice of
  SCALARS keeps its concrete type, and so does `[]byte` (still base64, not an array of
  numbers), so a working `[]string` or `[]byte` assertion must be left alone: "convert
  everything to `[]any`" would turn passing code into failing code. If one call site handles
  several slice shapes, read the value through JSON rather than type-asserting at all. There
  is no longer any input for which the emitted type depends on whether a needle happened to
  match, so an assertion that was passing by luck now fails consistently rather than
  intermittently. For (c) — the Go door only — remove the empty entry, but read the next line first, because
  removing it is a change in what your logs CONTAIN. A deployment carrying a stray empty
  needle has been masking every field of every log line; after the upgrade those fields
  appear again. That is the intent, and it is also the moment to check that nothing
  sensitive was relying on the accident: re-read the effective needle list against what your
  service actually logs, exactly as if you were configuring masking for the first time. If
  your list normalizes to NOTHING, startup now WARNs — treat that WARN as an error unless
  you deliberately disabled masking. Do this re-read BEFORE the upgrade, not after: a
  deployment carrying `SensitiveFields: [""]` today has EVERYTHING masked, so its needle list
  has never actually been exercised and may name nothing useful at all. Upgrade first and it
  finds that out in production, at whatever moment it next logs a sensitive field.
  For (d), repoint the alert or query at the new rendering before upgrading — matching
  `panic (type: ` is stable, matching the old `panic: {` is not. Do it BEFORE, because the
  break is silent: the query stops matching and the alert simply never fires again, which
  reads as "no job panics" rather than as a broken alert. For (e) there IS work, and it is the one branch of
  this atom where the change makes a call site worse rather than better — that shape used to
  crash and now logs in clear. The remedy depends on the payload's SHAPE, and the
  obvious form does not cover all three. Decoding into `map[string]any` works only for a
  top-level OBJECT: an array payload (`[{"password":"pw"}]`) fails to unmarshal into it
  entirely, so the decode errors and the raw bytes get logged anyway — a remedy that appears
  applied and changes nothing. **Decode into `any` instead** (or into the payload's known
  type): measured, that masks both `{"password":"pw"}` and `[{"password":"pw"}]`, because
  the walk reaches inside array elements. Verify that it does — mask INSIDE the element, not
  merely that the line changed. A top-level SCALAR payload (`"pw"`) cannot be fixed this
  way at all: there is no field name for the filter to match, and it still emits `"pw"`
  after decoding. For that shape use a size and a digest instead of the content, or drop the
  field from the log call — those are the only options, not a lesser preference. Do NOT reach for a needle — the
  filter matches field NAMES and the secret here is a key inside opaque bytes, so no entry in
  `log.sensitivefields` can reach it. Tracked as
  [#1133](https://github.com/gaborage/go-bricks/issues/1133). For (f), repoint whatever keys
  on the audit sink-failure line: it gains a `panic_type` field, and an unrenderable value
  produces the `(value unrenderable)` message instead of the usual one. Nothing else to do —
  the change is strictly in your favour, since that path used to kill the process.
- verify: for (a), log a document shaped `{"data":[{"password":"p","name":"n"}]}` through
  `Interface()` and again through `WithFields()`. Both must emit
  `{"data":[{"password":"***","name":"n"}]}` — masked INSIDE the array element, which proves
  the walk reaches in rather than merely not crashing. Log a typed nil slice in the same
  pass — `[]any(nil)` under any field — and confirm it still emits `null`, not `[]`; that is
  the one shape in this change that would otherwise be wire-visible to a log parser.
  For (b), exercise the call site that consumes `FilterValue`'s result with a slice whose
  elements the filter REWRITES (a `[]any` of maps, not a `[]string`). A type switch is the
  case to check by running rather than reading: confirm the intended arm is still selected
  and that you have not silently landed in `default`.
  For (c), construct your real `FilterConfig` and log a two-field document: a field your list
  does NOT name must appear in clear. If it comes out masked, your list still carries an
  entry that matches everything. If your list is one that normalizes away entirely, confirm
  the masking-disabled WARN now appears at startup — it is the only signal that the config
  masks nothing, and it is suppressed at `log.level: error` and above.
  For (e), **use a synthetic secret and a disposable staging sink** — the whole point of the
  probe is that you do not yet know whether the value is masked, so assume it is not: a real
  credential put through it lands in the log in clear, and a log you cannot delete is a
  rotation, not a test. A literal such as `not-a-real-secret-0000` proves the same thing. Log
  the payload and read the output: the synthetic secret inside the pre-encoded bytes must no
  longer appear. Decoding into `any` first is the remedy
  that puts an object OR array payload back under the filter — confirm the field is masked, not merely absent,
  because "absent" can also mean you dropped the wrong field.
  If you matched (d), make a scheduler job panic in staging and read two things off the
  result: the summary line's `error` must read `panic (type: …)` and carry NO field value
  from the panic, and the job must be recorded as FAILED — `GET /_sys/job` shows
  `failureCount` incremented and `lastExecutionStatus` `failure`. The second half matters
  because the accounting runs after the panic is reported: if the report ever fails, the
  outcome must still be recorded. Confirm your repointed alert fires on that run.
  If you matched (f), point a throwaway `AuditRecorder` at staging and have its `Record`
  panic with a slice of maps. The migration must COMPLETE, the process must survive, and
  `migration.audit.sink_failures` must increment by one with a log line naming `audit_type`
  and `target`. Before this hop that panic killed the process from a bare consumer
  goroutine, so "the migration finished" is the assertion that matters.
- ref: [ADR-079](adr_079_log_filter_walks_slices_without_comparing.md) ·
  [ADR-072](adr_072_default_log_filter_names_key_material_explicitly.md) (addendum: its JWKS
  consequence described a leak where the array shape actually panicked) ·
  [ADR-019](adr_019_migration_audit_delivery.md) ·
  `logger/filter.go` (`filterSliceOrArrayWithProtection`, `rewritesType`, `normalizeNeedles`) ·
  `logger/logger.go` · `app/app_builder.go` · `scheduler/module.go` ·
  `migration/audit_emitter.go` · `messaging/internal/delivery/delivery.go` (comment only) ·
  follow-ups [#1132](https://github.com/gaborage/go-bricks/issues/1132), [#1133](https://github.com/gaborage/go-bricks/issues/1133), [#1134](https://github.com/gaborage/go-bricks/issues/1134)

### [C60.13] the default log filter drops its bare `key` needle · silent-behavior · when: match

- detect: **this one un-masks, so the detect is the important part.** Your Go code cannot tell
  you: the field name comes from wherever you build the log line, and nothing fails. Grep your
  own log calls for key-shaped field names, then read each hit for whether the VALUE is key
  material or an identifier:

  ```bash
  # 1. interpreted string literals — the common case
  git grep -niE '"[^"]*key' -- '*.go'
  # 2. raw string literals, which quote 1 misses entirely
  git grep -niE '`[^`]*key' -- '*.go'
  # 3. constants and variables holding a field name (a map key spelled as a
  #    literal is already in 1; a map key held in a variable is here)
  git grep -niE '(const|var)[^=]*key|key[A-Za-z0-9_]*[[:space:]]*(=|:=)[[:space:]]*("|`)' -- '*.go'
  # 4. field names the call site does not spell — read each hit whose first
  #    argument is an identifier or a call rather than a quoted literal
  git grep -nE '\.(Str|Int|Int64|Bool|Float64|Dur|Time|Bytes|Interface|Any|Fields|RawJSON)\(' -- '*.go'
  ```

  Keep the `-i` on 1-3 and do not anchor on a closing quote: an anchored pattern misses `keys`,
  `key_id` and `KEY`, which is most of what changed. Keep the patterns plain — `git grep -E` is
  POSIX ERE and silently ignores `\b`, `\s`, `\d` and `\w`. Greps 3 and 4 are deliberately
  wide, because a field name assembled from a constant, a map key, or a helper
  (`fieldFor(resource)`) carries no `key` text at the log call at all; skim them for names that
  reach the filter rather than reading every hit. **Then — required, and the only step that sees
  what your code cannot —** search your log backend for fields currently rendering `***` whose
  name contains `key`: everything there is about to change, in one direction or the other, and a
  helper-generated name shows up there under its real spelling. Skip the greps only if you never
  log a `*key*` field at all; do not skip the backend audit.
- scope: `logger/filter.go` only, as a change to the needle list `DefaultFilterConfig` returns.
  The matcher is untouched — still case-insensitive, still SUBSTRING, still applied at the same
  seam. Removed: the bare `key`. Added: `private_key`, `privatekey`, `signing_key`, `signingkey`,
  `encryption_key`, `encryptionkey`. Already present and kept: `api_key`, `apikey`. Both
  spellings of each, plus the hyphenated `api-key`, `private-key`, `signing-key` and
  `encryption-key`, because substring matching relates none of them — `api_key` does not contain
  `apikey`, nor either of them `api-key`. The hyphenated ones matter because `httpclient` logs
  whole `http.Header` maps through this filter under `LogPayloads`, and a header is spelled
  `X-Api-Key`. `secret_key` and `secretkey` get no needle of their own: the `secret` needle
  already covers them.
- gate: match = you log any field whose name contains `key`, on either side of the line. Two
  outcomes, and you want to know which you are in. **Identifiers stop being masked** — `key`,
  `keys`, `tenant_key`, `cache_key`, `routing_key` now log in clear, which is the point of the
  change and also means the framework's own fifteen `key` sites start emitting what they were
  hiding: fourteen a tenant or resource identifier (the app factory resolver, the messaging
  manager, the database manager), and one — `server/handler.go` — the NAME of a reserved
  envelope-meta key a handler collided with. **Secrets the new list does not name also stop
  being masked** — `license_key`, `hmac_key`, `master_key`, `session_key`, or a vendor header
  like `Ocp-Apim-Subscription-Key` — and that is a leak, silently, on upgrade. Check your
  outbound HTTP headers specifically if you run `httpclient` with `LogPayloads`, and note the one
  shape that does not read as a secret: `keys` is the JWKS container. The bare needle stopped the
  filter's walk there; now it recurses into the array, and a JWK's `d` — the RSA private exponent
  — matches no needle. Add `keys` to `log.sensitivefields` if you log private key sets.
  no-match = no `*key*` field names anywhere in your logs.
- apply: for anything in the second group, add the needle you need — one line, additive, merged
  into the defaults, no code change:

  ```yaml
  log:
    sensitivefields:
      - license_key
      - hmac_key
  ```

  `app.Options.LoggerFilterConfig` is the other seam and REPLACES the whole list; if you use it,
  start from `logger.DefaultFilterConfig()` and append, or you lose every default. Nothing to do
  for the first group — that is the fix.
- verify: log one line carrying both shapes — a field named `tenant_key` and a field named
  whatever your secret is called — and read the output. The identifier renders its value; the
  secret renders `***` only if the new list names it or you added it. Checking one shape and
  assuming the other is exactly the mistake this atom exists to prevent.
  **Use a synthetic value for the secret field, and run this in staging or against a disposable
  logger writing to a scratch sink.** The whole point of the probe is to find out whether that
  field is still masked, so assume it is not: a real credential put through it lands in the log
  in clear, and a log you cannot delete is a rotation, not a test. A literal such as
  `not-a-real-secret-0000` proves the same thing.
- ref: [ADR-072](adr_072_default_log_filter_names_key_material_explicitly.md) ·
  `logger/filter.go` (`DefaultFilterConfig`) · `wiki/observability.md` (the list and both seams)
  · #1037

---

### [C60.14] the `config.TestKey*` constants are deleted · compile-break · when: match

- detect: `git grep -nE '(^|[^[:alnum:]_])TestKey[A-Za-z0-9_]*([^[:alnum:]_]|$)' -- '*.go'` — treat every hit as a
  CANDIDATE, not proof: the pattern also matches a comment, a string literal, and any unrelated
  local identifier that happens to start with `TestKey`. Read each one and keep the hits that
  reference a deleted constant; those are what this applies to. Nothing is silent — the build
  fails on the identifier — but the grep is a shortlist, not the verdict.
  Search the IDENTIFIER rather than a `config.` qualifier: an aliased import
  (`cfg.TestKeyServerPort`) or a dot-imported one (`TestKeyServerPort`) carries no `config.`
  and would leave a qualifier-anchored grep reporting no impact right up to the build failure.
  The bracket expressions are POSIX classes on purpose — `git grep -E` silently ignores `\b`
  and `\w`, so a word-boundary pattern would look precise and match nothing.
- scope: `config/testkeys.go` is deleted whole; all 33 exported `TestKey*` constants go with it,
  and nothing replaces them. No configuration key is renamed and no loader path moves — this
  removes names for keys, not the keys.
- gate: match = at least one `TestKey*` reference, anywhere, under any import spelling, test
  files included.
  no-match = none, which is the case for every consumer we can see; the constants had no call
  site inside this repository either, which is why they are going.
- apply: inline the key string at the call site, the way every test in this repository already
  does. **Read the table before you inline** — five of the constants named keys the loader does
  not read, so copying their values forward copies a test that proves nothing:

  | Constant | Its value (wrong) | The key to inline |
  | --- | --- | --- |
  | `TestKeyDatabaseConnectionString` | `database.connection_string` | `database.connectionstring` |
  | `TestKeyMessagingBrokerHost` | `messaging.broker.host` | no such key — the broker is addressed by `messaging.broker.url` |
  | `TestKeyMessagingBrokerPort` | `messaging.broker.port` | no such key — part of `messaging.broker.url` |
  | `TestKeyMessagingBrokerUser` | `messaging.broker.username` | no such key — credentials live in `messaging.broker.url` |
  | `TestKeyMessagingBrokerPassword` | `messaging.broker.password` | no such key — credentials live in `messaging.broker.url` |

  `BrokerConfig` has only ever had `url` and `virtualhost`, so the four broker keys were never
  real at any point in this project's history — a test setting one takes the zero value and
  passes. The other 28 constants held correct values; inline them as they were. Of the three
  `custom.api.*` ones, two were fixtures for the config-injection tests and one mirrors the
  README's injection example — none is framework schema, so nothing there can be wrong against a
  loader.
- verify: `go build ./... && go test ./...` — **both**, and the second is the one that matters
  here. These constants named config keys for TESTS, so that is where a consumer's references
  live, and `go build ./...` does not compile `_test.go` files: it would report a clean tree
  while every reference sits in a file it never opened. `go vet ./...` compiles test files too
  and is the cheaper check if you only want the compile. There is no runtime behaviour to
  exercise beyond that — the break is compiler-caught once the compiler is actually shown the
  files.
- ref: [ADR-073](adr_073_test_key_constants_removed.md) · `config/testkeys.go` (deleted) · #1028

---

## E61 · v0.60.0 → v0.61.0 — the server's own 400 details stop echoing request input, span sinks record errors by type, the query builder validates and normalizes every identifier door and `Having` gains a sanctioned expression path, `jf.Eq` renders nil and lists like `f.Eq`, and the framework owns the Flyway URL

- gist: Two of the framework's own 400 details echoed caller-controlled text. `FieldError.Value`
  was `fmt.Sprintf("%v", err.Value())` — the rejected input, for ANY failed validation tag — and
  `FieldError.Field` carried a `dive`-validated map's input key verbatim (`Limits[4111…-SECRET]`),
  as did the human-readable message built from it. A bind failure rendered `bindErr.Error()`: the
  JSON decoder's own text, which under a Go 1.27 build reports a map destination's field path as
  `limits.<input key>`, or a strconv error quoting the rejected query/path/header value. Both were
  gated on `IsDevelopment()` alone, and no response body passes through the logger's
  `SensitiveDataFilter`. `Value` is removed outright (C61.1), namespaces are bracket-redacted at
  both doors, bind failures render a payload-free summary, and every response detail map moves
  behind `Debug && IsDevelopment` at the single `devDetails` funnel — the `[C60.30]` posture, now
  at every status and on the raw renderer too (C61.2). The safe-rendering primitives themselves
  move out of `messaging` into `internal/saferender` unchanged; `messaging`'s exported surface and
  rendering are untouched (ADR-084). Separately, on the Oracle rendering side, `Insert().Columns`
  and `.SetMap` join the reserved-word quoting their `InsertWithColumns` sibling already applied,
  so a column named `level` stops emitting SQL Oracle rejects (C61.6). The same hop also closes the
  matching hole on the other side of the platform boundary: every framework span sink dropped
  `exception.message` and reports the error's Go type instead, through one exported helper
  (C61.3, ADR-083). The same hop makes two more
  query builders deterministic: `InsertStruct` and a field-list-free `SetStruct` ranged over a Go
  map, so identical input emitted a different column order per process (C61.5). Both identifier
  renderers recognize a whole quoted identifier before splitting on dots, so `"my.col"` is one
  column rather than two (C61.7). The query builder's
  three identifier validators return the normalized identifier, so every validator-routed door
  renders the value validation judged rather than the caller's padded original (C61.10), and
  `BuildUpsert`'s column keys — the one door that carve-out excluded — join them: they are
  trimmed, rendered and compared in their trimmed spelling, under a single acceptance rule that
  both vendors now apply, so PostgreSQL refuses the qualified and function-shaped keys only Oracle
  refused and both refuse a doubled-quote key (C61.15).
  Separately on the query-builder side: `Having` forwarded its predicate straight to the
  underlying builder, so a `qb.Expr()` RawExpression — the spelling `Select`, `GroupBy` and
  `OrderBy` all take — was rejected, leaving a raw string as the ONLY way to write a HAVING
  clause. `Having` now accepts a RawExpression (an alias on it is an error: a predicate
  projects nothing), and the string form is documented as the escape hatch it always was, so
  the `// SECURITY: Manual SQL review completed` annotation rule now covers four doors rather
  than three (C61.8). The JoinFilter value doors render a nil operand as `IS NULL` and a slice or
  array as `IN (…)` like their Filter siblings — a typed nil pointer and a `driver.Valuer`
  reporting NULL counting as nil — while the ordering doors refuse every one of those forms, and
  a Valuer holding a value binds the value it resolves to rather than the wrapper, and a nil
  pointer to a Valuer type renders `IS NULL` where it used to panic (C61.11).
  Separately on the migration side: `database.tls` reached Flyway only as `DB_SSL*` environment
  variables that the operator's `flyway.conf` may or may not have interpolated into its own JDBC
  URL, so a conf naming no `sslmode` migrated in cleartext under a `verify-full` config and a conf
  naming a different host migrated a different database than the runtime connects to. The framework
  now builds the URL from `database.*` and passes it as `-url=`, which outranks the conf, for every
  PostgreSQL discrete-field config; the WARN and the `DB_SSL*` export are gone and a PG
  client-certificate migration fails closed (C61.4, ADR-085).
  The same hop makes the native streams lane opt-in at the build graph (C61.25, ADR-091):
  `app` no longer imports `messaging/streams`, so a core-only consumer carries none of the
  vendor client, and a leftover `messaging.streams.uri` without that import fails startup
  naming it.

---

### [C61.1] `server.FieldError.Value` is removed · breaking · when: always

- detect: `git grep -nE '[.]Value([^A-Za-z0-9_]|$)' -- '*.go'` over your handlers, error middleware and tests,
  and read the hits whose receiver is a `server.FieldError` — the name is common enough that the
  grep alone does not decide. Then grep your RESPONSE fixtures and contract tests for a `value`
  key inside a `validationErrors` entry (`git grep -n '"value"' -- '*.json' '*.yaml' '*_test.go'`);
  the wire shape loses the key too, and no Go grep finds a fixture.
- scope: `NewValidationError` built each `FieldError` with `Value: fmt.Sprintf("%v", err.Value())`,
  the rejected input for every failed tag — a `min=8` failure on a password field echoed the
  password into the 400 body. The field is deleted rather than redacted: every candidate rule is
  shape-based (a length floor, a charset, a PAN detector) over a value whose meaning belongs to
  the consumer, and `min=8` on a password is indistinguishable from `gte=100` on an amount. In the
  same pass `Field` — and the message built from it — is rendered through the bracketed-span
  redaction, so a `dive`-validated map's element reads `Limits[*]` instead of carrying the input
  key. A struct with no map keeps exactly the name it had.
- gate: always — the field is gone from the struct, so any reference is a build failure.
- apply: delete the read. If the rejected value is genuinely needed for a user-facing message,
  the handler that owns the request already has it and can put it in its own `WithDetails` entry,
  where the decision to expose it is explicit and yours; the framework will not make it for you.

  ```go
  // before
  for _, fe := range ve.Errors {
      log.Printf("%s: %s (got %q)", fe.Field, fe.Message, fe.Value)
  }

  // after
  for _, fe := range ve.Errors {
      log.Printf("%s: %s", fe.Field, fe.Message)
  }
  ```

- verify: `go vet ./...` — not `go build ./...`, which does not compile `_test.go` files, and
  fixtures aside this break lands most often in a test that asserted the echoed value. Then POST
  a body that fails a `dive`-validated map field with a PAN-shaped key and read the 400 body: it
  must show `Limits[*]`, no `value` entry, and the key nowhere in the bytes.
- ref: [ADR-084](adr_084_response_error_details_carry_no_request_input.md) · `server/validator.go` · issue #1175

---

### [C61.2] every response `error.details` map requires `app.debug` AND a development env · silent-behavior · when: match

- detect: grep every deployment surface — YAML, `.env` files, Helm values, Kustomize overlays,
  rendered manifests — for `app.env`/`APP_ENV` set to a development alias (`development`, `dev`,
  `local`) held together with `app.debug`/`APP_DEBUG` set to `false` or left unset. That pairing
  is the only one whose emitted body changes. Separately, grep smoke tests, debugging scripts and
  contract fixtures for a reader of `details.error` on a **400** — that string changes shape even
  where the gate is open.
- scope: two independent changes, both landing on the same map. First the gate: `devDetails` is
  the single funnel both response renderers (enveloped and raw) already used, and it now requires
  `cfg.App.Debug && cfg.App.IsDevelopment()` instead of the environment alone. `[C60.30]` made
  that trade for the 5xx `details.error`; this extends it to the WHOLE map at EVERY status — a
  handler's own `WithDetails` entries, the `validationErrors` list, and the captured
  `stackTrace` frames included. Second the content: a bind failure's `details.error` was
  `bindErr.Error()` and is now a payload-free summary — for a JSON body, the decode summary
  (`json: type mismatch (want int, offset 41)`, with the field path included only when the
  request type cannot put input text into it, which a map, an `any`, or any
  `json.Unmarshaler` such as `time.Time` can); for query, path and header binding, the source
  and the destination field named by its struct tag (`failed to bind query param "ratio"`). The
  raw cause is unchanged on the log line and one `errors.Unwrap` away.
- gate: match = `app.env` is a development alias AND `app.debug` is false or unset (the gate
  half), or anything reads a 400's `details.error` (the content half). no-match = a production
  posture that read no details to begin with.
- apply: set `app.debug: true` in the development environments that were relying on details, or
  accept the loss. For the content half there is nothing to change in the framework — repoint the
  assertion at the new phrasing, or read the log line, which still carries the decoder's own text.
- verify: against a service running `app.env: development` with `app.debug: false`, POST a body
  that fails validation: the 400 must decode to an `error` object with NO `details` key at all.
  Flip `app.debug: true` and repeat: `details.validationErrors` is back. Check a raw-mode route in
  the same pass — it shares the funnel, and a probe of the enveloped route does not answer for it.
- ref: [ADR-084](adr_084_response_error_details_carry_no_request_input.md) · `server/handler.go`
  (`devDetails`, `requestProcessor.process`) · `server/bind_error.go` · issues #1173, #1175

---

### [C61.6] Oracle reserved-word columns are quoted on `Insert().Columns` and `.SetMap` · silent-behavior · when: match

- detect: `git grep -nE '[.](Columns|SetMap)[(]' -- '*.go'` over the call sites that start from
  `qb.Insert(` (NOT `InsertWithColumns`, which already quoted), then read each column list for a
  name Oracle reserves — `level`, `comment`, `size`, `number`, `date`, `session` and the rest of
  the reserved list. Only an Oracle deployment is affected; on PostgreSQL the emitted SQL is
  byte-identical.
- scope: both doors now route their names through the same vendor quoting funnel
  `InsertWithColumns` has always used, so a reserved word renders `"level"` instead of `level`.
  Before this, those statements were REJECTED by Oracle at execution with ORA-00904 — the change
  turns SQL that could not run into SQL that runs, so nothing that worked stops working. Case is
  preserved verbatim and a non-reserved name is left unquoted, exactly as on the sibling doors.
  `SetMap` sorts the caller's own names before quoting, so the column order it emits is the order
  it emits today; quoting first would have ordered `"level"` ahead of `id` on the leading quote.
  Validation and its error wording are unchanged at both doors.
- gate: match = the service runs Oracle AND builds an INSERT through `qb.Insert(...).Columns(...)`
  or `.SetMap(...)` with a reserved-word column. no-match = PostgreSQL only, or every INSERT goes
  through `InsertWithColumns`/`InsertStruct`/`InsertFields`.
- apply: nothing to change in your code. If you worked around the bug — pre-quoting the name
  yourself as `` `"level"` ``, or renaming the column to dodge it — drop the workaround: a
  caller-quoted name still passes through untouched, so it is not broken, but it is now noise.
- verify: on Oracle, `qb.Insert("accounts").Columns("id", "level").Values(1, 3).ToSQL()` must
  render `INSERT INTO accounts (id,"level") VALUES (:1,:2)`; the same statement built with
  `SetMap` must render the same column order. Run one against a real Oracle instance if you kept
  a workaround, since that is the case where the emitted text changes twice.
- ref: `database/internal/builder/query_builder.go` (`InsertQueryBuilder.Columns`, `.SetMap`,
  `newInsertBuilder`) · issue #1154 · related ADR-082

---

### [C61.3] every framework span sink records an error by its Go TYPE · silent-behavior · when: match

- detect: query your TRACING backend, not your code — every changed value is one the framework
  emitted. Search saved queries, monitors and dashboards for `exception.message`, and for a span
  status description matched by text, on these spans: `job.execute` (scheduler), `<queue> receive`
  and `<stream> receive` (message delivery, both lanes), the HTTP client's `<METHOD> <peer>` (or
  `HTTP <METHOD>` where no peer name is configured), and `<destination> publish` on both the AMQP
  and the stream publish paths. Search the same places for the AMQP `publish.retry` event's
  `error` attribute, which carried the broker's message verbatim and is now `error.type` — a
  dashboard reading `error` there goes blank rather than erroring. `exception.type` and the
  `error.type` attribute on the metric paths are unchanged, as is every log line. A Go grep finds nothing: no consumer API changes.
- scope: `span.RecordError(err)` exported `err.Error()` as `exception.message`, and each site
  copied the same text into the span status description. Both leave the platform with the tracing
  exporter and neither passes the logger's `SensitiveDataFilter`, while the errors are
  consumer-authored — a job's `Execute` error, a handler's error, a response interceptor's error,
  a caller-supplied `RoundTripper`'s error. Every framework sink now goes through one helper,
  `observability.RecordErrorByType`, which emits ONE `exception` event carrying `exception.type` =
  the error's outer `%T` and NO `exception.message`, and sets `codes.Error` with that same type as
  the description. Four sinks change: the scheduler's job-error path, the delivery pipeline's
  handler-error path (both lanes), the HTTP client's span end, and AMQP/stream publish failure. The
  HTTP client keeps a classified error-type label (`transport_error`, `interceptor_failed`, …) as
  its status description where one exists — that is framework vocabulary — and it no longer exports
  the query-stripped message it used to build. The database sink already reported by type and only
  changes spelling; the scheduler panic path still reads `panic` as its status description, and the
  RECOVERED value's type — which used to ride in `exception.message` as `panic (type: T)` — moves to
  its own span attribute, `job.panic_type`. One span ATTRIBUTE changes with them: the AMQP
  `publish.retry` event's `error` (the broker's verbatim `Reason`) becomes `error.type`.
- gate: match = any monitor, dashboard, saved query, runbook or trace-based test reads
  `exception.message` or a message-bearing span status description on one of the spans above.
  no-match = you group and alert on `exception.type`, `error.type`, the span status CODE, or your
  logs.
- apply: the replacement is NOT one value for every path — repoint each query at what ITS span
  actually emits, or at the log line, which still carries the full message everywhere.
  `exception.message` on a job, handler, HTTP-client or publish failure becomes `exception.type`
  (a Go type name, e.g. `*url.Error`, `*fmt.wrapError`). A STATUS DESCRIPTION matched by text
  depends on the path: the HTTP client keeps its classification (`transport_error`,
  `interceptor_failed`, …) and `HTTP <code>` on a 5xx, so those queries need no change; the
  scheduler panic path keeps `panic`, and the recovered value's type — previously
  `panic (type: T)` inside `exception.message` — is now the `job.panic_type` span ATTRIBUTE; only
  the paths with no framework classification fall back to the Go type. A `publish.retry` query
  reading that EVENT's `error` attribute moves to the same event's `error.type`, NOT to
  `exception.type`, which the retry event does not carry. There is
  no framework switch that restores the message: the removal is the decision. A
  span you start yourself is yours — `observability.RecordErrorByType` is offered for it, and
  nothing stops you calling `span.RecordError` in your own code.
- verify: make one job fail, one handler fail and one HTTP client call fail against an unreachable
  host, then read the three spans in your backend: each carries exactly one `exception` event with
  `exception.type` and no `exception.message`, and no status description contains the error text.
  Add the paths whose span contract DIFFERS, since a probe of the three above does not answer for
  them: a failing AMQP publish (the `publish.retry` event now reads `error.type`, and the terminal
  status is the Go type), a failing stream publish, a panicking job (status stays `panic`, and the
  recovered value's type is in `job.panic_type`), and — if you run streams — a failing handler on
  that lane as well as the classic one.
  Confirm in the same pass that the corresponding LOG lines still carry the messages — that is the
  sink the message moved to, and a check that only reads the span cannot tell removal from loss.
- ref: [ADR-083](adr_083_span_sinks_record_errors_by_type.md) · `observability/span_error.go` ·
  `scheduler/module.go` · `messaging/internal/delivery/delivery.go` · `messaging/amqp_client.go` ·
  `messaging/streams/publisher.go` · `httpclient/internal/tracking/tracing.go` ·
  `database/internal/tracking/utils.go` · issue #1132

---

### [C61.4] the framework builds the PostgreSQL Flyway JDBC URL · breaking · when: match

- detect: `grep -n 'flyway.url' <your flyway.conf(s)>` — every conf the migrate path uses, including
  per-environment ones. Then check every PostgreSQL `host` value your fleet can deliver —
  static YAML, `tenants.yaml`, and whatever your `DBConfigProvider` returns — for anything
  that is not a bare hostname or IP (a `host` carrying a port, a URL, a DSN, or a stray path
  or query fragment now fails; an IPv6 literal is fine in EITHER spelling, `::1` or `[::1]`),
  and every `port` value for one below 0 or above 65535 — a NEGATIVE port used to be treated
  as unset and silently took the driver's default, so check anything computed or
  environment-derived rather than typed. Read every `tls.ca` too: a CA set without `tls.mode`
  `verify-ca`/`verify-full` now fails, and unlike the other new rules this one binds EVERY
  config — `require` + `ca` passes `config.Validate`, so a fully-validated config can carry
  this shape; also grep for the literal `ca: system`, which is refused outright.
  Read every `tls.mode` value the same way: once trimmed it
  must be one of the libpq six, matched case-sensitively — so a case-variant (`Require`) that
  reached the migrator without passing `config.Validate` now fails, while a space-padded value
  normalizes exactly as the runtime normalizes it.
  Separately, and NOT only for TLS-configured tenants: any tenant whose `host` or `database`
  can arrive EMPTY while another of `host`/`port`/`database` is set now fails, TLS or not — so
  audit every delivery path for a blankable target field. Then read your `database:` block (or
  your `DBConfigProvider`'s per-tenant configs) for `type: postgresql` with discrete
  `host`/`port`/`database` fields, separately for `tls.cert`/`tls.key`, and separately again for
  a `connectionstring` carrying any `tls.*` beside it. Also
  `grep -rn 'DB_SSLMODE\|DB_SSLROOTCERT\|DB_SSLCERT\|DB_SSLKEY'`
  over the confs and any wrapper script that exported them.
- scope: for a PostgreSQL config using discrete fields, the framework now builds
  `jdbc:postgresql://<host>[:<port>]/<database>?ApplicationName=<app.name>[&sslmode=…][&sslrootcert=…]`
  from `database.*` and appends `-url=<that>` to the Flyway argv. A command-line flag outranks a
  conf key and **Flyway does not warn about it**, so a `flyway.url` in your conf is silently
  ignored — including one pointing at a different host or database. `database.tls.mode` becomes
  `sslmode` and `database.tls.ca` becomes `sslrootcert`; the once-per-migrator WARN and the
  whole `DB_SSLMODE`/`DB_SSLROOTCERT`/`DB_SSLCERT`/`DB_SSLKEY` export are removed, closing the
  residual gap `[C60.2]` documented. `ApplicationName` is set from `app.name`, URL-encoded, with
  no new config key — pgjdbc's spelling, not libpq's `application_name`, which pgjdbc drops
  because it builds startup parameters from a whitelist of known keys; it still lands in the
  server's `application_name` column. Credentials are NOT on the URL — `DB_USER`/`DB_PASSWORD` stay
  environment-delivered, because argv is world-readable in the process list. With
  `database.tls.cert` or `database.tls.key` set, a PostgreSQL run now FAILS with
  `migration.ErrMigrationMTLSUnsupported` — `validate` and `info` too, not just `migrate` — rather
  than connecting without the client certificate. The limitation is the FRAMEWORK's, not a pgjdbc
  format rule: the framework does not forward `database.tls.cert`/`database.tls.key` as the JDBC
  `sslcert`/`sslkey` parameters at all, so rather than silently dropping them it rejects the pair
  fail-closed. pgjdbc itself would read them — `LibPQFactory` dispatches on the file name, sending
  a `.p12` or `.pfx` path to `PKCS12KeyManager`, a `.key` or `.pem` path to `PEMKeyManager`
  (unencrypted PKCS#8 PEM, the `BEGIN PRIVATE KEY` header), and anything else to
  `LazyKeyManager` (raw PKCS#8 DER). Forwarding blindly would still
  not be safe, though: `PEMKeyManager` parses ONLY unencrypted PKCS#8, so a PKCS#1
  `BEGIN RSA PRIVATE KEY` or an encrypted PEM — both of which `database.tls.key` may legitimately
  be, since it is validated against libpq semantics — matches no reader on that path and would
  fail inside the driver instead. Two further shapes now fail rather than
  run: a `database.host` that is not an IP literal or a plain DNS name
  (`migration.ErrInvalidMigrationHost` — the host is the one URL component that must stay a
  routable address, so it is validated instead of percent-encoded; unescaped, a value like
  `h/?sslmode=disable&x=` ends the URL authority early and pgjdbc reads the injected
  parameters, so a `verify-full` config connects in cleartext to a host the value chose), and
  a PARTIALLY filled block — one of the three fields that become the URL (`host`, `port`,
  `database`) set but not a usable `host` AND `database`; credentials do not count, so a block
  carrying only `username`/`password` beside a conf-owned URL still defers — or any config carrying `database.tls.*`
  that cannot produce a URL (`migration.ErrIncompleteMigrationTarget`; a target broken in
  transit is not a deliberate hand-off, and no TLS setting may silently fail to reach the
  connection now that the `DB_SSL*` export is gone). A `database.tls.ca` set under any mode other than `verify-ca`/`verify-full` now fails with
  `migration.ErrMigrationTLSCARequiresVerify`: pgjdbc reads `sslrootcert` ONLY under those two —
  `require`, `allow` and `prefer` use a non-validating socket factory and an unset mode is
  `prefer` — so the CA was emitted onto the URL and then ignored, leaving the migration
  authenticating nothing. This is deliberately stricter than `config.Validate`, which admits
  `require` + `ca` because pgx treats that pair as `verify-ca` (a libpq inheritance) and so the
  runtime really does verify; pgjdbc does not, and this door answers for pgjdbc, so the shape is
  honored at runtime and refused for migration. `database.tls.ca: system` is refused outright
  (`migration.ErrMigrationTLSCASystemUnsupported`) — it is a libpq/pgx sentinel for the platform
  trust store, and pgjdbc's `LibPQFactory` treats `sslrootcert` as a file path with no
  special-casing (verified against REL42.7.12), so it named a nonexistent file; it is not
  remapped to the JVM default trust store, which is a DIFFERENT trust set from the one pgx uses.
  A `database.tls.mode` outside `disable`/`allow`/`prefer`/`require`/`verify-ca`/`verify-full`
  now fails with `migration.ErrInvalidMigrationTLSMode` instead of being copied onto the JDBC
  URL verbatim and left for the driver; an unset mode is unaffected, and the mode is trimmed
  before matching because `config.Validate` trims it too — the migrator mirrors the runtime's
  normalization, not just its list, so only a case-variant or a genuinely unknown mode fails. A `database.port` below 0 or above 65535 also now fails
  (`migration.ErrInvalidMigrationPort`): the URL builder omits the port when it is zero so the
  driver takes its default, and every non-positive port took that branch, so a negative one
  migrated against pgjdbc's 5432 while plainly naming a port. `0` still means unset. A FOURTH
  shape now fails: a config carrying both
  `database.connectionstring` and any `database.tls.*` field
  (`migration.ErrMigrationTLSWithConnectionString`). The framework does not parse DSNs, so the
  TLS block could only be dropped; `config.Validate` has always rejected that pair (ADR-062),
  but `MigrateFor` takes per-tenant configs from a dynamic `DBConfigProvider` or the CLI's
  `tenants.yaml` that never necessarily passed it, and the migrator no longer trusts the caller
  to have validated. There is
  no escape hatch. Three shapes are
  unchanged: **Oracle** (`database.tls` is unsupported and REJECTED for it per ADR-062 — the
  field exists in the shared config and validation refuses it — and no Oracle URL is built),
  **`database.connectionstring` with no `database.tls` block** (the framework does not parse
  DSNs; with a TLS block it fails as above), and a **`database:` block
  carrying only `type`** with no TLS (conf-owned by construction — nothing to build a URL from;
  ADR-047 already rejects that block at app startup, so it survives only in migration-only
  processes) all keep the conf-owned URL and get no `-url=`.
- gate: match = you migrate PostgreSQL with `go-bricks-migrate` or `FlywayMigrator` using discrete
  `host`/`port`/`database` fields. no-match = Oracle migrations, TLS-free `connectionstring`
  configs (one carrying any `database.tls.*` MATCHES — it fails with
  `ErrMigrationTLSWithConnectionString` instead of migrating), or no go-bricks-driven Flyway at all.
- apply: delete `flyway.url` from the conf (it is dead weight that reads as authoritative), along
  with any `${env.DB_SSL*}` interpolation and any `application_name` you were hand-carrying. If the
  conf URL pointed somewhere your `database.*` block does not, that divergence was a bug — fix
  `database.*`, which is now the single source for both the runtime and the migration.

  ```properties
  # before — flyway.conf
  flyway.url=jdbc:postgresql://${env.DB_HOST}:${env.DB_PORT}/${env.DB_NAME}?sslmode=${env.DB_SSLMODE}&application_name=billing-api

  # after — delete the line; the framework passes -url= built from database.*
  ```

  If your conf spelled it `application_name` as above, note that pgjdbc never honoured it
  anyway — its property is `ApplicationName`, and unrecognized URL keys are dropped rather
  than forwarded — so deleting the line loses nothing you actually had.

  For a PostgreSQL mTLS migration there is no in-framework path, and note what the fallback
  requires: the MIGRATION configuration must OMIT `database.tls.cert` and `database.tls.key`
  entirely — server-authenticated TLS is `database.tls.mode` + `database.tls.ca` and nothing
  else. Merely adding `mode`/`ca` to a config that still carries the client-certificate pair
  does NOT help; that config still returns `ErrMigrationMTLSUnsupported`. If the same config
  serves both, split it: separate runtime and migration configurations, the runtime one keeping
  its `cert`/`key`. Otherwise run Flyway outside the framework, supplying `sslcert`/`sslkey` in your own conf URL.
  Runtime mTLS is unaffected — `database.tls.cert`/`key` still work for the service's own
  connections.

  For a `connectionstring` tenant the remedy has TWO halves, and the DSN is only the first:
  move the TLS settings into the connection string
  (`?sslmode=verify-full&sslrootcert=/etc/ssl/ca.pem`) and delete the `database.tls` block —
  that secures the RUNTIME pool, which uses the DSN verbatim. It does NOT secure the
  migration: a `connectionstring` config is conf-owned, so no `-url=` is passed and Flyway
  reads its JDBC URL from `flyway.conf`. Put `sslmode`/`sslrootcert` on THAT url too, and — this
  half is not optional either — confirm it names the SAME host and database as the DSN, since an
  encrypted migration applied to the wrong database is still the wrong database and the framework
  cannot cross-check a DSN it does not parse — otherwise the migration runs
  unencrypted, or against a different target, which is exactly what `[C61.4]` closes for
  discrete-field configs.
- verify: point a throwaway tenant's `host` at `h/?sslmode=disable&x=` and confirm the migrate
  refuses with `ErrInvalidMigrationHost` rather than connecting; set that tenant's `port` to
  `-1` and confirm `ErrInvalidMigrationPort` rather than a migration against 5432, then `0`
  and confirm the URL carries no port at all; set its `tls.mode` to `Require` and confirm
  `ErrInvalidMigrationTLSMode` rather than that value landing in the URL's `sslmode`; set
  `tls.ca` with `tls.mode: require` and confirm `ErrMigrationTLSCARequiresVerify` rather than a
  `sslrootcert` pgjdbc would ignore, then move the mode to `verify-full` and confirm the
  migration runs with `sslrootcert` on the URL; set `tls.ca: system` and confirm
  `ErrMigrationTLSCASystemUnsupported`; blank that tenant's `host`
  while leaving `port`/`username` set and confirm `ErrIncompleteMigrationTarget`, with and
  without `tls.mode`. Give a tenant a `connectionstring` plus a `tls.mode` and confirm
  `ErrMigrationTLSWithConnectionString` rather than a migration on the bare DSN; drop the
  `tls.*` and confirm it defers to the conf again. Then run a migrate with `--verbose` (or read the process list) and confirm the argv carries
  `-url=jdbc:postgresql://<your host>:<your port>/<your database>?…` with the `sslmode` you
  configured, and that no username or password appears anywhere in it. On a multi-tenant fleet run,
  confirm two tenants with different hosts produce two different `-url=` values. Then, on the
  database side, `SELECT application_name, ssl FROM pg_stat_activity JOIN pg_stat_ssl USING (pid)`
  during a migration: `application_name` must be your `app.name` and `ssl` must be `t` for any
  `sslmode` above `prefer`.
- ref: [ADR-085](adr_085_framework_owned_flyway_url.md) · `migration/flyway_url.go` ·
  `tools/migration/README.md` · issue #1047

---

### [C61.5] `InsertStruct` and `SetStruct` render columns in sorted order · silent-behavior · when: match

- detect: your Go code cannot tell you this — the change is in the emitted SQL, not the API. Grep
  your test suite and fixtures for a PINNED statement built by `qb.InsertStruct(...)` or
  `UpdateQueryBuilder.SetStruct(...)` WITHOUT an explicit field list: a golden file, a contract
  test, an `assert.Equal` on generated SQL, a recorded statement in a mock. Anything that asserts
  the SQL by SUBSTRING (`Contains` on a single column) is unaffected. Also check any dashboard or
  slow-query report that groups by statement TEXT, and any allowlist keyed on the exact statement.
- scope: both doors ranged over the struct's field map, and Go randomizes map iteration, so the
  emitted column order — and for `SetStruct` the `SET a = ?, b = ?` order — varied between
  processes for identical input. Column/value pairing was always correct, so results were right and
  only the statement TEXT moved. Both now iterate the same `sortedKeys` helper `BuildUpsert` and the
  vendor upsert builders already used, so identical input yields byte-identical SQL on every call,
  on PostgreSQL and Oracle alike. `InsertFields` (explicit field list) and `SetStruct` WITH an
  explicit field list were already deterministic — they follow the caller's order and are
  unchanged.
- gate: match = anything pins, caches or groups by the exact statement text of one of those two
  doors. no-match = you assert results rather than SQL, or you always pass an explicit field list.
- apply: re-record the golden file or fixture ONCE — the order will not move again. It is
  alphabetical by the VENDOR-QUOTED column name, which is not the same as alphabetical by column
  name: the map keys carry Oracle's reserved-word quoting, and `"` (0x22) sorts ahead of every
  letter, so a struct with `id`/`level`/`name` emits `("level",id,name)` on Oracle and
  `(id,level,name)` on PostgreSQL. An aliased `Columns` sorts on the `u.` prefix for the same
  reason. There is no opt-out: the previous behaviour was not an order, it was the absence of one.
- verify: render the same struct through the door twice in one process and compare the SQL
  strings — they were already equal within a process about half the time, so run the assertion in
  a loop (twenty rounds is plenty) or across two processes. A prepared-statement cache should now
  show one entry per logical statement where it previously showed several.
- ref: `database/internal/builder/query_builder.go` (`InsertStruct`, `UpdateQueryBuilder.SetStruct`)
  · `database/internal/builder/helpers.go` (`sortedKeys`) · issue #1157

---

### [C61.7] a quoted identifier carrying a dot renders as one name · silent-behavior · when: match

- detect: `git grep -nE '"[^"]*\.[^"]*"' -- '*.go'` over the identifiers your code hands the
  builder — any caller-quoted segment carrying a dot, most often a column an external system
  named. The pattern is deliberately quote-aware rather than charset-based: a quoted name is
  free to hold spaces and punctuation, so `"my col.name"` and `"my-col.name"` are exactly the
  shapes a charset pattern would miss. Separately, `git grep -n 'EscapeIdentifier(' -- '*.go'`
  — it is exported, and its rendering of an unparseable identifier changes too. And grep for an
  UNQUOTED upsert key carrying a parenthesis (`COUNT(`, not `"COUNT("`): those are refused now.
- scope: both identifier renderers split on `.` BEFORE asking whether the whole string was
  already a well-formed quoted identifier, so `"my.col"` was torn into `"my` and `col"` and each
  half quoted separately. Both now split through the package's quote-aware walker: a dot inside a
  quoted segment stays part of the name, while `"a"."b"` and `t.level` still split as they did.
  A string the walker rejects — unbalanced quotes, `a"b.c` — renders as ONE escaped identifier
  rather than being split on a dot the parser never reached; `EscapeIdentifier` returns
  `"a""b.c"` where it used to return `"a""b"."c"`. Both are fully escaped. That path is live
  rather than theoretical: `EscapeIdentifier` is EXPORTED for quoting a dynamic identifier before
  it goes into raw SQL, so it is a trust boundary, not a post-validation step — the internal
  renderer, by contrast, is only reached through doors that validate first.
  `BuildUpsert` is the one door that does NOT gain the dotted-quoted name, and the rule there is
  per-vendor rather than shared. On ORACLE a key must render as ONE column name, so anything
  whose rendering carries a dot is refused — ADR-071 records that and this change deliberately
  leaves it standing. On PostgreSQL nothing is refused: its escaper splits on the dot and quotes
  each part, so `t.name` builds as a QUALIFIED REFERENCE rather than a column named `t.name`.
  That is unchanged here and is not an endorsement of the key; refusing it on PostgreSQL would be
  a second breaking change.
  Separately, the Oracle quoter's function-shaped pass-through is deleted — it returned any
  `NAME(args)` string verbatim, and since ADR-082 no public door admits a parenthesis, nothing
  reached it. That is internal, with one visible edge: Oracle's upsert refuses an unquoted key
  carrying a parenthesis, including a malformed one like `COUNT(` that used to be accepted as the
  literal column `"COUNT("`. Quote such a key to keep it.

- gate: match = your code hands the builder a caller-quoted identifier containing a dot; or reads
  `EscapeIdentifier`'s output for a string with unbalanced quotes; or runs ORACLE and passes
  `BuildUpsert` an UNQUOTED column key carrying a parenthesis (`COUNT(`, `SUM(x)`), which is
  refused now. no-match = every identifier is a plain or dot-qualified name and you do not use
  `BuildUpsert` on Oracle with computed keys — the overwhelming majority.
- apply: drop any workaround that pre-split a dotted quoted name into segments, or that renamed
  the column to avoid the mangling — the name now survives whole. Nothing else to change.
- verify: `qb.EscapeIdentifier("\"my.col\"")` returns `"my.col"` unchanged on both vendors, and
  `t."my.col"` keeps its second segment intact. On Oracle, `SELECT :1 AS "my.col"` is the
  statement shape this makes expressible — through the query doors, not through `BuildUpsert`.
- ref: `database/internal/builder/query_builder.go` (`EscapeIdentifier`) ·
  `database/internal/builder/oracle.go` (`oracleQuoteIdentifier`) ·
  `database/internal/builder/helpers.go` (`keyIsFunctionShaped`) · issues #1151, #1149 ·
  related ADR-071, ADR-082

---

### [C61.8] `Having` accepts `qb.Expr()`, and a string predicate joins the annotation rule · convention · when: match

- detect: `git grep -nE 'Having\(' -- '*.go'` across your own code. Every hit passing a STRING
  is a raw-SQL door that now owes an annotation; every hit that wanted an expression can stop
  hand-building the string.
- scope: `Having(pred any, rest ...any)` previously forwarded `pred` straight to the underlying
  builder, which rejects a `dbtypes.RawExpression` at render time — so `qb.Expr("SUM(x) > ?")`
  failed where the identical expression works in `Select`, `GroupBy` and `OrderBy`, and a raw
  string was the only working spelling. That made the annotation ask awkward: it would have
  been a review requirement on the sole available API. `Having` now recognizes a
  `RawExpression` ahead of the string case, renders its SQL with the args in order on both
  vendors, and rejects one carrying an alias with `dbtypes.ErrAliasInHaving` through the
  builder's deferred-error channel — HAVING takes a predicate, which projects nothing, so an
  alias would be silently dropped. Nothing about the string form changed. What changed around
  it is the CONVENTION: a string predicate is a raw-SQL door on par with `f.Raw`, `jf.Raw` and
  `database.Raw`, and carries the same inline
  `// SECURITY: Manual SQL review completed - <what was verified>` annotation. HAVING is still
  NOT validated against the identifier grammar in either form — it is a predicate, not an
  identifier (ADR-082).
- gate: match = your code calls `Having` at all. no-match = it does not.
- apply: prefer the expression spelling, which needs no annotation — exempt for
  consistency with `Select`/`GroupBy`/`OrderBy`, NOT because it is safer.
  `RawExpression.Validate()` never inspects the SQL body, so an expression body is
  raw SQL exactly as a string predicate is; review it the same way, and grep it by
  its own name (`git grep -nE 'MustExpr\(|[.]Expr\(|RawExpression\{'`) rather than by an
  annotation. Whether `qb.Expr` bodies should carry the annotation repo-wide is a
  policy question above this change; it is filed separately. —

  ```go
  // before — the only spelling that worked
  qb.Select("dept").From("emp").GroupBy("dept").Having("SUM(amount) > ?", 100)

  // after — sanctioned; qb.Expr returns (RawExpression, error), qb.MustExpr panics instead
  qb.Select("dept").From("emp").GroupBy("dept").Having(qb.MustExpr("SUM(amount) > ?"), 100)
  ```

  Keep a string predicate where the expression form does not fit, and annotate it at the call
  site naming what you checked — value-side parameterization, no user input concatenated.
- verify: `git grep -nE 'Having\(' -- '*.go'` and confirm every string-predicate hit has an
  annotation above it; then run one query each way and read the SQL — the expression form must
  render `HAVING SUM(amount) > $2` (`:2` on Oracle) with the arg numbered AFTER any `Where`
  arg, and `Having(qb.MustExpr("x > ?", "alias"), 1)` must fail `ToSQL()` with
  `errors.Is(err, dbtypes.ErrAliasInHaving)`. Use a DANGEROUS alias as a second case —
  `Having(dbtypes.RawExpression{SQL: "x > ?", Alias: "total;"}, 1)` — which must report the same
  sentinel: for HAVING no alias is legal, so the error must not vary with the alias's content.
  A benign alias alone cannot tell the two orderings apart.
- ref: #1147 · #1146 · `database/internal/builder/query_builder.go` (`Having`) ·
  `database/types/errors.go` (`ErrAliasInHaving`) · [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md)

---

### [C61.9] a `RawExpression` alias must be an unquoted identifier · breaking · when: match

- detect: `git grep -nE 'Expr\(|MustExpr\(|RawExpression\{' -- '*.go'` and read the ALIAS argument
  of every hit — the second argument to `Expr`/`MustExpr`, or the `Alias:` field of a struct literal.
  Anything that is not a bare unquoted identifier now fails. Separately,
  `git grep -n 'ErrDangerousAlias' -- '*.go'` — that sentinel is gone and any reference is a build
  failure.
- scope: `RawExpression.Validate()` checked the alias against a six-substring denylist (`;`, `'`,
  `"`, `--`, `/*`, `*/`) and accepted everything else. A denylist accepts what it does not
  enumerate, so `Alias: "x FROM users"` — carrying none of the six — rendered
  `SELECT 1 AS x FROM users FROM users`, ending the alias and opening a clause the caller chose;
  a space, a parenthesis, a newline and a backtick all passed too. The alias is now judged by the
  same grammar every other identifier argument satisfies (ADR-031/ADR-082):
  `sqllex.IsUnquotedIdentifier`, a letter or underscore followed by letters, digits, underscore,
  `$` or `#`. Note it is the UNQUOTED predicate: `IsBareIdentifier` also admits the framework's
  quoted reserved-word form (`"level"`), and an alias may not use it — the framework never emits a
  quoted alias, so admitting one would widen the grammar for caller text alone. An empty alias is
  still "no alias" and renders without `AS`. The SQL BODY is still not validated; that is what the
  hatch is for.
  The check lives in `Validate()`, the funnel `[C60.29]` established, so it applies at BOTH doors:
  `Expr()`/`MustExpr()` at construction, and `Select`/`GroupBy`/`OrderBy`/the `JoinFilter` value
  doors at consumption, where a struct literal is indistinguishable from a constructed value.
- gate: match = any alias in your code is not a bare unquoted identifier, or anything references
  `ErrDangerousAlias`. no-match = every alias is already a plain name.
- apply: rename the alias to an identifier. There is no quoted-alias escape:

  ```go
  // before — accepted by the denylist, rejected now
  qb.MustExpr("COUNT(*)", "total count")
  qb.MustExpr("COUNT(*)", `"total"`)

  // after
  qb.MustExpr("COUNT(*)", "total_count")
  qb.MustExpr("COUNT(*)", "total")
  ```

  `ErrDangerousAlias` is DELETED rather than reworded — a sentinel named for dangerous characters
  cannot honestly report a grammar failure. Match `errors.Is(err, dbtypes.ErrInvalidAlias)`
  instead; it covers every rejection the old one did and the ones it missed.
- verify: `go vet ./...` first — not `go build ./...`, which does not compile `_test.go` files, and
  a test asserting `ErrDangerousAlias` is where this break most often lands. Then check the two
  doors SEPARATELY, because a rejected alias never reaches the second one: at CONSTRUCTION,
  `qb.Expr("1", "my alias")` returns `ErrInvalidAlias` immediately and `qb.MustExpr` panics, so
  there is no value left to render; at CONSUMPTION, build a struct literal
  (`dbtypes.RawExpression{SQL: "1", Alias: "my alias"}`) — which never passed through the
  constructor — and push it through `Select`, `GroupBy` and `OrderBy` on PostgreSQL AND Oracle,
  confirming each returns `ErrInvalidAlias` from `ToSQL()`. One door proving it is not proof for
  the others. Use a quoted alias (`"total"`) as a second case: the denylist accepted it and the
  grammar does not.
- ref: #1164 · [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) (2026-08-24
  addendum) · [C60.29] · `database/types/expression.go` · `database/internal/sqllex/identifier.go`

---

### [C61.10] a padded identifier renders trimmed · silent-behavior · when: match

- detect: `git grep -nE '(Select|From|OrderBy|GroupBy|Columns|SetMap|Insert|Update|Delete)\("(([[:space:]]|\\[nrt])[^"]*|[^"]*([[:space:]]|\\[nrt]))"' -- '*.go'`
  finds a padded LITERAL — note the POSIX class and the `\n`/`\r`/`\t` alternative: `git grep -E`
  is ERE, where `\s` matches nothing at all, and a padded identifier is as likely to be written
  as an escape as a literal space. That is the cheap first pass; the real shape is an identifier
  your code BUILDS rather than
  types — a name read from a config file, a CSV header, an HTTP parameter, or joined from
  fragments — where a stray space or newline can ride along. If every identifier in your code is
  a literal you typed, you are not affected.
- scope: the column, clause and table-name validators judged the TRIMMED identifier and returned
  only an error, so the doors that do not go through the column funnel rendered the caller's
  ORIGINAL padded string: `Insert().Columns("id\n")` emitted `INSERT INTO t (id\n)`, and
  `From(" users ")` / `OrderBy("id\n")` likewise carried the whitespace into the SQL. All three
  now return the normalized identifier and every caller renders THAT, matching the
  select-identifier validator, which has worked this way since ADR-082. No injection was
  possible either way — `TrimSpace` removes only whitespace — the emitted SQL was malformed-
  looking rather than dangerous. Filter and JoinFilter columns, UPDATE SET targets and every
  comparison helper were already unaffected: they funnel through `quoteColumnForQuery`, which
  trimmed before rendering. One door was NOT covered on this hop's first pass: `BuildUpsert`'s
  conflict, insert and update COLUMN keys go through a separate acceptance system that never
  normalized, so on PostgreSQL a padded key rendered verbatim inside the quoted identifier
  (`"  name  "`) while Oracle trimmed it. Its table argument was already normalized. `[C61.15]`
  closes that carve-out on the SAME hop — the keys are normalized too, under one acceptance rule
  per vendor — so a consumer crossing E61 sees both changes at once.
- gate: match = your code passes a computed or externally-sourced identifier to a builder door.
  no-match = every identifier is a literal in your source, or you never relied on the padding
  surviving.
- apply: nothing, unless something downstream READ the padded form back — a test asserting the
  emitted SQL byte-for-byte, a log-line matcher, or a query-fingerprint hash. Those see the
  trimmed spelling now. `cols.As` is the deliberate outlier and keeps its no-trim contract
  (ADR-082), so an alias' whitespace is still yours to manage.
- verify: `qb.Select("name").From(" users ").ToSQL()` renders byte-identically to the same call
  with `"users"` on both vendors; the same holds for a padded ORDER BY term, INSERT column and
  JOIN table. `BuildUpsert`'s column keys are covered by `[C61.15]` on this hop — verify them
  there, not here.
- ref: `database/internal/builder/identifiers.go` (`validateIdentifier`, `validateClauseIdentifier`,
  `validateTableName`) · `database/internal/builder/query_builder.go` (`validateIdentifiers`,
  `normalizedTableRef`) · issue #1158 · related ADR-082

---

### [C61.11] `jf.Eq` renders nil, slices and arrays like `f.Eq` · silent-behavior · when: match

- detect: first pass, the common spelling —
  `git grep -nE '(^|[^[:alnum:]_])jf[.](Eq|NotEq|Lt|Lte|Gt|Gte)[(]' -- '*.go'`. The boundary is
  spelled POSIX, not `\b`: `git grep -E` is ERE, where `\b` matches nothing, and the pattern would
  report no hits at all.
  That pass matches the RECEIVER NAME, so it misses every other spelling of the same call —
  `joinFilter.Eq(…)`, `jff.Eq(…)`, `qb.JoinFilter().Eq(…)`. For the complete sweep drop the
  receiver: `git grep -nE '[.](NotEq|Eq|Lte|Lt|Gte|Gt)[(]' -- '*.go'`, then keep only the hits
  whose receiver is a `JoinFilterFactory` — the Filter doors share all six names, so this pass
  over-reports by design and you filter it, rather than under-reporting silently. `assert.Equal`,
  `require.NotEqual` and any `Eqx`/`Ltd` are excluded by the trailing `[(]`, so the noise is
  `f.Eq`-shaped, not test-helper-shaped. If your editor has a type-aware search — gopls
  "Find References" on `JoinFilterFactory.Eq`, or `go vet`-style analysis — prefer it: it answers
  the receiver question that grep cannot.
  EVERY nil, slice or ARRAY operand is affected, literal or computed: `nil`, `[]int{1}`, `[]int{}`,
  `[3]int{1,2,3}`, a
  typed nil pointer `(*int)(nil)`, and a `driver.Valuer` reporting NULL — the equality doors now
  RENDER them and the ordering doors now ERROR on them, where both previously produced
  `col op ?`. A scalar operand keeps its SQL, `[]byte` included — but read the Valuers among them
  too: a `driver.Valuer` HOLDING a value keeps `col op ?` and changes its ARGUMENT, and a NIL
  POINTER to a Valuer type (`(*sql.NullString)(nil)`) stops PANICKING. Reading each call site
  matters more than the grep: the shape is decided by the operand's runtime value, so a
  parameter typed `any` tells you nothing until you know what reaches it.
- scope: the rule, once and precisely —
  A `dbtypes.RawExpression` operand is spliced before any of this — its SQL is emitted into the
  predicate verbatim, with no placeholder and no argument — so it is not resolved and not
  classified. For every other operand:
  After resolution — a `driver.Valuer` becomes its `Value()`, a pointer becomes its element, and
  a NIL pointer is nil before either is asked — the JoinFilter compare doors classify the
  operand as NIL (`nil`, a typed nil pointer, a Valuer reporting NULL), a LIST (a slice or an
  array), or a SCALAR (anything else, `[]byte` and a Valuer holding a value included); equality
  renders nil and list, ordering refuses nil and list, and a scalar is bound to `col op ?`
  whatever its Go type — a struct no driver accepts is passed through, not diagnosed.
  The rest of this clause is why it changed.
  The JoinFilter value doors built `col op ?` with exactly one placeholder, so a nil
  operand rendered `col = ?` with a NULL ARGUMENT — never true whatever the data, since SQL
  equality against NULL is UNKNOWN, and the text was always the placeholder, never a literal
  `col = NULL` — and a slice bound to a
  SINGLE argument, which the driver rejects or the vendor coerces. Equality now delegates to the
  same construct `f.Eq`/`f.NotEq` use: nil renders `IS NULL` / `IS NOT NULL`, a slice or array
  expands to
  `IN (…)` / `NOT IN (…)`, an empty slice or array renders the constant `(1=0)` / `(1=1)`. Two
  shapes that
  could not work now work, and nothing that worked emits different SQL. A SCALAR operand keeps its
  SQL, including a `[]byte`, which counts as a scalar and not a list — squirrel's own rule, shared
  here so the two doors cannot disagree about what a list is. One scalar's ARGUMENT does move: a `driver.Valuer`
  holding a value binds the RESOLVED value (`int64(5)`), not the wrapper (`sql.NullInt64{5,true}`),
  at all six compare doors (`Eq`, `NotEq`, `Lt`, `Lte`, `Gt`, `Gte`); `jf.In`, `jf.NotIn`,
  `jf.Like` and `jf.Between` never resolved an operand and are untouched. The SQL is unchanged;
  the argument list is not. The driver receives the same value either way — it is the value `database/sql`
  would have unwrapped at bind time, and the one `f.Eq` has always bound — so only something
  reading `ToSQL`'s args back, a golden file or a contract test, can see it.
  "Nil" is decided after the same resolution the underlying builder performs: a `driver.Valuer`
  reporting NULL (`sql.NullString{}`) and a typed nil pointer (`(*int)(nil)`) are nil at both
  EQUALITY doors, `f.Eq` and `jf.Eq` alike, which is what an optional column read from a nullable
  field usually is. The ordering doors are where the two diverge, immediately below.
  The JOINFILTER ORDERING doors (`<`, `<=`, `>`, `>=`) now REFUSE a nil, slice or array operand with
  `dbtypes.ErrOrderingOperandNotComparable` — exported from `database/types`, so consumer code can
  match it — surfaced through the JoinFilter error channel at `ToSQL`. The FILTER ordering doors
  are unchanged and are NOT the same rule: they delegate to the underlying builder, which refuses
  nil, slices and a Valuer reporting NULL with its own messages, but does not dereference a
  pointer — so `f.Lt(col, (*int)(nil))` still renders `col < ?` where `jf.Lt` now errors. That
  divergence is tracked in #1205, not closed here. A second one opens on the same seam: a NIL
  POINTER to a Valuer type — `(*sql.NullString)(nil)`, an optional column read into a pointer —
  satisfies `driver.Valuer` through the value receiver, so asking it for its value dereferences
  nil. squirrel asserts the interface before it tests the pointer and PANICS (expr.go:168), and
  `jf` used to panic with it; `jf` now settles the nil pointer FIRST and renders `IS NULL`, so
  `f.Eq(col, (*sql.NullString)(nil))` still panics where `jf.Eq` no longer does. `jf.In`,
  `jf.NotIn` and `jf.Between` hand the operand to squirrel untouched and panic too; tracked in
  #1209, not closed here.
  JoinFilter refuses because IT has no rendering
  for those three — the `col op ?` it used to emit for them silently matched nothing, and the error
  replaces that. Do not read the absence as universal: Filter renders what #1205 documents.
- gate: classify every operand with the scope clause's rule, then:
  match = a `jf.Eq`/`jf.NotEq` call whose operand can be any of the NON-SCALAR forms (it
  starts working), or a `jf.Lt`/`Lte`/`Gt`/`Gte` call whose operand can be one of them (it starts
  erroring). Those forms are: `nil`; a slice; an ARRAY (`[3]int{1,2,3}` is a list operand exactly
  as `[]int{1,2,3}` is); a typed nil pointer (`(*int)(nil)`); and a `driver.Valuer` reporting NULL
  (`sql.NullString{}`) — the last two because the JoinFilter doors resolve the operand before
  classifying it.
  A `driver.Valuer` HOLDING a value matches too, at ALL six doors, but for the argument only: the
  SQL stays `col op ?` and the bound value becomes the resolved one.
  no-match = every JoinFilter operand is a scalar and none of them is a `driver.Valuer`; a `[]byte`
  is such a scalar.
- apply: for equality, nothing — the new rendering is what the call always meant; if you worked
  around it by spelling `jf.In`/`jf.Null` explicitly, that spelling is still the clearer one and
  needs no change. For ordering, handle the error: a nil operand there was a bug that returned no
  rows, so the call site needs to decide what it meant. For a Valuer holding a value, nothing at
  the call site — but retire any golden file or contract test pinning `ToSQL`'s args to the
  wrapper, which now reads as the resolved value. Read every nil guard around a `jf` call before
  removing it, and remove ONLY the ones that existed to dodge this door's defects — a crash on a
  nil pointer to a Valuer type, or a `col = ?` bound to NULL that matched nothing. A guard that
  deliberately OMITS the predicate when the operand is absent is a different thing and must STAY:
  dropping it changes `WHERE` with no condition into `WHERE col IS NULL`, which is a narrower
  query, not the same one. An unguarded nil operand now MEANS `IS NULL`, so the guard is the only
  thing left expressing "no predicate at all". And if the same operand also reaches `f`, `jf.In`,
  `jf.NotIn` or `jf.Between`, keep the guard regardless — those still panic (#1209).
- verify: `jf.Eq("u.id", nil)` and `jf.Eq("u.id", sql.NullString{})` both render `u.id IS NULL`;
  `jf.Eq("u.id", [3]int{1,2,3})` renders `u.id IN (?,?,?)`; `jf.Eq("u.id", []int{1,2})` renders
  `u.id IN (?,?)` with two arguments; `jf.Lt("u.id", nil)` makes `ToSQL` return an error matching
  `errors.Is(err, dbtypes.ErrOrderingOperandNotComparable)`; and `jf.Eq("u.id", sql.NullInt64{Int64: 5, Valid: true})`
  renders `u.id = ?` with args `[]any{int64(5)}`, not the `sql.NullInt64` wrapper — print the args,
  not just the SQL, or this one is invisible; and `jf.Eq("u.id", (*sql.NullString)(nil))` renders
  `u.id IS NULL` instead of panicking, while the same operand at `f.Eq` still panics — assert on
  the panic, not only on the SQL; and `expr, _ := qb.Expr("NOW()"); jf.Eq("u.ts", expr)` renders
  `u.ts = NOW()` with NO argument — a RawExpression is spliced before resolution, so none of the
  classification above applies to it. Note `jf.Between` reports squirrel's
  own message for a nil bound rather than this sentinel — it never built `col op ?` and is
  untouched here.
- ref: `database/internal/builder/join_filter.go` (`compare`, `resolveOperand`) ·
  `database/types/errors.go` (`ErrOrderingOperandNotComparable`) · issue #1167 ·
  scalar inequality still differs between the doors (#1200)

---

### [C61.12] a panic outside `Recover` answers 500 instead of dropping the connection · silent-behavior · when: match

- detect: nothing in your Go code names this — the change is in what the framework does with a
  panic your code already has. Ask the question at the two ends instead.
  In your LOGS: search your standard-output/stderr stream, NOT your structured log stream, for
  `http: panic serving` — net/http's own renderer, which no framework field ever appears beside.
  Every hit is a request that took this path, and the line carries the panic VALUE, so treat the
  hits as a leak to triage as well as a count.
  In your CLIENTS: an unexplained `EOF`, `connection reset by peer` or an empty 502 from your own
  gateway, on a route whose access log shows NO line at all for that request, is CONSISTENT with
  this event seen from the other side. Treat it as a filter, not as a finding. The missing
  access-log line only rules out the ordinary case — a normal 500 has one — and does not rule in a
  panic: a client that hung up mid-request, and a gateway that timed out and synthesized its own
  502, both produce the same three symptoms with no access-log line, because in every one of these
  the handler never returned to write one.
  What CLASSIFIES a hit is the stderr side above: correlate the request by time and route with an
  `http: panic serving` line, or reproduce it by registering a middleware that panics ahead of the
  framework's `Recover`. Without one of those two, an `EOF` is a candidate to investigate and
  nothing more.
  For the code half, read your middlewares registered before the framework's `Recover` — that is
  a consumer-supplied `multitenant.TenantResolver` (`ResolveTenant` is yours) and anything a
  `RouteRegisterer`/global middleware calls during tenant resolution or request enrichment — and
  ask whether they can panic: a nil map write, a type assertion without the comma-ok, an index on
  a header-derived slice.
- scope: the rule, once and precisely —
  An outermost recover is registered as the FIRST Echo middleware, so every other middleware runs
  inside it. On a panic it logs ONE ERROR line — message `Panic recovered`, the same one the error
  handler already uses for a panic caught downstream, so an alert keyed on it catches both sides —
  carrying `panic_type` (the value's `%T`), `request_id`, `method` and `path`, never the value,
  which is consumer-chosen and therefore beyond the log filter's field-name matching (ADR-081). It
  answers with the standard error envelope at 500. `request_id` is empty when the panic happened
  before the request-id middleware, which runs INSIDE this guard.
  `http.ErrAbortHandler` is re-panicked unchanged, by IDENTITY rather than `errors.Is`, so
  net/http's abort contract still drops that connection: the sentinel carries no data and a
  wrapped one would hand the wrapper's payload to net/http's renderer.
  What this REPLACES is the previous outcome, which was not an error path at all: the panic
  unwound past Echo into net/http, which printed `http: panic serving <addr>: <value>` and a stack
  to the standard logger and closed the connection. So three things change together — the caller
  gets a 500 envelope instead of a dropped connection, your structured log gains an ERROR line
  where it previously had none, and your stdout/stderr loses the net/http line that carried the
  value.
  Panics DOWNSTREAM of `Recover` are untouched: handler panics, and anything in the middlewares
  registered after it, still go through `Recover` + `sanitizePanicValue` exactly as before, with
  the same `*server.panicTypeError`, the same stack capture and the same `Panic recovered` line.
  The span is deliberately untouched too. A pre-`Recover` panic ends its span without an error
  status, because the OTel middleware itself is one of the middlewares running inside this guard —
  reaching around it from outside would mean re-implementing what it does. A panicking tenant
  resolver therefore shows as a span with no error status and a 500 the span did not record.
  `http.Server.ErrorLog` is NOT what changed, and wiring it would not have worked: net/http
  formats the value into the message before any adapter sees it.
  The reporting call is itself contained: a consumer `logger.Logger` that panics while the guard
  is writing its line would otherwise unwind past Echo into net/http with the LOGGER's panic value
  — the same failure, one layer up — so the guard recovers around it and still answers 500
  (ADR-079 guarded the framework's other two panic-reporting calls for this reason).
- gate: match = your stdout/stderr carries `http: panic serving`, OR a client sees EOF on a route
  with no access-log line, OR any middleware you supply that runs before `Recover` — a
  `multitenant.TenantResolver` is the common one — can panic.
  no-match = no `http: panic serving` anywhere in your logs and no consumer-supplied pre-`Recover`
  middleware. Nothing changes for you; the guard is a path your deployment never reaches.
- apply: nothing is required — the new outcome is strictly better than a dropped connection. Two
  things are worth doing anyway. Any alert or dashboard keyed on the CONNECTION symptom (an EOF
  rate, a gateway 502 count) now sees 500s instead, so repoint it at the status code and at the
  new ERROR line, whose `panic_type` field is the searchable signal. And fix the panics the detect
  step just surfaced: the guard turns them into a clean 500, it does not make them correct, and
  the request still fails.
- verify: point a route at a consumer middleware that panics before `Recover` — a
  `multitenant.TenantResolver` whose `ResolveTenant` panics with a marker string is the shape the
  issue used — and request it over a real listener, not an in-process handler call, since it is
  net/http that used to print the line. Expect a 500 whose body is the standard `{"error":…}`
  envelope, an ERROR line reading `Panic recovered` with `panic_type: string` and the marker
  NOWHERE in it, and no
  `http: panic serving` on stdout or stderr. Then panic with `http.ErrAbortHandler` from the same
  middleware and confirm the connection still drops with no response and no log line — that
  contract is preserved, and a verification that only checks the 500 cannot see it.
- ref: [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) (amended 2026-08-28) ·
  `server/panic_guard.go` (`outermostRecoverEcho`) · `server/middleware.go` (`SetupMiddlewares`,
  first registration) · `wiki/global_middleware.md` (chain diagram) · issue #1144

### [C61.13] `LogEvent.Err` masks an error field the operator marked sensitive · silent-behavior · when: match

- detect: read the whole needle list at BOTH doors, not a fixed window around the key — a list
  longer than the context you print hides its own tail, and the needle you are looking for is as
  likely to be last as first. YAML door: `yq '.log.sensitivefields[]' config*.yaml` (or
  `yq '.. | select(has("sensitivefields")).sensitivefields[]'` when the block is nested), piped
  through `grep -i -E 'err|rror'`. Code door: `git grep -n 'LoggerFilterConfig' -- '*.go'` and read
  the `SensitiveFields` slice each hit builds, including entries appended in a loop or read from
  another source. Match SUBSTRING-wise and case-insensitively, not by equality — `error` itself
  fires, and so do `err`, `Err` and `rror`.
- scope: `LogEvent.Err(err)` wrote its message through zerolog's own `Err`, which applies no
  field-name masking, so a deployment that named `error` in `log.sensitivefields` had the message
  rendered in clear at every framework `Err` site while the same value under a `Str("error", …)`
  call was masked. The two doors now agree: `Err` applies the needle, and where both a needle and
  a `FilterConfig.ErrorRedactor` are configured the MASK wins, the redactor's output being a value
  under a field name the operator called sensitive. Nothing changes for a default configuration —
  `error` is not a default needle and none of the defaults substring-matches it, which the logger
  suite now pins — so this fires only for a deployment that asked for it.
- gate: match = your effective needle list contains an entry that substring-matches `error`.
  no-match = it does not, and every `Err` line renders exactly as before, byte for byte.
- apply: nothing to change. Expect the mask value (`***` unless you set `MaskValue`) where those
  lines used to carry the message — including framework lines you do not author: handler failures,
  job failures, message-handler failures, and the server's 5xx detail under `app.debug`.
- verify: force one framework error (an unhandled 5xx under `app.debug` is the easiest) and read
  its `error` field in each of the three configurations SEPARATELY, because they are three
  different values and only the pair distinguishes this change from a redactor someone left on:
  needle set and NO redactor → the mask value (`***` unless you set `MaskValue`), raw message
  absent from the line; redactor set and NO needle → the redactor's own output, which is not the
  raw message either; neither set → the raw message, byte-identical to before this hop. Note what
  the middle case means for a rollback check: dropping the needle does NOT restore the raw text
  while a redactor stays configured, so testing only "remove the needle, is it back?" reports a
  failure that is not there.
- ref: issue #1182 · `logger/adapter.go` (`LogEventAdapter.Err`) · `logger/filter.go` (`maskField`)

### [C61.14] a caller-set `traceparent` is validated before it is re-emitted · silent-behavior · when: match

- detect: your Go code shows you only half of this, because the other half is data already
  in your database. For the code half —
  `git grep -nE '(^|[^[:alnum:]_])(traceparent|HeaderTraceParent|tracestate|HeaderTraceState)' -- '*.go'`
  — and read every
  hit that WRITES rather than reads: a `PublishOptions.Headers` map literal, a `map[string]any`
  handed to `outbox.Publish`, or a direct `trace.InjectIntoHeaders` call on an accessor your own
  code pre-populated. A hit that only reads a traceparent is unaffected.
  The state keys are in the search because a `tracestate` is affected on its OWN: the scope clause
  below removes one that sits beside a `traceparent` this hop refuses, AND one set with no
  `traceparent` at all, whose state annotates a parent this hop never saw. Code that sets only a
  `tracestate` therefore moves, and a `traceparent`-only grep never sees it. The boundary is spelled
  POSIX, not `\b`: `git grep -E` is ERE, where `\b` matches nothing and the pattern reports no
  hits at all.
  For the data half, query your outbox table for rows whose persisted headers carry a traceparent
  `ValidateTraceParent` refuses. That rule is FIVE clauses, not one regex: dash-delimited
  LOWERCASE hex in the version-00 positions (two version digits, a 32-digit trace-id, a
  16-digit parent-id, two flag digits); a FUTURE version (01..fe) may carry further
  dash-delimited printable-ASCII fields after those, and is valid; version `ff` is forbidden;
  version `00` must be exactly 55 characters; the whole value is at most 255 bytes; and neither
  id may be all zeros. A version-00-only pattern gets two of them wrong in opposite directions —
  it reports a legitimate future-version value as poisoned, and passes an all-zero or `ff` value
  the validator refuses. On PostgreSQL, with `headers` stored as JSON:

  ```sql
  SELECT id FROM outbox_events, LATERAL (SELECT headers::jsonb ->> 'traceparent' AS tp) t
  WHERE t.tp IS NOT NULL
    AND ( t.tp !~ '^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}(-[!-~]+)*$'
       OR length(t.tp) > 255
       OR left(t.tp, 2) = 'ff'
       OR (left(t.tp, 2) = '00' AND length(t.tp) <> 55)
       OR substring(t.tp from 4 for 32) = repeat('0', 32)
       OR substring(t.tp from 37 for 16) = repeat('0', 16) );
  ```

  (`[!-~]` is the POSIX-class-free spelling of printable non-space ASCII, the charset the
  validator's own pattern uses for a future version's extra fields.)
  That query finds rows WITH a traceparent, so it cannot see the other carrier this hop removes: a
  persisted `tracestate` with no `traceparent` beside it. Ask for those separately, by KEY PRESENCE
  rather than by value, because the scope clause decides on presence — a `traceparent` key holding
  an empty string counts as present and is NOT standalone:

  ```sql
  SELECT id FROM outbox_events
  WHERE headers::jsonb ? 'tracestate'
    AND NOT (headers::jsonb ? 'traceparent');
  ```

  (`?` is jsonb's key-existence operator; in a driver that treats `?` as a bind placeholder, use
  `jsonb_exists(headers::jsonb, 'tracestate')` instead.) Those rows re-emitted a state annotating a
  parent this hop never saw, and stop after it.
  Those rows are the second carrier, and they re-emitted their value on every relay cycle until
  this hop. A row whose traceparent is well-formed keeps its precedence, and an adjacent
  `tracestate` is preserved instead of being overwritten by the context's.
  Your LOG or TRACING backend answers the same question from the other end: a downstream service
  logging a `traceparent` field that fails that shape was handed it by this framework's publish
  path, and stops being handed it here.
- scope: the rule, once and precisely —
  `trace.InjectIntoHeaders` is the only writer, and its PRECEDENCE is unchanged: a `traceparent`
  already in the header map still outranks the context's parent, which still outranks a generated
  one. What changed is that the pre-set value must survive `ValidateTraceParent` — the same
  function every ingress door in `[C60.17]` uses, so the emit side cannot drift from them — to keep
  that rank. One that does not is discarded, and the context's parent, or a freshly generated
  parent, is emitted in its place. The discard is SILENT: the `trace` package has no logger and
  gains none.
  A `tracestate` sitting beside a `traceparent` that does not ship does not ship either. That
  covers a REFUSED pre-set value — decided by PRESENCE, so a header map carrying the `traceparent`
  key with an empty value counts — and a header map carrying a `tracestate` with NO `traceparent`
  at all, whose state annotates a parent this hop never saw. The removal is written only where a
  value is actually displaced, so an ordinary publish still emits no `tracestate` header. It is overwritten with the
  context's own state, or emptied when the context carries none. This is ADR-070's carrier scoping
  read on the outbound carrier: vendor state annotates ONE parent, so re-emitting it under the
  parent that replaced it attaches it to a trace it never belonged to. The same rule now protects
  the other direction, which is a SECOND change on this line: a `tracestate` beside an ACCEPTED
  pre-set `traceparent` is kept AFTER VALIDATION, where the context's state used to overwrite it
  unconditionally. Kept means it outranks the context's state, not that it is exempt from the
  rule: it goes through the same `ValidateTraceState` every other door applies — the cap plus a
  control-byte refusal, no grammar — so a valid carried state ships unchanged and one carrying
  CR/LF or NUL is emptied. The carrier reaches this path from a caller's header map AND from a
  persisted outbox row the relay replays, so neither source is taken on trust. Both halves say the same thing — the state that ships is the one written beside
  the parent that ships.
  The outbound `X-Request-ID` is derived from whatever traceparent wins, and that derivation now
  requires the trace-id field to be 32 LOWERCASE HEX digits rather than 32 bytes of anything. It
  reuses the same validator and field parser, not a second pattern. When the derivation fails the
  id falls back to the context's own trace id, unchanged from before.
  Two things are deliberately NOT in scope. The CONTEXT's parent is still taken as-is: `WithTraceParent`
  is exported first-party API, not caller input, and the publish-side `CorrelationId` guard
  (`[C60.17]`) remains the defense in depth for what it can carry — which is why a poisoned context
  parent still yields an empty `CorrelationId` rather than a poisoned one. And `ValidateRequestID`'s
  charset is unchanged; widening it was refused in ADR-070 and is refused here.
- gate: match = your code sets a `traceparent` header itself — a `PublishOptions.Headers` entry,
  an `outbox.Publish` headers map, or a `trace.InjectIntoHeaders` call on an accessor you populated
  — AND the value it can carry is not always spec-exact; or your outbox table holds rows whose
  persisted `traceparent` fails the detect query; or you set a `tracestate` beside a `traceparent`
  from either of those places; or you set a `tracestate` with NO `traceparent` at all, in code or in
  a persisted row, which is a match ON ITS OWN — that state annotates a parent this hop never saw,
  so it is removed whether or not a `traceparent` was ever involved.
  no-match = every `traceparent` your code emits comes from this framework (the overwhelmingly
  common case — the publish path injects one for you), and the detect query returns no rows.
  Nothing changes for you: a well-formed pre-set value still wins, and a generated one is generated
  as before.
- apply: for a malformed value your code plants deliberately — a fixture, a synthetic id, a
  correlation scheme of your own — stop planting it in the `traceparent` header, because it no
  longer propagates. Carry it in a header of your own naming, or make it spec-exact. For the outbox
  rows, nothing: the backlog drains and each row republishes under whatever parent the relay
  context carries — a generated one where it carries none, which is the common case, since the
  relay rehydrates that context from the SAME persisted headers and the malformed value is
  discarded there too. Either way the row stops re-emitting the poisoned value, which never
  achieved anything anyway — the id it produced was refused downstream.
  For a `tracestate` you set beside your own `traceparent`, fix the `traceparent` first; the state
  rides on it and is dropped with it.
  For a `tracestate` you set with NO `traceparent`, there is nothing to fix first and the state is
  simply dropped: it described a parent this hop never emitted. If the vendor data matters, set it
  beside a spec-exact `traceparent` you also own, so the pair ships together; if it was there to
  carry something of your own, move that to a header of your own naming. For persisted rows the
  answer is again nothing — the backlog drains and each row republishes with the relay context's
  state, or none.
- verify: publish with `PublishOptions{Headers: map[string]any{"traceparent": "00-" + strings.Repeat("!", 32) + "-1234567890123456-01", "tracestate": "vendor=x"}}` and read the
  message the broker delivers: `traceparent` is a well-formed value that is NOT the one you set,
  `tracestate` is absent or empty, and `CorrelationId` is non-empty and equal to the `X-Request-ID`
  header — before this hop the poisoned value went out verbatim and `CorrelationId` was empty.
  Then publish the SAME way with a spec-exact `traceparent` of your own and confirm BOTH headers
  are delivered unchanged — the `traceparent` you set, and the `tracestate` still exactly
  `vendor=x`. Precedence is what did not move, and a verification that only checks the rejection
  cannot see that; the state assertion is the one that catches the SECOND change on this line,
  since the accepted-parent path is where the scope clause stops the context's state from
  overwriting yours, and a drop or an overwrite there looks identical to a pass if only the
  `traceparent` is read.
  Finally publish with a `tracestate` and NO `traceparent` at all, and confirm the delivered message
  carries a generated `traceparent` and an absent or empty `tracestate` — the standalone carrier,
  which neither of the two cases above exercises.
- ref: [ADR-070](adr_070_inbound_trace_identifier_validation.md) (amended 2026-08-28) ·
  [C60.17] (the ingress doors this completes) · [C60.10] ·
  `trace/trace.go` (`InjectIntoHeaders`, `computeTraceParent`, `extractTraceIDFromParent`) ·
  `trace/validate.go` (`ValidateTraceParent`, `splitTraceParent`) · issue #1121

---

### [C61.15] `BuildUpsert` column keys answer to ONE acceptance rule, normalized · breaking · when: match

- detect: `git grep -nE '(^|[^[:alnum:]_])BuildUpsert[(]' -- '*.go'` — the boundary is spelled
  POSIX, not `\b`, because `git grep -E` is ERE and `\b` matches nothing there. Read the
  `conflictColumns` slice and the KEYS of both column maps at every hit. Only the keys are
  affected; values are bound parameters and are untouched.
- scope: the conflict, insert and update keys went through an acceptance system separate from the
  identifier validators, and that system applied a different rule per vendor. Oracle refused a
  key its MERGE cannot name — qualified (`t.name`), function-shaped (`COUNT(*)`), empty — while
  PostgreSQL tested only for an unescaped quote and rendered the rest, so `COUNT(*)` built
  `ON CONFLICT ("COUNT(*)")` there and errored on Oracle (#1187). The two disagreed in the other
  direction too: a key spelling an interior quote as a doubled one (`a""b`, `"a""b"`) was accepted
  on Oracle and refused on PostgreSQL. And the keys were never NORMALIZED, so a padded one
  rendered verbatim on PostgreSQL (`INSERT INTO users ("  name  ", …)`) while Oracle trimmed it,
  a padded CONFLICT key failed with a confusing `must be present in insert columns` on
  PostgreSQL, and `{"id": 1, " id ": 2}` passed distinctness there and rendered
  `INSERT INTO users ("id","id")` — invalid at execution (#1196).
  One rule now decides, on BOTH vendors: the key is trimmed, then must be a single column name —
  no qualifier, no function call, no empty name — carrying no quote of its own beyond a plain
  wrapping pair. Read that last clause precisely: the DOUBLED quote is what is refused, and
  well-formed caller quoting is NOT. A key wrapped in quotes with no interior quote (`"ID"`,
  `"level"`, `"count(*)"`) keeps working exactly as before, including ADR-071's identity rule that
  makes `"ID"` and `id` one Oracle column — the narrowing is the escape, not the quotes. The trimmed spelling is what the statement renders, and identity — what
  membership and distinctness compare — is the RENDERED identifier on both vendors, which is the
  normalized-return contract `[C61.10]` gave the other identifier doors. On PostgreSQL that means
  `ID` and `"ID"` are ONE column, since `EscapeIdentifier` renders both as `"ID"`, while `id` and
  `ID` stay two — case survives the quoting. So PostgreSQL now refuses what only Oracle refused, both vendors refuse the
  doubled-quote key, `" id "` matches its `id` insert key, and `{"id", " id "}` is rejected naming
  both spellings. One clause WIDENS, on PostgreSQL only: its old rule refused a key carrying any
  quote at all, wrapper included, so `"ID"` and `"count(*)"` were refused there while Oracle took
  them. Both vendors take them now — the wrapper is how a caller states a name the bare grammar
  cannot spell — and the bound is the narrowing itself: a quoted key may hold no interior quote, so
  nothing inside it can end the identifier early. Nothing here was an injection: every key was quote-wrapped and escaped in the
  emitted SQL both before and after (ADR-082). The defect was that one door spelled a column
  differently from every other door, and differently per vendor.
- gate: match = any `BuildUpsert` key in your code is qualified, function-shaped, carries a quote,
  or can arrive PADDED because it is computed rather than typed (read from config, a CSV header,
  an HTTP parameter, joined from fragments). no-match = every key is a plain typed column name.
  A PostgreSQL call that ERRORED on a caller-quoted key now builds instead; that needs no action,
  but a test asserting the old rejection starts failing.
- apply: rewrite the key. Note there is no expression-key hatch at this door —
  `conflictColumns` is `[]string` and both column maps are keyed by string — so a
  schema this rule cannot spell needs a hand-written statement (`database.Raw`,
  with its `// SECURITY: Manual SQL review completed` annotation):

  ```go
  // before — built on PostgreSQL, errored on Oracle
  qb.BuildUpsert("t", []string{"COUNT(*)"}, map[string]any{"COUNT(*)": 1, "id": 2}, nil)
  // after — quote a column that is genuinely NAMED count(*); otherwise name a real column
  qb.BuildUpsert("t", []string{`"count(*)"`}, map[string]any{`"count(*)"`: 1, "id": 2}, nil)

  // before — accepted on Oracle only
  map[string]any{`a""b`: 2}
  // after — an identifier argument carries no quoting; the door quotes. A name
  // holding a quote has no key form here: hand-write the statement.

  // before — dotted key, accepted on PostgreSQL only
  map[string]any{"t.name": 2}
  // after
  map[string]any{"name": 2}
  ```

  A padded key needs no edit — it now names the column it always meant — UNLESS you relied on the
  padding surviving into the SQL, or your map holds both spellings, which is now rejected rather
  than silently rendered twice.
- verify: run one `BuildUpsert` per shape on BOTH vendors and confirm the verdicts MATCH, since
  every defect here was a vendor disagreement and one vendor proving it is not proof for the
  other: `COUNT(*)`, `t.name` and `a""b` each return
  `… column %q is not a single column name for upsert`; `"count(*)"` still builds; and
  `qb.BuildUpsert("users", []string{"  id  "}, map[string]any{"id": 1, "  name  ": "x"}, nil)`
  builds on both, with the PostgreSQL SQL reading `("id","name")` and `ON CONFLICT ("id")` — no
  padded identifier anywhere in it. Then re-record any golden file or contract test pinning
  PostgreSQL upsert SQL built from a computed key.
- ref: issues #1187 · #1196 · [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md)
  (2026-08-28 amendment) · [C61.10] (this closes its carve-out) ·
  `database/internal/builder/helpers.go` (`normalizeUpsertColumns`,
  `isAcceptableUpsertColumnKey`, `keyCarriesInteriorQuote`) ·
  `database/internal/builder/oracle.go` (`BuildUpsert`)

### [C61.16] `f`'s nine value doors and both families' `In`/`NotIn`/`Between` resolve the operand nil-first · silent-behavior · when: match

- detect: the operands, not the doors — this atom moves shapes, and nine doors of each family now
  share one rule: `Eq`, `NotEq`, `Lt`, `Lte`, `Gt`, `Gte`, `Between`, `In` and `NotIn`. `Like` is
  NOT among them — it takes a string pattern, so there is no operand to resolve. Sweep the call sites first:
  `git grep -nE '[.](Eq|NotEq|Lt|Lte|Gt|Gte|In|NotIn|Between)[(]' -- '*.go'`, keeping the hits whose
  receiver is a `FilterFactory` or a `JoinFilterFactory` (the boundary is spelled POSIX, not `\b`:
  `git grep -E` is ERE, where `\b` matches nothing). Then read each operand for the shapes that move.
  Four reach a door directly: a typed nil pointer (`(*int)(nil)`), a NIL POINTER to a
  `driver.Valuer` type (`(*sql.NullString)(nil)`), untyped `nil` or a slice at an ORDERING or
  `Between` door, and a `driver.Valuer` reporting NULL. A parameter typed `any` or a pointer field
  read from a nullable column tells you nothing until you know what reaches it — which is exactly
  where these operands come from.
  Four more shapes move, and they are NOT confined to the list doors — read them at every call
  site, then read the ELEMENT TYPE of each slice you pass `In`/`NotIn` as well, since an element
  is invisible at the surface:
  an ARRAY, which is a list operand exactly as a slice is — equality expands it, ordering and
  `Between` refuse it, `In`/`NotIn` expand it;
  a POINTER to a slice, which resolves to the slice at every door — so equality expands it and
  ordering refuses it, where the list doors used to bind it as ONE argument holding the whole
  slice and now expand it to one argument per element;
  a `[]byte`, which stays a SCALAR at the compare and `Between` doors (squirrel's own rule, so
  `col = ?` is unchanged there) while its `In`/`NotIn` rendering moves from `= ?` / `<> ?` to
  `IN (?)` / `NOT IN (?)`;
  and a nil-pointer element inside a list — typed, or pointing at a `driver.Valuer` — which is
  the one of the four that only the list doors can see.
- scope: `[C61.11]` gave the six `jf` compare doors one resolution — nil pointer settled FIRST, then
  `driver.Valuer`, then pointer dereference — and left the `f` family and `jf.In`/`jf.NotIn`/
  `jf.Between` delegating to squirrel, which runs the same steps in the OTHER order: it asserts
  `driver.Valuer` before it tests the pointer. That order is what made a nil pointer to a Valuer
  type fatal — `sql.NullString` declares `Value()` on a VALUE receiver, so `*sql.NullString`
  satisfies the interface too and asking a nil one dereferences nil. This atom gives every
  remaining door the `[C61.11]` resolution, so ONE operand means ONE thing everywhere. Four shapes
  move:
  a NIL POINTER to a Valuer type stops PANICKING inside `ToSQL` at `f.Eq`, `f.NotEq`, `f.Lt`,
  `f.Lte`, `f.Gt`, `f.Gte`, `f.Between` and `jf.Between` — `jf.Between`'s MIXED shape included,
  where one bound is a `qb.Expr()` RawExpression and the other a value: that value went into a
  `squirrel.Expr`, which resolves nothing at build time, so it rendered `col <= ?` bound to the
  pointer and was dereferenced at EXEC instead — equality now renders `IS NULL` /
  `IS NOT NULL`, ordering and both `Between` bounds now return
  `dbtypes.ErrOrderingOperandNotComparable` through the deferred-error channel `ToSQL` already has;
  a POINTER CHAIN is now walked to its end rather than one level deep, at both families: a `**int`
  or `**sql.NullString` whose INNER pointer is nil was left as a typed nil `*int` — neither `== nil`
  nor a list — and classified a SCALAR, so equality rendered `col = ?` bound to NULL (squirrel
  unwrapped the last level at render time, so it reached the driver as NULL and matched nothing) and
  ordering bound the nil pointer to `col < ?`, while a `**sql.NullString` panicked at the Valuer
  assertion; equality now renders `IS NULL` and ordering returns the sentinel, the same as for a
  single-level nil pointer;
  a TYPED NIL POINTER at an `f` ordering door stops rendering `col < ?` bound to a nil pointer —
  the silent-no-rows shape, since the driver sends it as NULL and `col < NULL` is never true — and
  returns the sentinel instead;
  untyped `nil`, a slice, an array and a Valuer reporting NULL at an `f` ordering door or at either
  `Between` return that SAME sentinel rather than squirrel's own text, so `errors.Is(err,
  dbtypes.ErrOrderingOperandNotComparable)` now works at both families instead of one;
  a CYCLIC pointer operand — `var v any; v = &v`, or any chain that returns to a pointer already
  followed — now surfaces an operand-resolution error at `ToSQL` instead of spinning the walk
  forever on the calling goroutine (the chain does not have to shorten: an `any` holding a pointer
  to itself reintroduces the pointer at every level);
  and every one of these doors BINDS THE RESOLVED VALUE — `int64(5)`, not `sql.NullInt64{5,true}`;
  the element, not the pointer — extending to `f` and to `In`/`NotIn`/`Between` what `[C61.11]`
  did for the six `jf` compare doors.
  `In` and `NotIn` never panicked at build: they bind list elements untouched, so a nil pointer to
  a Valuer survived to EXEC and crashed there, in `database/sql`, one layer below the door that
  accepted it — and a nil element rendered `IN (NULL)`, matching nothing, with nothing said. Both
  families now resolve every ELEMENT, so the crash cannot reach exec and the NULL is spelled by the
  door. TWO SQL texts move at those two doors. A `[]byte` operand renders
  `IN (?)` / `NOT IN (?)` where it rendered `col = ?` / `col <> ?` before: a `[]byte` is a
  `driver.Value`, so squirrel classified it as a SCALAR and collapsed the list — while the door's own
  contract has always been that a scalar is wrapped in a ONE-element list, which is what it now is.
  The argument is unchanged (one `[]byte`, not one placeholder per byte) and so is the meaning;
  `IN` with one element and `=` select the same rows.
  A POINTER to a slice moves the more disruptive of the two: `&[]int{1,2}` renders `IN (?,?)` where
  it rendered a single `IN (?)` bound to the whole slice, so the PLACEHOLDER COUNT changes rather
  than an operator and the argument list goes from one element to one per member. The apply clause
  names both together, where the re-recording instruction lives.
  What does NOT move: a SCALAR operand's SQL at every COMPARE door, `[]byte` included — it is a
  `driver.Value`, so it stays a scalar there and does not become an `IN` list; `f` equality on untyped
  nil, a slice, an array, a typed nil pointer or a NULL Valuer, which squirrel already resolved and
  which already rendered `IS NULL` / `IN (…)`; `Like`; and `jf.NotEq`'s `!=` against `f.NotEq`'s
  `<>` for a scalar, which is still the one remaining divergence between the families (#1200).
- gate: match = any door in the detect sweep can receive one of the four moved shapes. Concretely:
  a nil pointer to a `driver.Valuer` type anywhere (it stopped crashing — at `ToSQL` for the
  compare and `Between` doors, at exec for `In`/`NotIn`); a MULTI-LEVEL pointer (`**int`,
  `**sql.NullString`) whose inner pointer can be nil, at any door — an optional field held behind
  two pointers, or a `*T` passed into an `any` parameter that is itself taken by pointer; a typed nil pointer, untyped `nil`, a
  slice, an array or a NULL-reporting Valuer at `f.Lt`/`Lte`/`Gt`/`Gte` or at either family's
  `Between` (they start erroring, and the error TEXT changes even where one was already returned);
  a `[]byte` operand at either family's `In`/`NotIn` (its SQL becomes `IN (?)` / `NOT IN (?)`);
  a POINTER TO A SLICE at `In`/`NotIn` (`&[]int{1,2}`), which resolved to the slice and was then
  bound as ONE argument holding the whole slice — squirrel expanded only the outer list, so the
  statement carried `IN (?)` and the driver rejected the argument; it now expands to `IN (?,?)`
  with one argument per element, while a pointer to a `[]byte` stays one operand and a pointer to
  a nil slice is one NULL;
  or anything reading `ToSQL`'s ARGUMENT list back — a golden file, a contract test, a query
  recorder — at any `f` door or at `In`/`NotIn`/`Between`, since the bound value is now the
  resolved one. One detail for a test comparing that list structurally rather than by value: an
  EMPTY list at `In`/`NotIn` renders the same `(1=0)` / `(1=1)` constant it always did, with the
  argument list now nil rather than an allocated empty slice. Both are length zero and neither
  reaches a driver; only a deep-equality assertion on `[]any{}` can see it.
  no-match = every operand reaching those doors is a plain scalar that is neither a pointer nor a
  `driver.Valuer`, and nothing reads `ToSQL`'s args back.
- apply: for the panics, delete the nil guard that existed ONLY to dodge the crash — but read each
  one first, exactly as `[C61.11]` asks: a guard that deliberately OMITS the predicate for an
  absent operand must STAY, because an unguarded nil now MEANS `IS NULL` and dropping the guard
  narrows the query rather than preserving it. For the ordering and `Between` doors, handle the
  error. What that replaces differs by door, so read the call site rather than assuming it used to
  work: an `f` ordering door RENDERED a predicate that matched nothing (`col < ?` bound to NULL,
  the silent-no-rows shape), `jf`'s ordering doors already returned
  `dbtypes.ErrOrderingOperandNotComparable` from `[C61.11]`, and the `Between` doors either errored
  with squirrel's own message or panicked at its `driver.Valuer` assertion. Only the first is a
  behavior change a caller can miss, and all four now report through the same sentinel:
  `errors.Is(err, dbtypes.ErrOrderingOperandNotComparable)` identifies it at both families, so one
  branch covers them. If you were matching squirrel's message text (`cannot use null with less
  than or greater than operators`) at an `f` ordering door, repoint that match at the sentinel — the
  text is gone. For the argument change, re-record any golden file or contract test pinning
  `ToSQL`'s args at an `f` door or at `In`/`NotIn`/`Between`. TWO shapes move the SQL TEXT at the
  list doors, not just the argument list, so re-record those assertions too: a `[]byte` renders
  `IN (?)` / `NOT IN (?)` where it rendered `= ?` / `<> ?`, and a POINTER to a slice renders one
  placeholder per element (`&[]int{1,2}` gives `IN (?,?)`) where it rendered a single `IN (?)`
  bound to the whole slice.
- verify: `f.Eq("u.id", (*sql.NullString)(nil))` renders `u.id IS NULL` instead of panicking, and
  `f.NotEq` the same operand renders `u.id IS NOT NULL`; `f.Lt("u.id", (*int)(nil))` makes `ToSQL`
  return an error matching `errors.Is(err, dbtypes.ErrOrderingOperandNotComparable)` where it used
  to render `u.id < ?`; `f.Between("u.id", nil, 10)` and `jf.Between("u.id", nil, 10)` both return
  that same sentinel, and `jf.Between("u.id", (*sql.NullString)(nil), 10)` returns it instead of
  panicking; `f.In("u.id", (*sql.NullString)(nil))` renders `u.id IN (?)` with args `[]any{nil}` —
  print the ARGS, not just the SQL, or the resolution is invisible;
  `f.In("u.id", []byte("raw"))` renders `u.id IN (?)` where it rendered `u.id = ?`, with the same
  single `[]byte` argument;
  `lo, _ := qb.Expr("18"); jf.Between("u.id", lo, nil)` returns the sentinel where it rendered
  `u.id >= 18 AND u.id <= ?` bound to NULL; and
  `f.Eq("u.id", sql.NullInt64{Int64: 5, Valid: true})` renders `u.id = ?` with args
  `[]any{int64(5)}`, not the wrapper.
- ref: `database/internal/builder/helpers.go` (`resolveOperand`, `orderingOperand`,
  `resolveListOperands`, `wrapOperandErr`, `orderingOperandErr`) ·
  `database/internal/builder/filter.go` (`equality`, `Lt`, `Lte`, `Gt`, `Gte`, `In`, `NotIn`,
  `Between`) · `database/internal/builder/join_filter.go` (`compare`, `In`, `NotIn`, `Between`) ·
  `database/types/errors.go` (`ErrOrderingOperandNotComparable`) · issues #1205, #1209 · closes the
  carve-outs `[C61.11]` recorded · scalar inequality still differs between the doors (#1200)

### [C61.17] an over-long publish destination is refused instead of publishing · silent-behavior · when: match

- detect: the question is whether any destination your code publishes to, or declares, can exceed
  255 BYTES — bytes, not characters, so a multi-byte name spends more of the budget than its
  length suggests and a name that looks well short of the limit can exceed it.
  `git grep -nE 'PublishToExchange\(|[.]Publish\(|RoutingKey:|Exchange:' -- '*.go'` and read every
  hit whose value is COMPUTED rather than a literal: a tenant slug, a resource id, a user-supplied
  event name, a routing key built by joining segments. A literal in your source is self-evidently
  fine; a `fmt.Sprintf` over request data is the shape that reaches the ceiling.
  Header KEYS count too, at every nesting depth — `git grep -nE 'Headers:\s*map\[string\]any'` and
  read what builds the keys, not the values (values are longstrs and bounded far higher).
  From the other end, your LOGS answer it retrospectively: search for
  `amqp: shortstr .* exceeds 255 bytes`, amqp091's own message, and for a WARN reading
  `Publish failed, retrying...` immediately followed by a reconnect. That pairing IS this bug —
  one unwritable frame taking the shared connection down, several times over, once per retry.
- scope: the rule, once and precisely —
  Before the retry loop, before the readiness pre-flight and before the publish span exists,
  `Publish` and `PublishToExchange` check every caller-supplied shortstr the publish puts on the
  wire — `Exchange` and `RoutingKey`, which travel in the basic.publish METHOD frame, and each
  `Headers` KEY, which travels in the CONTENT-HEADER frame that follows it beside `CorrelationId` — including keys inside a nested table
  and inside a FIELD-ARRAY, since amqp091 encodes an array's elements back through the same
  writeField/writeTable path, so a key one array deep is written by the same writeShortstr as a key
  at the top. Any one
  over 255 BYTES fails the call with an error wrapping the new exported
  `messaging.ErrInvalidPublishDestination` — match it with `errors.Is` — whose text names the FIELD
  and the byte length and never the value, because an over-long destination is usually built from
  request data and this error reaches logs and spans. Zero channel attempts, no reconnect, and
  `ErrPublishRetriesExhausted` is NOT involved: the frame is unwritable whatever the broker's state,
  so retrying only re-tears the connection it just brought back.
  Empty is legal and unchanged — the default exchange publishes with an empty exchange name, a
  fanout binding with an empty routing key. 255 bytes is legal; 256 is not.
  LENGTH only. The charset is deliberately not judged, which is where this rule DIVERGES from the
  consume-side one in `[C60.17]`: there the value belongs to a foreign publisher and lands in a log
  field, so the charset is the load-bearing half. Here the value is the service's OWN destination,
  and a broker that dislikes it answers with a CHANNEL error — recoverable, and not the
  connection-wide failure this bound exists to prevent. The two doors share a ceiling, not a policy.
  The same length rule now also runs in `Declarations.Validate`, so an over-long declared exchange
  name, exchange TYPE (a shortstr of its own in exchange.declare), queue name, binding or publisher
  routing key — or a KEY in any of their `Args`/`Headers` tables, each naming its own location so an
  error says which table to read — fails STARTUP rather than the first publish. Violations are
  aggregated with `errors.Join`, one error per declaration FIELD, so a boot reports the over-long
  exchange name AND the over-long publisher routing key together rather than one per deploy; WITHIN
  a single `Args`/`Headers` table the walk stops at the first oversized key it finds, and Go's map
  order is undefined, so a table carrying two of them names one — fix it and the next boot names
  the other. Consumer declarations are not covered: their tag and queue reach
  basic.consume, and no publisher's connection depends on them. That error names the declaration kind and the length; the offending value is the name
  itself, and repeating 256 bytes of it into a boot failure helps nobody.
  What this REPLACES: the field reached `writeShortstr`, which refused it, and amqp091 answers any
  frame-write failure by shutting down the whole `Connection` — every publisher in the process
  shares it. The error came back classified as transient, so the bounded retry loop tried again on
  the reconnected connection and tore it down again, up to `messaging.reconnect.maxpublishattempts`
  times. amqp091 also embeds the full offending value in that error via `%q`, so it reached the
  publish WARN and the span.
- gate: match = any `Publish`/`PublishToExchange` call whose `Exchange`, `RoutingKey` or `Headers`
  keys are computed rather than literal AND can exceed 255 bytes; or any declaration whose name or
  routing key can; or your logs carry `exceeds 255 bytes` or a retry-then-reconnect pairing.
  no-match = every destination and header key your code publishes or declares is a literal, or is
  built from values you bound below 255 bytes yourself. Nothing changes: the check is a length
  comparison on a path that already ran.
- apply: bound the value at ITS source rather than at the publish — a routing key is a routing
  decision, and truncating one at the last moment silently routes the message somewhere else, which
  is worse than the error. Where the segment is genuinely unbounded (a user-supplied name), hash it
  or reject the request that produced it. Then handle the new error at the call site:
  `errors.Is(err, messaging.ErrInvalidPublishDestination)` is a permanent failure, so a caller that
  retries every publish error must NOT retry this one — nothing about the message will become
  writable. For an event published through the OUTBOX, read `[C61.19]`, which supersedes what this
  clause used to say: on that hop `outbox.Publish` refuses such a destination before the INSERT and
  the relay dead-letters one that reached the ledger anyway, instead of retrying it forever. Bound
  the value at its source regardless — the relay parking a row is a backstop, not a delivery.
- verify: publish with a 256-byte routing key against a broker you can watch, and expect the call to
  return an error satisfying `errors.Is(err, messaging.ErrInvalidPublishDestination)` while the
  connection stays up and the broker records no publish — before this hop the same call took the
  connection down and retried. Then publish with a 255-byte key and confirm it still succeeds: the
  bound is the AMQP limit, not one below it, and a verification that only checks the rejection
  cannot see that. Finally, declare an over-long exchange name and confirm startup now fails naming
  the declaration kind.
- ref: [ADR-070](adr_070_inbound_trace_identifier_validation.md) (amended 2026-08-28) ·
  `messaging/publish_destination.go` · `messaging/amqp_client.go` (`PublishToExchange`) ·
  `messaging/declarations.go` (`Validate`) · `[C60.17]` (the consume-side rule this one is NOT) ·
  issue #1123

### [C61.18] the log filter masks inside opaque payloads · silent-behavior · when: match

- detect: the payloads, not the call sites — this atom changes what a log LINE contains, not
  what compiles. Find the places a pre-encoded body reaches the logger:
  `git grep -nE '[.](Interface|Bytes|Str)\(' -- '*.go'` and
  `git grep -nE 'WithFields\(' -- '*.go'`, then keep the hits whose VALUE is a
  `json.RawMessage`, a `[]byte`, a `[]json.RawMessage`, or a string holding a JSON document —
  a marshalled request or response body, a webhook payload, a stored blob replayed into a log.
  A DEFINED type over either builtin counts and is easy to miss in that sweep, because the value
  reads as a domain type rather than as bytes or text: `type Blob []byte`, `type JSONText string`,
  and the `[]byte`-based column types a driver hands back. The filter judges the KIND, so those
  move exactly as the builtins do; grep your own type declarations
  (`git grep -nE '^type [A-Za-z0-9_]+ (\[\]byte|string)$' -- '*.go'`) and check whether any of them
  reaches a log door.
  `Str` belongs in that sweep as much as the others: a JSON document handed over as TEXT is a
  payload, and it is judged in the shared dispatch like every other shape, so a `.Str("body", …)`
  carrying a document moves exactly as the `[]byte` spelling does.
  (The boundary is spelled POSIX: `git grep -E` is ERE, where `\b` matches nothing.)
  Also grep your own log FIXTURES and any assertion that pins a logged body byte-for-byte. The
  needle list is your own `log.sensitivefields` plus the defaults, so the complete sweep is a
  fixture REVIEW rather than one pattern; this is a shortlist of the shapes that move most often:
  `git grep -nE '"(password|passwd|secret|token|api_?key|private_?key|authorization|d|p|q|dp|dq|qi|oth)":' -- '*_test.go' '*.json'`
  — the single letters are the JWK private members ADR-086 masks by shape, which is why a fixture
  can move without carrying an obviously sensitive NAME. Add `-e 'PRIVATE KEY'` for PEM blocks, and
  read any fixture whose body is pinned byte-for-byte even when neither pattern hits it.
- scope: the filter masks by field NAME, and an OPAQUE payload — bytes or a string whose
  structure it cannot see into — was a single leaf named by whatever key carried it, however
  many named fields it held of its own. Four verified leaks close: a `json.RawMessage`
  `{"password":"pw"}` under `body` through `Interface`, `WithFields` and `Bytes`; a root JWK's
  `d`; a plain JWKS `{"keys":[…]}` (ADR-072's `log.sensitivefields: [keys]` covered it only for
  consumers who opted in); and `[]json.RawMessage`, which leaked since #1131 stopped it
  panicking.
  Bytes — including a NAMED byte-slice type such as `type Blob []byte`, matched by kind rather
  than by the two spellings `json.RawMessage` and `[]byte` — and strings are judged at EVERY door — the check sits in the filter's shared type
  dispatch, so `Interface`, `WithFields`, `Bytes`, `Str` and any nested struct, map or slice
  element inherit it. A payload whose first non-space byte is `{` or `[` is parsed, walked with
  the SAME needles, and re-encoded ONLY when something was masked — so a payload with nothing to mask
  ships the bytes it arrived with, key order and number spelling intact. Nothing else is
  parsed: a bare number, an id, a message, a non-JSON byte slice never reaches the decoder.
  Two SHAPE rules sit on top of the names, for key material no name needle can match: an
  object carrying `kty` is a JWK and its `d p q dp dq qi k oth` members are masked wherever it
  sits — root, inside a JWKS `keys` array, or nested — matched EXACTLY and never by substring,
  since a bare `d` needle would mask a field named `date`; and a PEM block whose label ends in
  `PRIVATE KEY` is masked whole — both a bare key logged on its own and one sitting as a string
  member inside a JSON document, where only that member is masked — while a `CERTIFICATE` or
  `PUBLIC KEY` block stays readable.
  Fail-closed arm: a payload that LOOKS like JSON but does not parse, is not EXACTLY ONE JSON
  document (`{"a":1}{"password":"pw"}` and `{}]{"password":"pw"}` alike — trailing whitespace is
  still one document), nests deeper than `logger.DefaultMaxDepth`, or exceeds the new
  `FilterConfig.MaxPayloadBytes` (default 64 KiB) renders as the configured mask value instead of
  in full. Note the depth rule masks the WHOLE
  payload, where the name filter masks only the subtree past its own budget — this door walks
  bytes a CALLER supplied, so the nesting is not the logging code's choice.
  Deliberately NOT inspected: JWT strings, XML, form-encoded bodies, and the log MESSAGE text
  (`Msg`) — fields only.
- gate: match = any log field in the detect sweep carries a JSON payload, OR anything downstream
  reads those lines back. Concretely: a payload whose own field names hit the needle list starts
  rendering the mask value where it used to render in clear (that is the fix); a payload the
  filter masks is RE-ENCODED, so its key order and whitespace normalize on that path, which a
  byte-for-byte fixture or a log-diffing test can see; a JSON-looking payload that does not
  parse, or one above 64 KiB, renders as the mask value entirely; and a JWK or PEM private key
  logged under ANY field name is masked by shape. no-match = you log no pre-encoded bodies, and
  nothing reads your log lines back byte-for-byte.
- apply: nothing at the call sites — the masking is the point, and a payload with nothing to
  mask is byte-identical to before. Re-record any fixture or assertion that pins a MASKED
  payload's bytes, since that path re-encodes. If a payload of yours is legitimately above the
  cap and you would rather see it unmasked, raise `FilterConfig.MaxPayloadBytes` through
  `app.Options.LoggerFilterConfig` — which REPLACES the whole config, so start from
  `logger.DefaultFilterConfig()` and set the field, never a bare struct literal. Setting it
  NEGATIVE disables the payload door entirely and restores the pre-ADR-086 name-only behavior;
  setting it to zero means the default, so a bare literal cannot silently opt out. And if you
  added `log.sensitivefields: [keys]` for JWKS on ADR-072's advice, it is now redundant — the
  `kty` shape rule covers it — though leaving it costs nothing but a masked field named `keys`.
- verify: log a `json.RawMessage` `{"password":"pw","user":"alice"}` and read the line — it
  renders `{"password":"***","user":"alice"}` where it used to render the password in clear;
  log `{"kty":"RSA","n":"modulus","d":"PRIVATE"}` and confirm `d` is masked while `kty` and `n`
  stay readable; log a payload with nothing sensitive in it and confirm its key ORDER and its
  number literals (`1e3`, a 20-digit integer) are unchanged — print the line, not a re-parsed
  map, or this one is invisible; log a truncated `{"password":"` and confirm the whole field
  renders as the mask value.
- ref: `logger/opaque.go` (`filterOpaquePayload`, `maskJSONValue`, `looksLikeJSON`,
  `isJWK`, `looksLikePEMPrivateKey`) · `logger/filter.go` (`FilterConfig.MaxPayloadBytes`,
  `DefaultMaxPayloadBytes`) · `logger/adapter.go` (`Bytes`, `Str`) ·
  [ADR-086](adr_086_mask_inside_opaque_payloads.md) · issue #1133 · JWT, XML and form-encoded
  payloads remain uninspected by design

### [C61.19] an unpublishable outbox destination is refused at Publish and parked by the relay · silent-behavior · when: match

- detect: the question is whether an outbox event's destination can exceed 255 BYTES — bytes, not
  characters, so a multi-byte name spends more of the budget than its length suggests and a name
  that looks well short of the limit can exceed it. `git grep -nE 'OutboxEvent\{' -- '*.go'`
  and read every `Exchange`, `RoutingKey`, `EventType` and `Headers` KEY that is COMPUTED rather
  than a literal — `EventType` counts because it is what an empty `RoutingKey` falls back to, and
  header keys count at every nesting depth. Then read `outbox.defaultexchange` in every
  environment's config, since an empty `Event.Exchange` falls back to it. From the other end, query
  the ledger: rows whose `exchange` or `routing_key` is 256 bytes or longer — `routing_key` is where
  an over-long `EventType` lands, since the empty-`RoutingKey` fallback persists it there — rows
  whose `headers` JSON carries a key that long at any depth, and rows `pending` with a `retry_count`
  far past `outbox.maxretries` whose `error` column names `publish destination`.
- scope: the rule, once and precisely —
  `outbox.Publish` runs the same length rule the publish doors run (`[C61.17]`), now exported as
  `messaging.ValidatePublishDestination`, on the values it is about to PERSIST: the exchange after
  the `outbox.defaultexchange` fallback, the routing key after the `EventType` fallback, and the
  caller's header keys at every depth. Any one over 255 bytes fails the call BEFORE `Store.Insert`,
  under the package's `outbox:` prefix, with `errors.Is(err, messaging.ErrInvalidPublishDestination)`
  holding; the text names the FIELD and the byte length, never the value. The trace keys the
  framework adds are literals and are not judged. Nothing is written, so the caller's transaction
  carries no undeliverable row.
  The outbox module's `Init` applies the same rule to `outbox.defaultexchange` and fails startup
  when it is over-long, naming the config KEY and the byte length — an over-long default makes
  every event that falls back to it unpublishable, which is a deployment fault, not a per-event one.
  In the relay, a publish error satisfying `errors.Is(err, messaging.ErrInvalidPublishDestination)`
  is now a second POISON class, on the same path as an undecodable header: `retry_count` advances
  each cycle and, at `outbox.maxretries`, the row is parked via `MarkDeadLettered`
  (`status = 'failed'`) with the dead-letter WARN and the dead-lettered outcome count. Below the
  ceiling it stays `pending`. Parking is at the ceiling, never on the first hit.
  Connectivity is UNCHANGED: a broker NACK, a confirmation timeout, not-connected, a deadline and
  `ErrPublishRetriesExhausted` still retry forever, and shutdown/cancel still does not advance
  `retry_count`.
  What this REPLACES: `Publish` persisted any destination it was handed, and the relay classified
  the refusal as connectivity, so such a row was re-attempted every cycle for the life of the table.
- gate: match = any `OutboxEvent` whose exchange, routing key, event type or header keys are
  computed rather than literal AND can exceed 255 bytes; or an `outbox.defaultexchange` over 255
  bytes in any environment; or ledger rows matching the detect queries.
  no-match = every outbox destination is a literal or is bounded below 255 bytes at its source, and
  `outbox.defaultexchange` is short or empty. Nothing changes: the check is a length comparison on
  a path that already ran, and the relay branch is never reached.
- apply: bound the value at its source — a routing key is a routing decision, so hash or reject an
  unbounded segment rather than truncating it late. Handle the new `Publish` error where you handle
  its other errors: it is permanent, so a caller that retries must not retry this one, and because
  it fires BEFORE the INSERT the surrounding transaction is free to roll back or continue without a
  half-written event. Shorten an over-long `outbox.defaultexchange` before deploying, since Init now
  refuses to start with one. Re-drive any row this hop parks the way you re-drive other `failed`
  rows — the framework does not auto-delete or re-publish them.
- verify: publish an outbox event with a 256-byte routing key and confirm `Publish` returns an error
  satisfying `errors.Is(err, messaging.ErrInvalidPublishDestination)` and that the ledger gained NO
  row; then publish one with a 255-byte key and confirm it inserts, since a verification that only
  checks the rejection cannot see that the bound is the AMQP limit rather than one below it. Start
  the app with a 256-byte `outbox.defaultexchange` and confirm Init fails naming
  `outbox.defaultexchange`. Repeat the first check for each remaining field the rule covers — a
  256-byte `Exchange`, a 256-byte `EventType` with an EMPTY `RoutingKey` (the fallback puts it on
  the wire), and a 256-byte header KEY nested one table deep — since a verification that exercises
  only the routing key cannot see a field the walk misses. Finally, insert a row with an over-long
  destination directly and run the
  relay `outbox.maxretries` times: `retry_count` advances each cycle and the last one parks it as
  `status = 'failed'` instead of leaving it `pending` forever.
- ref: [ADR-033](adr_033_outbox_retry_count_status_parking.md) (amended 2026-08-29) ·
  `outbox/publisher.go` (`Publish`) · `outbox/relay.go` (`publishRecord`, `deadLetterPoison`) ·
  `outbox/module.go` (`validateDefaultExchange`) · `messaging/publish_destination.go`
  (`ValidatePublishDestination`) · `[C61.17]` (the publish-door rule this one reuses) · issue #1229

### [C61.20] the observability teardown is best-effort at shutdown · silent-behavior · when: match

- detect: nothing in your Go code changes, so sweep what READS the shutdown outcome. Grep for
  the process exit path — `git grep -nE 'app[.]Run\(|[.]Run\(ctx' -- '*.go'` — and read whether
  its error is turned into a non-zero exit, an alert, or a test assertion. Then grep your
  deployment for anything keyed on that exit: a Kubernetes `preStop`/termination check, a
  systemd unit's restart policy, a CI smoke test that shuts the app down and asserts a clean
  exit, a log-based alert matching `Failed to shutdown observability`.
- scope: the observability phase of `App.Shutdown` no longer contributes to the error
  `App.Run()` returns. A provider `Shutdown` failure — ANY error, not only a deadline — is
  logged once at WARN as `Observability shutdown failed; telemetry may have been lost`, with
  the error and the phase duration, and shutdown proceeds to the closers exactly as before.
  It used to log at ERROR and append `observability: <err>` to the aggregated error, so a
  deployment whose collector was down exited non-zero from a graceful shutdown in which
  nothing but the telemetry sink had failed — and with the framework's own defaults it could
  not do otherwise: the batch processor's in-flight export runs on a context of the SDK's own
  (bounded by `observability.*.export.timeout`, 10s or 60s), while the phase's own budget is
  what the HTTP drain left of `server.timeout.shutdown` (10s by default).
  Phase ORDER is unchanged ([ADR-029](adr_029_graceful_shutdown_order.md)), the preceding
  `ForceFlush` and its WARN are unchanged, a SUCCESSFUL observability shutdown logs as before,
  and every OTHER phase — HTTP server, modules, closers, the hard-stop timeout — folds its
  errors exactly as it did.
- gate: match = anything keys on `Run()` returning an error, or on a non-zero exit, when the
  collector is down at shutdown — a deploy health check, a CI smoke test, an alert on the
  ERROR line. no-match = you read only the logs, or you never run with an unreachable
  collector.
- apply: nothing in application code. Repoint an alert or a log query matching the old ERROR
  line `Failed to shutdown observability` at the WARN line
  `Observability shutdown failed; telemetry may have been lost`, and repoint — rather than
  delete — any test that asserts a non-zero exit for a downed collector: the contract it
  guarded still exists, so assert `Run()` returning nil, that WARN line with its phase
  duration, and the closers running after it. To KEEP the telemetry rather than merely
  stop failing on it, size `server.timeout.shutdown` above `observability.*.export.timeout`.
- verify: shut the app down with the OTLP endpoint pointed at a dead port — `App.Run()`
  returns nil and the process exits 0, one WARN line carries the error and the phase
  duration, and the closers' own "closed successfully" lines still follow it.
- ref: `app/lifecycle.go` (`shutdownObservability`) · `app/app.go`
  (`observabilityShutdownWarnMsg`) · [ADR-029](adr_029_graceful_shutdown_order.md)
  (2026-08-29 amendment) · issue #1225 · #1229's `[C61.19]` lands on this same hop, so the
  hop row's count moves by one for each — keep both if you rebase over it

### [C61.21] a panicking create no longer kills the process, it fails that acquisition · silent-behavior · when: match

- detect: nothing in your Go code names this — the change is in what the framework does with a
  panic your own factory already has. Ask the question at the two ends instead.
  In your LOGS: search your standard-output/stderr stream, NOT your structured log stream, for a
  bare `panic: ` line with no framework field beside it whose stack runs through
  `singleflight.(*Group).doCall` and `resourcepool.(*Pool[...]).createEntry`. Every hit is a
  process death that took this path, and the line carries the panic VALUE, so treat the hits as a
  leak to triage as well as a count. A restart your orchestrator recorded with no shutdown line
  before it is the same event seen from the other side; it is a filter, not a finding, because an
  OOM kill and a liveness-probe restart look the same from there.
  For the code half, read the factories you supply to a pool-backed manager and ask whether they
  can panic: a `DBConfigProvider.DBConfig` (the common one — it runs per tenant on first use), a
  cache `Connector`, a messaging publisher factory. The shapes are the usual ones: a nil map
  write, a type assertion without the comma-ok, an index into a slice derived from configuration.
- scope: the rule, once and precisely —
  `resourcepool.Pool` runs create inside `singleflight.Group.DoChan`, which re-panics on a NEW
  goroutine once any caller used DoChan, so no caller-side recover — Echo's `Recover` included —
  could catch it: one bad factory took the process down. The pool now recovers around the create
  CALL itself — not around the rest of the installation, where a panic arrives after the entry
  already holds its seed lease — and every caller still waiting on that create receives
  `resourcepool: panic during create for key "<key>" (type: <T>)` — the value's Go TYPE only, never
  the value (ADR-081), which is consumer-chosen and therefore beyond the log filter's field-name
  matching. A caller whose own context ended first still gets its `ctx.Err()`, as it always did.
  The failure counts as one error in `PoolStats.Errors`, in the singleflight leader, not
  once per waiter; `TotalCreated` does not move and no entry is installed. The pool stays usable:
  the create ran before the pool mutex was taken, so the unwind leaves nothing locked and a later
  create for the same key runs normally.
  This binds every pool-backed manager — `database.DbManager`, `cache.CacheManager` and the
  messaging publisher pool — at their create seam only. A panic anywhere else is untouched: a
  handler panic still goes through the server's `Recover`, and a message-handler panic still nacks
  without requeue. A panic in the CLOSER is deliberately NOT covered: it runs after the new entry
  is installed, so answering it with an acquisition error would strand that entry's seed lease —
  it stays fatal, exactly as it was before this hop.
  What you LOSE is the panic value: it used to reach stderr in net/http's or the runtime's own
  renderer, and now it is printed nowhere at all. The type and the key are what remain, so a
  factory panicking with a value that identified WHICH input was bad now needs that detail in an
  error your factory returns rather than in the panic.
- gate: match = your stderr carries a `panic: ` line with a `singleflight` frame in its stack, OR a
  restart with no shutdown line before it, OR any factory you supply to a pool-backed manager — a
  dynamic `DBConfigProvider` is the common one — can panic.
  no-match = every factory you hand the framework returns errors and cannot panic. Nothing changes
  for you; the guard is a path your deployment never reaches.
- apply: nothing is required — failing one acquisition beats losing the process. Two things are
  worth doing anyway. Any alert keyed on the process symptom (a restart count, a stderr `panic:`
  match) stops firing for this class, so repoint it at the failing request and at the pool's error
  counter. And fix the panics the detect step surfaced: the guard turns them into a clean error,
  it does not make them correct, and the acquisition still fails — returning an error from the
  factory keeps the detail the panic value used to carry.
- verify: point a manager at a `DBConfigProvider` whose `DBConfig` panics with a marker string,
  then call `Get` twice for the same tenant. Expect the first call to return a non-nil error
  reading `panic during create for key` with the value's TYPE and the marker NOWHERE in it, the
  process to survive, and the second call to succeed. Reading only the first call cannot see the
  half that matters operationally — that the pool is still usable afterwards.
- ref: issue #1141 · [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) ·
  `internal/resourcepool/resourcepool.go` (`Pool.acquireShared`) · `messaging/manager.go`
  (`EnsureConsumers`, the same guard on the messaging manager's own DoChan) · [C61.12] (the same
  blast-radius class on the HTTP middleware chain)

### [C61.22] a user-named config section must be reachable by an environment variable · breaking · when: match

- detect: read the KEYS you chose under three maps — `databases.<name>`,
  `multitenant.tenants.<id>` and `keystore.keys.<name>` — in every config file AND every overlay,
  `config.yaml` / `config.yml` and `config.<env>.yaml` / `config.<env>.yml` alike — the loader
  tries `.yaml` and falls back to `.yml` (`config/config.go`, `tryLoadYAMLFile`), so a repo that
  spells its overlays either way is in scope.
  `git grep -nE '^[[:space:]]{2,}[A-Za-z0-9_.-]+:' -- '*.yaml' '*.yml'`
  narrowed to those three blocks (`[[:space:]]`, never `\s` — `git grep -E` has no PCRE escape and a
  pattern carrying one silently matches nothing, which would report "not affected" to every
  consumer), or read them by eye: the question is whether any key carries a
  character outside `[a-z0-9-]` — an underscore and an uppercase letter are the two that occur in
  practice. A hand-built `config.Config` counts too: the check runs in `config.Validate`, which every
  construction path calls (ADR-064), so a literal `map[string]DatabaseConfig{"report_db": …}` in Go
  fails the same way. Dynamic tenant providers do NOT count — their IDs never reach this check.
- scope: the rule, once and precisely —
  A key under `databases`, `multitenant.tenants` or `keystore.keys` must match `^[a-z0-9-]+$`.
  One that does not fails `config.Validate` with a `*ConfigError` whose `Field` is the KEY PATH
  (`databases.report_db`, `multitenant.tenants.acme_corp`, `keystore.keys.my_key`) and whose action
  says rename and states the rule. One exception, in all three sections: a name containing `.` is
  reported against the PARENT map (`databases`, `multitenant.tenants`, `keystore.keys`), because a
  dotted name would make the key path itself ambiguous — `keystore.keys.my.key` reads as a `key`
  under `my`. Hyphen is legal; there is no length bound here (the resolver keeps
  its own `{1,64}`).
  The reason is reachability. `Load` maps an environment variable to a key by lowercasing it and
  turning `_` into `.`, so `DATABASES_REPORT_DB_PORT` reaches `databases.report.db.port`: a section
  named `report_db` cannot be addressed by ANY variable. Alone, that surfaced as a startup failure
  blaming a phantom `databases.report`; beside a real sibling `databases.report`, the variable was
  applied to the SIBLING's subtree while `report_db` silently kept its YAML value. What that costs
  depends on the name: `report.db.port` matches no field, so the override is silently DROPPED, while
  a section named `report_pool` beside a `report` sends `DATABASES_REPORT_POOL_MAX_CONNECTIONS` to
  `databases.report.pool.max.connections`, which is a real field on the sibling. Either way the
  setting an operator wrote does not reach the section they wrote it for, and nothing errors.
  Where the rule does NOT reach: header maps (`*.headers.<name>`) are protocol identifiers and are
  untouched; a DYNAMIC tenant source's IDs are gated by the resolver's own `^[a-z0-9-]{1,64}$` at
  request time; and the env transform itself is byte-identical, so every variable that reached a key
  before still reaches it (ADR-024's flat-smushed leaf keys included).
  The three pre-existing rejections are unchanged and still fire first where they apply: an empty
  name, the `gb_` reserved prefix, and a name containing `.`.
- gate: match = any key under those three maps carries a character outside `[a-z0-9-]`, in a file,
  an overlay, or a hand-built `Config`.
  no-match = every such key is already lowercase letters, digits and hyphens. Nothing changes: the
  check is a regexp match on names that already pass it.
- apply: rename the key, and move everything addressed to it in the same commit — the YAML key in
  every overlay, any `DATABASES_<NAME>_*` / `MULTITENANT_TENANTS_<ID>_*` / `KEYSTORE_KEYS_<NAME>_*`
  variables in your manifests and secret stores, and every call that names it in Go
  (`deps.DBByName("report_db")`, a keystore lookup, a tenant fixture). Prefer a hyphen or a smush
  (`report-db`, `reportdb`); if you take the hyphen, check the runtime that sets the variable —
  Docker and Kubernetes accept `-` in a variable name, POSIX `export` does not, so a hyphenated name
  is env-settable in a container and not from a login shell. There is no escape hatch and no alias:
  ADR-024 rejected the double-underscore spelling, and ADR-090 rejects a mapping layer for the same
  reason.
- verify: start the app with the renamed key and confirm it boots; then set the variable the new name
  implies (`DATABASES_REPORT_DB_PORT` becomes `DATABASES_REPORT-DB_PORT` for `report-db`, or
  `DATABASES_REPORTDB_PORT` for `reportdb`) and confirm the value ARRIVES — read it back from the
  running config, since the failure this atom fixes was a variable that reached a different key
  without erroring. If you had the sibling shape, confirm the sibling's own value is unchanged too.
  A config you did not rename fails startup naming the offending key, which is the other half of the
  check.
- ref: [ADR-090](adr_090_env_reachable_section_names.md) · `config/validation.go`
  (`sectionNamePattern`, `checkSectionName`, and its three call sites) ·
  [ADR-024](adr_024_config_key_flatsmush.md) (`[C401.1]`, the leaf-key half of the same property) ·
  `wiki/multi_tenant_resolvers.md` (the resolver grammar this reuses) · issue #1124

### [C61.23] the outbox ledger is sequenced, laned and drained by one leader · silent-behavior + compile-break · when: match

- detect: two independent sweeps, because this atom breaks at COMPILE time for one audience and
  changes RUNTIME behavior for another.
  Compile: `git grep -n 'outbox[.]Store' -- '*.go'` for a `Store` implemented outside the
  framework — it gains `Lead`, so an outside implementation stops compiling — and
  `git grep -nE 'config[.]OutboxConfig' -- '*.go'` read for anything COMPARING two values
  (`==`, `!=`, a map key, a `switch` on the struct), since `SuperStreams []string` makes the
  struct non-comparable. A field-by-field read or `reflect.DeepEqual` is unaffected.
  Runtime: `git grep -n 'outbox:' -- '*.yaml' '*.yml'` and your deployment's env for
  `OUTBOX_ENABLED` — every enabled outbox moves, whether or not you touch Go code.
- scope: four columns join the ledger — `seq` (a per-ledger identity assigned at insert),
  `lane` (`amqp` or `stream`), `stream` and `partition_key` — plus a companion
  `<table>_leader` table holding one row. `FetchPending` orders by `seq` instead of
  `created_at`. The guarantee that buys is CAUSAL, not global: a dependent event's transaction
  begins after its cause committed, so its `seq` is higher and it drains later. Two INDEPENDENT
  transactions may still commit out of `seq` order — `FetchPending` orders what is VISIBLE to
  it — and the relay promises nothing between them. What ends is the `created_at` TIE, where two
  rows written in the same tick could drain in either order however they were related. The pending index moves to `(seq)`.
  Two behaviors change for every deployment, including single-tenant ones that configure
  nothing new. **One relay instance per ledger drains at a time**: a cycle takes the leader row
  `FOR UPDATE NOWAIT` before fetching and holds it for the cycle, so a second replica logs
  `another instance leads this ledger` at DEBUG and does nothing. Where two replicas used to
  publish the same rows — reliable duplication that at-least-once made survivable rather than
  correct — one now does. And **a failed row parks its key's later rows for the cycle**: they
  are neither published nor marked, so their `retry_count` no longer advances while an earlier
  row of the same key is failing. The key is the tenant stamp for a stamped AMQP row, the
  destination (`exchange` + routing key) otherwise, and stream + partition key on the stream
  lane. Only a FAILURE parks; a dead-lettered row is terminal and an unrecorded one was
  delivered, so neither blocks what follows.
  The super-stream leg is LIVE on this hop, not a later one. `outbox.superstreams` lists the
  super streams the relay may target, and it is optional: unset — the default — every event
  stays on the AMQP lane and nothing about publishing changes. Set it and each listed name gets
  one publisher declared by the outbox module, so a super stream the outbox publishes to cannot
  also be published to directly by another module in the same process; the streams manager
  refuses the second publisher. A stream target with `messaging.streams.uri` unset fails startup
  rather than dead-lettering every stream row.

  **The relay now moves the tenant stamp out of a row's persisted headers and onto the publish
  context.** A consumer that read `x-tenant-id` off an outbox-relayed message still reads it —
  the framework re-stamps from the context (ADR-087) — but a consumer or test that asserted the
  header survives the ledger UNCHANGED, or that relied on a hand-written stamp in
  `OutboxEvent.Headers` reaching the broker verbatim, sees it replaced by the framework's. A
  stamp that disagrees with the framework's resolved tenant is refused rather than silently
  overwritten.
  A lost leader row mid-cycle — an `idle_in_transaction_session_timeout`, a recycled connection,
  a partition — ends the cycle cleanly, marks nothing further, and is reported as itself rather
  than as a broker outage. The next tick re-leads.
  The table name gains a 49-byte bound on its own segment, so every identifier derived from it
  (`idx_<name>_published` is the longest) stays distinct under PostgreSQL's 63-byte truncation.
  A longer `outbox.tablename` now fails startup instead of silently truncating an index name.
- gate: match = `outbox.enabled: true` anywhere (schema and behavior), OR `outbox.Store` is
  implemented outside the framework, OR your code compares `config.OutboxConfig` values, OR
  `outbox.tablename` names a table whose last segment exceeds 49 bytes. no-match = the outbox is
  disabled everywhere and you neither implement `Store` nor compare the config struct.
- apply: with `outbox.autocreatetable: true`, nothing — the new columns, the index and the leader
  table are created for you. With MANAGED migrations, run the statements for your vendor from
  [wiki/outbox.md](outbox.md) BEFORE deploying, in their documented order: the `ALTER`, then the
  `seq` backfill, then the index. The backfill is EXPLICIT and not optional — an identity column
  populates existing rows in the order the rewrite reads them (heap order on PostgreSQL, rowid
  order on Oracle), which is not `created_at` order, and the outbox updates pending rows, so the
  divergence lands precisely on the retried rows a backlog is made of. Grant the relay role
  `SELECT … FOR UPDATE` on the leader table. An outside `Store` adds `Lead`; code comparing two
  `OutboxConfig` values compares the fields it cares about instead. If `outbox.tablename` is
  longer than 49 bytes for its last segment, rename the table before upgrading. Leave
  `outbox.superstreams` unset on this hop.
- verify: after upgrading, `SELECT seq, lane FROM gobricks_outbox ORDER BY seq LIMIT 5` returns
  rows with `lane = 'amqp'`; a relay cycle logs `parked=0` on a healthy tick; with two replicas
  running, exactly one logs a cycle summary and the other logs `another instance leads this
  ledger` at DEBUG. If you migrated a BACKLOG, confirm migration history shows the `seq`
  backfill ran before the pending-index creation, and
  `SELECT COUNT(*) FROM gobricks_outbox WHERE seq IS NULL` is 0 — every row has a
  sequence. Do not expect `seq` to match `created_at` order: the backfill is heap
  order on PostgreSQL and rowid order on Oracle. An unbackfilled ledger is not a
  valid migration — stop before deployment until every row has a non-NULL `seq`;
  otherwise the backlog drains in undefined order and nothing reports it.
- ref: `outbox/store.go` (`Record`, `Store.Lead`, `Leadership`, `leadRow`) ·
  `outbox/store_postgres.go` · `outbox/store_oracle.go` · `outbox/relay.go` (`relayKey`,
  `runRelayLoop`) · `outbox/publisher.go` · `database/errors.go` (`IsLockNotAvailable`) ·
  [ADR-088](adr_088_outbox_ordered_leader_relay.md) · issue #1232

### [C61.24] a tenant's cache config error is addressed to that tenant · breaking · when: match

- detect: `git grep -nE 'ConfigError|IsNotConfigured|\.Field ==' -- '*.go'` and read every match
  that can be reached by a CACHE failure — a factory-resolver error, a `deps.Cache(ctx)` failure,
  anything routing on `ConfigError.Field`. What you are looking for is a matcher written against
  the ROOT spelling — `field == "cache.redis.host"`, `field == "cache"`, `strings.HasPrefix(field,
  "cache.")` — that must now also match `multitenant.tenants.<id>.cache.…`. A suffix match
  (`strings.HasSuffix(field, "cache.redis.host")`) keeps working and needs no edit.
  Outside code: grep your runbooks, dashboards and alert rules for `CACHE_REDIS_HOST` and
  `CACHE_ENABLED` as REMEDIES — a runbook telling an operator to set one of those to fix a single
  tenant was already wrong and now says so itself.
  This only fires where a resource key is non-empty, so a single-tenant deployment has nothing to
  find: `git grep -nE 'multitenant.*enabled|source.*dynamic' -- '*.yaml'` tells you whether you are
  in scope at all.
- scope: the rule, once and precisely —
  The per-key cache factory (`app`'s `CacheConnector`) resolves a key, so its four config errors —
  a nil config, `Enabled=false`, an unsupported `type`, an empty Redis host — are now addressed to
  the key that produced them. For a non-empty key K: `Field` becomes
  `multitenant.tenants.K.cache.<leaf>` (the bare section for the nil-config and disabled cases,
  which name no leaf), and a hint the framework generated is re-pointed to match — naming
  `MULTITENANT_TENANTS_<K>_CACHE_<LEAF>` when that key round-trips through an environment
  variable, and dropping the env half entirely when it does not (a key containing `_`, since
  `_`→`.` is not injective). The empty key is the ROOT cache and is returned UNTOUCHED — the same
  error value, so a single-tenant deployment's `Field`, `Action` and rendered text are
  byte-identical to v0.60.0.
  `Category` never moves: a missing host stays `missing`, a disabled cache stays `not_configured`
  and satisfies `config.IsNotConfigured`, an unsupported type stays `invalid`.
  MESSAGES are unchanged, including the nil-config message's `configuration is nil for key
  'acme'` — the key travels in `Field` now as well, but rewording the message would break the
  root spelling's byte-identity for no gain.
  A hand-written `Action` is untouched: the unsupported-type error keeps `must be one of: redis`.
  What this REPLACES is a disagreement between two doors: `checkTenantCache` already spelled a
  tenant's cache failure `multitenant.tenants.acme.cache.redis.host` at STARTUP, while the runtime
  door reported `cache.redis.host` for the same tenant — so a consumer routing on `Field` could not
  treat them alike, and the hint sent an operator to configure the root cache, which leaves the
  failing tenant exactly as broken. Both doors now call one exported
  `config.QualifyCacheConfigErrorForKey`, so they cannot drift apart again.
  Databases got this on the previous hop (`[C60.19]`); this closes the cache asymmetry that atom
  recorded as still open. There is no named-cache spelling — `databases.<name>` has no cache
  analogue, so a non-empty key is always a tenant id.
- gate: match = you are multi-tenant or run a dynamic `ResourceSource`, AND your code reads
  `ConfigError.Field`/`.Action` on a cache failure with an equality or prefix matcher, OR a runbook
  names `CACHE_REDIS_HOST`/`CACHE_ENABLED` as the fix for one tenant.
  no-match = single-tenant (every key is `""`, and every string is byte-identical to before), or
  you only ever read `err.Error()` for display. Nothing changes for you.
- apply: replace an equality matcher on a cache `Field` with one scoped to the family — accept
  `cache.<leaf>`, `multitenant.tenants.<id>.cache.<leaf>`, and the BARE `multitenant.tenants.<id>.cache`
  the nil-config and disabled cases produce, the way `[C60.19]` had you do for `database.` — or
  match the leaf as a suffix, remembering those two failures have no leaf to match. Repoint any runbook step that says "set
  `CACHE_REDIS_HOST`" for a tenant failure at the tenant's own key; the error now names it for
  you, and when it names only a YAML path that is because the tenant id cannot be spelled as an
  environment variable, so YAML is the only door.
- verify: resolve a cache for a tenant whose section has no Redis host and read the typed error,
  not the rendered string: `Field` is `multitenant.tenants.<id>.cache.redis.host` and `Action`
  names `MULTITENANT_TENANTS_<ID>_CACHE_REDIS_HOST`. Then do the same for a tenant id containing
  an underscore and confirm the action names only the YAML path — that is the round-trip guard,
  and a verification that checks only the first tenant cannot see it. Finally resolve the ROOT
  cache with no host and confirm `cache.redis.host` and `CACHE_REDIS_HOST` come back exactly as
  they did before the hop.
- ref: issue #1125 · [ADR-076](adr_076_section_qualified_config_error_field.md) (2026-08-30
  amendment) · `config.QualifyCacheConfigErrorForKey` · `app/factory_resolver.go`
  (`CacheConnector`) · `[C60.19]`, whose residual this closes

### [C61.25] the native streams lane is opt-in at the build graph · compile-break + breaking · when: match

- detect: `git grep -nE 'app\.StreamDeclarer|[.]DeclareStreams\(' -- '*.go'`
  and `git grep -nF '"github.com/gaborage/go-bricks/messaging/streams"' -- '*.go'`
  and `git grep -nE 'messaging\.streams\.uri|MESSAGING_STREAMS_URI' -- '*.yaml' '*.yml' '*.env' '*.go'`
  Hits on `app.StreamDeclarer` or a `DeclareStreams(` call on the registry are compile
  breaks. Hits on the quoted import path mean you already link the lane. Hits on the URI
  in-repo with no matching import in the same module are the new startup failure.
  The URI also arrives from `MESSAGING_STREAMS_URI`, Helm, Vault, or AWS Secrets Manager —
  a repo grep miss is not proof the process is unconfigured; check those sources before
  taking the no-match path.
- scope: `app` no longer imports `messaging/streams`. The lane is present if and only if
  that package is in the import graph — a blank `_ "github.com/gaborage/go-bricks/messaging/streams"`
  is enough; a module that already imports it to implement `DeclareStreams(*streams.Declarations)`
  needs nothing more. `messaging.streams.uri` set without that import fails startup with
  `app.ErrStreamsNotLinked`, which names the import. No URI and no import starts clean, as
  before. Config keys, declaration methods and the manager lifecycle are unchanged.
  `app.StreamDeclarer` and `ModuleRegistry.DeclareStreams` are deleted because they named
  `*streams.Declarations`; collection lives on the registered runtime, which type-asserts
  `streams.StreamDeclarer`. `HeldMessage` / `HoldLedger` / `HoldReplayer` are aliases of
  the same seam types on `app` and `messaging/streams`, so `inbox` no longer pulls the
  vendor client.
- gate: match = you import `app.StreamDeclarer`, call `DeclareStreams` on the registry,
  set `messaging.streams.uri` (in-repo or via env/secret), or implement `DeclareStreams`.
  no-match = you never configured native streams — including in env and secret sources —
  and never named those types.
- apply: add `import _ "github.com/gaborage/go-bricks/messaging/streams"` (or a real
  import, if you declare topology) to every process that sets `messaging.streams.uri`.
  Replace `app.StreamDeclarer` with `streams.StreamDeclarer`. Stop calling
  `ModuleRegistry.DeclareStreams` — the framework collects declarations through the
  registered runtime. A leftover URI on a service that does not use streams should be
  deleted rather than importing the lane to satisfy the new check.
- verify: `go build ./...` is clean. A process with the URI and the import does not fail
  with `app.ErrStreamsNotLinked` — it may start, no-op on empty declarations, or fail for
  validation, hold, or broker reasons. A process with neither starts clean. `go list -deps`
  of a core-only consumer (`app`, `inbox`, `messaging`, …) does not name
  `rabbitmq-stream-go-client`, `snappy`, `pierrec/lz4`, `pkg/errors` or `murmur3`.
- ref: gaborage/go-bricks#1169 · [ADR-091](adr_091_streams_opt_in_registration.md) ·
  `app/stream_runtime.go` · `internal/streamruntime` · `messaging/streams/register.go`

---

## E62 · v0.61.0 → v0.62.0 — the literal `Local` timezone is refused + cache readiness goes non-critical by default + the keystore secret-length floor is mandatory

- gist: `normalizeIANATimezone` validated every timezone key with `time.LoadLocation`, and Go's
  loader resolves the exact spelling `Local` to the host zone without consulting the IANA
  database — ADR-016 even listed it as accepted. That made `Local` an undocumented second
  spelling of the deliberate `"-"` opt-in, and not an equivalent one: `"-"` leaves a database
  session on the server's default zone, while `Local` handed the application host's zone to the
  driver. The shared normalizer now refuses exact `Local` on every key it serves and steers to
  `"-"` (C62.1, ADR-093). `local`/`LOCAL` were already unknown zones and stay refused.
  Separately on the readiness side: ADR-046 (v0.56.0) made an absent `cache.critical` mean
  critical, so a cache-enabled service answered `/ready` with `503` while Redis was unreachable —
  on every replica at once, and on every local or CI boot without a Redis. A cache with an origin
  behind it degrades correctly, and for that common shape the strict default converted one Redis
  blip into a fleet-wide eviction, while the deployment that opted out with `critical: false`
  paid an unsuppressible startup WARN for the safer choice. ADR-094 reverses the default: an
  absent key leaves the probe informational, `critical: true` is the only way into readiness
  gating, and the explicit-false WARN is deleted (C62.2).
ADR-065 made `keystore.secretminlength` a tri-state pointer and kept `0` as a
  deprecated opt-out behind two startup WARNs, so a consumer with a genuinely short
  partner key could say so before the floor became mandatory. None did. The floor is now
  mandatory at 32 and a set value can only raise it: `config.Validate` rejects `0` and
  anything below 32 on every door, both WARNs are gone, and a symmetric secret shorter than
  32 bytes has no keystore path (C62.3, ADR-095).

---

### [C62.1] the literal `Local` timezone is refused · breaking · when: match

- detect: `git grep -nE "timezone: *[\"']?Local[\"']?" -- '*.yaml' '*.yml'` for YAML, and
  `git grep -nE "TIMEZONE=+[\"']?Local[\"']?" -- '*.env' '*.yaml' '*.yml' '*.sh'` for the env spelling
  (`SCHEDULER_TIMEZONE`, `DATABASE_TIMEZONE`, `DATABASES_<NAME>_TIMEZONE`,
  `MULTITENANT_TENANTS_<ID>_DATABASE_TIMEZONE`), and `git grep -nE 'Timezone: *"Local"' -- '*.go'`
  for a hand-built `config.Config`. The value also arrives from Helm, Vault, AWS Secrets
  Manager, the CLI's `tenants.yaml` and a dynamic `DBConfigProvider` payload — a repo grep miss
  is not proof; check those sources before taking the no-match path.
- scope: `scheduler.timezone`, `database.timezone`, every `databases.<name>.timezone` and
  `multitenant.tenants.<id>.database.timezone`, at `config.Validate` and at the runtime door
  (`ApplyDatabasePoolDefaultsForKey`, which the `go-bricks-migrate` CLI and a dynamic
  `DBConfigProvider` go through). Exact `Local` returns a `*ConfigError` whose `Field` is the
  key and whose message reads `timezone "Local" is not accepted; use "-" for the documented
  opt-out or an explicit IANA zone`; the `Action` is the existing valid-options list. `"-"`, `UTC`, empty (defaulted to
  `UTC`) and every IANA name are unchanged, and so is what `"-"` means at runtime. `local` and
  `LOCAL` fail as they did, as unknown zones — only the exact spelling was ever special-cased.
  `scheduler.Module.Init` does not re-check: it requires a `config.Validate`-normalized config
  (ADR-075), so a hand-assembled `ModuleDeps` around an unvalidated config is not covered.
- gate: match = any timezone key delivered as exactly `Local`, in-repo or from an env/secret
  source. no-match = every timezone value is `"-"`, unset, `UTC` or an IANA name.
- apply: on `scheduler.timezone`, write `"-"` (quoted, as every example in the wiki spells it)
  if host-local was the intent, or the IANA zone otherwise. On a database key `"-"` is NOT
  host-local: it leaves the session on the SERVER's default zone (ADR-016). Write `"-"` there
  only if that is what you want; if `Local` was giving you the application host's zone, write
  that zone as an explicit IANA name.
- verify: the failure point differs by path, so probe each one you run. A static config fails at
  STARTUP with a `*ConfigError` naming the key. A dynamic `DBConfigProvider` record fails at that
  tenant's FIRST acquisition, addressed `multitenant.tenants.<id>.database.timezone` (ADR-076) —
  a green boot says nothing about it, so exercise the tenant. The migrate CLI refuses the tenant
  before dialing (`go-bricks-migrate info` per tenant is the cheap probe). With `"-"` or an IANA
  zone in place, each path proceeds. `go test ./config/ -run 'Timezone|LiteralLocal'` covers both
  sections.
- ref: gaborage/go-bricks#1292 · [ADR-093](adr_093_reject_literal_local_timezone.md) ·
  `config/defaults.go`

### [C62.2] an absent `cache.critical` leaves the cache probe non-critical · silent-behavior · when: match

- detect: `git grep -n 'critical' -- '*.yaml' '*.yml'` over every deployment's config, and the
  environment of each for `CACHE_CRITICAL`; for a hand-built config,
  `git grep -nE 'CacheConfig\{' -- '*.go'` and read whether `Critical` is set. A cache-enabled
  deployment (`cache.enabled: true`, or a custom `Options.CacheConnector`) with NO hit relies on
  the v0.61.0 default. Then grep your alerting and log queries for the WARN line
  `cache.critical is explicitly false`, which stops firing.
- scope: `Config.IsCacheCritical` answers `false` for a nil `Critical` pointer and for a nil
  receiver, so a cache-enabled deployment that says nothing now answers `/ready` `200` through a
  Redis outage, with `cache: "unhealthy"` and a climbing `cache_stats.errors` in the body and NO
  `Readiness check failed` ERROR line — v0.61.0 answered
  `503 {"status":"not ready","cache":"unhealthy","error":"cache unavailable"}` and logged that
  line on every poll. An explicit `critical: true` is unchanged: the `503`, its sanitized body
  (ADR-048) and the ERROR line all stand. An explicit `critical: false` is unchanged in effect
  and no longer emits the startup WARN. The key stays a tri-state `*bool` with no registered
  koanf default, so `config.CacheConfig{Critical: new(true)}` and every YAML and env spelling
  parse as before, a delivered-empty `CACHE_CRITICAL=` still fails resolution (ADR-077), and
  `multitenant.tenants.<id>.cache.critical` still parses and is still ignored.
- gate: match = EITHER `cache.critical` is absent in a cache-enabled deployment
  (`cache.enabled: true`, or a custom `Options.CacheConnector`) that needs a Redis outage to take
  the replica out of rotation — a rate limiter that must fail closed, a session store, an
  idempotency ledger — OR an alert or log query keys on the `cache.critical is explicitly false`
  WARN line. no-match = `cache.critical` is absent but the cache fronts an origin the service can
  serve from (the new default is the posture you wanted); or `cache.critical` is already set
  explicitly, `true` or `false`, and nothing watches the WARN line — nothing changes for you.
- apply: set `cache.critical: true` (or `CACHE_CRITICAL=true`) in that deployment BEFORE the
  bump — the key is accepted on v0.61.0 and is a no-op there, so it can ship ahead of the
  upgrade. A deployment already on `critical: false` needs nothing in config; drop any log-based
  alert on `cache.critical is explicitly false`, and the key itself may stay or go.
- verify: with the cache enabled and Redis stopped,
  `curl -s -o /dev/null -w '%{http_code}' "$APP/ready"` prints `200` under the default (and the
  body carries `"cache":"unhealthy"`) and `503` once `cache.critical: true` is set.
- ref: `config/config.go` (`IsCacheCritical`) · `app/app_builder.go` (the WARN is deleted) ·
  `app/readiness.go` (`cacheProbe`) ·
  [ADR-094](adr_094_cache_readiness_non_critical_default.md) ·
  [cache.md#readiness](cache.md#readiness) · issue #1296

---

### [C62.3] `keystore.secretminlength` below 32 fails startup · breaking · when: match

- detect: `git grep -nE 'secretminlength|SECRETMINLENGTH|SecretMinLength' -- '*.yaml' '*.yml' '*.toml' '*.env' '*.go' '*.tf' '*.json'`
  over your service AND its deployment repos, then read each hit's VALUE: in scope when it is
  `0` or any number below 32. A hit is only a shortlist — the value may be interpolated from a
  secret manager or a Helm values file the grep does not reach, so check every environment's
  rendered config too. A config that never sets the key is out of scope: absence still means 32.
- scope: `config.Validate` now rejects a set `keystore.secretminlength` below 32 — `0`, the
  former opt-out, included — with a `*ConfigError` on that key reading
  `must be at least 32: the symmetric-secret length floor is mandatory (ADR-095)`, judged
  before the empty-keys return so a config no key follows is rejected too. Because every
  construction path validates ([C59.12]), `config.Load`, `app.New*` and a hand-built
  `*config.Config` all fail the same way. `keystore.Module.Init` refuses the same
  values itself, so a hand-built `*app.ModuleDeps` handed straight to it — the one
  door that skips `Validate` — cannot load key material behind a weaker floor either.
  Both doors judge the floor before the keys, so a config carrying NO symmetric secret
  at all — every entry an RSA pair or a PKCS#12 bundle — is rejected too when it sets a
  value below 32: the floor is configuration, not a per-entry rule, and an inert `0` is
  still a `0` a later `secret:` entry would silently inherit. `keystore.Module.Init` no longer WARNs about a
  disabled floor or an admitted short secret: those paths are deleted. nil still means 32
  and `N ≥ 32` still sets the floor to `N`; the field stays `*int` (`new(n)`).
- gate: match = any environment or Go literal sets the key to `0` or to a value below 32.
  no-match = the key is absent everywhere, or every set value is 32 or more — nothing to do.
- apply: remove the key (the default is 32) or set it to 32 or more. Then look at WHY it was
  below 32: if a symmetric secret shorter than 32 bytes was being admitted, lengthen it — the
  floor exists because such a key is weak — or, for a partner-mandated key that cannot change,
  load it outside `keystore` in your own code; the framework no longer offers a weaker floor.

  ```yaml
  # before
  keystore:
    secretminlength: 0        # floor off (deprecated since ADR-065)

  # after
  keystore:
    secretminlength: 32       # or delete the line — absent means 32
  ```

- verify: the service starts. On the affected shape it exits during startup with
  `invalid configuration: keystore config: config_invalid: keystore.secretminlength must be at least 32`.
  `git grep -n 'secret length floor disabled'` over your log-based alerts and runbooks finds
  nothing that still expects the WARN.
- ref: [ADR-095](adr_095_keystore_secret_floor_mandatory.md) · [ADR-065](adr_065_keystore_secretminlength_tristate.md) · `config/keystore_section.go` · issue #1036

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

### [C60.17] the HTTP trace headers and the AMQP delivery identity are validated before every FRAMEWORK sink · silent-behavior · when: always

- detect: your Go code cannot tell you this either — every offending value comes from a
  caller or a foreign publisher. Search your LOG BACKEND for a `traceparent` field that is
  not spec-exact — the full rule is [C60.8]'s, unchanged: dash-delimited LOWERCASE hex in
  the version-00 positions (two version digits, a 32-digit trace-id, a 16-digit parent-id,
  two flag digits), with an all-zero trace-id or parent-id rejected, version `ff` rejected,
  version `00` exactly 55 characters, and a later version's extra fields printable
  non-space ASCII within 255 bytes total. A query matching only the delimiters and lengths
  will miss the uppercase-hex, all-zero and `ff` classes, which are discarded too, and for `amqp_correlation_id`, `message_id`,
  `routing_key` or `exchange` values outside what the shapes below allow — each of those was being
  emitted and is now omitted. A non-spec-exact `traceparent` you find may also be
  FIRST-PARTY and unaffected by this hop — see "What this atom does NOT cover" below before
  chasing the emitter. The cheapest detect, once upgraded, is the new field: query
  your consume lines for `identity_rejected: true` — it fires on exactly the deliveries this
  atom changed, and `delivery_tag` on the failure line identifies each one. Then re-read anything that consumes the RESPONSE
  `traceparent` header of your own services, since a client that echoed its own value back
  to itself now gets a framework-minted one instead. On the metric side, check dashboards
  filtering `messaging.rabbitmq.destination.routing_key` or `messaging.rabbitmq.exchange`,
  or grouping by `messaging.destination.name`, on the AMQP consume instruments. A delivery
  whose exchange or routing key fails the shape loses that ATTRIBUTE entirely, and the
  destination name — which is built as `exchange:routing_key:queue` and is always emitted —
  keeps its shape with that segment EMPTY. A dashboard grouping by destination name
  therefore gains a new series such as `:user.created:my-queue` rather than losing one.
- scope: [C60.8] closed `trace.ExtractFromHeaders`. It did not reach three seams, and all
  three are closed now on the same terms (ADR-070, amended).
  **HTTP ingress.** `enrichTraceContext` read `req.Header` directly, so the HTTP door was
  the one ingress storing a caller's `traceparent` verbatim and re-emitting it on every
  outbound hop. It is validated with the same spec-exact rule now; the accompanying
  `tracestate` gets the 512-byte cap, a printable-ASCII charset AND the carrier scoping — a `tracestate` arriving with
  no valid `traceparent` to annotate is dropped rather than attached to a freshly minted
  one, and one already in the request context is dropped when this request brings a parent
  of its own. An invalid `traceparent` is treated as ABSENT, never as a reason to reject: the
  request is served and continues on a minted id, byte-identical to the path every request
  without the header already takes.
  **Response reflection.** `ensureTraceParentHeader` echoed the raw request header onto the
  response from six call sites, and the access-log metadata reader read it raw into a log
  field. Both validate their own read now — they read the request header directly, so the
  context-level guard above does not cover them.
  **`tracestate` charset.** The cap is no longer the whole rule at ANY door, this one
  included: a `tracestate` containing a control byte — CR, LF, NUL, ESC — is discarded where
  it used to be stored and re-emitted. Go's header reader already refused those on an
  inbound HTTP request, so this bites the AMQP and outbox doors, where a longstr carries any
  byte. Grammar is still deliberately unvalidated; the charset is a strict superset of the
  W3C list syntax, so no conforming value is affected.
  **AMQP properties and envelope metadata.** `CorrelationId` and `MessageId` are content-header
  PROPERTIES; `Exchange` and `RoutingKey` are `basic.deliver` ENVELOPE metadata. The
  distinction does not change the rule — no header extractor reaches either, since
  `ExtractFromHeaders` reads the `headers` table alone — but it is why no amount of header
  validation was ever going to cover them. The classic consume
  lane read all four raw into log fields, span attributes and metric attributes. They are resolved once per
  delivery now and the one verdict is threaded to every framework sink. `Exchange` answers the
  same rule as the routing key. `CorrelationId` and
  `MessageId` answer to `^[A-Za-z0-9_-]{1,128}$`, the request-id charset — note that a
  dotted or `urn:uuid:`-shaped value from a foreign publisher fails it. `RoutingKey`
  answers to a separate rule: printable ASCII — space allowed, control bytes and non-ASCII
  not — up to the 255-byte shortstr ceiling, because the request-id charset would discard
  the dotted routing key of essentially every real deployment. Only the charset half can
  fire on this door; a consumed routing key is already within 255 bytes by the wire format.
  A value that fails is OMITTED from the sink — no substitute, no truncation — which also
  means a field the delivery never carried is now ABSENT from the log line where it used to
  be present and empty: `message_id` on the DEBUG, success, failure and panic lines,
  `routing_key` and `exchange` on the DEBUG, failure and panic lines,
  `amqp_correlation_id` on the failure and panic lines.
  **Two fields are ADDED, so the omission is searchable.** Every consume line that dropped
  something stamps `identity_rejected: true` — a bounded boolean, never the rejected value —
  and the failure and panic lines stamp `delivery_tag`, the one identifier no publisher
  supplies, so a delivery whose every vouched field was dropped is still attributable to one
  message. A saved query can therefore find affected traffic without knowing what the
  rejected value was. The `messaging/streams` lane is unchanged: it surfaces none of these
  four today, though an AMQP 1.0 message does carry a message id and a correlation id.
  **Two rules, deliberately.** The identifier rule and the routing-key rule are separate and
  neither is a relaxation of the other: `CorrelationId`/`MessageId` are correlation
  identifiers, so they answer to the identifier charset every other door applies, and a
  dotted or colon-separated one fails it. A routing key is an AMQP address, not an
  identifier — every key this framework publishes is dot-delimited and a topic binding
  legally carries `*` and `#` — so applying the identifier charset there would discard the
  routing key of essentially every real deployment. The routing-key rule refuses what
  ADR-070 refuses everywhere (CR/LF, NUL, ANSI escapes) and bounds the value at the AMQP
  shortstr ceiling, and nothing more.
  **What this atom does NOT cover.** The EMIT side is unchanged: `computeTraceParent` still
  prefers a `traceparent` already present in the outgoing header map and takes it verbatim,
  and `extractTraceIDFromParent` still checks the trace-id's length rather than its charset.
  Ingress validation closes that transitively for values the framework itself put there, so
  what remains is first-party code hand-setting `PublishOptions.Headers["traceparent"]` (or
  calling `trace.InjectIntoHeaders` on a pre-populated accessor) and outbox rows persisted
  BEFORE this hop — the relay sanitizes the publish CONTEXT but republishes the stored
  header map as-is, so a pre-upgrade row re-emits its malformed traceparent until the
  backlog drains. Neither is remote-triggerable. Tracked in
  [#1121](https://github.com/gaborage/go-bricks/issues/1121); do not read this atom as
  closing them.
  **The guarantee covers FRAMEWORK sinks only** — the log lines, span attributes and metric
  attributes this framework emits. It is not a guarantee that a raw value is unreachable.
  Your own code still sees the unvalidated bytes in two places by design: a handler receives
  the raw `*amqp.Delivery` (that is deliberate, so a legitimate foreign shape the charset
  refuses is still available to you), and an outgoing header map you pre-populate is taken
  verbatim on the emit side ([#1121](https://github.com/gaborage/go-bricks/issues/1121)). If
  you copy either into your OWN log, span, metric or outbound header, validate it there —
  `trace.ValidateRequestID` and `trace.ValidateTraceParent` are exported for exactly that.
- gate: always — every service serving HTTP, and every service consuming classic AMQP,
  emits at least one of these fields.
- apply: nothing to change in your code — no request is rejected, no delivery is rejected,
  and nothing fails at startup. Note the two failure modes are DIFFERENT and neither is a
  repair. At the HTTP door the caller's `traceparent` is dropped and the framework mints its
  own, exactly as it does for a request that sent none. At the AMQP sinks nothing is minted:
  the field is simply absent from that log line, span or metric. In particular an invalid
  `amqp_correlation_id` is NOT replaced by the framework trace id — `correlation_id` on the
  same line already carries that, and stamping it twice under two names would forge a
  correlation the publisher never asserted.
  Then decide what to do about each upstream the detect
  finds. A gateway emitting a non-spec-exact `traceparent` loses trace linkage through your
  service, as it already did at the messaging doors after [C60.8]. A foreign publisher whose
  `CorrelationId` or `MessageId` is dotted, colon-separated or over 128 characters loses that
  field from your framework log lines and its span attribute — the handler still sees the raw
  `*amqp.Delivery`, so read the property yourself if you need the foreign shape. Repair any
  saved query or alert that treats one of these fields as always present; a query asserting
  `message_id != ""` is the shape most likely to have depended on the empty stamp.
- verify: four probes against a running service; the fourth is broker-dependent.

  1. **HTTP ingress and response.** Run this DIFFERENTIALLY, because validating a
     `traceparent` does not decide a status code — an arbitrary handler legitimately answers
     201, 204, 4xx or 5xx, so a bare "expect 200" would fail on a healthy service. Send the
     same request to the same endpoint twice: once WITHOUT the header, to establish that
     endpoint's baseline status, and once with
     `traceparent: 00-!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!-00f067aa0ba902b7-01` (32 non-hex bytes
     where the trace-id belongs — the shape the old length-only check accepted). Expect the
     status to be UNCHANGED between the two, which is the claim that matters: an unusable
     traceparent is treated as absent and never changes how the request is answered. Then
     expect the RESPONSE `traceparent` on the second call to be spec-exact and NOT the value
     you sent. That much is runnable by every HTTP service, with or without a broker.

     If the service also publishes, point the request at a handler that performs one
     downstream publish and additionally expect a published message whose `traceparent` is
     likewise minted, an `X-Request-ID` equal to that minted traceparent's 32-hex trace-id,
     and a POPULATED `CorrelationId` — before this change the aligned id was 32 exclamation
     marks, which the publish-side guard refused, so `CorrelationId` shipped empty.

     Repeat both with a valid `traceparent` and confirm it propagates end to end unchanged.
  2. **AMQP delivery identity.** Publish to a consumed queue with `correlation_id` set to
     `urn:uuid:0b8a...` (or any value carrying `:` or `.`), `message_id` set to the same
     shape, and a routing key containing a newline, and make the handler fail so the failure
     line is emitted. Expect the failure line to carry no `amqp_correlation_id`, no
     `message_id` and no `routing_key`, to carry `identity_rejected: true` and a
     `delivery_tag`, and the receive span to carry none of
     `messaging.message.conversation_id`, `messaging.message.id` or
     `messaging.rabbitmq.destination.routing_key`. Repeat with `correlation_id: probe-2`,
     `message_id: probe-2-msg` and routing key `user.created` and confirm all three are
     present, unchanged, and that `identity_rejected` is ABSENT — a marker that fired on
     clean traffic would be no signal.

     Then run BOTH of those again with a handler that SUCCEEDS, because the guarantee is not
     failure-scoped: `message_id` reaches the success line too, so expect it present on a
     clean delivery and absent — with `identity_rejected: true` beside it — on the poisoned
     one. If your handlers can panic, force one as well: the panic line is built by the same
     helper as the failure line and carries the same fields, `delivery_tag` included.
  3. **`tracestate` control bytes.** This one needs a NON-HTTP carrier, because Go's own
     header reader already rejects control bytes on an inbound HTTP request — the door this
     probe exercises is the AMQP one, where a longstr carries any byte. Publish to a consumed
     queue with a valid `traceparent` header and a `tracestate` header containing CR, LF, NUL
     or ESC, and have the handler perform one downstream publish. Expect the outbound message
     to carry the inbound `traceparent` and NO `tracestate` at all — dropped, not truncated
     and not emptied. Repeat with a clean `tracestate` such as `vendor=probe` and confirm it
     propagates unchanged, and once more with a clean `tracestate` but NO `traceparent`,
     where it must again be absent outbound: a tracestate with no parent to annotate is an
     orphan, and the carrier scoping drops it rather than attaching it to a minted parent.
  4. **Exchange.** Broker-dependent, and either outcome is a pass. Try to
     `exchange.declare` a name carrying a byte outside printable ASCII and bind your
     consumed queue to it. If your broker REFUSES the declare, that is the answer: the field
     cannot reach the sink from that broker, and nothing changes for you. If it accepts,
     publish through it and expect the failure line to carry no `exchange`, the receive span
     no `messaging.rabbitmq.exchange`, and the consume metrics' `messaging.destination.name`
     to be built with that half empty. Skip the probe entirely if you control every exchange
     name in the deployment — the field cannot fail the shape then, which is why it was the
     last one added rather than the first.
- ref: [ADR-070](adr_070_inbound_trace_identifier_validation.md) (amended 2026-08-20) ·
  [C60.7] · [C60.8] · [C60.10] ·
  [#1121](https://github.com/gaborage/go-bricks/issues/1121) (the emit-side residual this
  atom does not cover) · `server/trace_context.go` (`enrichTraceContext`) ·
  `server/handler.go` (`ensureTraceParentHeader`) · `server/logger.go`
  (`extractRequestMetadata`) · `server/request_utils.go` (`validateTraceParent`) ·
  `messaging/delivery_identity.go` · `messaging/registry.go` (`processMessage`) ·
  `trace/validate.go` (`ValidateTraceState`, new — the tracestate cap is shared as a
  function so a second clause cannot reach one door and miss the other)

### [C60.23] a recovered panic value is reported by type, never by value · breaking · when: match

- detect: every row of the `scope` table breaks a matcher SILENTLY — the query stops
  matching and the alert simply never fires, which reads as "no panics" rather than as a
  broken alert. **The span rows no log grep reaches at all.** Grep your log-based alerts,
  saved queries, dashboards and log-parsing tests against that table, row by row.
  **Start with the HTTP rows (6-8)**, because they are the only ones that need
  no messaging, no scheduler and no `AuditRecorder` to reach you: one panicking request emits
  all of them. In production posture only rows 6 and 7 CHANGE — row 8 is emitted unchanged
  there and moves only under `app.debug: true`, so check it in that posture or not at all. Then the audit sink-failure line (`audit sink panicked; event dropped`), the
  scheduler job-panic line (`Job panicked - recovered and marked as failed`), the settle
  line (`Panic recovered while settling a delivery; not retried`), and — the widest of
  the messaging ones — **the shared delivery outcome line on BOTH messaging lanes**, whose classic
  spelling is `Panic recovered in message handler - discarding without requeue` and whose
  streams spelling is its own constant (`messaging/streams/runner.go`). It fires for any
  consumer whose MESSAGE HANDLER panics, which needs no `AuditRecorder`
  and no scheduled jobs:
  `git grep -nE '(^|[^_a-zA-Z])panic([^_a-zA-Z]|$)|panic_type|error_type|exception\.type|exception\.stacktrace|"exception"|audit sink panicked|Job panicked|Panic recovered|PANIC RECOVER' -- '*.json' '*.yaml' '*.yml' '*.tf'`.
  Two of those arms cover consumers no message-text or `panic`-field pattern can reach.
  **`error_type`** is rows 7 and 8: row 7's production value flips from the panicking value's own
  error type to the CONSTANT `*server.panicTypeError`, so anything GROUPING, faceting or filtering
  on that value silently collapses to one bucket or stops matching. **`exception`** is the SDK
  event itself: `WithoutPanicRecording()` means an unwinding panic emits no exception event at
  all, so a rule keyed on the event NAME, on `exception.type` or on `exception.stacktrace` drops
  to zero hits and never fires again. Neither is a rename you can grep for by its new spelling —
  one is a changed VALUE and the other is an ABSENCE.
  **Do not use `Panic recovered` to tell the lanes apart.** At least four lines share that
  prefix — the delivery settle line, the classic outcome line, the streams outcome line
  (`Panic recovered in stream handler - offset not committed`) and the HTTP error handler's
  own line, whose message is exactly `Panic recovered`. A hit on it in an HTTP-only service
  is that service's own affected line, not a messaging concern to dismiss. `PANIC RECOVER`
  — upper-case — is the one spelling unique to the HTTP side; the bare-word `panic` arm,
  being lower-case, does not match it.

  **Then run a SECOND, DIFFERENT command over your Go and Markdown** — the gate mentions
  log-parsing tests and runbooks, and neither lives in the config globs above:
  `git grep -nE 'panic_type|error_type|exception\.type|exception\.stacktrace|"exception"|audit sink panicked|Job panicked|Panic recovered|panic in message handler|PANIC RECOVER|"panic"|panic !=|\[panic' -- '*_test.go' '*.md' ':!wiki/migrations.md' ':!wiki/adr_*.md'`.
  **The `panic_type` arm stays, and the two pathspec exclusions are why.** `panic_type` is NOT a
  post-bump-only spelling — `[C60.21]` put it on the audit line's `(value unrenderable)` fallback
  in this same hop's predecessor, so a consumer's repo can legitimately hold it BEFORE the bump
  and dropping the arm would miss them. What it must not match is THIS runbook and these ADRs,
  which discuss the field on every page: run inside a go-bricks checkout without the exclusions
  and the arm selects `when: match` off nothing but our own prose.

  **These are two commands with two patterns on purpose; do not merge them.** Adding
  `'*.go'` to the config sweep above turns it from 4 hits into several hundred across the
  tree, because its bare-word `panic` arm matches every ordinary `panic(`
  call in Go source — and a detect step returning several hundred lines is one an
  operator abandons, which is the same failure as returning too few. The source sweep
  therefore drops the bare-word arm and keeps only the field and message spellings: the
  quoted JSON field, the `panic != ""` query form, the `fields: [panic` list form, and
  the five message texts.

  Two things to expect from it. **A log-LEVEL enumeration is the common false positive**
  — `[]string{"debug", "info", "warn", "error", "fatal", "panic"}` matches `"panic"` and
  has nothing to do with this atom — so skim for the field in a log or query context
  rather than the bare word. And if you run it inside a go-bricks CHECKOUT rather than
  your own service, it also matches the framework's own tests and docs; that flood is
  expected and is not your exposure.
  The word-boundary alternation is load-bearing: a pattern matching only the QUOTED
  `"panic"` misses `panic != ""` and a bare field list `fields: [panic, stack]`, which is how
  most query languages name a field. Verified against a fixture holding all four spellings
  over your alerting config, plus the same terms in your log backend's saved searches, which
  no repo grep reaches. Also search for the retired message
  `audit sink panicked; event dropped (value unrenderable)` — it has no successor.

  **Then run the span-side detect, which the log grep above cannot substitute for.** In your
  tracing backend, search span exception messages and status descriptions for
  `panic in message handler`, for `cleanup: tenant` and for `[PANIC RECOVER]` — that last is
  the HTTP SERVER span's status description, which changes on every panicking request in
  any service that serves HTTP at all — and grep alert and SLO definitions
  for `exception.message`:
  `git grep -nE 'exception\.message|exception\.type|exception\.stacktrace|"exception"|otel\.status_description|panic in message handler' -- '*.json' '*.yaml' '*.yml' '*.tf'`.
  The three `exception` arms beyond `.message` are the EVENT-existence consumers: an alert that
  counts exception events, or matches their name or stacktrace, sees a silent drop to zero rather
  than changed text, which no message-shape pattern detects.
  This one deliberately omits `PANIC RECOVER`: it shares the config sweep's globs, so that arm
  would only re-return hits you already triaged. Read the `PANIC RECOVER` hits from the first
  sweep TWICE — once as the HTTP action line (row 6), once as the server span (row 11).
  A shop that alerts on traces rather than logs matches NOTHING in the log grep and is still
  fully affected.
- scope: **the rule is the contract, not the count: after this hop no framework sink carries
  a recovered panic's VALUE — on any sink, not only the log.** Work the table below as a
  checklist rather than trusting a total. That total grew three times while this atom was
  being written, so read the table as the surfaces known TODAY and judge anything it does not
  list against the rule, not against the row count.

  | # | surface | field that changes | before | after | stack |
  | --- | --- | --- | --- | --- | --- |
  | 1 | audit sink-failure line (`migration`) | `panic` → `panic_type` | the value | the Go type | `stack` |
  | 2 | scheduler job-panic line (`scheduler`) | `panic` → `panic_type` | the value | the Go type | `stackTrace` — different name |
  | 3 | delivery settle line (`messaging/internal/delivery`) | `panic` → `panic_type` | the value | the Go type | none — `panic_type` only |
  | 4 | delivery outcome line, classic lane (`messaging`) | `panic` → `panic_type` | the value | the Go type | `stack` |
  | 5 | delivery outcome line, streams lane (`messaging/streams`) | `panic` → `panic_type` | the value | the Go type | `stack` |
  | 6 | HTTP action line (`server`) | `error` | `[PANIC RECOVER] <value> <stack>` | `[PANIC RECOVER] panic (type: T) <stack>` | inside the rendered `error`, not a field |
  | 7 | HTTP error handler's `Panic recovered` line (`server`) | `error_type` in production posture; `error` under `app.debug` | the panicking value's own error type — `*errors.errorString` where Echo rendered a non-error, the concrete type where it panicked with one | `*server.panicTypeError` (a CONSTANT); `panic (type: T)` in debug | `stack` — name unchanged, CONTENT shrinks to the panicking goroutine |
  | 8 | HTTP `unhandled error` line (`server`) | `error` — **`app.debug: true` ONLY** | `[PANIC RECOVER] <value> <stack>` | `[PANIC RECOVER] panic (type: T) <stack>` | inside `error`. In production posture this line is UNCHANGED: its `error_type` is the wrapper `*middleware.PanicStackError` either way |
  | 9 | messaging consume span, BOTH lanes | `exception.message` + status description | `panic in message handler: <value>` | `panic in message handler (type: T)` | n/a |
  | 10 | scheduler job span (incl. `multitenant` cleanup) | `exception.message` ONLY | carries the value | `panic (type: T)` | n/a — its status description is the literal `"panic"` (`scheduler/module.go`) before AND after, so do not repoint that |
  | 11 | HTTP server span | status description | `[PANIC RECOVER] <value> <stack>` | `[PANIC RECOVER] panic (type: T) <stack>` | n/a |

  Rows 6-8 are on the HTTP lane and need no messaging, scheduler or `AuditRecorder` usage —
  one panicking request emits rows 6, 7, 8 and 11 together. In production posture rows 6, 7
  and 11 are the ones that CHANGE; row 8 moves only under `app.debug: true`. Rows 7 and 8 are emitted by
  `server/server.go`, which no diff in this hop touches; its inputs moved instead.
  Six packages report a recovered panic value, and after this hop none of them
  carries the value itself. **The one that reaches every
  service is the HTTP lane**: Echo's `Recover` rendered a handler panic with its own `%v`,
  and both the OTel middleware and the request logger sit OUTSIDE it, so the value went to
  the server span's status description AND the action line's `error` field on every
  panicking request — in production posture, ungated by `app.debug`. Two kinds of site are
  covered, and the second is the one a log-focused reading misses: four sites report the
  value directly to a log field, and three RENDER it into an ERROR that later reaches a
  span and, for the scheduler, a log line too. **`messaging/internal/delivery.AppendOutcome` is the delivery
  SPINE**, shared by the classic AMQP lane and the streams lane, so the rename reaches every
  consumer whose message handler panics — the widest-reaching field rename in this hop, and
  the one a reader scanning for "audit" or "scheduler" will miss. The audit emitter's `deliverToSink` and the scheduler's FR-021
  recovery both replace the log field `panic` (the value, passed through the sensitive-data
  filter) with `panic_type` (the value's Go type, via `%T`). The scheduler's
  `span.RecordError` and summary `Err()` already reported the type after [C60.21] and are
  unchanged. **Relying on the filter here was never protection**: it matches FIELD names, and
  the field is `panic`, which is no needle — so a bare `panic("secret")` was emitted in
  clear, as was a map carrying a key the needle list does not name (`licenseKey`), while a
  key it does name (`password`) was masked. Protection varied with the shape of a value
  chosen by consumer code. The stack trace is retained wherever it was emitted before, but the
  field NAME is not uniform and one line has none — read the `stack` column of the table above
  before keying a repoint on it. `audit_type` / `target` / `jobID` still make each report
  attributable. The audit emitter's second message,
  `(value unrenderable)`, is GONE: it existed only for a value whose rendering panicked, and
  nothing renders the value now.
- gate: match on ANY of — (a) an alert, saved query, dashboard or log-parsing test reads the
  `panic` field, or matches on a panic VALUE, on ANY of the five NON-HTTP lines, rows 1-5 of
  the `scope` table (every HTTP surface is clause (e) instead — none of them carries a
  `panic` field to read) — including a
  lane-shape test of your own that pins the delivery spine's key set, which
  `messaging/internal/lanecontract` treats as a contract and this hop changes;
  (d) an alert, saved query, dashboard, SLO or trace-based test reads `exception.message` or
  the span status description on a messaging consume span or a scheduler job span — that
  text changes independently of the log fields, so satisfying (a) does NOT cover it;
  (e) anything reads any of rows 6-8 or row 11 of the `scope` table for a panicking request —
  **this is the broadest clause in the atom and
  needs no messaging, scheduler or audit usage to apply**: it fires for any service whose
  handler can panic, and one request touches all four rows; (b) you match the literal
  message `audit sink panicked; event dropped (value unrenderable)`; (c) a runbook or triage
  procedure instructs an operator to read the panic value out of these lines. no-match =
  ALL of the following hold:

  - nothing you own reads any log row of the `scope` table — its changed field or its message
    text — including the HTTP rows 6, 7 and 8;
  - you have no lane-shape test pinning the delivery spine's key set;
  - nothing reads `exception.message` or the span status description on rows 9, 10 or 11
    (row 10's status description is unchanged — its `exception.message` is the part that moves);
  - nothing GROUPS, facets or filters on the `error_type` VALUE of rows 7 or 8 — that value
    becomes a constant, so a rule keyed on the old one stops matching without erroring;
  - nothing counts SDK `exception` EVENTS, or matches `exception.type` / `exception.stacktrace`,
    on a panicking span — `WithoutPanicRecording()` means those events stop being emitted at all,
    which reads as "no panics" rather than as a broken rule;
  - no runbook depends on the panic value being present.

  **An HTTP-only service does NOT reach no-match.** Running no scheduled jobs, registering no
  `AuditRecorder` and consuming no messages clears rows 1-5 and 9-10 and changes nothing:
  rows 6, 7, 8 and 11 fire on every panicking request. Clear each of those rows
  separately — satisfying yourself about the action line says nothing about the other three.
- apply: for (a), repoint at `panic_type`, which all of rows 1-5 carry. Do NOT key the repoint
  on `stack` — read the table's `stack` column first: the name differs per row and row 3 has
  none, so a `stack`-keyed rule stops matching the scheduler line silently and never matches
  the settle line.
  For (b), delete the matcher; that message has no successor.
  For (c), rewrite the runbook step around the type and the stack.

  **There is no way to recover the VALUE from any framework sink — that is the point of the
  change, not an oversight.** A procedure that needs it must get it from a debugger, a core
  dump, or the consumer code that panics. Do not send an operator hunting the log for a
  value that is no longer anywhere: it is not in the log field, not in `exception.message`,
  not in the span status description, and not in the returned error.

  **The `exception.message` half of that is true only because of a provider option, and it
  costs you an event.** The OTel SDK records an exception event itself, carrying the value,
  on any span that unwinds with a live panic — so before this hop the value reached your
  tracing backend from six framework sites regardless of what the code around them spelled.
  `sdktrace.WithoutPanicRecording()` closes it. **The consequence to plan for: an unwinding
  panic no longer produces an exception event on that span at all.** If you alert on
  `exception` events, or count them, a panic that used to surface as one now surfaces only
  as the span's error status and the framework's own log line with `panic_type` and the
  stack. That is a silent reduction in event volume, not an error — check any monitor whose
  threshold was tuned against the old rate.

  **For (d), the SPAN side, which is a separate repoint from the log side.** On both
  messaging lanes `exception.message` and the span status description change from
  `panic in message handler: <value>` to `panic in message handler (type: T)`. The
  scheduler's cleanup-job error changes the same way — `multitenant`'s per-tenant cleanup
  used to convert a panicking `RetentionDelete` callback into an ordinary error carrying the
  value, one frame below the scheduler, so it reached `span.RecordError`, the span status
  AND the job summary line. Repoint span-based alerts on their own; **an alerting rule keyed
  on `exception.message` sees a break that no amount of log-field repointing describes.**

  **For (e), the HTTP lane — the broadest repoint here.** The action line's `error` field and
  the server span's status description change from `[PANIC RECOVER] <value> <stack>` to
  `[PANIC RECOVER] panic (type: T) <stack>`. Repoint anything matching the value; the type and
  the stack remain, and the stack still names the function that panicked. The stack also
  SHRINKS — it now covers the panicking goroutine only, where it used to dump every goroutine
  up to 4 KB — so a query keying on an unrelated goroutine's frames stops matching.
  **This lane emits no `panic_type` field**, unlike the other five: the type lives INSIDE the
  rendered `error` string (`server/middleware.go`'s `panicTypeError` renders
  `panic (type: T)`, and Echo's `PanicStackError` wraps it as
  `[PANIC RECOVER] <that> <stack>`), and so does the stack. Repoint this lane with a
  substring match on `panic (type:`, never by adding a `panic_type` field selector — no HTTP
  row carries that field, and a field-based rule matches nothing there.

  **Rows 7 and 8 repoint differently from row 6, and which field moves is gated on
  `app.debug`** — read those rows' `field that changes` column. Two consequences worth
  planning for: row 7's new production value is a CONSTANT, so a dashboard that used to
  spread panics across concrete error types now shows one bucket; and row 8 changes under
  `app.debug: true` ONLY, so a production-posture shop can skip it entirely.

  **Read this if you handle a disclosure question. Audit TWO backends, and only one of the
  three exposures was gated on `app.debug`.**

  1. **Your TRACING backend — the worst half, and ungated.** Before this hop a handler that
     panicked with anything sensitive put that value off-platform on every panicking HTTP
     request and on every panicking delivery on both lanes, in every deployment posture.
  2. **Your LOG backend, via the HTTP action line (row 6) — also UNGATED.** That line's
     `error` field carried Echo's rendering of the raw panic value in every posture,
     production included: `server/logger.go`'s `Err()` has no `app.debug` condition, and
     `LogEvent.Err` applies no filtering at all, so the sensitive-data filter never saw it.
     Do not skip your log backend because you run production posture.
  3. **Your LOG backend, via rows 7 and 8 — `app.debug: true` ONLY.** In production posture
     those two lines carried a TYPE (`error_type`), never the value, so a deployment that
     never set `app.debug: true` has no exposure from them. Check whether any environment
     you run — staging and ad-hoc debugging sessions included — ever did.

  All three are now closed, and all three were open in every prior release. If your handlers
  can panic with credentials or PII, treat the historical span data AND the historical log
  data as exposed and audit both separately; upgrading stops the bleeding and does not clean
  up what already shipped.
- verify: for (a), in staging make an `AuditRecorder.Record` panic with a synthetic secret
  (`panic("not-a-real-secret-0000")`) and make a scheduler job panic the same way. **Use a
  synthetic value on a disposable sink** — the whole point is that you do not yet know
  whether it is disclosed, and a log you cannot delete is a rotation, not a test. On both
  lines the secret must be ABSENT and `panic_type` present (`string` for that example), the
  migration must COMPLETE, and the job must be recorded as FAILED with `failureCount`
  incremented — the accounting runs after the reporting, so a report that failed must not
  cost the outcome record. Then confirm your repointed alert fires on that run.
  For (b), grep your alerting config and saved searches for the retired message once more
  after repointing and expect ZERO hits; a matcher left behind does not error, it just never
  fires again. For (c), walk the runbook step against that same staged panic with only the
  log in front of you: an operator must be able to complete it from `panic_type` and the
  stack trace alone. If the step cannot be completed, the value was load-bearing for that
  procedure and the procedure needs rewriting now rather than during an incident.
  For (d), use the SAME staged panic but look at the trace, not the log: make a message
  handler panic with a synthetic secret and read the consume span. `exception.message` and
  the span status description must read `panic in message handler (type: string)` with the
  secret ABSENT. Do the scheduler half by making a `RetentionDelete` callback panic and
  reading the job span. For (e), make an HTTP handler panic with a synthetic secret against a
  staging service running `app.debug: false`, then read BOTH the action log line and the
  server span: the secret must be absent from the log's `error` field and from the span status
  description, and both must read `panic (type: string)`. That one request also emits rows 7
  and 8 — read them from the same capture and confirm row 7's `error_type` is
  `*server.panicTypeError`. **Only if some environment you run sets `app.debug: true`**,
  repeat the run in that posture and confirm rows 7 and 8 read `panic (type: string)` rather
  than the secret; a shop that runs production posture everywhere can skip that second run,
  since row 8 changes in debug only. **Check every row even if the first passed** — they
  changed for different reasons and a probe of one cannot tell you about the others.
- ref: [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) ·
  [ADR-079](adr_079_log_filter_walks_slices_without_comparing.md) (its primary-path claim is
  corrected in place) · [ADR-019](adr_019_migration_audit_delivery.md) ·
  `migration/audit_emitter.go` · `scheduler/module.go` · `server/middleware.go` ·
  `server/server.go`

### [C60.27] every Filter and JoinFilter column is a validated identifier · breaking · when: match

- detect: `git grep -nE '\.(Eq|NotEq|Lt|Lte|Gt|Gte|In|NotIn|Like|Regex|RegexI|NotRegex|NotRegexI|JSONContains|Null|NotNull|Between|InSubquery|EqColumn|NotEqColumn|LtColumn|LteColumn|GtColumn|GteColumn)\(' -- '*.go'`
  lists every call site. Keep the pattern POSIX ERE — `git grep -E` silently ignores the PCRE escapes.
  Nothing is compiler-caught; the failure is an error returned from `ToSQL()`. Read only the sites
  whose COLUMN argument (the first, and for the `*Column` forms both) is not a literal or a
  `cols.Col(...)` lookup.
- scope: `database/internal/builder/` only. `quoteColumnForQuery` — the single point every column
  argument passes through before becoming SQL — now validates against the ADR-031 identifier grammar
  and returns an error, so all 18 `FilterFactory` and 18 `JoinFilterFactory` column doors, the six
  comparison helpers, `BuildRegex`, `BuildJSONContains`, `BuildCaseInsensitiveLike` and the UPDATE SET
  targets refuse a column that is not a bare or qualified identifier. A violation surfaces from
  `ToSQL()` through the existing deferred-error Sqlizer, including from inside `And`/`Or`/`Not` and
  from a subquery.
  This is the stage that matters most on PostgreSQL. `quoteColumnForQuery`'s default branch rendered
  the column VERBATIM, so before this hop a Filter column was interpolated exactly as written and
  `f.Eq("id = 1 OR 1=1 -- ", v)` emitted `WHERE id = 1 OR 1=1 -- = $1`. Oracle quoted the column, so
  the same input was inert there.
- gate: match = any Filter or JoinFilter column argument is not a literal or `cols.Col(...)` output.
  no-match = every column is developer-written. A census of this repository found 1027 such call sites
  and none outside the grammar, so most codebases will not match.
- apply: pass a name, not an expression. A computed comparison belongs in `f.Raw(...)`/`jf.Raw(...)`,
  which carry the `// SECURITY: Manual SQL review completed - <rationale>` annotation requirement; a
  request-derived column needs an allowlist your own code owns, mapping the caller's value to one of a
  fixed set before it reaches the builder.
- verify: both directions. Assert the filters you intend to ship still build, then feed one known-bad
  column (`id = 1 OR 1=1 -- `) to the same door and assert `err != nil` reading `invalid column
  identifier`. Checking only that good columns still build passes whether or not the validation runs.
- ref: [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) · issue #1143

---

### [C60.26] SELECT and INSERT column lists are validated identifiers · breaking · when: match

- detect: `git grep -nE 'Select\(|InsertWithColumns\(|\.Columns\(|\.SetMap\(' -- '*.go'` lists every
  call site. Keep the pattern POSIX ERE — `git grep -E` silently ignores `\b`, `\s`, `\d` and `\w`.
  Nothing is compiler-caught; the failure is an error returned from `ToSQL()`. The grep alone does not
  answer the question, because a multi-argument `Select` hides its expression strings from any pattern
  that only sees the first argument — `Select("department", "COUNT(*)")` reads as a plain column list.
  Run the build after the bump and let the errors enumerate them, or hand-read every `Select` taking
  more than one string.
- scope: `database/internal/builder/` only. `QueryBuilder.Select` validates each string column against
  a new `select` identifier context — the ADR-031 grammar plus the wildcard, so `*` and `t.*` are
  accepted. `QueryBuilder.InsertWithColumns`, `InsertQueryBuilder.Columns` and
  `InsertQueryBuilder.SetMap` validate their columns against the plain identifier grammar;
  `InsertQueryBuilder.SetMap` is the notable one, since `UpdateQueryBuilder.SetMap` — the same shape on
  the sibling builder — has validated its keys since ADR-031 and the two disagreed until now.
  A `dbtypes.RawExpression` from `qb.Expr()`/`MustExpr()` passes through untouched; only strings are
  judged. Filter and JoinFilter column arguments are NOT in this hop — they follow in #1143.
- gate: match = any `Select` argument is a string that is not a bare/qualified identifier or a
  wildcard, OR any INSERT column list carries a non-identifier. no-match = every column is a plain
  name, a `cols.Col(...)` lookup, or already a `qb.Expr(...)`.
- apply: move the expression into `qb.Expr(...)`. Three shapes break, and only the first is obvious:
  the `EXISTS` idiom `qb.Select("1")` becomes `qb.Select(qb.MustExpr("1"))`; a bare function string
  `qb.Select("COUNT(*)")` becomes `qb.Select(qb.MustExpr("COUNT(*)"))`; and a function carrying an
  alias splits, so `qb.Select(colID, "COUNT(o.id) AS order_count")` becomes
  `qb.Select(colID, qb.MustExpr("COUNT(o.id)", "order_count"))` — the alias is the second argument to
  `Expr`, not part of the SQL. `qb.Select("*")` is unaffected.
- verify: both directions. Assert the columns you intend to ship still build (`err == nil` from
  `ToSQL()`), then feed one known-bad column (`id; DROP TABLE users--`) to the same door and assert
  `err != nil`. The message names the door, and the three spellings differ — `invalid select
  identifier` from `Select`, `invalid insert column identifier` from `InsertWithColumns` and
  `.Columns`, and `invalid SetMap column identifier` from `InsertQueryBuilder.SetMap` — note
  `UpdateQueryBuilder.SetMap` reports `invalid column identifier` instead, because it validates
  through the shared column funnel that `[C60.27]` introduces — so match on
  `invalid ` and the identifier itself rather than on one fixed phrase. Checking only that good
  columns still build passes whether or not the validation runs.
- ref: [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) · issue #1143

---

### [C60.24] every INSERT and upsert door validates its table argument · breaking · when: match

- detect: `git grep -nE 'Insert\(|InsertWithColumns\(|InsertStruct\(|InsertFields\(|BuildUpsert\(' -- '*.go'`
  lists every call site. Keep the pattern POSIX ERE — `git grep -E` silently ignores `\b`, `\s`,
  `\d` and `\w`. Nothing is compiler-caught; the failure is an error returned from `ToSQL()`
  (or from `BuildUpsert` directly). The grep finds the calls but not the risk: a literal table
  name always passes, so read only the sites whose table comes from a parameter, a config value,
  a struct field or a concatenation.
- scope: `database/internal/builder/` only. `Insert`, `InsertWithColumns`, `InsertStruct`,
  `InsertFields` and `BuildUpsert` now validate their `table` against the same grammar
  `From`, `Update` and `Delete` have applied since ADR-031 — a simple or qualified identifier
  (`users`, `app.users`, `schema.app.users`) plus at most one inline alias (`users u`). Anything
  else — whitespace beyond the alias, a semicolon, a `--` or `/* */` comment, parentheses, a
  function call, extra tokens — is refused. A table sits FIRST in every one of these statements,
  so a trailing comment used to take the rest of the statement with it, which no column
  precondition could catch. The five doors were the last interpolating a table unchecked.
- gate: match = any of those five is called with a table argument that is not a literal, OR with
  a literal carrying anything beyond a name and an optional alias. no-match = every call passes a
  bare or qualified literal.
- apply: pass a name, not an expression. A dynamic table needs an allowlist of names your own
  code owns — map the caller's value to one of a fixed set before it reaches the builder — since
  the builder will not accept a computed one. A table plus alias stays legal (`"users u"`), and
  `Table("users").As("u")` is unchanged.
- verify: two directions, because one of them is what the atom is for. Call each affected site
  with the name you intend to ship and assert it still builds — `err == nil` from `ToSQL()`, or a
  nil third return value from `BuildUpsert`. Then feed one KNOWN-BAD name (`users; DROP TABLE x--`)
  to the same site and assert the opposite: `err != nil`, reading `invalid table identifier "…":
  must be a simple or qualified identifier with an optional alias`. A suite that only checks the
  first direction passes unchanged whether or not the validation runs at all.
- ref: [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) · issues #1104, #1143

---

### [C60.25] both identifier renderers double an interior quote · silent-behavior · when: match

- detect: `git grep -nE 'EscapeIdentifier\(' -- '*.go'` for the exported helper, then search your
  schema for a column or table whose NAME contains a double quote — `information_schema.columns`
  on PostgreSQL, `ALL_TAB_COLUMNS` on Oracle, `WHERE column_name LIKE '%"%'`. A clean catalog is
  NOT the whole answer: an ALIAS exists only in the statement that names it and appears in no
  catalog, so also `git grep -nE '\.As\(|" [A-Za-z_]"' -- '*.go'` over your own builder calls for
  an alias carrying a quote. Both clean means this atom cannot affect you.
- scope: `oracleQuoteIdentifier` and `QueryBuilder.EscapeIdentifier` wrapped a name in quotes
  without doubling the quotes inside it, so a name carrying one ended the identifier early and
  the remainder was parsed as SQL rather than as part of the name. Both now double it, which is
  how Oracle and PostgreSQL each spell a quote inside a quoted identifier. The pass collapses
  before it doubles, so a name already written in escaped form (`a""b`, denoting `a"b`) is
  unchanged rather than renamed. PostgreSQL identifiers that were emitted bare are still emitted
  bare — nothing gains quoting, so no case-folding changes.
- gate: match = a column, table or alias name in your schema contains a double quote.
  no-match = none does, which is the ordinary case.
- apply: nothing to change. SQL that used to fail at parse for such a name now builds correctly;
  SQL that used to execute as something other than what the name said now names the column. If
  you had worked around the old rendering by pre-doubling a quote yourself, that still works —
  the pass is idempotent.
- verify: hand `qb.EscapeIdentifier` a name whose only quote is a lone one. It comes back with
  that quote doubled and the whole name wrapped, where it used to come back wrapped with the
  quote untouched — one identifier now, two SQL tokens before.
- ref: [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) · issue #1104

---

### [C60.22] the client IP is derived from observed hops only, and a trusted-proxy list covering an address family fails startup · breaking · when: match

- detect: `git grep -nE 'trustedproxies' -- '*.yaml' '*.yml' '*.json' '*.toml'` and the same
  sweep over your deployment surfaces (Helm values, Kustomize overlays, `.env` files,
  rendered manifests) for `DEBUG_TRUSTEDPROXIES`, `SCHEDULER_SECURITY_TRUSTEDPROXIES` and
  `SERVER_TRUSTEDPROXIES` — all THREE, because the widening below reaches the third key too.
  **A `0.0.0.0/0` or `::/0` entry in the first two now fails startup**, and so does a list whose
  entries TOGETHER cover a family (`["0.0.0.0/1","128.0.0.0/1"]`) or the v4-mapped default
  route `::ffff:0.0.0.0/96` — read the values, not just the key. Do not stop at the two
  lenient keys: only the literal default route was already refused on
  `server.trustedproxies`, so a split-coverage or v4-mapped list fails on ALL THREE keys
  now and setting them consistently does not make you unaffected. The exposure came from
  the two lenient keys accepting what the third refused; the widening reaches all three.
  Then check `debug.allowedips` on BOTH rules. An entry that is neither an IP address nor a
  CIDR range is refused — a typo there used to deny everything silently at runtime and now
  names itself at startup — and so is one that parses cleanly as a CIDR but has HOST BITS
  SET (`192.168.1.55/16`), which used to silently widen the allowed range to
  `192.168.0.0/16`. An entry that clears the first rule can still fail the second, so read
  both; the remedy is the canonical network address or the single host without a prefix.
  On the behavior side your code cannot tell you anything: the derived address depends on
  what a caller sends. Search your ACCESS LOGS for debug-endpoint or `/_sys/job` requests
  that were ALLOWED and whose recorded client IP does not match the peer you expect —
  particularly any allowed request carrying `X-Real-IP`, which is no longer consulted.
- class note: this atom is `breaking`, not `silent-behavior`, because half of it fails
  startup outright — a list trusting an entire address family aborts the process on ANY of
  the three keys, which no operator can discover from a log search. The derivation change IS silent, and the hop row
  lists this atom under both categories for that reason; the header takes the louder of the
  two so a reader triaging by class does not skip a startup failure.
- scope: `server.ClientIP` derives the address for the framework's two IP-based
  access-control checks — the debug-endpoint allowlist and the scheduler CIDR middleware
  guarding `/_sys/job`. Both fail closed on an unparseable address but fail OPEN on one
  that parses and is wrong, and five defects could produce exactly that.
  It now follows one rule: **the answer is either an identified untrusted hop, or the peer
  address actually observed — never a value the caller wrote.** Concretely: every
  `X-Forwarded-For` field LINE is read and joined (`Header.Get` read only the first, so a
  proxy that adds a second line left the real chain invisible); `[`/`]` are stripped per
  entry (a bracketed IPv6 client used to be denied); an entry that carries no readable address STOPS the
  walk and yields the peer rather than continuing left into a caller-authored entry — that
  means an RFC 7239 `for=_hidden` or `unknown`, NOT a port suffix or IPv6 brackets, both of
  which are normalized away first (`192.0.2.1:443` and `[2001:db8::1]:443` both resolve); a chain whose hops are all trusted
  yields the peer rather than the left-most entry; a hop that is loopback, unspecified or
  link-local yields the peer too, because no proxy observes a real client at those
  addresses and they are exactly what the shipped allowlists contain; and `X-Real-IP` is
  not consulted at all,
  which is what ADR-057 decided and this function alone never implemented.
  All three trusted-proxy keys now reject a trust list whose MERGED coverage spans an
  entire address family — not merely one whose entry is a literal default route. A single
  offending entry keeps the message `'%s' trusts every address, which restores
  X-Forwarded-For spoofing`; a set that does it together names the set. This catches
  `["0.0.0.0/1","128.0.0.0/1"]` and `["0.0.0.0/1","128.0.0.0/2","192.0.0.0/2"]`, which no
  per-entry rule reaches, and `::ffff:0.0.0.0/96` — a v4-mapped default route that measures
  96 of 128 mask bits while matching every IPv4 address. `server.trustedproxies` answers to
  the same rule at startup AND at `server.New`, so a service constructed without
  `config.Validate` cannot trust everyone either. `server.ParseCIDRs` enforces the
  same rule at the runtime door, treating a total-coverage list as no trust list at all,
  because it is exported and a caller outside app construction never passes through
  `config.Validate`. The lenient
  partial-invalid tolerance on the debug and scheduler keys is UNCHANGED — a single typo
  still does not disable the whole list. What is newly refused on those two keys is any
  list trusting an ENTIRE ADDRESS FAMILY: a literal default route, a set covering one
  between its entries, or the v4-mapped `::ffff:0.0.0.0/96`. `server.trustedproxies`
  already refused the literal form and newly refuses the other two, so the widening
  reaches all three keys, not just the lenient pair.
  `debug.allowedips` gains CIDR-syntax validation that accepts BARE ADDRESSES, because the
  shipped default `["127.0.0.1","::1"]` is not CIDR notation. It also accepts an entry
  wrapped in single or double quotes, because the RUNTIME parser has always stripped those
  — a shell-quoting slip such as `DEBUG_ALLOWEDIPS='"127.0.0.1"'` works today and keeps
  working, rather than becoming a startup failure the runtime would have served fine.
  **Allowlists are deliberately exempt from the default-route rule.** `["0.0.0.0/0"]` on
  `debug.allowedips` or `scheduler.security.cidrallowlist` stays valid — ADR-049 recommends
  it. An allowlist that admits everything is a posture; a trust list that trusts everything
  restores header spoofing.
- preflight: **capture one real `X-Forwarded-For` from production before you bump and look
  at what your proxies actually append.** An entry the walk cannot READ now stops the walk
  and yields the peer — a trusted proxy your allowlist almost certainly does not contain —
  so such a request is DENIED where it previously resolved to something.
  Only one shape is affected, and it is rare: an RFC 7239 obfuscated identifier
  (`for=_hidden`) or `unknown`, emitted where a proxy is deliberately concealing the
  client. Those carry no address at all, so no parser can recover one; if you find one,
  fix the proxy before upgrading or accept that those requests are denied at both doors.
  **Port-suffixed and bracketed entries need NO action** — `192.0.2.1:443`, which AWS ALB's
  `routing.http.xff_client_port.enabled` appends on every request, and `[2001:db8::1]:443`
  are both read correctly now. So is a bracketed IPv6 entry without a port, which used to
  be denied and now works. Refusing to read a shape a documented load-balancer toggle
  emits would have been this framework exporting a parser limitation to operators as an
  incident, which is why it is parsed rather than warned about.
- gate: match — you are affected if you set ANY of the three trusted-proxy keys,
  `server.trustedproxies` included: its per-entry rule was already strict, but the
  set-coverage and v4-mapped rules are new to it too, so a list it accepted yesterday can
  abort startup today. You are also affected if either access-control path runs behind a
  proxy — and **setting none of the three keys does NOT make you unaffected**. Echo trusts
  loopback, link-local and RFC1918 ranges by DEFAULT (`server/server.go`), so an in-VPC
  service behind a load balancer consults `X-Forwarded-For` with no configuration at all, and
  the derivation change reaches it. Only a deployment whose immediate peer is outside those
  default ranges AND which sets none of the three keys is unaffected, because no hop it
  observes is trusted and the headers are never consulted for it.
- apply: three NEW startup failures, all of them naming the key and the remedy. A trust
  list covering an entire address family (see scope) now aborts, as does a
  `debug.allowedips` entry that is neither an IP nor a CIDR, or one with host bits set
  (`192.168.1.55/16` silently admitted 65,536 hosts where one address was written — the
  same widening the proxy keys already refuse, now in the same words). Note
  `debug.allowedips` is validated **even when `debug.enabled: false`**: a block you wrote
  must be valid whether or not it is registered, so a typo surfaces at deploy time rather
  than during the incident in which someone flips it on.
  Then remove the total-coverage list from whichever of the THREE trusted-proxy keys holds
  one — a literal default route, a set covering a family between its entries, or the
  v4-mapped form — and list the specific proxy ranges instead — that is the startup error's own instruction, and it is the fix for
  the exposure, not a formality. If you relied on `X-Real-IP` reaching either access-control
  check, switch the proxy to append to `X-Forwarded-For`; there is no configuration that
  restores the old behavior, deliberately. If a legitimate client was being denied because
  your proxy brackets IPv6 XFF entries, that now works.
  Only `config.Validate` ABORTS. The two runtime doors refuse the same lists without an
  error: `server.New` installs no trust options and logs, and `server.ParseCIDRs` returns
  nil nets with every trimmed entry reported invalid. A service built without
  `config.Validate` therefore BOOTS trusting nobody rather than failing — same security
  outcome, no crash to alert on, so check the log line rather than waiting for a restart.
- verify: three probes against a running service.

  1. **Startup.** Set `debug.trustedproxies: ["0.0.0.0/0"]` and start. Expect a startup
     failure naming `debug.trustedproxies` and saying it "trusts every address". Repeat for
     `scheduler.security.trustedproxies` AND `server.trustedproxies`. Then repeat all three
     with `["0.0.0.0/1","128.0.0.0/1"]` and with `["::ffff:0.0.0.0/96"]` — those are the
     shapes no per-entry rule catches, so a probe using only `0.0.0.0/0` passes without
     testing the rule this atom is about. Probe the `debug.allowedips` host-bits rule on its
     own too — set `debug.allowedips: ["192.168.1.55/16"]` and expect a startup failure naming
     the key; an entry that is merely unparseable exercises the other rule, not this one, which
     parses cleanly and widens. Then set `debug.allowedips: ["0.0.0.0/0"]` and
     confirm it still starts — the allowlist exemption is deliberate, and a failure there
     would be a regression.
  2. **The bypass, at the debug door.** With a legitimate `debug.trustedproxies` (a real
     proxy range that does NOT contain your test client) and the default
     `debug.allowedips`, send a request directly to a debug endpoint carrying
     `X-Real-IP: 127.0.0.1`, then again carrying `X-Forwarded-For: 127.0.0.1`. Expect 403
     for both. Before this change, a default route in that key made both succeed.
  3. **`/_sys/job`.** Same two requests against the scheduler endpoint with the shipped
     empty allowlist (localhost-only). Expect 403 for both. Then confirm a genuine
     request from an allowlisted address still succeeds. At RUNTIME this change denies
     only where it previously granted wrongly, and it newly GRANTS normalized forms a
     proxy really emits (bracketed IPv6, port-suffixed entries) that used to be denied —
     so a legitimate caller still being denied means your trusted-proxy list no longer
     describes your topology. A total-coverage list never reaches this test: it fails
     startup.
- ref: [ADR-080](adr_080_client_ip_answers_only_from_observed_hops.md) ·
  [ADR-057](adr_057_trusted_proxy_ip_extraction.md) (completed by it; its comparison table
  is retired in an amendment) · [ADR-049](adr_049_debug_endpoints_fail_closed.md) (never
  examined IP derivation, which is why this survived it) · `server/clientip.go` ·
  `config/validation.go` (`rejectTotalCoverage`, `CoversAddressFamily`,
  `validateIPOrCIDRList`)
- residual: an earlier draft of this atom recorded `["0.0.0.0/1","128.0.0.0/1"]` as
  passing unchanged, with a union-coverage check named as a follow-up. That is obsolete:
  the check SHIPPED here, and the three doors answer it in two different ways.
  `config.Validate` FAILS STARTUP on such a list at any of the three keys. The two runtime
  doors do not fail, they DROP: `server.New` installs no trust options and logs an error,
  and `server.ParseCIDRs` returns no error at all — it hands back no trust nets plus every
  trimmed entry as invalid, so the caller's own WARN names them. Either way the list ends
  up trusting nobody rather than everybody. What remains is posture, not parsing — a trust
  list that
  correctly describes its topology still believes whatever those proxies append, which is
  identification and not authorization ([ADR-043](adr_043_forwarded_client_cert.md)).

---

### [C60.30] the 5xx response body's error detail requires `app.debug` AND a development env · silent-behavior · when: match

- detect: grep every deployment surface — YAML, `.env` files, Helm values, Kustomize overlays,
  rendered manifests — for `app.env`/`APP_ENV` set to a development alias (`development`, `dev`,
  `local`; the comparison lowercases and trims, so `Dev ` counts) held together with
  `app.debug`/`APP_DEBUG` set to `false` or left unset. That pairing is the only one whose
  emitted body changes. A deployment whose `app.env` is not a development alias was already
  clean, and one that runs `app.debug: true` in a development environment is unchanged.
- scope: `classifyError` attached `details.error` — the raw `err.Error()` of an unhandled 5xx,
  or Echo's `[PANIC RECOVER] …` string with the captured stack for a recovered panic — to the
  response body under `cfg.App.IsDevelopment()` alone, while both LOG sinks withheld the same
  text under `cfg.App.Debug`. The two sinks could therefore disagree, and they disagreed in the
  wrong direction: an operator who turned `app.debug` off while `app.env` stayed a development
  alias silenced the copy they could read and kept shipping the copy the CALLER reads. The body
  now shares the log gate and adds the environment requirement on top — `cfg.App.Debug &&
  cfg.App.IsDevelopment()` — so the stricter of the two keys wins and the sinks cannot diverge.
  Nothing about the LOG paths changes, and the debug rendering still carries what ADR-081
  leaves it: the panic's TYPE, never its value, since `sanitizePanicValue` has already replaced
  the value one frame lower. The `details` map itself is not otherwise touched — a development
  build still renders a handler-supplied `details` entry and the captured stack frames under
  the environment gate alone.
- gate: match = `app.env` is a development alias AND `app.debug` is false or unset.
  no-match = any other pairing, including every production posture.
- apply: nothing to change if the loss of that detail is what you wanted. If you were reading
  `details.error` off a development service's 500 responses — a smoke test, a local debugging
  script, a contract fixture pinning the key's presence — set `app.debug: true` in that
  environment to get it back, or read the framework log's `error` field instead, which the same
  key has always gated.
- verify: against a service running `app.env: development` with `app.debug: false`, make a
  handler panic and make another return a plain error. Both responses must decode to an
  `error.details` map with NO `error` key, where the panic response previously carried
  `[PANIC RECOVER] panic (type: string)` plus the stack. Then flip `app.debug: true` and repeat:
  the key is back on both. Check BOTH routes — the panic path and the unhandled-error path reach
  the same sink but through different middleware, and a probe of one does not answer for the other.
- ref: [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) (addendum: the response-body
  sink shares the debug gate) · `server/server.go` (`classifyError`) · issue #1140

---

### [C60.29] a `RawExpression` struct literal is validated where it is consumed · breaking · when: match

> **Superseded in part on the E61 hop by `[C61.9]`.** The alias check described below is a
> six-substring denylist, and a denylist accepts everything it does not enumerate — `[C61.9]`
> replaces it with the identifier grammar and deletes `ErrDangerousAlias`. Read this atom for the
> funnel (which is unchanged and still correct); take the alias rules from `[C61.9]`.

- detect: `git grep -nE 'RawExpression\{' -- '*.go'` lists every struct literal — the only
  construction path this hop changes. Keep the pattern POSIX ERE; `git grep -E` silently ignores the
  PCRE escapes. Nothing is compiler-caught: `RawExpression` stays a plain exported struct with
  exported fields, and the failure is an error returned from `ToSQL()` (or, on a JoinFilter, from the
  filter's own `ToSQL()`). Values from `qb.Expr(...)`/`qb.MustExpr(...)` are unaffected — they already
  passed the same checks at construction.
- scope: `database/types/` and `database/internal/builder/`. `RawExpression` gains an exported
  `Validate() error` — the single funnel `Expr()` now calls at construction and every door that
  interpolates an expression calls again at consumption: `QueryBuilder.Select` (including the
  `[]RawExpression` and `[]any` forms), `SelectQueryBuilder.GroupBy`, `SelectQueryBuilder.OrderBy`,
  and the `JoinFilterFactory` value doors `Eq`, `NotEq`, `Lt`, `Lte`, `Gt`, `Gte` and both bounds of
  `Between`. It rejects two things and nothing else: SQL that is empty or whitespace
  (`ErrEmptyExpressionSQL`) and an alias containing `;`, `'`, `"`, `--`, `/*` or `*/`
  (`ErrDangerousAlias`). The SQL body itself is still NOT validated — that is what the escape hatch is
  for, and the caller still owns it.
  The alias is the injection vector `[C60.26]` left open. `Select` renders it as `<sql> AS <alias>`
  verbatim, so `RawExpression{SQL: "1", Alias: "x FROM users; DROP TABLE t--"}` emitted
  `SELECT 1 AS x FROM users; DROP TABLE t-- FROM users` on both vendors, while the same alias through
  `qb.MustExpr` had always panicked. The guard existed; it was optional, because the type it guards is
  constructible without it.
- gate: match = a `RawExpression` is built as a struct literal anywhere. Constant fields are NOT an
  exemption — a hardcoded `Alias: "total--"` is refused exactly like a computed one, so every literal
  has to be read. no-match = every expression comes from `qb.Expr(...)` / `qb.MustExpr(...)`, which
  already ran the same check at construction. A census of this repository found no struct literals
  outside tests and `Expr()` itself.
- apply: build expressions through `qb.Expr(sql, alias)` — it returns the same error, at construction,
  where the stack still names your code. A request-derived alias needs an allowlist your own code
  owns, mapping the caller's value to one of a fixed set before it reaches the builder; passing it
  through the builder does not make it safe, because the check is a denylist and an alias avoiding the
  six sequences is accepted (see `residual:` below). `MustExpr` keeps panicking, so reserve it for
  static initialization.
- verify: both directions. Assert the expressions you intend to ship still build (`err == nil` from
  `ToSQL()`, and the SQL is byte-identical to what the same expression rendered before), then feed one
  known-bad literal (`RawExpression{SQL: "1", Alias: "x--"}`) to the same door and assert
  `errors.Is(err, dbtypes.ErrDangerousAlias)` — on v0.61.0 that sentinel is gone and the assertion
  is `errors.Is(err, dbtypes.ErrInvalidAlias)` (`[C61.9]`). Checking only that good expressions
  still build passes whether or not the validation runs.
- residual: **CLOSED on the E61 hop by `[C61.9]`, which replaces the denylist with the identifier
  grammar.** As shipped here the alias check is a denylist, not a grammar: an alias carrying none of
  the six sequences still renders verbatim — `Alias: "a, (SELECT password FROM users) b"` is accepted
  by `qb.Expr()` and therefore by every door. This hop makes the two construction paths agree; it does not narrow what
  they accept. Treat an alias as developer-controlled input, the same contract the expression SQL
  carries.
- ref: [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) (2026-08-23 addendum) ·
  issue #1153 · `database/types/expression.go` (`RawExpression.Validate`)

---

### [C60.28] the table alias handed to `As` is a validated identifier · breaking · when: match

- detect: `git grep -nE 'Columns\(&?[A-Za-z_]' -- '*.go'` finds the metadata handles, then
  `git grep -nE '\.As\(' -- '*.go'` every alias call on one. Keep the pattern POSIX ERE —
  `git grep -E` silently ignores `\b`, `\s`, `\d` and `\w`. The second grep also matches
  `TableRef.As` and every other `As` in your tree, which is NOT this door — read the receiver.
  Nothing is compiler-caught and nothing surfaces from `ToSQL()`: the failure is a PANIC at
  the `As` call. A literal alias passes only if it matches the grammar below — an ordinary one
  (`"u"`) does, `"users u"` or `"u.x"` does not — so read the literals against the grammar, and
  read EVERY site whose alias comes from a parameter, a config value, a struct field or a
  concatenation.
- scope: `database/internal/columns` and `database/types`. `Columns.As` validated only that
  the alias was non-empty, then every `Col`/`Cols`/`All`/`FieldMap`/`AllFields` rendering
  emitted `alias + "." + column` verbatim. It now validates against the ADR-031 grammar for
  the alias half of `"users u"` — ONE bare identifier (`u`, `_u1`, `u$1#a`) or the framework's
  own quoted reserved-word form (`"level"`). A qualified name (`u.x`), two tokens (`u x`), a
  semicolon, a comment marker, an interior or unbalanced quote, and leading or trailing
  whitespace are all refused; the argument is deliberately NOT trimmed, so `" u "` is refused
  rather than accepted and rendered with its spaces. The grammar itself moved to
  `database/internal/sqllex` so every identifier judge inside `database/` — the builder's doors
  and the columns package's own `db`-tag check included — reads one copy.
  What changes for you is WHERE the refusal happens. `[C60.26]` and `[C60.27]` already refuse
  the rendered string at `Select`, at the Filter columns and in the INSERT column lists, so an
  alias carrying SQL was reaching those doors and failing there; it now fails at `As`. The
  doors that never validate are where this is not merely earlier: `Having` is a raw-SQL door
  and interpolates `u.Col("ID")` as written, as does anything you render into your own SQL.
- gate: match = any `Columns.As` call takes an alias that is not a literal, OR a literal that
  is not a single bare or framework-quoted identifier. no-match = every alias is a bare
  literal, which is the ordinary case. A test asserting the OLD empty-alias panic value
  matches too — see apply.
- apply: pass an identifier, not an expression. A dynamic alias needs an allowlist your own
  code owns — map the caller's value to one of a fixed set before it reaches `As` — since the
  door will not accept a computed one. And repoint any test that asserted the empty-alias
  panic: the value was the string `"alias cannot be empty"` and is now a
  `*dbtypes.InvalidAliasError`, so `assert.PanicsWithValue` on the old string fails while
  `assert.Panics` still passes. Match the new value with `errors.As` against
  `*dbtypes.InvalidAliasError`, whose `Alias` field carries the refused value.
- verify: both directions, because one of them is what the atom is for. Assert the alias you
  intend to ship still qualifies its columns — `cols.As("u").Col("ID")` returns `u.id` and the
  statement builds. Then hand the same door one KNOWN-BAD alias (`id FROM secrets--`) and
  assert it PANICS and that no SQL is produced. A suite that only checks the first direction
  passes unchanged whether or not the validation runs at all. Note the failure is a panic, not
  a `ToSQL()` error, so a table test that only inspects returned errors sees nothing.
- ref: [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) (its 2026-08-23
  addendum) · [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) (why the panic
  value is a typed error) · issue #1150

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
