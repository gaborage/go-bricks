# ADR-053: Delete `server`'s exported test-timeout constants

- **Status**: Accepted
- **Date**: 2026-08-07
- **Related**: [ADR-052](adr_052_remove_jose_policy_registry.md) (the same disposition, same hop)

## Context

`server/constants.go` declared three exported constants under a section header asserting a
use that never existed:

```go
// Test-Specific Timeouts
//
// These constants are used exclusively in test files for simulating
// timeout scenarios and synchronization.

const (
	TestShortTimeout  = 100 * time.Millisecond
	TestMediumTimeout = 1 * time.Second
	TestLongTimeout   = 5 * time.Second
)
```

Nothing in the repository referenced them — zero hits outside their own declaration, in
production or test code, for their whole life. The header was not describing a convention that
had lapsed; it was describing one that was never adopted.

They are also an odd thing for a framework to export at all. `server` is a `go get`-consumed
package, and test-timing values carry no framework semantics: no production code read them, so
their values constrained nothing.

## Decision

**Delete all three.**

**Adopt them instead**, replacing the ad-hoc literals in `server`'s own tests, was the
alternative worth taking seriously — it would make the header true with no API change. Counted
first, as the issue asked: 14 occurrences of `100 * time.Millisecond`, 10 of `1 * time.Second`,
3 of `5 * time.Second`. But most of the `1 * time.Second` hits are `SlowRequestThreshold`
values, which is a *threshold*, not a timeout, and several of the `100ms` hits are rate-limiter
refill sleeps. Substituting `TestMediumTimeout` there would name the value after something it
is not, making the tests less legible in exchange for retiring an issue. Where the vocabulary is
genuinely wanted, an unexported constant in the test file is the better home.

**Deprecate in place** was rejected for the reason the manifesto gives: obsolete exported paths
are removed, not shimmed.

## Consequences

**Positive.** Three exported symbols leave the public surface along with a comment that
asserted a convention the codebase never followed.

**Negative.** This is apidiff-INCOMPATIBLE. Consumer impact is plausibly zero — test-timing
constants exported from a framework's `server` package are an unlikely dependency — but that
cannot be proven from inside this repo, so it pays the breaking-change ceremony like any other.
Replacement is a literal or a constant in the consumer's own test package; `[C58.2]` in
[migrations.md](migrations.md) records the three values.

**Neutral.** Nothing behavioural changes. No framework code ever read these, so the only
possible effect is on whether code naming them compiles.

## References

- #818 — the report, from the `doc-drift` audit at `bb43eb1`
- `server/constants.go`
- [migrations.md](migrations.md) `[C58.2]`
