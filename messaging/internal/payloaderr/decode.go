package payloaderr

import (
	"encoding/json"
	"reflect"
	"sync"

	"github.com/gaborage/go-bricks/internal/saferender"
	"github.com/gaborage/go-bricks/internal/validation"
)

// Validator is the one validator instance every typed handler on either lane
// shares. validator caches struct metadata by reflect.Type, so per-message
// construction would throw that cache away on every delivery; the instance is
// safe for concurrent use, which is what lets one adapter serve every worker.
var Validator = sync.OnceValue(validation.New)

// Codec decodes a raw payload into a destination. It is a seam rather than a
// hardcoded call because schema negotiation and non-JSON payloads (issue #346)
// widen it without an API break on either lane.
type Codec interface {
	Unmarshal(data []byte, v any) error
	// Summarize renders a decode failure with NO payload bytes in it. Returning
	// "" means the shape was not audited; NewDecode substitutes the fail-closed
	// phrase, so a codec never spells it out.
	//
	// fieldPathIsSchema tells the codec whether a field path the decoder reports
	// can be trusted as schema-only. The caller decides it from the destination
	// type, once per registration; a codec must never infer it from the error.
	Summarize(err error, fieldPathIsSchema bool) string
}

// JSONCodec is the only codec today: message bodies are JSON on every path the
// framework publishes, on both lanes.
type JSONCodec struct{}

func (JSONCodec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// Summarize delegates to the shared safe-rendering seam; see
// saferender.JSONDecodeSummary for the rules and their rationale.
func (JSONCodec) Summarize(err error, fieldPathIsSchema bool) string {
	return saferender.JSONDecodeSummary(err, fieldPathIsSchema)
}

// Decoder turns a message body into a T. Every field is decided once at
// construction and read-only afterwards, so one Decoder is shared by every
// worker goroutine and every tenant replaying the same declarations.
type Decoder[T any] struct {
	codec Codec
	// fieldPathIsSchema is the decode-summary gate for T, decided once here
	// because it depends on T alone. See saferender.FieldPathIsSchema.
	fieldPathIsSchema bool
}

// NewDecoder is the single construction point, so the field-path gate cannot be
// forgotten on one of a lane's entry points. A nil codec is JSONCodec.
func NewDecoder[T any](codec Codec) *Decoder[T] {
	if codec == nil {
		codec = JSONCodec{}
	}

	return &Decoder[T]{
		codec:             codec,
		fieldPathIsSchema: saferender.FieldPathIsSchema(reflect.TypeFor[T]()),
	}
}

// Decode fills dst from data and validates it, returning nil on success and the
// failure's Body otherwise. dst is written only on a successful decode; a
// caller that reuses it across messages would still see a partial value, so
// every lane passes a fresh one per delivery.
//
// A non-struct T reaches validation with a *validator.InvalidValidationError,
// which yields no fields and still carries StageValidate — failing closed on the
// first delivery rather than silently skipping validation forever.
func (d *Decoder[T]) Decode(data []byte, dst *T) *Body {
	if err := d.codec.Unmarshal(data, dst); err != nil {
		return NewDecode(err, d.codec.Summarize(err, d.fieldPathIsSchema))
	}

	if err := Validator().Struct(*dst); err != nil {
		return NewValidate(err)
	}

	return nil
}
