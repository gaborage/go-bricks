# ADR-048: `/ready` Error Sanitization Is the Default, Not an Opt-In

**Status:** Accepted
**Date:** 2026-08-05
**Supersedes in part:** [ADR-046](adr_046_cache_readiness_strict_default.md) — it keeps
ADR-046's `PublicErr` seam and its `cache.critical` decision untouched, and reverses only
that ADR's "sanitization is per-probe, not a blanket rewrite of the shared branch" scoping.

## Context

ADR-046 introduced `HealthStatus.PublicErr` so the cache probe could serve a fixed
`cache unavailable` string on `/ready`'s `503` instead of a connector error naming the
Redis host, port and resolved dial IP. `[C57.1]` reused that seam for the database probe,
whose pgconn error renders ``user=<username> database=<dbname>`` plus the resolved
`host:port`. Both were per-probe fixes, and ADR-046 said so explicitly: the sanitization
was "per-probe, not a blanket rewrite of the shared branch".

That left the shared branch as written:

```go
func publicProbeError(result *HealthStatus) string {
	if result.PublicErr != "" {
		return result.PublicErr
	}
	return result.Err.Error()
}
```

The safe path was opt-in. Two probes each had to remember to declare a constant, and a
third critical probe added later — a vault probe, a downstream-dependency probe, a
consumer's own `Prober` — renders its raw error into an unauthenticated response body by
doing nothing at all. `/ready` carries no authentication and no IP allowlist by design,
because load balancers must reach it, so "does this probe leak" is decided by whether one
author remembered one field.

Nothing enforced it. The `Prober` doc comment stated the contract in prose. `[C57.1]`
added `TestCreateHealthProbesCriticalProbesDeclarePublicError`, which asserted every
critical probe declared a non-empty `PublicErr` — a test, not a constraint, and one that
only covered the probes `createHealthProbes` wires. A consumer-implemented `Prober` was
outside its reach entirely (a future-facing gap, not a present one: probe registration is
framework-internal today, so nothing consumer-written reaches `readyCheck` yet — `Prober`
is exported, which is what makes the gap reachable once a registration API lands).

## Decision

**An empty `PublicErr` synthesizes `"<name> unavailable"`; the raw error is never
rendered.** `publicProbeError` no longer reads `result.Err` at all:

```go
func publicProbeError(result *HealthStatus) string {
	if result.PublicErr != "" {
		return result.PublicErr
	}
	return result.Name + " unavailable"
}
```

`PublicErr` inverts from a requirement into an override: a probe that wants wording other
than the synthesized default sets it, and everything else is safe by omission. The
disclosure decision now lives in one function rather than at every probe constructor.

**This emits byte-identical output today.** `componentDatabase` and `componentCache` are
the literal strings `"database"` and `"cache"`, so the synthesized defaults are exactly
`database unavailable` and `cache unavailable` — the two constants `[C57.1]` and
`[C56.12]` introduced. Those constants are deleted along with their `publicErr:` field
assignments: their values now restate what the default produces, and two places that must
agree on one string is precisely the drift this ADR removes. No shipped probe changes its
`503` body.

**`Err` is untouched.** It still carries the full driver error to the application log
(`readyCheck` logs it at ERROR with a `component` field before returning) and — where debug
is enabled and access-controlled, since that endpoint's protection is conditional (see
`[C57.1]`'s `apply:`) — to `<debug.pathprefix>/health-debug`, which renders it verbatim.
This ADR changes what the *unauthenticated* body discloses, not what operators can see.

**The function does not depend on its caller for a non-nil `Err`.** `readyCheck` calls it
only where `result.Err != nil`, but because the new implementation never dereferences
`Err` — the one field that was previously unguarded — a future caller cannot turn it into a
nil-pointer panic on an unauthenticated request path. It still dereferences its
`*HealthStatus` argument, so a nil receiver panics as before.

## Alternatives rejected

**Collapse `critical` and `PublicErr` into one field.** A single `criticalPublicErr string`
would make the safe path structurally unavoidable — a probe is critical precisely when it
declares a public string. It conflates two orthogonal facts: criticality decides whether a
failure blocks readiness, and the public string decides what a failure discloses. A
non-critical probe can still surface its name in the 200 body, and `cache.critical`
(ADR-046) is operator-controlled config while the public string is a framework constant.
Fusing them would make `cache.critical: false` silently change disclosure semantics.

**Require the public string as a constructor parameter.** Making `publicErr` a positional
argument to a `newHealthProbe(...)` factory forces every author to confront it. Go cannot
enforce it: `healthProbeFunc` is an in-package struct and a composite literal that omits
the field still compiles, as every probe in `app/health.go` is written today. It is a
nudge, not a guard, and it costs a constructor plus a call-site rewrite at every probe to
buy less safety than a default that cannot be omitted.

**Keep the opt-in and rely on the invariant test.** The test that existed only enumerated
probes returned by `createHealthProbes`. A consumer's own `Prober` — the case ADR-046
explicitly supports — is invisible to it, and so is any framework probe registered by a
path the test does not construct. A default protects code the test never sees.

## Consequences

**Adopt-only for every shipping consumer.** The two critical probes emit the same bytes.
The atom exists for a consumer who wrote their own critical `Prober` and relied on the raw
error reaching the `503` body: that body now reads `<name> unavailable`. The full error is
still on the log and the debug endpoint, and `PublicErr` remains available for anyone who
wants different fixed wording. See [migrations.md](migrations.md) `[C57.2]`.

**Reviewing a `PublicErr` override is now the only disclosure review left.** An override
must be a constant. A value derived from config — a host, a DSN, a tenant key — would
reintroduce exactly the leak the default prevents, and it would do so on a path no test
covers by default. The `Prober` doc comment says this at the interface.

**The invariant test changed shape.** It no longer asserts that every critical probe
declares a non-empty `PublicErr`; it drives each critical probe's status through
`publicProbeError` with an identity-bearing error substituted in and asserts the rendered
string does not contain it. That is the property that actually matters, and it holds for a
probe that declares nothing. `TestReadyCheckSanitizesCriticalProbeWithoutPublicError`
covers the case this ADR exists for — a critical probe with no `PublicErr`, driven through
`readyCheck` — and fails against the pre-inversion code.

**`healthProbeFunc.publicErr` is now set by no framework probe.** The field and its
`HealthStatus.PublicErr` plumbing are kept as the in-package override seam, exercised by
`TestHealthProbeFuncRun`. Deleting it would leave an exported `PublicErr` that the
framework's own `Prober` implementation could not populate.

See [ADR-046](adr_046_cache_readiness_strict_default.md) for the seam this extends and the
`cache.critical` decision it sits on, and [migrations.md](migrations.md) `[C57.1]`,
`[C57.2]` for the upgrade atoms.
