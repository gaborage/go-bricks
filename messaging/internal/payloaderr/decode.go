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

// Decoder turns a message body into a T. Every field is decided once at
// construction and read-only afterwards, so one Decoder is shared by every
// worker goroutine and every tenant replaying the same declarations.
type Decoder[T any] struct {
	// fieldPathIsSchema is the decode-summary gate for T, decided once here
	// because it depends on T alone. See saferender.FieldPathIsSchema.
	fieldPathIsSchema bool
}

// NewDecoder is the single construction point, so the field-path gate cannot be
// forgotten on one of a lane's entry points.
func NewDecoder[T any]() *Decoder[T] {
	return &Decoder[T]{
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
	if err := json.Unmarshal(data, dst); err != nil {
		return NewDecode(err, saferender.JSONDecodeSummary(err, d.fieldPathIsSchema))
	}

	return ValidateStruct(*dst)
}

// ValidateStruct runs the shared validator over an already-decoded value — the
// second half of Decode, exposed for a lane that decoded by other means (the
// sealed opener splices the plaintext back itself) and still owes validation.
func ValidateStruct(v any) *Body {
	if err := Validator().Struct(v); err != nil {
		return NewValidate(err)
	}

	return nil
}
