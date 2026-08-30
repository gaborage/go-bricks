# ADR-088: The Outbox Ledger Is Sequenced, Laned, and Drained by One Leader

- **Status**: Accepted
- **Date**: 2026-08-30
- **Related**: [ADR-033](adr_033_outbox_retry_count_status_parking.md) (the connectivity-vs-poison classification a parked key composes with) · [ADR-041](adr_041_shared_ledger_tenancy.md) (the control-plane ledger a leader is taken per) · [ADR-032](adr_032_lease_refcount_tenant_handles.md) (the per-tenant lease scope a relay cycle runs inside)

## Context

The outbox relay drained a ledger by fetching pending rows and publishing each one
independently. Two properties that reads as obvious were never actually held.

**Order.** `FetchPending` ordered by `created_at`, which TIES under concurrent inserts, so two
events written in the same tick could be published in either order. Worse, order was not
preserved across a failure at all: a row that failed was retried on a later cycle while the
rows behind it went out immediately, so an aggregate's second event could reach the broker
before its first. Consumers that tolerate duplicates — which the at-least-once contract
requires of them — do not thereby tolerate inversion.

**One drainer.** Nothing stopped two replicas polling the same ledger. Both fetched the same
rows and both published them. At-least-once made that survivable rather than correct: the
duplicate rate scaled with the replica count, and the two drainers interleaved, so ordering
had no meaning even within a single key.

Both gaps are invisible in a single-replica deployment with an idle broker, which is why they
survived: they appear exactly when a service scales out or a destination starts failing.

A third pressure arrived with the plan for a native streams leg. A row needs to say which
transport it belongs to before the relay can dispatch it two ways, and a stream-lane row needs
a partition key that has nothing to do with an AMQP routing key.

## Decision

**A per-ledger sequence orders the drain, a leader row admits one drainer, and a failed key
parks its own later rows for the cycle.**

1. **Sequence.** Every row carries `seq`, an identity column the database assigns at insert.
   `FetchPending` orders by it. The promise is causal and deliberately narrow: a dependent
   event's transaction begins after its cause committed, so its sequence is higher. Two
   INDEPENDENT transactions may commit out of sequence order and the relay claims nothing
   between them.

2. **Leader row, not an advisory lock.** A cycle opens a transaction, takes the single row of a
   companion `<table>_leader` table `FOR UPDATE NOWAIT`, and holds it until the cycle ends. The
   row lock IS the claim, so releasing it is a rollback — there is nothing to commit. One
   mechanism covers both vendors and needs only DML privileges.

   Rejected: PostgreSQL `pg_try_advisory_xact_lock` plus Oracle `DBMS_LOCK` — two mechanisms to
   keep in step, and the Oracle half needs an `EXECUTE` grant application roles routinely lack.
   Rejected: a lease row with TTL renewal — a write per record, TTL arithmetic, and a
   dependency on clock agreement between replicas.

   `NOWAIT` rather than waiting: a cycle that blocks on the lock is a cycle that runs late, and
   the next tick will try again anyway.

3. **The claim is probed before every record.** A leader can be deposed without noticing — an
   `idle_in_transaction_session_timeout`, a recycled connection, a partition. A trivial
   statement on the leader transaction before each record fails once the transaction is gone,
   and the cycle stops there. This bounds the window in which two instances could both consider
   themselves leader to a single record. A coarser cadence widens that window by the cadence;
   the probe is a sub-millisecond round trip against a per-record broker confirmation, so it is
   not the cost that matters here.

4. **The leader is taken BEFORE the fetch.** This is correctness, not tidiness. Fetching first
   and leading second can return rows the previous leader published in the window between the
   read and the lock, which the new leader would then publish again.

5. **A failed key parks its own later rows, for the cycle only.** The first row of key K that
   fails puts K aside; K's later rows in that batch are neither published nor marked, so their
   `retry_count` does not advance and they keep their place in sequence order. Only a FAILURE
   parks — a dead-lettered row is terminal and an unrecorded one was already delivered, so
   neither blocks what follows.

6. **The ordering key is the scope a row actually competes in**, namespaced by lane so two
   scopes never collide by sharing a string: the tenant stamp for a stamped AMQP row
   (deliberately spanning that tenant's exchanges, which is the ordering one tenant's event
   stream needs), the destination — exchange AND routing key — otherwise, and the stream plus
   partition key on the stream lane. Keying by routing key alone would park rows bound for
   unrelated exchanges behind each other.

7. **Lane, stream and partition key are dedicated columns**, not overloaded onto
   `exchange`/`routing_key`. A reader of the table should not need to know the lane to know what
   a column means.

8. **A lost leader row is reported as itself.** It travels on its own error rather than through
   the broker-outage path, because its cause is the database: reporting it as "messaging not
   available" sends an operator to a broker that is fine.

## Consequences

**A migrated ledger needs an explicit backfill.** Adding an identity column populates existing
rows in the order the rewrite READS them — heap order on PostgreSQL, rowid order on Oracle —
which is not `created_at` order. Because the outbox updates pending rows, a non-HOT update
relocates them, so the divergence lands precisely on the retried rows a backlog is made of.
The documented migration therefore backfills `seq` explicitly with a
`row_number() OVER (ORDER BY created_at, id)` and then advances the identity past what it
wrote, before the index is built. Skipping it drains the backlog once in an arbitrary order,
and nothing reports that it happened.

**A parked key still occupies its batch slots.** `FetchPending` returns the oldest `BatchSize`
rows by sequence, so a key whose head keeps failing keeps its later rows in every batch until
that head succeeds or dead-letters. A large enough backlog for one key starves the others.
This is a known limitation, not a hidden one; a per-key fetch cap is a follow-up.

**Every tick pays for the leader row, including idle ones.** A cycle takes the lock before
knowing whether there is work, so an idle deployment spends one row-lock round trip per tick
per ledger. Bounded by `outbox.pollinterval`, and the alternative reintroduces the staleness
in decision 4.

**A DB connection is held for the length of a cycle**, and cycle length is driven by broker
latency. A deployment with a tight `idle_in_transaction_session_timeout` can lose leadership
mid-cycle. That is detected by the probe and ends the cycle cleanly — no partial write — but
it is real churn, and such deployments should size that timeout against
`outbox.batchsize` × `outbox.publishtimeout`.

**Exactly one replica per ledger now drains.** An alert or dashboard that expected every
replica to report a relay cycle will fire falsely; the others log `another instance leads this
ledger` at DEBUG. Similarly, a dashboard reading `retry_count` as liveness will see a parked
key as a stall, because a parked row's count deliberately does not move.

**The table name is bounded at 49 bytes for its last segment.** Every identifier the store
derives must stay distinct under PostgreSQL's 63-byte truncation, and the longest derivation
is `idx_<name>_published` at +14. Past the bound the failures are silent: truncation emits a
NOTICE rather than an error, so an index quietly exists under a name nobody wrote, and a
63-byte name collapses onto its own `_leader` companion.

**`config.OutboxConfig` stops being comparable** — `SuperStreams []string` makes it so — and
`outbox.Store` gains `Lead`, which breaks an implementation outside the framework. Both are
compile-time, both are documented in `[C61.23]`.
