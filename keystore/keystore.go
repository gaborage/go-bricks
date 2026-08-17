// Package keystore provides named key-material management for GoBricks
// applications: RSA key pairs and raw symmetric secrets (HMAC/CMAC keys,
// HKDF input).
//
// Material is loaded at startup from files or base64-encoded values (typically
// injected via environment variables for Kubernetes/EKS deployments). Once
// loaded, the store is read-only and safe for concurrent access. Each entry is
// either an RSA pair or a symmetric secret — a mixed entry is rejected by the
// config layer at startup (structural detection, no explicit discriminator).
//
// # Configuration
//
// Keys are configured in YAML under the "keystore" section:
//
//	keystore:
//	  secretminlength: 32                        # default 32; explicit 0 disables (deprecated, WARNs — #1036)
//	  keys:
//	    signing:
//	      public:
//	        file: "certs/signing_public.der"       # Local dev
//	      private:
//	        value: "${SIGNING_PRIVATE_KEY_BASE64}"  # EKS (base64-encoded DER)
//	    mac-key:
//	      secret:
//	        value: "${MAC_KEY_BASE64}"              # base64 raw key material
//
// # Usage
//
// Register the module before modules that need keys:
//
//	if err := fw.RegisterModule(keystore.NewModule()); err != nil {
//	    log.Fatal(err)
//	}
//	if err := fw.RegisterModule(&myapp.JWEModule{}); err != nil {
//	    log.Fatal(err)
//	}
//
// Access keys via ModuleDeps (nil-check for fail-fast if keys are required):
//
//	func (m *Module) Init(deps *app.ModuleDeps) error {
//	    if deps.KeyStore == nil {
//	        return fmt.Errorf("KeyStore required but not configured")
//	    }
//	    m.keyStore = deps.KeyStore
//	    return nil
//	}
//
//	privKey, err := m.keyStore.PrivateKey("signing")
//
// Secret returns a defensive copy of symmetric key material; the caller owns
// the slice and may zeroize it after use:
//
//	macKey, err := m.keyStore.Secret("mac-key")
package keystore

import (
	"bytes"
	"crypto/rsa"
	"fmt"
	"maps"
	"slices"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/keymaterial"
)

// errKeyNotFoundFmt is the fmt.Errorf format used by every accessor when a
// logical key name is absent from the store (%q = the requested name).
const errKeyNotFoundFmt = "keystore: key %q not found"

// keyEntry holds the parsed material for one logical name: either an RSA pair
// (public set, private optional) or a symmetric secret. The two are mutually
// exclusive — the config layer rejects mixed entries before newStore runs.
type keyEntry struct {
	public  *rsa.PublicKey
	private *rsa.PrivateKey // May be nil if only public key is configured
	secret  []byte          // Non-nil only for symmetric-secret entries
}

// store implements app.KeyStore.
// All keys are loaded at construction time; access is read-only and thread-safe.
type store struct {
	keys map[string]*keyEntry
}

// PublicKey returns the RSA public key for the given key pair name.
func (s *store) PublicKey(name string) (*rsa.PublicKey, error) {
	kp, ok := s.keys[name]
	if !ok {
		return nil, fmt.Errorf(errKeyNotFoundFmt, name)
	}
	if kp.public == nil {
		return nil, fmt.Errorf("keystore: key %q has no public key configured", name)
	}
	return kp.public, nil
}

// PrivateKey returns the RSA private key for the given key pair name.
func (s *store) PrivateKey(name string) (*rsa.PrivateKey, error) {
	kp, ok := s.keys[name]
	if !ok {
		return nil, fmt.Errorf(errKeyNotFoundFmt, name)
	}
	if kp.private == nil {
		return nil, fmt.Errorf("keystore: key %q has no private key configured", name)
	}
	return kp.private, nil
}

// Secret returns a defensive copy of the raw symmetric key material for the
// given name. The caller owns the returned slice and may zeroize it.
func (s *store) Secret(name string) ([]byte, error) {
	kp, ok := s.keys[name]
	if !ok {
		return nil, fmt.Errorf(errKeyNotFoundFmt, name)
	}
	if kp.secret == nil {
		return nil, fmt.Errorf("keystore: key %q has no symmetric secret configured", name)
	}
	return bytes.Clone(kp.secret), nil
}

// shortSecret names a symmetric secret the store admitted below
// config.DefaultKeyStoreSecretMinLength because the configured floor allowed
// it. Never carries the material.
type shortSecret struct {
	name string
	n    int
}

// newStore creates a KeyStore by loading all configured entries.
// secretMinLength is the byte floor for symmetric secrets (0 disables it —
// deprecated). Fails fast if any entry cannot be loaded, parsed, or fails
// the floor.
func newStore(keys map[string]config.KeyPairConfig, secretMinLength int) (*store, error) {
	parsed := make(map[string]*keyEntry, len(keys))

	// Sorted so the first error names the same key every run.
	for _, name := range slices.Sorted(maps.Keys(keys)) {
		kpCfg := keys[name]
		entry, err := loadKeyEntry(name, &kpCfg, secretMinLength)
		if err != nil {
			return nil, err
		}
		parsed[name] = entry
	}

	return &store{keys: parsed}, nil
}

// belowRecommended lists the symmetric secrets shorter than
// config.DefaultKeyStoreSecretMinLength, sorted by name — the set #1036 will
// reject once the floor becomes mandatory. Symmetric entries are the ones
// with material (see keyEntry.secret).
func (s *store) belowRecommended() []shortSecret {
	var short []shortSecret
	for _, name := range slices.Sorted(maps.Keys(s.keys)) {
		if kp := s.keys[name]; kp.secret != nil && len(kp.secret) < config.DefaultKeyStoreSecretMinLength {
			short = append(short, shortSecret{name: name, n: len(kp.secret)})
		}
	}
	return short
}

// loadKeyEntry loads one entry as either a symmetric secret or an RSA pair.
// The config layer guarantees the two are mutually exclusive.
func loadKeyEntry(name string, cfg *config.KeyPairConfig, secretMinLength int) (*keyEntry, error) {
	if cfg.Secret.IsSet() {
		return loadSecretEntry(name, cfg.Secret, secretMinLength)
	}
	return loadRSAEntry(name, cfg)
}

// loadSecretEntry loads raw symmetric key material and enforces the byte floor.
func loadSecretEntry(name string, src config.KeySourceConfig, secretMinLength int) (*keyEntry, error) {
	raw, err := loadKeyBytes(src, name, "secret")
	if err != nil {
		return nil, err
	}
	// src.IsSet() is true here, so loadKeyBytes never returns nil without error.
	if secretMinLength > 0 && len(raw) < secretMinLength {
		return nil, fmt.Errorf("keystore: key %q: secret is %d bytes, minimum is %d", name, len(raw), secretMinLength)
	}
	return &keyEntry{secret: raw}, nil
}

// loadRSAEntry loads an RSA pair: public required, private optional, matched.
func loadRSAEntry(name string, cfg *config.KeyPairConfig) (*keyEntry, error) {
	kp := &keyEntry{}

	pubDER, err := loadKeyBytes(cfg.Public, name, "public")
	if err != nil {
		return nil, err
	}
	if pubDER == nil {
		return nil, fmt.Errorf("keystore: key %q: public key is required", name)
	}
	kp.public, err = parsePublicKey(pubDER, name)
	if err != nil {
		return nil, err
	}

	privDER, err := loadKeyBytes(cfg.Private, name, "private")
	if err != nil {
		return nil, err
	}
	if privDER != nil {
		kp.private, err = parsePrivateKey(privDER, name)
		if err != nil {
			return nil, err
		}
		// Fail fast if public and private keys don't match
		if kp.private.E != kp.public.E || kp.private.N.Cmp(kp.public.N) != 0 {
			return nil, fmt.Errorf("keystore: key %q: public and private keys do not match", name)
		}
	}

	return kp, nil
}

// loadKeyBytes resolves a KeySourceConfig to raw bytes — DER for RSA keys,
// raw key material for secrets. Returns nil if neither file nor value is set.
// Delegates the file/value resolution mechanism to internal/keymaterial (also
// consumed by cmd/seal-payload) and adds the keystore error-namespace prefix.
// Secrets route through LoadSecretBytes: unlike RSA DER, raw symmetric
// material has no detectable shape, so its loader never echoes the
// configured source on a failed read.
func loadKeyBytes(src config.KeySourceConfig, keyName, keyType string) ([]byte, error) {
	var data []byte
	var err error
	if keyType == "secret" {
		data, err = keymaterial.LoadSecretBytes(src.File, src.Value)
	} else {
		data, err = keymaterial.LoadBytes(src.File, src.Value)
	}
	if err != nil {
		return nil, fmt.Errorf("keystore: key %q %s: %w", keyName, keyType, err)
	}
	return data, nil
}

// parsePublicKey parses DER-encoded public key (PKIX format) into an RSA public key.
func parsePublicKey(der []byte, keyName string) (*rsa.PublicKey, error) {
	rsaPub, err := keymaterial.ParseRSAPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("keystore: key %q public: %w", keyName, err)
	}
	return rsaPub, nil
}

// parsePrivateKey parses DER-encoded private key with PKCS8 first, PKCS1 fallback.
func parsePrivateKey(der []byte, keyName string) (*rsa.PrivateKey, error) {
	rsaKey, err := keymaterial.ParseRSAPrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("keystore: key %q private: %w", keyName, err)
	}
	return rsaKey, nil
}
