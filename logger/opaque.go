package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
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
func looksLikeJSON[T ~string | ~[]byte](payload T) bool {
	for i := range len(payload) {
		b := payload[i]
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		return b == '{' || b == '['
	}
	return false
}

// pemBeginMarker gates the PEM regexp. Every string in every parsed payload is
// tested for a private-key block, and a substring scan for this marker is far
// cheaper than the pattern — which then runs only for the vanishingly few values
// that carry a PEM header at all.
const pemBeginMarker = "-----BEGIN"

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
func filterOpaquePayload[T ~string | ~[]byte](f *SensitiveDataFilter, payload T, original any) any {
	limit := f.maxPayloadBytes()
	if limit < 0 {
		return original
	}

	// A payload that is not JSON-shaped gets ONE more question asked of it,
	// because a PEM block opens with `-----BEGIN` and so can never pass the JSON
	// gate: is the payload itself a private key? Gating the PEM rule on
	// looksLikeJSON left it reachable only for a key already embedded inside a
	// JSON document, while a key logged on its own — the ordinary way one
	// reaches a log call — sailed past untouched.
	//
	// The order matters and is not interchangeable. Asking about PEM FIRST would
	// mask a whole JSON envelope merely because one of its members holds a key,
	// throwing away every other field in it; a JSON payload is walked instead,
	// and maskJSONValue masks that member and only that member.
	if !looksLikeJSON(payload) {
		if looksLikePEMPrivateKey(payload) {
			return f.config.MaskValue
		}
		return original
	}
	if len(payload) > limit {
		return f.config.MaskValue
	}

	decoded, err := decodeJSONPayload([]byte(payload))
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
	// Trailing content means the payload was not one document, and it is
	// rejected by DECODING AGAIN and demanding io.EOF rather than by asking
	// More(). More() answers "is there another element in the current context",
	// which is not the same question: `{}]{"password":"pw"}` leaves a `]` next,
	// which reads as a closing delimiter rather than another value, so More()
	// says no. The walk would then have masked the empty object it decoded,
	// found nothing, and — byte-exactness being the rule for a clean payload —
	// shipped the ORIGINAL bytes, password and all. Fail closed instead: one
	// document or nothing.
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
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
var pemPrivateKeyPattern = regexp.MustCompile(`-{5}BEGIN [A-Z0-9 ]*PRIVATE KEY-{5}`)

// isJWK reports whether a decoded object is a JWK, by the presence of the `kty`
// member RFC 7517 requires on every one.
func isJWK(object map[string]any) bool {
	_, hasKeyType := object["kty"]
	return hasKeyType
}

// looksLikePEMPrivateKey reports whether a string carries a PEM private-key
// block. The whole string is masked when it does — a PEM block is base64 of the
// key itself, so there is no part of it worth keeping.
func looksLikePEMPrivateKey[T ~string | ~[]byte](value T) bool {
	if !containsPEMBeginMarker(value) {
		return false
	}
	// The conversion is paid only by a value that already carries a PEM header,
	// which is the rare case; the marker scan above is what every other value
	// pays, and it allocates nothing for bytes or for a string.
	return pemPrivateKeyPattern.MatchString(string(value))
}

// containsPEMBeginMarker scans for the header without converting between string
// and []byte. Converting first is what a bytes payload must not pay: it copies
// the whole slice on the ordinary path where the answer is no, which a
// benchmark caught as 32 B and one allocation for a non-JSON byte slice.
func containsPEMBeginMarker[T ~string | ~[]byte](value T) bool {
	width := len(pemBeginMarker)
	for i := 0; i+width <= len(value); i++ {
		if value[i] != '-' {
			continue
		}
		// Comparing a converted sub-slice against a constant does not allocate:
		// the compiler recognizes the shape.
		if string(value[i:i+width]) == pemBeginMarker {
			return true
		}
	}
	return false
}

// maskJSONValue walks a decoded document with the same needles the name filter
// uses, reporting whether anything was masked so the caller can keep the
// original bytes when nothing was.
//
// This is a SECOND walker beside filterValueWithProtection, which traverses the
// same two shapes a decoded document can hold, and the duplication is
// deliberate on two counts. It REPORTS whether anything changed, where the
// shared walker always rebuilds — a rebuilt map is indistinguishable from an
// untouched one, and detecting the difference by comparing input to output is
// what panics on the uncomparable dynamic types a decoded document is full of
// (see filterSliceOrArrayWithProtection's own note). And it carries the two
// payload shape rules, which are per-OBJECT (a JWK's kty marker) and per-STRING
// (a PEM block): hooking those into the shared walker would put JSON-payload
// knowledge on the path of every struct and map field the framework ever logs.
//
// What the two share is the part that must not drift: the DefaultMaxDepth
// budget, isSensitiveField, and config.MaskValue. Change the masking rule in
// one and check the other.
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

// opaquePayloadValue is the payload door for a caller that still holds its
// payload CONCRETELY, as a string or a byte slice. It reports whether the door
// took the value, so an untouched payload never has to be boxed into an `any`
// merely to be handed back — a slice header does not fit in an interface word,
// so that box is a real allocation on a path every logged byte slice takes.
//
// filterOpaquePayload answers with the ORIGINAL when it declines, which is what
// makes the comparison below sound: identity, not equality.
func opaquePayloadValue[T ~string | ~[]byte](f *SensitiveDataFilter, payload T) (filtered any, handled bool) {
	if f == nil {
		return nil, false
	}
	result := filterOpaquePayload(f, payload, declinedPayload{})
	if _, declined := result.(declinedPayload); declined {
		return nil, false
	}
	return result, true
}

// declinedPayload is the sentinel filterOpaquePayload hands back untouched when
// it decides a payload is not one. A distinct empty type rather than nil: nil is
// a value a masked payload could in principle be, and an empty struct costs no
// allocation to box.
type declinedPayload struct{}
