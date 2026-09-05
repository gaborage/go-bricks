# ADR-101: A Shipped Test Helper Aborts on the Failure That Invalidates the Rest

**Status:** Accepted
**Date:** 2026-09-05

## Context

`keystore/testing` ships assertion helpers that consumers import into their own
tests. `AssertKeyNotFound` asserted twice in sequence:

```go
_, pubErr := ks.PublicKey(name)
assert.Error(t, pubErr, "public key %q should not be found", name)

_, privErr := ks.PrivateKey(name)
assert.Error(t, privErr, "private key %q should not be found", name)
```

`assert` records a failure and continues. So when the public key is
unexpectedly PRESENT — the exact condition the helper exists to catch — the
helper carries on and judges the private key of a keystore it already knows is
in the wrong state. Whatever the second assertion reports is noise: a passing
private-key lookup reads as partial success in the output, and a failing one
buries the finding that matters under a second message.

testifylint's `require-error` check flags the pattern generically; #1092 is the
work of adopting that linter, and the check is not enabled in `.golangci.yml`
yet. The reason to act on this instance ahead of the tooling is specific: the
helper is consumer-facing, so the misleading output lands in someone else's test
run, where the keystore they are debugging is not one they can read from this
repository.

## Decision

The public-key check uses `require.Error`. An unexpectedly found public key
aborts the caller's test at that line.

The private-key check keeps `assert.Error`. It is the last statement, so there
is nothing left for it to invalidate, and aborting there would buy nothing.

The exported signature does not change. `AssertKeyNotFound(t *testing.T, …)`
delegates to an unexported `assertKeyNotFound` taking a `require.TestingT` +
`Helper` interface, which is how the helper's own failure path becomes
observable to a recording double. That is the shape `observability/testing.TB`
and `messaging/internal/lanecontract.T` already use. `cache/testing` does the
same delegation for its counters, but its `testReporter` is `Helper` + `Errorf`
only — it cannot express an abort, so it is the pattern's precedent and not this
interface's.

## Alternatives

**Keep `assert` and silence the linter with `//nolint:testifylint`.** Cheapest,
and wrong for a shipped helper: it would record that we looked at the pattern
and decided the misleading output was acceptable in consumers' test runs. A
nolint is the right answer where the flagged shape is the helper's deliberate
contract, and #1092's package PRs use it for such cases; it is not the right
answer for an assertion whose successor is meaningless once it fails.

**Split into `AssertPublicKeyNotFound` and `AssertPrivateKeyNotFound`.** Gives
callers the choice and breaks nothing, but it is a breaking API expansion that
replaces one helper with two at consumers' call sites — this repository has none
outside the helper's own tests — and the common case, assert both and care about
the first, then needs two lines and gains nothing.

## Consequences

A consumer whose test deliberately continues after an unexpectedly found public
key stops at that assertion instead. That test was reporting on a keystore in a
state it did not expect; the change makes the first failure the last line rather
than one of several.

When the key is genuinely absent — the passing path, and every existing green
test — nothing changes: both lookups return errors and both assertions pass.

What this does not fix, and #1457 tracks: the helper judges absence purely by
error-ness, so a KeyStore returning a key ALONGSIDE an error still satisfies it.

## References

- #1092 (testifylint adoption; `require-error`)
- [migrations.md](migrations.md) `[C64.4]`
- `cache/testing/assertions.go` — the delegate + recording-double precedent
