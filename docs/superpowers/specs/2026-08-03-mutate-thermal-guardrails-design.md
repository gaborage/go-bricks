# Thermal guardrails for `make mutate`

**Date:** 2026-08-03
**Status:** approved, ready for implementation plan

## Problem

`make mutate` repeatedly drives this development machine (Apple M4 Pro, 8
performance + 4 efficiency cores) to dangerous sustained temperatures. The
existing `MUTATE_WORKERS ?= 2` knob does not bound CPU use, despite a Makefile
comment implying it does.

Three parallelism multipliers compose, and only the first is bounded:

1. `MUTATE_WORKERS` gremlins workers, each of which shells out a full `go test`;
2. `go build -p N` inside each `go test`, where `N` defaults to `NumCPU` (12);
3. the test binary's own `GOMAXPROCS` (12), which also sets the `t.Parallel()`
   limit.

Two workers therefore admit roughly 24-way parallelism against 12 cores.

The load is also *sustained*, which is what actually heat-soaks the chassis. Per
`wiki/testing.md#mutation-gate`, a package's cost is approximately
`mutants x (build + suite)`; `./observability` alone is 268 mutants against a
~25s suite.

## Scope

`make mutate` is local-only. `make mutate-baseline` runs solely in the nightly
`mutation-nightly.yml` workflow, on a 4-vCPU hosted runner where the binding
constraint is memory, not heat.

The guardrail therefore applies to `make mutate` alone. CI keeps running at full
speed.

## Design

### 1. Knobs

| Variable | Default | Effect |
|---|---|---|
| `MUTATE_CPU` | `4` | Total core budget for the local gate |
| `MUTATE_WORKERS` | `2` | Concurrent gremlins workers (unchanged) |
| `MUTATE_COOLDOWN` | `30s` | Pause after each package's mutants |

Derived: `perChild = max(1, MUTATE_CPU / MUTATE_WORKERS)`, which is `2` at the
defaults. All three use `?=`, so a single run can override any of them without
editing the Makefile.

`MUTATE_CPU` and `MUTATE_COOLDOWN` reach `scripts/mutatediff` as the `-cpu` and
`-cooldown` flags. `MUTATE_COOLDOWN` accepts any `time.ParseDuration` string,
matching `MUTATE_CEILING_FLOOR`'s existing convention.

### 2. Enforcement point

Inside `run()` only, before the package loop, `mutatediff` sets `GOMAXPROCS` to
`perChild` and appends `-p=<perChild>` to `GOFLAGS` in its own process
environment. Every descendant inherits it: `go run gremlins`, gremlins' coverage
pass, `measureSuite`'s three timing passes, and each mutant's `go test`.

Setting `GOMAXPROCS` via `os.Setenv` after start does not change `mutatediff`'s
own runtime, which reads the variable at init — it affects children only.
`mutatediff` is I/O-bound regardless.

Two deliberate choices:

**Global environment, not per-`exec.Cmd`.** Uniformity is a correctness
property here, not a matter of tidiness. The per-mutant ceiling derives from
`ceil((real_suite + build_budget) / cached_replay)`; if the coefficient
measurement observes a different machine than the mutants run on, every ceiling
is wrong. Setting the budget once makes that desync impossible by construction,
and the arithmetic self-corrects — a slower budgeted suite yields a
proportionally larger coefficient, so ceilings scale with actual cost.

`wiki/testing.md#timeout-ceiling` records a prior bug in exactly this area:
adding `-timeout` to the cached measurement passes warmed a cache entry gremlins
never read, multiplying the coefficient by the suite instead of the replay (a
60-minute ceiling on `./observability`). Uniform inheritance is what keeps this
change from re-opening that trap.

**Not on the `-coefficient` path.** `printCoefficient` serves
`make mutate-baseline`, which invokes gremlins directly and unbudgeted.
Budgeting that measurement while its mutants ran at full speed would inflate
every ceiling CI computes. Gating the budget on `run()` keeps CI at full speed.
`mergeShards` is likewise untouched.

`GOFLAGS` is appended to, never replaced, so a caller-supplied value survives.

### 3. Cooldown

After a package's engine run returns, `mutatediff` sleeps for
`MUTATE_COOLDOWN`. The sleep is suppressed in two cases:

- the package had no mutants on changed lines, so it paid only a dry-run
  coverage pass, not a mutant loop;
- the package is the last one, so there is no subsequent work to protect.

The sleep is injected as a function value so tests can assert its placement
without waiting.

**Known limitation.** A typical diff touches one to three packages, and one
usually dominates. Within a single package the core budget is the only active
guardrail: `mutatediff` drives gremlins once per package and cannot interrupt
its internal mutant loop. The cooldown is real relief between packages, not a
duty cycle. For a single dominant hot package the wiki's own advice still
stands — speeding up the slowest tests is the lever, not this guardrail.

### 4. Reporting

`make mutate` prints its effective budget once, before the first package:

```
mutatediff: CPU budget 4 cores (2 workers x 2), 30s cooldown between packages
```

This makes an overridden run self-documenting in a scrollback or a pasted
transcript.

## Out of scope

- `make mutate-baseline` and `mutation-nightly.yml` — unchanged, full speed.
- `.gremlins.yaml`'s `workers: 4`, the fallback for a bare `gremlins unleash`.
- `taskpolicy -b` / background QoS (efficiency-core exile). Considered and
  rejected: strongest thermal guarantee available, but an estimated 3-5x
  wall-clock cost was not worth it against a 4-core P-core budget.
- Thermal sensing and closed-loop governing. `pmset -g therm` records nothing on
  this machine, and `powermetrics` requires sudo, which does not belong in a
  `make` target.
- Wall-clock abort ceilings.
- `make check` and `make test` — short bursts, not the reported problem.

No ADR: this is development tooling with no consumer-facing API surface.

## Testing

- Derivation table test: `cpu`/`workers` to `perChild`, including the clamp at
  1 (e.g. `MUTATE_CPU=1` with `MUTATE_WORKERS=2`), and that appending to
  `GOFLAGS` preserves a pre-existing value.
- Cooldown placement test: a fake sleep records its calls; assert no sleep
  follows the last package and none follows a skipped package.

Both are unit tests in `scripts/mutatediff`, consistent with the package's
existing `*_test.go` layout and the repo's camelCase test-naming rule.

## Documentation

- `wiki/testing.md` knobs table: add `MUTATE_CPU` and `MUTATE_COOLDOWN`, and
  **correct** the `MUTATE_WORKERS` row, which currently reads as a core count.
- Makefile comments above the same knobs, for the same reason.
