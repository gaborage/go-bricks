// Package saferender renders decode and validation failures without echoing the
// input that caused them. Both the AMQP typed-consumer path and the HTTP request
// pipeline turn such failures into text that leaves the process — a nacked
// message's log line, a 400 response body — and the input behind them is partner
// PII/PCI on either side, so the rendering rules belong to one package instead of
// being re-derived per transport.
package saferender

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// RedactedIndex replaces the contents of every bracketed namespace segment.
const RedactedIndex = "[*]"

// RedactNamespace redacts everything from a namespace's first '[' to its last
// ']'. validator interpolates map keys verbatim, so Limits[4111111111111111]
// would otherwise carry a PAN — and a key may itself contain '[', ']' or '.',
// so no per-segment parse of the flat string is unambiguous. Redacting the whole
// bracketed span is the only rule a hostile key cannot walk out of: a key of
// "]4111111111111111" defeats a depth counter, which returns to zero at the
// key's own ']' and then copies the digits straight through.
//
// Numeric indices go too — a digits-only exemption looks safe but is not, since
// a card number is all digits. A namespace with several bracketed segments
// collapses to the first, losing the trailing field path.
func RedactNamespace(namespace string) string {
	open := strings.IndexByte(namespace, '[')
	if open < 0 {
		return namespace
	}

	// Anything past the last ']' is outside every bracket, so it is schema.
	rest := namespace[open+1:]
	tail := ""
	if end := strings.LastIndexByte(rest, ']'); end >= 0 {
		tail = rest[end+1:]
	}

	return namespace[:open] + RedactedIndex + tail
}

// The two interfaces a destination type can use to take decoding into its own
// hands, and with it the field path the decoder reports. encoding/json calls
// UnmarshalText for a JSON string whose destination implements only the second.
var (
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// FieldPathIsSchema reports whether a decoder's field path can be trusted to
// name destination schema only. The answer depends on the destination type
// alone, so it is computed once per registration, not per message or request.
func FieldPathIsSchema(t reflect.Type) bool {
	return !reachesInputPath(t, map[reflect.Type]bool{})
}

// reachesInputPath walks struct fields, pointers, slices and arrays looking for
// a type whose decode can put input text into the reported field path:
//
//   - a map, whose path segment IS the input key;
//   - an interface, which decodes into map[string]any;
//   - a json.Unmarshaler or an encoding.TextUnmarshaler, which decode into
//     whatever they like — a map into a local variable is invisible to this
//     walk, and the error they return is reported against THEIR field, so the
//     path reads "k", not "inner". The text door is not even 1.27-specific: a
//     TextUnmarshaler returning its own UnmarshalTypeError already renders
//     "inner.<input>" on Go 1.26.
//
// seen stops a self-referential type from recursing forever.
func reachesInputPath(t reflect.Type, seen map[reflect.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true

	// Both forms: json.Unmarshal takes a pointer, so a pointer-receiver
	// UnmarshalJSON is reached for an addressable value of the bare type.
	ptr := reflect.PointerTo(t)
	for _, iface := range []reflect.Type{jsonUnmarshaler, textUnmarshaler} {
		if t.Implements(iface) || ptr.Implements(iface) {
			return true
		}
	}

	switch t.Kind() {
	case reflect.Map, reflect.Interface:
		return true
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return reachesInputPath(t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			if reachesInputPath(t.Field(i).Type, seen) {
				return true
			}
		}
	default:
		// Every remaining kind is a leaf: no element or field to descend into.
	}

	return false
}

// JSONDecodeSummary renders a JSON decode failure without any payload bytes.
// json.UnmarshalTypeError.Value carries the raw literal ("number 1234.56") and
// json.SyntaxError's message quotes the offending byte, so neither error's own
// text may be rendered. Type and Offset are destination-schema facts and always
// render. Anything else — including json.Decoder.DisallowUnknownFields, which
// names the sender's key — yields "", which means the shape was not audited: the
// caller substitutes its own fail-closed phrase rather than rendering err.
//
// The error may be wrapped; errors.As is what reaches the cause.
//
// SECURITY: Field is schema-only for SOME destination types, not for all.
// Through Go 1.26 encoding/json built it from the matched destination field's
// json tag and map keys never entered its FieldStack; the json/v2 decoder
// behind Go 1.27 reports "limits.<input key>" for a map destination, dotted
// like a nested struct path, so a hostile or PII-shaped key would reach every
// sink of the error. The caller's fieldPathIsSchema gate — computed from the
// destination type via FieldPathIsSchema, never from this string — is what keeps
// the two apart. A gated-off summary still carries the wanted type and byte
// offset.
func JSONDecodeSummary(err error, fieldPathIsSchema bool) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		wantType := "unknown"
		if typeErr.Type != nil {
			wantType = typeErr.Type.String()
		}
		if fieldPathIsSchema && typeErr.Field != "" {
			return fmt.Sprintf("json: type mismatch at field %q (want %s, offset %d)", typeErr.Field, wantType, typeErr.Offset)
		}

		return fmt.Sprintf("json: type mismatch (want %s, offset %d)", wantType, typeErr.Offset)
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("json: syntax error at offset %d", syntaxErr.Offset)
	}

	return ""
}
