// Package sealcli hosts the flag, key and payload plumbing the seal-payload
// and seal-event commands share: the four key-source flags with their help
// text, the exactly-one-of-per-pair refusal keymaterial deliberately leaves to
// its callers, and the file-or-stdin payload read. Each command keeps only its
// own flags, its own required-flag checks and its own sealing call.
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
	if !exactlyOne(k.SignFile, k.SignValue) {
		return errors.New("exactly one of -sign-key-file or -sign-key-value is required")
	}
	if !exactlyOne(k.EncryptFile, k.EncryptValue) {
		return errors.New("exactly one of -encrypt-key-file or -encrypt-key-value is required")
	}
	return nil
}

// Load re-runs Validate — so the type is safe to use without the CLI-side call
// — then loads and parses both keys and returns the producer-role resolver
// under the given kids. The refusals precede any I/O, so a mistyped invocation
// costs no file read.
func (k *KeySources) Load(signKid, encryptKid string) (*keymaterial.ProducerKeys, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}

	signPriv, err := keymaterial.LoadRSAPrivateKey(k.SignFile, k.SignValue)
	if err != nil {
		return nil, fmt.Errorf("sign key: %w", err)
	}

	encPub, err := keymaterial.LoadRSAPublicKey(k.EncryptFile, k.EncryptValue)
	if err != nil {
		return nil, fmt.Errorf("encrypt key: %w", err)
	}

	return &keymaterial.ProducerKeys{
		SignKid:    signKid,
		SignPriv:   signPriv,
		EncryptKid: encryptKid,
		EncPub:     encPub,
	}, nil
}

// exactlyOne reports whether precisely one of a, b is a non-empty string.
func exactlyOne(a, b string) bool {
	return (a != "") != (b != "")
}

// ReadPayload reads the payload from the positional file argument, or from
// stdin when the path is absent ("") or "-".
func ReadPayload(path string, stdin io.Reader) ([]byte, error) {
	if path == "" || path == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return data, nil
	}

	// #nosec G304,G703 -- operator-named CLI input file; reading it is this command's purpose
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read payload file: %w", err)
	}
	return data, nil
}
