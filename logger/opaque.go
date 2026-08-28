package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode"
)

// errTrailingJSON marks a payload that decoded one value and had more bytes
// after it, which is not a single JSON document.
var errTrailingJSON = errors.New("trailing content after JSON document")

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
	if !looksLikeJSON(payload) {
		return original
	}

	decoded, err := decodeJSONPayload(payload)
	if err != nil {
		return f.config.MaskValue
	}

	masked, changed := f.maskJSONValue(decoded)
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

// maskJSONValue walks a decoded document with the same needles the name filter
// uses, reporting whether anything was masked so the caller can keep the
// original bytes when nothing was.
func (f *SensitiveDataFilter) maskJSONValue(value any) (result any, changed bool) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if f.isSensitiveField(key) {
				v[key] = f.config.MaskValue
				changed = true
				continue
			}
			masked, childChanged := f.maskJSONValue(child)
			if childChanged {
				v[key] = masked
				changed = true
			}
		}
		return v, changed
	case []any:
		for i, child := range v {
			masked, childChanged := f.maskJSONValue(child)
			if childChanged {
				v[i] = masked
				changed = true
			}
		}
		return v, changed
	default:
		return value, false
	}
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
