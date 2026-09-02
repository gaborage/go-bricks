// Package keymaterial hosts the file-or-value key loading and DER parsing
// mechanism shared by the keystore module and the cmd/seal-payload CLI.
// keystore wraps these calls with its "keystore: key %q ..." error prefixes;
// cmd/seal-payload consumes them directly, so the CLI can never accept a key
// format the middleware's keystore would reject.
package keymaterial

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"github.com/gaborage/go-bricks/internal/secretfile"
)

// LoadBytes resolves file-or-value key material to raw bytes (DER for RSA).
// When both are set, file wins (keystore's historical behavior — callers
// wanting exactly-one-of must enforce it themselves). Returns (nil, nil)
// when neither is set.
func LoadBytes(file, value string) ([]byte, error) {
	hasFile := file != ""
	hasValue := value != ""

	if !hasFile && !hasValue {
		return nil, nil
	}

	if hasFile {
		if secretfile.LooksLikeKeyMaterial(file) {
			return nil, errors.New("file looks like key material, not a path (pass inline material via the value source instead)")
		}
		// #nosec G304 -- the path is deployment configuration, not request input:
		// reading an operator-named file IS this function. Inline material is
		// rejected above.
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, secretfile.ReadError(file, err)
		}
		return data, nil
	}

	// Base64-encoded value (typically from env var)
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	return data, nil
}

// LoadSecretBytes is LoadBytes for raw symmetric secrets. Secret material has
// no detectable shape (no PEM/DER structure), so a mis-filed value cannot be
// caught by LooksLikeKeyMaterial — instead, NO error on this path ever echoes
// the configured file value or the underlying path.
// SECURITY: a transposed secret.file/secret.value must not put key material
// into a fatal startup log line; strip the path from wrapped OS errors.
func LoadSecretBytes(file, value string) ([]byte, error) {
	hasFile := file != ""
	hasValue := value != ""
	if !hasFile && !hasValue {
		return nil, nil
	}
	if hasFile {
		if secretfile.LooksLikeKeyMaterial(file) {
			return nil, errors.New("file looks like key material, not a path (pass inline material via the value source instead)")
		}
		data, err := os.ReadFile(file) // #nosec G304 -- deployment configuration, as in LoadBytes
		if err != nil {
			return nil, fmt.Errorf("secret file unreadable (source elided): %w", secretfile.Errno(err))
		}
		return data, nil
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("secret value is not valid base64 (value elided)")
	}
	return data, nil
}

// ParseRSAPublicKey parses PKIX DER into an *rsa.PublicKey.
func ParseRSAPublicKey(der []byte) (*rsa.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("ParsePKIXPublicKey: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("expected *rsa.PublicKey, got %T", pub)
	}
	return rsaPub, nil
}

// ParseRSAPrivateKey parses PKCS#8 DER (PKCS#1 fallback) into an *rsa.PrivateKey.
func ParseRSAPrivateKey(der []byte) (*rsa.PrivateKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 parsed but not RSA (got %T)", key)
		}
		return rsaKey, nil
	}

	rsaKey, err2 := x509.ParsePKCS1PrivateKey(der)
	if err2 != nil {
		return nil, fmt.Errorf("PKCS8 failed (%w), PKCS1 fallback also failed: %w", err, err2)
	}
	return rsaKey, nil
}
