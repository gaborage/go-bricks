# ADR-107: Bare-JWE Mode Is a Field on the Policy, Not a Second Door

**Status:** Accepted
**Date:** 2026-09-09
**Issue:** #1575

## Context

`jose` ships exactly one wire shape. `Seal` signs the payload as a compact JWS and
encrypts that JWS to the peer (`jose/sealer.go`); `Open` decrypts and then verifies
(`jose/opener.go`). Everything in the package is written to that shape: `Policy.Validate`
requires a `SigAlg` on the signature allowlist, `validateInbound`/`validateOutbound`
require a verify or sign kid beside the decrypt or encrypt kid, and
`allowedContentEncs` holds `A256GCM` alone.

Visa **Message Level Encryption** does not have that shape. An MLE payload is a single
compact JWE with no inner JWS: `alg` is `RSA-OAEP-256`, `enc` is `A128GCM`, the protected
header carries `typ: "JOSE"` and an `iat` in Unix epoch **milliseconds**, and the sender is
authenticated out of band — Visa's `X-Pay-Token` header and mTLS do that job, not a
signature over the body. Every one of those five facts is refused by the package as it
stands: no policy can omit the signature, no policy can select `A128GCM`, and no seam
writes a caller-chosen protected header at all.

The `iat` is the awkward one. It is spelled like the JWT claim `jose.Claims` already
carries, and it is not that claim: it lives in the JWE protected header rather than in the
payload, and it counts milliseconds rather than seconds. A consumer reading it as the
familiar claim is off by a factor of a thousand, in the direction that makes every token
look impossibly far in the future.

ADR-097 settled the neighbouring question for AMQP events: the seal layer never judges
replay — `Meta.DedupKey()` plus `inbox.ProcessOnce` do. Nothing had yet decided whether
the HTTP-body seal layer judges freshness.

## Decision

**The wire shape is a field on `Policy`. `Seal` and `Open` remain the only two doors, and
they read `Policy.Mode` to decide what to produce and accept.**

- **`Policy.Mode SealMode`, zero value `SealModeJWEofJWS`.** `SealModeBareJWE` is the
  encrypt-only shape. A policy that never mentions `Mode` keeps today's posture
  byte-for-byte, so the default is not a decision any existing caller has to re-make.
  There is no second policy type and no `SealBare`/`OpenBare` on the exported surface —
  the unexported `sealBare`/`openBare` in `jose/bare.go` are the mode's half of the two
  doors, reached only through `Seal`/`Open`. One door means the direction guard, the
  resolver guard, the validation order and the error shapes are written once and cannot
  drift between shapes.
- **`Typ string`, `ProtectedHeaders map[string]any` and `IATMillis bool` are OUTBOUND,
  bare-mode-only fields.** `Typ` writes the JWE protected `typ`. `ProtectedHeaders` is
  copied verbatim into the protected header. `IATMillis` makes `Seal` stamp `iat` with
  `time.Now().UnixMilli()` at seal time. A `SealModeJWEofJWS` policy that sets any of the
  three is refused with `JOSE_POLICY_MODE_MISMATCH`, and a bare INBOUND policy that sets
  any of them is refused too: they describe headers `Seal` writes, nothing would read them
  on the way in, and silently ignoring them would let a consumer believe a header was
  being enforced.
- **A static map plus a bool, not a `func(...) map[string]any`.** The headers a partner
  prescribes are deployment constants; a callback would make the protected header a
  per-request decision, unvalidatable at startup, and would put caller code inside the
  seal path. The one value that genuinely varies per request is the timestamp, and it is
  the framework's clock, not the caller's — hence a bool rather than a field the caller
  fills in. `Policy.bareExtraHeaders` merges the two without mutating the policy's map.
- **`typ` is its own field because the collision guard owns it.**
  `cryptoadapter.CheckExtra` refuses any `Extra` entry naming an adapter-owned or
  JOSE-reserved param, `typ` included, so `ProtectedHeaders{"typ": "JOSE"}` could only ever
  be an error. A named field is the honest spelling of a header the adapter already writes
  through `WithType`. The guard is now exported and additionally runs at policy-validation
  time, so a colliding map fails at startup rather than once per request, with
  `JOSE_POLICY_HEADER_COLLISION` — which also covers a hand-written `iat` beside
  `IATMillis: true`.
- **`A128GCM` is admitted by MODE, never globally.** `IsAllowedEncFor(mode, enc)` and
  `AllowedContentEncsFor(mode)` are the mode-aware predicates; `IsAllowedEnc` and
  `AllowedContentEncs` keep their JWE-of-JWS meaning, so the nested path's floor stays
  `A256GCM` and a widening of it would have to be written deliberately. An unknown mode
  resolves to a nil allowlist, so it rejects every content encryption and every token.
  `A128GCM` is defensible here and not there: a bare JWE carries no inner signature whose
  strength the content encryption has to match, and Visa specifies it. `RSA1_5`,
  `alg=none`, `HS*` and ECDSA stay rejected in both modes — the key-algorithm and
  signature allowlists do not move.
- **`jose` does not judge an inbound `iat`.** `Open` in bare mode decrypts, applies the
  same permissive `cty` rule as the nested path, and returns. It reports the header as
  `OpenHeader.JWE.IATMillis` and judges nothing about it — the same stance ADR-097 takes
  on replay. Freshness windows are partner-specific (the page has said so about the JWT
  claims since the package shipped), and in bare mode the value is integrity-protected by
  the JWE authentication tag but not sender-authenticated — nobody in the middle can edit
  it, and nothing proves who wrote it — so refusing on it would be enforcing a
  peer-controlled number.
- **`Header` gains scalar fields, not a map.** `Typ string` and `IATMillis int64` join
  `Kid`/`Alg`/`Enc`/`Cty`. An `Extra map[string]any` would have surfaced every protected
  header at once and cost `Header` its comparability — the mistake this change is
  deliberately making once, on `Policy`, and refuses to make twice.
- **`Seal` validates fully before touching the keystore, in BOTH modes.** It ran
  `validateAlgorithms` only; it now runs `validateMode`, `validateAlgorithms` and
  `validateDirection` first. Bare mode hands `ProtectedHeaders` to the crypto adapter
  verbatim, so a policy that reached `Seal` without going through the tag scanner or
  `httpclient`'s `Build` must fail closed rather than be trusted, and a bad policy should
  not resolve a key before it fails.
- **The `Policy` comparability break is accepted, not shimmed.** A struct holding a map is
  not comparable in Go, so `==`, `!=` and map-key use on `jose.Policy` stop compiling. A
  pointer-to-map would restore `==` as pointer identity — two identical policies comparing
  unequal, code that keeps building and silently changes meaning — which is the trade
  ADR-058 already rejected for `ConsumeOptions`. The framework does not shim its own
  surface (root CLAUDE.md); the break is documented as `[C64.15]` and left to the
  compiler.
- **"Seal" now means "apply the policy's mode".** The glossary term covered encrypt+sign
  and listed "encrypt (alone)" as a word to avoid. Sealing is the operation both shapes
  perform: the JWE-of-JWS shape (and `jose/sealed`'s signed-JWE-inside-JWS events) is the
  default, the bare-JWE shape is the out-of-band-authenticated one. **Seal mode** is the
  new term for which one a policy selects.

**Bare mode is a `Policy`-level door only, this round.** There is no `mode` key in the
`jose:` struct-tag grammar (`jose/tag.go`), so no route can opt in through a tag, and
`httpclient`'s `normalizedJOSEPolicy` defaults `SigAlg` to `RS256` before validating —
which a bare policy must not carry — so `WithJOSE` cannot carry one either. `jose.Seal`
and `jose.Open` (and `jose/testing`'s `SealForTest`/`OpenForTest`, which call them) are the
whole surface until the `httpclient` envelope hooks land in the next stacked PR.

## Alternatives

**A — a second door: `SealBare`/`OpenBare`, or a `BarePolicy` type.** No comparability
break, and each door's validation reads only about its own shape. Rejected: the two shapes
share the direction guard, the resolver guard, the kid resolution, the error mapping and
the `cty` rule, so a second door duplicates all of it and invites the copies to drift; and
a caller holding a `*Policy` from configuration would have to switch on a type to decide
which function to call — the switch this design puts in one place, inside `Seal`/`Open`.

**B — `ProtectedHeaders func(context.Context) map[string]any`.** One field instead of
three, and `iat` becomes just another entry. Rejected: it defers to request time what a
partner contract fixes at deploy time, so the collision guard could no longer run at
startup and a colliding header would surface as a per-request 500; and it puts consumer
code on the seal path, where a panic or a slow call becomes a crypto-path failure.

**C — `typ` as an entry in `ProtectedHeaders`.** Rejected on the guard: `typ` is
adapter-owned, `CheckExtra` refuses it, and exempting it would poke a hole in the one
mechanism that keeps a caller from overwriting `alg`, `enc`, `kid` or `cty`.

**D — widen `allowedContentEncs` to hold `A128GCM` globally.** One list, no mode-aware
predicate pair. Rejected: it lowers the nested path's content-encryption floor for every
existing deployment to serve a shape none of them use, and it would make `IsAllowedEnc` —
a predicate consumers can call — start answering `true` for a value the JWE-of-JWS path
must keep refusing.

**E — judge `iat` freshness inside `Open`.** Reject a bare token older than a configured
window. Rejected: the value is an unsigned header written by the peer, the tolerance is
partner-specific, and ADR-097 already fixed the framework's stance that the seal layer
reports and the caller decides. A freshness knob here would also be the first thing in
`jose` that needs a clock on the inbound path.

## Consequences

- **`jose.Policy` stops being comparable.** `==`, `!=` and map-key use on it no longer
  compile — including in `_test.go` files, which `go build ./...` does not type-check.
  Compare the fields that matter, or key on the kids. Assignment, copying, passing by
  value, struct literals and field access are unchanged, and a policy that sets no
  `ProtectedHeaders` produces byte-identical output. `reflect.DeepEqual` still compiles and
  now walks the map, distinguishing a nil map from an empty one. See `[C64.15]`.
- **`A128GCM` is now reachable in this framework.** It is reachable only behind
  `SealModeBareJWE`, and a reviewer grepping for it should read the mode beside it. The
  nested path, `jose/sealed` (ADR-097) and every existing policy stay on `A256GCM`.
- **A bare-mode peer is authenticated by the deployment, not by `jose`.** Nothing in the
  bare path verifies a signature, so `OpenHeader.JWS` comes back zero and a successful
  `Open` proves only that the payload was encrypted to our public key — which any holder
  of that public key can do. The peer's identity must come from the transport (mTLS) or an
  out-of-band token (Visa's `X-Pay-Token`). A deployment that turns bare mode on without
  one of those has removed sender authentication from that route.
- **There is no accept-unsealed or mixed-mode arm, and the mismatch is not symmetric.** A
  policy has one mode and nothing negotiates. A bare token offered to a nested policy fails
  at parse time when it uses `A128GCM` (`JOSE_MALFORMED`, the nested allowlist) and at the
  inner layer when it uses `A256GCM`, where its plaintext is not a compact JWS
  (`JOSE_INNER_NOT_JWS`). A NESTED token offered to a bare policy DECRYPTS — the outer JWE
  is the same object in both shapes — so `openBare` fails closed on the marker the nested
  seal writes: a JWE whose protected `cty` is `JWS` is rejected with `JOSE_CTY_REJECTED`
  regardless of `Policy.Cty`, and the inner compact JWS never reaches the caller as
  unverified plaintext. Bare mode never carries an inner JWS and Visa MLE uses
  `typ: JOSE`, so the unconditional rule costs no interop; `Policy.Cty` remains the
  consumer's own content-type pin.
- **Three new error codes reach registration and startup, not the wire.**
  `JOSE_POLICY_MODE_UNKNOWN`, `JOSE_POLICY_MODE_MISMATCH` and
  `JOSE_POLICY_HEADER_COLLISION` all come out of `Policy.Validate` (and now out of `Seal`'s
  pre-flight), where `JOSE_ALGORITHM_DISALLOWED` already lived: they are configuration
  failures, and they carry `ErrPolicyMismatch` as their sentinel.
- **`Seal` fails earlier than it used to for a bad policy in EITHER mode.** A policy with
  the wrong kids for its direction used to resolve a key first and fail afterwards; it now
  fails before the resolver is called. A caller whose test double counted resolver calls
  on a rejected policy sees one fewer.
- **`Header` grew two fields and stayed comparable.** `Typ` is populated on the nested path
  too — it is read off whichever protected header the layer has — so a nested deployment
  that logs `OpenHeader` starts seeing a `typ` value where a peer sets one.

## References

- Issue #1575 (bare-JWE mode for Visa Message Level Encryption)
- [ADR-097](adr_097_sealed_amqp_messages.md) — the seal layer reports and never judges
  replay; the stance this ADR extends to inbound `iat`. `jose/sealed` is untouched by this
  change and stays `A256GCM`
- [ADR-058](adr_058_consumer_scoped_amqp_arguments.md) — the precedent for accepting a
  comparability break rather than hiding a map behind a pointer
- [ADR-084](adr_084_jose_error_envelope_details_gate.md) — the JOSE error envelope, whose
  pre-trust/post-trust split is unchanged by bare mode
- [wiki/jose.md](jose.md#bare-jwe-mode-visa-message-level-encryption) — the consumer-facing
  bare-mode documentation
- [wiki/migrations.md](migrations.md) atom `[C64.15]` — the detect/gate/apply runbook for
  the comparability break
- `jose/policy.go` (`SealMode`, `validateMode`, `validateBareHeaders`,
  `validateBareDirection`, `validateBareKids`), `jose/bare.go` (`sealBare`, `openBare`,
  `bareExtraHeaders`), `jose/algorithms.go` (`IsAllowedEncFor`, `AllowedContentEncsFor`),
  `jose/opener.go` (`Header`), `jose/sealer.go`,
  `jose/internal/cryptoadapter/extra.go` (`CheckExtra`)
