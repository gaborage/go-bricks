# JOSE Middleware (Deep Dive)

The `jose` package provides nested JWE-of-JWS protection on HTTP request and response bodies — sign-then-encrypt outbound and decrypt-then-verify inbound on every payload. It is designed for **Visa Token Services**-style integrations and any partner API that requires this level of payload protection. That nested shape is the default and everything below describes it; a policy can select one of two other shapes instead — the encrypt-only [Bare-JWE mode](#bare-jwe-mode-visa-message-level-encryption) Visa Message Level Encryption specifies, or the encrypt-then-sign [JWS-of-JWE mode](#jws-of-jwe-mode-visa-token-service-issuer-api) the Visa Token Service Issuer API expects.

## JOSE Middleware

The `jose` package provides nested JWE-of-JWS protection on HTTP request and response bodies. Designed for **Visa Token Services**-style integrations and any partner API that requires sign-then-encrypt outbound and decrypt-then-verify inbound on every payload.

**Key Features:**

- **Struct-tag opt-in**: Add a `jose:` tag to a sentinel field on the request/response type — no per-route plumbing
- **Bidirectional symmetry enforced**: both request and response must carry tags or neither (registration-time check)
- **Strict algorithm allowlist**: `RS256`/`PS256` for signing; `RSA-OAEP-256` + `A256GCM` for encryption on this nested path — `A128GCM` is admitted too, but *only* behind `Policy.Mode: jose.SealModeBareJWE` (see [Bare-JWE mode](#bare-jwe-mode-visa-message-level-encryption)). `RS512` is admitted for signatures in one mode only, the open-only `Policy.Mode: jose.SealModeJWSofJWEMinimal` (see [Minimal JWS-of-JWE mode](#minimal-jws-of-jwe-mode-kid-less-inbound-peers)). `alg=none`, `HS*`, `RSA1_5`, and `ES256` are rejected at parse time. Bodies are **compact serializations only** — go-jose also accepts the JSON form and tolerates whitespace inside a compact, and the framework refuses both at every door, since RFC 7515 §7.1 and RFC 7516 §7.1 admit neither; whitespace *around* a compact is still trimmed, so a token read from a file with a trailing newline opens — and every compact the framework opens, at either layer and in every mode, must carry an encoded protected-header segment of at most 16 KiB — the base64url text as it arrives, measured before any decode, so the decoded JSON header it admits is about three quarters of that. A larger one is refused before it is base64-decoded, as `JOSE_MALFORMED` on the outer layer and `JOSE_INNER_NOT_JWS` on a nested inner JWS — the same codes those layers already use for any unparseable body, so the size refusal is not distinguishable on the wire. The refusal class is API-visible via `errors.Is` on the error chain to framework-internal callers, and is deliberately not logged. go-jose imposes no header-size bound of its own, and the only other ceiling is the 10 MiB body cap, which a negative `MaxResponseBytes` can disable on the client transport. ECDSA support is gated on extending `keystore.KeyStore` to return ECDSA keys (tracked in [#347](https://github.com/gaborage/go-bricks/issues/347))
- **Hybrid error envelope**: pre-trust failures (decrypt failed, signature invalid) emit a plaintext minimal `{code,message}` envelope to leak nothing to unauthenticated peers; post-trust handler errors emit the standard `APIResponse` envelope, encrypted with the route's outbound policy; its `details` follow the same `app.debug` AND development-`app.env` gate as the standard envelope (ADR-084), so a production error body carries `code`, `message` and `meta` only
- **Fail-Fast at startup**: every `kid` is resolved against the keystore at `RegisterHandler` time. Missing keys, asymmetric tags, and `WithRawResponse()` conflicts panic at startup, never at runtime
- **Observability**: spans (`jose.decode_request`, `jose.encode_response`), failure counter (`jose.failures.total` by code/direction), duration histogram (`jose.operation.duration`)

**Tag syntax:**

```go
type CreateTokenRequest struct {
    _   struct{} `jose:"decrypt=our-signing,verify=visa-vts-verify"`
    PAN string   `json:"pan" validate:"required"`
}

type CreateTokenResponse struct {
    _     struct{} `jose:"sign=our-signing,encrypt=visa-vts-encrypt"`
    Token string   `json:"token"`
}
```

**Tag keys (all kids are case-sensitive: runs of `[A-Za-z0-9_-]` joined by single dots, so a dotted keystore name such as `tokens.our` is a kid as written — [ADR-144](adr_144_dotted_keystore_entry_names.md); `..` or a leading or trailing `.` is `JOSE_TAG_KID_INVALID`):**

- Request: `decrypt` (our private key), `verify` (peer public key)
- Response: `sign` (our private key), `encrypt` (peer public key)
- Optional everywhere: `sig_alg` (default `RS256`), `key_alg` (default `RSA-OAEP-256`), `enc` (default `A256GCM`), `cty` (default `application/json`)

**Wiring:**

```yaml
keystore:
  keys:
    our-signing:
      public:  { file: certs/our-signing.pub.der }
      private: { file: certs/our-signing.key.der }
    visa-vts-encrypt:
      public:  { value: ${VISA_VTS_ENCRYPT_PUB_B64} }
    visa-vts-verify:
      public:  { value: ${VISA_VTS_VERIFY_PUB_B64} }
```

```go
// Register the keystore module BEFORE any module declaring jose-tagged routes.
// app/module_registry.go automatically wires deps.KeyStore + deps.Logger +
// deps.Tracer + deps.MeterProvider into the JOSE middleware.
for _, m := range []app.Module{
    keystore.NewModule(),
    &payments.TokensModule{}, // declares jose-tagged routes
} {
    if err := fw.RegisterModule(m); err != nil {
        log.Fatal(err)
    }
}
```

**Failure mode → IAPIError mapping (every code surfaces on the wire).** The rows are listed in evaluation order: the Content-Type is checked before the body is read, so a wrong-Content-Type request is rejected with 415 without its body being consumed. The cap on the read that follows is hard: it holds even when `server.bodylimit` is raised above it. A request whose `Content-Length` exceeds `server.bodylimit` is rejected by echo's `BodyLimit` middleware before the JOSE path runs, and that rejection carries the framework's **standard** error envelope rather than the minimal pre-trust one. The two limits are equal by default, so it is raising `server.bodylimit` above the JOSE cap that puts oversize rejections back on the minimal-envelope path.

| Failure | Status | Code |
| --- | --- | --- |
| Wrong Content-Type (not `application/jose`) | 415 | `JOSE_PLAINTEXT_REJECTED` |
| Body over the 10 MiB JOSE cap, or an unknown-length body overflowing a lower `server.bodylimit` mid-stream | 413 | `JOSE_BODY_TOO_LARGE` |
| Body required / empty | 400 | `JOSE_BODY_REQUIRED` |
| Compact JWE parse failure | 400 | `JOSE_MALFORMED` |
| `enc`/`alg` not allowed on the wire | 400 | `JOSE_MALFORMED` |
| `alg=none` (downgrade attempt) | 400 | `JOSE_MALFORMED` (rejected by allowlist parse) |
| Header missing `kid` (not raised when a bare or minimal JWS-of-JWE inbound policy sets `AcceptMissingKid` and the header has no `kid` member) | 401 | `JOSE_KID_MISSING` |
| Unknown `kid` in header | 401 | `JOSE_KID_UNKNOWN` |
| Decryption failed | 401 | `JOSE_DECRYPT_FAILED` |
| Inner payload not a JWS | 400 | `JOSE_INNER_NOT_JWS` |
| Body is not a compact JWS under a `SealModeJWSofJWE` or `SealModeJWSofJWEMinimal` policy (a JWE-outer body included) | 400 | `JOSE_OUTER_NOT_JWS` |
| Outer `alg` is not the declared `Policy.SigAlg` (`SealModeJWSofJWE` and `SealModeJWSofJWEMinimal` only) | 400 | `JOSE_ALGORITHM_DISALLOWED` |
| JWS signature invalid | 401 | `JOSE_SIGNATURE_INVALID` |
| Inner JWS `cty` disagrees with policy | 400 | `JOSE_CTY_REJECTED` |
| Outbound seal failed (server-side) | 500 | `JOSE_OUTBOUND_FAILED` |

`JOSE_ALGORITHM_DISALLOWED` is raised at registration time (an invalid `jose:` struct tag or `Policy.Validate()` failure) on every mode, and additionally at request time in the two JWS-of-JWE modes alone (`SealModeJWSofJWE`, `SealModeJWSofJWEMinimal`), where `Open` refuses an outer `alg` other than the declared `Policy.SigAlg` with a 400 before any key is touched. A disallowed `alg`/`enc` on the wire fails go-jose's compact parse instead, which surfaces as `JOSE_MALFORMED` (or `JOSE_INNER_NOT_JWS` for the inner-JWS layer) above.

**Security invariant** (asserted by tests): a response is JOSE-encrypted iff inbound was successfully verified AND the route has an outbound policy. Tampered-byte negative tests must produce *plaintext* error responses; observing `Content-Type: application/jose` on the failure path is a security regression.

**Sealed body shape** depends on the handler's return type:

| Handler returns | Sealed JWE payload |
| --- | --- |
| Bare value / `Result[R]` / `NoContentResult` | Bare `data` (no envelope) |
| `ResultWithMeta[R]` / any `ResultEnvelopeProvider` | Standard `{data, meta}` envelope with framework-managed `timestamp` and `traceId` merged in |
| `IAPIError` (post-trust handler failure) | Standard `{error, meta}` envelope (`buildErrorEnvelope`); `error.details` only under `app.debug` + a development `app.env` (ADR-084) |

Vanilla `Result[R]` continues to seal raw `data` so VTS-style vendor-prescribed JSON shapes work unchanged. Handlers explicitly opt into envelope semantics by returning `ResultWithMeta` (see [handler_patterns.md](handler_patterns.md#custom-envelope-meta-resultwithmetar)).

**Replay protection**: the framework verifies the JWS signature and exposes verified claims via `jose.ClaimsFromContext(ctx)`. Applications enforce `iat`/`exp`/`jti` policies (Visa skew rules vary by product); `jose.CheckJTIReplay(ctx, recorder, claims, window)` provides the cache-backed `jti` half. `CheckJTIReplay` requires a non-empty `claims.Issuer` — iss-less token profiles must call `jose.CheckJTIReplayInNamespace(ctx, recorder, policy.VerifyKid, claims, window)` instead, so partners sharing no issuer don't collide on the same jti namespace.

**Test utilities** (`jose/testing/`):

- `GenerateTestKeyPair(t)` — 2048-bit RSA pair for fast tests
- `NewTestResolver(map[string]any{kid: key})` — in-memory KeyResolver
- `SealForTest(t, payload, policy, resolver)` — produce compact JWE for arrange step
- `OpenForTest(t, compact, policy, resolver)` — decrypt + verify in assert step

**For complete examples**, see [llms.txt](../llms.txt) JOSE section.

**Outbound httpclient JOSE wrapping** (calls TO Visa): `httpclient.JOSETransport` is an `http.RoundTripper` (`httpclient/jose_transport.go`) that **seals** outbound request bodies via `jose.Seal` and **opens** inbound response bodies via `jose.Open`. What those mean is the policy's `Mode`: sign+encrypt and decrypt+verify on the nested JWE-of-JWS default, encrypt-only and decrypt-only on a `SealModeBareJWE` policy, which carries no signature to verify, encrypt-then-sign / verify-then-decrypt on a `SealModeJWSofJWE` policy, and verify-then-decrypt only on an `Inbound` `SealModeJWSofJWEMinimal` policy, which never seals. It sits below the httpclient retry loop, so for a request that carries a body and has an `Outbound` policy set, each retry attempt produces a freshly-sealed request — with a bare-mode `iat` recomputed at seal time only when `Policy.IATMillis` is `true` (important for protocols requiring unique `iat`/`jti` claims per attempt). Configure via `Inner` (delegate transport), `Outbound`/`Inbound` (`*jose.Policy`), `Resolver` (`jose.KeyResolver`), `MaxResponseBytes` (caps the inbound response read; defaults to `DefaultMaxJOSEBodyBytes`, 10 MiB), and the optional `Envelope` (a `httpclient.BodyEnvelope`, whose `Wrap`/`Unwrap` shape the body on the wire). With `Envelope` nil — the default — the compact itself is the request body and only `application/jose` responses are unwrapped, other Content-Types on a FAILURE status passing through untouched, mirroring the server's hybrid error envelope; when `Envelope` and `Inbound` are both set, it replaces that Content-Type rule with `Unwrap`'s verdict and buffers every eligible response body (still capped) before it runs. **A 2xx must have been unwrapped**, in either mode: a successful response the `Inbound` policy never opened is refused as `httpclient.ErrJOSEPlaintextResponse` (body closed, never handed to the caller or to a response interceptor, the error and one WARN naming the status and the peer), because a ciphertext stripped in transit is otherwise indistinguishable from a legitimate plaintext reply — `AllowPlaintextSuccess` opts a whole transport out as a Strangler-migration knob (ADR-107 amendment). `httpclient.VisaMLEEnvelope()` returns the `BodyEnvelope` implementing Visa MLE's `{"encData":"<compact>"}` JSON envelope, recognized inbound by shape — see [httpclient.md](httpclient.md#jose-body-envelopes-visa-message-level-encryption). Only bodies are protected. A request that carries no body is not sealed and goes out with its headers unchanged, whatever the method — so a payload-free `POST` is *not* signed either. A response that net/http guarantees is empty (`204`, `304`, and any reply to `HEAD`) is returned with an empty body even when it advertises `application/jose`, and is never a plaintext-success violation — there is no body to mistake for a payload, and bytes a hand-rolled `Inner` supplies there anyway are closed and dropped rather than handed on. `1xx` is not uniformly bodyless: net/http consumes every informational response except `101` before the final one, so a `RoundTripper` never sees them, while `101 Switching Protocols` IS terminal and its `Body` is the read/write upgrade stream — being below `200` it is skipped on the status alone, passed through with its body intact, neither unwrapped nor refused; without an `Envelope`, every other `application/jose` response is decrypted and verified as before. The boundary is deliberately net/http's guarantee rather than the RFC's bodyless set — `205` and a `2xx` answer to `CONNECT` carry no body per RFC 9110, but net/http reads one anyway, so skipping them would hand a peer's unverified bytes to the caller under a status code it chose. See `httpclient/jose_transport_test.go` for usage examples.

## Bare-JWE mode (Visa Message Level Encryption)

Visa **Message Level Encryption** does not use the nested shape above: an MLE payload is a *single* compact JWE with **no inner JWS**, `enc` is `A128GCM`, the protected header carries `typ: "JOSE"` and an `iat` in Unix epoch **milliseconds**, and the sender is authenticated **out of band** — Visa's `X-Pay-Token` header and mTLS do that job, not a signature over the body. `Policy.Mode` selects that shape (ADR-107). Those specifics are Visa's *profile*, not bare mode's floor: the framework emits `typ` only when `Policy.Typ` is set and `iat` only when `Policy.IATMillis` is `true`, a bare `Open` accepts a token carrying neither, and `Policy.Enc` may be `A128GCM` or `A256GCM`:

| `Policy.Mode` | Wire shape | Content encryption allowed |
| --- | --- | --- |
| `jose.SealModeJWEofJWS` (zero value) | `JWE(JWS(payload))` — sign-then-encrypt / decrypt-then-verify | `A256GCM` |
| `jose.SealModeBareJWE` | `JWE(payload)` — encrypt only, no signature | `A128GCM` · bare mode only, `A256GCM` |
| `jose.SealModeJWSofJWE` | `JWS(JWE(payload))` — encrypt-then-sign / verify-then-decrypt | `A256GCM` |
| `jose.SealModeJWSofJWEMinimal` | `JWS(JWE(payload))` — verify-then-decrypt only (open-only), `RS512` outer signature | `A256GCM` |

`RSA-OAEP-256` is the only key-wrapping algorithm in every mode; `alg=none`, `HS*`, `RSA1_5` and ECDSA stay rejected in all of them. `IsAllowedEncFor(mode, enc)` and `AllowedContentEncsFor(mode)` are the mode-aware predicates — `IsAllowedEnc`/`AllowedContentEncs` keep the JWE-of-JWS meaning, so the nested floor stays `A256GCM`, and an unknown mode yields an empty allowlist that rejects every token. Field-level sealing of AMQP events (`jose/sealed`, ADR-097) is a different door and is unaffected: it stays `A256GCM`.

**`KeyAlg` and `Enc` are read on the way in, not only on the way out.** On an outbound policy they are what `Seal` writes; on an **inbound** policy they are what `Open` accepts — it narrows the parser's allowlist to exactly the declared values, in every mode. A bare inbound policy declaring `Enc: josev4.A128GCM` therefore refuses an `A256GCM` token even though bare mode admits both: that token is not the shape the deployment agreed with the peer. The refusal is `JOSE_MALFORMED`, raised by go-jose's compact parse before any key material is touched. `Validate` already rejects a value that is off the mode's allowlist, so declaring one can only ever narrow, never widen. Leaving `Enc` or `KeyAlg` unset on an inbound policy keeps the whole mode-wide allowlist — reachable only with a hand-built policy, since both the tag parser and `Validate` insist on a value.

**Use it only when the peer is authenticated out of band.** Nothing in bare mode verifies a signature, so a successful `Open` proves only that the payload was encrypted to your public key — which any holder of that public key can do. Without mTLS or a partner token such as `X-Pay-Token`, turning bare mode on removes sender authentication from that route.

**Policy pair** (outbound to the peer, inbound from the peer). `A128GCM` has no go-bricks alias, so the go-jose constant is imported under its own name:

```go
import (
    "github.com/gaborage/go-bricks/jose"
    josev4 "github.com/go-jose/go-jose/v4"
)

outbound := &jose.Policy{
    Direction:  jose.DirectionOutbound,
    Mode:       jose.SealModeBareJWE,
    EncryptKid: "visa-mle-encrypt",       // peer public key; the ONLY kid a bare outbound policy may set
    KeyAlg:     jose.DefaultKeyAlg,       // RSA-OAEP-256
    Enc:        josev4.A128GCM,
    Cty:        jose.DefaultCty,          // application/json
    Typ:        "JOSE",                   // JWE protected `typ`
    IATMillis:  true,                     // stamp `iat` in epoch MILLISECONDS at seal time
    ProtectedHeaders: map[string]any{     // copied verbatim into the protected header
        "iss": "acme-payments",
    },
}

inbound := &jose.Policy{
    Direction:  jose.DirectionInbound,
    Mode:       jose.SealModeBareJWE,
    DecryptKid: "our-mle-decrypt",        // our private key; the ONLY kid a bare inbound policy may set
    KeyAlg:     jose.DefaultKeyAlg,       // inbound: the only key-wrapping alg Open accepts
    Enc:        josev4.A128GCM,           // inbound: the only content encryption Open accepts
    Cty:        jose.DefaultCty,
}

compact, err := jose.Seal(payload, outbound, resolver) // -> one compact JWE, five segments
if err != nil {
    return err
}
plaintext, claims, hdr, err := jose.Open(compact, inbound, resolver)
if err != nil {
    return err
}
```

**Validation rules** (all enforced by `Policy.Validate()`, and by `Seal` itself before it touches the keystore):

- A bare **outbound** policy declares `EncryptKid` as its **only key identity** — a `SignKid`, `VerifyKid`, `DecryptKid` or any `SigAlg` is `JOSE_POLICY_DIRECTION_MISMATCH`, because bare mode signs nothing. Everything else the policy configures (`KeyAlg`, `Enc`, `Cty`, `Typ`, `IATMillis`, `ProtectedHeaders`) is allowed, as the example above sets. A bare **inbound** policy declares `DecryptKid` as its only key identity, on the same terms — a `SignKid`, `VerifyKid`, `EncryptKid` or any `SigAlg` is the same code.
- `Typ`, `ProtectedHeaders` and `IATMillis` are **outbound only, and address a JWE the framework builds directly** — bare mode's token, or the inner JWE of `SealModeJWSofJWE`. On a `SealModeJWEofJWS` policy they are `JOSE_POLICY_MODE_MISMATCH` whatever the direction, since the mode check runs first; on a bare-JWE or JWS-of-JWE *inbound* policy they are `JOSE_POLICY_DIRECTION_MISMATCH` — nothing would read them on the way in, and accepting them would suggest a header was being enforced.
- `AcceptMissingKid` is **inbound only**, in bare mode and in `SealModeJWSofJWEMinimal` (below), and `OmitKid` **bare-mode, outbound only**: on a `SealModeJWEofJWS` or `SealModeJWSofJWE` policy either one is `JOSE_POLICY_MODE_MISMATCH` whatever the direction, as is `OmitKid` on a minimal JWS-of-JWE policy, since the mode check runs first, and in the wrong direction it is `JOSE_POLICY_DIRECTION_MISMATCH`. Neither makes a kid field optional: a bare inbound policy without `DecryptKid`, or a bare outbound one without `EncryptKid`, is still `JOSE_POLICY_INCOMPLETE`.
- **Collision guard**: a `ProtectedHeaders` key naming a param the framework writes (`alg`, `enc`, `kid`, `cty`, `typ`) or one JOSE reserves is `JOSE_POLICY_HEADER_COLLISION`, never a silent overwrite — as is a hand-written `iat` beside `IATMillis: true`. That is why `typ` is its own field.
- An unrecognized `Mode` is `JOSE_POLICY_MODE_UNKNOWN`. All of these codes are configuration failures raised at validation time; they never reach an HTTP caller. `JOSE_ALGORITHM_DISALLOWED` is the one code raised at both times — a request-time 400 in both JWS-of-JWE modes, where `Open` pins the outer `alg` to `Policy.SigAlg`.

**What `Open` returns.** The plaintext is the caller's bytes verbatim (there is no JWS to unwrap), `claims` are parsed out of that payload if it carries JWT claims, and the header comes back as `OpenHeader.JWE` — with `.Typ` and `.IATMillis` (epoch milliseconds, `0` when absent or malformed) beside the existing `.Kid`/`.Alg`/`.Enc`/`.Cty`. `OpenHeader.JWS` is the **zero** `jose.Header`: no inner layer exists. A peer that declares a `cty` must agree with the policy's (`JOSE_CTY_REJECTED`), one that omits it is accepted — the same permissive rule the nested path applies. **A bare `Open` refuses `cty: JWS` unconditionally**, whatever `Policy.Cty` says (`JOSE_CTY_REJECTED`): bare mode never carries an inner JWS, so that header means a peer is still sending the nested shape, and the compact JWS must never reach the caller as if it were the payload. `Cty` is therefore the consumer's own content-type pin, not the nested-token guard.

**`jose` does not judge the inbound `iat`.** It reports it and nothing more — the same stance ADR-097 takes on replay for sealed events. The value is a peer-written header — integrity-protected by the JWE authentication tag, but not sender-authenticated — and the tolerance is partner-specific, so the freshness check is the caller's, as `iat`/`exp`/`jti` on the nested path already are.

**Kid-less peers.** A peer that names keys out of band may send a JWE with no `kid` at all. A bare inbound policy opts in with `AcceptMissingKid: true`; `DecryptKid` stays required, because it names the resolver key, not the wire header. Only a literally **absent** `kid` member is excused. A member that is present is compared exactly as before, so `"kid": ""` and `"kid": null` are `JOSE_KID_UNKNOWN` under the flag (go-jose reads a `null` member as absent; go-bricks fails closed), and a non-string `kid` stays `JOSE_MALFORMED`. Every other bare check still runs. The opt-in costs one diagnostic: a kid-less token sealed to another key fails `JOSE_DECRYPT_FAILED`, since there is no kid to report as unknown. After a kid-less open, `OpenHeader.JWE.Kid` is `""`; it is never filled in from `DecryptKid`. The other direction is `OmitKid: true` on a bare outbound policy: `Seal` then writes neither the `kid` header member nor a recipient key id, `EncryptKid` still selects the key, and `Cty`, `Typ`, `IATMillis` and `ProtectedHeaders` are written exactly as without it; the collision guard still refuses a hand-written `kid`. See [ADR-150](adr_150_jose_missing_kid_policy_opt_in.md).

**Reach**: bare mode is reachable from `jose.Seal` / `jose.Open` (and `jose/testing`'s `SealForTest` / `OpenForTest`, which call them), from `httpclient.Builder.WithJOSE` for outbound calls, and from the `seal-payload` CLI's `-mode bare` (below) — `Build()` skips the `SigAlg` default on a bare-mode policy, so the pair above passes validation as written. There is still no `mode` key in the `jose:` struct-tag grammar, so inbound server routes cannot select it. `AcceptMissingKid` and `OmitKid` reach `jose.Open` / `jose.Seal` and the `Inbound` / `Outbound` policies of `httpclient.Builder.WithJOSE` through the policy alone, with no tag key and no `seal-payload` flag. Visa MLE's `{"encData":"<compact>"}` body envelope is carried by `httpclient`'s `Envelope` field (a `BodyEnvelope`) and the ready-made `httpclient.VisaMLEEnvelope()` — see [httpclient.md](httpclient.md#jose-body-envelopes-visa-message-level-encryption). Inbound, an `Unwrap` that declines a body is a pass-through only on a failure status: on a 2xx it is `httpclient.ErrJOSEPlaintextResponse`, so an MLE peer that answers some successful routes in plaintext needs `AllowPlaintextSuccess` set while that lasts — see [httpclient.md](httpclient.md#successful-responses-must-have-been-unwrapped).

## JWS-of-JWE mode (Visa Token Service Issuer API)

Visa's **Token Service Issuer** API inverts the nesting: the body is a compact **JWS whose payload is a compact JWE**, so the signature is the outer layer. `Policy.Mode: jose.SealModeJWSofJWE` selects that shape (ADR-111). `Seal` builds exactly the JWE bare mode builds — `RSA-OAEP-256` + `A256GCM`, `kid`, `Policy.Typ`, `Policy.ProtectedHeaders`, and a millisecond `iat` when `Policy.IATMillis` is set — and signs that compact string **verbatim** as the JWS payload. `Open` reverses it: verify first, then decrypt.

The outer protected header is **fixed by the mode**, not by the policy: `alg` (from `Policy.SigAlg`), `kid` (from `Policy.SignKid`), `typ: "JOSE"`, `cty: "JWE"`, and `iat` in Unix epoch **seconds** — always, whatever `Policy.IATMillis` says about the inner JWE. Setting `Policy.Typ` therefore changes the inner `typ` only.

> **Visa requires `PS256`. Set `SigAlg` explicitly.** The package default is `RS256` (`jose.DefaultSigAlg`), and both algorithms are on the allowlist, so a policy that omits `SigAlg` builds and seals happily — and is then rejected by Visa, not at startup.

`Policy.Cty` is **not written** in this mode: the wire shape carries no inner `cty`, and `httpclient.Builder.WithJOSE` fills `Cty` with its default for every mode, so `Seal` drops it rather than emitting a header Visa does not expect.

**What `Open` enforces**, each with its own code: the body must be a 3-segment compact JWS (`JOSE_OUTER_NOT_JWS` — a 5-segment JWE-outer body is poison here, never a fallback to another mode); the outer `alg` must be exactly `Policy.SigAlg`, checked before any key is touched (`JOSE_ALGORITHM_DISALLOWED`); the signature must verify against `Policy.VerifyKid` (`JOSE_SIGNATURE_INVALID`, or `JOSE_KID_MISSING` / `JOSE_KID_UNKNOWN`); the verified outer header must declare `cty: "JWE"` (`JOSE_CTY_REJECTED`). Only then is the payload handed to the bare opener with `Policy.DecryptKid`.

**Header reporting.** `OpenHeader.JWS` carries the outer layer and `OpenHeader.JWE` the inner one. `OpenHeader.JWS.IATMillis` is always `0` here: the outer `iat` is seconds, and that field states milliseconds. Neither `iat` is judged, exactly as in the other modes.

**Key separation.** Do not point a `DecryptKid` at a route in this mode *and* a bare-JWE route. The inner JWE lifted out of a signed body decrypts on the bare route, where nothing authenticates the sender. An outer signature also attests who *sent* the body, not who encrypted it.

```go
outbound := &jose.Policy{
    Direction:  jose.DirectionOutbound,
    Mode:       jose.SealModeJWSofJWE,
    SignKid:    "our-vts-signing",         // our private key
    EncryptKid: "visa-vts-encrypt",        // peer public key
    SigAlg:     josev4.PS256,              // Visa requires PS256; the default is RS256
    KeyAlg:     jose.DefaultKeyAlg,        // RSA-OAEP-256
    Enc:        josev4.A256GCM,
    Typ:        "JOSE",                    // inner JWE `typ`
    IATMillis:  true,                      // inner JWE `iat`, epoch MILLISECONDS
}

inbound := &jose.Policy{
    Direction:  jose.DirectionInbound,
    Mode:       jose.SealModeJWSofJWE,
    DecryptKid: "our-vts-decrypt",
    VerifyKid:  "visa-vts-verify",
    SigAlg:     josev4.PS256,              // Open accepts this algorithm and no other
    KeyAlg:     jose.DefaultKeyAlg,
    Enc:        josev4.A256GCM,
}

compact, err := jose.Seal(payload, outbound, resolver) // -> a 3-segment JWS over a 5-segment JWE
if err != nil {
    return err
}

// Verify-then-decrypt. hdr.JWS is the outer layer, hdr.JWE the inner one.
plaintext, claims, hdr, err := jose.Open(compact, inbound, resolver)
if err != nil {
    return err
}
```

**Validation rules**: an outbound policy requires `SignKid` **and** `EncryptKid`; an inbound one requires `VerifyKid` **and** `DecryptKid`; a cross-direction kid is `JOSE_POLICY_DIRECTION_MISMATCH`. `SigAlg` must be on the signature allowlist — this mode signs, so leaving it unset is `JOSE_ALGORITHM_DISALLOWED`. **Which door you come through decides whether unset is even reachable**: `httpclient.Builder.Build()` fills an unset `SigAlg` with `jose.DefaultSigAlg` (`RS256`) before it validates, so an omitted one silently becomes RS256 and Visa rejects the signature at runtime; a hand-built policy handed straight to `jose.Seal` / `jose.Open` is refused at once, because nothing defaults it there. Set `SigAlg: josev4.PS256` explicitly either way. `Enc` is `A256GCM` only. The `ProtectedHeaders` collision guard applies to the inner JWE exactly as in bare mode.

**Reach**: `jose.Seal` / `jose.Open` (and `jose/testing`'s `SealForTest` / `OpenForTest`), and `httpclient.Builder.WithJOSE` in both directions — `Build()` applies the `SigAlg` default here, because this mode signs. There is no `mode` key in the `jose:` struct-tag grammar, so inbound server routes cannot select it.

Byte-stable vectors for this shape live in `jose/testdata/jwsofjwe_vectors.json`, built with go-jose directly rather than through `Seal`; the positive vector's outer signature is checked for the 32-byte PSS salt nimbus-jose-jwt produces. Regenerate with `go test ./jose -update`.

## Minimal JWS-of-JWE mode (kid-less inbound peers)

Some inbound peers sign the JWS-of-JWE shape their own way: an `RS512` outer signature, no `kid` on either token, and no outer `cty`. `Policy.Mode: jose.SealModeJWSofJWEMinimal` opens exactly that (ADR-150). The wire shape is the one `SealModeJWSofJWE` opens, an outer compact JWS whose payload is the inner JWE compact string verbatim, and `Open` runs the same path: verify first, then decrypt. The mode is **open-only**.

**What `Open` enforces**, each with its own code: the body must be a 3-segment compact JWS (`JOSE_OUTER_NOT_JWS`, which also covers a 5-segment JWE body and, as in `SealModeJWSofJWE`, an outer `kid` that is not a string); the outer `alg` must be exactly `Policy.SigAlg` (`RS512`), checked before any key is touched (`JOSE_ALGORITHM_DISALLOWED`); the signature must verify against `Policy.VerifyKid` (`JOSE_SIGNATURE_INVALID`, or `JOSE_KID_MISSING` / `JOSE_KID_UNKNOWN`); the verified outer `cty` must be absent or exactly `"JWE"` (`JOSE_CTY_REJECTED`; a present `cty` of any other value, `""` and `null` included, is refused). Only then is the payload handed to the bare opener with `Policy.DecryptKid`, so the inner JWE keeps every bare-mode check: `RSA-OAEP-256` + `A256GCM` only (`JOSE_MALFORMED` otherwise), `cty: JWS` refused unconditionally, and a `cty` contradicting `Policy.Cty` refused (`JOSE_CTY_REJECTED`). `typ` and `iat` are not judged. `OpenHeader.JWS` reports the outer JWS (its `Kid` is `""` when the wire carries none, and `IATMillis` is always `0`), `OpenHeader.JWE` the inner one.

**`kid` on both tokens.** `AcceptMissingKid: true` covers the outer JWS and the inner JWE alike. A `kid` member that is absent passes; one that is present must still equal `VerifyKid` (outer) or `DecryptKid` (inner), so a wrong `kid`, `"kid": ""` and `"kid": null` are each `JOSE_KID_UNKNOWN`. With the flag off, an absent member, `""` and `null` are all `JOSE_KID_MISSING`. `VerifyKid` and `DecryptKid` stay required: they name resolver keys, not wire headers. The outer `kid` is checked before the signature, and the inner JWE is reached only after the outer verifies, so with the flag off a kid-less request reads `JOSE_KID_MISSING` for every refusal behind the outer `kid` check; only the segment count, the `alg` pin and go-jose's outer parse fire ahead of it.

**Detached signatures.** These peers usually send the JWE as a JSON member of the body and the outer JWS in a request header without its payload (RFC 7515 Appendix F): the detached-signature envelope. `Open` takes only the attached compact, so rebuild it first with `jose.AttachDetachedJWS(detached, jwe)`, which returns `header.base64url(jwe).signature`. The helper does no crypto and touches no key: `Open` verifies the result. It trims surrounding whitespace from `detached` and applies the compact checks every door applies (the 16 KiB protected-header bound, base64url only), then requires exactly three segments with an empty middle and a non-empty header and signature. An embedded payload, a 5-segment JWE, a JSON serialization, a leftover `JWS ` scheme prefix and an empty `jwe` are each a 400 `JOSE_MALFORMED`; `Cause` is set only when the shared compact checks refused. `jwe` is encoded verbatim, never trimmed or parsed, because its exact bytes are what was signed; `Open` applies every inner-JWE check after the signature verifies. HTTP framing stays consumer code: finding the header, stripping its scheme, refusing a missing or duplicated one, and capping the whole header value.

> **Signed input.** The mode rests on one fact: the detached signature's payload is the exact JWE compact string. That was confirmed against a published profile sample only, not against live traffic. A peer that signs the raw or re-serialized body instead cannot be opened by this mode.

**Key separation.** As in `SealModeJWSofJWE`, do not point one `DecryptKid` at a route in this mode *and* a bare-JWE route. The inner JWE lifted out of a signed body decrypts on the bare route, where nothing authenticates the sender.

```go
inbound := &jose.Policy{
    Direction:        jose.DirectionInbound,
    Mode:             jose.SealModeJWSofJWEMinimal,
    DecryptKid:       "our-decrypt",      // our private key
    VerifyKid:        "peer-verify",      // peer public key
    SigAlg:           josev4.RS512,       // required: the only signature this mode admits
    KeyAlg:           jose.DefaultKeyAlg, // RSA-OAEP-256
    Enc:              josev4.A256GCM,
    AcceptMissingKid: true,               // the peer writes no kid on either token
}

// detached is the signature header's value, scheme stripped; jwe is the body's JWE member.
attached, err := jose.AttachDetachedJWS(detached, jwe)
if err != nil {
    return err
}
plaintext, claims, hdr, err := jose.Open(attached, inbound, resolver)
if err != nil {
    return err
}
```

**Validation rules**: only an inbound policy validates; an outbound one is `JOSE_POLICY_DIRECTION_MISMATCH`, so `Seal` refuses it before touching the resolver. `SigAlg` must be `RS512`: `RS256`, `PS256` or unset is `JOSE_ALGORITHM_DISALLOWED`, and `RS512` stays refused in every other mode. `Enc` is `A256GCM` only. `VerifyKid` and `DecryptKid` are required (`JOSE_POLICY_INCOMPLETE`); `SignKid`, `EncryptKid`, `Typ`, `ProtectedHeaders` and `IATMillis` are `JOSE_POLICY_DIRECTION_MISMATCH`; `OmitKid` is `JOSE_POLICY_MODE_MISMATCH`.

**Reach**: `jose.Open` and `jose/testing`'s `OpenForTest`; `SealForTest` cannot reach it, because `Seal` refuses the mode. By construction it is also reachable as an `httpclient` `JOSEConfig.Inbound` policy through `Builder.WithJOSE`, inbound only, for an attached compact JWS(JWE) response body: the transport has no detached-signature envelope. `Build()` defaults an unset `SigAlg` to `RS256` and then fails with `JOSE_ALGORITHM_DISALLOWED`, so set `RS512` explicitly; an `Outbound` policy in this mode fails `Build()` with `JOSE_POLICY_DIRECTION_MISMATCH`. There is no `Seal`, no `jose:` struct-tag key and no tagged server route.

## Serving a detached-signature envelope peer (imperative recipe)

Some inbound peers send a [minimal JWS-of-JWE](#minimal-jws-of-jwe-mode-kid-less-inbound-peers) token in a detached-signature envelope: the JWE travels in the JSON body and its detached signature in a request header. [ADR-150](adr_150_jose_missing_kid_policy_opt_in.md) keeps the server side of such a peer in consumer code: no `jose:` tag, route option or middleware serves it. This recipe composes the existing doors on an untagged route. A typed handler registered with `server.WithRawResponse()` rebuilds the attached JWS with `jose.AttachDetachedJWS`, opens it with `jose.Open` under a `SealModeJWSofJWEMinimal` inbound policy with `AcceptMissingKid`, and seals the reply's `data` value with a `SealModeBareJWE` outbound policy with `OmitKid`.

The tagged path cannot serve this peer, and the recipe does not try: its inbound gate requires `Content-Type: application/jose` (415 `JOSE_PLAINTEXT_REJECTED`), the tag grammar has no mode key, and a `jose:`-tagged route combined with `WithRawResponse` panics at registration.

### Wire shape

- **Request body:** `application/json` `{"data":"<compact JWE>"}`. The JWE is `RSA-OAEP-256` + `A256GCM` with no `kid`.
- **Signature header:** a request header the consumer names (here `X-Example-Signature`) carries `JWS <detached compact JWS>`: three segments, the middle one empty, `alg` `RS512`, no `kid`.
- **Signed bytes:** the detached payload is the exact JWE compact string from `data` (RFC 7515 Appendix F).
- **Bodyless requests:** a request without a body carries no JWE, so there is nothing for a detached signature to sign. The recipe's bodyless route reads no signature header.
- **Reply:** the consumer's own JSON envelope. When the reply has a top-level `data` value, that value is replaced by a compact JWE sealed to the tenant's response-seal key. Every other member stays plaintext, a reply without `data` is plaintext, and no reply carries a signature.
- **Body cap:** on an untagged route the request body is bounded by `server.bodylimit` alone; the tagged path's 10 MiB JOSE read cap does not apply.

> **Caveat: the signed bytes.** This recipe works only when the peer signs the JWE compact string itself. A peer that signs the raw request body, or a re-serialized body, fails every request with `JOSE_SIGNATURE_INVALID`, and the recipe does not apply to it.

The tenant comes from the framework's tenant resolution: with `multitenant.enabled` and `multitenant.resolver.type: header`, the middleware reads the header `multitenant.resolver.header` names (default `X-Tenant-ID`), refuses an unresolvable request with 400 "Invalid tenant" before any handler runs, and the handler reads the result with `multitenant.TenantID`. Resolution is identification, not authorization: the tenant header is caller-written (see note a).

### Example

`jose.KeyResolver` takes no context, so the handler builds one per request from the resolved tenant's keys. `Order`, `Receipt` and the two interfaces stand in for the consumer's own types. A test-backed copy lives in `jose/example_test.go`; the two stay identical except for import aliases.

```go
import (
    "context"
    "crypto/rsa"
    "encoding/json"
    "errors"
    "net/http"
    "strings"

    josev4 "github.com/go-jose/go-jose/v4"

    "github.com/gaborage/go-bricks/config"
    "github.com/gaborage/go-bricks/jose"
    "github.com/gaborage/go-bricks/logger"
    "github.com/gaborage/go-bricks/multitenant"
    "github.com/gaborage/go-bricks/server"
)

// Resolver key names for one tenant's three keys. They name keys inside the
// per-request resolver, not wire headers: the peer sends no kid. If it ever
// sends one, it must equal the constant, or Open refuses it (JOSE_KID_UNKNOWN).
const (
    peerVerifyKid = "peer-verify" // peer public key: verifies the detached signature
    ourDecryptKid = "our-decrypt" // our private key: opens the request JWE
    peerSealKid   = "peer-seal"   // peer public key: seals the reply data

    // maxSignatureHeaderBytes bounds the header value before any parse (DoS bound).
    maxSignatureHeaderBytes = 8 << 10
)

// The consumer's own reply codes.
const (
    codeSignatureHeaderInvalid = "SIGNATURE_HEADER_INVALID"
    codeDataMissing            = "DATA_MISSING"
    codePayloadInvalid         = "PAYLOAD_INVALID"
    codeInternal               = "INTERNAL"
    codeRequestRejected        = "REQUEST_REJECTED"
)

var errUnknownKey = errors.New("unknown key name")

// consumerCodes maps jose codes into the consumer's own vocabulary; any other
// code answers codeRequestRejected.
var consumerCodes = map[string]string{
    "JOSE_SIGNATURE_INVALID": "SIGNATURE_INVALID",
    "JOSE_KID_UNKNOWN":       "KEY_UNKNOWN",
    "JOSE_MALFORMED":         "MALFORMED",
    "JOSE_OUTER_NOT_JWS":     "MALFORMED",
}

type envelopeConfig struct {
    SignatureHeader string `config:"example.envelope.signatureheader" required:"true"` // e.g. X-Example-Signature
}

type envelopeRequest struct {
    Data string `json:"data"`
}

// statusRequest is the bodyless twin's request: a struct (ADR-121) with no data field.
type statusRequest struct{}

// envelopeReply is the consumer's envelope. Only Data is ever sealed.
type envelopeReply struct {
    Code string `json:"code,omitempty"`
    Data string `json:"data,omitempty"`
}

// TenantKeySet is one tenant's key material, parsed by the consumer (PEM included).
type TenantKeySet struct {
    PeerVerify *rsa.PublicKey
    OurDecrypt *rsa.PrivateKey
    PeerSeal   *rsa.PublicKey
}

// TenantKeys is the consumer's per-tenant key source. ForTenant runs on every request,
// so it returns keys already parsed and cached.
type TenantKeys interface {
    ForTenant(ctx context.Context, tenantID string) (TenantKeySet, error)
}

// Operations is the consumer's business logic; Order and Receipt are its own types.
type Operations interface {
    Submit(ctx context.Context, tenantID string, in Order) (Receipt, error)
    Status(ctx context.Context, tenantID string) (Receipt, error)
}

// tenantKeyStore adapts one tenant's keys to jose.KeyStoreLike. A nil key answers
// errUnknownKey: jose.KeyStoreResolver passes a nil key through unchanged.
type tenantKeyStore TenantKeySet

func (s tenantKeyStore) PrivateKey(name string) (*rsa.PrivateKey, error) {
    if name == ourDecryptKid && s.OurDecrypt != nil {
        return s.OurDecrypt, nil
    }
    return nil, errUnknownKey
}

func (s tenantKeyStore) PublicKey(name string) (*rsa.PublicKey, error) {
    if name == peerVerifyKid && s.PeerVerify != nil {
        return s.PeerVerify, nil
    }
    if name == peerSealKid && s.PeerSeal != nil {
        return s.PeerSeal, nil
    }
    return nil, errUnknownKey
}

func inboundPolicy() *jose.Policy {
    return &jose.Policy{
        Direction:        jose.DirectionInbound,
        Mode:             jose.SealModeJWSofJWEMinimal,
        DecryptKid:       ourDecryptKid, // resolver key name, not a wire header
        VerifyKid:        peerVerifyKid,
        SigAlg:           josev4.RS512,
        KeyAlg:           jose.DefaultKeyAlg,
        Enc:              jose.DefaultEnc, // A256GCM; pins Open to exactly this
        AcceptMissingKid: true,
        // Cty left empty: the inner cty is not pinned.
    }
}

func outboundPolicy() *jose.Policy {
    return &jose.Policy{
        Direction:  jose.DirectionOutbound,
        Mode:       jose.SealModeBareJWE,
        EncryptKid: peerSealKid,
        KeyAlg:     jose.DefaultKeyAlg, // required: Validate refuses an empty KeyAlg
        Enc:        jose.DefaultEnc,    // required: Validate refuses an empty Enc
        OmitKid:    true,
        // Cty and Typ empty: Seal writes no cty or typ header.
    }
}

type envelopeHandler struct {
    cfg    envelopeConfig
    keys   TenantKeys
    ops    Operations
    log    logger.Logger
    bearer []server.MiddlewareFunc // e.g. auth.Middleware(verifier); see note a
}

// newEnvelopeHandler is called from the module's Init with deps.Config and deps.Logger.
func newEnvelopeHandler(
    cfg *config.Config, keys TenantKeys, ops Operations, log logger.Logger, bearer ...server.MiddlewareFunc,
) (*envelopeHandler, error) {
    h := &envelopeHandler{keys: keys, ops: ops, log: log, bearer: bearer}
    if err := cfg.InjectInto(&h.cfg); err != nil {
        return nil, err
    }
    return h, nil
}

func (h *envelopeHandler) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
    g := r.Group("/example", h.bearer...)
    server.POST(hr, g, "/orders", h.submit, server.WithRawResponse())
    server.GET(hr, g, "/orders/status", h.status, server.WithRawResponse())
}

func (h *envelopeHandler) submit(req envelopeRequest, ctx server.HandlerContext) (server.Result[envelopeReply], server.IAPIError) {
    // Header.Values, not RequestHeader: Header.Get cannot see a duplicated header.
    values := ctx.Request().Header.Values(h.cfg.SignatureHeader)
    if len(values) != 1 { // missing or duplicated
        return refuse(http.StatusUnauthorized, codeSignatureHeaderInvalid), nil
    }
    if len(values[0]) > maxSignatureHeaderBytes {
        return refuse(http.StatusBadRequest, codeSignatureHeaderInvalid), nil
    }
    detached, ok := strings.CutPrefix(values[0], "JWS ")
    if !ok {
        return refuse(http.StatusUnauthorized, codeSignatureHeaderInvalid), nil
    }
    // Refused here, not by a validate tag, so the consumer's envelope renders. This also
    // catches a body the binder skipped because Content-Type was missing or not JSON.
    if req.Data == "" {
        return refuse(http.StatusBadRequest, codeDataMissing), nil
    }

    rctx := ctx.RequestContext()
    tenantID, resolver, err := h.tenantResolver(rctx)
    if err != nil {
        return h.fail(err), nil
    }
    compact, err := jose.AttachDetachedJWS(detached, req.Data)
    if err != nil {
        return h.fail(err), nil
    }
    plaintext, _, _, err := jose.Open(compact, inboundPolicy(), resolver)
    if err != nil {
        return h.fail(err), nil
    }
    // Replay protection goes here, now that Open returned nil (note c).

    // The opened plaintext is unvalidated input: the pipeline's validate tags ran
    // on envelopeRequest only.
    var in Order
    if json.Unmarshal(plaintext, &in) != nil || in.Validate() != nil {
        return refuse(http.StatusBadRequest, codePayloadInvalid), nil
    }

    out, err := h.ops.Submit(rctx, tenantID, in)
    if err != nil {
        return h.fail(err), nil
    }
    return h.sealReply(out, resolver), nil
}

// status is the bodyless twin: nothing was signed, so it calls neither
// AttachDetachedJWS nor Open, but it still seals the reply data (note b).
func (h *envelopeHandler) status(_ statusRequest, ctx server.HandlerContext) (server.Result[envelopeReply], server.IAPIError) {
    rctx := ctx.RequestContext()
    tenantID, resolver, err := h.tenantResolver(rctx)
    if err != nil {
        return h.fail(err), nil
    }
    out, err := h.ops.Status(rctx, tenantID)
    if err != nil {
        return h.fail(err), nil
    }
    return h.sealReply(out, resolver), nil
}

func (h *envelopeHandler) tenantResolver(ctx context.Context) (string, jose.KeyResolver, error) {
    tenantID, err := multitenant.TenantID(ctx)
    if err != nil {
        return "", nil, err // ErrNoTenant: a wiring fault (multi-tenancy off, or a skipped route), so a 500
    }
    keys, err := h.keys.ForTenant(ctx, tenantID)
    if err != nil {
        return "", nil, err
    }
    return tenantID, jose.NewKeyStoreResolver(tenantKeyStore(keys)), nil
}

// sealReply runs on success paths only, so every error reply stays plaintext.
func (h *envelopeHandler) sealReply(out Receipt, resolver jose.KeyResolver) server.Result[envelopeReply] {
    body, err := json.Marshal(out)
    if err != nil {
        return h.fail(err)
    }
    sealed, err := jose.Seal(body, outboundPolicy(), resolver)
    if err != nil {
        return h.fail(err)
    }
    return server.Result[envelopeReply]{Data: envelopeReply{Data: sealed}}
}

// fail maps any error to the consumer's envelope. Only jerr.Code is logged for a jose
// refusal; Cause, Error() and plaintext never reach the wire.
func (h *envelopeHandler) fail(err error) server.Result[envelopeReply] {
    var jerr *jose.Error
    if !errors.As(err, &jerr) {
        h.log.Error().Err(err).Msg("detached-signature envelope failed")
        return refuse(http.StatusInternalServerError, codeInternal)
    }
    h.log.Warn().Str("jose_code", jerr.Code).Msg("detached-signature envelope refused")
    status := jerr.Status
    if status == 0 { // Validate refusals and KeyStoreResolver lookup errors carry no status
        status = http.StatusInternalServerError
    }
    code, ok := consumerCodes[jerr.Code]
    if !ok {
        code = codeRequestRejected
    }
    return refuse(status, code)
}

func refuse(status int, code string) server.Result[envelopeReply] {
    return server.Result[envelopeReply]{Data: envelopeReply{Code: code}, Status: status}
}
```

### Notes

**a. Sender authentication.** On a body-carrying request, the detached `RS512` signature proves the sender holds the verify key the resolver chose for the header-named tenant. It covers the JWE compact string only, not the method, path, query, the caller-written tenant header or any other body member, so a signed body replayed to another route of the same tenant also verifies. The tie to the tenant holds only while each tenant has its own verify key: if two tenants share one verify key, a body signed for one verifies under the other tenant's header. A bodyless request carries no JWE, so nothing is signed over it; the bearer credential and the transport are its only sender authentication. `auth.Middleware(v)` on the route group is the framework's bearer door, and it is identification, not authorization: the handler still has to authorize the principal for the resolved tenant, because the tenant header is caller-written.

**b. Reply sealing departs from the tagged path's rule.** The tagged path's invariant is that a response is sealed only when the inbound request verified (see the security invariant above). This recipe seals the reply `data` on every method, bodyless ones included, where nothing was verified. Sealing to the tenant's key gives confidentiality toward that key's holder; it does not authenticate the requester. The minimal mode is open-only, so the reply is a bare seal and carries no signature.

**c. Replay.** The `jose` package does not judge replay. A sound dedup key is a digest (for example SHA-256) of the JWE compact string from `data`, computed only after `jose.Open` returned nil. Before the detached signature has verified, two different strings can decode to the same bytes: go-jose's non-strict base64url decode lets a segment's spare trailing bits vary without changing the decoded bytes. Record the key through `jose.ReplayRecorder.GetOrSet` (`cache.Cache`, from `deps.Cache(ctx)`, satisfies it) under a tenant-namespaced key, with a window at least the consumer's acceptance horizon. Keep one ledger per tenant, shared across every route of the profile, not one per route, because the signature does not cover the route (note a). `jose.CheckJTIReplay` and `jose.CheckJTIReplayInNamespace` apply only when the opened plaintext carries a `jti` claim (they read the `*Claims` `Open` returns); the JWE-string digest works whether or not it does. A canonical-base64url pre-check is tracked in [#2164](https://github.com/gaborage/go-bricks/issues/2164).

**d. Telemetry.** On this path the framework records no `jose.*` span or metric: only the tagged path records `jose.decode_request`, `jose.encode_response`, `jose.failures.total` and `jose.operation.duration`, and the `jose` package itself imports no OpenTelemetry. The consumer instruments its own work (for example through `deps.Tracer` or `deps.MeterProvider`) and never records `Cause` or plaintext. `jose.IsInboundVerified` and `jose.ClaimsFromContext` stay unset on this path.

**e. Keys.** The consumer loads the three keys per tenant. The keystore module reads DER or PKCS#12 key material, not PEM, and is not used here: the consumer parses its own per-tenant keys (PEM included) and hands `*rsa` keys to its resolver. The request-decrypt key must not also back a bare-JWE inbound route, the minimal mode's [key-separation rule](#minimal-jws-of-jwe-mode-kid-less-inbound-peers): the inner JWE lifted out of a signed body would decrypt on the bare route, where nothing authenticates the sender.

**f. Algorithms.** `A256CBC-HS512` and SHA-1 `RSA-OAEP` are not supported in any mode (an ADR-150 non-goal); a peer that uses them fails `Open` at the parser allowlist. The minimal mode accepts only an `RS512` outer signature, from its own table; `IsAllowedSigAlg(RS512)` stays false, because the global table is unchanged.

**g. Refusals.**

| Input | Refused by | Code | Status |
| --- | --- | --- | --- |
| Signature header missing, duplicated, over the bound, or without the `JWS ` scheme | consumer handler | consumer's own | consumer's choice |
| Empty `data`, including a body the binder skipped (no JSON `Content-Type`) | consumer handler | consumer's own | consumer's choice |
| Detached token with an embedded payload, a non-compact value, or a header segment over 16 KiB | `jose.AttachDetachedJWS` | `JOSE_MALFORMED` | 400 |
| Header segment that is not a JSON object, or an outer `kid` that is not a string | `jose.Open` | `JOSE_OUTER_NOT_JWS` | 400 |
| Outer `alg` other than `RS512` (checked before any key lookup) | `jose.Open` | `JOSE_ALGORITHM_DISALLOWED` | 400 |
| A `kid` present on the outer JWS or the inner JWE (`""` and `null` included) that is not the resolver key name | `jose.Open` | `JOSE_KID_UNKNOWN` | 401 |
| Tampered JWE string | `jose.Open` | `JOSE_SIGNATURE_INVALID` | 401 |
| Outer `cty` present and not `JWE` (`""`, `null` and a non-string value included), or inner `cty: JWS` | `jose.Open` | `JOSE_CTY_REJECTED` | 400 |
| Unsupported `alg` or `enc` on the inner JWE | `jose.Open` | `JOSE_MALFORMED` | 400 |
| A key the tenant's store does not hold (the example's store also answers so for a nil key) | `jose.KeyStoreResolver` | `JOSE_KID_UNKNOWN` | 0 (the recipe answers 500) |

## Sealing test payloads with curl (seal-payload CLI)

> This section is about HTTP request bodies. Field-level sealing of AMQP **events** (the
> `seal` tag, `jose/sealed`) is a separate door with its own page: [sealing.md](sealing.md).

Exercising a jose-tagged endpoint with `curl` requires a valid nested `JWE(JWS(payload))` body — hand-writing one is impractical outside Go. `cmd/seal-payload` is a small CLI that seals a JSON payload with fixture keys using `jose.Seal` and the keystore's own DER-loading semantics (via `internal/keymaterial`), so a sealed payload is one the middleware will accept by construction.

Install:

```sh
go install github.com/gaborage/go-bricks/cmd/seal-payload@latest
```

Generate DER fixture keys with openssl (one pair per role — matches the DER formats keystore accepts):

```sh
# Caller signing pair — its PUBLIC half is what the endpoint's verify= entry
# holds in the server keystore (sign.pub.der is what you configure there)
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -outform DER -out sign.der
openssl pkey -inform DER -in sign.der -pubout -outform DER -out sign.pub.der

# Encryption public key — the SERVER's public key, whose private half the
# endpoint's decrypt= entry names; extract the PKIX DER public half
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -outform DER -out enc.der
openssl pkey -inform DER -in enc.der -pubout -outform DER -out enc.pub.der
```

Seal a payload and POST it:

```sh
echo '{"pan":"4111111111111111"}' | seal-payload \
  -sign-key-file sign.der -encrypt-key-file enc.pub.der \
  -sign-kid visa-vts-verify -encrypt-kid our-signing > sealed.txt

curl -X POST https://api.example.com/v1/tokens \
  -H "Content-Type: application/jose" \
  --data-binary @sealed.txt
```

**Kid rule**: `-sign-kid` must equal the target endpoint's `verify=` tag name, and `-encrypt-kid` must equal its `decrypt=` tag name — the server binds kid headers to the policy's configured kids, and a mismatch fails with `JOSE_KID_UNKNOWN`. Both pass through verbatim: `tokens.our` and `tokens-our` are two different kids.

**Partner kids**: the kid of a tag is the keystore entry name and travels on the wire unchanged, so a partner that pins kids sees exactly that string. A dotted name (`tokens.our`, settable with a POSIX `export` as `KEYSTORE_KEYS_TOKENS_OUR_*`) is a legal kid under RFC 7515 §4.1.4, which gives `kid` no structure, but confirm the partner accepts `.` before using one, and rename a live partner kid only as part of a key rotation with that partner ([keystore.md](keystore.md#entry-names), runbook R2).

**Bare mode (Visa MLE)**: `-mode bare` (default `nested`) emits one compact JWE with no inner JWS. It takes only the encryption key and `-encrypt-kid`; `-sign-key-file`, `-sign-key-value`, `-sign-kid` or `-sig-alg` is refused by name. `-enc A128GCM` is accepted under bare only, and `-typ`, `-iat-ms` (millisecond `iat`) and repeatable `-protected key=value` (string values) are bare-only — under nested, the CLI refuses each by name (`-typ requires -mode bare`). `-envelope visa-mle` prints `{"encData":"<compact>"}` instead of the bare token — the envelope is a body wrapper, so it applies in nested mode too, but Visa's own MLE endpoints expect a bare `A128GCM` JWE inside it, which is what `-mode bare` mints:

```sh
echo '{"pan":"4111111111111111"}' | seal-payload -mode bare \
  -encrypt-key-file enc.pub.der -encrypt-kid visa-mle-key \
  -enc A128GCM -typ JOSE -iat-ms -protected channel=mobile \
  -envelope visa-mle > body.json

curl -X POST https://sandbox.api.visa.com/v1/example \
  -H "Content-Type: application/json" --data-binary @body.json
```

The response comes back sealed too — decrypting it is out of the CLI's scope (v1 only produces outbound tokens); standalone Go programs unwrap it with `jose.Open`, first pulling the compact token out of `encData` when the peer answers with the same `{"encData":"…"}` envelope (`jose.Open` parses a compact token, not the JSON wrapper — `httpclient` does this extraction itself when the builder is given `httpclient.VisaMLEEnvelope()`); `jose/testing.OpenForTest` is for Go test code only (it requires a `testing.TB`). For the Go-test-side equivalent of sealing a payload, see `jose/testing.SealForTest` above.

```go
var body struct{ EncData string `json:"encData"` }
json.NewDecoder(resp.Body).Decode(&body)
plaintext, claims, hdr, err := jose.Open(body.EncData, inbound, resolver)
```
