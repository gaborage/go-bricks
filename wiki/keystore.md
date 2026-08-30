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
  secretminlength: 32          # default 32 when absent; explicit 0 disables the floor (deprecated, WARNs — see #1036)
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

Within any source, set **exactly one** of `file` (path) or `value`
(base64-encoded bytes). Setting both, or setting a `secret` alongside
`public`/`private`, is rejected by the config validation layer at startup with
a clear `ConfigError`.

### Minimum-length floor

`keystore.secretminlength` is a tri-state setting (ADR-065, see
[CONTEXT.md](../CONTEXT.md)): **absent** (nil in Go) applies the default of
**32** bytes; an explicit **`0`** disables the floor entirely (deprecated —
see below); **`N > 0`** sets the floor to `N`. Negative values are rejected
at config validation. Go literals set the pointer with `new(n)` —
`SecretMinLength: new(0)` to disable, `new(48)` to raise it.

The floor is a defensive control against silently weak HMAC/HKDF keys, so
disabling it is deprecated and admitting a short secret WARNs at startup
(tracked in [#1036](https://github.com/gaborage/go-bricks/issues/1036), which
will make the 32-byte floor mandatory): once if the floor itself is `0`
(`keystore: secret length floor disabled`), and once per admitted secret
shorter than 32 bytes, naming the key and its byte length — never the
material.

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
- The minimum-length floor is on by default — keep it on for HMAC/HKDF keys;
  only disable it (`secretminlength: 0`) with a deliberate, documented reason,
  and expect the startup WARN — the opt-out is deprecated and will be removed.
- Derivation (HKDF expansion, etc.) is left to the consumer — the keystore
  intentionally exposes raw material rather than a built-in derive helper
  (smallest viable surface; can layer on later if demand appears).
