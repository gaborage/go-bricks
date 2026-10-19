# ADR-150: A Missing Kid Is a Policy Opt-In, and a Kid-less Inbound Peer Is Served Imperatively

**Status:** Accepted
**Date:** 2026-10-18
**Issue:** #2163, #2142

## Context

Some peers name keys out of band: they put no `kid` on either JOSE object and expect replies
without one. `jose` could serve neither direction. The internal crypto adapter refused a
missing `kid` unconditionally, before any decrypt or verify, and `Seal` always wrote one, both
as the protected-header member and as go-jose's recipient key id.

Neither rule chose a key. In bare mode `Open` resolves `KeyResolver.PrivateKey(DecryptKid)`
first and only compares the wire `kid` to it, so the refusal was strictness about the peer's
profile, not key selection. "Missing" also covered more than absence: an absent member,
`"kid": ""` and `"kid": null` all read as an empty string and were refused alike.

The inbound peer behind #2142 shows the whole shape:

- The request body carries a compact JWE (`RSA-OAEP-256` + `A256GCM`, no `kid`) as a JSON
  member.
- A detached `RS512` JWS with no `kid` travels in a request header. Its payload is the JWE
  compact string (RFC 7515 Appendix F).
- Every reply, for every method including GET, replaces the value of the top-level `data`
  member with a compact JWE: no `kid`, no `cty`, no `typ`, no response signature. The rest of
  the envelope stays plaintext.
- Keys are selected per tenant from a tenant header.

No go-bricks service is ready to adopt this profile yet, and existing consumers already do
this JWE work in their own code.

## Decision

### 1. Kid optionality is two `Policy` fields

- **`AcceptMissingKid bool`, inbound only.** A bare `Open` accepts a JWE whose header has
  **no `kid` member at all** and decrypts it with `PrivateKey(DecryptKid)`. Every other bare
  check runs unchanged: the compact-only refusal and the 16 KiB header-segment bound, go-jose's
  parse narrowed to the declared `KeyAlg` / `Enc`, the unconditional `cty: JWS` refusal and the
  permissive `cty` rule.
- **`OmitKid bool`, outbound only.** A bare `Seal` writes no `kid`: no protected-header member
  and no go-jose `Recipient.KeyID`. The key is still resolved with `PublicKey(EncryptKid)`.
  `Cty`, `Typ`, `IATMillis` and `ProtectedHeaders` are written exactly as without the flag, and
  the `ProtectedHeaders` collision guard still refuses a hand-written `kid`.
- **Both zero values keep today's behavior byte for byte.** No kid field becomes optional:
  `DecryptKid`, `EncryptKid` and `VerifyKid` name the resolver key, not the wire header, and
  stay required wherever they were.
- **Only a literally absent member counts as missing.** A written member of any value, `""`
  and `null` included, is compared to `DecryptKid` and refused as `JOSE_KID_UNKNOWN`, with an
  empty `Kid` on the error. This departs on purpose from go-jose, whose header sanitizer skips
  a `null` member and so reads it as absent: go-bricks fails closed. A non-string `kid` stays
  `JOSE_MALFORMED` under both settings, because go-jose's parse refuses it before any kid
  check.
- **The opt-in gives up one diagnostic.** A kid-less token sealed to another key fails
  `JOSE_DECRYPT_FAILED`: there is no kid to report as unknown. `OpenHeader.JWE.Kid` stays `""`
  after a kid-less open; it is never filled in from `DecryptKid`.
- **`Validate` admits both fields only in `SealModeBareJWE`.** In `SealModeJWEofJWS` or
  `SealModeJWSofJWE` either one is `JOSE_POLICY_MODE_MISMATCH` whatever the direction, checked
  after the unknown-mode refusal and before the direction rules. `AcceptMissingKid` on an
  outbound policy and `OmitKid` on an inbound one are `JOSE_POLICY_DIRECTION_MISMATCH`.
- **The adapter gains matching options** whose zero values are today's refusal and today's
  kid write. `sealBare` and `openBare` copy the policy fields straight into them with no mode
  guard; `Validate` is what keeps the fields false in every other mode. `jose/sealed` never
  sets them.
- `AcceptMissingKid` is extended to the minimal JWS-of-JWE mode by #2142, where it covers both
  the outer JWS and the inner JWE. `OmitKid` stays bare-only, because the minimal mode is
  open-only.
- The change is additive: no `!`, no migrations atom.

### 2. The minimal JWS-of-JWE seal mode (#2142)

Implemented by #2142:

- `SealModeJWSofJWEMinimal`, whose `String()` follows the existing spelling convention:
  `"jws-of-jwe-minimal"`.
- A variant of `SealModeJWSofJWE` with the same signed bytes: the outer JWS payload is the JWE
  compact string.
- Its own signature table, exactly `{RS512}`, mirroring the per-mode content-encryption
  tables. The global `IsAllowedSigAlg(RS512)` stays false.
- An outer `cty` absent or exactly `"JWE"`; any other value is `JOSE_CTY_REJECTED`.
- The inner JWE keeps every bare-mode check. `typ` and `iat` are not judged.
- **Open-only**: `Validate` refuses an outbound policy in this mode. Reopen trigger: a
  go-bricks client that calls such a gateway.
- Refusals: a present wrong `kid` on either JOSE object is `JOSE_KID_UNKNOWN`; a tampered JWE
  string is `JOSE_SIGNATURE_INVALID`.
- **Open risk.** The signing input (the exact JWE compact string) was confirmed against a
  published profile sample only, not against live traffic. If live traffic signs the raw or
  re-serialized request body instead, this design does not hold and #2142 is re-triaged.

### 3. Detachment is an envelope, not part of the mode

`jose.Seal` / `jose.Open` keep their single-compact signatures; ADR-107 made them the only two
doors, and they stay so. A pure exported helper added by #2142,
`AttachDetachedJWS(detached, jwe string) (string, error)`, rebuilds the attached compact by
inserting `base64url(JWE compact)` into the detached token's empty payload segment, returning
header + `.` + `base64url(jwe)` + `.` + signature. It does no crypto. Its rules:

- the detached value must have exactly three dot-separated segments with an empty middle;
- the existing 16 KiB protected-header bound and the base64url alphabet checks apply;
- an embedded payload, a non-compact input and an oversize value are each refused with a
  `*jose.Error` coded `JOSE_MALFORMED`, `Status` 400.

HTTP framing stays consumer code: locating the header, stripping its scheme, refusing a
missing or duplicated header, and capping the header value.

### 4. The server stays imperative

The peer is served by a typed handler on an untagged route registered with
`server.WithRawResponse`, which:

- builds a `jose.Policy` per request from the resolved tenant;
- builds a consumer `jose.KeyResolver` per request (it takes no context);
- calls `jose.Open` / `jose.Seal`.

It seals the reply's top-level `data` member with a bare outbound policy (`OmitKid: true`,
`Cty` and `Typ` empty, so `Seal` writes neither header). A reply without `data` stays
plaintext, and nothing signs the reply. This reply seal is what `OmitKid` exists for. The
recipe lives in [wiki/jose.md](jose.md) (#2165).

Reasons:

- no go-bricks service is ready to adopt it;
- sealing replies to bodyless requests, which verify nothing inbound, breaks the tagged server
  rule that a response is sealed only when the inbound request verified;
- the peer's reply envelope is not go-bricks' `data`/`meta` envelope;
- existing consumers already do this work in their own code.

### 5. Replay

The seal code stays replay-blind, as in ADR-107 Alternative E: the seal code reports and the
caller decides. A sound dedup key is a digest of the JWE compact string, computed only
**after** the detached signature verified. Before verification, go-jose's non-strict
base64url decode lets a segment's spare trailing bits vary without changing the decoded bytes.
A canonical-base64url pre-check is #2164.

## Non-goals

- **`A256CBC-HS512`.** Not admitted anywhere; `IsAllowedEnc` and the per-mode tables are
  unchanged. A published profile sample uses it, but fixtures and captures use `A256GCM`, and
  which `enc` live clients send is not yet confirmed. Reopen only on live-traffic evidence.
- **RSA-OAEP with SHA-1.** Not admitted anywhere. Reopen only on live-traffic evidence.
- **PEM loading.** The consumer's resolver loads and parses its own per-tenant key material;
  keystore is not involved.
- **A server-first-class profile or policy door**: a server-side body envelope, a
  signature-header locator, exported inbound middleware for `RouteRegistrar.Add` routes, a
  per-request verify-key hook with a client-key-not-found code, a `server.WithJOSEPolicy`
  route option, and an environment switch. Decision 4 gives the reasons.
- **Replay judgment in the seal code.** Decision 5: freshness and dedup are the caller's.
- **An outbound minimal mode.** No go-bricks client calls such a gateway.

## Alternatives

- **A kid-optional seal mode instead of fields.** Rejected: kid handling is orthogonal to the
  wire shape, and a mode would have to be duplicated for every shape that wants it.
- **Relaxing the kid refusal globally.** Rejected: it weakens every existing peer, whose
  kid-less token is refused today.
- **Reading `null` as absent, as go-jose does.** Rejected: a written member is a statement by
  the peer, and comparing it fails closed.
- **Detached-signature `Seal` / `Open` signatures, or a third door.** Rejected per ADR-107:
  the two doors stay the only two.
- **The server-first-class design.** Rejected for the Decision 4 reasons.

## Consequences

- **Reach is the policy alone.** Both fields arrive at `jose.Seal` / `jose.Open` and at
  `httpclient.Builder.WithJOSE` with no plumbing: `JOSEConfig` carries `*jose.Policy`, and
  `Build` normalizes a whole-struct copy, so a wrong direction fails `Build`. There is no
  `jose:` tag key, no `seal-payload` flag and no server change; #1577 stays rejected.
- **Under `AcceptMissingKid` a wrong-key token is `JOSE_DECRYPT_FAILED`**, not
  `JOSE_KID_UNKNOWN`. Operators lose that hint for kid-less traffic only.
- **The imperative path emits no server-side `jose.*` telemetry**; only the tagged path
  records it.
- **A bodyless request carries no JWE**, so nothing is signed for it, and the transport or
  bearer credential is its only sender authentication.
- **The bare-mode out-of-scope note's reopen trigger fired.** It was answered with this
  Policy-level door, served imperatively, and `.out-of-scope/jose-tag-mode-bare.md` now says
  so.
- **Not a breaking change.** Two appended fields whose zero values keep today's behavior: no
  `wiki/migrations.md` atom and no `breaking-changes` entry.

## References

- Issue #2163 (this change), #2142 (the minimal JWS-of-JWE mode and the reattach helper),
  #2165 (the imperative server recipe), #2164 (the canonical-base64url pre-check), #1577 (the
  rejected `jose:` tag `mode` key)
- [ADR-107](adr_107_jose_bare_jwe_mode.md) — bare mode, the two doors, and the replay-blind
  seal code (Alternative E)
- [ADR-111](adr_111_jose_jws_of_jwe_mode.md) — the JWS-of-JWE mode the minimal mode varies
- RFC 7515 §4.1.4 and RFC 7516 §4.1.6 (`kid` is OPTIONAL); RFC 7515 Appendix F (detached
  content)
- [wiki/jose.md](jose.md#bare-jwe-mode-visa-message-level-encryption) — the consumer-facing
  documentation of both fields
- `jose/policy.go` (`validateMode`, `validateKidOptInModes`, `validateDirection`),
  `jose/bare.go` (`sealBare`, `openBare`), `jose/internal/cryptoadapter` (`Decrypt`,
  `Encrypt`, `parsedHeader`)
