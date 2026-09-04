package sealed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// subjectSpan locates the Subject member inside the framework's own serialization of an
// event: the compact JSON document encoding/json produced. It returns the member's raw
// value and the byte span it occupies so the sealer can swap in the JWE string without
// re-encoding anything else — wire member order stays the struct order, clear members
// never pass through a map, and the Subject member is guaranteed present (G9).
type subjectSpan struct {
	value      json.RawMessage
	start, end int
}

var (
	errDocNotObject       = errors.New("document is not a JSON object")
	errSubjectAbsent      = errors.New("subject member absent from the document")
	errSubjectDuplicate   = errors.New("subject member appears more than once")
	errDocTrailingContent = errors.New("document has trailing content")
	errNotCompactJOSE     = errors.New("replacement is not a compact JOSE serialization")
)

// locateSubject walks the top-level members of doc and returns the span of the member
// named path. The walk is token-level: values are consumed as json.RawMessage, which the
// decoder copies verbatim from the input, so end-start == len(value) exactly.
func locateSubject(doc []byte, path string) (subjectSpan, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	if err := expectDelim(dec, '{'); err != nil {
		return subjectSpan{}, err
	}
	var found *subjectSpan
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return subjectSpan{}, docError(err)
		}
		key, _ := tok.(string) // in key position the decoder yields a string or an error, never another token
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return subjectSpan{}, docError(err)
		}
		if key != path {
			continue
		}
		if found != nil {
			return subjectSpan{}, errSubjectDuplicate
		}
		end := int(dec.InputOffset())
		found = &subjectSpan{value: raw, start: end - len(raw), end: end}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return subjectSpan{}, err
	}
	if dec.More() {
		return subjectSpan{}, errDocTrailingContent
	}
	if found == nil {
		return subjectSpan{}, errSubjectAbsent
	}
	return *found, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return docError(err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != want {
		return errDocNotObject
	}
	return nil
}

// docError wraps a decoder failure by type only: a *json.SyntaxError message quotes the
// offending input byte, which on this path could be a byte of the Subject plaintext.
func docError(err error) error {
	return fmt.Errorf("%w: %T", errDocNotObject, err)
}

// splice returns doc with the span replaced by compact as a JSON string. compact must be a
// compact JOSE serialization — base64url segments joined by dots — so quoting it needs no
// escaping and the output stays the framework's serialization with one value swapped.
func splice(doc []byte, span subjectSpan, compact string) ([]byte, error) {
	if !isCompactJOSE(compact) {
		return nil, errNotCompactJOSE
	}
	quoted := make([]byte, 0, len(compact)+2)
	quoted = append(quoted, '"')
	quoted = append(quoted, compact...)
	quoted = append(quoted, '"')
	return spliceRaw(doc, span, quoted), nil
}

// spliceRaw returns doc with the span replaced by replacement verbatim — the opener's
// reverse step (decrypted plaintext back over the JWE string) uses it with raw JSON. The
// three slices are copied into a fresh buffer; doc is not mutated.
func spliceRaw(doc []byte, span subjectSpan, replacement []byte) []byte {
	out := make([]byte, 0, len(doc)-len(span.value)+len(replacement))
	out = append(out, doc[:span.start]...)
	out = append(out, replacement...)
	out = append(out, doc[span.end:]...)
	return out
}

// isCompactJOSE reports whether s consists only of base64url characters and dots.
func isCompactJOSE(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}
