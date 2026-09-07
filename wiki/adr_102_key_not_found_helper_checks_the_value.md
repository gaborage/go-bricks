# ADR-102: A Key-Absence Helper Checks the Returned Key, Not Only the Error

**Status:** Accepted
**Date:** 2026-09-06

## Context

`keystore/testing.AssertKeyNotFound` is a shipped helper: consumers import it into
their own tests to assert that a name is a miss on both `PublicKey` and
`PrivateKey`. It judged that miss by error-ness alone — it discarded both returned
keys and asserted only that each error was non-nil:

```go
_, pubErr := ks.PublicKey(name)
require.Error(t, pubErr, "public key %q should not be found", name)

_, privErr := ks.PrivateKey(name)
assert.Error(t, privErr, "private key %q should not be found", name)
```

An `app.KeyStore` is an interface a consumer may implement. A store that returns a
cached key TOGETHER with an error — the natural shape of a refresh failure that
falls back to what it already holds — satisfied every assertion in the helper while
handing the caller the key the test was asserting was absent. The helper's name
claims "not found"; what it verified was "reported an error".

The three sibling helpers in the same file already check their returned value:
`AssertPublicKeyAvailable`, `AssertPrivateKeyAvailable` and `AssertSecretAvailable`
each assert on the key or secret as well as on the error. This one was the outlier.

## Decision

Each lookup's returned key is checked as well as its error. The check sits inside a
nil guard, and a non-nil key fails the test.

The failure message renders the stray key by its dynamic TYPE only — `%T` — and
never the key itself.

**Key material in a test assertion is reported by TYPE, never by value.** The rule
generalizes past this helper: any assertion whose failure message can carry a key, a
secret or a private-key struct renders it with `%T`, because testify prints the operand
of a failed assertion and a test log is archived wherever CI keeps its output. Nothing
enforces this — no check in `.golangci.yml` covers it — so violations are found by
reading, not by tooling:
`git grep -nE '(assert|require)\.(Nil|Empty|NotEmpty|Equal|Len)' -- '*_test.go' '*/testing/*.go'`
lists the assertion forms that render their operand, and each hit is reviewed for a
key-typed one. Both halves of that command matter. The verb set is wider than `Nil`
because every one of those forms prints the value it was handed on failure, so they leak
identically. The pathspec covers `*/testing/*.go` as well as `*_test.go` because the
assertions most worth reading are the SHIPPED helpers, which do not live in `_test.go`
files: a `*_test.go`-only pathspec never scans `keystore/testing/assertions.go` itself,
nor `observability/testing/helpers.go`, `migration/provisioning/testing/assertions.go`,
`inbox/testing/assertions.go`, `outbox/testing/assertions.go`, `cache/testing/` or
`database/testing/`. The shape
follows [ADR-081](adr_081_recovered_panic_values_reported_by_type.md), which made the same TYPE-not-value rule
for recovered panic values.

The public-key check fails with `require.Fail`; the private-key check fails with
`assert.Fail`. That split is not a fresh decision: [ADR-101](adr_101_test_helpers_abort_on_first_failure.md)
settled the positional rule for this helper — the public-key arm aborts because the
private-key assertion that follows would otherwise judge a keystore already known to
be in the wrong state, and the private-key arm records because it is the last
statement and invalidates nothing. The new value checks inherit the verb of the
position they occupy.

The exported signature does not move. `app.KeyStore`'s interface documentation
gains one sentence stating that a lookup returning an error returns no key, so the
contract the helper enforces is written where implementers read it.

## Alternatives

**Document the gap and check nothing.** Disclose in the doc comment that the helper
reads error-ness only, and leave the assertion as it was. Rejected: the three
sibling helpers in the same file all check their returned value, so this is the
outlier being brought into line, not a new burden being invented. A documented hole
in a shipped assertion is a hole that lands in someone else's test run.

**`assert.Nil(t, key)`.** The obvious spelling, and it leaks. On failure testify
renders the value: an `*rsa.PrivateKey` prints roughly 3 KB including the decimal
digits of `D` and of `Primes`. That is key material in the test output and in
whatever CI log archives it. Rejected for the leak — not for any lint rule; no nil
check is configured in this repository's `.golangci.yml`, and nothing here is a
linter ratchet.

**`require.Nil(t, key)`.** Rejected for the same rendering leak, and additionally
because using it on the private-key arm would abort where ADR-101 decided to record.

**Pick the verbs afresh for the value checks.** Rejected: ADR-101 already settled
the positional split for this helper, and re-arguing it per assertion would let the
two arms drift apart for no reason a reader could reconstruct.

## Consequences

A consumer store that returned a cached key alongside an error now fails the helper
where it used to pass. The fix is to fix the store so an error path returns no key,
or — if the cached-key fallback is deliberate — to assert that behaviour with your
own direct `PublicKey`/`PrivateKey` call. Never by wrapping the helper or routing
around it: the helper's contract is now "absent", and a wrapper that restores the
old reading re-hides the same gap under a local name.

The passing path is unchanged. When the key is genuinely absent and the store
returns `nil`, both lookups error, both keys are nil, and every assertion passes as
before.

In-repo blast radius is zero: the framework's own keystore and its `MockKeyStore`
return `nil` on every error path, so no test in this tree changes behaviour.

The TYPE-not-value rule was swept across the framework's own key-loading tests in
`keystore`, `internal/keymaterial`, `internal/secretfile` and `internal/sealcli`, where
every absent-material assertion now reports the operand's type or byte length (#1493).

`Secret` keeps the gap this ADR closes for keys. `AssertKeyNotFound` consults only
`PublicKey` and `PrivateKey`, and there is no absence helper for `Secret` at all, so a
name this helper reports as a miss may still return a live secret. Extending absence
coverage to `Secret` is deliberately NOT covered here; it is tracked the way ADR-101
tracked this very gap, as a note in the record until someone closes it.

## References

- #1457
- [ADR-101](adr_101_test_helpers_abort_on_first_failure.md) — the positional verb rule this inherits
- [migrations.md](migrations.md) `[C64.6]`
- `keystore/testing/assertions.go`, `app/module.go` (`KeyStore` interface doc)
