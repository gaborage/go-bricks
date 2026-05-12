# ADR-020: Shared Oracle Container for Integration Tests with Per-Test Schema Isolation

**Status:** Accepted
**Date:** 2026-05-12
**Related issues:** [#402](https://github.com/gaborage/go-bricks/issues/402) (this decision — investigation/exploration), follow-up `kind/feature` implementation issue to be opened on acceptance.

## Context

The `framework-integration-test` job is the dominant cost in CI today and the most flake-prone. Doc-only PRs [#398](https://github.com/gaborage/go-bricks/pull/398) and [#399](https://github.com/gaborage/go-bricks/pull/399) both timed out on `database/oracle` at the 10-minute Go test ceiling on 2026-05-12, requiring manual `gh run rerun --failed`. Even successful runs sit right against that ceiling. PR [#401](https://github.com/gaborage/go-bricks/pull/401) addresses doc-only PRs by skipping the integration job when no `.go` files change, but the flake class remains for any PR that *does* touch `.go`.

### Measurement

Pulled per-package timing from a representative successful run (`framework-integration-test` job 25748764278, 2026-05-12):

| Package | Wall time | Share of suite |
|---|---|---|
| `database/oracle` | **572.131s** (9m32s) | **~80%** of integration time |
| `messaging` | 149.458s | ~21% |
| `database/postgresql` | 71.618s | ~10% |
| `cache/redis` | 18.898s | ~3% |
| All others combined | ~80s | ~11% |

Sampled total integration-job wall time across 7 successful runs (same day): **median 11m40s, range 11m19s–12m01s**. The Go test default timeout is 10m, applied per-package — when `database/oracle` slips past 10m it panics with the observed `panic: test timed out after 10m0s`.

### Root cause

Counting calls in `database/oracle/connection_integration_test.go`:

- `setupTestContainer(t)` called from **23** test functions — each invocation calls `containers.MustStartOracleContainer(...).WithCleanup(t)`.
- `containers.MustStartOracleContainer` called directly from **8** more test functions that skip the helper.
- Total: **31 cold container starts** per package execution.

Zero tests call `t.Parallel()`, so all 31 starts run sequentially. At ~18.5s per `gvenzl/oracle-free:23-slim` cold-start on a GHA `ubuntu-24.04` runner, that's 31 × 18.5 = **~573s**, matching the measured 572.131s to within rounding. The implied test-logic budget is ~1s aggregated across all 31 tests — so **~99.8% of the current Oracle suite wall time is container startup**, with the actual SQL assertions running in rounding error. (The "~100%" framing in the rest of this ADR refers to that ~99.8% — see the Safety margin note in §Decision for why the projected post-refactor wall time is still measured in minutes, not seconds.)

For comparison, `database/postgresql` uses the exact same anti-pattern (20 `setupTestContainer` calls) but Postgres cold-starts in ~3s, so 20 × 3s ≈ 60s — closely matching the measured 71.6s. **PostgreSQL is fine because Postgres is fast to start, not because the test design is different.** This decision therefore scopes to Oracle only; PG does not need the same treatment.

### What we are deciding

How to bring the Oracle integration-test wall time down while keeping the test architecture honest. The eight directions enumerated in [#402](https://github.com/gaborage/go-bricks/issues/402) cluster into three groups, evaluated against the measurement:

| Group | Directions | Math against root cause |
|---|---|---|
| **A. Reduce per-start cost** | Slimmer image; pre-bake schemas; cache Docker layer | We already use `gvenzl/oracle-free:23-slim` and pre-pull in CI. The ~18.5s is mostly Oracle's *startup* (listener boot, PDB open), not image pull. Optimistic ceiling: maybe -3s per start = ~93s suite-wide. Doesn't solve the problem. |
| **B. Mask the symptom** | Bump `-timeout=20m`; auto-retry on timeout; isolate Oracle in its own CI job | Eliminates the panic but leaves the cost. Hides the systemic drift that put us at 10m in the first place. Wins back nothing in developer-laptop time. |
| **C. Eliminate redundant starts** | Share one container across the package via `TestMain` / testcontainers reuse; aggressive `t.Parallel()` | This is where the math actually moves. Going from 31 starts to 1 drops the floor to ~80s (1 × 18.5s container + 31 × ~2s schema lifecycle + ~1s test logic). The conservative headline estimate of **~250s (~55% reduction)** carries a deliberate ~3× safety margin over that floor — see Safety margin note in §Decision. Either number clears the issue's 50% target. |

Aggressive `t.Parallel()` is additive but secondary: with one shared container, parallel tests would only need fresh schemas, not fresh containers. We list it as a future enhancement, not part of this ADR's scope.

## Options Considered

### Option 1: Bump `-timeout` to 20m, leave architecture alone (Rejected)

Tiny effort, eliminates timeout-class failures immediately. Rejected because it doesn't address the root cause: developer-laptop runs already take 12–15 minutes on slower Docker setups, and CI cost scales linearly with the redundant container starts. Masking the symptom guarantees we revisit this in three months when the suite drifts past 15 minutes.

We *will* apply this **as an interim safeguard** in the implementation PR — the refactor is the real fix, and a temporary timeout bump lets the refactor land without an ongoing flake window. The interim timeout is reverted (or kept at a sensible new ceiling like 15m) once the refactor proves out.

### Option 2: testcontainers `WithReuse` flag + identifier label (Rejected)

testcontainers-go supports a `Reuse: true` flag that, combined with `Name`, lets multiple processes (or repeat runs) attach to the same running container. On a developer laptop this gives instant re-runs; in CI each fresh runner is a new VM so reuse provides no benefit there.

Rejected because:
- CI is where the flake hurts most, and reuse provides nothing on fresh runners.
- The `Reuse` flag interacts oddly with testcontainers' Ryuk reaper — a reused container outlives the test session that "owned" it, and Ryuk's container-cleanup heuristics depend on session labels we'd need to manage by hand.
- Containers persisting across test runs can mask state-leak bugs that a clean baseline would catch (e.g., a test that forgets to drop a sequence still passes the next run because the sequence already exists).

The local-developer benefit is real, but we can capture it more cleanly via a `GO_BRICKS_ORACLE_CONTAINER` env var that points existing tests at a developer-managed long-lived container (documented in the troubleshooting wiki, not in code).

### Option 3: One container per package via `TestMain` + per-test schema isolation (Chosen)

`database/oracle/main_test.go` provisions exactly one Oracle container in `TestMain`, exposes the connection details to package tests via package-level state, and terminates the container in the `m.Run()` post-step. Every existing `setupTestContainer(t)` call site is rewritten to:

1. Generate a random schema/user name (e.g., `gbtest_<random suffix>`).
2. `CREATE USER ... IDENTIFIED BY ...; GRANT CONNECT, RESOURCE, CREATE TYPE, CREATE PROCEDURE TO ...;`.
3. Build a `*Connection` against that schema.
4. Register `t.Cleanup` to `DROP USER ... CASCADE` (which removes all objects the test created — tables, sequences, UDTs, procedures).

This satisfies test isolation (each test sees a virgin schema) without paying for container provisioning. Oracle user/schema creation is ~50–200ms on a warm container; `DROP USER ... CASCADE` on a populated schema is typically ~500ms–2s depending on object count. Floor math is therefore ~18.5s (one container) + 31 × ~2s (schema create + drop) + ~1s (test logic) = **~80s** suite-wide. The headline estimate of ~250s elsewhere in this ADR is intentionally ~3× this floor — see Safety margin below.

**Chosen because:**

1. **The math matches the diagnosis.** ~99.8% of Oracle suite wall time is currently container startup (572s measured / 573s computed); eliminating 30 of 31 startups is the only lever proportional to the cost.
2. **Test isolation contract becomes explicit.** Today, isolation is implicit ("each test gets a fresh container"). Under this ADR, isolation is explicit and grep-able: every test must operate inside its assigned schema and `DROP USER ... CASCADE` is the contract for cleanup. This is *better*, not worse, for catching state-leak bugs.
3. **Compatible with `t.Parallel()` as a future step.** Once schemas are namespaced, individual tests can opt into `t.Parallel()` without colliding on global table/UDT names like `PRODUCT_TYPE` or `test_seq`. We don't ship parallelism in the initial implementation — first prove the schema-per-test refactor is stable, then enable parallel.
4. **Doesn't change CI infrastructure.** No new jobs, no caching layers, no Docker-layer-cache plumbing. The change is contained to `database/oracle/*_test.go` plus a small helper in `testing/containers/oracle.go` for the `CREATE USER` / `DROP USER` lifecycle.
5. **Honest about local DX.** Developer-laptop runs see the same one-container speedup. Estimated local Oracle suite time drops from 12–15 minutes to 3–5 minutes.

## Decision

For the `database/oracle` integration suite:

1. **Provision exactly one Oracle container per test binary execution** in a package-level `TestMain` (`database/oracle/integration_main_test.go`, build tag `//go:build integration`).
2. **Each integration test creates a randomized Oracle user/schema in `t.Cleanup`-managed isolation.** A new helper in `testing/containers/oracle.go` — `(*OracleContainer).NewSchema(t)` — returns a `*Connection` bound to a fresh schema and registers cleanup to drop the user with `CASCADE`.
3. **Rename the legacy `setupTestContainer(t)` helper** to `setupTestSchema(t)`. The signature stays the same (`(*Connection, context.Context)`), but the implementation now acquires from the package container.
4. **Keep the existing public surface in `testing/containers/oracle.go`** — third-party consumers of `MustStartOracleContainer` continue to work. The shared-container pattern is a `database/oracle`-specific implementation detail, not a global testing convention.
5. **Apply a one-line interim timeout safeguard** (`go test -timeout 15m -tags=integration ./database/oracle/...` in the implementation PR) until the refactor has stabilized across at least 20 consecutive green runs.
6. **PostgreSQL is explicitly out of scope.** The PG suite is fast enough today; introducing the same pattern there would be churn without measurable benefit.

### Test isolation contract (explicit)

Under this ADR, every integration test in `database/oracle`:

- **MUST** acquire its schema via `setupTestSchema(t)` (which calls `OracleContainer.NewSchema(t)`).
- **MUST NOT** create globally-named objects (no `CREATE PUBLIC SYNONYM`, no `CREATE TYPE` outside the test's own schema). Tests that previously created `PRODUCT_TYPE`, `ORDER_ITEM`, `SIMPLE_TYPE`, etc., at the application-user level will be migrated to fully-qualified `CREATE TYPE <test_schema>.PRODUCT_TYPE`.
- **MUST NOT** rely on tearing down its own tables/sequences/UDTs by name. `DROP USER ... CASCADE` is the contract; tests that try to `DROP TABLE test_seq` explicitly will see a no-op or already-dropped error.
- **MAY** opt into `t.Parallel()` once the refactor is stable. Initial implementation does **not** enable parallel; that lands as a separate PR after the schema-per-test pattern proves stable.

### Safety margin in the suite-time estimate

The floor math (1 container + 31 schemas + test logic) lands at **~80s**. The headline estimate of **~250s** and the acceptance bar of **≤ 286s** are deliberately ~3× that floor. The margin covers:

- **`DROP USER ... CASCADE` variance.** On schemas with many objects (the UDT tests create types, collection types, tables, and procedures), Oracle's recursive drop can spike to 5–10s under garbage-collection pressure. We don't have a clean sample of this in CI yet, so we plan for the high end.
- **CI runner variability.** GHA `ubuntu-24.04` runners are not bare-metal; nested-Docker IO contention has been observed to inflate Oracle ops by 2–4× during noisy-neighbor windows. The current `cache/redis` integration time (3–19s across runs) shows this variance directly.
- **First-test penalty.** The one remaining container start is on the critical path, not amortized — if it lands on a slow runner at ~30s instead of ~18.5s, that single delta consumes ~15% of the floor estimate.
- **Unmeasured per-test fixed costs** (logger init, connection-pool warmup, the `Health(ctx)` ping `setupTestContainer` performs today) that I rolled into "~1s test logic" without per-test instrumentation.

Recording these explicitly so future readers don't see "80s floor vs 250s estimate" as an unexplained pessimism. The implementation PR will measure actual wall time across a 10-run sample; if results cluster near 80s, that's a win and the ADR's headline number gets revised in a follow-up edit, not retroactively.

### Acceptance bar for implementation issue

- Median Oracle suite wall time **≤ 286s** (50% reduction from current 572s baseline) over a 10-run sample. This is the *floor-of-acceptable*; the target is closer to ~80–150s.
- Zero `panic: test timed out` flakes across 20 consecutive runs of `framework-integration-test`.
- No regression in coverage attributable to test consolidation (SonarCloud delta).

## Consequences

**Pros:**

- At least ~55% reduction in Oracle suite wall time (572s → ~250s conservative estimate; floor math points to ~80s). Either result closes the 10-minute timeout cliff with comfortable headroom.
- Local developer iteration time drops dramatically (12–15 minutes → ~3–5 minutes).
- Test isolation becomes explicit and grep-able rather than implicit.
- No new CI infrastructure, no Docker-image maintenance, no testcontainers reuse flag quirks.
- Unblocks a future `t.Parallel()` rollout for additional wins.

**Cons:**

- Every existing Oracle integration test must be touched in the implementation PR. That's ~31 test functions across one file (`connection_integration_test.go`); mechanical but unavoidable.
- Tests that today create UDTs at the application-user scope (`CREATE TYPE PRODUCT_TYPE`) must be rewritten to fully-qualify the schema (`CREATE TYPE <schema>.PRODUCT_TYPE`). Six tests in the UDT block (`TestOracleUDTCollectionIntegration` and siblings) carry this churn.
- A poorly-written test that hangs no longer takes the entire suite down via container-startup timeout; it now hangs against an existing container and would time out at the suite level instead. We address this by keeping the 15m suite-level timeout as a final safety net.
- The `setupTestContainer` → `setupTestSchema` rename surfaces in PR diffs as a behavioural change; reviewers should be reminded that the public contract (a working `*Connection` bound to a clean schema) is preserved.

## Migration

No breaking changes to the framework's public testing surface. `containers.MustStartOracleContainer` and friends remain — third-party consumers of `testing/containers/` are unaffected. The new `(*OracleContainer).NewSchema(t)` helper is additive.

In-tree changes are localized to:

- `testing/containers/oracle.go` — add `NewSchema(t)` helper.
- `database/oracle/integration_main_test.go` (new) — package-level `TestMain` provisions the shared container.
- `database/oracle/connection_integration_test.go` — `setupTestContainer` becomes `setupTestSchema`; 8 direct `MustStartOracleContainer` call sites are migrated to use the package container; UDT tests fully-qualify their type/object owners.
- `.github/workflows/ci-v2.yml` — Oracle test invocation gets `-timeout 15m` as an interim safety net for the rollout window. Reverted (or held at 15m) once stability is proven.

## Stakeholder check

This decision is internal to test infrastructure and affects no consumers of the published framework. No external stakeholder coordination is required.

## References

- Issue [#402](https://github.com/gaborage/go-bricks/issues/402) — investigation tracker (this decision).
- PR [#401](https://github.com/gaborage/go-bricks/pull/401) — partial mitigation: skip integration job when no `.go` files change.
- [wiki/testing.md](testing.md) — integration testing conventions; updated alongside the implementation PR with the new schema-per-test pattern.
- [wiki/troubleshooting.md](troubleshooting.md) — gets a section documenting the developer-laptop `GO_BRICKS_ORACLE_CONTAINER` env-var pattern.
- [testcontainers-go Reuse pattern](https://golang.testcontainers.org/features/reuse/) — Option 2's mechanism, rejected for the reasons above; reproduced here for future reviewers.
