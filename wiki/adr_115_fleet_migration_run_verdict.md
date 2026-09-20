# ADR-115: A Fleet Migration Run Carries a Three-Way Verdict, and Never-Dispatched Tenants Are Counted, Not Rowed

**Status:** Accepted
**Date:** 2026-09-17
**Issue:** #1692

## Context

`migration.MigrateAll` returns a `*MigrateAllResult` whose `Results` hold one row per tenant it
dispatched, and `Failed()` returns the rows whose `Err` is non-nil. A tenant that was listed but
never dispatched has no row. That is a pinned decision: three quiesce tests assert the absence, and
the parallel path trims `Results` to the dispatched count so callers do not see synthetic empty
tenants. Because the result did not record how many tenants were listed, nothing showed that a
tenant was missing.

So three states the operator must tell apart collapsed into two:

- An empty listing produced a zero-row result, a nil error and an empty `Failed()`. A pipeline
  gating deploy on "no failures" proceeded against schemas that were never considered.
- A run stopped before its first dispatch (context done, quiesce set) and a run stopped halfway
  both returned an error and a short `Results`. One touched no schema; the other may have left the
  fleet at mixed versions.
- In parallel mode with a context that was already done, the dispatch `select` had both the done
  channel and a free worker slot ready, so it dispatched a random prefix of tenants. The sequential
  path checked the context first and dispatched none.

## Options Considered

**A — a per-tenant `TenantOutcome` enum with synthetic "not attempted" rows in `Results`.** This is
the issue body's proposal. Rejected: it reverses the pinned one-row-per-dispatched-tenant contract,
and a caller iterating `Results` would meet rows with no vendor, no duration and no Flyway outcome.

**B — a `Prepare` skip sentinel that marks a tenant not attempted before Flyway runs.** Rejected
with #1690: no pre-tenant callback exists, and this decision does not add one.

**C — a run-level verdict, plus the listed count and the never-dispatched tenant IDs on the result.**
Chosen.

**D — the verdict as an `int` or an exit code in the library.** Rejected: exit codes belong to the
CLI. Sentinels compose with `errors.Is` in library callers and tests, and a nil verdict for a clean
run matches Go's success idiom, so `if v := res.Verdict(); v != nil` reads correctly.

## Decision

- `MigrateAllResult` gains `NeverDispatched []string`, the listed IDs the run stopped before
  dispatching, in listing order, and a `Listed()` method, the dispatched rows plus those IDs.
  `Listed()` is derived rather than stored so a hand-built result cannot contradict itself. Both
  dispatch paths walk the listing in order, so the never-dispatched tenants are always the
  listing's tail. `Results` keeps its meaning, and the quiesce pins stand unmodified.
- `(*MigrateAllResult).Verdict() error` classifies the run:
  - **nil (clean):** at least one tenant was dispatched, every listed tenant was dispatched, and
    none failed.
  - **`ErrFleetSplit`:** at least one tenant was dispatched, and at least one listed tenant failed
    or was never dispatched. The fleet may be at mixed versions, and a re-run is needed.
  - **`ErrNothingAttempted`:** no tenant was dispatched, whether because the listing was empty or
    because the context or quiesce stopped the run first. No schema was touched. A nil result is
    `ErrNothingAttempted` too, which covers every `MigrateAll` return that carries no result: a
    nil argument, an invalid migrator identity, or a listing failure.
- Classification is by **dispatch**, never by the failure's cause. A dispatched tenant that ends
  in `ErrFlywayTimeout` or `ErrFlywayCanceled` stays a failure, because its schema state is unknown.
  So does an in-flight sibling that fail-fast canceled, and so does a dispatched tenant whose
  database config could not be resolved. Nothing is retried.
- `MigrateAll`'s `(result, error)` shape and `Failed()` are unchanged. `Failed()`'s godoc says
  it excludes never-dispatched tenants and points at `Verdict`. The verdict is computed from the
  result's fields alone and does not replace the error, so callers check both.
- Both runners share one pre-dispatch check: the context, then the quiesce gate, then the context
  again, because a database-backed quiesce check that a cancel interrupts fails open. The
  sequential runner runs it before each tenant. The parallel runner checks only the context before
  contending for a worker slot, so a context that is already done dispatches zero tenants,
  deterministically, as the sequential path does; once it holds a slot it runs the full check and
  releases the slot when blocked, because waiting for a slot can outlast a fail-fast cancel or a
  quiesce flip. The context now wins
  over quiesce in both runners: a parallel run that is both canceled and quiesced returns the
  context's error, where it returned `ErrQuiesceBlocked`.
- **CLI mapping, decided here and shipped separately.** `go-bricks-migrate` will exit **0** for a
  clean run, **1** for a split fleet, and **2** when zero tenants were dispatched. Exit 2 also
  covers failures before the loop exists: a failed tenant listing, or a credential provider that
  could not be built. The text and `--json` summary will report the listed, attempted, failed and
  not-attempted counts plus the verdict, and the CLI will emit it on the exit-2 paths that reach
  `migrate`/`validate`/`info` too, so a pipeline parsing that stream always gets one summary record
  per run it started. (Amended: a flag cobra rejects outright never reaches an action and emits no
  record, while still exiting 2 — see the amendment below.) The CLI pins a released go-bricks and CI builds it with
  `GOWORK=off`, so this part lands after the release that carries this library change and the pin
  bump that follows it.

## Amended 2026-09-19 (#1692) — the CLI mapping shipped

`go-bricks-migrate` now exits 0, 1 and 2 as decided above. Three details the decision left to the
implementation:

- `total` stays, with the meaning it has always had: the dispatched count, which `attempted` now
  names too. Dropping it was tried and reverted — the acceptance criteria say "unchanged output apart
  from the new summary fields", and a pipeline may already read it. The record carries `total`,
  `listed`, `attempted`, `failed`, `not_attempted` and `verdict` beside `event` and `action`. The
  redundancy between `total` and `attempted` is deliberate and is a follow-up candidate, not
  something this change decides. The never-dispatched IDs stay a library-side field, not a summary
  key.
- Every misuse exits 2, not 1. Exit 1 is reserved for a split fleet so a pipeline can trust it, and a
  command that never ran dispatched nothing, which is what exit 2 means. This covers an unresolvable
  flag combination (marked in `runAction`), an unknown flag, an unparseable flag value and a stray
  positional argument (marked at the root, because cobra rejects those before any action runs), a
  half-set `GOBRICKS_MIGRATE_MIGRATOR_USER`/`_PASSWORD` pair (#1766), and
  everything that fails before `list` or `quiesce` does its work: `list`'s flag resolution and
  tenant-source construction, `quiesce`'s control-plane connection, an unusable `--table` and the
  rest of controller construction. Their own work failing still exits 1, carrying no fleet meaning —
  `list`'s listing call and the printing of its result, `quiesce`'s `set`/`clear`/`status`
  operation. An invocation that reaches `migrate`/`validate`/`info` emits exactly one
  summary record; a flag cobra rejects outright never reaches them and emits none, while still
  exiting 2.

- The verdict names the FLEET; the exit code names the RUN. The record's verdict is derived from
  `Verdict()`, so it never contradicts the counts printed beside it, while the exit code is derived
  from the error the process returns. The two agree everywhere except one reachable state: a
  parallel run whose every tenant was dispatched and succeeded still returns the parent context's
  error (`runParallel`'s tail), so the fleet is consistent and the record says `clean` while the
  process exits 1. Deriving both from the error instead was tried and rejected — it made the record
  claim `fleet_split` beside `failed: 0, not_attempted: 0`, contradicting the count-based definition
  of the verdict this ADR sets. A pipeline that must distinguish the two asks the record about the
  fleet and the exit code about the run.

See [migrations.md](migrations.md) `[C67.1]`.

## Consequences

**Positive:** an empty fleet no longer passes a gate that reads the verdict. "Nothing was touched"
is distinct from "the fleet is split", and the never-dispatched IDs name exactly which tenants a
re-run still has to reach. A parallel run with a done context behaves as the sequential one does.

**Negative:** a fleet that is legitimately empty, such as a new environment with zero tenants, is
now `ErrNothingAttempted`. A pipeline that must pass there has to accept that verdict explicitly.
The split verdict is conservative: under fail-fast, a first tenant whose credentials could not be
read never reached Flyway, yet it counts as failed and the run reads as split rather than as
nothing attempted. The verdict does not inspect why a tenant failed.

**Neutral:** the added field breaks an unkeyed `MigrateAllResult` composite literal. Keyed literals,
including the CLI's test fixtures, still compile.

## References

- #1692 — this decision; #1690 — the rejected `Prepare` skip sentinel
- `migration/multi_tenant.go` — `MigrateAllResult`, `Verdict`, `ErrFleetSplit`,
  `ErrNothingAttempted`, `dispatchBlocked`, `(*parallelState).dispatch`
- [ADR-018](adr_018_multi_tenant_migration_cli.md) — the multi-tenant migration CLI
- [multi_tenant_migration.md](multi_tenant_migration.md#run-verdicts),
  [migration_quiesce.md](migration_quiesce.md), [migrations.md](migrations.md) `[C66.5]`
