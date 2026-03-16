// Package keystore provides named RSA key pair management for GoBricks applications.
//
// Keys are loaded at startup from DER-encoded files or base64-encoded values
// (typically injected via environment variables for Kubernetes/EKS deployments).
// Once loaded, the store is read-only and safe for concurrent access.
//
// # Configuration
//
// Keys are configured in YAML under the "keystore" section:
//
//	keystore:
//	  keys:
//	    signing:
//	      public:
//	        file: "certs/signing_public.der"       # Local dev
//	      private:
//	        value: "${SIGNING_PRIVATE_KEY_BASE64}"  # EKS (base64-encoded DER)
//
// # Usage
//
// Register the module before modules that need keys:
//
//	fw.RegisterModules(
//	    keystore.NewModule(),
//	    &myapp.JWEModule{},
//	)
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
package keystore

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/gaborage/go-bricks/config"
)

// keyPair holds a parsed public/private RSA key pair.
type keyPair struct {
	public  *rsa.PublicKey
	private *rsa.PrivateKey // May be nil if only public key is configured
}

// store implements app.KeyStore.
// All keys are loaded at construction time; access is read-only and thread-safe.
type store struct {
	keys map[string]*keyPair
}

// PublicKey returns the RSA public key for the given key pair name.
func (s *store) PublicKey(name string) (*rsa.PublicKey, error) {
	kp, ok := s.keys[name]
	if !ok {
		return nil, fmt.Errorf("keystore: key %q not found", name)
	}
	return kp.public, nil
}

// PrivateKey returns the RSA private key for the given key pair name.
func (s *store) PrivateKey(name string) (*rsa.PrivateKey, error) {
	kp, ok := s.keys[name]
	if !ok {
		return nil, fmt.Errorf("keystore: key %q not found", name)
	}
	if kp.private == nil {
		return nil, fmt.Errorf("keystore: key %q has no private key configured", name)
	}
	return kp.private, nil
}

// newStore creates a KeyStore by loading all configured key pairs.
// Fails fast if any key cannot be loaded or parsed.
func newStore(keys map[string]config.KeyPairConfig) (*store, error) {
	parsed := make(map[string]*keyPair, len(keys))

	for name, kpCfg := range keys {
		kp := &keyPair{}

		// Load public key (required)
		pubDER, err := loadDERBytes(kpCfg.Public, name, "public")
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

		// Load private key (optional)
		privDER, err := loadDERBytes(kpCfg.Private, name, "private")
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

		parsed[name] = kp
	}

	return &store{keys: parsed}, nil
}

// loadDERBytes resolves a KeySourceConfig to raw DER bytes.
// Returns nil if neither file nor value is set (key not configured).
func loadDERBytes(src config.KeySourceConfig, keyName, keyType string) ([]byte, error) {
	hasFile := src.File != ""
	hasValue := src.Value != ""

	if !hasFile && !hasValue {
		return nil, nil
	}

	if hasFile {
		data, err := os.ReadFile(src.File)
		if err != nil {
			return nil, fmt.Errorf("keystore: key %q %s: read file %q: %w", keyName, keyType, src.File, err)
		}
		return data, nil
	}

	// Base64-encoded value (typically from env var)
	data, err := base64.StdEncoding.DecodeString(src.Value)
	if err != nil {
		return nil, fmt.Errorf("keystore: key %q %s: base64 decode: %w", keyName, keyType, err)
	}
	return data, nil
}

// parsePublicKey parses DER-encoded public key (PKIX format) into an RSA public key.
func parsePublicKey(der []byte, keyName string) (*rsa.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("keystore: key %q public: ParsePKIXPublicKey: %w", keyName, err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("keystore: key %q public: expected *rsa.PublicKey, got %T", keyName, pub)
	}
	return rsaPub, nil
}

// parsePrivateKey parses DER-encoded private key with PKCS8 first, PKCS1 fallback.
func parsePrivateKey(der []byte, keyName string) (*rsa.PrivateKey, error) {
	// Try PKCS8 first (modern format)
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("keystore: key %q private: PKCS8 parsed but not RSA (got %T)", keyName, key)
		}
		return rsaKey, nil
	}

	// Fallback to PKCS1 (legacy format)
	rsaKey, err2 := x509.ParsePKCS1PrivateKey(der)
	if err2 != nil {
		return nil, fmt.Errorf("keystore: key %q private: PKCS8 failed (%v), PKCS1 fallback also failed: %w", keyName, err, err2)
	}
	return rsaKey, nil
}
