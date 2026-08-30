# Outbox Ordered Super-Stream Relay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Implementers drive each task with `/mattpocock-skills:tdd` — the seams are pre-agreed in every task's **Seams** block; do not ask for them.

**Goal:** Make the outbox relay an ordered-per-key drain that runs on one instance per ledger and can publish a row to a super stream through the native streams lane, partitioned by the row's *tenant stamp* (deliverable B of the multi-tenant messaging end state, issue #1232).

**Architecture:** Every row carries a *lane* (`amqp` or `stream`) and a monotonic per-ledger *sequence* assigned by the database at insert; `FetchPending` orders by that sequence. A stream-lane row also carries its super stream name and its *partition key* — the tenant stamp taken from the context at `Publish`. The relay takes a per-ledger leader row `FOR UPDATE NOWAIT` in a transaction it holds for the cycle (probing it before every record), drains rows in sequence order, parks every later row of a key whose row just failed, and dispatches each row by lane: the AMQP lane is today's `PublishToExchange`; the stream lane is a `streams.Publisher` the outbox module declared for each configured target. Publish outcomes keep ADR-033's connectivity-vs-poison classification.

**Tech Stack:** Go 1.26 · `github.com/rabbitmq/amqp091-go` · `github.com/rabbitmq/rabbitmq-stream-go-client` v1.8.3 (murmur3 super-stream routing, ADR-063) · pgx (`pgconn.PgError`) and go-ora (`oranet.OracleError`) for vendor error codes · koanf config · testify · testcontainers (`testing/containers`).

**Spec:** [docs/superpowers/specs/2026-08-29-multitenant-messaging-end-state.md](../specs/2026-08-29-multitenant-messaging-end-state.md) — decision 10 (source ordering) and the carve row for B; the "Agent Brief" comment on #1232 (`GH_TOKEN=$(gh auth token -u gaborage) gh issue view 1232`) is the same design as an issue. Where they differ, the spec wins. This plan CONSUMES plan A ([2026-08-29-messaging-shared-tenancy.md](2026-08-29-messaging-shared-tenancy.md)) — the tenant stamp and the relay's rehydrate-and-strip step (plan A Task 12) exist on this plan's base.

**Vocabulary:** [CONTEXT.md](../../CONTEXT.md) `### Tenancy` — *Tenant stamp*, *Partition key*, *Control-plane key*, *Tenancy*. Use those words in comments, docs and commit messages; avoid "tenant header", "routing key" (for the stream lane's key), "shard key", "root key".

**Stack position:** three dependent PRs, one `/gh-stack`, merged bottom-up by the maintainer. **B is blocked by A (#1230):** the stack's base is A's top branch `feat/messaging-tenancy-outbox-stamp-docs`, not `main`, until A merges — after which `gh stack sync --prune` rebases it onto `main`. The worktree is `.claude/worktrees/B`.

| PR | Branch | Base | Carries |
| --- | --- | --- | --- |
| B1 | `feat/outbox-ledger-sequence-and-lane` | `feat/messaging-tenancy-outbox-stamp-docs` (A4) | Tasks 1–2 + Task 3 (gates) |
| B2 | `feat/outbox-key-ordered-leader-relay` | `feat/outbox-ledger-sequence-and-lane` | Tasks 4–5 + Task 6 (gates) |
| B3 | `feat/outbox-super-stream-leg-docs` | `feat/outbox-key-ordered-leader-relay` | Tasks 7–9 + Task 10 (gates) |

Titles: B1 `feat(outbox)!: order the ledger by a sequence and mark each row's lane` (the `!` is apidiff's, see decision 2), B2 `feat(outbox): drain each ledger key-ordered under one relay leader`, B3 `feat(outbox): publish stream-lane rows through the native streams lane`. One ADR (ADR-088, Task 9) and one migrations atom (Task 9) for the whole stack, carried by B3; B1's PR body points at them.

## Global Constraints

- Test function names are **camelCase** (`TestRelayParksLaterRowsOfAFailedKey`); table-driven case names are **snake_case** (`{name: "stream_target_without_tenant"}`). The `check-test-conventions.sh` hook flags violations.
- Commit with `git commit -F <file>`; the commit hook rejects heredoc `-m`. Commits MUST be signed — if signing fails, STOP and report; never pass `--no-gpg-sign`, never set `commit.gpgsign=false`.
- Every `gh` call is prefixed `GH_TOKEN=$(gh auth token -u gaborage)`. Never switch the gh account globally.
- Implementers run `make check` before every commit, detached (`nohup sh -c 'make check' > /tmp/gb-lanes/check-B.log 2>&1 & disown`, then poll the log for `EXIT=`), under a chassis slot when the orchestrator says lanes are sharing the machine; `git branch --show-current` must print the branch of the PR the task belongs to. The controller runs the pre-push gates and every push.
- No new `//nolint`. Comments are bare-minimum: rationale a reader cannot derive, or a `// SECURITY:` annotation. `outbox/store_postgres.go` and `store_oracle.go` keep their file-level `//nolint:dupl` (already present).
- Raw SQL in the stores is the package's existing pattern (`fmt.Sprintf` with a `sqlid`-validated table name and vendor placeholders); no `f.Raw`/`database.Raw` door is added, so no `// SECURITY: Manual SQL review` annotation is owed. New identifiers derived from the table name go through `sqlid` (Task 1 adds the helper).
- `messaging/streams` must NOT import `github.com/gaborage/go-bricks/messaging`; `outbox` may import both (`streams` never imports `outbox`).
- apidiff (CI job "API compatibility") fails an incompatible exported change. `config.OutboxConfig` is comparable today; `SuperStreams []string` makes it non-comparable, which apidiff reports as `old is comparable, new is not`. The documented remedy is the `!` title marker plus a compile-break atom (decision 2), not a redesign. `app.OutboxEvent` already carries `Payload any` and `Headers map[string]any`, so `Stream string` is additive. `outbox.Record` gains fields only; `outbox.Store` gains one method — an interface addition, which apidiff also reports as incompatible for outside implementations: the same `!` covers it (the brief's "both `Store` implementations" are the framework's own).
- Sequence order is the relay's ONLY ordering promise, and it is causal: a dependent event's transaction begins after its cause committed, so its sequence is higher and it becomes visible later. Independent transactions may commit out of sequence order; the relay makes no claim between them.
- Panic values are rendered by type only (ADR-081) — nothing in this plan recovers a panic; do not add a recover.
- The default deployment (no `outbox.superstreams`, every row on the `amqp` lane) changes in exactly two observable ways: rows drain in sequence order and one instance per ledger drains at a time. Every existing relay test passes unchanged apart from the leader step (Task 5 extends `fakeStore` so the step is a no-op for them).

## Decisions the plan makes

1. **Stream targets are declared through config, not bound lazily.** `outbox.superstreams: [name, …]` lists the super streams the relay may target. The outbox module implements `app.StreamsDeclarer` and calls `DeclareSuperStreamPublisher` once per entry, so binding at `Manager.Start`, startup validation (a target no module declared as a super stream fails startup with the declarations' own "undeclared super stream" error) and closing with the app all ride the existing machinery. The brief's "lazily" was a default the ADR may revisit, and it was revisited: a post-start bind needs a new `Manager` API, an app-to-module factory, and a manager that starts with nothing declared — three new seams to replace one config key. Consequence: an outbox-targeted super stream has the outbox as its ONE publisher in the process (`registerPublisher` panics on a second); a module that also publishes directly to it must publish through the outbox instead.
2. **`outbox.superstreams` is a `[]string`, and B1 carries the `!`.** A comma-separated `string` would dodge apidiff but break the repo's `[]string` config precedent (`multitenant.resolver.order`). The compile-break is real for anyone comparing `config.OutboxConfig` values or implementing `outbox.Store` outside the framework, so it is titled and atomised as one (`[C61.22]`, decision 9) — the spec's "no `!`" predates this consequence.
3. **Leader = a leader row taken `FOR UPDATE NOWAIT` in a cycle-long transaction, probed per record.** One mechanism, both vendors, DML privileges only. Rejected: PostgreSQL `pg_try_advisory_xact_lock` + Oracle `DBMS_LOCK` (two mechanisms, and `DBMS_LOCK` needs an `EXECUTE` grant application roles routinely lack); a lease row with TTL renewal (a write per record, TTL arithmetic, and a clock). Loss of leadership mid-cycle — an `idle_in_transaction_session_timeout`, a recycled connection, a partition — is detected by a `SELECT 1` on the lock transaction before every record; the cycle aborts on the first failed probe, so a deposed leader never publishes another row. The companion table is `<table>_leader` with one row; `CreateTable` creates and seeds it, managed deployments run the documented statements.
4. **Row shape: dedicated columns.** `lane` (`amqp`/`stream`), `stream`, `partition_key`, `seq`. Overloading `exchange`/`routing_key` for the stream lane was rejected: a reader of the table should not need the lane to know what a column means. `seq` is a `GENERATED BY DEFAULT AS IDENTITY` column on both vendors, assigned at insert; `Insert` never writes it. The pending index moves to `(seq)`.
5. **Partition key = the tenant stamp, taken at `Publish`.** A stream-targeted event with no tenant in context is refused (`ErrStreamTargetRequiresTenant`); one naming an exchange or a routing key beside a stream is refused (`ErrConflictingTargets`); one naming a stream absent from `outbox.superstreams` is refused (`ErrStreamNotAnOutboxTarget`) — at the producer, where the developer sees it, not as poison cycles later. The relay still classifies an unknown lane or an unlisted stream on a persisted row as poison (config drift between deploys, hand-edited rows).
6. **Key-ordering key.** Stream lane: `partition_key`. AMQP lane: the tenant stamp when the row carries one (`x-tenant-id` in its persisted headers), else the routing key. A row parks its key's later rows in the cycle only on `outcomeFailed`; a dead-lettered row is terminal and an unrecorded row was delivered, so neither parks.
7. **Stream-leg outcome classification.** Aborted: `context.Canceled`, `streams.ErrPublisherClosed` (shutdown). Poison (dead-letters at `MaxRetries`): unknown lane, a stream not in `outbox.superstreams`, an empty partition key. Connectivity (retry, never parked): everything else — `streams.ErrPublisherNotStarted`, `context.DeadlineExceeded` from the per-record `PublishTimeout`, a broker confirmation failure. The tenant stamp is rehydrated into the publish context and stripped from the properties by plan A's Task 12 step, which runs before the lane dispatch, so the streams publisher stamps it itself and never sees a caller-supplied one.
8. **A parked key still occupies its batch slots.** `FetchPending` returns the oldest `BatchSize` pending rows by sequence; a key whose head keeps failing keeps its later rows in every batch until the head dead-letters or succeeds, and a large enough backlog for one key starves the others. Named in ADR-088's consequences as the known limitation; a per-key fetch cap is a follow-up, not this plan.
9. **Atom and hop.** One silent-behavior + compile-break atom, written as `[C61.22]` on the E61 hop (`v0.60.0 → v0.61.0`), since `v0.61.0` is untagged at planning time (`git tag` tops out at `v0.60.0`). If `v0.61.0` ships before B3 lands, open the E62 hop with the same conventions and renumber; sibling lanes already hold C61.19–C61.21 and the merge pass renumbers by merge order. ADR-088 likewise renumbers if another ADR lands first.
10. **Cross-vendor identity of the NOWAIT failure.** PostgreSQL `55P03` (`lock_not_available`), Oracle `ORA-00054`. A new `database.IsLockNotAvailable(err) bool` beside `IsUniqueViolation` owns both codes; the store maps it to `outbox.ErrNotLeader`.

---

## PR B1 — the ledger learns its lane and its order

### Task 1: Row shape, DDL, sequence order, leader table, `outbox.superstreams`

**Files:**

- Modify: `outbox/store.go` (`Record` `24-37`, `Store` doc of `FetchPending` `53-57`)
- Modify: `outbox/store_postgres.go` (DDL `14-38`, `Insert` `54-77`, `FetchPending` `79-112`, `CreateTable` `175-191`)
- Modify: `outbox/store_oracle.go` (DDL `19-48`, `Insert` `64-87`, `FetchPending` `93-133`, `CreateTable` `201-218`)
- Modify: `internal/sqlid/sqlid.go` (beside `IndexBaseName` `53`)
- Modify: `config/types.go` (`OutboxConfig` `718-771` — new field + the defaults comment `710-717`)
- Modify: `outbox/config.go` (`validateConfig` `15-36`)
- Test: `outbox/store_postgres_test.go`, `outbox/store_oracle_test.go`, `internal/sqlid/sqlid_test.go`, `outbox/config_test.go`

**Interfaces:**

- Produces, in `outbox`:
  - `const (LaneAMQP = "amqp"; LaneStream = "stream")`
  - `Record` gains `Lane string`, `Stream string`, `PartitionKey string`, `Seq int64` (the DB-assigned sequence; zero before insert, never written by `Insert`).
  - Both stores' `Insert` write `lane, stream, partition_key` (Oracle maps `""` to NULL on scan for `stream`/`partition_key` exactly as it does for `exchange`); both `FetchPending` select `…, lane, stream, partition_key, seq` and `ORDER BY seq ASC`.
  - `CreateTable` also creates `<table>_leader (id SMALLINT PRIMARY KEY)` (Oracle `NUMBER(3)`) and seeds its single row `1` (PostgreSQL `INSERT … ON CONFLICT DO NOTHING`; Oracle `MERGE INTO … USING dual`), and the pending index is `ON (seq) WHERE status = 'pending'` (Oracle: `CASE WHEN status = 'pending' THEN seq END`).
- Produces, in `sqlid`: `func LeaderTableName(name string) string` — `name + "_leader"`, schema prefix preserved (`myschema.outbox` → `myschema.outbox_leader`); the input has already passed `ValidateTableName`.
- Produces, in `config`: `OutboxConfig.SuperStreams []string` with tag `koanf:"superstreams" json:"superstreams" yaml:"superstreams" toml:"superstreams" mapstructure:"superstreams"`, doc: "Super streams the relay may publish to over the native streams lane. Each name must be declared as a super stream by a module's DeclareStreams; the outbox declares its publisher. Requires messaging.streams.uri. Default: none (every event stays on the AMQP lane)."
- Produces, in `outbox/config.go`: `validateConfig` rejects an empty entry (`outbox: superstreams[%d] must not be empty`) and a duplicate (`outbox: superstreams lists %q twice`).

DDL, verbatim (PostgreSQL; the `%s` is the validated table name):

```sql
CREATE TABLE IF NOT EXISTS %s (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    seq           BIGINT GENERATED BY DEFAULT AS IDENTITY,
    lane          VARCHAR(16) NOT NULL DEFAULT 'amqp',
    event_type    VARCHAR(255) NOT NULL,
    aggregate_id  VARCHAR(255) NOT NULL,
    payload       BYTEA NOT NULL,
    headers       BYTEA,
    exchange      VARCHAR(255) NOT NULL DEFAULT '',
    routing_key   VARCHAR(255) NOT NULL DEFAULT '',
    stream        VARCHAR(255),
    partition_key VARCHAR(255),
    status        VARCHAR(20) NOT NULL DEFAULT 'pending',
    retry_count   INTEGER NOT NULL DEFAULT 0,
    error         TEXT,
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    published_at  TIMESTAMP WITH TIME ZONE
)
```

```sql
CREATE INDEX IF NOT EXISTS idx_%s_pending ON %s (seq) WHERE status = 'pending'
```

```sql
CREATE TABLE IF NOT EXISTS %s (id SMALLINT PRIMARY KEY)
```

```sql
INSERT INTO %s (id) VALUES (1) ON CONFLICT (id) DO NOTHING
```

Oracle:

```sql
CREATE TABLE %s (
    id            VARCHAR2(36) PRIMARY KEY,
    seq           NUMBER(19) GENERATED BY DEFAULT AS IDENTITY,
    lane          VARCHAR2(16) DEFAULT 'amqp' NOT NULL,
    event_type    VARCHAR2(255) NOT NULL,
    aggregate_id  VARCHAR2(255) NOT NULL,
    payload       BLOB NOT NULL,
    headers       BLOB,
    exchange      VARCHAR2(255),
    routing_key   VARCHAR2(255),
    stream        VARCHAR2(255),
    partition_key VARCHAR2(255),
    status        VARCHAR2(20) DEFAULT 'pending' NOT NULL,
    retry_count   NUMBER(10) DEFAULT 0 NOT NULL,
    error_msg     CLOB,
    created_at    TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
    published_at  TIMESTAMP WITH TIME ZONE
)
```

```sql
CREATE INDEX idx_%s_pending ON %s (CASE WHEN status = 'pending' THEN seq END)
```

```sql
CREATE TABLE %s (id NUMBER(3) PRIMARY KEY)
```

```sql
MERGE INTO %s t USING (SELECT 1 AS id FROM dual) s ON (t.id = s.id) WHEN NOT MATCHED THEN INSERT (id) VALUES (s.id)
```

The managed-migration statements (documented in Task 9, both vendors):

```sql
-- PostgreSQL
ALTER TABLE gobricks_outbox
    ADD COLUMN seq BIGINT GENERATED BY DEFAULT AS IDENTITY,
    ADD COLUMN lane VARCHAR(16) NOT NULL DEFAULT 'amqp',
    ADD COLUMN stream VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN partition_key VARCHAR(255) NOT NULL DEFAULT '';
CREATE TABLE gobricks_outbox_leader (id SMALLINT PRIMARY KEY);
INSERT INTO gobricks_outbox_leader (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
-- Oracle
-- Oracle keeps stream/partition_key NULLABLE: '' IS NULL there, so NOT NULL DEFAULT ''
-- would reject every AMQP-lane insert with ORA-01400 (issue #586). FetchPending maps
-- NULL back to "" on scan, exactly as it already does for exchange/routing_key.
ALTER TABLE gobricks_outbox ADD (
    seq NUMBER(19) GENERATED BY DEFAULT AS IDENTITY,
    lane VARCHAR2(16) DEFAULT 'amqp' NOT NULL,
    stream VARCHAR2(255),
    partition_key VARCHAR2(255));
CREATE TABLE gobricks_outbox_leader (id NUMBER(3) PRIMARY KEY);
MERGE INTO gobricks_outbox_leader t USING (SELECT 1 AS id FROM dual) s ON (t.id = s.id) WHEN NOT MATCHED THEN INSERT (id) VALUES (s.id);
```

Adding an identity column DOES populate existing rows, but in the order the rewrite reads
them — heap order on PostgreSQL, rowid order on Oracle — which is not `created_at` order.
The outbox updates pending rows (`MarkFailed` bumps `retry_count`), and a non-HOT update
relocates a row, so the divergence lands precisely on the retried rows that make up a
backlog. The backfill is therefore explicit, and the identity is then advanced past the
values it just wrote:

```sql
-- PostgreSQL
WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS rn FROM gobricks_outbox
)
UPDATE gobricks_outbox o SET seq = ordered.rn FROM ordered WHERE o.id = ordered.id;
-- The three-argument form, because an EMPTY ledger has no max: setval(seq, 0) violates
-- the identity's MINVALUE 1 and errors. is_called is false when the table is empty, so
-- the first nextval returns 1; true when rows exist, so it returns max+1.
SELECT setval(pg_get_serial_sequence('gobricks_outbox', 'seq'),
              (SELECT coalesce(max(seq), 1) FROM gobricks_outbox),
              (SELECT max(seq) IS NOT NULL FROM gobricks_outbox));
-- Oracle
MERGE INTO gobricks_outbox t USING (
    SELECT id, ROW_NUMBER() OVER (ORDER BY created_at, id) AS rn FROM gobricks_outbox
) s ON (t.id = s.id) WHEN MATCHED THEN UPDATE SET t.seq = s.rn;
-- START WITH LIMIT VALUE restarts at max(seq)+1; on an EMPTY ledger there is no max, so
-- run it only when rows exist and leave a fresh identity at its own start value otherwise.
ALTER TABLE gobricks_outbox MODIFY (seq GENERATED BY DEFAULT AS IDENTITY (START WITH LIMIT VALUE));
```

Only now the pending index, so it is built once over final `seq` values instead of being
maintained through every row the backfill UPDATE touches:

```sql
-- PostgreSQL
DROP INDEX IF EXISTS idx_gobricks_outbox_pending;
CREATE INDEX idx_gobricks_outbox_pending ON gobricks_outbox (seq) WHERE status = 'pending';
-- Oracle
DROP INDEX idx_gobricks_outbox_pending;
CREATE INDEX idx_gobricks_outbox_pending ON gobricks_outbox (CASE WHEN status = 'pending' THEN seq END);
```

`id` is the tie-breaker, so rows sharing a `created_at` tick get a stable order rather than
an arbitrary one. Order of the whole migration: `ALTER` (adding the columns, PostgreSQL's
stream columns `NOT NULL DEFAULT ''` so existing rows are readable immediately), then the
backfill, then the index — so the index is built over final values and no window exists in
which `FetchPending` reads a row it cannot scan.

The table name is bounded at 49 bytes for its own segment (`outbox.tablename`), so every
identifier derived from it stays distinct under PostgreSQL's 63-byte truncation. The longest
derivation is `idx_<name>_published` (+14), not the `<name>_leader` companion (+7). Past the
bound the failures are silent rather than loud: from 50 bytes PostgreSQL truncates
`idx_<name>_published` (and from 52 the `_pending` one too) with a NOTICE rather than an
error, so the index exists under a name nobody wrote; from 57 bytes the two truncate onto
the SAME identifier, since only the shared `idx_<name>_p…` prefix survives the 63-byte cut;
and a 63-byte name collapses onto its own `_leader` companion, so `CreateTable` skips the
leader table and aims the seed at the ledger.

`CreateTable` does not perform any of this: it is `IF NOT EXISTS`, so against an existing
table it no-ops the table and then fails creating the `(seq)` index — surfaced by the caller
as a warning. Auto-creation stays a fresh-database convenience; an existing deployment runs
the statements above.

**Seams (pre-agreed):** both stores through `dbtesting.NewTestDB(vendor)` expectations (`ExpectTransaction().ExpectExec`, `ExpectQuery(...).WillReturnRows`, `ExpectExec` for each DDL statement — the existing `store_*_test.go` pattern); `sqlid.LeaderTableName` directly; `validateConfig` directly (the existing `outbox/config_test.go` pattern).

- [ ] **Step 1: Red — stores**

| case name | vendor | call | expect |
| --- | --- | --- | --- |
| `insert_writes_lane_and_stream_columns` | pg, ora | `Insert` with `Lane: "stream", Stream: "orders", PartitionKey: "acme"` | the exec SQL contains `lane, stream, partition_key` and NOT `seq`; args include `"stream"`, `"orders"`, `"acme"` in that order after `routing_key` |
| `insert_amqp_row_defaults_lane` | pg, ora | `Insert` with `Lane` empty | arg for `lane` is `"amqp"` (the store fills it, so a hand-built `Record` never inserts an empty lane) |
| `fetch_pending_orders_by_seq` | pg, ora | `FetchPending` | the query SQL contains `ORDER BY seq ASC` and `seq` in the select list; two rows with the same `created_at` come back in the row-set order with `Seq` 7 and 8 scanned |
| `fetch_pending_maps_null_stream_to_empty` | ora | row with NULL `stream`, `partition_key` | `Stream == ""`, `PartitionKey == ""` |
| `create_table_creates_leader_and_seq_index` | pg, ora | `CreateTable` | exec log has, in order: the table DDL containing `seq` and `lane`, the pending index DDL containing `(seq)` (Oracle: `THEN seq END`), the published index, the leader table DDL naming `gobricks_outbox_leader`, the seed statement |
| `create_table_seed_error_is_reported` | pg | seed exec returns an error | `CreateTable` returns an error containing `leader` |

- [ ] **Step 2: Red — `sqlid.LeaderTableName`**

| input | output |
| --- | --- |
| `gobricks_outbox` | `gobricks_outbox_leader` |
| `myschema.outbox` | `myschema.outbox_leader` |

- [ ] **Step 3: Red — `validateConfig`**

| case name | `SuperStreams` | expect |
| --- | --- | --- |
| `superstreams_empty_entry` | `[]string{"orders", ""}` | error contains `superstreams[1] must not be empty` |
| `superstreams_duplicate` | `[]string{"orders", "orders"}` | error contains `lists "orders" twice` |
| `superstreams_ok` | `[]string{"orders", "payments"}` | nil |

- [ ] **Step 4: Run, expect FAIL** (`go test ./outbox/ ./internal/sqlid/`).
- [ ] **Step 5: Green** — the DDL constants, the two `Insert`/`FetchPending`/`CreateTable` edits, `LeaderTableName`, the config field, the two validations. Keep `Record.Seq` out of `Insert`; `Insert` sets `lane` to `LaneAMQP` when `record.Lane == ""`.
- [ ] **Step 6: `go test ./outbox/... ./internal/sqlid/... ./config/...` PASS**, every pre-existing store test unchanged except the row-set column lists, which gain `lane, stream, partition_key, seq` (update the fixtures, not the assertions).
- [ ] **Step 7: `make check`, commit** — `feat(outbox)!: sequence the ledger, mark each row's lane, seed a leader row`.

### Task 2: `Publish` learns the stream target

**Files:**

- Modify: `app/module.go` (`OutboxEvent` `130-148`)
- Modify: `outbox/publisher.go` (`newPublisher` `25-30`, `Publish` `32-100`)
- Modify: `outbox/module.go` (`lazyPublisher.Publish` `390-403` — pass the target set)
- Create: `outbox/errors.go`
- Test: `outbox/publisher_test.go` (beside `TestPublisherPublishWithDefaultExchange`), `outbox/module_test.go`

**Interfaces:**

- Produces, in `app`: `OutboxEvent.Stream string` — doc: "Stream targets a super stream on the native streams lane instead of an exchange; the partition key is the tenant stamp from ctx (a tenant is required), and Exchange and RoutingKey must be empty. The name must be listed in outbox.superstreams."
- Produces, in `outbox` (`errors.go`):
  - `var ErrStreamTargetRequiresTenant = errors.New("outbox: a stream target takes its partition key from the context tenant, and the context carries none")`
  - `var ErrConflictingTargets = errors.New("outbox: an event targets either an exchange or a stream; a stream target takes no exchange or routing key")`
  - `var ErrStreamNotAnOutboxTarget = errors.New("outbox: stream is not listed in outbox.superstreams")`
- Produces: `newPublisher(store Store, defaultExchange string, superStreams []string) app.OutboxPublisher` — the slice is turned into a set once; `lazyPublisher.Publish` passes `p.module.cfg.SuperStreams`.
- Behaviour of `Publish`, in this order after the existing nil/empty checks: if `event.Stream != ""` — `event.Exchange != "" || event.RoutingKey != ""` → `ErrConflictingTargets`; name not in the set → `fmt.Errorf("%w: %q", ErrStreamNotAnOutboxTarget, event.Stream)`; `multitenant.GetTenant(ctx)` not ok → `ErrStreamTargetRequiresTenant`; else the record is `Lane: LaneStream, Stream: event.Stream, PartitionKey: <tenant>`, `Exchange`/`RoutingKey` empty. Otherwise the record is `Lane: LaneAMQP` with today's exchange/routing-key fallbacks. The tenant-stamp persistence plan A added to `marshalHeaders` is unchanged and applies to both lanes.
- Consumes: `multitenant.GetTenant` (plan A keeps it), plan A's stamp write in `marshalHeaders`.

**Seams (pre-agreed):** `outboxPublisher.Publish` through `fakeStore` (`InsertCalls`, and the captured record — add `InsertLastRecord *Record` to `fakeStore` in `test_helpers_test.go`); `lazyPublisher.Publish` through `Module` with a stubbed store (the existing `module_test.go` fixtures).

- [ ] **Step 1: Red**

| case name | ctx tenant | event | expect |
| --- | --- | --- | --- |
| `stream_target_persists_lane_and_key` | `acme` | `{EventType:"customer.created", AggregateID:"c1", Stream:"customers"}` with set `{customers}` | `InsertCalls == 1`; record `Lane == "stream"`, `Stream == "customers"`, `PartitionKey == "acme"`, `Exchange == ""`, `RoutingKey == ""` |
| `stream_target_without_tenant` | none | same | `errors.Is(err, ErrStreamTargetRequiresTenant)`; `InsertCalls == 0` |
| `stream_target_with_exchange` | `acme` | `Stream:"customers", Exchange:"orders"` | `errors.Is(err, ErrConflictingTargets)`; `InsertCalls == 0` |
| `stream_target_with_routing_key` | `acme` | `Stream:"customers", RoutingKey:"x"` | `ErrConflictingTargets` |
| `stream_target_not_configured` | `acme` | `Stream:"payments"` with set `{customers}` | `errors.Is(err, ErrStreamNotAnOutboxTarget)`; message contains `"payments"` |
| `amqp_event_marks_lane` | none | `{EventType:"order.created", AggregateID:"o1"}` | record `Lane == "amqp"`, `RoutingKey == "order.created"` (existing fallback), `Stream == ""` |
| `lazy_publisher_passes_configured_targets` | `acme` | module with `cfg.SuperStreams = {customers}`, event `Stream:"customers"` | insert reaches the store; with `cfg.SuperStreams` empty the same event fails with `ErrStreamNotAnOutboxTarget` |

- [ ] **Step 2: Run, expect FAIL.** **Step 3: Green.** **Step 4: `go test ./outbox/... ./app/...` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(outbox): let an event target a super stream keyed by the tenant stamp`.

### Task 3: Gates for PR B1 (controller only)

- [ ] **Step 1: `make check`** (detached, read `EXIT=0`).
- [ ] **Step 2: `/simplify`** — re-run `make check` if it changed code.
- [ ] **Step 3: `/security-audit`** — no sentinel or error text echoes a payload; the DDL strings interpolate only `sqlid`-validated names.
- [ ] **Step 4: `/code-review`** — apply findings, `make check` again, re-run `/code-review` if code changed.
- [ ] **Step 5: `make mutate`, backgrounded, after committing** — the three `Publish` guards each need a separable failing test (the table above gives one per guard); the `lane == ""` default in `Insert` is killed by `insert_amqp_row_defaults_lane`.

---

## PR B2 — one leader, key-ordered

### Task 4: `Store.Lead`, `database.IsLockNotAvailable`, `ErrNotLeader`

**Files:**

- Modify: `database/errors.go` (constants `13-17`, beside `IsUniqueViolation` `18-36`)
- Modify: `outbox/store.go` (`Store` `49-80`), `outbox/errors.go` (Task 2's file)
- Modify: `outbox/store_postgres.go`, `outbox/store_oracle.go` (new method each)
- Modify: `outbox/test_helpers_test.go` (`fakeStore` `65-155`)
- Test: `database/errors_test.go`, `outbox/store_postgres_test.go`, `outbox/store_oracle_test.go`

**Interfaces:**

- Produces, in `database`: `const pgLockNotAvailable = "55P03"`, `const oraResourceBusy = 54 // ORA-00054`; `func IsLockNotAvailable(err error) bool` — same `errors.As` shape as `IsUniqueViolation`.
- Produces, in `outbox`:
  - `var ErrNotLeader = errors.New("outbox: another relay instance leads this ledger")`
  - `type Leadership interface { Probe(ctx context.Context) error; Release(ctx context.Context) error }`
  - `Store` gains `Lead(ctx context.Context, db dbtypes.Interface) (Leadership, error)` — doc: "Lead takes the ledger's leader row FOR UPDATE NOWAIT in a transaction it holds until Release. ErrNotLeader when another instance holds it. Probe fails once the transaction is gone (timeout, recycled connection, partition), and the caller must stop draining on the first failed probe."
  - Both stores: `Lead` = `db.Begin(ctx)` → `tx.QueryRow(ctx, "SELECT id FROM <leader> WHERE id = 1 FOR UPDATE NOWAIT").Scan(&id)`; `database.IsLockNotAvailable(err)` → `tx.Rollback(ctx)`, return `ErrNotLeader`; `sql.ErrNoRows` → rollback, return `fmt.Errorf("outbox <vendor>: leader row missing in %s; run the documented migration", leaderTable)`; other errors → rollback, wrap. The returned `leadership{tx}` has `Probe` = `tx.Exec(ctx, "SELECT 1")` (Oracle: `SELECT 1 FROM dual`) and `Release` = `tx.Rollback(ctx)` (nothing to commit).
  - `fakeStore` gains `LeadErr error`, `LeadCalls int`, `ProbeErrAfter int` (probe returns `ProbeErr` from the N-th call), `ProbeErr error`, `ProbeCalls int`, `ReleaseCalls int`; its `Lead` returns a `fakeLeadership` bound to those counters. Default (all zero) = leader, probes pass — so every existing relay test keeps passing untouched.

**Seams (pre-agreed):** `database.IsLockNotAvailable` with a constructed `*pgconn.PgError{Code: "55P03"}` and `*oranet.OracleError{ErrCode: 54}` (mirror `database/errors_test.go` for the existing helpers); both stores' `Lead` through `dbtesting.NewTestDB(vendor).ExpectTransaction()` — `ExpectQuery("FOR UPDATE NOWAIT").WillReturnRows(...)` / `.WillReturnError(...)`, then `AssertRolledBack` after `Release`.

- [ ] **Step 1: Red — `IsLockNotAvailable`**

| input | expect |
| --- | --- |
| `&pgconn.PgError{Code: "55P03"}` wrapped with `%w` | true |
| `&pgconn.PgError{Code: "23505"}` | false |
| `&oranet.OracleError{ErrCode: 54}` | true |
| `errors.New("x")`, nil | false |

- [ ] **Step 2: Red — `Lead`**

| case name | vendor | tx expectation | expect |
| --- | --- | --- | --- |
| `lead_acquires_row` | pg, ora | query `FOR UPDATE NOWAIT` returns one row `1` | non-nil `Leadership`; the query SQL names `gobricks_outbox_leader`; `Probe` runs `SELECT 1` (`FROM dual` on ora) and returns nil; `Release` rolls the tx back (`AssertRolledBack`) |
| `lead_not_leader` | pg, ora | query returns a lock-not-available vendor error | `errors.Is(err, ErrNotLeader)`; tx rolled back |
| `lead_row_missing` | pg | query returns `sql.ErrNoRows` | error contains `leader row missing` and the table name; rolled back |
| `probe_reports_dead_transaction` | pg | `Exec("SELECT 1")` returns an error | `Probe` returns it |

- [ ] **Step 3: Run, expect FAIL.** **Step 4: Green.** **Step 5: `go test ./database/ ./outbox/...` PASS** (the relay tests still pass with the zero-value fake).
- [ ] **Step 6: `make check`, commit** — `feat(outbox): take the ledger's leader row for the length of a relay cycle`.

### Task 5: The relay leads, probes, and drains key-ordered

**Files:**

- Modify: `outbox/relay.go` (`Relay` `32-43`, `relayTenant` `82-132`, `runRelayLoop` `146-182`, `publishRecord` `233-303`)
- Test: `outbox/relay_test.go` (beside `TestRelayExecuteCountsFailuresAndContinues` and `TestRelayNeverDeadLettersConnectivityEvenPastMaxRetries`)

**Interfaces:**

- Produces, in `relay.go`:
  - `relayTenant`: after the DB resolves and BEFORE `FetchPending`: `lead, err := r.store.Lead(ctx, db)`; `errors.Is(err, ErrNotLeader)` → `log.Debug().Msg("Outbox relay: another instance leads this ledger; skipping cycle")`, return nil; other error → return `fmt.Errorf("leader: %w", err)`; `defer lead.Release(ctx)`. The `!brokerUsable` outage path runs under leadership too (marks are writes).
  - `runRelayLoop(ctx, log, db, msgClient, lead Leadership, records)`: `parked := map[string]struct{}{}`; per record: `if ctx.Err() != nil { break }`; `if err := lead.Probe(ctx); err != nil { log.Warn().Err(err).Msg("Outbox relay lost leadership mid-cycle; stopping"); res.outageErr = fmt.Errorf("leadership lost: %w", err); return res }`; `key := relayKey(&records[i])`; `if _, p := parked[key]; p { res.parked++; continue }`; then today's switch, plus `case outcomeFailed: res.failed++; parked[key] = struct{}{}`.
  - `relayBatchResult` gains `parked int`; `logCycle` gains `.Int("parked", parked)`; `total` still equals `len(records)` and `published+unrecorded+failed+deadlettered+parked` sums to it on a full cycle.
  - `func relayKey(record *Record) string` — `LaneStream` → `record.PartitionKey`; otherwise decode the persisted headers and, if `x-tenant-id` is a non-empty string, return it, else `record.RoutingKey`. A decode failure returns `record.RoutingKey` (the row itself is poison and `publishRecord` dead-letters it; parking by routing key keeps later rows of the same key behind it until it parks — consistent with a permanently failing head).
  - The leadership loss also stops `markOutage`'s remainder walk: `markOutage` takes `lead` and probes before each mark.
- Consumes: Task 4's `Leadership`, `ErrNotLeader`; `messaging.TenantStampHeader` (plan A).

**Seams (pre-agreed):** `Relay.Execute` through `newRelayWithFakes(store, amqp)` + `newFakeJobCtx` — observing `fakeStore` counters (`LeadCalls`, `ProbeCalls`, `ReleaseCalls`, `MarkFailedCalls`, `MarkPublishedLastID`), `fakeAMQP.PublishCalls` and the per-key `PublishErrFor` map; a captured logger is NOT required — assert on counters and outcomes.

- [ ] **Step 1: Red**

| case name | records (id/key/lane) | fakes | expect |
| --- | --- | --- | --- |
| `not_leader_skips_cycle` | one pending | `LeadErr: ErrNotLeader` | `Execute` returns nil; `FetchPendingCalls == 0`; `PublishCalls == 0`; `ReleaseCalls == 0` |
| `leader_error_fails_cycle` | one pending | `LeadErr: errors.New("leader row missing")` | `Execute` returns an error containing `leader`; no publish |
| `leader_released_after_cycle` | two pending | defaults | `LeadCalls == 1`; `ReleaseCalls == 1`; `ProbeCalls == 2` (one per record) |
| `lost_leadership_stops_batch` | three pending | `ProbeErrAfter: 2, ProbeErr: errors.New("gone")` | `PublishCalls == 1`; `MarkFailedCalls == 0` for the rest; `Execute` returns an error containing `leadership lost`; `ReleaseCalls == 1` |
| `failed_key_parks_later_rows` | K1 (rk `k`), K2 (rk `k`), J1 (rk `j`) in that order | `PublishErrFor["ex:k"] = ErrPublishConfirmTimeout` | K1: `MarkFailedCalls == 1` (id K1); K2 NOT attempted (`PublishCalls == 2`: K1 and J1); J1 `MarkPublishedLastID == "J1"`; `MarkFailedLastID == "K1"` — K2's retry_count untouched |
| `next_cycle_reattempts_parked_key_in_order` | same rows, error cleared before the second `Execute` | — | second cycle publishes K1 then K2 (assert `fakeAMQP` records publish order — add `PublishOrder []string` of routing keys to the fake) |
| `stamped_amqp_rows_key_by_stamp` | A1 headers `{"x-tenant-id":"acme"}` rk `a`, A2 headers `{"x-tenant-id":"acme"}` rk `b`, B1 headers `{"x-tenant-id":"beta"}` rk `a` | `PublishErrFor["ex:a"]` fails only the FIRST call (add `PublishErrOnce` to the fake) | A2 parked (`PublishCalls == 2`: A1, B1); B1 published although its routing key equals A1's |
| `stream_rows_key_by_partition_key` | S1 `{Lane:"stream", PartitionKey:"acme"}`, S2 same key | stream publisher (Task 7) absent — rows dead-letter as poison at `MaxRetries` 1 | with `MaxRetries: 1`: S1 dead-lettered (`outcomeDeadLettered` does NOT park); S2 also attempted and dead-lettered (`MarkDeadLetteredCalls == 2`) |
| `dead_lettered_row_does_not_park` | K1 poison headers `{not json}` `RetryCount: 2`, K2 rk `k` | `MaxRetries: 3` | K1 dead-lettered; K2 published |
| `outage_path_marks_under_leadership` | two pending, broker not ready | `amqp.Ready = false` | `LeadCalls == 1`; `MarkFailedCalls == 2`; `ReleaseCalls == 1`; `Execute` returns the outage error |
| `existing_relay_tests_unchanged` | — | — | every pre-existing `TestRelay*`/`TestPublishRecord*` test passes with the zero-value fake |

- [ ] **Step 2: Run, expect FAIL.** **Step 3: Green.** **Step 4: `go test -race ./outbox/...` PASS.**
- [ ] **Step 5: `make check`, commit** — `feat(outbox): drain each ledger key-ordered under one relay leader`.

### Task 6: Gates for PR B2 (controller only)

- [ ] **Step 1: `make check`**, **Step 2: `/simplify`**, **Step 3: `/security-audit`** (the leader SQL interpolates only the validated name; `ErrNotLeader` and the probe error carry no row content), **Step 4: `/code-review`**.
- [ ] **Step 5: `make mutate`, backgrounded, after committing** — hand-apply what gremlins cannot: delete the `parked[key] = struct{}{}` line (→ `failed_key_parks_later_rows` fails), delete the probe call (→ `lost_leadership_stops_batch` fails), swap `errors.Is(err, ErrNotLeader)` for `err != nil` (→ `leader_error_fails_cycle` fails). Record the three failing test names in the report.

---

## PR B3 — the stream lane, the docs, ADR-088

### Task 7: The outbox declares its stream publishers; the relay publishes stream-lane rows

**Files:**

- Modify: `outbox/module.go` (`Module` `34-49`, `Init` `78-144` — a new check beside `validatePublishTimeout`, `RegisterJobs` `306-350`; new method `DeclareStreams`)
- Modify: `outbox/relay.go` (`Relay` `32-43`, `publishRecord` `233-303` — lane dispatch; new `publishStreamRecord`, `classifyStreamError`)
- Modify: `outbox/test_helpers_test.go`, `outbox/relay_test.go` (`newRelayWithFakes` `49-62`)
- Test: `outbox/module_test.go`, `outbox/relay_test.go`

**Interfaces:**

- Produces, in `outbox`:
  - `type streamPublisher interface { Publish(ctx context.Context, msg *streams.PublishMessage) error }` (satisfied by `*streams.Publisher`; unexported).
  - `Module.streamPublishers map[string]streamPublisher`; `func (m *Module) DeclareStreams(decls *streams.Declarations)` — no-op when `!m.cfg.Enabled || len(m.cfg.SuperStreams) == 0`; otherwise for each name `m.streamPublishers[name] = decls.DeclareSuperStreamPublisher(&streams.SuperStreamPublisherOptions{SuperStream: name})`. (`app.StreamsDeclarer` is satisfied; the registry calls it during `prepareRuntime`, before jobs register — `app/lifecycle.go:25-63`, `startSlots` precedes `RegisterJobs`.)
  - `Init`: `len(m.cfg.SuperStreams) > 0 && m.config.Messaging.Streams.URI == ""` → `errors.New("outbox: superstreams is set but messaging.streams.uri is not; the relay cannot reach a super stream without the stream protocol")`.
  - `Relay.streamPublisher func(name string) (streamPublisher, bool)` — `RegisterJobs` wires `func(name string) (streamPublisher, bool) { p, ok := m.streamPublishers[name]; return p, ok }` (the map is written once in `DeclareStreams` before any cycle; no lock).
  - `publishRecord`: after plan A's rehydrate-and-strip step: `switch record.Lane { case LaneAMQP, "": <today's AMQP path>; case LaneStream: return r.publishStreamRecord(ctx, log, db, pubCtx, headers, record); default: return r.deadLetterPoison(ctx, log, db, record, fmt.Sprintf("unknown lane %q", record.Lane)), nil }`.
  - `publishStreamRecord`: `pub, ok := r.streamPublisher(record.Stream)`; `!ok` → poison `"stream %q is not an outbox target"`; `record.PartitionKey == ""` → poison `"stream row has no partition key"`; `recCtx, cancel := context.WithTimeout(pubCtx, r.config.PublishTimeout)`; `err := pub.Publish(recCtx, &streams.PublishMessage{Data: record.Payload, Properties: headers, RoutingKey: record.PartitionKey})`; `cancel()`; `errors.Is(err, context.Canceled) || errors.Is(err, streams.ErrPublisherClosed)` → `outcomeAborted`; any other error → `markRecordFailed` + `outcomeFailed` (connectivity — `ErrPublisherNotStarted`, `DeadlineExceeded`, a confirmation failure); nil → `MarkPublished` exactly as the AMQP path (share the tail: extract `r.recordPublished(ctx, log, db, record) publishOutcome` from `publishRecord` `292-302` and call it from both lanes).
  - `newRelayWithFakes` gains a variadic `withStreamPublishers(map[string]streamPublisher)` option or a third parameter — pick the third parameter `streams map[string]streamPublisher` (nil for the existing tests; update their call sites mechanically).
  - `fakeStreamPublisher` in `test_helpers_test.go`: `Err error`, `Calls int`, `LastMsg *streams.PublishMessage`, `LastCtx context.Context`.
- Consumes: `streams.Publisher`, `streams.PublishMessage`, `streams.ErrPublisherClosed`, `streams.ErrPublisherNotStarted`, `streams.Declarations.DeclareSuperStreamPublisher`; plan A's stamp rehydration (`multitenant.GetTenant(pubCtx)` holds the row's tenant, so the streams publisher stamps the property itself — the relay does NOT put `x-tenant-id` in `Properties`).

**Seams (pre-agreed):** `Relay.Execute` through `newRelayWithFakes(store, amqp, streams)` observing `fakeStreamPublisher` and `fakeStore`; `Module.DeclareStreams` through a real `streams.NewDeclarations()` observing `decls.Stats().Publishers` and the map; `Module.Init` through the existing `module_test.go` fixtures.

- [ ] **Step 1: Red — module**

| case name | cfg | expect |
| --- | --- | --- |
| `declare_streams_declares_one_publisher_per_target` | `SuperStreams: {customers, payments}`, enabled | `decls.Stats().Publishers == 2`; `len(m.streamPublishers) == 2`; keys `customers`, `payments` |
| `declare_streams_noop_without_targets` | `SuperStreams` nil | `Publishers == 0` |
| `declare_streams_noop_when_disabled` | disabled, targets set | `Publishers == 0` |
| `init_rejects_targets_without_stream_uri` | targets set, `Messaging.Streams.URI == ""` | `Init` error contains `messaging.streams.uri` |
| `init_accepts_targets_with_stream_uri` | targets set, URI `rabbitmq-stream://x:5552` | `Init` nil (the rest of the fixture as `TestModuleInitEnabledWithBothResolvers`) |

- [ ] **Step 2: Red — relay**

| case name | row | stream fake | expect |
| --- | --- | --- | --- |
| `stream_row_publishes_with_partition_key` | `{Lane:"stream", Stream:"customers", PartitionKey:"acme", Payload:[]byte("p"), Headers:{"traceparent":…}}` | `customers` → nil | `Calls == 1`; `LastMsg.RoutingKey == "acme"`; `LastMsg.Data == "p"`; `LastMsg.Properties["x-outbox-event-id"] == row id`; `Properties` has NO `x-tenant-id`; `multitenant.GetTenant(LastCtx) == ("acme", true)` when the row's headers carry the stamp; `MarkPublishedCalls == 1`; `PublishCalls == 0` on the AMQP fake |
| `stream_row_confirmation_failure_is_connectivity` | same | `Err: errors.New("publish to stream \"customers\" was not confirmed by the broker")`, `RetryCount: 99` | `MarkFailedCalls == 1`; `MarkDeadLetteredCalls == 0` |
| `stream_row_not_started_is_connectivity` | same | `Err: streams.ErrPublisherNotStarted` | `MarkFailedCalls == 1` |
| `stream_row_closed_aborts` | same | `Err: streams.ErrPublisherClosed` | no mark; batch stops (`outcomeAborted`: a following AMQP row is not attempted) |
| `stream_row_unknown_stream_is_poison` | `Stream:"payments"` with fakes `{customers}` `RetryCount: 2`, `MaxRetries: 3` | — | `MarkDeadLetteredCalls == 1`; `Calls == 0` |
| `stream_row_empty_key_is_poison` | `PartitionKey:""` `RetryCount: 2` | — | dead-lettered; `Calls == 0` |
| `unknown_lane_is_poison` | `Lane:"carrier-pigeon"` `RetryCount: 2` | — | dead-lettered; neither fake called |
| `stream_row_honors_publish_timeout` | fake blocks until ctx done and returns `ctx.Err()` (add `Block bool`) with `PublishTimeout: 50ms` | — | returns within 1s; `MarkFailedCalls == 1` (`DeadlineExceeded` is connectivity) |
| `amqp_rows_untouched` | existing AMQP tests | — | all pass with a nil streams map |

- [ ] **Step 3: Run, expect FAIL.** **Step 4: Green.** **Step 5: `go test -race ./outbox/...` PASS.**
- [ ] **Step 6: `make check`, commit** — `feat(outbox): publish stream-lane rows through the native streams publisher`.

### Task 8: Integration proofs on real databases

**Files:**

- Create: `outbox/store_integration_test.go` (`//go:build integration`)
- Test only.

**Interfaces:**

- Consumes: `testing/containers` (`MustStartPostgreSQLContainer`, `StartOracleContainerForTestMain` — mirror `inbox/store_integration_test.go` for the connection factory and the Oracle `TestMain` shape used in `database/oracle/integration_main_test.go`), the real stores, `Relay` with `fakeAMQP`.

**Seams (pre-agreed):** the real `Store` against a container; two `Relay` values sharing one table.

- [ ] **Step 1: Write the tests** (they are red only in the sense that Task 1's DDL is what makes them pass; run them once here with `-tags=integration`):

| test | vendor | proves |
| --- | --- | --- |
| `TestOutboxStoreCreateTableAndOrderIntegration` | pg, ora | `CreateTable` succeeds twice (idempotent on PG; Oracle's second call errors and the test asserts the first shape survives); 20 rows inserted in one transaction with an identical `CreatedAt` come back from `FetchPending` in insertion order with strictly increasing `Seq` |
| `TestOutboxStoreManagedAlterIntegration` | pg, ora | create the PRE-B1 table shape by hand (the old DDL, copied into the test as a constant), insert 3 rows, apply the documented `ALTER`/`CREATE`/`INSERT` statements from Task 1 verbatim, then `FetchPending` returns the 3 rows with `Lane == "amqp"` and increasing `Seq`, and `Lead` succeeds |
| `TestOutboxRelayTwoInstancesOneLedgerIntegration` | pg | 200 pending rows across 5 keys; two `Relay` values (real store, one `fakeAMQP` each recording publish order) run `Execute` concurrently 10 cycles each; every row is published EXACTLY once across both fakes; per key, the publish order equals `seq` order; at least one cycle returned nil without publishing (the non-leader) |
| `TestOutboxRelayDeposedLeaderStopsIntegration` | pg | relay A takes leadership through `store.Lead` directly; from a second connection `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE '%FOR UPDATE NOWAIT%' AND pid <> pg_backend_pid()`; A's `Probe` then fails; relay B's `Lead` succeeds within one cycle |

- [ ] **Step 2: `go test -race -count=1 -tags=integration ./outbox/` PASS** (Docker required; Oracle cases skip cleanly on Windows the way `TestMigrateAllMixedVendors` does).
- [ ] **Step 3: `make check`, commit** — `test(outbox): prove sequence order, the managed migration and the leader on real databases`.

### Task 9: ADR-088, the migrations atom, and the docs

**Files:**

- Create: `wiki/adr_088_outbox_ordered_relay_and_stream_lane.md`
- Modify: `wiki/architecture_decisions.md` (an `### [ADR-088: …]` entry after ADR-087's, and the `through ADR-087` counter → `ADR-088`)
- Modify: `wiki/adr_033_outbox_retry_count_status_parking.md` (an amendment block at the TOP in ADR-070's 2026-08-28 format: the classification now also covers the stream lane and the parked outcome)
- Modify: `wiki/migrations.md` (E61 hop row: count +1, gist clause, build-caught list gains `C61.22`, preflight guidance; a new `### [C61.22]` atom after the last one on the hop)
- Modify: `.claude/skills/breaking-changes/SKILL.md` (one line keyed `(ADR-088)`)
- Modify: `wiki/outbox.md` (How It Works step 3 gains the lane; a new `## Lanes and ordering` section after `## Retry & Dead-Lettering`; the config table gains `outbox.superstreams`; the schema/managed-migration statements from Task 1; the Multi-Tenant section notes the leader is per ledger)
- Modify: `wiki/streams.md` (under `## Publishing`: "The outbox relay as a super-stream producer" — one paragraph pointing at `outbox.superstreams` and the one-publisher-per-target consequence)
- Modify: `llms.txt` (the outbox YAML gains `superstreams: [customers]`; the `OutboxEvent` example gains a stream-targeted publish with the tenant in ctx)
- Modify: `config/types.go` (`OutboxConfig` defaults comment: `SuperStreams: none`)

**Interfaces:** none (docs).

**ADR-088 text** (write it as the file, in the ADR-086 header shape):

```markdown
# ADR-088: The Outbox Relay Drains Key-Ordered Under One Leader and Gains a Stream Lane

- **Status**: Accepted
- **Date**: <merge date>
- **Related**: [ADR-033](adr_033_outbox_retry_count_status_parking.md) (the classification this extends to a second lane and a parked outcome) · [ADR-063](adr_063_streams_native_publishing.md) (the confirmed super-stream publisher and its murmur3 interop this lane rides) · [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md) (the tenant stamp this lane partitions by)

## Context

Ordered consumption over a super stream (ADR-087) is worthless if the producer reorders at
the source. Three things did, all in `outbox`:

- `FetchPending` ordered by `created_at`, which ties at clock resolution, so even one relay's
  order was not defined.
- A row whose publish failed was retried on a later cycle, after rows created later than it —
  one transient failure inverted a tenant's `customer.created` / `payment_instrument.created`.
- Every replica of a producer service ran its own relay against the same table: interleaved
  rows and duplicate publishes, with the consumer's `x-outbox-event-id` check as the only
  dedup.

And the relay could not reach a super stream at all: it published over AMQP 0.9.1 only. A super
stream is a direct exchange with binding keys `"0"…"n-1"` (vendor source, `super_stream.go`),
so reaching it over 0.9.1 means reimplementing the vendor's murmur3 partition hash — the exact
interop ADR-063 already guarantees on the native lane.

## Decision

1. **A per-ledger sequence, assigned by the database at insert.** `seq` is an identity column
   on both vendors; `FetchPending` orders by it and `created_at` becomes diagnostic. The promise
   is causal: a dependent event's transaction begins after its cause committed, so its sequence
   is higher. Independent transactions may commit out of sequence order; no claim is made
   between them.
2. **Key-ordered drain.** Within a cycle, the first row that fails for partition key K parks
   every later row for K: not published, not marked, `retry_count` untouched. The key is the
   tenant stamp (`partition_key` on the stream lane; the persisted `x-tenant-id` on the AMQP
   lane) and, for an unstamped AMQP row, the routing key. A dead-lettered row is terminal and a
   delivered-but-unrecorded row was delivered, so neither parks.
3. **One leader per ledger.** A cycle takes the ledger's leader row `FOR UPDATE NOWAIT` in a
   transaction held until the cycle ends and probed before every record; a failed probe stops
   the cycle. A deposed or dead leader loses the row when its transaction dies — a crashed
   process at once, a partitioned one when the server drops the session — with no operator
   action. Non-leaders log at DEBUG and return.
4. **A stream lane.** An `OutboxEvent` may target a super stream listed in
   `outbox.superstreams`; the row records `lane = 'stream'`, the stream, and the partition key —
   the tenant stamp from the publishing context, required. The outbox module declares one
   super-stream publisher per listed target (`app.StreamsDeclarer`), so binding, validation
   and shutdown ride the streams manager, and the relay publishes through `streams.Publisher`
   with `RoutingKey = partition_key`. ADR-033's classification applies: broker-side and
   confirmation failures are connectivity; an unknown lane, an unlisted stream or an empty key
   is poison.

## Alternatives considered

- **AMQP 0.9.1 into the super stream's exchange.** Rejected: the relay would carry its own
  murmur3 and query the partition count; a hash drift from the vendor silently re-partitions
  every tenant.
- **Lazily bound stream publishers.** Rejected: a post-start bind API on the streams manager, an
  app-to-module factory, and a manager that starts with nothing declared — three seams for one
  config key.
- **PostgreSQL advisory lock + Oracle `DBMS_LOCK`.** Rejected: two mechanisms, and `DBMS_LOCK`
  needs an `EXECUTE` grant application roles routinely lack.
- **A lease row with TTL renewal.** Rejected: a write per record, TTL arithmetic against the
  cycle length, and a clock — for a guarantee the probed transaction gives for free.
- **A per-key sequence the consumer gap-checks.** Rejected: couples every consumer to every
  producer's sequence contract.

## Consequences

- `config.OutboxConfig` gains a slice and `outbox.Store` a method: both are compile-breaks for
  code outside the framework (`[C61.22]`); the schema gains four columns, a companion leader
  table and a re-keyed index — `autocreatetable` deployments get them on the next start,
  managed deployments run the documented statements before upgrading.
- A parked key keeps its later rows in every batch until its head succeeds or dead-letters; a
  backlog for one key can starve the others. A per-key fetch cap is a follow-up.
- The leader's transaction is idle for the length of a cycle. A cycle is bounded by
  `batchsize × publishtimeout`; an `idle_in_transaction_session_timeout` shorter than that
  deposes a healthy leader every cycle — set it above the bound or leave it unset.
- An outbox-targeted super stream has the outbox as its one publisher in the process; a module
  that also publishes directly to it must go through the outbox.
- Duplicate publishes across replicas end; `x-outbox-event-id` dedup stays as defense in depth.
```

**The atom** (`### [C61.22] the outbox ledger is sequenced, laned and led · silent-behavior + compile-break · when: match`): detect = `outbox.enabled: true` anywhere; or `git grep -n 'outbox.Store' -- '*.go'` for an outside implementation; or code comparing `config.OutboxConfig` values. scope = the four columns, the leader table, the `(seq)` index, sequence order, per-key parking, one leader per ledger, `outbox.superstreams`, `OutboxEvent.Stream`, the three sentinels, `Store.Lead`. gate = match if the outbox is enabled (schema) or `Store` is implemented outside (compile). apply = `autocreatetable: true` → nothing; managed migrations → run Task 1's statements for your vendor BEFORE deploying, and grant the relay role `SELECT … FOR UPDATE` on the leader table; outside `Store` implementations add `Lead`. verify = after upgrade, `SELECT seq, lane FROM gobricks_outbox ORDER BY seq LIMIT 5` returns rows; a relay log line reads `parked=0` on a healthy cycle; with two replicas, one logs `another instance leads this ledger` at DEBUG. ref = ADR-088, `outbox/store_postgres.go`, `outbox/store_oracle.go`, `outbox/relay.go`, issue #1232.

**Seams (pre-agreed):** greps (`git grep -n 'ORDER BY created_at' -- 'outbox/*.go'` empty; `git grep -n 'ADR-088' wiki/architecture_decisions.md` non-empty; `grep -c '\[C61.22\]' wiki/migrations.md` ≥ 2 — hop row and atom); `make lint-md`.

- [ ] **Step 1:** write ADR-088, the index entry, the counter, the ADR-033 amendment.
- [ ] **Step 2:** write the atom, update the E61 hop row (re-read its current count — siblings moved it), the skill index line.
- [ ] **Step 3:** `wiki/outbox.md`, `wiki/streams.md`, `llms.txt`, the config comment.
- [ ] **Step 4:** `make lint-md`; the greps above.
- [ ] **Step 5: `make check`, commit** — `docs(outbox): ADR-088, the [C61.22] atom, lanes and ordering`.

### Task 10: Gates for PR B3 (controller only)

- [ ] **Step 1: `make check`**, **Step 2: `/simplify`**, **Step 3: `/security-audit`** (the stream leg's error texts name the stream and the lane, never a payload or a tenant; the relay never puts the stamp into `Properties` itself), **Step 4: `/code-review`**.
- [ ] **Step 5: `make mutate`, backgrounded, after committing** — hand-apply: swap `LaneStream` for `LaneAMQP` in the dispatch (→ `stream_row_publishes_with_partition_key` fails); drop the `ErrPublisherClosed` clause (→ `stream_row_closed_aborts` fails); drop the empty-key guard (→ `stream_row_empty_key_is_poison` fails). Record the failing test names.
- [ ] **Step 6:** the integration suite once more: `go test -race -count=1 -tags=integration ./outbox/`.

---

## Self-review against the spec

- Spec decision 10, "monotonic per-ledger sequence" → Task 1 (`seq`, identity, `ORDER BY seq`). "first failed row for key K parks K's later rows without marking them" → Task 5 (`parked` map, `outcomeFailed` only, `retry_count` untouched: `failed_key_parks_later_rows`). "one relay instance per ledger at a time (leader lock, a dead leader releases without operator action)" → Tasks 4–5 (`FOR UPDATE NOWAIT`, probe per record, `TestOutboxRelayDeposedLeaderStopsIntegration`). "native streams leg using the existing confirmed `streams.Publisher`" → Task 7; "0.9.1 route rejected" → ADR-088 alternatives. Brief §5 docs → Task 9. Brief's "declared lazily" → decision 1 (revisited, reasoned). Brief's "no new keys unless the ADR needs one" → `outbox.superstreams` is needed (decision 1); no leader-lock timeout key.
- Placeholder scan: every DDL statement, sentinel text, signature and test case is spelled out; the ADR carries a `<merge date>` the implementer fills at commit time — the one deliberate blank.
- Type consistency: `Leadership` (Task 4) is what Task 5 threads through `runRelayLoop`/`markOutage`; `streamPublisher` (Task 7) matches `newRelayWithFakes`'s third parameter and the fake; `Record.Lane/Stream/PartitionKey/Seq` (Task 1) are the names Tasks 2, 5, 7 read; `LaneAMQP`/`LaneStream` are used everywhere a lane is compared; `ErrNotLeader`, `ErrStreamTargetRequiresTenant`, `ErrConflictingTargets`, `ErrStreamNotAnOutboxTarget` live in `outbox/errors.go` (Task 2 creates it, Task 4 appends).
