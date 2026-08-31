# ADR-089: A Failed Stream Delivery Is Retried, Then Held Per Tenant

- **Status**: Accepted
- **Date**: 2026-08-29
- **Related**: [ADR-059](adr_059_streams_consumption.md) (skip-on-failure, now the
  no-hold behavior), [ADR-068](adr_068_delivery_pipeline.md) /
  [ADR-069](adr_069_pipeline_owns_settlement_timing.md) (the pipeline the retry lives in
  and the settlement it extends), [ADR-041](adr_041_shared_ledger_tenancy.md)
  (the shared ledger tenancy the hold requires),
  [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md) (the tenant stamp that
  keys a hold), [ADR-032](adr_032_lease_refcount_tenant_handles.md) (the lease
  discipline the drain's tenant lease follows), [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) /
  [ADR-083](adr_083_span_sinks_record_errors_by_type.md) (type-only rendering of
  the errors the ledger persists)

## Context

A RabbitMQ stream is an ordered lane, and the events on it are frequently
dependent: an account is created, then funded, then closed. ADR-059 settled a
failed delivery by skipping it — the offset commits, the lane moves on. That is
the right answer for independent events and the wrong one for dependent events:
the funding that follows a failed creation applies to an account that does not
exist, and no later retry can restore the causality the skip destroyed.

Stalling the partition instead is not the answer either. A partition carries many
tenants, so a stall on one tenant's failure punishes every tenant that hashes to
the same partition. And in a silo deployment — thousands of per-tenant databases —
one tenant's database being down is routine, not exceptional. The failure mode
has to be *isolated to the tenant that caused it*, and it has to preserve that
tenant's order.

## Decision

### 1. A failed delivery is retried in place, under a bounded policy

`streams.RetryOptions{MaxAttempts, InitialBackoff, MaxBackoff}` re-invokes the
handler inside the partition's own delivery callback. `MaxAttempts` counts the
first attempt, so `1` retries nothing; the wait before attempt *n* is
`InitialBackoff` doubled *n−2* times, capped at `MaxBackoff`. Because the waits
happen on the partition's goroutine, a declared policy is bounded by
`MaxRetryAttempts` (10) and `MaxRetryWait` (1m) — work needing more patience
belongs in the hold, which parks one tenant and lets the partition move.

`streams.Permanent(err)` is the handler's claim that retrying is pointless: the
delivery ends on the attempt that produced `err`, whatever the policy allows. A
panic is never retried — it is recovered, rendered by type per ADR-081, and
settled as a failure.

Retry defaults apply **only** to a consumer that declares `Hold: true`, which
then gets `DefaultHoldRetry` (3 attempts, 200ms initial, 2s cap). A consumer that
does not hold keeps ADR-059's single attempt unless it names a policy itself.

### 2. When the retries are exhausted, the tenant is held

Five steps, in order:

1. **Gate.** Before delivering, the runner checks whether the message's tenant is
   in the consumer's held set. A held tenant's message is not delivered — it is
   parked, and the runner waits on the gate rather than committing past it.
2. **Park.** The failed message — its tenant, stream, offset, body, and properties
   — is written to the hold ledger, together with the tenant marker row that puts
   the tenant *in* the held set. Park is a settlement action: it is what the lane
   does instead of committing a skip.
3. **Drain.** A scheduled job (`inbox-hold-drain`, every
   `inbox.hold.draininterval`) takes each due tenant under a lease and replays its
   rows back through the consumer's own handler, in ledger order — the same
   (stream, offset) order they were parked in. A row is deleted only *after* its
   replay succeeds. On the first failure the pass stops and the tenant is deferred
   under a per-tenant backoff capped at `inbox.hold.maxbackoff`.
4. **Release.** When a pass drains a tenant's last row, the tenant marker is
   deleted and the tenant leaves the held set — in one statement whose `NOT
   EXISTS` fence makes a concurrent park's row keep the tenant held.
5. **Visibility.** Three gauges — `inbox.hold.tenants`, `inbox.hold.rows`,
   `inbox.hold.oldest_age`, keyed by `messaging.consumer.name` — plus one WARN per
   drain pass naming any tenant held longer than `inbox.hold.maxage`.

### 3. One durable write covers both the gate and the park

The park writes the row and the tenant marker together, and the offset commits
only after that write succeeds. Ordering matters: the marker is taken and locked
*first*, so a concurrent release cannot delete a marker whose row is about to
land and orphan it. If the ledger write fails, nothing commits — the partition
stalls on that message until the write succeeds. That stall is the design: the
alternative is committing past a message the ledger does not have, which is the
loss the hold exists to prevent.

### 4. A tenant is drained by one drainer at a time, through a lease row

`lease_owner` / `lease_until` on the tenant marker, fenced in-statement: every
write the drain makes carries its own lease predicate, so a drainer whose lease
expired mid-pass cannot write. A crashed drainer's tenant simply waits out one
`inbox.hold.leaseduration` (60s) and is picked up by the next pass.

Not an advisory lock: connections are pooled (so a lock's session lifetime is not
the drain's lifetime), and the two supported vendors do not offer the same
primitive.

### 5. A release is learned locally, through the drain pass

Nothing pushes a release to a runner. Each drain pass reloads the held set for
the consumer, so a runner learns that a tenant is free on the next pass at the
latest. The held set carries a **generation** (guarding the set) and a
**promotion epoch** (guarding the gate), and a reload that cannot complete fails
**closed** — the gate stays shut rather than delivering past a hold whose state
is unknown.

### 6. Ownership transfer and idempotency

A parked message stops being the broker's and becomes the ledger's. The hold row
is keyed on `(consumer, stream, offset)`, so parking the same message twice is
idempotent — a redelivery after a crash between the write and the commit lands on
the row that is already there.

A held message is **never auto-dropped**. There is no retention on the hold
ledger and no expiry path: rows leave only through a successful replay, or
through an operator's explicit `DELETE`.

### 7. The ledger lives in the control plane

The hold ledger is `inbox`'s, and it requires `inbox.tenancy: shared` (ADR-041):
a held tenant's own database may be exactly what is down, so the hold cannot live
there. One control-plane ledger holds every tenant's parked messages.

## The race argument

Three hazards, each closed by a specific mechanism rather than by ordering luck:

**A park racing a release.** A release that ran between a park's row write and
its marker write would delete a marker whose row exists — a row held by nobody,
replayed by nobody. Closed by taking and locking the marker *before* writing the
row, and by fencing the release's `DELETE` with a `NOT EXISTS` over the row table
so a marker with rows cannot be released at all. On Oracle, a park that loses the
insert race (ORA-00001) re-locks the marker rather than proceeding.

**A promotion racing a reload.** A tenant released while a runner is mid-reload
could leave the runner acting on a set that predates the promotion. Closed by the
generation and epoch: a reload that observes a newer epoch discards its result,
and a reload that cannot finish closes the gate instead of opening it. The
failure direction is deliberate — a spurious hold costs one extra drain pass, a
spurious delivery costs the ordering guarantee.

**A drainer racing another drainer.** Two schedulers, or one scheduler and one
survivor of a partition, could both believe they own a tenant. Closed by the
lease predicate living inside every drain write rather than in a preceding
`SELECT`: a drainer whose lease has expired writes nothing, whatever it read.

## Alternatives considered

- **Stall the partition on any failure.** Rejected: a partition is shared, so one
  tenant's failure would stop every tenant that hashes to it.
- **Keep the skip and let applications park failures themselves.** Rejected:
  every consumer would rebuild the same ledger, lease, ordering, and drain — and
  most would rebuild it wrong.
- **A queue per tenant on the broker.** Rejected for the constraints ADR-087
  lists: the broker's object count and per-tenant declaration cost do not scale to
  the silo deployments this targets.
- **Advisory locks instead of a lease row.** Rejected: pooled connections do not
  give a lock the drain's lifetime, and PostgreSQL and Oracle do not offer a
  common primitive.
- **A hold in the tenant's own database.** Rejected: the tenant whose database is
  down is precisely the tenant that needs to be held.

## Consequences

### Positive

- Order survives a failure: a tenant's dependent events are replayed in the order
  they arrived, not dropped.
- Isolation is per tenant: a held tenant does not stop its partition mates.
- The backlog is visible — three gauges, a max-age WARN, and two queryable tables.

### Negative / accepted

- A gated or parked message costs a ledger write on the hot path.
- A crashed drainer's tenant waits out one lease before another pass takes it.
- A stale held set costs one extra drain pass, by design (it fails closed).
- The hold ledger is control-plane infrastructure: its outage stalls the
  partitions that need to park, which is the intended failure direction.
- The retry defaults apply only to a consumer that declares `Hold` — a non-holding
  consumer's behavior is unchanged from ADR-059.
