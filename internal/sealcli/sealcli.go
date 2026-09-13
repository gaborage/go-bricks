// Package sealcli hosts the flag, key and payload plumbing the sealing CLIs
// share: the four key-source flags with their help text, the
// exactly-one-of-per-pair refusal keymaterial deliberately leaves to its
// callers, and the size-capped file-or-stdin payload read. The flags come in
// two roles — KeySources for a producer (sign PRIVATE, encrypt PUBLIC) and
// ConsumerKeySources for an opener (sign PUBLIC, encrypt PRIVATE) — and each
// command keeps only its own flags, its own required-flag checks and its own
// call into jose.
package sealcli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gaborage/go-bricks/internal/keymaterial"
)

// ErrUsage marks flag-parse failures whose message the FlagSet already printed
// to stderr itself — a command must not print them a second time.
var ErrUsage = errors.New("usage error")

// PositionalPath parses args with fs and returns the single optional
// positional argument: the payload path ReadPayload then consumes, "" when
// absent. A parse failure is wrapped in ErrUsage so the caller can tell the
// already-reported ones apart; a second positional argument is refused here.
func PositionalPath(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if fs.NArg() > 1 {
		return "", errors.New("expected at most one payload-file argument")
	}
	return fs.Arg(0), nil
}

// KeySources holds the four key-source flags a seal CLI takes.
type KeySources struct {
	SignFile, SignValue, EncryptFile, EncryptValue string
}

// KeyFlags registers -sign-key-file, -sign-key-value, -encrypt-key-file and
// -encrypt-key-value on fs and returns the struct they bind to. signUse and
// encryptUse are the per-CLI purpose clauses appended to the two -key-file
// help strings ("used to sign the outbound JWS", "used to encrypt the subject
// member"), so each command keeps naming what its own keys are for.
func KeyFlags(fs *flag.FlagSet, signUse, encryptUse string) *KeySources {
	k := &KeySources{}
	fs.StringVar(&k.SignFile, "sign-key-file", "",
		"path to a DER-encoded RSA private key (PKCS#8 or PKCS#1) "+signUse)
	fs.StringVar(&k.SignValue, "sign-key-value", "",
		"base64-encoded DER RSA private key (alternative to -sign-key-file; argv is process-visible — fixture keys only)")
	fs.StringVar(&k.EncryptFile, "encrypt-key-file", "",
		"path to a DER-encoded RSA public key (PKIX) "+encryptUse)
	fs.StringVar(&k.EncryptValue, "encrypt-key-value", "",
		"base64-encoded DER RSA public key (alternative to -encrypt-key-file)")
	return k
}

// Validate enforces exactly-one-of per key-source pair. keymaterial's loaders
// let the file source win when both are set, so the choice has to be refused
// by the caller; each CLI runs this first in its own flag validation, which is
// what keeps the refusals ahead of the required-flag messages in stderr.
func (k *KeySources) Validate() error {
	return validateKeyPair(k.SignFile, k.SignValue, k.EncryptFile, k.EncryptValue)
}

// validateKeyPair is the exactly-one-of-per-pair rule itself, shared with the
// consumer-role sources so both binaries refuse in one vocabulary.
func validateKeyPair(signFile, signValue, encryptFile, encryptValue string) error {
	if !exactlyOne(signFile, signValue) {
		return errors.New("exactly one of -sign-key-file or -sign-key-value is required")
	}
	if !exactlyOne(encryptFile, encryptValue) {
		return errors.New("exactly one of -encrypt-key-file or -encrypt-key-value is required")
	}
	return nil
}

// Load re-runs Validate — so the type is safe to use without the CLI-side call
// — then loads and parses both keys and returns the producer-role resolver
// under the given kids. The refusals precede any I/O, so a mistyped invocation
// costs no file read.
func (k *KeySources) Load(signKid, encryptKid string) (*keymaterial.ProducerKeys, error) {
	signPriv, encPub, err := loadKeyPair(k.SignFile, k.SignValue, k.EncryptFile, k.EncryptValue,
		keymaterial.LoadRSAPrivateKey, keymaterial.LoadRSAPublicKey)
	if err != nil {
		return nil, err
	}
	return &keymaterial.ProducerKeys{
		SignKid:    signKid,
		SignPriv:   signPriv,
		EncryptKid: encryptKid,
		EncPub:     encPub,
	}, nil
}

// loadKeyPair is the loading half both roles run: validate, then load each source with the
// loader its role gives that half, under the "sign key: " / "encrypt key: " prefixes the
// CLIs report. Only the two loaders and the struct assembled from the result differ between
// the roles, so only those stay in the callers.
func loadKeyPair[S, E any](
	signFile, signValue, encryptFile, encryptValue string,
	loadSign func(file, value string) (S, error),
	loadEncrypt func(file, value string) (E, error),
) (sign S, encrypt E, err error) {
	var noSign S
	var noEncrypt E

	if vErr := validateKeyPair(signFile, signValue, encryptFile, encryptValue); vErr != nil {
		return noSign, noEncrypt, vErr
	}
	if sign, err = loadSign(signFile, signValue); err != nil {
		return noSign, noEncrypt, fmt.Errorf("sign key: %w", err)
	}
	if encrypt, err = loadEncrypt(encryptFile, encryptValue); err != nil {
		return noSign, noEncrypt, fmt.Errorf("encrypt key: %w", err)
	}
	return sign, encrypt, nil
}

// exactlyOne reports whether precisely one of a, b is a non-empty string.
func exactlyOne(a, b string) bool {
	return (a != "") != (b != "")
}

// Uncapped is the max a caller passes to ReadPayloadCapped to ask for no ceiling at all.
const Uncapped int64 = 0

// MaxPayloadBytes is the ceiling open-event applies. A sealed body has a ~1.4 KB floor and
// an event document is orders of magnitude under it; the cap exists so a mistyped path (a
// log, a core dump, a tarball) is refused at the door rather than buffered whole and then
// refused by the JSON or JOSE parser. It is not a package-wide policy: each command
// chooses, and the sealing CLIs deliberately stay uncapped.
const MaxPayloadBytes int64 = 1 << 20 // 1 MiB

// ErrPayloadTooLarge is what a payload over the caller's limit refuses with; only
// ReadPayloadCapped returns it.
var ErrPayloadTooLarge = errors.New("payload exceeds the size limit")

// ReadPayload reads the whole payload from the positional file argument, or from stdin
// when the path is absent ("") or "-", with no size limit. A command that wants one calls
// ReadPayloadCapped instead: the ceiling is the CALLER's policy, so adding one to a new
// binary cannot shrink what an existing one accepts.
func ReadPayload(path string, stdin io.Reader) ([]byte, error) {
	return ReadPayloadCapped(path, stdin, Uncapped)
}

// ReadPayloadCapped is ReadPayload with a ceiling: at most limit bytes are accepted, one byte
// more is ErrPayloadTooLarge and nothing is returned. limit of Uncapped (or any value below
// it) reads whatever the source holds.
func ReadPayloadCapped(path string, stdin io.Reader, limit int64) ([]byte, error) {
	if path == "" || path == "-" {
		data, err := io.ReadAll(capReader(stdin, limit))
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return checkSize(data, limit)
	}

	// #nosec G304,G703 -- operator-named CLI input file; reading it is this command's purpose
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read payload file: %w", err)
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(capReader(file, limit))
	if err != nil {
		return nil, fmt.Errorf("read payload file: %w", err)
	}
	return checkSize(data, limit)
}

// capReader stops one byte PAST the limit, so a payload exactly at it still reads whole
// while the first byte over is what makes the overrun observable.
func capReader(r io.Reader, limit int64) io.Reader {
	if limit <= Uncapped {
		return r
	}
	return io.LimitReader(r, limit+1)
}

// checkSize refuses a payload that reached past the limit, returning no bytes at all.
func checkSize(data []byte, limit int64) ([]byte, error) {
	if limit > Uncapped && int64(len(data)) > limit {
		return nil, fmt.Errorf("%w of %d bytes", ErrPayloadTooLarge, limit)
	}
	return data, nil
}
