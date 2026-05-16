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
- **Fail-fast minimum length** for secrets (default 32 bytes) so a too-short
  key is rejected at startup rather than silently weakening a digest
- **Fail-fast at startup**: any entry that cannot be loaded, parsed, mismatched
  (RSA pair), or is below the floor (secret) aborts boot

## Configuration

```yaml
keystore:
  secret_min_length: 32          # default 32; explicit 0 disables the floor
  keys:
    signing:                     # RSA pair (public required, private optional)
      public:
        file: "certs/signing_public.der"        # local dev
      private:
        value: "${SIGNING_PRIVATE_KEY_BASE64}"  # deployed (base64 DER)
    mac-key:                     # symmetric secret entry
      secret:
        file: "certs/mac-key.bin"               # local dev: raw key bytes
    mac-key-deployed:
      secret:
        value: "${MAC_KEY_BASE64}"              # deployed: base64 raw key
```

Each `keys.<name>` entry resolves to **exactly one** of:

| Shape | Required | Notes |
|---|---|---|
| `public` (+ optional `private`) | `public` | RSA pair. PKCS8 with PKCS1 fallback for private; public/private mismatch is a startup error |
| `secret` | the source | Raw symmetric bytes. Mutually exclusive with `public`/`private` |

Within any source, set **exactly one** of `file` (path) or `value`
(base64-encoded bytes). Setting both, or setting a `secret` alongside
`public`/`private`, is rejected by the config validation layer at startup with
a clear `ConfigError`.

### Minimum-length floor

`keystore.secret_min_length` (default **32**) is the byte floor enforced for
every symmetric secret after decoding. It is a defensive control against
silently weak HMAC/HKDF keys. Set it to an explicit `0` to opt out (documented,
deliberate); negative values are rejected at config validation.

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
    mac := hmac.New(sha256.New, key)
    mac.Write(payload)
    return mac.Sum(nil), nil
}
```

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
- The minimum-length floor is on by default — keep it on for HMAC/HKDF keys;
  only disable it (`secret_min_length: 0`) with a deliberate, documented reason.
- Derivation (HKDF expansion, etc.) is left to the consumer — the keystore
  intentionally exposes raw material rather than a built-in derive helper
  (smallest viable surface; can layer on later if demand appears).
