// Package secretfile hosts the error-hygiene guards shared by every code path
// that reads key material from an operator-named file. Consumers — the
// httpclient TLS loader, the server TLS listener, and the keystore — have
// sibling File/Value config fields, so a transposition puts inline material
// where a path belongs; without these guards it reaches a fatal startup log,
// which the logger's SensitiveDataFilter cannot mask because that filter
// matches field names, not error strings. LoadPEM is the shared file/value
// resolver httpclient and the server TLS listener both call; ParseTLSMinVersion
// is the shared min-version enum parser the same two callers share; ReadError
// is the shape any other read site should use directly; SafeRef is also
// useful on its own for any operator-supplied config string that must not be
// echoed unbounded.
//
// LooksLikeKeyMaterial catches PEM, base64 PEM, and base64 DER. It cannot catch
// raw symmetric secrets, which have no ASN.1 structure and may be short enough
// to slip MaxPathEcho — Errno still prevents the second copy, but the first is
// echoed. Widening the check to "any decodable base64" is not the fix: real
// paths such as /run/secrets/signingkey decode cleanly.
package secretfile

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// pemHeaderPrefix opens every PEM block, so a *File value containing it is PEM
// content pasted into the wrong field rather than a path.
const pemHeaderPrefix = "-----BEGIN"

// MaxPathEcho bounds what a read error quotes back. A *File value longer than
// this is elided: over-long values are the shape mis-filed material takes, and
// this error reaches startup logs.
const MaxPathEcho = 256

// minDERKeyBytes is the smallest real case this guard must still catch: an
// Ed25519 PKCS#8 private key marshals to exactly 48 bytes.
const minDERKeyBytes = 48

// LooksLikeKeyMaterial reports whether a *File value is inline key material
// rather than a path. It covers a raw PEM body, its base64 form (what a secret
// manager hands you, and so the likelier mix-up), and base64 of raw DER — which
// carries no PEM header at any level and, for EC and Ed25519 keys, is short
// enough to slip MaxPathEcho too. Decoding unpadded accepts both base64 forms:
// whether a value arrives padded otherwise depends on len(input)%3.
func LooksLikeKeyMaterial(file string) bool {
	if strings.Contains(file, pemHeaderPrefix) {
		return true
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(file, "="))
	if err != nil {
		return false
	}
	return strings.Contains(string(decoded), pemHeaderPrefix) || looksLikeDER(decoded)
}

// looksLikeDER reports whether b opens with an ASN.1 SEQUENCE — tag 0x30,
// which PKCS#8, SEC1, PKCS#12 and X.509 all start with — whose declared length
// matches the payload. A bare 0x30 first byte is not enough: short
// base64-alphabet paths such as "MAIN" decode to three bytes starting 0x30.
// Canonical-length encoding is deliberately not enforced: this is a guard, so a
// false negative echoes key material while a false positive only rejects a path.
func looksLikeDER(b []byte) bool {
	if len(b) < minDERKeyBytes || b[0] != 0x30 {
		return false
	}
	n := int(b[1])
	if n < 0x80 {
		return 2+n == len(b)
	}
	count := n & 0x7f
	if count == 0 || count > 4 || len(b) < 2+count {
		return false
	}
	length := 0
	for _, c := range b[2 : 2+count] {
		length = length<<8 | int(c)
		// Bail before the shift can overflow int on a 32-bit build: a declared
		// length past the payload can only grow, and an overflow would wrap to a
		// false negative — the direction that echoes key material.
		if length > len(b) {
			return false
		}
	}
	return 2+count+length == len(b)
}

// SafeRef renders an operator-supplied config value for an error message,
// eliding anything too long to be a plausible path.
// The bound applies to the %q-rendered form, not the raw value: escaping can
// expand a value well past MaxPathEcho, and the rendered form is what reaches
// the log.
func SafeRef(value string) string {
	quoted := fmt.Sprintf("%q", value)
	if len(quoted) > MaxPathEcho {
		return fmt.Sprintf("<%d-char value elided>", len(value))
	}
	return quoted
}

// LoadPEM reads one piece of PEM material from a file path or a base64-encoded
// value, returning (nil, nil) when neither source is set. prefix names the
// consumer's error namespace (e.g. "server: tls:"); what names the piece
// (e.g. "cert", "key", "ca").
func LoadPEM(prefix, file, value, what string) ([]byte, error) {
	switch {
	case file != "" && value != "":
		return nil, fmt.Errorf("%s %s: set file or value, not both", prefix, what)
	case file != "":
		// Never let mis-filed material reach os.ReadFile: both our wrap and the
		// *os.PathError it wraps would echo it into a startup error.
		if LooksLikeKeyMaterial(file) {
			return nil, fmt.Errorf("%s %s: file looks like key material, not a path (use the value field for inline PEM)", prefix, what)
		}
		// #nosec G304 -- the path is deployment configuration, not request input:
		// reading an operator-named file IS this function. Inline material is
		// rejected above.
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", prefix, what, ReadError(file, err))
		}
		return data, nil
	case value != "":
		// Never echo value here, bounded or not: an operator who pastes raw
		// (non-base64) PEM into this field fails decode, and for short key
		// types (Ed25519) SafeRef's %q-length bound does not trip — the
		// material would otherwise reach this error verbatim.
		data, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("%s %s: base64 decode failed: %w", prefix, what, err)
		}
		return data, nil
	default:
		return nil, nil
	}
}

// ParseTLSMinVersion maps the shared config enum to a tls.Config version
// constant: "" and "1.2" share the 1.2 floor, "1.3" raises it. prefix names
// the consumer's error namespace, as in LoadPEM. It lives here because both
// TLS material loaders already share this package.
func ParseTLSMinVersion(prefix, v string) (uint16, error) {
	switch v {
	case "", "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("%s minversion %s: accepted values are \"1.2\" and \"1.3\"", prefix, SafeRef(v))
	}
}

// ReadError shapes the error from a failed read of an operator-named file so a
// mis-filed value cannot reach a startup log: SafeRef bounds what is quoted and
// Errno drops the *os.PathError that would otherwise re-echo it in full. The
// message has no package prefix; callers wrap it with their own (e.g.
// fmt.Errorf("keystore: key %q: %w", name, err)). errors.Is still matches
// fs.ErrNotExist and friends through the wrap.
func ReadError(path string, err error) error {
	return fmt.Errorf("read file %s: %w", SafeRef(path), Errno(err))
}

// Errno strips the *os.PathError wrapper, which carries its own copy of the
// path and would re-echo a value SafeRef elided (the path may BE the secret).
// errors.Is is unaffected: the bare errno still matches fs.ErrNotExist and
// friends.
func Errno(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
