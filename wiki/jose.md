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
fw.RegisterModules(
    keystore.NewModule(),
    &payments.TokensModule{}, // declares jose-tagged routes
)
```

**Failure mode → IAPIError mapping (every code surfaces on the wire):**

| Failure | Status | Code |
|---|---|---|
| Body required / empty | 400 | `JOSE_BODY_REQUIRED` |
| Wrong Content-Type (not `application/jose`) | 415 | `JOSE_PLAINTEXT_REJECTED` |
| Compact JWE parse failure | 400 | `JOSE_MALFORMED` |
| `enc`/`alg` not allowed | 400 | `JOSE_ALGORITHM_DISALLOWED` |
| `alg=none` (downgrade attempt) | 401 | `JOSE_NONE_ALG_REJECTED` (rejected by allowlist parse) |
| Header missing `kid` | 401 | `JOSE_KID_MISSING` |
| Unknown `kid` in header | 401 | `JOSE_KID_UNKNOWN` |
| Decryption failed | 401 | `JOSE_DECRYPT_FAILED` |
| Inner payload not a JWS | 400 | `JOSE_INNER_NOT_JWS` |
| JWS signature invalid | 401 | `JOSE_SIGNATURE_INVALID` |
| Inner JWS `cty` disagrees with policy | 400 | `JOSE_CTY_REJECTED` |
| Outbound seal failed (server-side) | 500 | `JOSE_OUTBOUND_FAILED` |

**Security invariant** (asserted by tests): a response is JOSE-encrypted iff inbound was successfully verified AND the route has an outbound policy. Tampered-byte negative tests must produce *plaintext* error responses; observing `Content-Type: application/jose` on the failure path is a security regression.

**Replay protection**: the framework verifies the JWS signature and exposes verified claims via `jose.ClaimsFromContext(ctx)`. Applications enforce `iat`/`exp`/`jti` policies (Visa skew rules vary by product).

**Test utilities** (`jose/testing/`):
- `GenerateTestKeyPair(t)` — 2048-bit RSA pair for fast tests
- `NewTestResolver(map[string]any{kid: key})` — in-memory KeyResolver
- `SealForTest(t, payload, policy, resolver)` — produce compact JWE for arrange step
- `OpenForTest(t, compact, policy, resolver)` — decrypt + verify in assert step

**For complete examples**, see [llms.txt](../llms.txt) JOSE section. Outbound httpclient JOSE wrapping (calls TO Visa) is planned for a follow-up release; the current scope covers server in/out only.
