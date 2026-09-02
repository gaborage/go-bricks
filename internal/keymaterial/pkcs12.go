package keymaterial

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"os"
	"strings"

	"software.sslmate.com/src/go-pkcs12"

	"github.com/gaborage/go-bricks/internal/secretfile"
)

// ParsePKCS12RSA decodes a password-protected PKCS#12 bundle into its RSA
// private key. The leaf certificate must carry the matching RSA public key;
// any CA chain in the bundle is discarded.
func ParsePKCS12RSA(pfx []byte, password string) (*rsa.PrivateKey, error) {
	key, leaf, _, err := pkcs12.DecodeChain(pfx, password)
	if err != nil {
		if errors.Is(err, pkcs12.ErrIncorrectPassword) {
			return nil, errors.New("password incorrect")
		}
		return nil, fmt.Errorf("decode: %w", err)
	}
	priv, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, only RSA is supported", key)
	}
	pub, ok := leaf.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("certificate public key is %T, only RSA is supported", leaf.PublicKey)
	}
	if !priv.PublicKey.Equal(pub) {
		return nil, errors.New("certificate does not match private key")
	}
	return priv, nil
}

// LoadPassword resolves a PKCS#12 password from the named environment variable
// or from a file (trailing newlines stripped). No error echoes the variable
// name, the path, or the value.
func LoadPassword(env, file string) (string, error) {
	switch {
	case env != "":
		v := os.Getenv(env)
		if v == "" {
			return "", errors.New("environment variable not set or empty (name elided)")
		}
		return v, nil
	case file != "":
		data, err := os.ReadFile(file) // #nosec G304 -- deployment configuration, as in LoadBytes
		if err != nil {
			return "", fmt.Errorf("password file unreadable (source elided): %w", secretfile.Errno(err))
		}
		pw := strings.TrimRight(string(data), "\r\n")
		if pw == "" {
			return "", errors.New("password file is empty (source elided)")
		}
		return pw, nil
	default:
		return "", errors.New("no password source configured")
	}
}
