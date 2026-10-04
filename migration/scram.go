package migration

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	scramSHA256Prefix   = "SCRAM-SHA-256$"
	scramClientKeyLabel = "Client Key"
	scramServerKeyLabel = "Server Key"
)

var errSCRAMIterations = errors.New("migration: SCRAM iteration count must be positive")

// scramSHA256Verifier renders the PostgreSQL SCRAM-SHA-256 verifier for password (RFC 5802,
// RFC 7677): SCRAM-SHA-256$<iter>:<salt>$<StoredKey>:<ServerKey>, all base64 standard
// padded. The salted password comes from crypto/pbkdf2 rather than a loop over crypto/hmac
// keyed by the password, which FIPS 140-only mode refuses for keys under 14 bytes.
func scramSHA256Verifier(password string, salt []byte, iterations int) (string, error) {
	if iterations < 1 {
		return "", errSCRAMIterations
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("migration: derive SCRAM salted password: %w", err)
	}
	clientKey := hmacSHA256(salted, scramClientKeyLabel)
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, scramServerKeyLabel)

	enc := base64.StdEncoding
	return fmt.Sprintf("%s%d:%s$%s:%s", scramSHA256Prefix, iterations,
		enc.EncodeToString(salt), enc.EncodeToString(storedKey[:]), enc.EncodeToString(serverKey)), nil
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// isSCRAMSafePassword reports whether every byte of password is printable ASCII (0x20-0x7E),
// the range where PostgreSQL's SASLprep and pgx's OpaqueString normalization are both the
// identity, so a verifier computed here matches what every client derives at login.
func isSCRAMSafePassword(password string) bool {
	for i := 0; i < len(password); i++ {
		if c := password[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
