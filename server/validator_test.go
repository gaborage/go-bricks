package server

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test structs for validation
type TestStruct struct {
	Name          string `json:"name" validate:"required,min=2,max=50"`
	Email         string `json:"email" validate:"required,email"`
	Age           int    `json:"age" validate:"min=0,max=150"`
	Website       string `json:"website" validate:"omitempty,url"`
	MerchantCode  string `json:"merchant_code" validate:"required,mcc_code"`
	OptionalField string `json:"optional_field" validate:"omitempty,min=5"`
}

type NestedTestStruct struct {
	User TestStruct `json:"user" validate:"required"`
	ID   string     `json:"id" validate:"required,len=10"`
}

type EmptyStruct struct{}

func TestNewValidator(t *testing.T) {
	validator := NewValidator()

	require.NotNil(t, validator)
	require.NotNil(t, validator.validate)
}

func TestValidatorGetValidator(t *testing.T) {
	v := NewValidator()
	require.NotNil(t, v)
	require.Same(t, v.validate, v.GetValidator())
}

func TestValidatorValidateSuccess(t *testing.T) {
	validator := NewValidator()
	require.NotNil(t, validator)

	tests := []struct {
		name  string
		input interface{}
	}{
		{
			name: "valid_test_struct",
			input: TestStruct{
				Name:          "John Doe",
				Email:         "john@example.com",
				Age:           25,
				Website:       "https://example.com",
				MerchantCode:  "5411",
				OptionalField: "optional value",
			},
		},
		{
			name: "valid_test_struct_with_optional_empty",
			input: TestStruct{
				Name:          "Jane Smith",
				Email:         "jane@test.com",
				Age:           30,
				Website:       "",
				MerchantCode:  "1234",
				OptionalField: "",
			},
		},
		{
			name: "valid_nested_struct",
			input: NestedTestStruct{
				User: TestStruct{
					Name:         "Nested User",
					Email:        "nested@example.com",
					Age:          40,
					MerchantCode: "9999",
				},
				ID: "1234567890",
			},
		},
		{
			name:  "empty_struct",
			input: EmptyStruct{},
		},
		{
			name: "minimal_valid_struct",
			input: TestStruct{
				Name:         "Al",
				Email:        "a@b.co",
				Age:          0,
				MerchantCode: "0000",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.input)
			assert.NoError(t, err)
		})
	}
}

func TestValidatorValidateFailures(t *testing.T) {
	validator := NewValidator()
	require.NotNil(t, validator)

	tests := []struct {
		name           string
		input          interface{}
		expectedErrors int
		expectedFields []string
	}{
		{
			name: "missing_required_fields",
			input: TestStruct{
				// Missing Name, Email, MerchantCode
				Age: 25,
			},
			expectedErrors: 3,
			expectedFields: []string{"Name", "Email", "MerchantCode"},
		},
		{
			name: "invalid_email",
			input: TestStruct{
				Name:         "John Doe",
				Email:        "invalid-email",
				Age:          25,
				MerchantCode: "5411",
			},
			expectedErrors: 1,
			expectedFields: []string{"Email"},
		},
		{
			name: "name_too_short",
			input: TestStruct{
				Name:         "A",
				Email:        "john@example.com",
				Age:          25,
				MerchantCode: "5411",
			},
			expectedErrors: 1,
			expectedFields: []string{"Name"},
		},
		{
			name: "name_too_long",
			input: TestStruct{
				Name:         "This is a very long name that exceeds the maximum allowed length of fifty characters",
				Email:        "john@example.com",
				Age:          25,
				MerchantCode: "5411",
			},
			expectedErrors: 1,
			expectedFields: []string{"Name"},
		},
		{
			name: "age_negative",
			input: TestStruct{
				Name:         "John Doe",
				Email:        "john@example.com",
				Age:          -5,
				MerchantCode: "5411",
			},
			expectedErrors: 1,
			expectedFields: []string{"Age"},
		},
		{
			name: "age_too_high",
			input: TestStruct{
				Name:         "John Doe",
				Email:        "john@example.com",
				Age:          200,
				MerchantCode: "5411",
			},
			expectedErrors: 1,
			expectedFields: []string{"Age"},
		},
		{
			name: "invalid_url",
			input: TestStruct{
				Name:         "John Doe",
				Email:        "john@example.com",
				Age:          25,
				Website:      "not-a-url",
				MerchantCode: "5411",
			},
			expectedErrors: 1,
			expectedFields: []string{"Website"},
		},
		{
			name: "invalid_mcc_code_non_numeric",
			input: TestStruct{
				Name:         "John Doe",
				Email:        "john@example.com",
				Age:          25,
				MerchantCode: "abcd",
			},
			expectedErrors: 1,
			expectedFields: []string{"MerchantCode"},
		},
		{
			name: "invalid_mcc_code_wrong_length",
			input: TestStruct{
				Name:         "John Doe",
				Email:        "john@example.com",
				Age:          25,
				MerchantCode: "123",
			},
			expectedErrors: 1,
			expectedFields: []string{"MerchantCode"},
		},
		{
			name: "optional_field_too_short",
			input: TestStruct{
				Name:          "John Doe",
				Email:         "john@example.com",
				Age:           25,
				MerchantCode:  "5411",
				OptionalField: "abc",
			},
			expectedErrors: 1,
			expectedFields: []string{"OptionalField"},
		},
		{
			name: "multiple_validation_errors",
			input: TestStruct{
				Name:         "A",
				Email:        "invalid-email",
				Age:          -10,
				Website:      "not-a-url",
				MerchantCode: "abc",
			},
			expectedErrors: 5,
			expectedFields: []string{"Name", "Email", "Age", "Website", "MerchantCode"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.input)
			require.Error(t, err)

			var validationErr *ValidationError
			require.ErrorAs(t, err, &validationErr)

			assert.Len(t, validationErr.Errors, tt.expectedErrors)

			// Check that all expected fields have errors
			actualFields := make(map[string]bool)
			for _, fieldErr := range validationErr.Errors {
				actualFields[fieldErr.Field] = true
			}

			for _, expectedField := range tt.expectedFields {
				assert.True(t, actualFields[expectedField],
					"Expected field %s to have validation error", expectedField)
			}
		})
	}
}

func TestValidatorValidateNonStruct(t *testing.T) {
	validator := NewValidator()
	require.NotNil(t, validator)

	tests := []struct {
		name  string
		input interface{}
	}{
		{
			name:  "string",
			input: "test string",
		},
		{
			name:  "int",
			input: 42,
		},
		{
			name:  "slice",
			input: []string{"test", "slice"},
		},
		{
			name:  "map",
			input: map[string]string{"key": "value"},
		},
		{
			name:  "nil",
			input: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.input)
			// Non-struct types should return an error (not ValidationError)
			require.Error(t, err)

			var validationErr *ValidationError
			assert.False(t, errors.As(err, &validationErr),
				"Non-struct validation should not return ValidationError")
		})
	}
}

func TestValidationErrorError(t *testing.T) {
	tests := []struct {
		name     string
		errors   []FieldError
		expected string
	}{
		{
			name:     "no_errors",
			errors:   []FieldError{},
			expected: "validation failed",
		},
		{
			name: "single_error",
			errors: []FieldError{
				{Field: "Name", Message: "Name is required", Value: ""},
			},
			expected: "validation failed: Name is required",
		},
		{
			name: "multiple_errors",
			errors: []FieldError{
				{Field: "Name", Message: "Name is required", Value: ""},
				{Field: "Email", Message: "Email must be valid", Value: "invalid"},
			},
			expected: "validation failed: 2 errors",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ve := &ValidationError{Errors: tt.errors}
			assert.Equal(t, tt.expected, ve.Error())
		})
	}
}

func TestValidationErrorJSON(t *testing.T) {
	ve := &ValidationError{
		Errors: []FieldError{
			{Field: "Name", Message: "Name is required", Value: ""},
			{Field: "Email", Message: "Email must be valid", Value: "invalid-email"},
		},
	}

	jsonData, err := json.Marshal(ve)
	require.NoError(t, err)

	var result ValidationError
	err = json.Unmarshal(jsonData, &result)
	require.NoError(t, err)

	assert.Len(t, result.Errors, 2)
	assert.Equal(t, "Name", result.Errors[0].Field)
	assert.Equal(t, "Name is required", result.Errors[0].Message)
	assert.Equal(t, "", result.Errors[0].Value)
	assert.Equal(t, "Email", result.Errors[1].Field)
	assert.Equal(t, "Email must be valid", result.Errors[1].Message)
	assert.Equal(t, "invalid-email", result.Errors[1].Value)
}

func TestValidateMCCCode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid_mcc_code",
			input:    "5411",
			expected: true,
		},
		{
			name:     "valid_mcc_code_with_zeros",
			input:    "0000",
			expected: true,
		},
		{
			name:     "valid_mcc_code_numeric",
			input:    "9999",
			expected: true,
		},
		{
			name:     "invalid_mcc_code_too_short",
			input:    "123",
			expected: false,
		},
		{
			name:     "invalid_mcc_code_too_long",
			input:    "12345",
			expected: false,
		},
		{
			name:     "invalid_mcc_code_with_letters",
			input:    "abcd",
			expected: false,
		},
		{
			name:     "invalid_mcc_code_mixed",
			input:    "12ab",
			expected: false,
		},
		{
			name:     "invalid_mcc_code_empty",
			input:    "",
			expected: false,
		},
		{
			name:     "invalid_mcc_code_spaces",
			input:    "12 3",
			expected: false,
		},
		{
			name:     "invalid_mcc_code_special_chars",
			input:    "12@3",
			expected: false,
		},
	}

	// Create a test validator to access the custom validation function
	validator := NewValidator()
	require.NotNil(t, validator)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testStruct := struct {
				MCCCode string `validate:"mcc_code"`
			}{
				MCCCode: tt.input,
			}

			err := validator.Validate(testStruct)

			if tt.expected {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				var validationErr *ValidationError
				require.ErrorAs(t, err, &validationErr)
				assert.Len(t, validationErr.Errors, 1)
				assert.Equal(t, "MCCCode", validationErr.Errors[0].Field)
				assert.Contains(t, validationErr.Errors[0].Message, "valid 4-digit MCC code")
			}
		})
	}
}

func TestGetErrorMessage(t *testing.T) {
	validator := NewValidator()
	require.NotNil(t, validator)

	tests := []struct {
		name          string
		validationTag string
		field         string
		input         interface{}
		expectedMsg   string
	}{
		{
			name:          "required_error",
			validationTag: "required",
			field:         "Name",
			input: struct {
				Name string `validate:"required"`
			}{},
			expectedMsg: "Name is required",
		},
		{
			name:          "min_error",
			validationTag: "min",
			field:         "Name",
			input: struct {
				Name string `validate:"min=5"`
			}{Name: "Hi"},
			expectedMsg: "Name must be at least 5 characters",
		},
		{
			name:          "max_error",
			validationTag: "max",
			field:         "Name",
			input: struct {
				Name string `validate:"max=3"`
			}{Name: "TooLong"},
			expectedMsg: "Name must be at most 3 characters",
		},
		{
			name:          "len_error",
			validationTag: "len",
			field:         "Code",
			input: struct {
				Code string `validate:"len=4"`
			}{Code: "123"},
			expectedMsg: "Code must be exactly 4 characters",
		},
		{
			name:          "url_error",
			validationTag: "url",
			field:         "Website",
			input: struct {
				Website string `validate:"url"`
			}{Website: "not-url"},
			expectedMsg: "Website must be a valid URL",
		},
		{
			name:          "mcc_code_error",
			validationTag: "mcc_code",
			field:         "MerchantCode",
			input: struct {
				MerchantCode string `validate:"mcc_code"`
			}{MerchantCode: "abc"},
			expectedMsg: "MerchantCode must be a valid 4-digit MCC code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.input)
			require.Error(t, err)

			var validationErr *ValidationError
			require.ErrorAs(t, err, &validationErr)
			require.Len(t, validationErr.Errors, 1)

			assert.Equal(t, tt.field, validationErr.Errors[0].Field)
			assert.Equal(t, tt.expectedMsg, validationErr.Errors[0].Message)
		})
	}
}

func TestGetErrorMessageUnknownTag(t *testing.T) {
	validator := NewValidator()
	require.NotNil(t, validator)

	// Use a built-in validator tag that's not handled in getErrorMessage
	testStruct := struct {
		Number int `validate:"numeric"`
	}{
		Number: 0, // This should pass numeric validation
	}

	_ = validator.Validate(testStruct)
	// This should pass, so let's create a case that will fail

	testStructFail := struct {
		Text string `validate:"numeric"`
	}{
		Text: "abc", // This should fail numeric validation
	}

	err := validator.Validate(testStructFail)
	require.Error(t, err)

	var validationErr *ValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Len(t, validationErr.Errors, 1)

	assert.Equal(t, "Text", validationErr.Errors[0].Field)
	assert.Equal(t, "Text failed validation", validationErr.Errors[0].Message)
}

func TestNewValidationError(t *testing.T) {
	validator := NewValidator()
	require.NotNil(t, validator)

	// Create a struct that will fail validation
	invalidStruct := TestStruct{
		Name:         "",
		Email:        "invalid",
		MerchantCode: "abc",
	}

	err := validator.Validate(invalidStruct)
	require.Error(t, err)

	var validationErr *ValidationError
	require.ErrorAs(t, err, &validationErr)

	// Should have errors for Name (required), Email (email), and MerchantCode (mcc_code)
	assert.Len(t, validationErr.Errors, 3)

	// Check that field names and values are correctly set
	fieldErrors := make(map[string]FieldError)
	for _, fieldErr := range validationErr.Errors {
		fieldErrors[fieldErr.Field] = fieldErr
	}

	assert.Contains(t, fieldErrors, "Name")
	assert.Contains(t, fieldErrors, "Email")
	assert.Contains(t, fieldErrors, "MerchantCode")

	assert.Equal(t, "", fieldErrors["Name"].Value)
	assert.Equal(t, "invalid", fieldErrors["Email"].Value)
	assert.Equal(t, "abc", fieldErrors["MerchantCode"].Value)
}

func TestValidatorEdgeCases(t *testing.T) {
	validator := NewValidator()
	require.NotNil(t, validator)

	t.Run("struct_with_pointer_fields", func(t *testing.T) {
		type PointerStruct struct {
			Name *string `validate:"required"`
		}

		// Nil pointer should fail required validation
		err := validator.Validate(PointerStruct{Name: nil})
		assert.Error(t, err)

		// Valid pointer should pass
		name := "Test Name"
		err = validator.Validate(PointerStruct{Name: &name})
		assert.NoError(t, err)
	})

	t.Run("struct_with_nested_validation", func(t *testing.T) {
		invalidNested := NestedTestStruct{
			User: TestStruct{
				Name:         "",
				Email:        "invalid",
				MerchantCode: "abc",
			},
			ID: "short",
		}

		err := validator.Validate(invalidNested)
		require.Error(t, err)

		var validationErr *ValidationError
		require.ErrorAs(t, err, &validationErr)

		// Should have multiple errors from nested struct validation
		assert.Greater(t, len(validationErr.Errors), 1)
	})

	t.Run("struct_with_slice_field", func(t *testing.T) {
		type SliceStruct struct {
			Items []string `validate:"required,min=1"`
		}

		// Empty slice should fail
		err := validator.Validate(SliceStruct{Items: []string{}})
		assert.Error(t, err)

		// Valid slice should pass
		err = validator.Validate(SliceStruct{Items: []string{"item1", "item2"}})
		assert.NoError(t, err)
	})
}

func TestValidatorNilValidation(t *testing.T) {
	// Test what happens if validator creation fails (hypothetical scenario)
	// Since we can't easily mock the RegisterValidation failure,
	// we'll just test the current behavior
	validator := NewValidator()
	require.NotNil(t, validator)

	// Test behavior with a struct containing all validation rule types
	allRulesStruct := struct {
		Required    string `validate:"required"`
		MinLength   string `validate:"min=3"`
		MaxLength   string `validate:"max=10"`
		ExactLength string `validate:"len=5"`
		Email       string `validate:"email"`
		URL         string `validate:"url"`
		MCCCode     string `validate:"mcc_code"`
	}{
		Required:    "test",
		MinLength:   "abc",
		MaxLength:   "short",
		ExactLength: "exact",
		Email:       "test@example.com",
		URL:         "https://example.com",
		MCCCode:     "1234",
	}

	err := validator.Validate(allRulesStruct)
	assert.NoError(t, err)
}
