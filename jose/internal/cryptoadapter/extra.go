package cryptoadapter

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
)

// Sentinel errors for the extra-header seam and the pre-verify peek.
var (
	// ErrExtraCollision is returned by Sign/Encrypt when Extra names an adapter-owned or
	// JOSE-reserved param.
	ErrExtraCollision = errors.New("cryptoadapter: extra header collides with a reserved param")
	// ErrExtraAbsent is returned by the typed accessors when the header is not present.
	ErrExtraAbsent = errors.New("cryptoadapter: extra header absent")
	// ErrExtraMalformed is returned by the typed accessors when the header has the wrong shape.
	ErrExtraMalformed = errors.New("cryptoadapter: extra header malformed")
	// ErrPeekMalformed is returned by PeekProtectedHeader when the input is not a compact
	// serialization whose first segment is a base64url-encoded JSON object.
	ErrPeekMalformed = errors.New("cryptoadapter: protected header peek failed")
	// ErrHeaderTooLarge names the size refusal inside the chain, so a framework-internal caller
	// matching with errors.Is can tell an over-bound header from any other parse failure. This
	// package is internal/, so no consumer sees it; the wire sees only the generic code, and
	// the class is deliberately not logged.
	ErrHeaderTooLarge = errors.New("cryptoadapter: protected header exceeds bound")
	// ErrNotCompact names the refusal of a body that is not a compact serialization — a JSON
	// serialization, most importantly, which go-jose accepts and whose dot-delimited runs say
	// nothing about the size of its protected member.
	ErrNotCompact = errors.New("cryptoadapter: not a compact serialization")
)

// ownedParams are the protected-header names the adapter writes itself; Extra may not
// name them and Header.Extra never carries them (they fill the typed fields instead).
var ownedParams = map[string]struct{}{
	"alg": {}, "enc": {}, "kid": {}, "cty": {}, "typ": {},
}

// reservedParams are the remaining names go-jose interprets when producing or consuming a
// token (RFC 7515/7516/7797 registered params). Writing one through Extra would change wire
// semantics — "b64" flips the signed bytes, "zip" claims compression that never happened —
// so Extra may not name them either. They ARE surfaced in Header.Extra on read, so an opener
// can apply its own policy (e.g. reject "crit").
var reservedParams = map[string]struct{}{
	"zip": {}, "crit": {}, "apu": {}, "apv": {}, "epk": {}, "iv": {}, "tag": {},
	"x5c": {}, "x5t": {}, "x5t#S256": {}, "x5u": {}, "jku": {}, "jwk": {}, "nonce": {},
	"b64": {}, "p2c": {}, "p2s": {},
}

// maxExactInt is the largest magnitude a float64 represents exactly for every integer.
const maxExactInt = 1 << 53

// maxPeekHeaderBytes bounds segment 0 on every door that parses a compact — peek, decrypt
// and verify alike. A protected header is a handful of short params; anything larger on an
// unauthenticated body is rejected before it costs a base64 or JSON pass.
const maxPeekHeaderBytes = 16 * 1024

// ExtraString returns the named extra header when it is present and a string.
func (h *Header) ExtraString(name string) (string, bool) {
	if h == nil {
		return "", false
	}
	s, ok := h.Extra[name].(string)
	return s, ok
}

// ExtraInt64 returns the named extra header as an int64. JSON numbers decode as float64,
// so a non-integral value, a magnitude beyond 2^53 (no longer exact), or a non-number is
// ErrExtraMalformed; a missing header is ErrExtraAbsent.
func (h *Header) ExtraInt64(name string) (int64, error) {
	v, ok := h.lookup(name)
	if !ok {
		return 0, ErrExtraAbsent
	}
	n, isNum := v.(float64)
	if !isNum {
		return 0, fmt.Errorf("%w: %q is not a number", ErrExtraMalformed, name)
	}
	if n != math.Trunc(n) || math.Abs(n) > maxExactInt {
		return 0, fmt.Errorf("%w: %q is not an exactly representable integer", ErrExtraMalformed, name)
	}
	return int64(n), nil
}

// ExtraStringSlice returns the named extra header as []string. JSON arrays decode as
// []any; a non-array or a non-string member is ErrExtraMalformed; a missing header is
// ErrExtraAbsent.
func (h *Header) ExtraStringSlice(name string) ([]string, error) {
	v, ok := h.lookup(name)
	if !ok {
		return nil, ErrExtraAbsent
	}
	arr, isArr := v.([]any)
	if !isArr {
		return nil, fmt.Errorf("%w: %q is not an array", ErrExtraMalformed, name)
	}
	out := make([]string, len(arr))
	for i, m := range arr {
		s, isStr := m.(string)
		if !isStr {
			return nil, fmt.Errorf("%w: %q member %d is not a string", ErrExtraMalformed, name, i)
		}
		out[i] = s
	}
	return out, nil
}

func (h *Header) lookup(name string) (any, bool) {
	if h == nil {
		return nil, false
	}
	v, ok := h.Extra[name]
	return v, ok
}

// PeekProtectedHeader decodes segment 0 of a compact JWS/JWE into a Header WITHOUT verifying
// or decrypting. No key material is touched; callers use it to run header rules (typ, alg,
// key resolution) before choosing a key for Verify/Decrypt. The returned header is
// unauthenticated until Verify succeeds.
//
// The input must be a compact serialization — 3 or 5 base64url segments, surrounding
// whitespace aside — whose protected header is at most maxPeekHeaderBytes; anything else is
// ErrPeekMalformed, with ErrNotCompact or ErrHeaderTooLarge naming which rule it broke.
func PeekProtectedHeader(compact string) (Header, error) {
	_, segments, err := boundedSegments(compact)
	if err != nil {
		return Header{}, fmt.Errorf("%w: %w", ErrPeekMalformed, err)
	}
	params, err := decodeProtected(segments[0])
	if err != nil {
		return Header{}, err
	}
	return newHeader(params), nil
}

// boundedSegments prepares a compact for parsing and refuses it on three grounds, in
// increasing cost: a part count that is not 3 or 5, a protected header past
// maxPeekHeaderBytes, and any segment that is not base64url. Every door that parses a
// compact — Peek, Decrypt, Verify — runs this first. go-jose rejects a wrong part count
// itself but imposes no size bound, and it accepts the JSON serialization too, whose
// dot-delimited runs say nothing about the size of its protected member — so the charset
// gate is what keeps the size bound meaningful. Surrounding whitespace is trimmed rather
// than refused, matching the stripWhitespace go-jose applies before its own parse; interior
// whitespace is refused, which is stricter. It RETURNS the trimmed body: Decrypt and Verify
// parse that, not the caller's string, so go-jose and parsedHeader see exactly the bytes this
// measured. Each caller wraps the error in its own sentinel.
func boundedSegments(compact string) (trimmed string, segments []string, err error) {
	trimmed = strings.TrimSpace(compact)
	segments = strings.SplitN(trimmed, ".", 6)
	if len(segments) != 3 && len(segments) != 5 {
		return "", nil, fmt.Errorf("%w: expected 3 or 5 segments, got %d", ErrNotCompact, len(segments))
	}
	if len(segments[0]) > maxPeekHeaderBytes {
		return "", nil, fmt.Errorf("%w: segment 0 exceeds %d bytes", ErrHeaderTooLarge, maxPeekHeaderBytes)
	}
	for _, segment := range segments {
		if !isBase64URL(segment) {
			return "", nil, fmt.Errorf("%w: segment is not base64url", ErrNotCompact)
		}
	}
	return trimmed, segments, nil
}

// isBase64URL reports whether s holds only unpadded base64url characters.
func isBase64URL(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// decodeProtected base64url-decodes one protected-header segment into its JSON object.
func decodeProtected(segment string) (map[string]any, error) {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return nil, fmt.Errorf("%w: segment 0 is not base64url", ErrPeekMalformed)
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil || params == nil {
		return nil, fmt.Errorf("%w: segment 0 is not a JSON object", ErrPeekMalformed)
	}
	return params, nil
}

// parsedHeader rebuilds the Header for a compact serialization go-jose has already
// accepted. It decodes segment 0 itself rather than reading go-jose's ExtraHeaders, which
// omits the params go-jose promotes to struct fields (jwk, nonce, x5c); this keeps Extra
// identical to what PeekProtectedHeader returned for the same bytes.
func parsedHeader(compact string) (Header, bool) {
	segment, _, _ := strings.Cut(compact, ".")
	params, err := decodeProtected(segment)
	if err != nil {
		return Header{}, false
	}
	return newHeader(params), true
}

// newHeader builds a Header from a decoded protected-header object. Owned params fill the
// typed fields; everything else lands in Extra, nil when nothing remains.
func newHeader(params map[string]any) Header {
	return Header{
		Kid:   stringParam(params, "kid"),
		Alg:   stringParam(params, "alg"),
		Enc:   stringParam(params, "enc"),
		Cty:   stringParam(params, string(jose.HeaderContentType)),
		Typ:   stringParam(params, string(jose.HeaderType)),
		Extra: filterOwned(params),
	}
}

func stringParam(params map[string]any, name string) string {
	s, _ := params[name].(string)
	return s
}

func filterOwned(params map[string]any) map[string]any {
	var out map[string]any
	for k, v := range params {
		if _, owned := ownedParams[k]; owned {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(params))
		}
		out[k] = v
	}
	return out
}

// CheckExtra rejects Extra entries that name an adapter-owned or JOSE-reserved param.
// Sign and Encrypt run it themselves; the parent jose package also runs it at policy
// validation time so a bad protected-header map fails at startup rather than per request.
func CheckExtra(extra map[string]any) error {
	for k := range extra {
		_, owned := ownedParams[k]
		_, reserved := reservedParams[k]
		if owned || reserved {
			return fmt.Errorf("%w: %q", ErrExtraCollision, k)
		}
	}
	return nil
}
