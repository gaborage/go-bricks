# ADR-112: The Session Door Is a Method on `database.Interface`, Not a Capability Assertion

**Status:** Accepted
**Date:** 2026-09-13
**Issue:** #1009

## Context

Session-scoped state is invisible to a connection pool. `SELECT pg_advisory_lock(...)`, a `SET`
on the session, a `CREATE TEMP TABLE`, an Oracle `DBMS_SESSION` call — each belongs to one
physical backend, and `database/sql` is free to hand the next statement to a different one. A
service that takes an advisory lock on one statement and releases it on the next may release
nothing, and never learns: both statements succeed. The framework's own surface offered no way
to ask for a pinned connection, so the only honest answer was "don't do that on GoBricks".

The preceding link added the handle itself: `types.Session` (Querier + Transactor + Close), a
`*sql.Conn`-backed implementation in `database/internal/wrapper`, a tracking wrapper that records
session statements exactly like pool statements, and a `Session(ctx)` method on both vendor
connections. What it deliberately did not do was declare the method on `types.Interface`, so
reaching the door meant a type assertion to an anonymous
`interface{ Session(ctx context.Context) (Session, error) }`. That left three problems. A
consumer holding `deps.DB(ctx)` — which is `database.Interface` — had to write the assertion
itself, with no compiler help if the spelling drifted. The framework had to carry the same
assertion internally: `tracking.Connection.Session` duck-typed its wrapped connection through a
private `sessionOpener` and answered an unsupported one with a runtime error. And the test
doubles consumers build against (`database/testing.TestDB`, `testing/mocks.MockDatabase`) had no
session surface at all, so a handler that opens a session was untestable with the framework's own
fakes.

## Options Considered

**A — keep the capability assertion.** Additive, apidiff-silent, and every existing implementer
of `Interface` keeps compiling. Rejected: it makes the framework's central database interface
lie about what a database handle can do, and pushes an unchecked string-matched assertion into
every consumer that wants a pinned connection. An assertion that can fail at runtime where a
method signature could fail at compile time is the trade GoBricks' "Type Safety > Dynamic Hacks"
principle exists to refuse.

**B — a second interface, `SessionOpener`, exported for consumers to assert to.** Names the
capability, keeps `Interface` untouched, and reads like the small focused interfaces the
manifesto asks for. Rejected: the capability is not optional. Both vendors implement it, the
tracking wrapper implements it, and nothing in the framework can configure it away — so an
exported opt-in interface would advertise a choice that does not exist, and every consumer would
still write the assertion.

**C — `deps.Session(ctx)` as a `ModuleDeps` accessor beside `DB`.** Consumers never touch the
interface, and the accessor could own the per-tenant lease rule. Rejected: a session is acquired
from a specific connection, and `ModuleDeps` already hands out that connection. A parallel
accessor would duplicate the resolution path (named databases, per-tenant pools, dynamic
resources) for no new capability, and would hide which pool the session came from.

**D — add the method to `Interface` (chosen).** One compile break, found by the compiler in
every implementer, on a surface whose implementers are overwhelmingly the framework itself plus
test doubles.

## Decision

- **`types.Interface` declares `Session(ctx context.Context) (Session, error)`.** The method's
  godoc points at `types.Session`, which remains the single home of the contract; `Interface`
  does not restate it. Every implementer now provides it: the two vendor connections and the
  tracking wrapper (from the preceding link), `database/testing.TestDB`, `testing/mocks.MockDatabase`,
  and the test stubs in `database` and `database/internal/tracking`. A consumer holding
  `database.Interface` — `deps.DB(ctx)` included — calls the method directly.
- **The framework's own capability assertion is deleted.** `tracking.Connection.Session` calls
  `tc.conn.Session(ctx)` and the private `sessionOpener` duck-type is gone, along with the
  runtime "does not support dedicated sessions" error it existed to produce. The acquisition is
  still tracked as operation `SESSION`, the way `Begin`/`BeginTx` track `BEGIN`/`BEGIN_TX`.
- **The surface stays Querier + Transactor + Close.** No `Prepare`, no `Health`, no `Stats`, no
  migration-table methods: a statement cache, a pool health probe and pool statistics are
  properties of a pool, not of one pinned connection, and a migration runner has no business on
  a session handle. A session that needs a prepared statement can open a transaction, or the
  caller can keep using the pool handle it already has.
- **`database/testing` grows a strict `TestSession`.** `TestDB.ExpectSession()` queues a session;
  `TestDB.Session()` pops the queue in declaration order and returns an error once it is empty,
  so a call with no expectation behind it fails the test instead of receiving a permissive fake.
  `TestSession` carries its OWN query, exec and transaction expectations — a session runs on its
  own connection, so it matches none of the pool's — and `AssertSessionClosed` checks the handle
  was released. `testing/mocks.MockDatabase.Session` returns the `types.Session` the test
  supplied, with no session double of its own.
- **Breaking, with a migrations atom.** Adding a method to an interface breaks every
  implementation outside the framework (`[C65.5]`).

## Consequences

- **Every implementer of `database.Interface` outside the framework stops compiling** until it
  adds `Session`, or embeds a framework connection and inherits it. The population is test
  doubles and adapters; the compiler names each one, and `go vet ./...` catches the ones that
  live in `_test.go` files.
- **The contract is a runtime contract, and the interface cannot enforce it.** The `types.Session`
  godoc is the single authority for the full contract — blocking, concurrency, idempotency and the
  raw row-iteration exception all live there, not here. The one sentence that motivated this break,
  from `database/types/session.go:24`: *"the call that OBSERVES the death may return the driver's
  own error rather than a translated one … Every SUBSEQUENT call returns an error satisfying
  `errors.Is(err, sql.ErrConnDone)`."* So a caller that wants one classification for a dead
  backend must read both the first error and the next one.
- **A Session inherits the tenant-lease lifetime rule and cannot extend it** (ADR-032). The bound
  itself — the scope a session must not outlive, and what the tenant's pool does once the lease is
  released — is stated in the `types.Session` godoc, which this ADR does not restate.
- **A handler that opens a session is now testable with the framework's fakes**, and the strict
  queue means a test that forgets `ExpectSession()` fails loudly rather than silently exercising
  a pool path.
- **`Interface` grew, and `Querier`/`Transactor` did not.** Code that only needs statements or
  transactions keeps depending on the small interfaces, which is still the advice in the
  `Interface` godoc.

## References

- Issue #1009 (a pinned session handle for session-scoped state)
- `types.Session` (`database/types/session.go`) — the authoritative contract; `types.Interface`
  (`database/types/interfaces.go`) — the door
- [ADR-032](adr_032_lease_refcount_tenant_handles.md) — the tenant-lease lifetime rule a Session
  inherits and cannot extend
- [ADR-008](adr_008_database_testing_interface_segregation.md) — the `database/testing`
  design `TestSession` follows, including the per-handle expectation queues
- `[C65.5]` in [migrations.md](migrations.md) — the compile break and how to answer it
- `database/internal/wrapper/session.go` (the `*sql.Conn` implementation),
  `database/internal/tracking/session.go` (tracking parity),
  `database/testing/fake_session.go`, `testing/mocks/database.go`
