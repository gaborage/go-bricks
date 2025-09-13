// Package server provides request validation functionality.
// It wraps go-playground/validator with custom validation logic and error formatting.
package server

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/go-playground/validator/v10"
)

// Validator wraps go-playground/validator with custom validation logic.
// It provides request validation functionality with custom validators.
type Validator struct {
	validate *validator.Validate
}

// NewValidator creates a new Validator instance with custom validation rules registered.
func NewValidator() *Validator {
	v := validator.New()

	// Register custom validators
	err := v.RegisterValidation("mcc_code", validateMCCCode)
	if err != nil {
		return nil
	}

	return &Validator{validate: v}
}

// GetValidator returns the underlying validator instance.
func (v *Validator) GetValidator() *validator.Validate {
	return v.validate
}

// Validate performs validation on the provided struct and returns any validation errors.
func (v *Validator) Validate(i interface{}) error {
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
// It includes the field name, error message, and the invalid value.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Value   string `json:"value,omitempty"`
}

// NewValidationError creates a ValidationError from go-playground/validator errors.
// It converts the errors into a more user-friendly format with descriptive messages.
func NewValidationError(errs validator.ValidationErrors) *ValidationError {
	fieldErrors := make([]FieldError, 0, len(errs))

	for _, err := range errs {
		fieldErrors = append(fieldErrors, FieldError{
			Field:   err.Field(),
			Message: getErrorMessage(err),
			Value:   fmt.Sprintf("%v", err.Value()),
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

func getErrorMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return fmt.Sprintf("%s is required", fe.Field())
	case "min":
		return fmt.Sprintf("%s must be at least %s characters", fe.Field(), fe.Param())
	case "max":
		return fmt.Sprintf("%s must be at most %s characters", fe.Field(), fe.Param())
	case "len":
		return fmt.Sprintf("%s must be exactly %s characters", fe.Field(), fe.Param())
	case "url":
		return fmt.Sprintf("%s must be a valid URL", fe.Field())
	case "mcc_code":
		return fmt.Sprintf("%s must be a valid 4-digit MCC code", fe.Field())
	default:
		return fmt.Sprintf("%s failed validation", fe.Field())
	}
}

// Custom validator for MCC codes - demo of a specific business rule
func validateMCCCode(fl validator.FieldLevel) bool {
	mccCode := fl.Field().String()
	if len(mccCode) != 4 {
		return false
	}

	// Check if all characters are digits
	matched, _ := regexp.MatchString(`^\d{4}$`, mccCode)
	return matched
}
