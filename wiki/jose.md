# JOSE Middleware (Deep Dive)

The `jose` package provides nested JWE-of-JWS protection on HTTP request and response bodies — sign-then-encrypt outbound and decrypt-then-verify inbound on every payload. It is designed for **Visa Token Services**-style integrations and any partner API that requires this level of payload protection.

## JOSE Middleware

The `jose` package provides nested JWE-of-JWS protection on HTTP request and response bodies. Designed for **Visa Token Services**-style integrations and any partner API that requires sign-then-encrypt outbound and decrypt-then-verify inbound on every payload.

**Key Features:**

- **Struct-tag opt-in**: Add a `jose:` tag to a sentinel field on the request/response type — no per-route plumbing
- **Bidirectional symmetry enforced**: both request and response must carry tags or neither (registration-time check)
- **Strict algorithm allowlist**: `RS256`/`PS256` for signing; `RSA-OAEP-256` + `A256GCM` for encryption. `alg=none`, `HS*`, `RSA1_5`, and `ES256` are rejected at parse time. ECDSA support is gated on extending `keystore.KeyStore` to return ECDSA keys (tracked in [#347](https://github.com/gaborage/go-bricks/issues/347))
- **Hybrid error envelope**: pre-trust failures (decrypt failed, signature invalid) emit a plaintext minimal `{code,message}` envelope to leak nothing to unauthenticated peers; post-trust handler errors emit the standard `APIResponse` envelope, encrypted with the route's outbound policy
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
|---|---|---|
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
| JWS signature invalid | 401 | `JOSE_SIGNATURE_INVALID` |
| Inner JWS `cty` disagrees with policy | 400 | `JOSE_CTY_REJECTED` |
| Outbound seal failed (server-side) | 500 | `JOSE_OUTBOUND_FAILED` |

`JOSE_ALGORITHM_DISALLOWED` is raised only at registration time (an invalid `jose:` struct tag or `Policy.Validate()` failure) — it is never returned to an HTTP caller at request time. A disallowed `alg`/`enc` on the wire fails go-jose's compact parse instead, which surfaces as `JOSE_MALFORMED` (or `JOSE_INNER_NOT_JWS` for the inner-JWS layer) above.

**Security invariant** (asserted by tests): a response is JOSE-encrypted iff inbound was successfully verified AND the route has an outbound policy. Tampered-byte negative tests must produce *plaintext* error responses; observing `Content-Type: application/jose` on the failure path is a security regression.

**Sealed body shape** depends on the handler's return type:

| Handler returns | Sealed JWE payload |
|---|---|
| Bare value / `Result[R]` / `NoContentResult` | Bare `data` (no envelope) |
| `ResultWithMeta[R]` / any `ResultEnvelopeProvider` | Standard `{data, meta}` envelope with framework-managed `timestamp` and `traceId` merged in |
| `IAPIError` (post-trust handler failure) | Standard `{error, meta}` envelope (`buildErrorEnvelope`) |

Vanilla `Result[R]` continues to seal raw `data` so VTS-style vendor-prescribed JSON shapes work unchanged. Handlers explicitly opt into envelope semantics by returning `ResultWithMeta` (see [handler_patterns.md](handler_patterns.md#custom-envelope-meta-resultwithmetar)).

**Replay protection**: the framework verifies the JWS signature and exposes verified claims via `jose.ClaimsFromContext(ctx)`. Applications enforce `iat`/`exp`/`jti` policies (Visa skew rules vary by product); `jose.CheckJTIReplay(ctx, recorder, claims, window)` provides the cache-backed `jti` half. `CheckJTIReplay` requires a non-empty `claims.Issuer` — iss-less token profiles must call `jose.CheckJTIReplayInNamespace(ctx, recorder, policy.VerifyKid, claims, window)` instead, so partners sharing no issuer don't collide on the same jti namespace.

**Test utilities** (`jose/testing/`):

- `GenerateTestKeyPair(t)` — 2048-bit RSA pair for fast tests
- `NewTestResolver(map[string]any{kid: key})` — in-memory KeyResolver
- `SealForTest(t, payload, policy, resolver)` — produce compact JWE for arrange step
- `OpenForTest(t, compact, policy, resolver)` — decrypt + verify in assert step

**For complete examples**, see [llms.txt](../llms.txt) JOSE section.

**Outbound httpclient JOSE wrapping** (calls TO Visa): `httpclient.JOSETransport` is an `http.RoundTripper` (`httpclient/jose_transport.go`) that signs+encrypts outbound request bodies via `jose.Seal` and decrypts+verifies inbound response bodies via `jose.Open`. It sits below the httpclient retry loop so each retry attempt produces a freshly-sealed request (important for protocols requiring unique `iat`/`jti` claims per attempt). Configure via `Inner` (delegate transport), `Outbound`/`Inbound` (`*jose.Policy`), `Resolver` (`jose.KeyResolver`), and `MaxResponseBytes` (caps the inbound response read; defaults to `DefaultMaxJOSEBodyBytes`, 10 MiB). Only `application/jose` responses are unwrapped — other Content-Types pass through untouched, mirroring the server's hybrid error envelope. Only bodies are protected. A request that carries no body is not sealed and goes out with its headers unchanged, whatever the method — so a payload-free `POST` is *not* signed either. A response that net/http guarantees is empty (`1xx`, `204`, `304`, and any reply to `HEAD`) is returned as-is even when it advertises `application/jose`; every other `application/jose` response is decrypted and verified as before. The boundary is deliberately net/http's guarantee rather than the RFC's bodyless set — `205` and a `2xx` answer to `CONNECT` carry no body per RFC 9110, but net/http reads one anyway, so skipping them would hand a peer's unverified bytes to the caller under a status code it chose. See `httpclient/jose_transport_test.go` for usage examples.

## Sealing test payloads with curl (seal-payload CLI)

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
