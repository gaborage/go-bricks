# ADR-044: `httpclient.Builder.Build` Fails Closed on Unsafe Transport Composition

**Status:** Accepted
**Date:** 2026-08-01

## Context

`httpclient.Builder` has a single base-transport slot. Three builder compositions can
displace whatever currently occupies it — `WithTransport` after `WithTLSConfig`,
`WithTLSConfig` after `WithTransport`, and a wrapper option (e.g. `WithJOSE`) registered
without any `WithTransport` base when `WithHTTPClient` supplied a client with its own
`Transport` — and until this change, `Build() Client` only logged a WARN before handing
back a client whose client certificate, pinned roots, or caller-supplied `RoundTripper`
had silently vanished. A caller who never looks at WARN-level logs (a common posture in
production, where WARN is noise-level) gets a working `Client` that talks over
`net/http.DefaultTransport` instead of the mTLS transport it thinks it configured.

Two defects in the WARN-only mechanism needed fixing before turning it into a hard
failure, or the failure would have been unfixable or actively misleading:

1. `WithTLSConfig` unconditionally cloned `net/http.DefaultTransport` as its base,
   ignoring whatever `RoundTripper` already occupied the slot. `WithTransport(custom)`
   followed by `WithTLSConfig(cfg)` therefore always collided — there was no way to
   combine a caller's proxy/dialer settings with a loaded `*tls.Config`, even though
   both are legitimate configuration a caller might reasonably want together.
2. `discardsTLSConfig()`'s WARN message told the caller to "set `TLSClientConfig` on
   that transport yourself" — but following that advice did nothing to silence the
   warning; the predicate was pure call-order bookkeeping (`displacedBase == baseTLS`)
   and never inspected the replacement transport. Under a WARN this is a wording
   defect a caller could shrug off; under a hard failure it would have been an
   **unfixable build error** — the message tells you to do something that provably
   cannot clear the check.

## Decision

**Compose instead of collide (case 2) — but only where nothing is lost.** `WithTLSConfig`
now clones whichever `*http.Transport` already occupies the base-transport slot (if any)
instead of always cloning `http.DefaultTransport`. `WithTransport(custom).WithTLSConfig(cfg)`
now produces a transport carrying `custom`'s proxy, dialer, and pool settings *and* `cfg`'s
TLS material — not a discard — **provided `custom` decides no TLS of its own: neither a
`TLSClientConfig` carrying security material nor a `DialTLS`/`DialTLSContext`**.
`WithTLSConfig` overwrites `TLSClientConfig` unconditionally and clears both dialer hooks,
so an incumbent that had either (a client certificate, pinned roots, a pinning dialer) still
loses that material; the transport-tuning fields compose, but the TLS material does not, and
that case remains a reported displacement even though the clone itself succeeds.

**Determinism: this check reads the incumbent's security material, not whether
`TLSClientConfig` is nil.** `*http.Transport.Clone()` mutates its own receiver as a side
effect — `net/http/transport.go`'s `onceSetNextProtoDefaults`, run once per transport
instance, lazily installs an ALPN-only `TLSClientConfig` (`NextProtos: ["h2", "http/1.1"]`,
nothing else) the first time that transport is cloned or dialed, even if the field started
nil. A nil check would therefore flip to "carries material" merely because *some other
builder* had already cloned the same shared transport, making composition depend on
construction order rather than the transport's own configuration — a caller sharing one
tuned `*http.Transport` across several `httpclient.Builder`s (a normal pattern) would see
one builder succeed and a functionally identical one fail. `tlsConfigCarriesMaterial`
(`httpclient/tls.go`, which holds the authoritative list) instead tests for actual
security-relevant fields — certificates and the client-certificate hook, `RootCAs`,
`InsecureSkipVerify`, the version floor and ceiling, `ServerName`, cipher suites, curve
preferences, renegotiation policy, and the `VerifyPeerCertificate`/`VerifyConnection`
hooks — and ignores `NextProtos` entirely, so the
same incumbent produces the same result regardless of how many builders have already cloned
it. This is deliberately the harder-to-write check; a future maintainer simplifying it back
to `TLSClientConfig == nil` would silently reintroduce the order-dependence.

`base.DialTLS`/`DialTLSContext` are still cleared on the clone regardless of source, and
matter *more* here: a caller-supplied transport is more likely to carry a live
TLS-bypassing dialer than a synthesized `DefaultTransport` clone, and clearing it is what
makes `TLSClientConfig` actually take effect. When the incumbent is an opaque,
non-`*http.Transport` `RoundTripper`, it cannot be cloned and composing is impossible — that
case remains a genuine, reported displacement too.

**Make the case-1 remedy actually work (case 1 fix).** `discardsTLSConfig()` now
inspects the replacement: if the transport that displaced the loaded `*tls.Config` is
itself a `*http.Transport` that decides its own TLS — meaningful `TLSClientConfig`
material, or a `DialTLS`/`DialTLSContext` — the predicate is suppressed; the caller
did exactly what the message asks. Both base-slot directions ask that question
through one predicate, `transportCarriesTLSMaterial`, so they cannot drift into
disagreeing about the same transport. This is deliberately **not** a nil check on `TLSClientConfig`:
the first version of this fix used one, and it fails open the same way the
compose-path nil check did — `*http.Transport.Clone()` can leave an otherwise-bare
replacement with a non-nil, ALPN-only `TLSClientConfig`, which a nil check would
treat as "configured" and wrongly suppress, silently dropping the caller's client
certificate or pinned roots with no error. An opaque replacement, or a
`*http.Transport` whose `TLSClientConfig` carries no security material (nil or
ALPN-only), is still reported.

**`Build()` returns `(Client, error)`; `BuildStrict` does not ship as a second
entrypoint.** An earlier draft of this change (PR #839) added `BuildStrict() (Client,
error)` alongside the existing `Build() Client`, leaving both in place so callers could
opt in gradually. That is two doors into the same construction path for a hazard that
should not have a silent-degradation door at all: `Build` now returns
`(Client, error)` directly, and returns a non-nil error exactly when one of the three
composition predicates (`discardsTLSConfig`, `discardsProvidedTransport`,
`discardsClientTransport`) reports true after the Step 1/2 fixes above. There is no
`BuildStrict`.

**Error, not panic.** The predicates that trigger this failure are data-dependent on
values the caller controls at runtime — `WithTLSConfig(nil)` is an early-return no-op,
so whether a given composition is hazardous depends on which options were called and
with what, not on anything knowable at compile time. A panicking `Build` would be
*green in a staging environment that happens not to exercise the hazardous
combination* and crash-looping in production from byte-for-byte identical source the
moment a config change (a new TLS cert path, a new proxy transport) puts the same code
through the hazardous branch. An error return makes the failure deterministic and
visible at the call site instead of a runtime surprise gated on which code path
executes. It also matches the two real panic-recovery sites already in the framework
and what they do to a panic that reaches them: `server/middleware.go:114` installs
`middleware.Recover()`, which turns a handler-path panic into a 500 and keeps serving —
exactly the "still up, quietly wrong" outcome this ADR is trying to avoid, not
reproduce inside a constructor. `messaging/registry.go:688` recovers a consumer-handler
panic and nacks the message **without requeue** — a `Build()` panic reached from a
message handler would silently drop the message instead of failing the deployment.
Compare `app/module_registry.go`, which is the framework's actual fail-fast idiom:
`Module.Init()` returns an `error`, propagated and joined by the registry, so a
misconfigured module fails application startup loudly and deterministically. `Build`
returning an error puts it on the same footing — callers thread it through their own
`Init()` (or equivalent) exactly like any other construction error, and the failure
happens once, at startup, instead of being deferred into whichever request or consumer
path first exercises the hazardous branch.

**Scope: this validates base-transport-slot displacement, not TLS posture.** `Build`'s
error return catches exactly the three composition predicates above — a caller's own
transport, TLS config, or client silently disappearing because of `With*` call order.
It does **not** catch, and callers must not read it as covering:

- The `WithHTTPClient` override boundary — an explicit `WithTransport` call is treated
  as the caller's deliberate override and always wins; that is spec, not a hazard.
- `InsecureSkipVerify` set on a hand-built `*tls.Config` passed to `WithTLSConfig` —
  `Build` composes transports, it does not audit `tls.Config` fields.
- A hand-built `*tls.Config` with no `MinVersion` set (and therefore no floor) — only
  `NewClientTLSConfig` enforces the TLS 1.2 floor; a config built by hand and handed to
  `WithTLSConfig` directly bypasses that loader entirely.
- `WithTLSConfig(nil)` reached via a swallowed config-loader error — `WithTLSConfig`
  treats `nil` as an intentional no-op (documented behavior), so a caller whose TLS
  material failed to load upstream and silently substituted `nil` gets a plaintext
  base transport with no signal from `Build`.
- A replaced `net/http.DefaultTransport` global (test doubles, APM agents) — `Build`'s
  predicates reason about the base-transport *slot*, not about what `DefaultTransport`
  currently points at; the fallback-clone logic (`baseTransportForTLS`) already handles
  a replaced global functionally, but `Build` does not flag the replacement itself as a
  hazard.

One more scope note that is really a determinism guarantee: the "does this incumbent carry
material" check above is decided from the incumbent's actual security-relevant `tls.Config`
fields (`tlsConfigCarriesMaterial`), never from whether `TLSClientConfig` is nil — see
**Determinism** above for why a nil check would make the answer depend on which builder
happened to clone a shared transport first.

**Timing: affordable now, not later.** `WithTLSConfig` shipped 2026-07-25 and released
in v0.55.0 on 2026-07-27 (`CHANGELOG.md`). This ADR lands within the same release cycle
that introduced it — no fleet has had more than a few days to adopt the WARN-only
`Build`, so there is no accumulated body of deployed call sites relying on the
silently-degraded behavior. Making this change later, after consumers had shipped and
forgotten about a WARN they never look at, would have meant breaking working
production configurations instead of breaking call sites at compile time before they
ship.

## Consequences

**Breaking change.** `Builder.Build()`'s signature changes from `Client` to
`(Client, error)`. Every call site — framework tests, consumer code, documentation
examples — must handle the second return value, but only the ones that capture the
result by single-value assignment fail to compile. **Four** shapes stay compile-valid
and silently drop both results, so the compiler enumerates *most* affected call sites,
not all: a bare `builder.Build()` expression statement; a blank-identifier assignment
`client, _ := builder.Build()`; `defer builder.Build()`; and `go builder.Build()`. All
four compile identically before and after this change, and `go vet` flags none of them
(verified by compiling all four against a two-return `Build`). The blank-identifier form
is the most dangerous, being the tempting way to "fix" a compile error: it suppresses
exactly the signal this change exists to add and leaves a nil `Client` on the error path.
All four need manual review; [wiki/migrations.md](migrations.md) (`[C56.6]`) carries the
probes and is the authoritative list.
`NewClient(log logger.Logger) Client`
keeps its existing signature (`c := NewClient(log)`, no error to handle): a bare
`NewBuilder(log).Build()` chain never calls `WithTransport`, `WithTLSConfig`, or
`WithHTTPClient`, so none of the three composition predicates can find anything to
report, and the discarded error `NewClient` carries internally is provably unreachable —
pinned by a dedicated test so a future predicate change that could fire on the bare-chain
path trips CI instead of silently making `NewClient` lossy.

**`ErrUnsafeTransportComposition` is an exported sentinel, not a taxonomy.**
`Build` currently has exactly one error kind, so today `err != nil` and
`errors.Is(err, ErrUnsafeTransportComposition)` are equivalent — the sentinel adds no
new information yet. It earns its place anyway because `Build` returning an error is
now mandatory: every consumer wraps it into their own module `Init()` error (per the
manifesto's "wrap once at boundaries" rule), and once wrapped, the message text is no
longer reliably matchable — only `errors.Is` survives an arbitrary chain of `%w`. It
also gives this ADR's own anticipated follow-ups (more base-transport-slot composition
checks added to `Build` over time) a stable thing to classify against without a caller
having to track each new message string. It is not a set of distinct error *kinds* —
there is one sentinel for one class of problem (unsafe transport composition), not a
per-predicate taxonomy.

**Compose-not-collide is a net behavior improvement independent of the error return.**
When `custom` decides no TLS of its own — no meaningful `TLSClientConfig` (nil, or the
ALPN-only shape `Clone()` can leave behind) and no `DialTLS`/`DialTLSContext` —
`WithTransport(custom).WithTLSConfig(cfg)` now produces a client that actually has both
`custom`'s proxy/dialer settings and `cfg`'s TLS material, where before it silently lost the
former. This fixes real, previously-unaddressable configurations, not just the
error-reporting path around them. When `custom` DOES carry its own security material or a
TLS dialer, the tuning still composes but that material is still replaced or cleared — and
`Build` still reports it as a displacement, exactly as before this change.

**No DB schema migration, no config key changes.** This is a Go API signature change
only — see [wiki/migrations.md](migrations.md) (`[C56.6]`) for the upgrade atom.

**Delivered as a three-PR stack, in dependency order:** the compose-not-collide fix,
then the discard-predicate fix, then the `Build` signature change. The order is
load-bearing rather than cosmetic — either precursor merged *after* the hard failure
would have shipped an interval on `main` where a legitimate
`WithTransport(custom).WithTLSConfig(cfg)` chain failed construction with no remedy the
error message could name. Both precursors are non-breaking on their own: they only
narrow which compositions are reported, and until the third PR lands the report is
still a WARN.

See [wiki/httpclient.md](httpclient.md#transport-composition) for the full builder
transport-composition narrative and worked examples.
