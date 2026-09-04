# KeyStore (Deep Dive)

The `keystore` package provides named key-material management for GoBricks
applications: **RSA key pairs** (signing/encryption, consumed by the JOSE
middleware) and **raw symmetric secrets** (HMAC/CMAC keys, HKDF input keying
material). Both live under one custody and rotation story — a file in local
dev, a base64 env var / secrets-manager value in deployed environments, loaded
once at startup and held read-only in memory.

**Key Features:**

- **One custody story** for asymmetric and symmetric material — no parallel,
  un-audited secret path, no deriving a MAC key off an RSA private key
- **Per-entry RSA *or* secret**, never both — a mixed entry is a startup config
  error via structural detection (no `kind:` discriminator needed)
- **Defensive copies**: `Secret` returns a fresh slice the caller owns and may
  zeroize; the in-memory master is never handed out
- **Fail-fast minimum length** for secrets (32 bytes, mandatory — a set value
  can only raise it) so a too-short
  key is rejected at startup rather than silently weakening a digest
- **Fail-fast at startup**: any entry that cannot be loaded, parsed, mismatched
  (RSA pair), or is below the floor (secret) aborts boot

## Configuration

```yaml
keystore:
  secretminlength: 32          # 32 when absent; a set value can only raise it — below 32 fails startup (ADR-095)
  keys:
    signing:                     # RSA pair (public required, private optional)
      public:
        file: "certs/signing_public.der"        # local dev
      private:
        value: "${SIGNING_PRIVATE_KEY_BASE64}"  # deployed (base64 DER)
    mac-key:                     # symmetric secret entry — names match ^[a-z0-9-]+$
      secret:
        file: "certs/mac-key.bin"               # local dev: raw key bytes
    mac-key-deployed:
      secret:
        value: "${MAC_KEY_BASE64}"              # deployed: base64 raw key
    vts:                         # RSA pair from a password-protected PKCS#12 bundle
      pkcs12:
        file: "certs/vts.p12"                   # or value: base64 of the .p12/.pfx bytes
        password:
          env: "VTS_P12_PASSWORD"               # the variable's NAME; or file: a mounted secret
```

The `<name>` must match `^[a-z0-9-]+$`, so the entry stays addressable by environment
variable: an underscored or uppercase name fails startup
([ADR-090](adr_090_env_reachable_section_names.md)), and a hyphenated one is settable only
where the runtime permits `-` in a variable name, which Docker and Kubernetes do and POSIX
`export` does not.

Each `keys.<name>` entry resolves to **exactly one** of the following shapes:

| Shape | Required | Notes |
| --- | --- | --- |
| `public` (+ optional `private`) | `public` | RSA pair. PKCS8 with PKCS1 fallback for private; public/private mismatch is a startup error |
| `secret` | the source | Raw symmetric bytes. Mutually exclusive with `public`/`private` |
| `pkcs12` | the bundle source + `password` | RSA pair from a password-protected PKCS#12 bundle (see below). Mutually exclusive with both other shapes |

Within any source, set **exactly one** of `file` (path) or `value`
(base64-encoded bytes). Setting both, setting a `secret` alongside
`public`/`private`, or setting a `pkcs12` alongside either, is rejected by the
config validation layer at startup with a clear `ConfigError`.

### PKCS#12 bundles

Commercial payment and security platforms hand out RSA material as a
password-protected PKCS#12 (`.p12`/`.pfx`) bundle. A `pkcs12` entry loads it
directly, with no out-of-band `openssl pkcs12` conversion:

- **Bundle**: exactly one of `file` (path) or `value` (base64 of the bundle
  bytes), the same two sources every other shape uses. A bundle that cannot
  be read or decoded fails startup with the source elided from the error, as
  a `secret` does — the stanza sits next to its password, so a transposed
  field never reaches a startup log.
- **Password**: exactly one of `env` (the **name** of an environment variable)
  or `file` (a path, typically a mounted Kubernetes/Docker secret; trailing
  newlines are stripped). The password itself is never written in config:
  there is no `value` field, an `env` that is not a valid variable name is
  rejected at validation without echoing it, and an unset variable, an empty
  value, or an unreadable file fails startup with the source elided from the
  error.
- **Content**: exactly one private key and its leaf certificate. The private
  key must be RSA; an EC key fails startup naming the RSA allowlist (ECDSA is
  rejected by design, see #347). The leaf certificate's public key becomes the
  entry's `PublicKey` and is checked against the private key. Any CA chain in
  the bundle is **dropped**: `app.KeyStore` exposes keys, not certificates,
  and no consumer (JOSE included) uses `x5c`.
- **Errors** are distinct and never carry the password: `password incorrect`,
  `decode: …` (corrupt or not a PKCS#12 file), `private key is
  *ecdsa.PrivateKey, only RSA is supported`, `certificate does not match
  private key`.

Decoding uses [`software.sslmate.com/src/go-pkcs12`](https://pkg.go.dev/software.sslmate.com/src/go-pkcs12)
(pinned). `golang.org/x/crypto/pkcs12` is frozen: it decodes a single
certificate only and lacks the PBES2 (AES-256-CBC + PBKDF2) scheme that
OpenSSL 3 and current Java `keytool` emit by default, so most vendor bundles
would fail with it. Legacy RC2/3DES bundles decode with either.

### Minimum-length floor

`keystore.secretminlength` is the byte floor for symmetric secrets, and it is
mandatory (ADR-095, closing ADR-065's deprecation window): **absent** (nil in
Go) applies **32**; **`N ≥ 32`** raises the floor to `N`; anything below 32 —
`0`, the former opt-out, included — is rejected at config validation with a
`ConfigError` on `keystore.secretminlength`, before any key is read. The field
stays a pointer (`SecretMinLength: new(48)` in Go literals) so both
configuration doors can tell an explicit value from an absent key.

The floor is a defensive control against silently weak HMAC/HKDF keys, so
there is no configuration that admits a shorter secret: `config.Validate`
rejects the config, and `Module.Init` refuses a sub-32 floor that reached it
unvalidated (a hand-built `ModuleDeps`) rather than clamping it. A
partner-mandated key shorter than 32 bytes must be loaded by your own code,
outside the keystore.

### Generation entries (key families)

An entry named `<logical>-v<N>` is a **generation** of the Logical kid `<logical>`,
the shape AMQP payload sealing rotates by (spec #1309, issue #1306). The trailing
`-v<digits>` is the sole generation marker; every other name is an ordinary entry and nothing below applies to it — HTTP jose entries are unaffected.

At startup the store refuses a generation entry whose family part fails the Logical kid
grammar: the jose kid alphabet `^[A-Za-z0-9_-]+$` (already narrowed to `^[a-z0-9-]+$` by
the reachability rule above), at most 64 characters, and never itself ending in
`-v<digits>` — `x-v1-v2` is refused because its family `x-v1` would be a generation, so
every entry belongs to exactly one family by construction. The version is a
positive integer without leading zeros: `x-v0` and `x-v01` are refused (`v1`, not `v01`), so
two spellings can never alias one key.

**Consumer-visible risk:** an existing entry whose name already ends in `-v<digits>`
acquires generation semantics, and is refused if its family part fails the grammar.
No shipped example does (0 hits across `wiki/**`, `llms.txt`, `README.md`, the config
fixtures and the demo project's `config*.yaml` and jose tags).

```go
type FamilyEnumerator interface {
    Generations(logical string) []Generation // ascending by version; empty for an unknown family
}
```

The store implements `keystore.FamilyEnumerator`; type-assert `deps.KeyStore` to reach it,
and `MockKeyStore.WithGeneration(logical, version, role)` fakes it in tests.
Each `Generation` carries its `Logical` name, its `Version` (`"v2"`) and the `Role` its
material grants (`RolePublicOnly`, `RolePrivate`, `RoleSecret`); `Kid()` joins them into the
entry name that travels on the wire. The result **is** the accept set: no separate list widens or re-aims it; provisioning material is the
sole trust act.

### Activation (`messaging.seal.active`)

The producer picks which provisioned generation seals new traffic, per Logical kid:

```yaml
messaging:
  seal:
    active:
      svc-payments-sign: v2      # env: MESSAGING_SEAL_ACTIVE_SVC-PAYMENTS-SIGN=v2
```

`config.Validate` checks the shape — each key is an env-reachable section name, each value
`v<N>` with `N` a positive integer without leading zeros — and
`keystore.ActiveGeneration(store, active, logical)` resolves it against the keystore at
startup, once per Logical kid the producer resolves, sign and encrypt alike:

| Provisioned | Selector | Result |
| --- | --- | --- |
| 0 | any | error naming the family |
| 1 | absent | that generation is active |
| 2+ | absent | error listing the generations — startup never guesses |
| N | names a provisioned generation | that generation |
| N | names an unprovisioned generation | error naming the selector value |

The environment door is narrower than the YAML one. The loader lowercases a variable name
and maps `_` to `.`, so an `MESSAGING_SEAL_ACTIVE_*` override reaches only a Logical kid
spelled in `[a-z0-9]` — or one with hyphens where the runtime permits `-` in a variable name
(Docker and Kubernetes do, POSIX `export` does not,
[ADR-090](adr_090_env_reachable_section_names.md)). Under POSIX a hyphenated kid such as
`svc-payments-sign` is YAML-only. A selector for a Logical kid the producer never resolves is
ignored here.

## API

```go
type KeyStore interface {
    PublicKey(name string) (*rsa.PublicKey, error)
    PrivateKey(name string) (*rsa.PrivateKey, error)
    Secret(name string) ([]byte, error)
}
```

- `PublicKey` / `PrivateKey` — unchanged RSA behavior. Calling either on a
  secret-only entry returns a clear `"has no public/private key configured"`
  error rather than a nil key.
- `Secret` — returns a **defensive copy** (`bytes.Clone`) of the raw material.
  The caller owns the slice and may zeroize it after use. Calling `Secret` on
  an RSA entry returns `"has no symmetric secret configured"`; an unknown name
  returns `"key %q not found"`.

The store's master copy lives for the process lifetime (consistent with how RSA
private keys are already held). Zeroization is scoped to the caller's returned
copy — the keystore does not wipe its own master.

### Usage

```go
func (m *Module) Init(deps *app.ModuleDeps) error {
    if deps.KeyStore == nil {
        return fmt.Errorf("KeyStore required but not configured")
    }
    m.keyStore = deps.KeyStore
    return nil
}

func (s *Service) Digest(payload []byte) ([]byte, error) {
    key, err := s.keyStore.Secret("mac-key")
    if err != nil {
        return nil, fmt.Errorf("get mac key: %w", err)
    }
    defer func() { clear(key) }()  // caller owns the copy — zeroize after use
    mac := hmac.New(sha256.New, key)
    mac.Write(payload)
    return mac.Sum(nil), nil
}
```

For a complete worked HMAC-over-a-request example, see [Visa x-pay-token](httpclient.md#visa-x-pay-token-api-key--shared-secret).

Register `keystore.NewModule()` **before** any module that needs key material
(JOSE-tagged routes, services using `deps.KeyStore`). The framework wires the
store into `deps.KeyStore` via the `app.KeyStoreProvider` interface; a second
KeyStore provider is rejected at registration.

## Testing

```go
import kstest "github.com/gaborage/go-bricks/keystore/testing"

mock := kstest.NewMockKeyStore().
    WithPublicKey("signing", &priv.PublicKey).
    WithPrivateKey("signing", priv).
    WithSecret("mac-key", []byte("a-32-byte-symmetric-mac-key!!!!!"))

// Error injection
mock.WithSecretError(fmt.Errorf("key unavailable"))

// Assertion helpers
kstest.AssertPublicKeyAvailable(t, mock, "signing")
kstest.AssertPrivateKeyAvailable(t, mock, "signing")
kstest.AssertSecretAvailable(t, mock, "mac-key")
kstest.AssertKeyNotFound(t, mock, "nonexistent")
```

`WithSecret` copies its input and `Secret` returns a defensive copy, mirroring
the real store so tests exercise the same ownership contract.

## Security Notes

- Secrets come from files (local dev) or base64 env vars / secrets managers
  (deployed) — never hardcoded, one audited path, one rotation runbook.
- No key material appears in logs or error messages: load/parse errors carry
  the logical name, key type, and file path only; the framework logger's
  `SensitiveDataFilter` covers any incidental log lines.
- A PKCS#12 password reaches the process only through an environment
  variable or a mounted file; the config shape has no literal field, and
  password-load errors elide the source.
- The minimum-length floor is mandatory (32 bytes) and can only be raised; a
  secret that cannot meet it does not belong in the keystore.
- Never reuse a kid across HTTP jose and payload sealing (#1306). The store
  records the role of every startup resolution (`jose-route` from a route
  policy, `seal` from a sealed publisher or consumer) and the app logs one
  WARN per entry seen under both — entry name and roles only, never material.
  Warn only: there is no enforced prefix partition.
- Derivation (HKDF expansion, etc.) is left to the consumer — the keystore
  intentionally exposes raw material rather than a built-in derive helper
  (smallest viable surface; can layer on later if demand appears).
