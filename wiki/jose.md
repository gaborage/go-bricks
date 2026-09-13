# JOSE Middleware (Deep Dive)

The `jose` package provides nested JWE-of-JWS protection on HTTP request and response bodies — sign-then-encrypt outbound and decrypt-then-verify inbound on every payload. It is designed for **Visa Token Services**-style integrations and any partner API that requires this level of payload protection. That nested shape is the default and everything below describes it; a policy can select one of two other shapes instead — the encrypt-only [Bare-JWE mode](#bare-jwe-mode-visa-message-level-encryption) Visa Message Level Encryption specifies, or the encrypt-then-sign [JWS-of-JWE mode](#jws-of-jwe-mode-visa-token-service-issuer-api) the Visa Token Service Issuer API expects.

## JOSE Middleware

The `jose` package provides nested JWE-of-JWS protection on HTTP request and response bodies. Designed for **Visa Token Services**-style integrations and any partner API that requires sign-then-encrypt outbound and decrypt-then-verify inbound on every payload.

**Key Features:**

- **Struct-tag opt-in**: Add a `jose:` tag to a sentinel field on the request/response type — no per-route plumbing
- **Bidirectional symmetry enforced**: both request and response must carry tags or neither (registration-time check)
- **Strict algorithm allowlist**: `RS256`/`PS256` for signing; `RSA-OAEP-256` + `A256GCM` for encryption on this nested path — `A128GCM` is admitted too, but *only* behind `Policy.Mode: jose.SealModeBareJWE` (see [Bare-JWE mode](#bare-jwe-mode-visa-message-level-encryption)). `alg=none`, `HS*`, `RSA1_5`, and `ES256` are rejected at parse time. ECDSA support is gated on extending `keystore.KeyStore` to return ECDSA keys (tracked in [#347](https://github.com/gaborage/go-bricks/issues/347))
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

**Tag keys (all kids are case-sensitive, charset `[A-Za-z0-9_-]+`):**

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
| Header missing `kid` | 401 | `JOSE_KID_MISSING` |
| Unknown `kid` in header | 401 | `JOSE_KID_UNKNOWN` |
| Decryption failed | 401 | `JOSE_DECRYPT_FAILED` |
| Inner payload not a JWS | 400 | `JOSE_INNER_NOT_JWS` |
| Body is not a compact JWS under a `SealModeJWSofJWE` policy (a JWE-outer body included) | 400 | `JOSE_OUTER_NOT_JWS` |
| Outer `alg` is not the declared `Policy.SigAlg` (`SealModeJWSofJWE` only) | 400 | `JOSE_ALGORITHM_DISALLOWED` |
| JWS signature invalid | 401 | `JOSE_SIGNATURE_INVALID` |
| Inner JWS `cty` disagrees with policy | 400 | `JOSE_CTY_REJECTED` |
| Outbound seal failed (server-side) | 500 | `JOSE_OUTBOUND_FAILED` |

`JOSE_ALGORITHM_DISALLOWED` is raised at registration time (an invalid `jose:` struct tag or `Policy.Validate()` failure) on every mode, and additionally at request time in `SealModeJWSofJWE` alone, where `Open` refuses an outer `alg` other than the declared `Policy.SigAlg` with a 400 before any key is touched. A disallowed `alg`/`enc` on the wire fails go-jose's compact parse instead, which surfaces as `JOSE_MALFORMED` (or `JOSE_INNER_NOT_JWS` for the inner-JWS layer) above.

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

**Outbound httpclient JOSE wrapping** (calls TO Visa): `httpclient.JOSETransport` is an `http.RoundTripper` (`httpclient/jose_transport.go`) that **seals** outbound request bodies via `jose.Seal` and **opens** inbound response bodies via `jose.Open`. What those mean is the policy's `Mode`: sign+encrypt and decrypt+verify on the nested JWE-of-JWS default, encrypt-only and decrypt-only on a `SealModeBareJWE` policy, which carries no signature to verify, and encrypt-then-sign / verify-then-decrypt on a `SealModeJWSofJWE` policy. It sits below the httpclient retry loop, so for a request that carries a body and has an `Outbound` policy set, each retry attempt produces a freshly-sealed request — with a bare-mode `iat` recomputed at seal time only when `Policy.IATMillis` is `true` (important for protocols requiring unique `iat`/`jti` claims per attempt). Configure via `Inner` (delegate transport), `Outbound`/`Inbound` (`*jose.Policy`), `Resolver` (`jose.KeyResolver`), `MaxResponseBytes` (caps the inbound response read; defaults to `DefaultMaxJOSEBodyBytes`, 10 MiB), and the optional `Envelope` (a `httpclient.BodyEnvelope`, whose `Wrap`/`Unwrap` shape the body on the wire). With `Envelope` nil — the default — the compact itself is the request body and only `application/jose` responses are unwrapped, other Content-Types passing through untouched, mirroring the server's hybrid error envelope; when `Envelope` and `Inbound` are both set, it replaces that Content-Type rule with `Unwrap`'s verdict and buffers every eligible response body (still capped) before it runs. `httpclient.VisaMLEEnvelope()` returns the `BodyEnvelope` implementing Visa MLE's `{"encData":"<compact>"}` JSON envelope, recognized inbound by shape — see [httpclient.md](httpclient.md#jose-body-envelopes-visa-message-level-encryption). Only bodies are protected. A request that carries no body is not sealed and goes out with its headers unchanged, whatever the method — so a payload-free `POST` is *not* signed either. A response that net/http guarantees is empty (`1xx`, `204`, `304`, and any reply to `HEAD`) is returned as-is even when it advertises `application/jose`; without an `Envelope`, every other `application/jose` response is decrypted and verified as before. The boundary is deliberately net/http's guarantee rather than the RFC's bodyless set — `205` and a `2xx` answer to `CONNECT` carry no body per RFC 9110, but net/http reads one anyway, so skipping them would hand a peer's unverified bytes to the caller under a status code it chose. See `httpclient/jose_transport_test.go` for usage examples.

## Bare-JWE mode (Visa Message Level Encryption)

Visa **Message Level Encryption** does not use the nested shape above: an MLE payload is a *single* compact JWE with **no inner JWS**, `enc` is `A128GCM`, the protected header carries `typ: "JOSE"` and an `iat` in Unix epoch **milliseconds**, and the sender is authenticated **out of band** — Visa's `X-Pay-Token` header and mTLS do that job, not a signature over the body. `Policy.Mode` selects that shape (ADR-107). Those specifics are Visa's *profile*, not bare mode's floor: the framework emits `typ` only when `Policy.Typ` is set and `iat` only when `Policy.IATMillis` is `true`, a bare `Open` accepts a token carrying neither, and `Policy.Enc` may be `A128GCM` or `A256GCM`:

| `Policy.Mode` | Wire shape | Content encryption allowed |
| --- | --- | --- |
| `jose.SealModeJWEofJWS` (zero value) | `JWE(JWS(payload))` — sign-then-encrypt / decrypt-then-verify | `A256GCM` |
| `jose.SealModeBareJWE` | `JWE(payload)` — encrypt only, no signature | `A128GCM` · bare mode only, `A256GCM` |
| `jose.SealModeJWSofJWE` | `JWS(JWE(payload))` — encrypt-then-sign / verify-then-decrypt | `A256GCM` |

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
- **Collision guard**: a `ProtectedHeaders` key naming a param the framework writes (`alg`, `enc`, `kid`, `cty`, `typ`) or one JOSE reserves is `JOSE_POLICY_HEADER_COLLISION`, never a silent overwrite — as is a hand-written `iat` beside `IATMillis: true`. That is why `typ` is its own field.
- An unrecognized `Mode` is `JOSE_POLICY_MODE_UNKNOWN`. All of these codes are configuration failures raised at validation time; they never reach an HTTP caller. `JOSE_ALGORITHM_DISALLOWED` is the one code raised at both times — a request-time 400 in `SealModeJWSofJWE`, where `Open` pins the outer `alg` to `Policy.SigAlg`.

**What `Open` returns.** The plaintext is the caller's bytes verbatim (there is no JWS to unwrap), `claims` are parsed out of that payload if it carries JWT claims, and the header comes back as `OpenHeader.JWE` — with `.Typ` and `.IATMillis` (epoch milliseconds, `0` when absent or malformed) beside the existing `.Kid`/`.Alg`/`.Enc`/`.Cty`. `OpenHeader.JWS` is the **zero** `jose.Header`: no inner layer exists. A peer that declares a `cty` must agree with the policy's (`JOSE_CTY_REJECTED`), one that omits it is accepted — the same permissive rule the nested path applies. **A bare `Open` refuses `cty: JWS` unconditionally**, whatever `Policy.Cty` says (`JOSE_CTY_REJECTED`): bare mode never carries an inner JWS, so that header means a peer is still sending the nested shape, and the compact JWS must never reach the caller as if it were the payload. `Cty` is therefore the consumer's own content-type pin, not the nested-token guard.

**`jose` does not judge the inbound `iat`.** It reports it and nothing more — the same stance ADR-097 takes on replay for sealed events. The value is a peer-written header — integrity-protected by the JWE authentication tag, but not sender-authenticated — and the tolerance is partner-specific, so the freshness check is the caller's, as `iat`/`exp`/`jti` on the nested path already are.

**Reach**: bare mode is reachable from `jose.Seal` / `jose.Open` (and `jose/testing`'s `SealForTest` / `OpenForTest`, which call them), and from `httpclient.Builder.WithJOSE` for outbound calls — `Build()` skips the `SigAlg` default on a bare-mode policy, so the pair above passes validation as written. There is still no `mode` key in the `jose:` struct-tag grammar, so inbound server routes cannot select it. Visa MLE's `{"encData":"<compact>"}` body envelope is carried by `httpclient`'s `Envelope` field (a `BodyEnvelope`) and the ready-made `httpclient.VisaMLEEnvelope()` — see [httpclient.md](httpclient.md#jose-body-envelopes-visa-message-level-encryption).

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

**Validation rules**: an outbound policy requires `SignKid` **and** `EncryptKid`; an inbound one requires `VerifyKid` **and** `DecryptKid`; a cross-direction kid is `JOSE_POLICY_DIRECTION_MISMATCH`. `SigAlg` must be on the signature allowlist — this mode signs, so leaving it unset is `JOSE_ALGORITHM_DISALLOWED`. `Enc` is `A256GCM` only. The `ProtectedHeaders` collision guard applies to the inner JWE exactly as in bare mode.

**Reach**: `jose.Seal` / `jose.Open` (and `jose/testing`'s `SealForTest` / `OpenForTest`), and `httpclient.Builder.WithJOSE` in both directions — `Build()` applies the `SigAlg` default here, because this mode signs. There is no `mode` key in the `jose:` struct-tag grammar, so inbound server routes cannot select it.

Byte-stable vectors for this shape live in `jose/testdata/jwsofjwe_vectors.json`, built with go-jose directly rather than through `Seal`; the positive vector's outer signature is checked for the 32-byte PSS salt nimbus-jose-jwt produces. Regenerate with `go test ./jose -update`.

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

**Kid rule**: `-sign-kid` must equal the target endpoint's `verify=` tag name, and `-encrypt-kid` must equal its `decrypt=` tag name — the server binds kid headers to the policy's configured kids, and a mismatch fails with `JOSE_KID_UNKNOWN`.

The response comes back sealed too — decrypting it is out of the CLI's scope (v1 only produces outbound tokens); standalone Go programs unwrap it with `jose.Open`; `jose/testing.OpenForTest` is for Go test code only (it requires a `testing.TB`). For the Go-test-side equivalent of sealing a payload, see `jose/testing.SealForTest` above.
