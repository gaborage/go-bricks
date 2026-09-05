package sealed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// errSubjectCaseFoldTwin names nothing from the document on purpose: the twin's name is
	// by construction a case variant of the Subject path the caller already knows, and any
	// other document byte is caller data the error path must not carry (ADR-081 class).
	errSubjectCaseFoldTwin = errors.New("a clear member case-folds to the subject member")
)

// pinSubject is the SEALER's view of a document: locateSubject's rules plus the G9 case-fold
// rule enforced on the serialized bytes. ScanType applies G9 to a struct's declared fields,
// which a custom MarshalJSON can bypass and a raw document never went through at all, so the
// rule lives here where both doors meet. This is the only call site that asks for the rule;
// the opener goes through locateSubject, so its rule table is untouched.
func pinSubject(doc []byte, path string) (subjectSpan, error) {
	return walkToSubject(doc, path, true)
}

// locateSubject is the OPENER's door onto the same walk (rule 10); it judges nothing beyond
// the rules below.
func locateSubject(doc []byte, path string) (subjectSpan, error) {
	return walkToSubject(doc, path, false)
}

// walkToSubject walks the top-level members of doc and returns the span of the member named
// path. The walk is token-level: values are consumed as json.RawMessage, which the decoder
// copies verbatim from the input, so end-start == len(value) exactly.
//
// refuseCaseFoldTwin adds the sealer's G9 rule: a top-level member whose name case-folds to
// path without equalling it is refused, because encoding/json matches members
// case-insensitively on decode and a consumer would read the clear twin instead of the sealed
// member. The first such member ends the walk, so on a document that breaks another rule as
// well the refusal is whichever the walk reaches first — all of them are SEAL_DOCUMENT_INVALID
// at the door. The opener passes false and never reaches that branch.
func walkToSubject(doc []byte, path string, refuseCaseFoldTwin bool) (subjectSpan, error) {
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
			if refuseCaseFoldTwin && strings.EqualFold(key, path) {
				return subjectSpan{}, errSubjectCaseFoldTwin
			}
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

// expectDelim consumes the next token and requires it to be the delimiter want; any other
// token, or a decoder failure, is errDocNotObject (decoder failures by type only).
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
	quoted := append(append([]byte{'"'}, compact...), '"')
	return spliceRaw(doc, span, quoted), nil
}

// spliceRaw returns doc with the span replaced by replacement verbatim — the opener's
// reverse step (decrypted plaintext back over the JWE string) uses it with raw JSON. The
// three slices are copied into a fresh buffer grown by append (no precomputed size, so no
// length arithmetic on caller-sized inputs); doc is not mutated.
func spliceRaw(doc []byte, span subjectSpan, replacement []byte) []byte {
	out := append([]byte(nil), doc[:span.start]...)
	out = append(out, replacement...)
	return append(out, doc[span.end:]...)
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
