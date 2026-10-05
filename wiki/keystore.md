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
    tokens:                      # a namespace: the entries below are "tokens.our" and "tokens.peer"
      our:
        public:
          file: "certs/tokens_our_public.der"
        private:
          value: "${TOKENS_OUR_PRIVATE_KEY_BASE64}"
      peer:
        public:
          file: "certs/tokens_peer_public.der"
    mac-key:                     # symmetric secret entry — a one-segment name
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

The entry name is the path below `keys`, joined with `.`: the YAML above defines
`signing`, `tokens.our`, `tokens.peer`, `mac-key`, `mac-key-deployed` and `vts`. See
[Entry names](#entry-names) for the grammar and the environment form.

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

### Entry names

A `keystore.keys` entry name is a **dotted path of segments**
([ADR-144](adr_144_dotted_keystore_entry_names.md)). Each segment matches `[a-z0-9-]+`, and
YAML writes the segments as nested maps. The environment reaches the same path through the
unchanged transform (lowercase, `_` → `.`), so a variable and a nested YAML key name one
entry. The joined string is used verbatim everywhere: as the `Keys` map key, the
`app.KeyStore` argument (`PrivateKey("tokens.our")`), the `jose:` tag value, the wire `kid`
and, for a sealing generation, the sealed kid and inbox family. Nothing rewrites one name
into another, so `tokens-our` and `tokens.our` are two different names.

| Entry name | YAML | Environment variable | POSIX `export` |
| --- | --- | --- | --- |
| `tokens.our` | `keys: {tokens: {our: {private: …}}}` | `KEYSTORE_KEYS_TOKENS_OUR_PRIVATE_VALUE` | yes |
| `payments.sign.v1` | `keys: {payments: {sign: {v1: {private: …}}}}` | `KEYSTORE_KEYS_PAYMENTS_SIGN_V1_PRIVATE_FILE` | yes |
| `tokens-our` | `keys: {tokens-our: {private: …}}` | `KEYSTORE_KEYS_TOKENS-OUR_PRIVATE_VALUE` | no: Docker, Kubernetes or `env` |
| `signing` | `keys: {signing: {private: …}}` | `KEYSTORE_KEYS_SIGNING_PRIVATE_VALUE` | yes |

A name is settable with a POSIX `export` exactly when it contains no `-`. Every name that was
valid before ADR-144 is a one-segment name and keeps its meaning, its variable and its wire kid.

Startup refuses, each with a `ConfigError` naming the koanf path and the rename:

- **A quoted key containing `.`** (`"tokens.our":`). Write it nested; the nested path is the one
  the variable reaches.
- **A segment outside `[a-z0-9-]`** (`tokens_our`, `Tokens`). The error names the dotted spelling
  the variable already reaches (`write "tokens.our", which KEYSTORE_KEYS_TOKENS_OUR_* reaches`),
  unless that spelling would be refused too (`webhook_secret`, `audit_v1`).
- **A field name after a `.`.** An entry is recognized by its fields (`public`, `private`,
  `secret`, `pkcs12`), so `webhook.secret` would read as entry `webhook` with a `secret` field.
  Rename it `webhook-secret` or `webhook.hmac`. A first segment is never a field, so a legacy
  entry named `secret` stays valid.
- **An entry with a name below it** (`tokens` and `tokens.our`). Nested YAML and the environment
  cannot hold both, so before ADR-144 the nested one was dropped in silence. Rename one of them
  (`tokens` → `tokens.default`).
- **Two names that differ only in `-` versus `.`** (`tokens-our` and `tokens.our`, or `a-b.c`
  and `a.b-c`). A POSIX override of the hyphenated entry creates the dotted one instead, and
  the override is never applied. Keep one name. Two generation names (`payments-sign-v1` and
  `payments.sign.v1`) are refused as look-alike families instead (see
  [Generation entries](#generation-entries-key-families)), because moving a generation into the
  other family is a drain-then-cutover.
- **A key under an entry that is not one of its fields** (`privte`), and **a key under a field
  that is not one of its sources** (`vlaue`; a source takes `file` or `value`, `pkcs12` also
  takes `password`, which takes `env` or `file`). These were dropped in silence before ADR-144.

When an accessor misses a name and a configured entry differs from it only in `-` versus `.`,
the error names that entry: `keystore: key "tokens.our" not found; configured "tokens-our"
differs only in '-' versus '.'`.

**Known limitation.** Nothing reports an entry that no route, declaration or module asks for.
Take variables left over after the YAML moved from `tokens-our` to `tokens.our`:

- **`tokens.our` configured too:** the look-alike rule refuses the pair.
- **One half left over** (`KEYSTORE_KEYS_TOKENS-OUR_PRIVATE_VALUE` alone, `tokens.our`
  missing): startup fails with `keystore.keys.tokens-our.public key source required`. The
  error first suggests completing `tokens-our`, which is the wrong fix here: remove the
  variable.
- **A complete pair, or a `secret`, left over** (`tokens.our` missing): the stray entry loads
  in silence. A `jose:` route resolves its kids at startup, so startup fails with the
  look-alike hint above in the cause. Only a lazy `PrivateKey("tokens.our")` (or `PublicKey`,
  `Secret`) in module code passes startup and fails on first use, with the same hint.

Remove retired variables together with the YAML they override.

#### Runbooks

- **R0, upgrade.** Nothing to rename. A config that boots today boots unchanged, unless an entry
  carries a key that is not a field or source, or an entry is nested under another; startup now
  names both ([migrations.md](migrations.md) `[C72.17]`).
- **R1, rename an in-process entry** (every reader is in this service: `PrivateKey` literals and
  `jose:` tags whose peer is the service itself). Change the YAML, the code literals, the tags and
  the deployment variables in one deploy. The two spellings cannot coexist, so there is no
  overlap window.
- **R2, rename a partner-facing JOSE kid.** The kid is on the wire and the partner pins it. Treat
  the rename as a key rotation with the partner: confirm the partner accepts `.` in a kid,
  provision the new name beside the old one only if the two are not look-alikes (otherwise
  switch in one coordinated cutover), then retire the old name.
- **R3, roll back.** A config holding a dotted name fails on an earlier binary, so roll back the
  config together with the code.

A sealing family is renamed by draining it: see
[sealing.md](sealing.md#renaming-a-family). The recommendation is dotted names for new keys,
and leaving live partner kids and live sealing families alone.

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

> How generations, the accept set and the activation selector fit into a sealing rotation,
> per family: [sealing.md](sealing.md#rotation-runbooks).

An entry named for a version of a Logical kid is a **generation** of that family, the shape
AMQP payload sealing rotates by (spec #1309, issue #1306). The family fixes the marker
([ADR-144](adr_144_dotted_keystore_entry_names.md)):

| Family | Generation names | Example |
| --- | --- | --- |
| contains `.` | `<family>.v<N>`, a final segment | `payments.sign.v1`, `payments.sign.v2` |
| has no `.` | `<family>-v<N>` | `svc-payments-sign-v1`, `svc-payments-sign-v2` |

Every other name is an ordinary entry and nothing below applies to it. The rules judge the
name, not its use: an HTTP jose entry is unaffected only when its name matches neither shape,
and one named `partner.v2` or `partner.key-v1` is refused below like any other. Because the
marker is a function of the family, `Generation.Kid()` is too, and a family's marker can never
change: a one-segment family such as `signing` keeps `signing-v<N>` for its whole life. Moving
it to a dotted family is a rename, not a rotation ([sealing.md](sealing.md#renaming-a-family)).

`config.Validate` and the store refuse a name that carries a marker but is no generation,
each with the rename spelled out:

- the other family's marker: `payments.sign-v1` (rename `payments.sign.v1`), `audit.v1` and
  `payments-sign.v1` (rename `audit-v1` and `payments-sign-v1`, or give the family a second
  segment);
- a family that fails the Logical kid grammar: the jose kid alphabet (runs of
  `[A-Za-z0-9_-]` joined by single dots, already narrowed to `[a-z0-9-]` segments by the rules
  above), at most 64 characters, and never itself ending in a marker — `x-v1-v2` and `x.v1.v2`
  are refused because their families would be generations, so every entry belongs to exactly
  one family by construction;
- a version that is not a positive integer without leading zeros: `x-v0`, `x.y.v01` (`v1`, not
  `v01`), so two spellings can never alias one key.

Two families that differ only in `-` versus `.` (`payments-sign-v1` beside `payments.sign.v2`,
or beside `payments.sign.v1`) are refused: that is a second family, not a rotation. The error
names the variable that reaches the hyphenated generation (`KEYSTORE_KEYS_PAYMENTS-SIGN-V2_*`,
which a POSIX `export` cannot set). So are two families that nest, as written or once `-` is
read as `.` (`payments-v1` beside `payments.sign.v1`; `payments-sign-v1` beside `payments.sign.eu.v1`),
because `messaging.seal.active` could not hold a selector for both: a POSIX variable for the
first would land on the path of the second and be dropped. Two families without `.`
(`payments-sign`, `payments-sign-eu`) are exempt, as before.

**Consumer-visible risk:** an existing entry whose name already ends in `-v<digits>`, or in a
`v<digits>` segment, acquires generation semantics, and is refused if it is no well-formed
generation. No shipped example does (0 hits across `wiki/**`, `llms.txt`, `README.md`, the
config fixtures and the demo project's `config*.yaml` and jose tags).

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
      payments:
        sign: v2                 # family payments.sign; env: MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN=v2
      svc-payments-sign: v2      # env: MESSAGING_SEAL_ACTIVE_SVC-PAYMENTS-SIGN=v2
```

A selector key is a family name: a dotted family is written nested, exactly like its keystore
entries. `config.Validate` checks the shape — each key a dotted path of `[a-z0-9-]` segments,
no two keys that differ only in `-` versus `.` or nest once `-` is read as `.` (unless neither
contains `.`), each value `v<N>` with `N` a positive integer without leading zeros — and
refuses a selector that differs from a provisioned family only in
`-` versus `.`: `MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN=v2` beside family `payments-sign` would
select nothing and leave the old generation sealing. Then
`keystore.ActiveGeneration(store, active, logical)` resolves it against the keystore at
startup, once per Logical kid the producer resolves, sign and encrypt alike:

| Provisioned | Selector | Result |
| --- | --- | --- |
| 0 | any | error naming the family |
| 1 | absent | that generation is active |
| 2+ | absent | error listing the generations — startup never guesses |
| N | names a provisioned generation | that generation |
| N | names an unprovisioned generation | error naming the selector value |

The loader lowercases a variable name and maps `_` to `.`, so an `MESSAGING_SEAL_ACTIVE_*`
override reaches the dotted family its segments spell: `MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN`
is the selector for `payments.sign`, from any shell. A hyphenated family is settable that way
only where the runtime permits `-` in a variable name (Docker and Kubernetes do, POSIX `export`
does not, [ADR-090](adr_090_env_reachable_section_names.md)); under POSIX a hyphenated family
such as `svc-payments-sign` is YAML-only. A selector for a Logical kid the producer never
resolves is ignored here.

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
