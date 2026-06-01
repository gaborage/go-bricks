# ADR-021: Provisioning State Machine — Diverge on the Model, Mirror the Patterns

**Date:** 2026-05-13
**Status:** Accepted
**Issue:** #379

## Context

Issue #379 specifies a per-tenant provisioning state machine with durable,
crash-recoverable persistence. Each transition (`pending → schema_created →
role_created → migrated → seeded → ready`, with `cleanup → failed`
branches) must be persisted before the next step runs so a worker crash
mid-flow can be resumed.

The issue explicitly calls out a design question: the requirements
**"overlap strongly with `outbox/`"** (durable persisted state,
crash-recoverable, retry semantics) and require a written decision in the
implementing PR — reuse outbox patterns, share storage, or justify why
not. The decision drives whether the state machine extends `outbox/` or
lives in a new `migration/provisioning/` package.

This ADR records that decision and its rationale so future contributors
understand why the two systems sit in separate packages.

## Decision

**Diverge on the data model; mirror the patterns.**

The new `migration/provisioning/` package borrows the *engineering patterns*
that make `outbox/` battle-tested — but does not share storage tables,
the `Store` interface, or the relay/dispatcher mechanism.

### Patterns mirrored from `outbox/`

| Pattern | Outbox manifestation | Provisioning manifestation |
|---|---|---|
| Status enum on a row | `Record.Status` (`pending`/`published`/`failed`) | `Job.State` (8-value graph) |
| Retry counter | `Record.RetryCount` + `maxRetries` ceiling | `Job.Attempts` (bookkeeping; no ceiling — failures terminate) |
| Vendor-pluggable Store | `outbox.Store` interface; PG + Oracle impls | `provisioning.StateStore` interface; PG impl in v1 |
| Bundled DDL + idempotent `CreateTable` | `postgresCreateTableSQL` const + `Store.CreateTable` | `PostgresStateTableDDL` const + `StateStore.CreateTable` |
| In-memory mock under `<pkg>/testing/` | `outbox/testing/MockOutbox` + `AssertEventPublished` | `provisioning/testing/MockStateStore` + `AssertJobReachedState` |
| Configurable table name | `tableName` constructor arg + `validateTableName` | `tableName` constructor arg + `safePGTableIdent` regex |

### Concerns that diverge

1. **Queue vs finite-state-graph semantics.** Outbox is an append-only
   event queue with idempotency-token deduplication. The state machine has
   eight explicit states, a directed transition graph, and rejects edges
   not in that graph (`ErrIllegalTransition`). The outbox `Store`'s
   `FetchPending` / `MarkPublished` pair has no analog in the state machine
   because the state machine does not poll for work — the dispatcher
   feeding it is out of scope for #379.

2. **Per-deployment vs per-tenant scope.** The outbox relay runs once per
   deployment, draining events across all tenants. The provisioning
   executor operates on a single tenant per `Run(jobID)` call. Two
   executors against the same `jobID` is a bug; two relays against the
   same outbox table is by-design.

3. **Fire-and-forget vs blocking transitions.** Outbox publishes and
   forgets; consumer deduplicates. The state machine's `Run` is blocking
   from the caller's perspective and only returns when the job reaches a
   terminal state (or `ctx` is cancelled). Each transition is durably
   persisted before the next step runs, by contract.

4. **Implicit ordering vs explicit graph.** Outbox preserves insertion
   order (`ORDER BY created_at`) but doesn't enforce dependencies between
   events. The state machine has no temporal ordering — only the graph
   matters; transitions skipping intermediate states return errors.

5. **No in-flight state.** Outbox has no "publishing" state — a crash
   during publish leaves the row at `"pending"` and the broker sees a
   duplicate (which the consumer deduplicates). The state machine's design
   makes the same call: there is no `"step_in_flight"` state. A crash
   during a forward step leaves the persisted state at the *previous*
   transition, and the next `Run` re-invokes the same step. Steps must be
   idempotent — schema creation via `CREATE SCHEMA IF NOT EXISTS`, role
   creation via the idempotent template from #378, Flyway via its own
   `flyway_schema_history` tracking, and Seed by consumer contract.

### Why a separate package and not an outbox extension

Three concrete reasons:

1. **Different consumer contract.** Outbox consumers call `Publish(ctx,
   tx, event)` from inside a business transaction. Provisioning consumers
   register `Steps` callbacks at executor construction time and call
   `Run(ctx, jobID)`. The two APIs share no surface; co-locating them
   would force callers to learn one to use the other.

2. **Different table shape.** Outbox rows carry payload + headers +
   exchange + routing_key; provisioning rows carry tenant_id + attempts +
   last_error + metadata. Sharing the table would mean either bloating
   outbox with provisioning columns or introducing a discriminator that
   makes both packages' SQL more complex than the savings warrant.

3. **Different deployment lifecycle.** Outbox tables are part of every
   service's schema and are populated continuously by business
   transactions. Provisioning tables live in a control-plane database
   (or a dedicated provisioning schema) and are populated by the
   tenant-management workflow. Different ownership, different access
   patterns, different operational dashboards.

## Alternatives considered

### Reuse the outbox storage table with a discriminator column

Adding a `record_kind` column to `outbox.records` and storing provisioning
job state there. **Rejected** because the column lists are non-overlapping
and the consumer APIs diverge — every outbox query would grow a `WHERE
record_kind = ?` predicate, every provisioning query would too, and both
test suites would need to filter their fixtures.

### Reuse only the `outbox.Store` interface, with a new table

Implementing `outbox.Store` for a new `provisioning_jobs` table.
**Rejected** because the outbox `Store` interface's methods (`Insert`,
`FetchPending`, `MarkPublished`, `MarkFailed`, `DeletePublished`) don't
map to state-machine operations. Provisioning needs `Get`, `Upsert`,
`Transition` — all absent from the outbox interface. Forcing the fit
would mean either implementing no-op versions of the outbox methods (a
code smell) or extending the outbox interface with state-machine methods
(a worse smell, since outbox consumers would gain methods irrelevant to
them).

### Extend the outbox package with a sibling `outbox.StateMachine` type

A larger refactor: rename the package to `durable/` or `coordination/`
with `outbox/queue/` and `outbox/statemachine/` as siblings. **Rejected**
because the value of co-location is informational ("these share an
underlying pattern") and the cost is a backwards-incompatible package
move that breaks every existing `outbox` import. Citing the pattern in
this ADR achieves the informational goal at zero cost.

## Consequences

**Positive:**

- Outbox consumers see no change. Existing imports of
  `github.com/gaborage/go-bricks/outbox` continue to work unchanged.
- Provisioning consumers get a focused API surface. The package's exports
  are limited to state-machine concerns: `State`, `Job`, `Steps`,
  `Executor`, `StateStore`, and the PG reference impl.
- Each package's test patterns stay simple. `outbox/testing/` keeps
  testing queue semantics; `provisioning/testing/` tests state graph
  semantics. Neither has to mock the other.
- The mirrored patterns make adding new vendors cheap. When Oracle role
  separation lands (#385), an `OracleStateStore` follows the existing
  outbox-Oracle precedent in shape.

**Negative:**

- Two DDL constants, two tables, two `CreateTable` flows. Operators
  managing schema externally must apply both. Mitigated by exporting both
  as `[]string` (outbox) / `string` constant (provisioning) so external
  migration tooling can consume them.
- Two retry-counter bookkeeping surfaces. Outbox's `MaxRetries` skip-and-
  retry pattern is orthogonal to the state machine's "errors are
  terminal, retry via new jobID" pattern. Documented in each package
  README so consumers don't mistake one for the other.

## Future work

Issue #379 explicitly defers the **dispatcher** that feeds jobs into the
executor (control-plane DB polling vs SQS vs cron) to a follow-up. When
that lands, it may reuse parts of outbox's relay-scheduling pattern
(scheduler integration, slow-job warning thresholds). The dispatcher's
package home is an open question at the time of writing.

The **state transition audit events** (`state.transitioned`,
`cleanup.completed`) referenced in #382 will hook into the existing
ADR-019 audit-emitter seam once the state machine has a stable consumer.
They are out of scope for #379 to keep this PR focused on the
state-machine + storage contract.
