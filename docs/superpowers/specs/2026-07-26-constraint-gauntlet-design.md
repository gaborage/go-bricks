# Constraint Gauntlet: Diff-Scoped Mutation Gate + Property-Based Testing

**Date:** 2026-07-26
**Status:** Approved design, pending implementation plan

## Motivation

The goal is to harden the automated constraint surface around agent-written code so
that confidence comes from gates, not from re-reading diffs. The repo already has
strong machine gates (`make check`: fmt + lint + race tests + alloc guards + vuln +
gosec; SonarCloud 80% coverage; apidiff) and agent-judgment gates (/simplify,
/security-audit, /code-review). The two verification classes that exist only as
manual discipline are **mutation testing** (test strength) and **property-based
testing** (invariant coverage). This design automates the first and seeds the second.

Explicitly out of scope (considered, rejected for now): adversarial test authorship
(separate test-writing agent) and a Gherkin/behavioral spec layer.

## Decisions

| Question | Decision |
|---|---|
| Goal | Harden gauntlet; human/agent review stays the final gate |
| Layers | Mutation testing gate + property-based testing exemplar suites |
| Mutation enforcement | Diff-scoped local gate (`make mutate`) + advisory nightly full-repo baseline |
| Mutation engine | gremlins (`go-gremlins/gremlins`) + thin diff wrapper we own; fallback: avito-tech/go-mutesting fork |
| PBT scope | Exemplar suites on hot spots, no enforcement machinery |
| PBT library | `pgregory.net/rapid` (test-only dependency) |

## S1: Diff-scoped mutation gate (`make mutate`)

A small Go program at `scripts/mutatediff/main.go` (root module, stdlib-only, so no
new go.work module and no go.sum churn beyond rapid):

1. Compute `git merge-base HEAD origin/main`; collect changed non-test `.go` files
   and their changed line ranges (exclude `_test.go`, `testdata/`, `wiki/`).
2. Map changed files to packages; run gremlins per changed package with JSON output.
   Config lives in `.gremlins.yaml`: operator set, timeout coefficient, workers,
   integration build tags excluded.
3. Filter reported mutants to those whose position intersects a changed line range.
4. Verdict policy: **LIVED = fail** (exit non-zero, list survivors with file:line and
   operator), **NOT_COVERED = warn** (SonarCloud owns coverage; no double gate),
   **TIMED_OUT = killed** (the mutant hung the code and tests noticed via timeout).

Placement: last machine gate before push, after the agent gates settle, so the happy
path runs mutation once per push. Runnable standalone at any time.

**Step 1 of implementation is a compatibility spike:** gremlins is stale (~2023);
verify it runs on Go 1.26 against one package (e.g. `config`). If broken, switch the
engine to the avito-tech/go-mutesting fork behind the same wrapper interface — the
wrapper's diff-scoping and verdict logic are engine-agnostic by construction.

## S2: Nightly full-repo baseline

`.github/workflows/mutation-nightly.yml`: cron schedule, ubuntu-latest only,
full-repo gremlins run (coverage-guided). Advisory — never blocks anything. Outputs:
JSON report artifact + GitHub step-summary table of per-file LIVED / NOT COVERED
counts (gremlins reports file-level results; the JSON artifact is the full record).
Job timeout 4h. Action pinning follows repo policy (tag-pin first-party, SHA-pin
third-party, `persist-credentials: false` on read-only jobs).

## S3: Property-based exemplar suites

Add `pgregory.net/rapid` as a test-only dependency (root `go.mod`; run
`go mod tidy` in root **and** `tools/migration` afterward — go.work sync gotcha).

One file per package, named `<pkg>_properties_test.go` — a documented exception to
the source-to-test 1:1 naming rule, alongside the existing `testhelpers_test.go`
exception. Test functions use the mandatory camelCase convention.

| Package | Invariants |
|---|---|
| `database` (query builder) | placeholder count == arg count; Oracle reserved words always quoted; PG placeholders `$1..$n` sequential; same input twice → identical SQL |
| `config` | `InjectInto` never panics on arbitrary values; env > yaml > default priority holds; `time.Duration` and `[]string` round-trip |
| `jose` | random payload sign+encrypt → decrypt+verify round-trips; any single-byte ciphertext tamper is rejected |
| `multitenant` | arbitrary host/path/header never panics; resolver returns valid tenant or error; composite honors first-match order |

These serve as the pattern library for future property tests; no CI presence check.

## S4: Documentation and self-verification

- The wrapper's line-range intersection and verdict logic get their own unit tests
  (the gauntlet guards itself).
- `CLAUDE.md`: add `make mutate` to Quick Reference; note its position in the
  pre-push sequence.
- `wiki/testing.md`: new sections for the mutation gate (policy, config, how to read
  survivor output) and rapid property-test patterns.

## Risks

- **Engine staleness** — mitigated by the spike-first step and the engine-agnostic
  wrapper seam.
- **Runtime cost** — diff scoping + coverage-guided selection bound the local run to
  changed packages; nightly absorbs the full-repo cost.
- **Flaky mutants as noise** — verdict policy is deterministic (LIVED only fails on
  changed lines); rapid failures print a reproducible seed.

## Success criteria

1. `make mutate` on a branch with a deliberately weakened test fails, naming the
   surviving mutant at file:line.
2. `make mutate` on a no-op diff exits clean in seconds.
3. Nightly workflow publishes a per-file score table plus the JSON report artifact.
4. Four property suites run green under `-race` in normal `make test`.
