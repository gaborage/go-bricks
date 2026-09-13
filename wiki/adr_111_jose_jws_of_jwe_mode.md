# ADR-111: JWS-of-JWE Is a Third Seal Mode, Not a Second Door

**Status:** Accepted
**Date:** 2026-09-13
**Issue:** #1606

## Context

`jose` ships two wire shapes, both selected by `Policy.Mode`: the nested `SealModeJWEofJWS`
default (sign, then encrypt the JWS; `cty: JWS` on the JWE) and `SealModeBareJWE`
(encrypt only, no signature — ADR-107).

The Visa **Token Service Issuer** API uses neither. Its bodies are a compact **JWS whose
payload is a compact JWE**: encrypt first, sign the ciphertext. Observed against the legacy
nimbus-jose-jwt 10.x producer, the outer JWS carries `PS256` with `typ: JOSE`, `cty: JWE`
and a `kid`; the inner JWE carries `RSA-OAEP-256` + `A256GCM` with `typ: JOSE`, a `kid`, a
millisecond `iat` and no `cty`; and the JWE compact string is the JWS payload verbatim, not
JSON-wrapped or re-encoded.

Without framework support, a consumer had to reach into `jose/internal` or import go-jose
directly, hand-building both layers and their headers — the exact coupling ADR-107 removed
for bare mode.

## Decision

**Add a third `SealMode`, `SealModeJWSofJWE`, reached through the existing Policy-level
door.** `jose.Seal`, `jose.Open` and `httpclient.Builder.WithJOSE` speak it in both
directions; no new exported primitive, and no `mode` key in the `jose:` struct-tag grammar,
so inbound server routes still cannot select it (#1577 stays wontfixed).

**`Seal` reuses the bare builder, minus `cty`.** The inner JWE is exactly what
`sealBare` produces — `Policy.Typ`, `Policy.ProtectedHeaders` and a millisecond `iat` under
`Policy.IATMillis`, on the same collision guard — with `Cty` cleared on a Policy copy. The
wire shape carries no inner `cty`, and `httpclient`'s normalization fills `Policy.Cty` with
`DefaultCty` for every mode, so refusing a set `Cty` would have broken `WithJOSE` for this
mode or forced the special case the acceptance criteria forbid. Dropping it is the only
option that satisfies both.

**The outer JWS protected header is fixed by the mode**: `alg` from `Policy.SigAlg`, `kid`
from `Policy.SignKid`, `typ: JOSE`, `cty: JWE`, and `iat` in Unix epoch **seconds** —
always, whatever `Policy.IATMillis` says about the inner JWE (Visa spec 26.03 mandates the
seconds `iat`). There is no per-layer knob: `Policy.Typ` addresses the inner JWE only,
exactly as in bare mode.

**`Open` verifies before it decrypts**, and each refusal keeps its own code:

| Condition | Code |
| --- | --- |
| Body is not a 3-segment compact JWS (a 5-segment JWE-outer body included) | `JOSE_OUTER_NOT_JWS` |
| Outer `alg` is not exactly `Policy.SigAlg` | `JOSE_ALGORITHM_DISALLOWED` |
| Outer `kid` missing / not `Policy.VerifyKid` | `JOSE_KID_MISSING` / `JOSE_KID_UNKNOWN` |
| Signature does not verify | `JOSE_SIGNATURE_INVALID` |
| Verified outer header lacks `cty: JWE` | `JOSE_CTY_REJECTED` |

Only then does the payload reach the bare opener under `Policy.DecryptKid`. `Open` never
judges either `iat` (ADR-107's stance, itself ADR-097's).

**The outer signature algorithm is pinned to exactly `Policy.SigAlg`**, which is stricter
than the nested path's package-wide allowlist verify. A deployment that agreed `PS256` with
its peer refuses an `RS256` body rather than accepting it because the package permits
`RS256` in general. The check reads the peeked (still unauthenticated) `alg` before any key
is used, so a rejected algorithm never reaches key material; the pinned allowlist handed to
go-jose then repeats the rule.

**`OpenHeader` reports both layers**, with `JWS.IATMillis` left `0`: that field states
milliseconds, and the outer `iat` is seconds. No exported field was added for it.

## Alternatives

- **Exported `SignCompact` / `VerifyCompact` primitives** (issue Option B). Rejected on
  ADR-107's precedent: a nesting order is a property of the wire shape, so it belongs on
  `Policy`, not behind a second door consumers must sequence correctly.
- **Refuse `Policy.Cty` in this mode instead of dropping it.** Rejected: `WithJOSE` fills
  `Cty` for every mode, so every defaulted policy would fail `Build`.
- **A per-layer header knob** (`OuterTyp`, `OuterProtectedHeaders`, …). Rejected: the outer
  header is fully determined by the profile, so a knob could only express a wrong value.
- **Force the inner `typ` to `JOSE`.** Rejected: `Policy.Typ` behaves identically to bare
  mode, so a consumer sets `Typ: "JOSE"` and the Visa profile falls out. Forcing it would
  make this mode the one place where `Typ` is ignored.
- **Judge the outer `iat`.** Rejected — ADR-107 Alternative E stands: freshness tolerance is
  partner-specific and belongs to the caller.
- **Reuse `JOSE_MALFORMED` for the outer-shape mismatch.** Rejected: the acceptance criteria
  require a distinct code, and a JWE-outer body under this policy is a configuration or
  attack signal worth naming.

## Consequences

- **A third mode arm exists at every seam that switches on `SealMode`** — `validateMode`,
  `validateKids`, `contentEncsForMode`, `Seal` and `Open`. Each arm is explicit and the
  `default` still fails closed with `JOSE_POLICY_MODE_UNKNOWN`.
- **`Policy.Cty` is silently unused on this outbound path.** It is documented on the field
  and here; nothing else in the package ignores a set field, so this is the exception a
  reader must know about.
- **The `SigAlg` default is a live footgun for Visa consumers.** `jose.DefaultSigAlg` is
  `RS256` and `WithJOSE` applies it, so a policy that omits `SigAlg` builds, validates and
  seals — and is rejected by Visa at runtime. The mode does not force `PS256`, because the
  framework allowlist is not a Visa profile; `wiki/jose.md`, `llms.txt` and the README all
  say to set it explicitly.
- **Sharing a `DecryptKid` across modes is now a deployment hazard.** The inner JWE lifted
  out of a signed body decrypts on a bare-JWE route that uses the same kid, where nothing
  authenticates the sender. An outer signature attests who *sent* the body, not who
  encrypted it. Documented in `wiki/jose.md`.
- **Byte-stable vectors are built with go-jose directly** (`jose/testdata/jwsofjwe_vectors.json`,
  regenerated with `go test ./jose -update`), never through `Seal`, so they stay an oracle
  independent of this code. The positive vector additionally asserts the 32-byte PSS salt
  that nimbus and go-jose both produce, a property go-jose's own auto-detecting verify
  cannot fail on.
- **Not a breaking change.** An appended enum member with no config key, no tag key and no
  signature change: no `wiki/migrations.md` atom and no `breaking-changes` entry.

## References

- Issue #1606 (JWS-of-JWE mode for the Visa Token Service Issuer API)
- [ADR-107](adr_107_jose_bare_jwe_mode.md) — the precedent this ADR follows: a wire shape is
  a field on `Policy`, not a second door; and `Open` reports `iat` without judging it
- [ADR-097](adr_097_sealed_amqp_messages.md) — the seal layer never judges replay;
  `jose/sealed` is a different door and is untouched
- [wiki/jose.md](jose.md#jws-of-jwe-mode-visa-token-service-issuer-api) — the consumer-facing
  documentation, including the `PS256` and key-separation warnings
- `jose/jwsofjwe.go` (`sealJWSofJWE`, `openJWSofJWE`, `errOuterNotJWS`), `jose/policy.go`
  (`SealModeJWSofJWE`, `validateMode`, `validateKids`, `validateInnerJWEHeaders`),
  `jose/algorithms.go` (`contentEncsForMode`), `jose/errors.go` (`codeOuterNotJWS`),
  `jose/bare.go` (`sealBare`, `openBare`, reused for the inner layer)
