// Package keymaterial hosts the file-or-value key loading and DER parsing
// mechanism shared by the keystore module and internal/sealcli, through which
// the seal-payload and seal-event CLIs reach it. keystore wraps these calls
// with its "keystore: key %q ..." error prefixes; sealcli consumes them
// directly, so a key the CLIs accept is never one the middleware's keystore
// would reject. It also hosts the producer-role resolver sealcli assembles and
// the CLIs hand to jose once their keys are parsed.
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

// LoadRSAPrivateKey is LoadBytes followed by ParseRSAPrivateKey: the whole
// file-or-value-to-key hop a CLI needs for its own signing key. It adds no
// error prefix of its own, so callers keep whatever role wording they already
// use ("sign key: %w"). With neither source set LoadBytes yields nil DER and
// the parse rejects it — there is no silent nil key.
func LoadRSAPrivateKey(file, value string) (*rsa.PrivateKey, error) {
	der, err := LoadBytes(file, value)
	if err != nil {
		return nil, err
	}
	return ParseRSAPrivateKey(der)
}

// LoadRSAPublicKey is LoadBytes followed by ParseRSAPublicKey, the public-key
// counterpart of LoadRSAPrivateKey and with the same no-prefix contract.
func LoadRSAPublicKey(file, value string) (*rsa.PublicKey, error) {
	der, err := LoadBytes(file, value)
	if err != nil {
		return nil, err
	}
	return ParseRSAPublicKey(der)
}

// ProducerKeys resolves the two kids a producer holds: its own sign PRIVATE
// key and the audience's encrypt PUBLIC key. Any other kid is an error naming
// the kid and nothing else.
//
// The method set is exactly jose.KeyResolver's (and jose.KeyStoreLike's), so a
// *ProducerKeys satisfies both structurally — this package deliberately does
// not import jose: keystore imports keymaterial, and the dependency would drag
// go-jose into the keystore module for no gain.
type ProducerKeys struct {
	SignKid    string
	SignPriv   *rsa.PrivateKey
	EncryptKid string
	EncPub     *rsa.PublicKey
}

// PrivateKey returns the sign key for SignKid; every other kid is unknown.
func (k *ProducerKeys) PrivateKey(kid string) (*rsa.PrivateKey, error) {
	if kid == k.SignKid {
		return k.SignPriv, nil
	}
	return nil, fmt.Errorf("no private key registered for kid %q", kid)
}

// PublicKey returns the encrypt key for EncryptKid; every other kid is unknown.
func (k *ProducerKeys) PublicKey(kid string) (*rsa.PublicKey, error) {
	if kid == k.EncryptKid {
		return k.EncPub, nil
	}
	return nil, fmt.Errorf("no public key registered for kid %q", kid)
}
