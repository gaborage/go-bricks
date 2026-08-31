# Inbox Per-Tenant Hold Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Implementers drive each task with `/mattpocock-skills:tdd` — the seams are pre-agreed in every task's **Seams** block; do not ask for them.

**Goal:** On the streams lane, a failed delivery is retried in place within a bound (D), then parked in a per-tenant *hold* ledger that keeps that tenant's later messages behind the failed one while the rest of the partition keeps flowing (C); a scheduled drain replays held tenants in order, releases them, and reports them (deliverable C of #1231).

**Architecture:** D lives in the delivery pipeline both lanes share (`messaging/internal/delivery`): a `Request` gains an optional `Retry` policy and the streams runner is the only lane that sets it. C is split along the package boundary the import graph forces: `messaging/streams` owns the *port* (`HoldLedger`, `HeldMessage`, `HoldReplayer`) and the runner's gate/park/stall/held-set; `inbox` owns the ledger (two tables on the control-plane database), the drain job, the lease, the gauges; `app` wires the two through duck-typed setters at `RegisterModule` time, exactly as it wires `sharedResolverSetter`. Order per tenant is total because a tenant hashes to one partition, so `(stream, stream_offset)` is a total order within a tenant.

**Tech Stack:** Go 1.26 · `github.com/rabbitmq/rabbitmq-stream-go-client` · PostgreSQL (pgx, `$1`) and Oracle (`:1`) through `database/types` · `database/testing` (`TestDB`, `NewRowSet`) · OpenTelemetry observable gauges · testify.

**Spec:** [docs/superpowers/specs/2026-08-29-multitenant-messaging-end-state.md](../specs/2026-08-29-multitenant-messaging-end-state.md) — Decision 9 and the C row of "The carve"; the same design is the "Agent Brief" comment on #1231 (`GH_TOKEN=$(gh auth token -u gaborage) gh issue view 1231`). Where they differ, the spec wins.

**Vocabulary:** [CONTEXT.md](../../CONTEXT.md) `### Tenancy` — *Hold*, *Tenant stamp*, *Partition key*, *Control-plane key*; `### Messaging` — *Delivery pipeline*, *Settlement*, *Carrier*. Use those words in comments, docs and commit messages; avoid "DLQ", "quarantine", "retry queue", "parking lot", "tenant header".

**Stack position:** three dependent PRs, one `/gh-stack`, merged bottom-up by the maintainer. **C is blocked by A (#1230):** its base is A's top branch `feat/messaging-tenancy-outbox-stamp-docs` until A merges, after which `gh stack sync` retargets it onto `main`. C consumes A's names verbatim: the stamp read in the streams runner (`tenantstamp.Read`, `TenantOptional`, `TenantStamps`), `multitenant.TenantID`/`ErrNoTenant`, `multitenant.SetTenant`. C is independent of B (#1232).

| PR | Branch | Base | Carries |
| --- | --- | --- | --- |
| C1 | `feat/streams-delivery-retry` | `feat/messaging-tenancy-outbox-stamp-docs` | Tasks 1–2 + Task 3 (gates) |
| C2 | `feat/inbox-hold-ledger` | `feat/streams-delivery-retry` | Tasks 4–7 + Task 8 (gates) |
| C3 | `feat/inbox-hold-drain` | `feat/inbox-hold-ledger` | Tasks 9–11 + Task 12 (gates) |

Titles: C1 `feat(messaging): retry a failed stream delivery in place within a bound`, C2 `feat(inbox): park a tenant's failed stream delivery in a hold ledger`, C3 `feat(inbox): drain held tenants in order and report them`. No `!`. New tables under `inbox.autocreatetable` plus documented DDL for managed migrations (Task 11) — no migrations atom: nothing an existing deployment runs changes unless it opts in.

## Global Constraints

- Test function names are **camelCase** (`TestRunRetriesAHandlerErrorUpToTheBound`); table-driven case names are **snake_case** (`{name: "permanent_parks_at_once"}`). The `check-test-conventions.sh` hook flags violations.
- Commit with `git commit -F <file>`; the commit hook rejects heredoc `-m`. Commits MUST be signed — if signing fails, STOP and report; never pass `--no-gpg-sign`, never set `commit.gpgsign=false`.
- Every `gh` call is prefixed `GH_TOKEN=$(gh auth token -u gaborage)`. Never switch the gh account globally.
- Implementers run `make check` before every commit, detached (`nohup sh -c 'make check' > /tmp/gb-lanes/check-C.log 2>&1 & disown`, then poll the log for `EXIT=`); `git branch --show-current` must print the branch of the PR the task belongs to. The controller runs the pre-push gates and every push.
- No new `//nolint`. Comments are bare-minimum: rationale a reader cannot derive, or a `// SECURITY:` annotation.
- **Import direction is fixed by cycles:** `messaging/streams` imports neither `github.com/gaborage/go-bricks/messaging`, nor `inbox`, nor `app` (`inbox` imports `app`, `app` imports `streams`). `inbox` MAY import `messaging/streams` (it already imports `messaging`). The port therefore lives in `streams`; the ledger and the drain live in `inbox`; `app` wires them.
- **The classic lane is byte-for-byte unchanged:** `delivery.Request.Retry == nil` means exactly one attempt, as today. The lane-contract suites (`messaging/internal/lanecontract`, run by `messaging/lanecontract_test.go` and `messaging/streams/lanecontract_test.go`) pass unchanged; do not edit their scenarios.
- `TestRegistryProcessMessagePerDeliveryLoggerAllocs` (`messaging/registry_test.go`) is a tripwire guard on allocs/op of the classic consume path. The retry loop itself adds none for a nil `Retry`; the delivery attributes cost one allocation per message for the slice they are set through, measured at 25 against a 24 baseline and a 42 ceiling.
- apidiff (CI job "API compatibility") fails an incompatible exported change. `streams.ConsumerOptions` and `SuperStreamConsumerOptions` already carry a `func` field (non-comparable), so new pointer/bool fields there are safe. `config.InboxConfig` gains one struct field of comparable members (`InboxHoldConfig`: bools, strings, durations — no map/slice). `messaging/internal/delivery` is internal. `inbox` gains only NEW exported names. No exported function changes arity. Every new field on those exported structs is a pointer or a scalar so comparability survives; a downstream UNKEYED struct literal does break, but that is not a compatibility class this repo tracks, and ADR-089 says so explicitly.
- A panic value is rendered by TYPE only (ADR-081): the hold ledger's `last_error` column and every log line carry `res.Err.Error()` for a `HandlerError` and the `panic in message handler (type: %T)` text the pipeline already built for a `Panicked` result — never the recovered value.
- **The ledger is control-plane infrastructure:** every hold operation resolves the database through the inbox module's `sharedDB` resolver (`inbox.tenancy: shared`); a hold configured on per-tenant ledgers is refused at `Init`. Database time (`NOW()` / `SYSTIMESTAMP`) is the clock for leases and due times so replicas with skewed clocks agree; Go's `time.Now()` is used only for the gauges' age arithmetic.
- **Never drop a held row** except on a successful replay. No retention, no purge, no cap: an operator deletes rows by hand.
- Streams handlers keep their signature `func(ctx context.Context, msg *Message) error`; the `Message` struct gains no field.
- Tenant stamps are read only under `TenantStamps` (A's Decision 3); a hold consumer therefore only works under `multitenant.enabled` + `messaging.tenancy: shared`. Task 6's startup refusal keys on exactly that pair PLUS a resolvable ledger — not merely on a nil `HoldLedger` — and is evaluated BEFORE any dial, so a deployment that could never carry a tenant stamp fails at startup rather than at the first delivery.

## Decisions the plan makes

1. **A `Panicked` outcome is not retried; only `HandlerError` is.** A handler that panics on a message will panic on it again; re-running it three times hides a bug behind backoff. A panic parks at once, exactly like a permanent error. `Result.Attempts` is 1 for it.
2. **`Retry == nil` keeps today's behaviour on a non-hold consumer.** The framework default (`MaxAttempts: 3`, `InitialBackoff: 200ms`, `MaxBackoff: 2s`, doubling) applies only when the declaration says `Hold: true` and `Retry` is nil. Silently giving every existing stream consumer three retries with backoff would be a behaviour change no deployment asked for; a consumer that wants D without C sets `Retry` explicitly.
3. **Park is a settlement action; gate is a handle-step short-circuit.** The glossary's *settlement* is "the lane-specific step that turns an outcome into a broker action: commit-offset or skip"; this plan adds *park* to that list, so the streams `Settle` closure decides between commit, skip and park. The *gate* runs before the handler, inside the pipeline's `Handle` step, and returns `nil` after a durable append, so the pipeline records a `Succeeded` outcome and the lane commits the offset — the span carries `messaging.hold.gated=true` so the success is not mistaken for a handled message.
4. **One durable write shape for gate and park: `HoldLedger.Park`.** Both insert the row and upsert the tenant's held marker in one transaction. A separate "append" call would race the drain's release (Hazard 2 below) by inserting a row under a tenant marker the drain just deleted; the upsert makes the tenant held again in the same statement pair, so the drain sees it next pass.
5. **Stall = block the partition's delivery callback.** The stream client calls `deliver` from the partition's own read loop, so a durable write that fails is retried inside the callback with backoff until it succeeds or the consume context is canceled (`StopConsumers`). Nothing is committed while stalled; a restart redelivers from the last committed offset, which is the at-least-once the spec demands.
6. **The drain lock is a lease row, not an advisory lock.** `<hold>_tenant` carries `lease_owner` and `lease_until`; a drainer acquires a tenant with one `UPDATE … WHERE lease_until IS NULL OR lease_until < NOW()` and re-checks it before each replay. There is no heartbeat: each replay runs under a context whose deadline is the lease expiry, so the lease IS the handler-time bound, and a replay that outlives its lease is discarded — the row stays, nothing is committed, and the tenant is re-leased on a later pass. This is the same SQL on PostgreSQL and Oracle, needs no session-scoped lock (the framework hands out pooled connections, so a session lock's owner is not the caller), no transaction held across handler replays (handlers run their own), and no `DBMS_LOCK` grant. A dead drainer's lease expires after `inbox.hold.leaseduration` (default 60s) and another replica proceeds. Cost: a crashed drainer's tenant waits up to one lease before it is picked up again.
7. **Replicas learn releases through their own drain pass, not through messaging.** Every replica's drain job ends a pass by pushing the ledger's held-tenant set into its local runners (`HoldReplayer.ReloadHeld`). A stale "held" on a runner costs one detour through the ledger and one extra pass; a stale "unheld" cannot happen, because only a partition's owner parks for that partition and it updates its own set synchronously, and a partition that moves reloads on the SAC promotion callback before it consumes.
8. **Replay does not retry inline.** The drain replays one row through the pipeline with `Retry == nil`; the drain's own per-tenant backoff (`attempts`, `next_attempt_at`, capped at `inbox.hold.maxbackoff`) is the retry. A permanent error during replay keeps the row and defers the tenant like any other failure — "never auto-dropped" applies to the drain too.
9. **A hold consumer's message with no tenant is refused and skipped, never parked.** A `ReadError` from the stamp (A's fail-closed) produces the lane's failure outcome; with no tenant there is nothing to key a hold on, so `Settle` skips as today. `Hold: true` beside `TenantOptional: true` is a declaration error.
10. **The gauges read a snapshot the drain wrote, not the database.** Observable-gauge callbacks fire on the exporter's schedule; querying the ledger from them would put a database read on the metrics path. The drain refreshes `HoldStats` per consumer at the end of each pass into an `atomic.Pointer`, and the callback reports it. The atomic protects each VALUE, not the map that holds them: the map itself is guarded by a mutex on every read and write, because an observable-gauge callback fires on the exporter's own schedule and would otherwise read the map while the drain writes `d.stats[consumer]`.
11. **`internal/ledgererr` replaces the outbox's private `boundPersistedError`.** The hold's `last_error` column needs the same 1 KiB bound, control-byte scrub and `...[truncated]` marker; copying thirty lines into `inbox` would trip SonarCloud's 3 % new-code duplication gate, so the helper moves to `internal/ledgererr.Bound` and `outbox` becomes its first caller (behaviour identical, its tests move with it).
12. **The hold's table names derive from one key.** `inbox.hold.tablename` (default `gobricks_inbox_hold`) names the row table; the tenant table is `<tablename>_tenant`; indexes are `idx_<tablename>_tenant_order` and `idx_<tablename>_tenant_due`. The name bound is `63 - len("idx__tenant_order")` = 46 — PostgreSQL's effective identifier limit, against the LONGEST derived name rather than the tenant table's suffix, so every derived name fits on both vendors.

13. **The park and drain backoffs reuse the delivery package's saturating series.** As C1 shipped it, `delivery.backoffFor` computes each wait as a bounded shift that SATURATES before it can wrap, and `delivery.BackoffBudget(r, budget)` walks that series to report a total and whether it passes over — the streams lane already bounds a declared policy with it against `streams.MaxRetryWait`. The drain's per-tenant backoff calls ONE exported helper there rather than growing a second copy of the arithmetic, and carries an overflow-boundary case of its own.

14. **A failed held-set reload fails closed.** The SAC promotion callback reloads the partition's held tenants before it consumes; if that read fails, the partition does NOT consume. It retries with backoff and logs once, because consuming with a stale or empty held set delivers a held tenant's later message ahead of the one it is held behind — the exact ordering the hold exists to keep.

15. **Every post-replay write is fenced by the lease.** The row delete, the tenant release and the backoff update each carry `AND lease_owner = ? AND lease_until > NOW()`, and a fenced write that affects ZERO rows is read as lease loss: the outcome is discarded and nothing is committed, so a drainer whose lease expired mid-replay cannot delete a row a second drainer is already replaying. With Decision 6's deadline this is the pair that makes a lost lease harmless rather than merely unlikely.

---

## PR C1 — D: bounded in-place retry

**Branch:** `feat/streams-delivery-retry`, cut from `feat/messaging-tenancy-outbox-stamp-docs`.

### Task 1: `delivery.Retry`, `delivery.Permanent`, and the retry loop in `Run`

**Files:**

- Modify: `messaging/internal/delivery/delivery.go` (`Request` struct, `Result` struct, `Run`, `invoke`)
- Create: `messaging/internal/delivery/retry.go`
- Modify: `messaging/internal/delivery/delivery_test.go` (the `harness` at `newHarness`/`h.run`/`h.runRequest` is the fixture)

**Interfaces:**

- Produces, in `delivery`:
  - `type Retry struct { MaxAttempts int; InitialBackoff, MaxBackoff time.Duration }` — `MaxAttempts` counts the first attempt; backoff before attempt n (n ≥ 2) is `min(InitialBackoff << (n-2), MaxBackoff)`; a zero `InitialBackoff` means no wait.
  - `func Permanent(err error) error` — wraps `err` in an unexported `permanentError{err}` whose `Error()` is `err.Error()` and whose `Unwrap()` returns `err`; `Permanent(nil)` returns nil.
  - `func IsPermanent(err error) bool` — `errors.As` on the wrapper; nil → false.
  - `Request.Retry *Retry` — nil means one attempt.
  - `Result.Attempts int` — attempts made (≥ 1 whenever `Handle` ran).
  - Span attribute `messaging.delivery.attempts` (int64) set on every delivery, and `messaging.delivery.permanent=true` when the final error `IsPermanent`.
- Behaviour of `Run`: `invoke` runs in a loop: after a `HandlerError` result, if `req.Retry != nil && attempt < req.Retry.MaxAttempts && !IsPermanent(res.Err) && ctx.Err() == nil`, wait the backoff (a `select` on `time.After` and `ctx.Done()`; a canceled ctx ends the loop with the last result), then invoke again. `Panicked` ends the loop (Decision 1). `LogOutcome`, the consume record and `Settle` see only the FINAL result; `Duration` covers all attempts including waits.
- Consumed by: Task 2 (the streams runner), Task 9 (the replay leaves `Retry` nil).

**Seams (pre-agreed):** `delivery.Run` through the existing test harness (`newHarness`, `h.run`, `h.runRequest`), observing the returned `*Result`, the handler's call count, the span exporter, and `h.rec.seen` (`LogOutcome` calls); `Permanent`/`IsPermanent` directly.

- [ ] **Step 1: Red — the policy**

| case name | `Retry` | handler behaviour | expect |
| --- | --- | --- | --- |
| `nil_retry_is_one_attempt` | nil | fails always | `HandlerError`, `Attempts == 1`, handler called once, one `LogOutcome` |
| `fails_twice_then_succeeds` | `{3, 1ms, 4ms}` | error, error, nil | `Succeeded`, `Attempts == 3`, handler called 3×, ONE `LogOutcome` with the success, `Duration ≥ 1ms+2ms` |
| `exhausts_the_bound` | `{3, 0, 0}` | fails always | `HandlerError` carrying the THIRD error (`assert.Same`), `Attempts == 3` |
| `permanent_short_circuits` | `{3, 0, 0}` | `Permanent(errors.New("bad"))` on call 1 | `HandlerError`, `Attempts == 1`, `IsPermanent(res.Err)`, `res.Err.Error() == "bad"` |
| `panic_is_not_retried` | `{3, 0, 0}` | panics | `Panicked`, `Attempts == 1` |
| `canceled_ctx_ends_the_loop` | `{5, 50ms, 50ms}` | fails always; test cancels ctx after the first failure | `HandlerError`, `Attempts` ≤ 2, `Run` returns within 100ms |
| `attempts_on_the_span` | `{2, 0, 0}` | error then nil | span attribute `messaging.delivery.attempts == int64(2)`; no `messaging.delivery.permanent` |
| `permanent_on_the_span` | `{2, 0, 0}` | permanent error | `messaging.delivery.permanent == true` |
| `permanent_of_nil_is_nil` | — | — | `Permanent(nil) == nil`; `IsPermanent(nil) == false`; `errors.Is(Permanent(io.EOF), io.EOF)` |

- [ ] **Step 2: Run `go test ./messaging/internal/delivery/ -run 'TestRun|TestPermanent' -count=1`, expect FAIL** (undefined `Retry`, `Permanent`, `Attempts`).
- [ ] **Step 3: Green** — `retry.go` (the three names and the backoff arithmetic as one function `backoffFor(r *Retry, attempt int) time.Duration`), the loop in `Run` around `invoke`, `Attempts` on `Result`, the two span attributes set after the loop (they need the final result, so they are set beside `RecordErrorByType`).
- [ ] **Step 4: `go test ./messaging/internal/delivery/... ./messaging/... -count=1` PASS**, including both lane-contract suites and `TestRegistryProcessMessagePerDeliveryLoggerAllocs` (`go test ./messaging/ -run PerDeliveryLoggerAllocs -count=1`).
- [ ] **Step 5: `make check`, commit** — `feat(messaging): retry a failed delivery in place within a bound`.

### Task 2: `Retry` and `Hold` on the stream consumer declarations; `streams.Permanent`

**Files:**

- Modify: `messaging/streams/streams.go` (`ConsumerOptions`, `SuperStreamConsumerOptions`)
- Modify: `messaging/streams/declarations.go` (`consumerDeclaration`, `DeclareConsumer`, `DeclareSuperStreamConsumer`, `consumerErrors`)
- Modify: `messaging/streams/manager.go` (`newRunner`)
- Modify: `messaging/streams/runner.go` (`consumerRunner`, `deliver` — set `Request.Retry`)
- Create: `messaging/streams/retry.go` (the exported policy type and `Permanent`)
- Modify: `messaging/streams/declarations_test.go`, `messaging/streams/runner_test.go`

**Interfaces:**

- Produces:
  - `type RetryOptions struct { MaxAttempts int; InitialBackoff, MaxBackoff time.Duration }` (a copy of the shape, not an alias — `delivery` is internal).
  - `ConsumerOptions.Retry *RetryOptions`, `ConsumerOptions.Hold bool`; the same two fields on `SuperStreamConsumerOptions`; both copied into `consumerDeclaration` (`Retry *RetryOptions`, `Hold bool`).
  - `func Permanent(err error) error` — `return delivery.Permanent(err)`; doc: "the handler's claim that retrying is pointless; the delivery parks at once when the consumer holds, and is skipped at once otherwise."
  - `var DefaultHoldRetry = RetryOptions{MaxAttempts: 3, InitialBackoff: 200 * time.Millisecond, MaxBackoff: 2 * time.Second}` — applied by `newRunner` when `decl.Hold && decl.Retry == nil` (Decision 2).
  - `consumerRunner.retry *delivery.Retry` (nil when none), set by `newRunner` from the declaration; `deliver` passes it as `Request.Retry`.
  - Validation in `consumerErrors`: `Retry.MaxAttempts < 1` → `consumer %q on stream %q declares MaxAttempts %d; at least 1 is required`; negative backoff → `… has a negative InitialBackoff/MaxBackoff`; `InitialBackoff > MaxBackoff` → `… InitialBackoff exceeds MaxBackoff`; `Hold && TenantOptional` → `consumer %q on stream %q cannot hold and be tenant-optional: a hold is keyed by the tenant` (Decision 9). `Hold` without a ledger is Task 6's `Start` error, not a declaration error.
- Consumes: Task 1's `delivery.Retry`, `delivery.Permanent`.

**Seams (pre-agreed):** `Declarations.Validate()` for the four rules; `consumerRunner.deliver` through `newTestRunner` with a counting handler and a `fakeStorer`, observing handler call count and `storer.offsets()`; `newRunner` through `Manager` internals as `declarations_test.go` already does (`startOnFake`, then the tracked runner's `retry`).

- [ ] **Step 1: Red**

| case name | declaration | expect |
| --- | --- | --- |
| `validate_rejects_zero_attempts` | `Retry: &RetryOptions{MaxAttempts: 0}` | error contains `MaxAttempts 0` |
| `validate_rejects_negative_backoff` | `Retry: &RetryOptions{MaxAttempts: 1, InitialBackoff: -1}` | error contains `negative InitialBackoff` |
| `validate_rejects_inverted_backoff` | `{3, 2s, 1s}` | error contains `exceeds MaxBackoff` |
| `validate_rejects_hold_with_tenant_optional` | `Hold: true, TenantOptional: true` | error contains `cannot hold and be tenant-optional` |
| `validate_accepts_hold_without_retry` | `Hold: true` | nil |
| `runner_retries_then_commits` | runner with `retry = {3, 0, 0}`, handler error×2 then nil | handler called 3×, `storer.offsets() == []int64{offset}` |
| `runner_without_retry_is_unchanged` | runner with `retry == nil`, failing handler | handler called once, offsets empty (`TestRunnerDeliverHandlerErrorSkipsOffsetCommit` stays green) |
| `hold_defaults_the_retry` | `startOnFake` with `Hold: true`, no `Retry` (and a `HoldLedger` from Task 6's fake — until Task 6 lands, test `newRunner` directly with a `consumerDeclaration{Hold: true}`) | runner's `retry` equals `DefaultHoldRetry` converted |
| `explicit_retry_without_hold` | `Retry: &RetryOptions{2, 0, 0}`, `Hold: false` | runner's `retry.MaxAttempts == 2` |

- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Green** — the fields, the copy in both declare methods, the four validation rules, `newRunner`'s conversion (`toDeliveryRetry(*RetryOptions) *delivery.Retry`), `Permanent`.
- [ ] **Step 4: `go test ./messaging/streams/... -count=1` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(messaging): declare a retry policy and a hold on stream consumers`.

### Task 3: Gates for PR C1 (controller only)

- [ ] **Step 1: `make check`** (detached, read `EXIT=0`).
- [ ] **Step 2: `/simplify`** — re-run `make check` if it changed code.
- [ ] **Step 3: `/security-audit`** — no error text or span attribute carries a handler error's message beyond what ADR-083 already allows; the backoff wait must select on `ctx.Done()` (a canceled consumer must not sleep out its backoff).
- [ ] **Step 4: `/code-review`** — apply findings, `make check` again, re-run if code changed.
- [ ] **Step 5: `make mutate`**, backgrounded, after committing. gremlins does not mutate `&&`: hand-flip the loop condition's `!IsPermanent(res.Err)` to `true` and confirm `permanent_short_circuits` fails; drop the `attempt < MaxAttempts` conjunct and confirm `exhausts_the_bound` fails. Name both in the report.

---

## PR C2 — C: the hold ledger, gate and park

**Branch:** `feat/inbox-hold-ledger`, cut from `feat/streams-delivery-retry`.

### Task 4: `inbox.hold.*` config, defaults, and the `Init` refusal

**Files:**

- Modify: `config/types.go` (`InboxConfig` — add `Hold InboxHoldConfig` after `Tenancy`; new `InboxHoldConfig` type beside it)
- Modify: `inbox/config.go` (`applyDefaults`, `validateConfig`), `inbox/module.go` (`Init`)
- Modify: `inbox/config_test.go`, `inbox/module_test.go`

**Interfaces:**

- Produces, in `config`:

  ```go
  type InboxHoldConfig struct {
      Enabled       bool          `koanf:"enabled" …`
      TableName     string        `koanf:"tablename" …`      // default "gobricks_inbox_hold"; tenant table is "<tablename>_tenant"
      DrainInterval time.Duration `koanf:"draininterval" …`  // default 5s
      MaxBackoff    time.Duration `koanf:"maxbackoff" …`     // default 5m; per-tenant drain backoff cap
      MaxAge        time.Duration `koanf:"maxage" …`         // default 1h; a tenant held longer logs one WARN per drain pass
      LeaseDuration time.Duration `koanf:"leaseduration" …`  // default 60s; drain lease per tenant
  }
  ```

  (every field carries the full five-tag set the sibling fields use). Doc comments state: hold requires `inbox.tenancy: shared`; rows are never auto-dropped; DDL for managed migrations is in `wiki/outbox.md`.

- Produces, in `inbox`: `DefaultHoldTableName = "gobricks_inbox_hold"`, `DefaultHoldDrainInterval = 5 * time.Second`, `DefaultHoldMaxBackoff = 5 * time.Minute`, `DefaultHoldMaxAge = time.Hour`, `DefaultHoldLeaseDuration = 60 * time.Second`; `applyDefaults` fills the zero values only when `Hold.Enabled`; `validateConfig` rejects a negative duration, a zero `LeaseDuration` (after defaults), and a hold table name that fails `validateHoldTableName` (Task 5). `func (m *Module) holdEnabled() bool { return m.cfg.Enabled && m.cfg.Hold.Enabled }`. `Init` returns `errors.New("inbox: hold requires inbox.tenancy: shared — a tenant whose database is down cannot hold its own messages; set inbox.tenancy: shared or disable inbox.hold")` when `m.cfg.Hold.Enabled && !m.sharedLedger()`, checked right after `validateConfig` and before the disabled short-circuit is NOT the place — put it after the `!m.cfg.Enabled` return so a disabled inbox with a stray hold key stays a no-op.
- Consumed by: Tasks 5, 7, 9, 10.

**Seams (pre-agreed):** `applyDefaults` + `validateConfig` through `Module.Init(deps)` with a `config.Config` literal (the existing `testDeps()` and `probeReadyDB()` fixtures); `m.cfg` after `Init` for the defaults.

- [ ] **Step 1: Red**

| case name | config | expect |
| --- | --- | --- |
| `hold_defaults_applied` | `Enabled`, `Tenancy: shared`, `Hold.Enabled` (+ `stubSharedDB` via `SetSharedResolvers`) | `m.cfg.Hold.TableName == "gobricks_inbox_hold"`, `DrainInterval == 5s`, `MaxBackoff == 5m`, `MaxAge == 1h`, `LeaseDuration == 60s` |
| `hold_disabled_leaves_zero_values` | `Enabled`, no hold | `m.cfg.Hold.TableName == ""` |
| `hold_rejects_per_tenant_tenancy` | `Enabled`, `Tenancy: per-tenant`, `Hold.Enabled` | `Init` error contains `inbox.tenancy: shared` |
| `hold_rejects_negative_backoff` | `Hold.MaxBackoff: -1` | error contains `maxbackoff` |
| `hold_rejects_bad_table_name` | `Hold.TableName: "bad; DROP"` | error |
| `disabled_inbox_ignores_hold` | `Enabled: false`, `Hold.Enabled: true`, `Tenancy: per-tenant` | nil (no-op module) |

- [ ] **Step 2: Run `go test ./inbox/ -run 'TestModuleInit|TestHold' -count=1`, expect FAIL.**
- [ ] **Step 3: Green.** **Step 4: `go test ./inbox/... ./config/... -count=1` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(inbox): add the inbox.hold configuration`.

### Task 5: The hold store — `HoldStore`, PostgreSQL and Oracle, `internal/ledgererr`

**Files:**

- Create: `internal/ledgererr/ledgererr.go`, `internal/ledgererr/ledgererr_test.go` (moved from `outbox/relay.go`'s `boundPersistedError`, `maxPersistedErrorBytes`, `truncationMarker` and `outbox/relay_test.go`'s `TestBoundPersistedErrorMakesArbitraryTextSafeToStore` table)
- Modify: `outbox/relay.go` (call `ledgererr.Bound`), `outbox/relay_test.go` (the two `BoundsTheErrorItPersists` tests keep asserting through the relay; the helper's own table moves)
- Create: `inbox/hold_store.go`, `inbox/hold_store_postgres.go`, `inbox/hold_store_oracle.go`
- Create: `inbox/hold_store_postgres_test.go`, `inbox/hold_store_oracle_test.go`, `inbox/hold_store_test.go`

**Interfaces:**

- Produces, in `ledgererr`: `const MaxBytes = 1024`, `const TruncationMarker = "...[truncated]"`, `func Bound(msg string) string` — identical behaviour to today's `boundPersistedError`.
- Produces, in `inbox`:

  ```go
  // HoldRow is one parked stream delivery. (Consumer, Stream, Offset) is its identity.
  type HoldRow struct {
      Consumer   string
      Stream     string   // the partition for a super stream
      Offset     int64
      TenantID   string
      Data       []byte
      Properties []byte   // JSON-encoded application properties, nil when none
      HeldAt     time.Time
  }
  // HoldTenant is one held tenant's drain state.
  type HoldTenant struct {
      Consumer      string
      TenantID      string
      HeldSince     time.Time
      Attempts      int
      NextAttemptAt time.Time
      LastError     string
  }
  type HoldStats struct { Tenants, Rows int64; OldestHeldSince time.Time }

  type HoldStore interface {
      // Park inserts the row (idempotent on its identity) and marks its tenant held, in tx.
      Park(ctx context.Context, tx dbtypes.Tx, row *HoldRow) (inserted bool, err error)
      HeldTenants(ctx context.Context, db dbtypes.Interface, consumer string) ([]string, error)
      // DueTenants lists held tenants whose next_attempt_at has passed and whose lease is free, oldest first.
      DueTenants(ctx context.Context, db dbtypes.Interface, consumer string, limit int) ([]HoldTenant, error)
      // AcquireLease takes or renews the drain lease; false when another owner holds a live one.
      AcquireLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, lease time.Duration) (bool, error)
      ReleaseLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) error
      // NextRows returns the tenant's rows in (stream, offset) order, at most limit.
      NextRows(ctx context.Context, db dbtypes.Interface, consumer, tenant string, limit int) ([]HoldRow, error)
      // Fenced by the lease: a write affecting no rows means the lease was lost, and
      // the caller discards the replay's outcome rather than failing the drain.
      DeleteRow(ctx context.Context, db dbtypes.Interface, consumer, stream string, offset int64, tenant, owner string) (deleted bool, err error)
      // Defer records a failed replay: attempts+1, next_attempt_at = NOW()+backoff, last_error bounded, lease cleared.
      Defer(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string, backoff time.Duration, lastErr string) (updated bool, err error)
      // Release deletes the tenant marker only if no rows remain; released reports whether it did.
      Release(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) (released bool, err error)
      Stats(ctx context.Context, db dbtypes.Interface, consumer string) (HoldStats, error)
      CreateTable(ctx context.Context, db dbtypes.Interface) error
  }
  func NewPostgresHoldStore(tableName string) (HoldStore, error)
  func NewOracleHoldStore(tableName string) (HoldStore, error)
  ```

  `validateHoldTableName(name string) error` — `sqlid.ValidateTableName`, unqualified, `len ≤ 63 - len("idx__tenant_order")` = 46 (Decision 12): PostgreSQL truncates an identifier past 63 bytes rather than refusing, and the INDEX names are the longest derived ones — budgeting only for the tenant table leaves both indexes to truncate to the SAME identifier, at which point the second CREATE INDEX quietly does nothing.

- DDL, PostgreSQL (`%s` = table name; the tenant table and both indexes derive from it):

  ```sql
  CREATE TABLE IF NOT EXISTS %s (
      consumer      VARCHAR(255) NOT NULL,
      stream        VARCHAR(255) NOT NULL,
      stream_offset BIGINT       NOT NULL,
      tenant_id     VARCHAR(255) NOT NULL,
      data          BYTEA,
      properties    TEXT,
      held_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
      PRIMARY KEY (consumer, stream, stream_offset)
  );
  CREATE INDEX IF NOT EXISTS idx_%s_tenant_order ON %s (consumer, tenant_id, stream, stream_offset);
  CREATE TABLE IF NOT EXISTS %s_tenant (
      consumer        VARCHAR(255) NOT NULL,
      tenant_id       VARCHAR(255) NOT NULL,
      held_since      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
      attempts        INTEGER NOT NULL DEFAULT 0,
      next_attempt_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
      lease_owner     VARCHAR(255),
      lease_until     TIMESTAMP WITH TIME ZONE,
      last_error      TEXT,
      PRIMARY KEY (consumer, tenant_id)
  );
  CREATE INDEX IF NOT EXISTS idx_%s_tenant_due ON %s_tenant (consumer, next_attempt_at);
  ```

  Oracle: `VARCHAR2`, `NUMBER(19)` for the offset, `NUMBER(10)` for attempts, `BLOB`, `CLOB`, `SYSTIMESTAMP`, named constraints `pk_%s` and `pk_%s_tenant`, no `IF NOT EXISTS` (ORA-00955 is tolerated by the caller, as the inbox table's is), no `DEFAULT` on `tenant_id` (it is always non-empty — a hold row without a tenant is refused by `Park` with `errors.New("inbox hold: a row without a tenant cannot be parked")` before any SQL).

- Statements (PostgreSQL; Oracle mirrors with `:n`, `SYSTIMESTAMP`, `NUMTODSINTERVAL(:n, 'SECOND')`, and `database.IsUniqueViolation` in place of `ON CONFLICT`):
  - `Park`: `INSERT INTO %s (consumer, stream, stream_offset, tenant_id, data, properties, held_at) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (consumer, stream, stream_offset) DO NOTHING` (inserted = rows affected 1), then `INSERT INTO %s_tenant (consumer, tenant_id) VALUES ($1,$2) ON CONFLICT (consumer, tenant_id) DO NOTHING`.
  - `HeldTenants`: `SELECT tenant_id FROM %s_tenant WHERE consumer = $1 ORDER BY tenant_id`.
  - `DueTenants`: `SELECT consumer, tenant_id, held_since, attempts, next_attempt_at, COALESCE(last_error, '') FROM %s_tenant WHERE consumer = $1 AND next_attempt_at <= NOW() AND (lease_until IS NULL OR lease_until < NOW()) ORDER BY held_since ASC LIMIT $2` (Oracle: `FETCH FIRST :2 ROWS ONLY`).
  - `AcquireLease`: `UPDATE %s_tenant SET lease_owner = $1, lease_until = NOW() + ($2 * INTERVAL '1 second') WHERE consumer = $3 AND tenant_id = $4 AND (lease_until IS NULL OR lease_until < NOW() OR lease_owner = $1)` — rows affected 1 = acquired or renewed (the `lease_owner = $1` disjunct is what lets the holder renew).
  - `ReleaseLease`: `UPDATE %s_tenant SET lease_owner = NULL, lease_until = NULL WHERE consumer = $1 AND tenant_id = $2 AND lease_owner = $3`.
  - `NextRows`: `SELECT consumer, stream, stream_offset, tenant_id, data, properties, held_at FROM %s WHERE consumer = $1 AND tenant_id = $2 ORDER BY stream, stream_offset LIMIT $3`.
  - `DeleteRow`: `DELETE FROM %s WHERE consumer = $1 AND stream = $2 AND stream_offset = $3`.
  - `Defer`: `UPDATE %s_tenant SET attempts = attempts + 1, next_attempt_at = NOW() + ($1 * INTERVAL '1 second'), last_error = $2, lease_owner = NULL, lease_until = NULL WHERE consumer = $3 AND tenant_id = $4` with `$2 = ledgererr.Bound(lastErr)`.
  - `Release`: `DELETE FROM %s_tenant WHERE consumer = $1 AND tenant_id = $2 AND NOT EXISTS (SELECT 1 FROM %s WHERE consumer = $1 AND tenant_id = $2)` — released = rows affected 1.
  - `Stats`: `SELECT COUNT(*) FROM %s_tenant WHERE consumer = $1`; `SELECT COUNT(*) FROM %s WHERE consumer = $1`; `SELECT MIN(held_since) FROM %s_tenant WHERE consumer = $1` (scan into `sql.NullTime`).

**Seams (pre-agreed):** each `HoldStore` method through `dbtesting.NewTestDB(dbtypes.PostgreSQL|Oracle)` expectations (`ExpectExec(...).WillReturnRowsAffected(n)`, `ExpectQuery(...).WillReturnRows(dbtesting.NewRowSet(cols...).AddRow(...))`, `ExpectTransaction()` for `Park`), asserting the SQL pattern, the args and the returned values — the shape of `inbox/store_postgres_test.go`; `ledgererr.Bound` directly with the moved table.

- [ ] **Step 1: Red — `ledgererr`**: move the outbox table verbatim; add `TestOutboxRelayUsesLedgererr` is NOT needed — the two relay tests that assert through the relay stay in `outbox` and keep proving the call site.
- [ ] **Step 2: Red — the stores**, per vendor, one test per method plus:

| case name | expect |
| --- | --- |
| `park_inserts_row_and_marks_tenant` | two `ExecExec` in the transaction; `inserted == true` on affected 1 |
| `park_is_idempotent_on_identity` | first `Exec` affected 0 → `inserted == false`, tenant upsert still runs, nil error |
| `park_refuses_a_row_without_tenant` | error before any SQL (`AssertNoTransaction`) |
| `acquire_lease_free_returns_true` | affected 1 → true |
| `acquire_lease_held_by_other_returns_false` | affected 0 → false, nil error |
| `defer_bounds_last_error` | args carry `len(arg) ≤ ledgererr.MaxBytes` and the `...[truncated]` suffix for a 9000-byte input |
| `release_only_when_empty` | affected 1 → true; affected 0 → false |
| `next_rows_orders_by_stream_then_offset` | SQL pattern contains `ORDER BY stream, stream_offset` |
| `stats_with_no_tenants` | `MIN` scans NULL → `OldestHeldSince.IsZero()` |
| `table_name_bound` | `63 - len("idx__tenant_order")` = 46: `NewPostgresHoldStore(strings.Repeat("a", 46))` accepted, 47 rejected with `too long`; the same bound on the Oracle store; assert EVERY derived name fits, not just the tenant table |
| `oracle_unique_violation_is_not_inserted` | `Exec` returns an `ORA-00001`-shaped error `database.IsUniqueViolation` recognises → `inserted == false`, nil |

- [ ] **Step 3: Run, expect FAIL.** **Step 4: Green.**
- [ ] **Step 5: `go test ./inbox/... ./outbox/... ./internal/ledgererr/... -count=1` PASS.**
- [ ] **Step 6: `make check`, commit** — `feat(inbox): the hold ledger store for PostgreSQL and Oracle` (the `ledgererr` extraction is its own preceding commit: `refactor(outbox): move the persisted-error bound to internal/ledgererr`).

### Task 6: The port and the runner — `HoldLedger`, `HeldMessage`, gate, park, stall, held set, SAC reload

**Files:**

- Create: `messaging/streams/hold.go` (the port, `HeldMessage`, `heldSet`, `HoldReplayer` — the replayer's METHODS are implemented in Task 9; declare the interface here so Task 7 can wire the setter type)
- Modify: `messaging/streams/manager.go` (`ManagerOptions.Hold HoldLedger`; `Start` — reject `Hold: true` declarations with a nil ledger; `startStreamConsumer`/`startSuperStreamConsumer` — load the held set before `env.NewConsumer`, reload inside the SAC promotion closure; `runningConsumer` gains `runner *consumerRunner`)
- Modify: `messaging/streams/runner.go` (`consumerRunner` — `hold HoldLedger`, `held *heldSet`, `holdName string`; `deliver`; `commitOffset`)
- Create: `messaging/streams/hold_fake_test.go` (`fakeHoldLedger`), Modify: `messaging/streams/runner_test.go`, `messaging/streams/manager_test.go`

**Interfaces:**

- Produces, in `streams`:

  ```go
  // HeldMessage is one parked delivery as the ledger sees it.
  type HeldMessage struct {
      Consumer   string
      Stream     string
      Offset     int64
      TenantID   string
      Data       []byte
      Properties map[string]any
      HeldAt     time.Time
  }
  // HoldLedger is the port the runner parks through. Park is idempotent on
  // (Consumer, Stream, Offset) and marks the tenant held in the same write.
  type HoldLedger interface {
      Park(ctx context.Context, msg *HeldMessage) error
      HeldTenants(ctx context.Context, consumer string) ([]string, error)
  }
  // HoldReplayer is what the drain (inbox) drives; implemented by *Manager in C3.
  type HoldReplayer interface {
      HoldConsumers() []string
      Replay(ctx context.Context, consumer string, msg *HeldMessage) error
      ReloadHeld(consumer string, tenants []string)
  }
  ```

  `heldSet`: `sync.RWMutex` + `map[string]struct{}`; `has(tenant) bool`, `add(tenant)`, `replace(tenants []string)`. `ManagerOptions.Hold HoldLedger` (nil = no hold). `Start` returns `errors.New("streams: consumer %q on %s %q declares Hold but no hold ledger is configured; set inbox.enabled, inbox.tenancy: shared and inbox.hold.enabled")` for the first such declaration, before dialing. `newRunner` sets `hold`, `held`, `retry`. `func (m *Manager) reloadHeld(ctx, runner) error` — `HeldTenants` → `held.replace`; called in `startStreamConsumer`/`startSuperStreamConsumer` before `env.NewConsumer`/`NewSuperStreamConsumer` (a failure fails startup: `failed to load held tenants for consumer %q: %w`), and inside the SAC promotion closure before `resolveOffset` (a failure there is logged at ERROR with `Msg("Could not reload held tenants on promotion; gating from the last known set")` — the closure cannot fail the promotion).

- Behaviour of `deliver` for a runner with `hold != nil` (a runner with `hold == nil` is byte-for-byte A's):
  1. The runner reads NO stamp of its own. A's delivery pipeline reads it in `seedTenant`, above the retry loop, and plants it with `multitenant.SetTenant`; a stamp it refuses becomes a `HandlerError` before the handler runs, so `Attempts` stays 0 and the loop never sees it. The runner learns the tenant inside the `Handle` closure via `multitenant.TenantID(msgCtx)` and captures it in a per-delivery local, which the `Settle` closure closes over — both run on the partition's own goroutine, one delivery at a time, so the local needs no guard, and it stays EMPTY exactly when the pipeline refused the delivery, which is the same case as "no tenant to hold".
  2. `Handle` closure: `if readErr != nil { return readErr }` (Decision 9); `if r.held.has(tenant) { return r.park(msgCtx, heldMessage, gated=true) }` — `park` returns nil after a durable write, so the pipeline records `Succeeded`; span extra `attribute.Bool("messaging.hold.gated", true)` is added to `SpanExtras` when gated (append it inside the closure is impossible — instead set it through `trace.SpanFromContext(msgCtx).SetAttributes(...)`); otherwise `multitenant.SetTenant` and call the handler as A does.
  3. `Settle` closure: `res.Err == nil` → `commitOffset` as today. `res.Err != nil && tenant != "" && (res.Outcome == HandlerError || Panicked)` → `if err := r.park(r.baseCtx, heldMessage, gated=false); err == nil { commit as if succeeded: r.offsets.trackerFor(streamName).record(offset, nil, store) }` — the WARN line `Tenant held: delivery parked` with fields `stream`, `consumer`, `offset`, `tenant`, `attempts` (`res.Attempts`), `error_type` (`fmt.Sprintf("%T", res.Err)`) and, for a `HandlerError`, `Str("error", ledgererr.Bound(res.Err.Error()))` — the same 1 KiB bound the ledger column takes, since a handler's message reaches the log line as well as the row; for `Panicked`, `res.Err` is already the type-only text and is rendered as it stands (ADR-081). `res.Err != nil && tenant == ""` → skip as today (the empty local IS the refusal case; there is no separate branch for it).
  4. `park(ctx, msg, gated)`: `r.hold.Park(ctx, msg)` in a loop with backoff `min(200ms << n, 5s)` until nil or `ctx.Err() != nil` (Decision 5); each failed attempt logs `ERROR "Hold ledger write failed; partition stalled until it succeeds"` with `attempt`; on success `r.held.add(msg.TenantID)`. A canceled ctx returns the ctx error: in `Handle` that becomes a `HandlerError` (and `Settle` then sees `tenant != ""`, calls `park` again, which returns immediately on the canceled ctx and skips — nothing committed, nothing lost); in `Settle` it returns without committing.
  5. `heldMessage` is built once per delivery: `&HeldMessage{Consumer: r.name, Stream: streamName, Offset: offset, TenantID: tenant, Data: msg.Data, Properties: message.ApplicationProperties, HeldAt: time.Now()}`.

**Seams (pre-agreed):** `consumerRunner.deliver` through `newTestRunner`-style construction with `hold: &fakeHoldLedger{}` and `tenantStamps: true`, a message whose `ApplicationProperties` carries `x-tenant-id`, a counting handler, and a `fakeStorer` — observing the fake's recorded `Park` calls (in order, with `TenantID`, `Offset`), `storer.offsets()`, the handler's calls, and the runner's `held` set; `Manager.Start` through `startOnFake` for the ledger-required error, the start-time load (`fakeHoldLedger.heldCalls`), and the promotion reload (`fake.consumer(partition).promote(partition)` then the runner's set). `fakeHoldLedger`: records `parks []*HeldMessage`, `heldCalls int`, returns `held map[string][]string` per consumer, injects `parkErr error` for the first `failParkTimes` calls.

- [ ] **Step 1: Red**

| case name | setup | expect |
| --- | --- | --- |
| `gate_parks_a_held_tenant_without_running_the_handler` | held = {acme}; deliver offset 7 for acme | handler not called; one `Park` with `{TenantID: acme, Offset: 7, Data, Properties}`; `storer.offsets() == []int64{7}` |
| `gate_lets_another_tenant_through` | held = {acme}; deliver for `globex` | handler called with `TenantID(ctx) == "globex"`; committed; no `Park` |
| `park_after_exhausted_retry_commits_the_offset` | held = {}; retry {2,0,0}; handler fails always; deliver offset 9 for acme | handler called 2×; one `Park` with offset 9; offset 9 committed; runner's set now has acme; WARN line `Tenant held: delivery parked` with `attempts=2` and the error TYPE |
| `park_after_permanent_error_parks_at_once` | handler returns `Permanent(err)` | handler called once; one `Park` |
| `park_after_panic_parks_at_once` | handler panics | one `Park`; committed; the WARN carries `panic_type` via `AppendOutcome`, never the value |
| `later_message_for_a_parked_tenant_is_gated` | after the park above, deliver offset 10 for acme | handler not called; second `Park` with offset 10; committed |
| `no_tenant_is_skipped_not_parked` | property absent (fail-closed `ReadError`) | handler not called; NO `Park`; offset NOT committed (today's skip) |
| `park_failure_stalls_then_succeeds` | `failParkTimes: 2` | `Park` called 3×; the handler's offset committed after the third; total wall time ≥ 200ms+400ms |
| `park_failure_with_canceled_ctx_commits_nothing` | `failParkTimes: 100`; runner `baseCtx` canceled by the test after the first failure | `deliver` returns; offsets empty; `Park` calls ≤ 2 |
| `start_refuses_hold_without_ledger` | decl `Hold: true`, `ManagerOptions.Hold == nil` | `Start` error contains `inbox.hold.enabled`; nothing dialed (`fake.recorded()` empty) |
| `start_loads_the_held_set` | ledger holds {acme} for the consumer | after `startOnFake`, a delivery for acme is gated |
| `promotion_reloads_the_held_set` | ledger changed to {globex} after start; `fake.consumer(testPartition0).promote(testPartition0)` | a delivery for globex is gated; for acme runs |
| `promotion_reload_failure_keeps_the_last_set` | ledger returns an error on the second `HeldTenants` | promotion still returns an offset spec; ERROR line logged; acme still gated |

- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Green** — `hold.go`, the runner fields and the three closures, the manager's load/reload and the `Start` guard, `runningConsumer.runner`.
- [ ] **Step 4: `go test ./messaging/streams/... -count=1 -race` PASS**, including `TestStreamsLaneSatisfiesTheFailureContract` unchanged.
- [ ] **Step 5: `make check`, commit** — `feat(messaging): gate and park stream deliveries through a hold ledger`.

### Task 7: Wiring — the inbox ledger adapter, the app setters, the streams manager option

**Files:**

- Create: `inbox/hold.go` (`holdLedger` adapter, `Module.HoldLedger()`, the lazy hold store)
- Modify: `inbox/module.go` (`Init` — the hold store's lazy init mirrors `ensureStoreInitialized` with a second `tenantstore.Cache[HoldStore]`, `SingleKey: true`, `NewPostgres: NewPostgresHoldStore`, `NewOracle: NewOracleHoldStore`, `WarnMsg: "Inbox hold table creation failed (may already exist)"`; `verifyStartupDatabase` — when `holdEnabled()`, probe the hold ledger too with `HeldTenants(ctx, db, "")`, a read that proves table + privileges)
- Modify: `app/interfaces.go` (`holdLedgerProvider interface { HoldLedger() streams.HoldLedger }`, `holdReplayerSetter interface { SetHoldReplayer(func() streams.HoldReplayer) }`), `app/app.go` (`RegisterModule` — capture the provider into `a.holdLedgers []holdLedgerProvider` and call `SetHoldReplayer(...)` — `*Manager` does not implement `HoldReplayer` until Task 9, so in this task the source returns a plain nil rather than a guarded `a.streamsManager`: a typed-nil `*Manager` would satisfy the interface while implementing none of it, and the guard the plan first described cannot help because the manager does not compile into the interface yet. Task 9 makes the source return the manager. C2 therefore also ships `assertHoldIsDrainable` in `app/streams_setup.go`, which REFUSES a configured hold while `*streams.Manager` does not implement `HoldReplayer` — a hold nothing can drain is a permanent hold, and C2 must be safe to ship standalone. **Task 9 removes that guard and its two tests as part of making the manager a replayer**; it is written against the type, so it stops firing on its own the moment the methods exist, and deleting it is the deliberate half), `app/streams_setup.go` (`prepareStreamConsumers` — `Hold: a.holdLedger()`, the first non-nil `HoldLedger()` among providers; two providers → startup error)
- Modify: `inbox/module_test.go`, `app/streams_setup_test.go`, `app/app_test.go` (beside the `sharedResolverSetter` injection test)

**Interfaces:**

- Produces, in `inbox`:
  - `func (m *Module) HoldLedger() streams.HoldLedger` — nil unless `holdEnabled()`; otherwise `&holdLedger{module: m}`.
  - `holdLedger.Park(ctx, msg)`: resolves `m.getDB(ctx)` (the shared resolver), the lazy hold store, converts `*streams.HeldMessage` → `*HoldRow` (`Properties` JSON-encoded with `encoding/json`; nil map → nil), runs `database.WithTx(ctx, db, func(ctx, tx) error { _, err := store.Park(ctx, tx, row); return err })`.
  - `holdLedger.HeldTenants(ctx, consumer)`: store `HeldTenants` on the shared db.
  - `func (m *Module) SetHoldReplayer(src func() streams.HoldReplayer)` — stored for Task 9's drain.
- Produces, in `app`: the two duck-types above; `func (a *App) holdLedger() streams.HoldLedger`.

**Seams (pre-agreed):** `Module.HoldLedger()` + `Park`/`HeldTenants` through `TestDB` expectations on the shared resolver (`stubSharedDB` returning a `TestDB` with `ExpectTransaction().ExpectExec("INSERT INTO gobricks_inbox_hold")…`); `App.RegisterModule` with a module implementing both duck-types, observing the captured provider and that the setter received a func whose call returns nil before `prepareRuntime`; `prepareStreamConsumers` through `app/streams_setup_test.go`'s existing fixture, asserting the `ManagerOptions.Hold` the manager was built with (read `a.streamsManager` internals from the `app` test via an exported-for-test accessor is NOT allowed — assert instead that a `Hold: true` declaration starts when a provider is registered and fails with the Task 6 error when none is).

- [ ] **Step 1: Red**

| case name | expect |
| --- | --- |
| `hold_ledger_nil_when_disabled` | `HoldLedger() == nil` after `Init` without hold |
| `park_writes_through_the_shared_resolver` | `Park` runs one transaction with two `INSERT` execs against the `stubSharedDB` db; `Properties` arrive JSON-encoded |
| `held_tenants_reads_the_ledger` | `ExpectQuery("SELECT tenant_id").WillReturnRows(NewRowSet("tenant_id").AddRow("acme"))` → `[]string{"acme"}` |
| `startup_probe_covers_the_hold_table` | `Init` with hold enabled runs the `SELECT tenant_id` probe; a probe error fails `Init` with `TableUnusableError("inbox", <hold table>, "inbox.autocreatetable", …)` |
| `register_module_captures_the_provider` | a fake module with `HoldLedger()` → `a.holdLedgers` has it; a second provider → `prepareStreamConsumers` error contains `two modules provide a hold ledger` |
| `register_module_injects_the_replayer_source` | the fake's setter received a non-nil func; calling it before `prepareRuntime` returns nil |
| `streams_start_with_hold_consumer_and_provider` | `Hold: true` declaration + registered provider → start succeeds (fake env) |

- [ ] **Step 2: Run, expect FAIL.** **Step 3: Green.** **Step 4: `go test ./inbox/... ./app/... ./messaging/streams/... -count=1` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(inbox): expose the hold ledger to the streams lane`.

### Task 8: Gates for PR C2 (controller only)

- [ ] **Step 1: `make check`.** **Step 2: `/simplify`** — `make check` again if it changed code.
- [ ] **Step 3: `/security-audit`** — `last_error` and every WARN/ERROR line carry a bounded message or a type, never a panic value; the hold tables' names go through `validateHoldTableName` before any `fmt.Sprintf` into SQL; `Properties` are JSON-encoded, never string-concatenated; the stall loop selects on the consume context.
- [ ] **Step 4: `/code-review`**, re-run if findings changed code.
- [ ] **Step 5: `make mutate`** after committing. Hand-apply: delete the `r.held.has(tenant)` gate and confirm `gate_parks_a_held_tenant_without_running_the_handler` fails; swap `record(offset, nil, store)` after a park for `record(offset, res.Err, store)` and confirm `park_after_exhausted_retry_commits_the_offset` fails; make `Release`'s `NOT EXISTS` an `EXISTS` and confirm `release_only_when_empty` fails. Name all three.

---

## PR C3 — the drain, release, visibility, ADR-089 and docs

**Branch:** `feat/inbox-hold-drain`, cut from `feat/inbox-hold-ledger`.

### Task 9: `Manager.Replay`/`ReloadHeld`/`HoldConsumers` and the drain job

**Files:**

- Modify: `messaging/streams/manager.go` (`HoldConsumers`, `Replay`, `ReloadHeld`; the compile-time guard `var _ HoldReplayer = (*Manager)(nil)`), `messaging/streams/runner.go` (`replay`)
- Create: `inbox/hold_drain.go` (`HoldDrain`), `inbox/hold_drain_test.go` (with a `fakeJobCtx` copied in shape from `outbox/test_helpers_test.go` — embedded `context.Context`, field-backed accessors — and a `fakeHoldReplayer`)
- Modify: `inbox/module.go` (`RegisterJobs` — register `inbox-hold-drain` with `registrar.FixedRate` when `holdEnabled()`, after the cleanup registration, error `inbox: failed to register hold drain job: %w`)
- Modify: `messaging/streams/manager_test.go`, `inbox/module_test.go`

**Interfaces:**

- Produces, in `streams`:
  - `func (m *Manager) HoldConsumers() []string` — names of running consumers whose runner has `hold != nil` (under `m.mu`).
  - `func (m *Manager) Replay(ctx context.Context, consumer string, msg *HeldMessage) error` — finds the running consumer by name (under `m.mu`, snapshot the runner, release the lock before replaying); unknown name → `fmt.Errorf("streams: no running consumer %q to replay through", consumer)`. `runner.replay(ctx, msg)` runs `delivery.Run` with `Carrier: propertyAccessor(msg.Properties)`, `Destination: msg.Stream`, `SpanExtras` = the two lane attributes plus `attribute.Bool("messaging.hold.replay", true)`, `Retry: nil` (Decision 8), `Handle` = `multitenant.SetTenant(msgCtx, msg.TenantID)` then `r.handler(msgCtx, &Message{Data, Offset, Stream, Properties})`, `LogOutcome` = the lane's `logOutcome` with an extra `Bool("hold_replay", true)` field, `Settle: nil` (settlement is the drain's: delete or defer). Returns `res.Err`.
  - `func (m *Manager) ReloadHeld(consumer string, tenants []string)` — `runner.held.replace(tenants)`; unknown consumer is a no-op.
- Produces, in `inbox`:

  ```go
  type HoldDrain struct {
      store    HoldStore         // a lazyHoldStore over the module, the lazyStore shape
      getDB    func(context.Context) (dbtypes.Interface, error)
      replayer func() streams.HoldReplayer
      cfg      config.InboxHoldConfig
      owner    string            // hostname + "/" + pid + "/" + 8 random hex bytes, minted once
      stats    map[string]*atomic.Pointer[HoldStats]  // per consumer, for Task 10
      now      func() time.Time  // Go clock for the WARN age only
  }
  func (d *HoldDrain) Execute(jobCtx scheduler.JobContext) error
  ```

  Pass, per consumer in `replayer().HoldConsumers()` (a nil replayer — streams never started — logs DEBUG and returns nil):

  1. `due := store.DueTenants(ctx, db, consumer, 100)`.
  2. For each tenant: `AcquireLease(…, d.owner, cfg.LeaseDuration)`; false → continue (another replica). Then loop: `rows := NextRows(…, tenant, 50)`; empty → `Release` → if released, log INFO `Tenant released from hold` (`tenant`, `consumer`, `held_for`); break. For each row: renew the lease (`AcquireLease` again) when `time.Since(leaseTaken) > cfg.LeaseDuration/2`; `err := replayer.Replay(ctx, consumer, toHeldMessage(row))`; nil → `DeleteRow`; error → `Defer(…, tenant, backoff(attempts+1), err.Error())`, log WARN `Hold replay failed; tenant deferred` (`tenant`, `consumer`, `stream`, `offset`, `attempts`, `next_attempt_in`, `error_type`), `ReleaseLease` is implied by `Defer` clearing it; stop this tenant. `backoff(n) = min(cfg.DrainInterval << (n-1), cfg.MaxBackoff)` (n ≥ 1).
  (Decoding a held row's `Properties` is producer-controlled input: the column is unbounded and the map came off the wire, so the replay caps the byte length BEFORE `json.Unmarshal` — the payload-size rule ADR-086 applies to logs, applied here to a replay — and refuses an over-cap row rather than decoding it.)

  3. After all tenants: `held := HeldTenants(…)`; `replayer.ReloadHeld(consumer, held)`; `stats := Stats(…)` → store into `d.stats[consumer]`; for each due-or-not tenant older than `cfg.MaxAge` (from `DueTenants` plus a `HeldTenants`-sized read — use `Stats.OldestHeldSince` for the gauge and one WARN `Hold exceeds max age` per tenant whose `HeldSince` is older, read from the `DueTenants` rows and, for leased/deferred tenants, from a `HeldTenantDetails` listing: `ListTenants(ctx, db, consumer) ([]HoldTenant, error)` is part of `HoldStore` from Task 5, with both vendor implementations and their tests — `SELECT consumer, tenant_id, held_since, attempts, next_attempt_at, COALESCE(last_error,'') FROM %s_tenant WHERE consumer = $1 ORDER BY held_since`).
  4. Errors: per-tenant errors are collected with `errors.Join` and returned so the scheduler records the failure; a panic inside one tenant's loop is recovered per tenant (`fmt.Errorf("inbox hold drain: tenant %q: panic (type: %T)", tenant, r)`) — the shape of `multitenant.cleanupTenant`.

- Consumed by: Task 10 (stats), Task 11 (docs).

**Seams (pre-agreed):** `HoldDrain.Execute(fakeJobCtx)` with a `TestDB` behind `getDB` (expectations per statement, in order) and a `fakeHoldReplayer` recording `Replay` calls and `ReloadHeld` args, returning scripted errors per (consumer, offset); `Manager.Replay` through `startOnFake` + a handler that records `multitenant.TenantID(ctx)` and `msg.Offset`, with `lanecontract.SetupTelemetry(t)` for the span attribute; `Manager.ReloadHeld` observed through a subsequent gated `deliver`.

- [ ] **Step 1: Red**

| case name | script | expect |
| --- | --- | --- |
| `replay_runs_the_handler_with_the_tenant` | `Replay(ctx, testConsumerName, {TenantID: acme, Offset: 4})` | handler saw `acme`, offset 4; nil; span has `messaging.hold.replay == true`; no offset stored |
| `replay_returns_the_handler_error_untouched` | handler returns `errX` | `assert.Same(errX)` |
| `replay_unknown_consumer` | name `nope` | error contains `no running consumer "nope"` |
| `reload_held_swaps_the_set` | `ReloadHeld(name, {"globex"})` then deliveries | globex gated, acme runs |
| `hold_consumers_lists_hold_runners` | two consumers, one `Hold: true` | `[]string{that one}` |
| `drain_replays_in_order_and_deletes` | due {acme}; rows offsets 3,4,5; replayer succeeds | `Replay` called 3× in order 3,4,5; `DELETE` ×3; `Release` affected 1; INFO released; `ReloadHeld(consumer, [])` |
| `drain_defers_on_first_failure` | rows 3,4,5; replayer fails on 4 | `Replay` for 3 and 4 only; `DELETE` ×1; `Defer` with `attempts`-derived backoff and the error text; no `Release`; `ReloadHeld` still called with the ledger's set |
| `drain_skips_a_leased_tenant` | `AcquireLease` affected 0 | no `NextRows`, no `Replay` |
| `drain_backoff_caps_at_maxbackoff` | `cfg.DrainInterval=5s, MaxBackoff=5m`, attempts 20 | `Defer` receives 5m |
| `drain_panic_in_one_tenant_does_not_stop_others` | replayer panics for acme; globex fine | error names acme with `panic (type: string)`; globex drained |
| `drain_without_streams_is_a_noop` | replayer source returns nil | nil, no SQL |
| `drain_warns_past_max_age` | `ListTenants` returns held_since = now−2h, `MaxAge = 1h` | one WARN `Hold exceeds max age` with `tenant`, `held_for` |
| `register_jobs_adds_the_drain` | hold enabled | `fakeRegistrar` saw `FixedRate("inbox-hold-drain", …, 5s)` |

- [ ] **Step 2: Run, expect FAIL.** **Step 3: Green.** **Step 4: `go test ./inbox/... ./messaging/streams/... -count=1 -race` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(inbox): drain held tenants in order under a lease`.

### Task 10: Gauges

**Files:**

- Create: `inbox/hold_metrics.go`, `inbox/hold_metrics_test.go`
- Modify: `inbox/module.go` (`Init` — when `holdEnabled()` and `deps.MeterProvider != nil`, register; `Shutdown` — unregister)
- Modify: `inbox/hold_drain.go` (the snapshot write is already in Task 9; this task reads it)

**Interfaces:**

- Produces: meter `go-bricks/inbox`; instruments `inbox.hold.tenants` (`Int64ObservableGauge`, "Tenants currently held on this consumer"), `inbox.hold.rows` (`Int64ObservableGauge`), `inbox.hold.oldest_age` (`Float64ObservableGauge`, unit `s`, `time.Since(OldestHeldSince).Seconds()` or 0 when zero); attribute `messaging.consumer.name`. One `RegisterCallback` over the three, reading `d.stats` (Decision 10); the returned `metric.Registration` is unregistered in `Shutdown`. Instrument-creation errors are logged and the module keeps running (the `database/internal/tracking` shape).

**Seams (pre-agreed):** `obtest.NewTestMeterProvider()` handed through `ModuleDeps.MeterProvider`; after a drain pass with a `TestDB` script (`Stats` returning tenants=2, rows=5, oldest=now−90s) collect and assert the three values per consumer attribute; with no pass yet, the callback reports nothing for that consumer.

- [ ] **Step 1: Red** — the three assertions above, plus `shutdown_unregisters` (a second `Collect` after `Shutdown` reports no `inbox.hold.*` metrics).
- [ ] **Step 2: Run, expect FAIL.** **Step 3: Green.** **Step 4: `go test ./inbox/... -count=1` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(inbox): report held tenants, rows and oldest age`.

### Task 11: ADR-089, the docs, and the ADR-059 pointer

**Files:**

- Create: `wiki/adr_089_per_tenant_hold_on_the_streams_lane.md` (the number renumbers by merge order after ADR-087 — A — and B's; the controller assigns the final number at merge)
- Modify: `wiki/architecture_decisions.md` (entry at the top of the list in the ADR-086 shape; the counter `through ADR-0NN`)
- Modify: `wiki/adr_059_streams_consumption.md` (a blockquote after the header: `> **Extended by ADR-089 (2026-08-29):** "parking failed messages" from Future work now exists as the per-tenant hold; the skip described below is the behaviour of a consumer that does not declare Hold.`)
- Modify: `wiki/streams.md` (the "A failed message is skipped, not redelivered" paragraph gains D and C: `Retry`, `streams.Permanent`, `Hold: true`, the gate/park/drain/release sequence, the stall, the ownership-transfer invariant, the gauges, the max-age WARN, "never auto-dropped"; the "Parking failed messages is future work" sentence goes)
- Modify: `wiki/outbox.md` (new `### Hold ledger` under `## Multi-Tenant` after "Shared (control-plane) ledger": the two tables with the full DDL for both vendors — copied from Task 5 — the config keys, `inbox.tenancy: shared` requirement, the operator purge statement against the CONFIGURED names — `DELETE FROM <inbox.hold.tablename> WHERE consumer = … AND tenant_id = …` followed by `DELETE FROM <inbox.hold.tablename>_tenant …`, never the default spelled literally, and the lease semantics)
- Modify: `llms.txt` (in the messaging/streams section: a `DeclareSuperStreamConsumer` example with `Hold: true`, `Retry: &streams.RetryOptions{…}`, a handler returning `streams.Permanent(err)`; the `inbox:` config block gains `hold:` keys)
- Modify: `CLAUDE.md` `**inbox/**` line: append "`inbox.hold.*` parks a tenant's failed stream deliveries in order and drains them (ADR-089)"; `wc -c CLAUDE.md` must stay under 40,960.

**ADR-089 content (write it in full):**

- Header: `# ADR-089: A Failed Stream Delivery Is Retried, Then Held Per Tenant`; `- **Status**: Accepted`; `- **Date**: 2026-08-29`; `- **Related**: ADR-059 (skip-on-failure, now the no-hold behaviour), ADR-068/069 (the pipeline the retry lives in and the settlement it extends), ADR-041 (the shared ledger tenancy the hold requires), ADR-087 (the tenant stamp that keys a hold), ADR-032 (lease scope per message), ADR-081/083 (type-only rendering of the errors the ledger persists).
- Context: dependent events on an ordered lane; skip loses causality; a partition stall punishes a tenant's partition mates; thousands of silo databases make a down tenant routine.
- Decision, in order: D (bound, backoff, `Permanent`, panic not retried, default only with `Hold`); C's five steps verbatim from the spec; park as a settlement action; one durable write for gate and park; stall semantics; the lease row and why not an advisory lock; releases learned through the local drain pass; the ownership-transfer invariant and idempotency on (consumer, stream, offset); never auto-dropped; the ledger home and `inbox.tenancy: shared`.
- The race argument (Decision 7's three hazards) written out.
- Alternatives considered: stall the partition (rejected); skip + app-level parking (rejected — every consumer would rebuild it); per-tenant queues on the broker (rejected — the constraints ADR-087 lists); advisory locks (rejected — pooled connections, vendor asymmetry); a hold in the tenant's own database (rejected — a down tenant cannot hold its own messages).
- Consequences: positive (order survives a failure; isolation per tenant; visibility); negative/accepted (a gated message costs a ledger write on the hot path; a crashed drainer's tenant waits one lease; a stale held set costs one extra pass; the hold ledger is control-plane infrastructure whose outage stalls partitions by design; retry defaults apply only with `Hold`).

**Seams (pre-agreed):** greps — `git grep -n 'Parking failed messages is future work' wiki/` empty; `git grep -n 'ADR-089' wiki/architecture_decisions.md wiki/adr_059_streams_consumption.md CLAUDE.md` non-empty; `git grep -n 'through ADR-089' wiki/architecture_decisions.md` one hit; `git grep -n 'gobricks_inbox_hold_tenant' wiki/outbox.md` non-empty; `make lint-md` clean; `wc -c CLAUDE.md` < 40960.

- [ ] **Step 1: Write ADR-089**, the index entry, the counter, the ADR-059 pointer.
- [ ] **Step 2: The four doc edits and the CLAUDE.md line.**
- [ ] **Step 3: Run the greps and `make lint-md`.**
- [ ] **Step 4: `make check`, commit** — `docs(inbox): ADR-089 and the hold pages`.

### Task 12: Gates for PR C3 (controller only)

- [ ] **Step 1: `make check`.** **Step 2: `/simplify`.**
- [ ] **Step 3: `/security-audit`** — the drain's WARN lines carry `error_type` and a bounded message, never a payload; the lease owner string carries no secret; the replay sets the tenant from the ledger row, which was written by the framework, never from the payload; a DELETE never runs without a preceding successful `Replay` (grep the drain for `DeleteRow` call sites).
- [ ] **Step 4: `/code-review`** — CodeRabbit must see the final diff; a doc-vs-code pass on every claim in ADR-089 and `wiki/streams.md`.
- [ ] **Step 5: `make mutate`** after committing. Hand-apply: reorder the drain to `DeleteRow` before `Replay` and confirm `drain_defers_on_first_failure` fails (a failed row must survive); make `Release` run regardless of `NextRows` being empty and confirm the ordering test fails; flip the lease condition and confirm `drain_skips_a_leased_tenant` fails. Task 11 is operator-free — say so in the report.

---

## Self-review against the spec

- Decision 9 (D): Tasks 1–2 — bound, backoff, `Permanent`, framework defaults (with `Hold`), every `HandlerError` retried. Decision 9 (C, gate → park → drain → release → visibility): Task 6 (gate, park, stall, offset commit after the durable write, SAC reload, held set), Task 9 (drain in order through the pipeline with the replay marker, per-tenant backoff, one drainer per tenant, release), Task 10 (gauges), Task 9 step 3 (max-age WARN), "never auto-dropped" (no delete path but `DeleteRow` after a successful replay; no retention). Ledger home + `inbox.tenancy: shared`: Tasks 4, 5, 7. Ownership transfer + idempotency on (consumer, stream, offset): Task 5's `Park`, Task 6's commit-after-park, Task 11's ADR. Classic lane untouched: Global Constraints + Task 1's nil `Retry`. C independent of B: no file in `outbox/` beyond the `ledgererr` call-site move.
- Names used consistently: `delivery.{Retry, Permanent, IsPermanent}`, `Result.Attempts`, `streams.{RetryOptions, DefaultHoldRetry, Permanent, HeldMessage, HoldLedger, HoldReplayer}`, `ConsumerOptions.{Retry, Hold}`, `SuperStreamConsumerOptions.{Retry, Hold}`, `ManagerOptions.Hold`, `Manager.{HoldConsumers, Replay, ReloadHeld}`, `inbox.{HoldRow, HoldTenant, HoldStats, HoldStore, NewPostgresHoldStore, NewOracleHoldStore, HoldDrain}`, `Module.{HoldLedger, SetHoldReplayer}`, `config.InboxHoldConfig`, `ledgererr.{Bound, MaxBytes, TruncationMarker}`, span attributes `messaging.delivery.attempts`, `messaging.delivery.permanent`, `messaging.hold.gated`, `messaging.hold.replay`, metrics `inbox.hold.{tenants,rows,oldest_age}`, job id `inbox-hold-drain`.
- Placeholder scan: none ("TBD", "handle edge cases", "similar to" absent); every code step names the file, the signature and the observable.
