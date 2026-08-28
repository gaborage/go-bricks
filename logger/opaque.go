package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"unicode"
)

// errTrailingJSON marks a payload that decoded one value and had more bytes
// after it, which is not a single JSON document.
var errTrailingJSON = errors.New("trailing content after JSON document")

// errPayloadTooDeep marks a payload nested deeper than the walk's budget.
var errPayloadTooDeep = errors.New("payload nested deeper than the filter walks")

// opaqueBytes reports whether value is a byte slice — json.RawMessage included,
// since it is one by definition — and hands back its bytes. A named byte-slice
// type counts: what matters is that the NAME filter sees a single leaf where
// the payload may carry many named fields of its own.
func opaqueBytes(value any) (payload []byte, ok bool) {
	switch v := value.(type) {
	case json.RawMessage:
		return v, true
	case []byte:
		return v, true
	default:
		return nil, false
	}
}

// looksLikeJSON reports whether a payload is worth handing to the parser: its
// first non-space byte opens an object or an array. Deliberately narrow — a
// bare JSON number, string or `null` is a scalar the name filter already judged
// by its key, and parsing every string that happens to start with a digit would
// put the decoder on the hot path of ordinary logging for nothing.
func looksLikeJSON(payload []byte) bool {
	for _, b := range payload {
		if unicode.IsSpace(rune(b)) {
			continue
		}
		return b == '{' || b == '['
	}
	return false
}

// filterOpaquePayload masks inside a payload the name filter would otherwise
// treat as one leaf. The payload is re-encoded ONLY when something was actually
// masked; a clean payload ships byte-exact, which keeps the common path free of
// any re-serialization and its formatting drift (key order, number spelling,
// whitespace).
//
// A payload that LOOKS like JSON and does not parse is masked whole rather than
// passed through: it is opaque by definition — the filter cannot see what is in
// it — and the reason this door exists is that opaque payloads were shipping
// secrets in clear.
func (f *SensitiveDataFilter) filterOpaquePayload(payload []byte, original any) any {
	cap := f.maxPayloadBytes()
	if cap < 0 {
		return original
	}
	if !looksLikeJSON(payload) {
		return original
	}
	if len(payload) > cap {
		return f.config.MaskValue
	}

	decoded, err := decodeJSONPayload(payload)
	if err != nil {
		return f.config.MaskValue
	}

	masked, changed, err := f.maskJSONValue(decoded, DefaultMaxDepth)
	if err != nil {
		// Too deep to walk means too deep to vouch for: the same fail-closed
		// rule an unparseable payload takes, and the same one the name filter
		// applies when its own recursion budget runs out.
		return f.config.MaskValue
	}
	if !changed {
		return original
	}

	reencoded, err := json.Marshal(masked)
	if err != nil {
		// Nothing built from a decoded document should fail to re-encode, but a
		// masked document must never fall back to the ORIGINAL bytes: that is
		// the secret this call just decided to hide.
		return f.config.MaskValue
	}
	return json.RawMessage(reencoded)
}

// decodeJSONPayload decodes with UseNumber so a number that survives the walk
// re-encodes with the digits it arrived with. Without it 1e3 comes back 1000
// and an int64 beyond float64's exact range comes back rounded — a payload the
// filter never masked would still be altered by passing through it.
func decodeJSONPayload(payload []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()

	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	// Trailing content means the payload was not one document; treat it as
	// unparseable rather than silently logging only its first value.
	if decoder.More() {
		return nil, errTrailingJSON
	}
	return decoded, nil
}

// jwkPrivateMembers are the JWK members that ARE the private key, across every
// key type RFC 7517/7518 defines: RSA (d, p, q, dp, dq, qi, oth), EC and OKP
// (d), and oct (k). None of them is a name any needle matches — they are one
// and two letters long, and a bare "d" or "k" needle would mask half the fields
// in an ordinary document. The marker is what makes them safe to name: an
// object carrying `kty` is a JWK, so inside THAT object these eight are key
// material rather than short field names.
var jwkPrivateMembers = map[string]struct{}{
	"d": {}, "p": {}, "q": {}, "dp": {}, "dq": {}, "qi": {}, "k": {}, "oth": {},
}

// pemPrivateKeyPattern matches a PEM header whose label ends in PRIVATE KEY —
// `RSA PRIVATE KEY`, `EC PRIVATE KEY`, `PRIVATE KEY`, `OPENSSH PRIVATE KEY`.
// A CERTIFICATE or PUBLIC KEY block deliberately does not match: it is public,
// and masking it would remove exactly what an operator reads to diagnose a TLS
// problem.
var pemPrivateKeyPattern = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)

// isJWK reports whether a decoded object is a JWK, by the presence of the `kty`
// member RFC 7517 requires on every one.
func isJWK(object map[string]any) bool {
	_, hasKeyType := object["kty"]
	return hasKeyType
}

// looksLikePEMPrivateKey reports whether a string carries a PEM private-key
// block. The whole string is masked when it does — a PEM block is base64 of the
// key itself, so there is no part of it worth keeping.
func looksLikePEMPrivateKey(value string) bool {
	return pemPrivateKeyPattern.MatchString(value)
}

// maskJSONValue walks a decoded document with the same needles the name filter
// uses, reporting whether anything was masked so the caller can keep the
// original bytes when nothing was.
//
// Recursion is bounded by the same DefaultMaxDepth budget the name filter
// spends, and exhausting it is an ERROR rather than a masked subtree: the
// depth of this document is chosen by whoever produced the payload, not by the
// code logging it, so an arbitrarily nested body must not be able to drive the
// walk down an unbounded stack. The caller masks the whole payload, which is
// the same answer it gives for a payload it cannot parse.
func (f *SensitiveDataFilter) maskJSONValue(value any, depth int) (result any, changed bool, err error) {
	if depth <= 0 {
		return nil, false, errPayloadTooDeep
	}

	switch v := value.(type) {
	case map[string]any:
		// The JWK test is done once per object, not per member, and applies to
		// THIS object only — a nested JWK is judged by its own kty, and an
		// object that merely contains one does not inherit the rule.
		insideJWK := isJWK(v)
		for key, child := range v {
			if f.isSensitiveField(key) || (insideJWK && isJWKPrivateMember(key)) {
				v[key] = f.config.MaskValue
				changed = true
				continue
			}
			masked, childChanged, childErr := f.maskJSONValue(child, depth-1)
			if childErr != nil {
				return nil, false, childErr
			}
			if childChanged {
				v[key] = masked
				changed = true
			}
		}
		return v, changed, nil
	case []any:
		for i, child := range v {
			masked, childChanged, childErr := f.maskJSONValue(child, depth-1)
			if childErr != nil {
				return nil, false, childErr
			}
			if childChanged {
				v[i] = masked
				changed = true
			}
		}
		return v, changed, nil
	case string:
		// A PEM private key is one long opaque string under whatever field name
		// the consumer chose, so neither the name filter nor the JWK rule sees
		// it; the block header is the only thing that identifies it.
		if looksLikePEMPrivateKey(v) {
			return f.config.MaskValue, true, nil
		}
		return value, false, nil
	default:
		return value, false, nil
	}
}

// isJWKPrivateMember reports whether a member name is key material, matched
// EXACTLY rather than by substring: the names are one and two letters long, so
// a substring rule would mask every field containing a d or a k.
func isJWKPrivateMember(name string) bool {
	_, private := jwkPrivateMembers[name]
	return private
}

// filterJSONString offers a string value to the payload door, reporting whether
// the door took it. A string is only a payload when it LOOKS like one, and the
// caller keeps its ordinary string rendering otherwise — so a log line full of
// names, ids and messages never reaches the parser.
//
// The key is judged first: a sensitive NAME masks the whole value whatever its
// shape, which is the existing contract and must not be weakened by the payload
// door finding nothing to mask inside.
func (f *SensitiveDataFilter) filterJSONString(key, value string) (payload any, handled bool) {
	if f == nil {
		return nil, false
	}
	if f.isSensitiveField(key) {
		return nil, false
	}
	if !looksLikeJSON([]byte(value)) {
		return nil, false
	}
	filtered := f.filterOpaquePayload([]byte(value), value)
	if original, unchanged := filtered.(string); unchanged {
		// The door declined: hand it back as the plain string it was, so the
		// caller renders it with zerolog's Str and nothing is re-encoded.
		_ = original
		return nil, false
	}
	return filtered, true
}

// maxPayloadBytes resolves the configured cap. Zero means "unset", which takes
// the default rather than disabling the door — a config built as a bare struct
// literal must not silently opt out of masking. A negative value is the
// explicit opt-out.
func (f *SensitiveDataFilter) maxPayloadBytes() int {
	if f.config.MaxPayloadBytes == 0 {
		return DefaultMaxPayloadBytes
	}
	return f.config.MaxPayloadBytes
}
