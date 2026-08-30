# Messaging Shared Tenancy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Implementers drive each task with `/mattpocock-skills:tdd` — the seams are pre-agreed in every task's **Seams** block; do not ask for them.

**Goal:** Give the messaging kind a tenancy — `messaging.tenancy: shared` — so that under `multitenant.enabled` a service consumes and publishes once against the *control-plane key* on both lanes, with the tenant travelling as a framework-written *tenant stamp* that each consume lane reads back into context (deliverable A of #1230).

**Architecture:** One config key selects the tenancy of the whole kind. Under shared, startup takes the single-tenant consumer-bootstrap branch, `deps.Messaging(ctx)` resolves the control-plane publisher, the streams gate lifts, and each lane's publish door stamps `x-tenant-id` from the context tenant while each consume path reads it, validates it against the exported tenant-id grammar, and `SetTenant`s before the handler. A small internal package, `messaging/internal/tenantstamp`, owns the header name, the write, the read and the one conflict sentinel so the two lanes cannot drift.

**Tech Stack:** Go 1.26 · `github.com/rabbitmq/amqp091-go` · `github.com/rabbitmq/rabbitmq-stream-go-client` · koanf config · testify.

**Spec:** [docs/superpowers/specs/2026-08-29-multitenant-messaging-end-state.md](../specs/2026-08-29-multitenant-messaging-end-state.md) — tracked in this repo; the same design is the "Agent Brief" comment on #1230 (`GH_TOKEN=$(gh auth token -u gaborage) gh issue view 1230 --comments`). Where they differ, the spec wins.

**Vocabulary:** [CONTEXT.md](../../CONTEXT.md) `### Tenancy` — *Control-plane key*, *Tenancy*, *Replay*, *Tenant stamp*. Use those words in comments, docs and commit messages; avoid "root key", "empty key", "mode", "bootstrap", "tenant header".

**Stack position:** four dependent PRs, one `/gh-stack`, merged bottom-up by the maintainer. The worktree `.claude/worktrees/A` starts on `feat/messaging-tenancy-config` with the `CONTEXT.md` glossary already applied but uncommitted.

| PR | Branch | Base | Carries |
| --- | --- | --- | --- |
| A1 | `feat/messaging-tenancy-config` | `main` | Tasks 1–3 + Task 4 (gates) |
| A2 | `feat/messaging-tenancy-classic-lane` | `feat/messaging-tenancy-config` | Tasks 5–7 + Task 8 (gates) |
| A3 | `feat/messaging-tenancy-streams-lane` | `feat/messaging-tenancy-classic-lane` | Tasks 9–10 + Task 11 (gates) |
| A4 | `feat/messaging-tenancy-outbox-stamp-docs` | `feat/messaging-tenancy-streams-lane` | Tasks 12–13 + Task 14 (gates) |

Titles: A1 `feat(config): give the messaging kind a tenancy`, A2 `feat(app): replay consumers once on the control-plane key under shared tenancy`, A3 `feat(messaging): stamp and read the tenant on the streams lane under shared tenancy`, A4 `feat(outbox): persist the tenant stamp and document shared messaging tenancy`. No `!`, no migrations atom.

## Global Constraints

- Test function names are **camelCase** (`TestCheckMessagingRejectsAnUnknownTenancy`); table-driven case names are **snake_case** (`{name: "shared_with_tenant_broker"}`). The `check-test-conventions.sh` hook flags violations.
- Commit with `git commit -F <file>`; the commit hook rejects heredoc `-m`. Commits MUST be signed — if signing fails, STOP and report; never pass `--no-gpg-sign`, never set `commit.gpgsign=false`.
- Every `gh` call is prefixed `GH_TOKEN=$(gh auth token -u gaborage)`. Never switch the gh account globally.
- Implementers run `make check` before every commit, detached (`nohup sh -c 'make check' > /tmp/gb-lanes/check-A.log 2>&1 & disown`, then poll the log for `EXIT=`); `git branch --show-current` must print the branch of the PR the task belongs to. The controller runs the pre-push gates and every push.
- No new `//nolint`. Comments are bare-minimum: rationale a reader cannot derive, or a `// SECURITY:` annotation.
- `messaging/streams` must NOT import `github.com/gaborage/go-bricks/messaging`. It may import `messaging/internal/tenantstamp`, `messaging/internal/tracking` and `messaging/internal/delivery` (it already imports the last two).
- apidiff (CI job "API compatibility") fails an incompatible exported change. Adding a `map`/`slice`/`func` field to an exported struct that is comparable today breaks comparability — `messaging.ConsumerOptions`, `ConsumerDeclaration`, `streams.ConsumerOptions` and `SuperStreamConsumerOptions` already carry a map or func field, so a new `bool` there is safe; `messaging.ManagerOptions`, `streams.ManagerOptions` and `config.MessagingConfig` gain only `bool`/`string` fields. No exported function changes arity; new behaviour arrives through new setters/fields.
- `TestRegistryProcessMessagePerDeliveryLoggerAllocs` (`messaging/registry_test.go:2865`) is a tripwire guard on allocs/op of the classic consume path; the stamp read must not regress it (read the header once, no map allocation on the happy path).
- The `""` key is never a tenant: `multitenant.SetTenant(ctx, "")` is a no-op and `GetTenant` returns `ok=false` for it (`multitenant/context.go`). Nothing in this plan changes that.
- The default tenancy is `per-tenant`, byte-for-byte unchanged for every existing deployment. Every new branch is gated on `config.TenancyShared`.

## Decisions the plan makes

1. **The tenant-id grammar moves to `multitenant`.** #1004 asked for `server.ValidateTenantID`; `messaging` cannot import `server` (cycle), so the rule lives beside the context accessors: `multitenant.DefaultTenantIDPattern()` (an accessor over the unexported `defaultTenantIDPattern`, `^[a-z0-9-]{1,64}$`, moved from `server/middleware.go:214`) and `multitenant.ValidateTenantID(id string) error` returning `multitenant.ErrInvalidTenantID`. `server` keeps its behaviour by calling the accessor. The pattern is NOT an exported var: a consumer could otherwise reassign it and loosen tenant validation process-wide, or nil it into a panic. A4's PR body says `Closes #1004`.
2. **`TenantOptional bool`, not `RequireTenant`.** Go has no "unset" for a plain bool, and the safe default must be fail-closed; a pointer tri-state is more surface than a consumer needs. `TenantOptional: true` is the control-plane-consumer opt-out. It is read only when stamps are read (below).
3. **Stamps are READ only under `multitenant.enabled && messaging.tenancy == shared`.** Under `multitenant.enabled: false`, `shared` stays a no-op (ADR-041 env-parity); under per-tenant tenancy the replay key already seeds the context and a stamp is ignored. Stamps are WRITTEN whenever the context carries a tenant, in every mode — it is free and it is what B and C consume. The conflict check on a caller-supplied `x-tenant-id` runs in every mode.
4. **The fail-closed line is the lane's failure line, not a second WARN.** The stamp check runs inside the delivery pipeline's `Handle` step and returns an error; the classic lane's existing ERROR line ("Message processing failed - discarding without requeue") and nack-without-requeue follow, the streams lane's existing skip outcome follows. The error text carries the reason and the byte length, never the value. One line per refused delivery.
5. **`MultiTenantResourceProvider` learns its tenancy through a setter**, `SetMessagingTenancy(tenancy string)`, mirroring `SetDeclarations`; its exported constructor keeps its four parameters (apidiff).
6. **The relay rehydrates the persisted stamp into the publish context and removes it from the headers** before `PublishToExchange`, exactly as it rehydrates the trace context — otherwise the client's conflict check would refuse every relayed row. Per-tenant relay cycles already carry the tenant in ctx and are unaffected.
7. **Pre-warm runs under shared.** The messaging slot's start gate admits shared messaging alongside single-tenant, closing ADR-041's "first relay cycle logs an outage" trade-off for this mode.
8. **`app.assertStreamsSingleTenant` moves with the config gate** in A3, not A1. Between A1 and A3 a shared+streams config passes `config.Validate` and is refused at startup by the runtime assert — fail-closed, and gone one PR later.

---

## PR A1 — the config key and the accessor

**Branch:** `feat/messaging-tenancy-config`, cut from `main` (the worktree is already on it).

### Task 1: `messaging.tenancy` — field, default, enumeration, glossary

**Files:**

- Modify: `config/types.go` (`MessagingConfig` at `449-456`; the doc-comment shape to copy is `OutboxConfig.Tenancy` at `763-771`)
- Modify: `config/validation.go` (`applyMessagingDefaults` — add the default; `checkMessaging` at `442-451` — add the enumeration check)
- Modify: `config/validation_test.go`, `config/phases_test.go` (pick the file holding the nearest `checkMessaging` tests)
- Commit: `CONTEXT.md` (already modified in the worktree — stage it in this task's commit)

**Interfaces:**

- Produces: `config.MessagingConfig.Tenancy string` (`koanf:"tenancy"`), normalized to `config.TenancyPerTenant` when empty; `checkMessaging` rejects any other value with `NewValidationError("messaging.tenancy", ...)` naming both accepted values.
- Consumes, unchanged: `config.TenancyPerTenant` / `config.TenancyShared` (`config/types.go:669-670`), `NewValidationError`.

**Seams (pre-agreed):** `config.Validate(cfg *Config) error` end-to-end (normalize + check), observed through the returned error and the normalized `cfg.Messaging.Tenancy`. Do not test `applyMessagingDefaults` or `checkMessaging` directly.

- [ ] **Step 1: Red — the enumeration and the default**

| case name | `messaging.tenancy` | `multitenant.enabled` | expect |
| --- | --- | --- | --- |
| `unset_defaults_to_per_tenant` | `""` | false | nil error; `cfg.Messaging.Tenancy == "per-tenant"` |
| `per_tenant_accepted` | `per-tenant` | true | nil |
| `shared_accepted` | `shared` | true | nil |
| `shared_accepted_single_tenant` | `shared` | false | nil (env-parity) |
| `unknown_rejected` | `Shared` | true | error contains `messaging.tenancy` and both `per-tenant` and `shared` |

- [ ] **Step 2: Run the new test, expect FAIL** (unknown field / no default).
- [ ] **Step 3: Green** — field + doc comment (copy the `OutboxConfig.Tenancy` wording, replacing "ledger" with "consumers and publishers" and pointing at ADR-087), default in `applyMessagingDefaults`, enumeration in `checkMessaging` before the streams call.
- [ ] **Step 4: Run `go test ./config/...`, expect PASS.**
- [ ] **Step 5: `make check`, then commit** — `feat(config): add messaging.tenancy` with `CONTEXT.md` staged.

### Task 2: The cross-section rules — static tenants and the streams gate

**Files:**

- Modify: `config/validation.go` (`checkMultitenant` `1879-1912`; `validateNoSingleTenantConflict` `1943-1960`; `checkMessaging` `442-451`; `checkMessagingStreams` `459-492`)
- Modify: `config/validation_test.go` (`TestValidateMessagingStreamsRejectsMultiTenant` ≈`5943` and the second `single-tenant only` assertion ≈`6088`; the `validateNoSingleTenantConflict` cases ≈`2558`, ≈`5365`)

**Interfaces:**

- Produces: `checkMessagingStreams(cfg *StreamsConfig, perTenant bool) error` — the parameter is renamed to what it now means; `checkMessaging` computes `perTenant := multitenant && cfg.Tenancy == TenancyPerTenant`. `checkMultitenant` gains, after `checkTenantMessagingConsistency`, a rule: if `msg.Tenancy == TenancyShared` and any `isTenantMessagingConfigured(&tenant.Messaging)` → `&ConfigError{Category: errCategoryInvalid, Field: "multitenant.tenants.*.messaging", Message: "unreachable under messaging.tenancy: shared", Action: "remove the per-tenant messaging blocks or set messaging.tenancy: per-tenant"}`. `validateNoSingleTenantConflict` keeps rejecting a root `database:` beside static tenants but accepts a root `messaging:` when `msg.Tenancy == TenancyShared` (it is the control-plane broker).
- Consumes: Task 1's normalized `Tenancy`.

**Seams (pre-agreed):** `config.Validate` only.

- [ ] **Step 1: Red — three tables**

Static tenants (`source.type: static`, `multitenant.enabled: true`, two tenants each with a `database` block):

| case name | tenant `messaging.url` | root `messaging.broker.url` | `messaging.tenancy` | expect |
| --- | --- | --- | --- | --- |
| `per_tenant_brokers_ok` | set on both | `""` | per-tenant | nil |
| `shared_with_tenant_broker_rejected` | set on both | set | shared | error contains `multitenant.tenants.*.messaging` and `shared` |
| `shared_root_broker_ok` | `""` on both | set | shared | nil |
| `per_tenant_root_broker_rejected` | `""` on both | set | per-tenant | error contains `messaging` and `not allowed when static tenants are configured` (existing rule, now pinned) |

Streams (`messaging.streams.uri: rabbitmq-stream://svc:pw@broker:5552/%2f`, `multitenant.enabled: true`):

| case name | `messaging.tenancy` | expect |
| --- | --- | --- |
| `streams_rejected_per_tenant` | per-tenant | error contains `single-tenant only` and not the password |
| `streams_accepted_shared` | shared | nil |

- [ ] **Step 2: Run, expect FAIL** on `shared_with_tenant_broker_rejected`, `shared_root_broker_ok`, `streams_accepted_shared`.
- [ ] **Step 3: Green** — the three edits above. Keep `checkMessagingStreams`'s error text; only its gate condition changes.
- [ ] **Step 4: `go test ./config/...` PASS; the two existing streams tests still pass unchanged.**
- [ ] **Step 5: `make check`, commit** — `feat(config): admit streams and the root broker under shared messaging tenancy`.

### Task 3: `multitenant.TenantID`, `ErrNoTenant`, and the exported tenant-id grammar

**Files:**

- Modify: `multitenant/context.go` (add below `GetTenant`)
- Create: `multitenant/tenant_id.go`
- Modify: `server/middleware.go` (`defaultTenantIDRegex` at `214`, used at `283`) — delete the var, call `multitenant.DefaultTenantIDPattern()`
- Modify: `multitenant/context_test.go`; Create: `multitenant/tenant_id_test.go`; Modify: the `server` test that pins the regex if one exists (`git grep -n defaultTenantIDRegex server/`)

**Interfaces:**

- Produces:
  - `var ErrNoTenant = errors.New("multitenant: no tenant in context")`
  - `func TenantID(ctx context.Context) (string, error)` — `GetTenant` ok-form mapped to `ErrNoTenant`; nil ctx → `ErrNoTenant`.
  - `var defaultTenantIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)` (unexported) with `func DefaultTenantIDPattern() *regexp.Regexp` returning it
  - `var ErrInvalidTenantID = errors.New("multitenant: tenant id does not match the default grammar")`
  - `func ValidateTenantID(id string) error` — nil when `defaultTenantIDPattern.MatchString(id)`, else `fmt.Errorf("%w: %d bytes", ErrInvalidTenantID, len(id))` — the value never appears in the error.
- Consumed by: A2 Task 6/7, A3 Task 9/10, A4 Task 12, and `server`.

**Seams (pre-agreed):** the four exported functions/values; `server.buildTenantResolver` through the existing resolver tests (they must not change).

- [ ] **Step 1: Red**

| case name | input | expect |
| --- | --- | --- |
| `tenant_id_present` | `SetTenant(ctx, "acme")` | `"acme", nil` |
| `tenant_id_absent` | `context.Background()` | `"", ErrNoTenant` (via `errors.Is`) |
| `tenant_id_nil_ctx` | `nil` | `ErrNoTenant` |
| `validate_ok` | `"acme-1"` | nil |
| `validate_uppercase` | `"Acme"` | `ErrInvalidTenantID`; error text contains `4 bytes`, not `Acme` |
| `validate_too_long` | 65 × `a` | `ErrInvalidTenantID`; text contains `65 bytes` |
| `validate_empty` | `""` | `ErrInvalidTenantID` |

- [ ] **Step 2: Run, expect FAIL** (undefined symbols).
- [ ] **Step 3: Green** — the code above; `server/middleware.go:283` becomes `tenantRegex := multitenant.DefaultTenantIDPattern()`.
- [ ] **Step 4: `go test ./multitenant/... ./server/...` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(multitenant): export TenantID and the default tenant-id grammar`.

### Task 4: Gates for PR A1 (controller only)

- [ ] **Step 1: `make check`** (detached, read `EXIT=0`).
- [ ] **Step 2: `/simplify`** — re-run `make check` if it changed code.
- [ ] **Step 3: `/security-audit`** — the error texts must not echo a tenant id or a broker URL.
- [ ] **Step 4: `/code-review`** — apply findings, `make check` again, re-run `/code-review` if code changed.
- [ ] **Step 5: `make mutate`, backgrounded, after committing** — every mutant on the changed lines must die; the enumeration and the three check rules each need a separable failing test.

---

## PR A2 — the classic lane

**Branch:** `feat/messaging-tenancy-classic-lane`, cut from `feat/messaging-tenancy-config`.

### Task 5: The app takes the control-plane branch under shared

**Files:**

- Modify: `app/app.go` (`multiTenant()` at `99-102`: add `sharedMessaging()` and `perTenantMessaging()` beside it)
- Modify: `app/messaging_setup.go` (`prepareRuntimeConsumers` `20-40`)
- Modify: the messaging slot's start gate — `git grep -n 'preWarmMessaging' app/*.go`, the caller that checks the kind's tenancy
- Modify: `app/resource_provider.go` (`MultiTenantResourceProvider` struct, `Messaging`, and a shared helper for the control-plane path used by both providers)
- Modify: `app/bootstrap.go` (`127-130`, call the setter when shared), `app/managers.go` (`ManagerConfigBuilder` `16-35`, `BuildMessagingOptions` `77-90`), `app/bootstrap.go` `newManagerConfigBuilderFromConfig` `48-62`
- Modify: `messaging/manager.go` (`ManagerOptions` `89-111`: add `TenantStamps bool`; `Manager` stores it)
- Test: `app/messaging_setup_test.go`, `app/resource_provider_test.go`, `app/managers_test.go` (or the file that pins `BuildMessagingOptions`)

**Interfaces:**

- Produces:
  - `func (a *App) sharedMessaging() bool` — `a.cfg != nil && a.cfg.Messaging.Tenancy == config.TenancyShared`
  - `func (a *App) perTenantMessaging() bool` — `a.multiTenant() && !a.sharedMessaging()`
  - `func (p *MultiTenantResourceProvider) SetMessagingTenancy(tenancy string)`
  - `func controlPlaneMessaging(ctx context.Context, mgr *messaging.Manager, decls *messaging.Declarations) (messaging.AMQPClient, error)` — the body of today's `SingleTenantResourceProvider.Messaging` after the nil check (EnsureConsumers on `""` if decls != nil, then `Publisher(ctx, "")`, then `acquireLease`); both providers call it.
  - `messaging.ManagerOptions.TenantStamps bool`; `ManagerConfigBuilder.tenantStamps bool` set in `newManagerConfigBuilderFromConfig` to `cfg.Multitenant.Enabled && cfg.Messaging.Tenancy == config.TenancyShared` and copied by `BuildMessagingOptions`.
- Behaviour: `prepareRuntimeConsumers` returns early only when `a.perTenantMessaging()`; the pre-warm gate admits `!a.multiTenant() || a.sharedMessaging()`; `MultiTenantResourceProvider.Messaging` under shared skips the tenant lookup and calls `controlPlaneMessaging` (per-tenant path unchanged).

**Seams (pre-agreed):** `App.prepareRuntimeConsumers` through `newMinimalMessagingApp` (`app/messaging_setup_test.go:26-35`) with a manager built by `messaging.NewMessagingManager` and a `BrokerURLProvider` fake; `MultiTenantResourceProvider.Messaging` with a manager whose client factory returns a fake `AMQPClient` (see `TestMultiTenantResourceProvider`); `ManagerConfigBuilder.BuildMessagingOptions` through `newManagerConfigBuilderFromConfig`.

- [ ] **Step 1: Red**

| case name | config | expect |
| --- | --- | --- |
| `shared_replays_on_control_plane_key` | MT enabled, tenancy shared, one declared consumer, broker source resolving `""` only | `EnsureConsumers` called once with key `""`; no call with a tenant key; nil error |
| `shared_declared_consumer_cannot_start_fails_run` | same, broker source fails for `""` | error wraps the manager error; message contains `failed to start` |
| `shared_no_consumers_warns` | same, declarations with a publisher only | nil error, one WARN |
| `per_tenant_still_skips` | MT enabled, tenancy per-tenant | no `EnsureConsumers` call (keeps `TestPrepareRuntimeConsumersSkipsEnsureInMultiTenantMode` green) |
| `provider_shared_without_tenant` | provider with `SetMessagingTenancy("shared")`, ctx without tenant | client returned, `EnsureConsumers("")` + `Publisher("")` |
| `provider_shared_with_tenant_ignored` | same, ctx with tenant `acme` | same result; no call keyed `acme` |
| `provider_per_tenant_unchanged` | no setter call, ctx with `acme` | `EnsureConsumers("acme")`, `Publisher("acme")` |
| `provider_per_tenant_no_tenant` | no setter, no tenant | `ErrNoTenantInContext` |
| `options_tenant_stamps` | MT enabled + shared → `true`; MT enabled + per-tenant → `false`; MT disabled + shared → `false` | `BuildMessagingOptions().TenantStamps` |

- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Green** — edits above. The `prepareRuntimeConsumers` INFO line under per-tenant keeps its text; add no new log line for shared beyond the existing "Single-tenant consumers started successfully" (rename that message to "Consumers started on the control-plane key" so both modes read true — update the test that pins it).
- [ ] **Step 4: `go test ./app/... ./messaging/...` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(app): replay consumers once on the control-plane key under shared messaging tenancy`.

### Task 6: The stamp, write side — `messaging/internal/tenantstamp` and the classic publish doors

**Files:**

- Create: `messaging/internal/tenantstamp/tenantstamp.go`, `messaging/internal/tenantstamp/tenantstamp_test.go`
- Modify: `messaging/amqp_client.go` (`publishPrologue` `405-416`, `unsafePublish` `1249-1262`, `preparePublishing` `336-380`)
- Create: `messaging/tenant_stamp.go` (the exported name and sentinel)
- Delete: `messaging/tenant_publisher.go`, `messaging/tenant_publisher_test.go`
- Test: `messaging/amqp_client_test.go` (beside `TestPublishToExchangeCustomHeaders`)

**Interfaces:**

- Produces, in `tenantstamp`:
  - `const Header = "x-tenant-id"`
  - `var ErrConflict = errors.New("messaging: x-tenant-id is written by the framework from the context tenant; remove it from the caller's headers")`
  - `func CheckCallerHeaders(headers map[string]any) error` — `ErrConflict` when the key is present (any value), nil otherwise; nil map → nil.
  - `func Write(ctx context.Context, set func(key string, value any))` — `if id, ok := multitenant.GetTenant(ctx); ok { set(Header, id) }`.
  - `type ReadError struct{ Reason string; Len int }` with `Error()` = `"tenant stamp <Reason> (<Len> bytes)"`, `Reason` ∈ `{"missing", "not a string", "invalid"}`; `func Read(get func(key string) any) (string, error)` — absent → `ReadError{missing, 0}`; non-string → `ReadError{not a string, 0}`; `multitenant.ValidateTenantID` fails → `ReadError{invalid, len}`; else the id.
- Produces, in `messaging`: `const TenantStampHeader = tenantstamp.Header`; `var ErrTenantStampConflict = tenantstamp.ErrConflict`.
- Behaviour (as shipped, after the orchestrator's precedence ruling): `publishPrologue` and `unsafePublish` each call `c.tenantStamp(ctx, options.Headers)` right after `validatePublishDestination`, which runs `tenantstamp.Resolve(ctx, c.replayKey)` then `tenantstamp.CheckCallerHeaders(headers, stamp)`; `preparePublishing` takes the resolved stamp and calls `tenantstamp.Write(stamp, accessor.Set)` right after the trace injection. ONE source rule, stated once: the context tenant; the client's replay key when the context carries none; `ErrConflict` when both exist and differ; a caller-supplied header equal to the resolved stamp accepted, anything else refused. The stamp is resolved once per publish, above the retry loop, so every attempt writes the same value. `tenantAwarePublisher` is deleted (`git grep -n tenantAwarePublisher` → nothing). It was NOT dead when this plan was written — `manager.go` wrapped every pooled publisher with it, stamping `x-tenant-id` from the replay key and silently OVERWRITING a caller's value. Deleting it therefore moves the stamp to one writer with a stated precedence (context tenant; the replay key when the context carries none; `ErrConflict` when both exist and differ) and turns a conflicting caller-supplied stamp into a publish error.

**Seams (pre-agreed):** `tenantstamp` package API; `AMQPClientImpl.PublishToExchange` observed through `fakeChannel.lastPublishing.Headers` (`newClientWithFakeChannel`, `sendConfirmsAfterEachAttempt`); `unsafePublish` through the same fake.

- [ ] **Step 1: Red — `tenantstamp`**

| case name | call | expect |
| --- | --- | --- |
| `write_with_tenant` | `Write(SetTenant(ctx,"acme"), set)` | `set("x-tenant-id","acme")` once |
| `write_without_tenant` | `Write(ctx, set)` | `set` never called |
| `check_conflict` | headers `{"x-tenant-id":"x"}` | `ErrConflict` |
| `check_nil_and_clean` | nil; `{"a":1}` | nil, nil |
| `read_ok` | get → `"acme"` | `"acme", nil` |
| `read_missing` | get → nil | `ReadError{missing,0}`; text `tenant stamp missing (0 bytes)` |
| `read_not_string` | get → `42` | `ReadError{not a string,0}` |
| `read_invalid` | get → `"Acme"` | `ReadError{invalid,4}`; text contains `4 bytes`, not `Acme` |

- [ ] **Step 2: Red — the doors**

| case name | ctx | caller headers | expect |
| --- | --- | --- | --- |
| `publish_stamps_from_ctx` | tenant `acme` | nil | `lastPublishing.Headers["x-tenant-id"] == "acme"`, trace headers still present |
| `publish_no_tenant_no_stamp` | none | `{"custom":"v"}` | no `x-tenant-id` key; `custom` preserved |
| `publish_caller_stamp_refused` | tenant `acme` | `{"x-tenant-id":"acme"}` | `errors.Is(err, ErrTenantStampConflict)`; zero channel publishes |
| `unsafe_publish_caller_stamp_refused` | none | `{"x-tenant-id":"z"}` | same sentinel |

- [ ] **Step 3: Run, expect FAIL.**
- [ ] **Step 4: Green** — the package, the three door edits, the deletion.
- [ ] **Step 5: `go test ./messaging/...` PASS**, including `TestPublishToExchangeCustomHeaders` unchanged.
- [ ] **Step 6: `make check`, commit** — `feat(messaging): stamp the context tenant on every classic publish`.

### Task 7: The stamp, read side — `TenantOptional` and the classic consume path

**Files:**

- Modify: `messaging/helpers.go` (`ConsumerOptions` `98-111`, `NewConsumer` `113-128`), `messaging/registry.go` (`ConsumerDeclaration` `121-134`; `Registry` struct `56-77` — add `tenantStamps bool`; `processMessage` `760-785`)
- Modify: `messaging/manager.go` (`ensureConsumersInternal` — after `registry := NewRegistry(client, m.logger)` set `registry.tenantStamps = m.tenantStamps`, the field Task 5 stored from `ManagerOptions.TenantStamps`)
- Test: `messaging/registry_test.go` (beside `TestRegistryProcessMessageHandlerError`), `messaging/helpers_test.go`

**Interfaces:**

- Produces: `ConsumerOptions.TenantOptional bool` copied to `ConsumerDeclaration.TenantOptional bool` by `NewConsumer`. In `processMessage`'s `Handle` closure, before the handler: when `r.tenantStamps`, `id, err := tenantstamp.Read(func(k string) any { return delivery.Headers[k] })`; on error, if `consumer.TenantOptional` and the reason is `missing` → run the handler with `msgCtx` as is; otherwise return the `ReadError` (the pipeline turns it into the nack-without-requeue outcome and the failure line); on success `msgCtx = multitenant.SetTenant(msgCtx, id)`. When `!r.tenantStamps` the closure is unchanged.
- `Declarations.Hash()` — check whether it covers consumer fields; if it does, `TenantOptional` must be part of the hash like `AutoAck` is. Report which.

**Seams (pre-agreed):** `Registry.processMessage` with a `ConsumerDeclaration` whose `Handler` records `multitenant.TenantID(ctx)` and a `*amqp.Delivery` carrying an `Acknowledger` fake (the existing `TestRegistryProcessMessage*` fixtures); `NewConsumer` field copy.

- [ ] **Step 1: Red**

| case name | `tenantStamps` | `TenantOptional` | header | expect |
| --- | --- | --- | --- | --- |
| `stamped_delivery_seeds_tenant` | true | false | `"acme"` | handler called; `TenantID(ctx) == "acme"`; ack |
| `missing_stamp_nacks_without_requeue` | true | false | absent | handler NOT called; nack, requeue=false; failure line contains `tenant stamp missing (0 bytes)` |
| `invalid_stamp_nacks_length_only` | true | false | 300 × `a` | handler not called; nack; line contains `300 bytes`, not `aaaa` |
| `non_string_stamp_nacks` | true | false | `42` | nack; `not a string` |
| `optional_consumer_runs_without_stamp` | true | true | absent | handler called; `TenantID` → `ErrNoTenant`; ack |
| `optional_consumer_still_refuses_invalid` | true | true | `"Acme"` | nack; `invalid` |
| `per_tenant_ignores_stamp` | false | false | `"other"` | handler called; ctx tenant is whatever `ctx` carried (`acme` from the test's `SetTenant`), not `other` |
| `allocs_guard_holds` | — | — | — | `TestRegistryProcessMessagePerDeliveryLoggerAllocs` unchanged and green |

- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Green** — edits above.
- [ ] **Step 4: `go test ./messaging/... -run 'TestRegistryProcessMessage|TestNewConsumer' -count=1` PASS; then the package.**
- [ ] **Step 5: `make check`, commit** — `feat(messaging): read the tenant stamp before every classic delivery under shared tenancy`.

### Task 8: Gates for PR A2 (controller only)

- [ ] **Step 1: `make check`.**
- [ ] **Step 2: `/simplify`** — `make check` again if it changed code.
- [ ] **Step 3: `/security-audit`** — the `ReadError` text and the conflict sentinel must never carry the stamp's value; the control-plane provider path must not widen what a tenant-less request can reach beyond what single-tenant already allows.
- [ ] **Step 4: `/code-review`**, re-run if findings changed code.
- [ ] **Step 5: `make mutate`** after committing; gremlins does not mutate `&&`/`||` — hand-flip `a.perTenantMessaging()` to `a.multiTenant()` and confirm `shared_replays_on_control_plane_key` fails.

---

## PR A3 — the streams lane

**Branch:** `feat/messaging-tenancy-streams-lane`, cut from `feat/messaging-tenancy-classic-lane`.

### Task 9: Streams read side and the runtime gate

**Files:**

- Modify: `app/streams_setup.go` (`assertStreamsSingleTenant` `82-88`; `streams.NewManager` call `46-53` — pass `TenantStamps`)
- Modify: `messaging/streams/manager.go` (`ManagerOptions` `47-61`: add `TenantStamps bool`; `newRunner` `398-408`: copy it to the runner)
- Modify: `messaging/streams/streams.go` (`ConsumerOptions` `127-140`, `SuperStreamConsumerOptions` `153-166`: add `TenantOptional bool`; the internal `consumerDeclaration` carries it)
- Modify: `messaging/streams/runner.go` (`consumerRunner` `212-218` — add `tenantStamps bool`, `tenantOptional bool`; `deliver` `229-262` — the `Handle` closure)
- Test: `app/streams_setup_test.go`, `messaging/streams/runner_test.go` (beside `TestRunnerDeliverHandlerErrorSkipsOffsetCommit`), `messaging/streams/declarations_test.go`

**Interfaces:**

- Produces: `streams.ManagerOptions.TenantStamps bool`; `streams.ConsumerOptions.TenantOptional`, `streams.SuperStreamConsumerOptions.TenantOptional`. `assertStreamsSingleTenant` returns nil when `!a.cfg.Multitenant.Enabled || a.cfg.Messaging.Tenancy == config.TenancyShared`; its error text gains "unless messaging.tenancy is shared". The `Handle` closure in `deliver` mirrors Task 7 exactly, reading `message.ApplicationProperties[tenantstamp.Header]` through `tenantstamp.Read`; a `ReadError` returned from `Handle` is the existing handler-error outcome (offset not committed, logged, counted).
- Consumes: `messaging/internal/tenantstamp` (Task 6), `multitenant.SetTenant`.

**Seams (pre-agreed):** `consumerRunner.deliver` through the existing runner test fixtures (`runner_test.go` — handler records `multitenant.TenantID(ctx)`, `offsetStorer` fake observes the commit); `App.assertStreamsSingleTenant` with a config literal; `streams.NewManager` option threading through `Manager` internals as the existing declarations tests do.

- [ ] **Step 1: Red**

| case name | `TenantStamps` | `TenantOptional` | property | expect |
| --- | --- | --- | --- | --- |
| `stamped_delivery_seeds_tenant` | true | false | `"acme"` | handler sees `acme`; offset committed |
| `missing_stamp_skips` | true | false | absent | handler not called; offset NOT committed; outcome logged with `tenant stamp missing (0 bytes)` |
| `invalid_stamp_skips_length_only` | true | false | 300 × `a` | not called; line has `300 bytes`, no `aaaa` |
| `optional_consumer_runs_without_stamp` | true | true | absent | handler called; `ErrNoTenant`; committed |
| `stamps_off_ignores_property` | false | false | `"acme"` | handler called; `ErrNoTenant` |
| `assert_admits_shared` | MT enabled + shared | — | — | nil |
| `assert_rejects_per_tenant` | MT enabled + per-tenant | — | — | error contains `single-tenant only` |

- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Green.**
- [ ] **Step 4: `go test ./app/... ./messaging/streams/...` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(messaging): read the tenant stamp on the streams lane under shared tenancy`.

### Task 10: Streams write side

**Files:**

- Modify: `messaging/streams/publisher.go` (`send` `252-282` — conflict check after `routingError`; `buildMessage` `339-349` — stamp after trace injection)
- Modify: `messaging/streams/streams.go` (`PublishMessage.Properties` doc: "`x-tenant-id` is the framework's; a caller-supplied one fails the publish with `ErrTenantStampConflict`")
- Create: `messaging/streams/tenant_stamp.go` — `var ErrTenantStampConflict = tenantstamp.ErrConflict`
- Test: `messaging/streams/publisher_test.go` (beside `TestPublisherPublishRejectsAMismatchedRoutingKey`)

**Interfaces:**

- Produces: `streams.ErrTenantStampConflict` (the same error value as `messaging.ErrTenantStampConflict`, so `errors.Is` holds across both).
- Behaviour: `send` returns `tenantstamp.CheckCallerHeaders(msg.Properties)`'s error before `buildMessage`; `buildMessage` calls `tenantstamp.Write(ctx, func(k string, v any) { properties[k] = v })` after `InjectIntoHeaders`.

**Seams (pre-agreed):** `Publisher.Publish` through the existing fake environment/producer in `publisher_test.go`, observing the `message.StreamMessage`'s `ApplicationProperties` handed to the fake.

- [ ] **Step 1: Red**

| case name | ctx | properties | expect |
| --- | --- | --- | --- |
| `publish_stamps_from_ctx` | `acme` | nil | properties carry `x-tenant-id: acme` and `traceparent` |
| `publish_without_tenant` | none | `{"k":"v"}` | no `x-tenant-id`; `k` preserved; caller's map untouched |
| `publish_caller_stamp_refused` | `acme` | `{"x-tenant-id":"acme"}` | `errors.Is(err, streams.ErrTenantStampConflict)` and `errors.Is(err, messaging.ErrTenantStampConflict)` (assert the latter from a `messaging` test or an `app` test — `streams` tests cannot import `messaging`); no send registered |

- [ ] **Step 2: Run, expect FAIL.** **Step 3: Green.** **Step 4: `go test ./messaging/streams/...` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(messaging): stamp the context tenant on every stream publish`.

### Task 11: Gates for PR A3 (controller only)

- [ ] **Step 1: `make check`.** **Step 2: `/simplify`.** **Step 3: `/security-audit`** — `redactStreamURI` still guards every new error path; no stamp value in any line. **Step 4: `/code-review`.** **Step 5: `make mutate`** after committing; a test-only or operator-free hunk needs a hand-applied mutation pair named in the report.

---

## PR A4 — the outbox stamp, ADR-087 and the docs

**Branch:** `feat/messaging-tenancy-outbox-stamp-docs`, cut from `feat/messaging-tenancy-streams-lane`.

### Task 12: The outbox persists the stamp; the relay rehydrates it

**Files:**

- Modify: `outbox/publisher.go` (`Publish` `32-100` — conflict check on `event.Headers` beside the nil/empty checks; `marshalHeaders` `105-121` — stamp after trace injection, and the "common path" early return must also consider a tenant in ctx)
- Modify: `outbox/relay.go` (`publishRecord` `242-262` — after `ExtractFromHeaders`: `if id, ok := headers[messaging.TenantStampHeader].(string); ok { pubCtx = multitenant.SetTenant(pubCtx, id); delete(headers, messaging.TenantStampHeader) }`)
- Test: `outbox/publisher_test.go` (beside `TestPublisherPublishPreservesCallerHeadersWithTrace`), `outbox/relay_test.go` (beside `TestPublishRecordRehydratesTraceContextForPublish` — assert `multitenant.GetTenant(amqp.LastPublishCtx)` and that `LastPublishHdrs` has no `x-tenant-id`)

**Interfaces:**

- Consumes: `messaging.TenantStampHeader`, `messaging.ErrTenantStampConflict` (`outbox` already imports `messaging`), `multitenant.GetTenant`/`SetTenant`, `tenantstamp` is NOT imported (internal to `messaging`; use the exported name).
- Behaviour: `Publish` returns `fmt.Errorf("outbox: %w", messaging.ErrTenantStampConflict)` when `event.Headers` carries the key; `marshalHeaders` writes the stamp when ctx carries a tenant, so a tenanted publish with no caller headers persists a non-NULL headers column.

**Seams (pre-agreed):** `outboxPublisher.Publish` through `fakeStore` (`Insert` captures the record — the existing publisher tests); `Relay.publishRecord` through `fakeAMQP` (`LastPublishCtx`, `LastPublishHdrs`).

- [ ] **Step 1: Red**

| case name | ctx | `event.Headers` | expect |
| --- | --- | --- | --- |
| `publish_persists_stamp` | `acme` | nil | decoded headers contain `x-tenant-id: acme` (plus trace keys when traced) |
| `publish_without_tenant` | none | nil | headers NULL (existing behaviour) |
| `publish_caller_stamp_refused` | `acme` | `{"x-tenant-id":"acme"}` | `errors.Is(err, messaging.ErrTenantStampConflict)`; `InsertCalls == 0` |
| `relay_rehydrates_stamp` | relay ctx (no tenant) | row headers `{"x-tenant-id":"acme"}` | `GetTenant(LastPublishCtx) == "acme"`; `LastPublishHdrs` lacks the key; `x-outbox-event-id` present |
| `relay_untenanted_row` | — | row headers nil | no tenant in `LastPublishCtx` |

- [ ] **Step 2: Run, expect FAIL.** **Step 3: Green.** **Step 4: `go test ./outbox/...` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(outbox): persist the tenant stamp and replay it on relay`.

### Task 13: ADR-087, the ADR-041 pointer, and the docs

**Files:**

- Create: `wiki/adr_087_messaging_tenancy_and_tenant_stamp.md`
- Modify: `wiki/architecture_decisions.md` (an entry after ADR-086's at `1397-1410`, same shape; the counter at `1881`: `through ADR-087`)
- Modify: `wiki/adr_041_shared_ledger_tenancy.md` (lines `1-4`: a blockquote pointer after the header — `> **Extended by [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md) (2026-08-29):** §4's deferred consumer half now exists as `messaging.tenancy: shared`.`)
- Modify: `wiki/outbox.md` (`338-340` — replace the "Consumers on the shared broker are out of scope" bullet with a pointer to `messaging.tenancy: shared` and the stamp; the "tenant identity travels in the event" bullet names the stamp)
- Modify: `wiki/messaging.md` (new `## Multi-tenant consumption` section before `## Consumer Concurrency (v0.17+)` at `347`: the two tenancies, the stamp on both lanes, `TenantOptional`, fail-closed, `multitenant.TenantID`, the two conflict sentinels, the static-tenant check rule)
- Modify: `wiki/streams.md` (table row `25` "Multi-tenant" → `yes, under messaging.tenancy: shared (the stamp seeds the tenant)`; bullet `54-59` rewritten; a "partition sizing" note: 2–4× max replicas, growing = new super stream + cutover)
- Modify: `llms.txt` (`## Multi-Tenancy` at `3958`: a `messaging.tenancy: shared` example with a consumer reading `multitenant.TenantID(ctx)`; the messaging config block at `2854`: `tenancy: per-tenant  # or shared — see wiki/messaging.md`)
- Modify: `CLAUDE.md` line `116` (`**messaging/**`): append "`messaging.tenancy: shared` consumes and publishes once on the control-plane key with the tenant carried as a stamp (ADR-087)". `wc -c CLAUDE.md` must stay under 40,960 (it is 33,907).
- Modify: `config/types.go` `MessagingConfig` doc if Task 1's comment did not already point at ADR-087.

**ADR-087 content (write it in full):**

- Header: `# ADR-087: The Messaging Kind Has a Tenancy, and the Tenant Travels as a Stamp`; `- **Status**: Accepted`; `- **Date**: 2026-08-29`; `- **Related**: ADR-041 (shared ledger tenancy; §4 deferred this), ADR-039 (resolution is identification), ADR-059/063 (streams lane), ADR-070 (the publish frame's shortstrs).
- Context: the #1230 topology (silo tenants from a dynamic source, one control-plane broker, thousands of tenants); why the per-tenant broker model is vhost-per-tenant and why it cannot scale (connection per vhost, vhost cost, quorum-queue ceiling); the three concerns a "tenant in a header" conflates — a table with routing / isolation / authorization against header stamp / routing-key segment / vhost per tenant — and why broker-side authorization buys nothing when producers are platform services.
- Decision, numbered as in the spec's Decisions 1–7 plus 11: the stated goal **zero broker objects per tenant**; the stamp and its single writer; the kind-level tenancy and the control-plane branch; the check rules; the read side, `TenantOptional`, fail-closed with reason and length only; `TenantID`/`ErrNoTenant`, the exported grammar; streams accepted under shared only.
- Alternatives considered: consumers-only knob (`messaging.consumers.tenancy`) — rejected, leaves the kind half-shared; per-consumer tenancy — deferred, no deployment; routing-key segment as the contract — rejected, topic permissions need per-tenant credentials; vhost per tenant and per-tenant queues — rejected, the constraints above; a `RequireTenant *bool` tri-state — rejected for a zero-value-safe bool.
- Consequences: positive (no config lie, no second manager in `main`, ordering path open for B, hold path open for C); negative/accepted (the stamp is identification, not authorization — the deployment authorizes the resolved tenant; a stamp only helps once producers upgrade; under per-tenant tenancy a stamp is ignored, which a mixed fleet must know; existence surfaces at first `deps.DB`; partition sizing is a documented rule, not enforced).
- Pointers to B (#1232) and C (#1231) as the ordered-source and hold halves.

**Seams (pre-agreed):** greps — `git grep -n 'Consumers on the shared broker are out of scope'` empty; `git grep -n 'ADR-087' wiki/architecture_decisions.md wiki/adr_041_shared_ledger_tenancy.md CLAUDE.md` non-empty; `git grep -n 'through ADR-087' wiki/architecture_decisions.md` one hit; `make lint-md` clean; `wc -c CLAUDE.md` < 40960.

- [ ] **Step 1: Write ADR-087**, the index entry, the counter, the ADR-041 pointer.
- [ ] **Step 2: The five doc edits and the two config-doc edits.**
- [ ] **Step 3: Run the greps above and `make lint-md`.**
- [ ] **Step 4: `make check`, commit** — `docs(messaging): ADR-087 and the shared-tenancy pages`.

### Task 14: Gates for PR A4 (controller only)

- [ ] **Step 1: `make check`.** **Step 2: `/simplify`.** **Step 3: `/security-audit`** — the relay's rehydration must not let a persisted stamp reach the broker as a caller header; the outbox conflict error must not echo the header value. **Step 4: `/code-review`** — CodeRabbit must see the final diff; a doc-vs-code pass on every claim in ADR-087 and the wiki edits. **Step 5: `make mutate`** after committing — Task 12's two branches (conflict, rehydrate) each need a separable failing test; Task 13 is operator-free, say so in the report.

---

## Self-review against the spec

- Decision 1–2 (stamp, single writer): Tasks 6, 10, 12. Decision 3 (kind tenancy, control-plane branch, pre-warm, env-parity): Tasks 1, 5. Decision 4 (check rules): Task 2. Decision 5 (read side, grammar, `TenantOptional`): Tasks 3, 7, 9. Decision 6 (fail closed): Tasks 7, 9. Decision 7 (accessor): Task 3. Decision 11 (streams gate): Tasks 2, 9. Decision 12 (ADR-087, ADR-041 pointer, no atom): Task 13. Glossary: Task 1's commit.
- Names used consistently: `TenantOptional`, `TenantStamps`, `tenantstamp.{Header, ErrConflict, CheckCallerHeaders, Write, Read, ReadError}`, `messaging.{TenantStampHeader, ErrTenantStampConflict}`, `streams.ErrTenantStampConflict`, `multitenant.{TenantID, ErrNoTenant, DefaultTenantIDPattern(), ValidateTenantID, ErrInvalidTenantID}`, `SetMessagingTenancy`, `controlPlaneMessaging`, `sharedMessaging()`, `perTenantMessaging()`.
