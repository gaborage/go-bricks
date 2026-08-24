// Formats go-playground/validator errors into the framework's structured
// field-error shape. The validator instance itself, with the framework's custom
// rules already registered, is constructed in internal/validation.
package server

import (
	"errors"
	"fmt"

	"github.com/go-playground/validator/v10"

	"github.com/gaborage/go-bricks/internal/saferender"
	"github.com/gaborage/go-bricks/internal/validation"
)

// unauditedValidationSummary is the fail-closed rendering for a validation
// failure that is not a validator.ValidationErrors — it has no field list, and
// its text is not audited for request content.
const unauditedValidationSummary = "cause withheld (unaudited validation failure)"

// Validator wraps go-playground/validator with custom validation logic.
// It provides request validation functionality with custom validators.
type Validator struct {
	validate *validator.Validate
}

// NewValidator creates a new Validator instance with custom validation rules
// registered. It never returns nil: the shared constructor in internal/validation
// panics if a rule fails to register rather than yielding a partly-configured
// instance.
func NewValidator() *Validator {
	return &Validator{validate: validation.New()}
}

// Validator returns the underlying validator instance.
func (v *Validator) Validator() *validator.Validate {
	return v.validate
}

// Validate performs validation on the provided struct and returns any validation errors.
func (v *Validator) Validate(i any) error {
	if err := v.validate.Struct(i); err != nil {
		// Handle validation errors (field-specific errors)
		var validationErrors validator.ValidationErrors
		if errors.As(err, &validationErrors) {
			return NewValidationError(validationErrors)
		}
		// Handle invalid validation errors (non-struct inputs, etc.)
		return err
	}
	return nil
}

// ValidationError wraps validation errors with better messages and structured field errors.
// It provides a standardized format for validation error responses.
type ValidationError struct {
	Errors []FieldError `json:"errors"`
}

// FieldError represents a validation error for a specific field.
//
// SECURITY: it carries schema facts only. The rejected value is deliberately
// absent — FieldError reaches the 400 response body, which no log filter sees,
// and the value is request input for ANY failed tag. Field and Message name a
// dive-validated map's element through a redacted span (Limits[*]), because
// validator interpolates the input map key into the namespace verbatim.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error implements the error interface.
func (e FieldError) Error() string {
	return e.Message
}

// NewValidationError creates a ValidationError from go-playground/validator errors.
// It converts the errors into a more user-friendly format with descriptive messages.
func NewValidationError(errs validator.ValidationErrors) *ValidationError {
	fieldErrors := make([]FieldError, 0, len(errs))

	for _, err := range errs {
		field := saferender.RedactNamespace(err.Field())
		fieldErrors = append(fieldErrors, FieldError{
			Field:   field,
			Message: getErrorMessage(err, field),
		})
	}

	return &ValidationError{Errors: fieldErrors}
}

func (ve *ValidationError) Error() string {
	if len(ve.Errors) == 0 {
		return "validation failed"
	}

	if len(ve.Errors) == 1 {
		return fmt.Sprintf("validation failed: %s", ve.Errors[0].Message)
	}

	return fmt.Sprintf("validation failed: %d errors", len(ve.Errors))
}

// getErrorMessage renders the human-readable half of a FieldError. field is the
// ALREADY-redacted name: fe.Field() carries the input map key for a dived map,
// so reading it again here would reopen the leak the caller just closed.
func getErrorMessage(fe validator.FieldError, field string) string {
	switch fe.Tag() {
	case "required":
		return fmt.Sprintf("%s is required", field)
	case "min":
		return fmt.Sprintf("%s must be at least %s characters", field, fe.Param())
	case "max":
		return fmt.Sprintf("%s must be at most %s characters", field, fe.Param())
	case "len":
		return fmt.Sprintf("%s must be exactly %s characters", field, fe.Param())
	case "url":
		return fmt.Sprintf("%s must be a valid URL", field)
	// mcc_code is registered in internal/validation.New, not here.
	case "mcc_code":
		return fmt.Sprintf("%s must be a valid 4-digit MCC code", field)
	default:
		return fmt.Sprintf("%s failed validation", field)
	}
}
