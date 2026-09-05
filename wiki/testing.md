# Testing (Deep Dive)

This document covers the GoBricks testing conventions and the dedicated testing packages shipped with the framework. It walks through unit-test patterns for databases, caches, and the outbox, plus the testcontainers-based integration testing workflow. The naming conventions section comes first because it is mandatory and applies to every test file in the repository.

## Test Naming Conventions

<!-- markdownlint-disable-next-line MD036 -->
**MANDATORY: Use camelCase for ALL test function names**

```go
// ✅ CORRECT - camelCase naming
func TestUserServiceCreateUser(t *testing.T) { }
func TestCacheManagerGetOrCreateCache(t *testing.T) { }
func TestQueryBuilderWithComplexJoins(t *testing.T) { }

// ❌ WRONG - snake_case (NEVER use this)
func TestUserService_CreateUser(t *testing.T) { }
func Test_CacheManager_GetOrCreateCache(t *testing.T) { }
func TestQueryBuilder_with_complex_joins(t *testing.T) { }
```

**Table-Driven Test Naming:**

```go
// ✅ CORRECT
func TestFilterEq(t *testing.T) {
    tests := []struct {
        name     string  // Use snake_case for test case descriptions
        column   string
        value    any
        expected string
    }{
        {name: "simple_equality", column: "id", value: 1, expected: "id = :1"},
        {name: "string_value", column: "name", value: "Alice", expected: "name = :1"},
    }
}

// ❌ WRONG - function name uses underscores
func Test_Filter_Eq(t *testing.T) { }
```

**Rationale:**

- **Consistency:** GoBricks enforces camelCase across the entire codebase
- **Go Idioms:** Test function names are regular Go identifiers (prefer camelCase)
- **Tooling:** Some tools parse test names assuming camelCase convention
- **Legacy Code:** All existing tests use camelCase (>800 test functions)

**Exception:** Test case descriptions in table-driven tests use snake_case for readability (e.g., `name: "with_invalid_credentials"`)

## Secret-Shaped Fixtures

A test fixture that *looks* like a credential is flagged by org secret scanners, which costs a manual triage pass on every scan and raises the noise floor real findings have to clear. Never write a contiguous `-----BEGIN <type>-----` block, or a password-shaped literal beside a `password` key, as a source literal. Compose the value at runtime instead — `testconsts.PEMFixture(blockType)` and `testconsts.FakePassword(label)` in [testing/secretfixtures.go](../testing/secretfixtures.go) cover both cases, and `PEMFixture` is byte-identical to the literal it replaces, so parsing tests keep exercising real PEM structure.

`tools/migration` is a separate Go module pinned to a released `go-bricks`, so it keeps its own local copies rather than importing these — new helpers there must not reach across the module boundary until the version it pins ships them.

## Testing Strategy

- **Unit tests:** testify, `database/testing` (DB mocking), `cache/testing` (cache mocking), `outbox/testing` (outbox mocking), httptest (server), fake adapters (messaging)
- **Integration tests:** testcontainers, `-tags=integration` flag
- **Race detection:** All tests run with `-race` in CI
- **Coverage target:** 80% (SonarCloud)

## Database Testing

GoBricks provides `database/testing` package for easy database mocking without sqlmock complexity (**73% less boilerplate**).

**Simple Query Test:**

```go
import dbtest "github.com/gaborage/go-bricks/database/testing"

func TestProductServiceFindActive(t *testing.T) {
    // Setup (8 lines vs 30+ with sqlmock)
    db := dbtest.NewTestDB(dbtypes.PostgreSQL)
    db.ExpectQuery("SELECT").
        WillReturnRows(
            dbtest.NewRowSet("id", "name").
                AddRow(int64(1), "Widget").
                AddRow(int64(2), "Gadget"),
        )

    deps := &app.ModuleDeps{
        DB: func(ctx context.Context) (database.Interface, error) {
            return db, nil
        },
    }

    svc := NewProductService(deps)
    products, err := svc.FindActive(ctx)

    assert.NoError(t, err)
    assert.Len(t, products, 2)
    dbtest.AssertQueryExecuted(t, db, "SELECT")
}
```

**Transaction Testing:**

```go
db := dbtest.NewTestDB(dbtypes.PostgreSQL)
tx := db.ExpectTransaction().
    ExpectExec("INSERT INTO orders").WillReturnRowsAffected(1).
    ExpectExec("INSERT INTO items").WillReturnRowsAffected(3)

// Test code that uses transactions
svc.CreateWithItems(ctx, order, items)

dbtest.AssertCommitted(t, tx)
```

**Multi-Tenant Testing:**

```go
tenants := dbtest.NewTenantDBMap()
tenants.ForTenant("acme").ExpectQuery("SELECT").WillReturnRows(...)
tenants.ForTenant("globex").ExpectQuery("SELECT").WillReturnRows(...)

deps := &app.ModuleDeps{
    DB: tenants.AsGetDBFunc(),  // Resolves tenant from context
}

ctx := multitenant.SetTenant(context.Background(), "acme")
result, err := svc.Process(ctx)  // Uses acme's TestDB
```

**Key Features:**

- Fluent expectation API (ExpectQuery/ExpectExec)
- Multi-tenant support via TenantDBMap
- Transaction tracking (commit/rollback assertions)
- Vendor-agnostic RowSet builder
- Partial SQL matching by default (or strict with StrictSQLMatching())

See [database/testing](../database/testing/) package and llms.txt's "Database Testing" section for full examples.

## Cache Testing

GoBricks provides `cache/testing` package for easy cache mocking without Redis dependencies (**similar to database/testing pattern**).

**Simple Cache Test:**

```go
import cachetest "github.com/gaborage/go-bricks/cache/testing"

func TestUserServiceCaching(t *testing.T) {
    mockCache := cachetest.NewMockCache()

    deps := &app.ModuleDeps{
        Cache: func(ctx context.Context) (cache.Cache, error) {
            return mockCache, nil
        },
    }

    svc := NewUserService(deps)
    user, err := svc.GetUser(ctx, 123)

    assert.NoError(t, err)
    cachetest.AssertCacheHit(t, mockCache, "user:123")
}
```

**Configurable Failures:**

```go
mockCache := cachetest.NewMockCache().
    WithGetFailure(cache.ErrClosed)

// Service should gracefully degrade
user, err := svc.GetUser(ctx, 123)  // Falls back to database
assert.NoError(t, err)

// Verify cache operation was attempted — operation names are the cachetest.Op* constants;
// an unknown name fails the test instead of reading as zero calls
cachetest.AssertOperationCount(t, mockCache, cachetest.OpGet, 1)
```

**Multi-Tenant Testing:**

```go
tenantCaches := map[string]*cachetest.MockCache{
    "acme":   cachetest.NewMockCache(),
    "globex": cachetest.NewMockCache(),
}

deps := &app.ModuleDeps{
    Cache: func(ctx context.Context) (cache.Cache, error) {
        tenantID, _ := multitenant.GetTenant(ctx)
        return tenantCaches[tenantID], nil
    },
}

acmeCtx := multitenant.SetTenant(context.Background(), "acme")
result, err := svc.Process(acmeCtx)  // Uses acme's MockCache
```

**Key Features:**

- Fluent configuration API (`WithGetFailure`, `WithDelay`, `WithCloseCallback`)
- Operation tracking (Get/Set/Delete/GetOrSet/CompareAndSet/CompareAndDelete counts)
- 17 assertion helpers (`AssertCacheHit`, `AssertOperationCount`, `AssertValue`)
- TTL expiration testing (real time-based expiration)
- Multi-tenant isolation support

See [cache/testing](../cache/testing/) package for full API documentation and the Cache Testing Utilities section in [llms.txt](../llms.txt) for comprehensive examples.

## Outbox Testing

GoBricks provides `outbox/testing` package for mocking outbox operations in unit tests.

**Simple Test:**

```go
import outboxtest "github.com/gaborage/go-bricks/outbox/testing"

func TestOrderServiceCreateOrder(t *testing.T) {
    db := dbtest.NewTestDB(dbtypes.PostgreSQL)
    tx := db.ExpectTransaction().
        ExpectExec("INSERT INTO orders").WillReturnRowsAffected(1)

    mockOutbox := outboxtest.NewMockOutbox()

    getDB := func(ctx context.Context) (database.Interface, error) { return db, nil }
    svc := NewOrderService(getDB, mockOutbox)
    err := svc.CreateOrder(ctx, order)

    assert.NoError(t, err)
    dbtest.AssertCommitted(t, tx)
    outboxtest.AssertEventPublished(t, mockOutbox, "order.created")
}
```

**Configurable Failures:**

```go
mockOutbox := outboxtest.NewMockOutbox().
    WithError(fmt.Errorf("outbox unavailable"))

// Service should handle outbox failure (transaction rolls back)
err := svc.CreateOrder(ctx, order)
assert.Error(t, err)
```

**Key Features:**

- Fluent configuration API (`WithError`)
- Event tracking (type, aggregate ID, payload, exchange)
- Assertion helpers (`AssertEventPublished`, `AssertEventCount`, `AssertEventWithAggregate`, `AssertNoEvents`)
- Thread-safe for concurrent test scenarios

See [outbox/testing](../outbox/testing/) package for full API documentation.

## Messaging Publish Testing

Since ADR-096 no exported client carries a byte publish method, so a module's publishes are
observed at the typed handle, not at the client. Store the handle behind
`messaging.EventPublisher[T]` and inject `messaging/testing.CapturePublisher[T]`:

```go
type OrderService struct {
    orders messaging.EventPublisher[OrderCreated] // *messaging.Publisher[OrderCreated] in production
}

capture := messagingtesting.NewCapturePublisher[OrderCreated]()
svc := &OrderService{orders: capture}

require.NoError(t, svc.Create(ctx, order))

evt, ok := capture.Last()
require.True(t, ok)
assert.Equal(t, order.ID, evt.OrderID) // the typed value, never a byte frame to re-decode
```

`Fail(err)` makes later publishes return `err` while still recording the attempt; `Events()`
returns every recorded value oldest-first. A `testing/mocks.MockAMQPClient` handed to a real
`Publisher[T]` fails with `messaging.ErrPublishDoorUnavailable` — it is a client double for
declarations and consumption, not a publish sink.

## SQL Goldens

A store port is judged by the SQL it emits, not by the unit tests that pin substrings of it: `database/testing.SQLGolden` renders everything a `TestDB` and its transactions recorded — each statement verbatim, then every bound argument with its type — and `dbtesting.AssertGolden(t, path, got, *update)` pins that text under `testdata/sql/`. Capture the goldens BEFORE the port in the port PR's first commit, diff them after, and name every deliberate text change in the commit body. `SQLGolden{FixedClock: fixedAt}` prints the fixture time verbatim and any other clock value (a store's own `time.Now()`) as `<time>`, so a wrong binding fails while a wall clock does not. See `outbox/store_sql_golden_test.go` and `inbox/store_sql_golden_test.go`.

## Server / HandlerContext Testing

Unit-test a `Handler` or `MiddlewareFunc` without standing up a router by building a
`HandlerContext` directly. Use `server.NewHandlerContextForTest` when no routing state is
needed, or `server.NewHandlerContextForTestWithOptions` to seed it. The synthetic context is
never routed, so routing-derived state is empty by default — seed only what the code under
test reads:

| State | How to seed | Read back via |
| --- | --- | --- |
| Route template (`RouteTemplate()`) | `server.WithRouteTemplate("/api/orders/:id")` construction option | `ctx.RouteTemplate()` |
| Path params (`Param`, `param:"…"` binding) | `ctx.SetPathParams([]server.PathParam{…})` | `ctx.Param("id")` / `PathParams()` |
| Query params | already read from the request URL | `ctx.Query("limit")` |
| Request headers | already read from the request | `ctx.RequestHeader("X-…")` |

The two routing seams are deliberately different shapes: `RouteTemplate` is set once by the
router and has no runtime mutation path, so it is seeded by a **test-only construction
option** (it cannot corrupt a live routed context); path params have a legitimate runtime
setter (`SetPathParams`), reused as-is in tests.

Seed the context, then **drive the middleware/handler under test** and assert on its
observable effect — don't just assert on the seeded context:

```go
// Production middleware under test: record the matched route template.
func RouteTemplateRecorder(sink *string) server.MiddlewareFunc {
    return func(c server.HandlerContext, next func() error) error {
        *sink = c.RouteTemplate()
        return next()
    }
}

func TestRouteTemplateRecorder(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/api/orders/42", http.NoBody)
    c := server.NewHandlerContextForTestWithOptions(httptest.NewRecorder(), req, cfg,
        server.WithRouteTemplate("/api/orders/:id"),
    )

    var recorded string
    err := RouteTemplateRecorder(&recorded)(c, func() error { return nil }) // exercise the middleware

    require.NoError(t, err)
    assert.Equal(t, "/api/orders/:id", recorded) // asserts the middleware's behavior, not the seed
}
```

For end-to-end fidelity (real router populating template *and* params), register the route on
a `server.Server` and drive it with `httptest` / `ServeHTTP` instead.

## Integration Testing with Testcontainers

**Prerequisites:** Docker Desktop or Docker Engine running

**Run Integration Tests:**

```bash
make test-integration           # All integration tests
make test-coverage-integration  # With coverage
```

**Build Tag Isolation:** Integration tests use `//go:build integration` - testcontainers dependencies only compiled with `-tags=integration`

**Writing Integration Tests:**

```go
//go:build integration

func TestFeature(t *testing.T) {
    conn, ctx := setupTestSchema(t)   // Per-test schema on shared container (Oracle, ADR-020)

    _, err := conn.Exec(ctx, "CREATE TABLE widgets (id NUMBER PRIMARY KEY, name VARCHAR2(100))")
    require.NoError(t, err)
    // ... test against the real database
}
```

### Reaper lifetime across packages

Every integration binary in one `go test ./...` invocation shares a single Ryuk reaper: CI pins `TESTCONTAINERS_SESSION_ID` to the run id (PR #943), and locally testcontainers derives the same id from the parent `go test` process.

Ryuk exits 10s after its last client disconnects; `go test` starts packages in list order, up to `-p` at a time, and the ~20 tiny `internal/*` packages queued between the database/inbox group and `messaging` fill the slots for about that long — so `messaging` can look up a reaper that is already exiting.

testcontainers-go v0.44.0 then hangs until the 60s deadline (`wait for reaper <id>: context deadline exceeded`), or gets a handshake EOF and loses its freshly created container to Ryuk's exit prune (`Reaper handshake failed: read ack: EOF`, then `RWLayer of container <id> is unexpectedly nil`).

`RYUK_RECONNECTION_TIMEOUT=2m` (the `framework-integration-test` env in `.github/workflows/ci-v2.yml`, and an exported variable in the `Makefile`) keeps the reaper alive across inter-package gaps of up to two minutes (the observed gap is ~10s); it only takes effect in the process that *creates* the reaper, so it must be set before the first integration binary starts, never from inside a package.

Recognising a recurrence: grep the job log for `wait for reaper` or `Reaper handshake failed: read ack: EOF`. Either string points at the reaper rather than at the container image; confirm the race by checking that the failing package started roughly `RYUK_RECONNECTION_TIMEOUT` after the previous container-using package exited, and that the reaper container's env carries the 2m value. `RWLayer of container <id> is unexpectedly nil` on its own is the daemon reporting a container removed mid-create by anything; it is this race only when the same package's log shows the handshake EOF just before it.

### Oracle: shared container + per-test schema (ADR-020)

The `database/oracle` integration suite provisions exactly one Oracle container per test-binary execution (via package-level `TestMain`) and isolates each test in its own randomly-named schema. This avoids the ~18.5s per-test cold-start that previously pushed the package against the 10-minute Go test timeout.

**Test-isolation contract** — every Oracle integration test:

- **MUST** acquire its schema via `setupTestSchema(t)` (which delegates to `(*containers.OracleContainer).NewSchema(t)`).
- **MUST NOT** create globally-named objects. No `CREATE PUBLIC SYNONYM`, no `CREATE TYPE` outside the test's own schema — fully qualify with the per-test schema name (`CREATE TYPE <schema>.PRODUCT_TYPE`) so `DROP USER ... CASCADE` reclaims them on cleanup.
- **MUST NOT** rely on dropping its own tables/sequences/UDTs by name. `DROP USER ... CASCADE` is the cleanup primitive; tests that try to `DROP TABLE` explicitly will see no-op or already-dropped errors.
- **MAY** opt into `t.Parallel()` as a separate follow-up (not yet done).

Tests that need a *different* `DatabaseConfig` (pool sizing, keep-alive, timezone, connection-string format variants) call `packageOracleContainer().NewSchema(t)` directly and build their own `cfg` from the returned `*containers.OracleSchema` credentials — still on the shared container, just with custom wiring.

**CI/CD:** Integration tests run only on Ubuntu (Docker requirement), unit tests on all platforms

## Mutation Gate

`make mutate` runs mutation testing on the diff only: it computes changed line
ranges vs `git merge-base HEAD origin/main`, runs gremlins per changed package,
and applies this policy to mutants that land on changed lines:

| Status | Verdict | Rationale |
| --- | --- | --- |
| `LIVED` | **fail** (exit 1) | A mutant on a line you wrote survived your tests |
| `NOT COVERED` | warn | Coverage is SonarCloud's gate; no double-gating |
| `TIMED OUT` | warn | Indeterminate — see the timeout ceiling below |
| `KILLED` | pass | |

Additionally, a package for which the engine returns **timeouts and not one
`KILLED` or `LIVED`** fails the gate outright. That state means nothing was
tested, and it is what the ceiling arithmetic below exists to prevent.

Each package is dry-run first (mutants enumerated, none executed). If no mutant
lands on a changed line the package is **skipped and the gate passes** — an
intended pass-without-running, not a vacuous one. gremlins mutates the whole
subtree it is pointed at while the gate judges only changed lines, so a one-line
edit in `./database` would otherwise run 616 mutants to rule on a handful; a
comment-only edit there costs 3.7s instead. The skip is sound because `judge` and
the pre-check share one selector (`changedMutants`), so the set skipped is exactly
the set that would have been discarded.

Knobs (all `?=`, so the environment overrides):

| Variable | Default | Effect |
| --- | --- | --- |
| `MUTATE_CPU` | 2 | Whole-run core budget for `make mutate`. A per-worker share and an effective worker count are derived from it together, so `workers x share` never exceeds it — a budget below `MUTATE_WORKERS` shrinks the worker count rather than handing each worker a core. The share is pinned as `GOMAXPROCS`/`GOFLAGS -p` on every child. Negative values are rejected; `0` opts out. |
| `MUTATE_WORKERS` | 2 | Concurrent gremlins workers for `make mutate`. **Not** a core count — each worker is a full `go test`, whose own parallelism `MUTATE_CPU` is what bounds. |
| `MUTATE_COOLDOWN` | 30s | Pause after each mutated package so the machine sheds heat. Any `time.ParseDuration` string; `0` disables. Skipped after a skipped package and after the last one. |
| `MUTATE_BASELINE_WORKERS` | 2 | Same as `MUTATE_WORKERS`, for the nightly baseline; also bounds peak memory. Unbudgeted — CI runs at full speed. |
| `MUTATE_CEILING_FLOOR` | 30s | Minimum per-mutant ceiling (any `time.ParseDuration` string). |
| `MUTATE_FALLBACK_COEFFICIENT` | 600 | Used only when a package's coefficient cannot be computed. |
| `MUTATE_NO_CACHE` | *(empty)* | Any non-empty value bypasses the result cache below and re-mutates every package in the diff. |
| `MUTATE_GOCACHE_CAP` | 4096 | Cap, in MiB, on the gate's dedicated build cache (below). Env-only — read by `mutatediff`, not declared in the Makefile. `0` removes the cap; unset or unparsable falls back to the default. |

The budget is applied once, on `mutatediff`'s own environment, so gremlins, its
coverage pass, `measureSuite`'s timing passes, and every mutant's `go test` all
inherit the same share. That uniformity is load-bearing rather than tidy: the
per-mutant ceiling divides the real suite by a cache-served replay, so measuring
the two under different budgets would corrupt every ceiling. The `-coefficient`
path is deliberately excluded, because it serves `make mutate-baseline`, whose
mutants run unbudgeted.

### Disk sandbox

The same set-once-on-own-environment seam isolates the gate's disk footprint
(`scripts/mutatediff/sandbox.go`). Mutant builds otherwise flow through the
machine-shared `GOCACHE` — thousands of objects no other build will ever read —
and the only shared-cache remedy, `go clean -cache`, destroys every other
session's warm state. Instead the gate pins `GOCACHE` to a dedicated persistent
cache under the user cache dir (`~/Library/Caches/mutatediff/gocache` on macOS)
and the temp variables (`TMPDIR`/`TMP`/`TEMP`) to a per-run root, so gremlins'
working copies, the report dir, and coverage scratch files all land inside one
tree. Cleanup is deferred in `run`, so it fires on failures the same as on
passes: the per-run root is removed, and the dedicated cache is wiped only when
it exceeds `MUTATE_GOCACHE_CAP`. A run killed with `SIGKILL` skips its defers;
the next run's startup sweep removes orphaned `mutatediff-run-*` roots older
than 24h. The dedicated cache must stay **outside the repo**: gremlins copies
the whole module root per worker with an unfiltered `filepath.Walk`, so an
in-repo cache would be hauled into every working copy. The `-coefficient` and
`-merge` paths are unsandboxed — they serve `make mutate-baseline` in ephemeral
CI runners.

The cooldown gives no relief inside a single package — `mutatediff` drives
gremlins once per package and cannot interrupt its internal mutant loop. For a
package that dominates a run, speeding up its slowest tests remains the lever.

### Result cache

The gate used to have no memory: every invocation re-ran every package in the
branch diff, so amending a one-line fix on a wide branch re-mutated everything
at a full gremlins run plus that package's whole test suite each.
`scripts/mutatediff/cache.go` remembers packages that came back clean, in
`.mutatediff-cache/` at the repo root (git-ignored by the allowlist
`.gitignore`, and removed by `make clean`). Every skip prints
`mutatediff: <pkg> cached PASS (skipped)`, so a cached run is never mistakable
for one that did the work. `MUTATE_NO_CACHE=1` bypasses it entirely.

Because this is a gate, the cache is built to be wrong only in the direction of
doing work twice:

- **PASS only, and only a *fully* clean pass.** A surviving mutant is never
  stored — and neither is a `NOT COVERED` or `TIMED OUT` one, nor a vacuous
  package. A timeout is indeterminate and load-dependent, so freezing one into
  the cache would let a machine under thermal load mint a permanent pass for a
  mutant that was never evaluated. Refusing every non-clean verdict also keeps
  the report honest: a hit contributes nothing to it, so the final verdict lines
  read exactly as they would have without the cache.
- **The key covers everything that can change a verdict.** The package; the
  content of every `.go` file — test files included, since the package's own
  tests are what kill its mutants — and everything under `testdata/`, both in
  the subtree the engine mutates *and* in its transitive first-party dependency
  closure (a mutant in X is killed by X's tests, but X's *behavior* changes when
  a package it imports changes); the pinned engine command, whose `@version` is
  part of the string; `.gremlins.yaml`, which selects the mutator set; `go.mod`
  / `go.sum` / `go.work` / `go.work.sum`; the Go toolchain, `GOOS`/`GOARCH`, and
  the caller's `GOFLAGS`, which can carry `-tags` and decide which files compile
  at all. The closure comes from one `go list -deps -test ./...` per run.
- **The judged line set is compared, not hashed.** A cached pass proves only
  that mutants on the lines judged *at cache time* died. An exact match or a
  narrower set hits; one extra line — a widened hunk, a file the cached run
  never saw — misses. This is the subtlest way to mint a false pass, so it is
  checked line by line rather than by interval arithmetic.
- **Every doubt is a miss.** A read error, a parse error, a schema mismatch, a
  clock that moved, a dependency missing from the package listing, a module
  `go list` cannot resolve: all of them run the package. Nothing in the cache
  can turn a package that should run into one that does not.
- **Entries carry a schema version** and are ignored when it differs.

Deliberately *not* in the key: `MUTATE_WORKERS`, `MUTATE_CPU`, and
`MUTATE_CEILING_FLOOR`. Those move timings only, and any run with a
timing-sensitive verdict is refused by the first rule before it can be stored.

A hub package is not a leaf. Touching `logger/` on a 12-package diff correctly
re-ran the nine packages that import it and kept the three that do not — the
dependency-closure rule doing its job, not a cache miss to debug.

### Timeout ceiling

gremlins derives each mutant's wall-clock ceiling as
`baseline_elapsed × unleash.timeout-coefficient` and enforces it with a hard
deadline that must cover the mutant's whole `go test` invocation — **compiling
and linking the mutated binary, then running the suite**. With the engine's
default coefficient of 3 that ceiling routinely lands below what a single mutant
costs, and every mutant reports `TIMED OUT` without ever being evaluated.

That status is excluded from both of gremlins' published metrics
(`efficacy = KILLED/(KILLED+LIVED)`, `coverage = (KILLED+LIVED)/(KILLED+LIVED+NOT COVERED)`),
so the failure is invisible in the score. The first nightly baseline
(run `30423362023`) hit it in roughly half the repo's packages —
`observability` produced 241 timeouts and zero verdicts, `messaging` 219 — and
published a clean 86.2% efficacy over a run in which 38% of mutants never
produced a verdict. Because the gate treated `TIMED OUT` as a pass, changes to
those packages went through it vacuously.

Both entry points therefore compute the coefficient per package and pass
`--timeout-coefficient`. The arithmetic lives in
`scripts/mutatediff/timeout.go`; `mutatediff -coefficient <pkg>` prints the
value so the baseline loop shares one implementation. Retune the minimum with
`MUTATE_CEILING_FLOOR` (any `time.ParseDuration` string).

**Two timings, not one — this is the whole subtlety.** gremlins' baseline
command omits `-count=1`, so what it measures is a *cache-served replay* of the
suite. A mutant edits the source, so its own run is always a cache miss and pays
the *real* suite. On `./observability` those differ by 70×: 302ms replay versus a
24.7s suite. The coefficient is therefore
`ceil((real_suite + build_budget) / cached_replay)` — 148 for that package,
giving a ~45s ceiling. Scaling a fixed floor by the *suite* instead collapses to
the engine's default 3 (a 1.03s ceiling) for exactly the packages that need it
most, which is the trap the first attempt at this fell into.

Further notes for anyone touching it:

- **Use the flag, not the environment variable.** gremlins documents
  `GREMLINS_UNLEASH_TIMEOUT_COEFFICIENT`, but it is silently ignored:
  `configuration.Get[T]` does an unchecked `viper.Get(k).(T)`, and viper returns
  environment values as **strings**, so the assertion for an `int` fails and
  yields 0 — which the engine reads as unset and replaces with 3. Values in
  `.gremlins.yaml` are parsed as ints and *do* work; the flag is used because it
  can vary per package.
- **Keep the cached measurement passes argv-identical to the engine's.**
  `-timeout` participates in `go test`'s cache key, so adding it to those passes
  warms an entry gremlins never reads — leaving its own coverage run a cache miss
  whose elapsed is the real suite, and multiplying the coefficient by the suite
  instead of the replay (a 60-minute ceiling on `observability`). `vacuous` cannot
  catch that, because it only sees ceilings that are too *tight*. The flag goes on
  the `-count=1` pass only, where caching is already off.
- **A canceled measurement must not become a number.** `CommandContext` kills the
  child on Ctrl-C, and a killed process surfaces as `*exec.ExitError` — the same
  error a red suite produces, which the measurement deliberately tolerates (a red
  suite is never cached, so its three passes still time correctly). Only
  `ctx.Err()` separates the two, so `mutatediff -coefficient` consults it, emits
  nothing, and exits nonzero; otherwise the generous fallback of 600 would reach
  stdout, pass the loop's numeric guard, and make an interrupted run
  indistinguishable from a completed one.
- **Mutation now costs real time.** Those 90 advisory minutes were cheap because
  nothing ran. A package's mutation cost is roughly
  `mutants × (build + suite)`, so a slow suite dominates — `observability`'s
  single 20s `TestNewProviderEnvironmentAwareBatchTimeout` sets the price for all
  268 of its mutants. Speeding up the slowest tests is the lever, not raising the
  ceiling.

A `TIMED OUT` mutant warns rather than passing silently: a mutant that hangs the
code really is caught by the suite, so it should not block a push, but it carries
no verdict and must stay visible. A run with outstanding timeouts does not report
"all mutants killed".

Excluded from scope: `_test.go` files, `testdata/`, `tools/` (separate Go
module), `scripts/` (the wrapper itself — see the operational notes), and
`testing/containers/` (every file there carries `//go:build integration`, so the
package is empty under the default build and the engine fails to gather coverage
rather than reporting zero mutants).
Engine version is pinned via `GREMLINS_VERSION` in the Makefile;
runtime knobs live in `.gremlins.yaml`. The nightly `Mutation Baseline`
workflow runs every package except the excluded `scripts/` in advisory mode
(one engine process per package,
so a crashed engine process only loses that package's shard; the artifact
carries the raw per-package shards alongside the merged report, so even a
failed merge keeps the run's data) and publishes overall efficacy
plus a table of files with LIVED / NOT COVERED findings (first 50 rows) to the
job summary; the JSON artifact is the complete record.

Operational notes: a real `make mutate` run may leave `go.work.sum` modified
(gremlins' own module graph); if the file was clean before the run, restore it
(`git checkout -- go.work.sum`) rather than committing the churn — if it already
carried changes you made, unpick only the run's additions. `scripts/` is excluded from the gate's scope in
code: gremlins v0.5.0 misreports some mutants in the wrapper's nested
`package main` as KILLED (validated during rollout), so a green verdict there
would be untrustworthy — the wrapper's own unit tests are its safety net.
Library packages, which the gate actually polices, verdict correctly.

When the gate fails, strengthen the test so the listed mutant dies (assert the
boundary, the sign, the branch the operator flipped) — never respond by
excluding the file.

## Property-Based Tests

Invariant-heavy packages carry a `<pkg>_properties_test.go` suite built on
[`pgregory.net/rapid`](https://pkg.go.dev/pgregory.net/rapid) — an intentional
exception to the one-`_test.go`-per-source-file convention, alongside
`testhelpers_test.go`.
Current exemplars: `database` (placeholder arity, Oracle reserved-word quoting,
determinism), `config` (InjectInto round-trips, never-panic, env-beats-yaml
precedence), `jose` (seal/open integrity), `multitenant` (resolver contracts,
composite first-match, constructive success paths).

Pattern rules:

- One `rapid.Check` per invariant; name it `Test<Subject><Invariant>Property`.
- Expensive setup (key generation) lives OUTSIDE `rapid.Check`; iterations reuse it.
- State the invariant precisely. Example: tamper-resistance is not "any tamper
  errors" (base64 trailing bits can decode identically) but "Open never succeeds
  with altered plaintext".
- A failing property prints a reproducing seed (`-rapid.seed`); a genuine
  violation is a bug in the code under test — fix that code, never the
  generator or the property.
- Random generators alone can leave success paths vacuously untested (a random
  host virtually never ends in `.example.com`) — pair them with constructive
  draws that build guaranteed-success inputs.

Property suites are ordinary `go test` tests: they run in `make test`, under
`-race`, and count toward coverage.
