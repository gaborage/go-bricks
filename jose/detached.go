package jose

import (
	"encoding/base64"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// AttachDetachedJWS rebuilds the attached compact JWS Open takes from a detached one
// (header..signature, RFC 7515 Appendix F) and the compact JWE string it signed, returning
// header.base64url(jwe).signature. It does no crypto and touches no key: Open verifies the
// result.
//
// detached gets the checks every door applies (surrounding whitespace trimmed, an encoded
// protected header of at most 16 KiB, base64url only), then must be exactly three segments
// with an empty middle and a non-empty header and signature. jwe must be non-empty and is
// encoded verbatim, never trimmed or parsed: its exact bytes are what was signed, and Open
// applies every inner-JWE check after the signature verifies. Every refusal is a 400
// JOSE_MALFORMED. Finding the signature in an HTTP request (its header, the scheme prefix, a
// missing or repeated header, a cap on the whole value) is the caller's job.
func AttachDetachedJWS(detached, jwe string) (string, error) {
	segments, err := cryptoadapter.CompactSegments(detached)
	if err != nil {
		return "", errDetachedMalformed(err)
	}
	if len(segments) != 3 {
		return "", errDetachedMalformed(nil)
	}
	if segments[1] != "" {
		return "", errDetachedMalformed(nil)
	}
	if segments[0] == "" {
		return "", errDetachedMalformed(nil)
	}
	if segments[2] == "" {
		return "", errDetachedMalformed(nil)
	}
	if jwe == "" {
		return "", errDetachedMalformed(nil)
	}
	return segments[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(jwe)) + "." + segments[2], nil
}

func errDetachedMalformed(cause error) *Error {
	return &Error{
		Sentinel: ErrMalformed,
		Code:     codeMalformed,
		Status:   400,
		Message:  "Malformed detached JWS",
		Cause:    cause,
	}
}
