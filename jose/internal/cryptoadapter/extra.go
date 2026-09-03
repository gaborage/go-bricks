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

// maxPeekHeaderBytes bounds segment 0 before PeekProtectedHeader decodes it. A protected
// header is a handful of short params; anything larger on an unauthenticated body is
// rejected before it costs a base64 or JSON pass.
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
func PeekProtectedHeader(compact string) (Header, error) {
	segments := strings.SplitN(compact, ".", 6)
	if len(segments) != 3 && len(segments) != 5 {
		return Header{}, fmt.Errorf("%w: expected 3 or 5 segments, got %d", ErrPeekMalformed, len(segments))
	}
	if len(segments[0]) > maxPeekHeaderBytes {
		return Header{}, fmt.Errorf("%w: segment 0 exceeds %d bytes", ErrPeekMalformed, maxPeekHeaderBytes)
	}
	raw, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		return Header{}, fmt.Errorf("%w: segment 0 is not base64url", ErrPeekMalformed)
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil || params == nil {
		return Header{}, fmt.Errorf("%w: segment 0 is not a JSON object", ErrPeekMalformed)
	}
	return newHeader(stringParam(params, "kid"), stringParam(params, "alg"), params), nil
}

// newHeader builds a Header from the promoted kid/alg and a protected-header param map
// (go-jose's ExtraHeaders, or the raw object PeekProtectedHeader decodes). Owned params
// fill the typed fields; everything else lands in Extra, nil when nothing remains.
func newHeader[K ~string](kid, alg string, params map[K]any) Header {
	return Header{
		Kid:   kid,
		Alg:   alg,
		Enc:   stringParam(params, "enc"),
		Cty:   stringParam(params, string(jose.HeaderContentType)),
		Typ:   stringParam(params, string(jose.HeaderType)),
		Extra: filterOwned(params),
	}
}

func stringParam[K ~string](params map[K]any, name string) string {
	s, _ := params[K(name)].(string)
	return s
}

func filterOwned[K ~string](params map[K]any) map[string]any {
	var out map[string]any
	for k, v := range params {
		if _, owned := ownedParams[string(k)]; owned {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(params))
		}
		out[string(k)] = v
	}
	return out
}

// checkExtra rejects Extra entries that name an adapter-owned or JOSE-reserved param.
func checkExtra(extra map[string]any) error {
	for k := range extra {
		_, owned := ownedParams[k]
		_, reserved := reservedParams[k]
		if owned || reserved {
			return fmt.Errorf("%w: %q", ErrExtraCollision, k)
		}
	}
	return nil
}
