package logger

import (
	"slices"
	"testing"
)

const (
	testUsername = "test_user_john"
	testPassword = "test_password_123"
	testUserDoe  = "test_user_john_doe"
)

func TestDefaultFilterConfig(t *testing.T) {
	config := DefaultFilterConfig()

	if config == nil {
		t.Fatal("DefaultFilterConfig should not return nil")
	}

	if config.MaskValue != DefaultMaskValue {
		t.Errorf("Expected default mask value '***', got '%s'", config.MaskValue)
	}

	// Test that common sensitive fields are included
	expectedFields := []string{"password", "secret", "token", "api_key"}
	for _, expected := range expectedFields {
		if !slices.Contains(config.SensitiveFields, expected) {
			t.Errorf("Expected field '%s' to be in default sensitive fields", expected)
		}
	}
}

func TestNewSensitiveDataFilter(t *testing.T) {
	// Test nil config uses default
	filter := NewSensitiveDataFilter(nil)
	if filter == nil {
		t.Fatal("NewSensitiveDataFilter should not return nil")
	}
	if filter.config.MaskValue != DefaultMaskValue {
		t.Errorf("Expected default mask value '***', got '%s'", filter.config.MaskValue)
	}

	// Test custom config
	customConfig := &FilterConfig{
		SensitiveFields: []string{"custom_field"},
		MaskValue:       "[REDACTED]",
	}
	customFilter := NewSensitiveDataFilter(customConfig)
	if customFilter.config.MaskValue != "[REDACTED]" {
		t.Errorf("Expected custom mask value '[REDACTED]', got '%s'", customFilter.config.MaskValue)
	}
}

func TestFilterString(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password", "secret", "broker_url"},
		MaskValue:       DefaultMaskValue,
	})

	// Test sensitive field masking (complete masking for security)
	result := filter.FilterString("password", "mysecret")
	if result != DefaultMaskValue {
		t.Errorf("Expected '***', got '%s'", result)
	}

	// Test non-sensitive field
	result = filter.FilterString("username", testUserDoe)
	if result != testUserDoe {
		t.Errorf("Expected '%s', got '%s'", testUserDoe, result)
	}

	// Test URL masking (clean masking without URL encoding)
	result = filter.FilterString("broker_url", "amqp://user:pass@host/vhost")
	if result != "amqp://user:***@host/vhost" {
		t.Errorf("Expected clean masked URL, got '%s'", result)
	}
}

func TestFilterValue(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password", "secret"},
		MaskValue:       DefaultMaskValue,
	})

	// Test sensitive value masking
	result := filter.FilterValue("password", "secret123")
	if result != DefaultMaskValue {
		t.Errorf("Expected '***', got '%v'", result)
	}

	// Test non-sensitive value
	result = filter.FilterValue("username", testUsername)
	if result != testUsername {
		t.Errorf("Expected '%s', got '%v'", testUsername, result)
	}

	// Test map filtering
	input := map[string]any{
		"username": testUsername,
		"password": testPassword,
		"email":    "john@example.com",
	}
	result = filter.FilterValue("user_data", input)
	resultMap := result.(map[string]any)

	if resultMap["username"] != testUsername {
		t.Errorf("Expected username to remain '%s', got '%v'", testUsername, resultMap["username"])
	}
	if resultMap["password"] != DefaultMaskValue {
		t.Errorf("Expected password to be masked, got '%v'", resultMap["password"])
	}
}

func TestFilterFields(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password", "api_key"},
		MaskValue:       DefaultMaskValue,
	})

	input := map[string]any{
		"username": testUserDoe,
		"password": testPassword,
		"api_key":  "test_api_1234567890",
		"email":    "john@example.com",
	}

	result := filter.FilterFields(input)

	if result["username"] != testUserDoe {
		t.Errorf("Expected username to remain unchanged")
	}
	if result["password"] != DefaultMaskValue {
		t.Errorf("Expected password to be masked")
	}
	if result["api_key"] != DefaultMaskValue {
		t.Errorf("Expected api_key to be masked")
	}
	if result["email"] != "john@example.com" {
		t.Errorf("Expected email to remain unchanged")
	}
}

func TestMaskURL(t *testing.T) {
	filter := NewSensitiveDataFilter(nil)

	// Test AMQP URL with password (clean masking)
	result := filter.maskURL("amqp://user:secret@rabbitmq.example.com/vhost")
	expected := "amqp://user:" + DefaultMaskValue + "@rabbitmq.example.com/vhost"
	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}

	// Test URL without password
	result = filter.maskURL("https://api.example.com/v1/users")
	if result != "https://api.example.com/v1/users" {
		t.Errorf("Expected URL without password to remain unchanged")
	}

	// Test simple string (not a URL) - should pass through unchanged since no password to mask
	result = filter.maskURL("not-a-valid-url")
	if result != "not-a-valid-url" {
		t.Errorf("Expected simple string to pass through unchanged, got '%s'", result)
	}
}

// =============================================================================
// Enhanced Sensitive Data Filtering Tests
// =============================================================================

func TestSensitiveDataFilterRunHook(_ *testing.T) {
	filter := NewSensitiveDataFilter(nil)

	// Test that Run method can be called without panic
	// This is primarily a placeholder method but should work safely
	filter.Run(nil, 0, "test message")

	// The method should not panic or error, it's a no-op placeholder
}

func TestFilterValueStructFiltering(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password", "secret", "apiKey"},
		MaskValue:       DefaultMaskValue,
	})

	// Test struct with sensitive fields
	type TestStruct struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Email    string `json:"email"`
		APIKey   string `json:"apiKey"`
	}

	input := TestStruct{
		Username: "test_user_john_doe",
		Password: "test_secret123",
		Email:    "john@example.com",
		APIKey:   "test_api_123456",
	}

	result := filter.FilterValue("user", input)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal("Expected result to be a map[string]any")
	}

	// Check that non-sensitive fields are preserved
	if resultMap["username"] != "test_user_john_doe" {
		t.Errorf("Expected username to remain 'test_user_john_doe', got '%v'", resultMap["username"])
	}
	if resultMap["email"] != "john@example.com" {
		t.Errorf("Expected email to remain unchanged, got '%v'", resultMap["email"])
	}

	// Check that sensitive fields are masked
	if resultMap["password"] != DefaultMaskValue {
		t.Errorf("Expected password to be masked, got '%v'", resultMap["password"])
	}
	if resultMap["apiKey"] != DefaultMaskValue {
		t.Errorf("Expected apiKey to be masked, got '%v'", resultMap["apiKey"])
	}
}

func TestFilterValuePointerStruct(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password"},
		MaskValue:       DefaultMaskValue,
	})

	type TestStruct struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	// Test with pointer to struct
	input := &TestStruct{
		Username: testUsername,
		Password: "test_secret",
	}

	// Based on the implementation, pointers don't get converted to struct filtering
	// since FilterValue checks reflect.TypeOf(value).Kind() == reflect.Struct,
	// but pointers have Kind() == reflect.Ptr
	result := filter.FilterValue("user", input)

	// The pointer should pass through unchanged since it's not a struct
	if result != input {
		t.Errorf("Expected pointer to struct to pass through unchanged, got different value")
	}
}

func TestFilterValueNilPointer(t *testing.T) {
	filter := NewSensitiveDataFilter(nil)

	type TestStruct struct {
		Username string
	}

	var input *TestStruct

	result := filter.FilterValue("user", input)
	// The filterStruct method returns the original value for nil pointers
	if result != input {
		t.Errorf("Expected nil pointer to be returned as-is, got '%v'", result)
	}
}

func TestFilterValueUnexportedFields(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password"},
		MaskValue:       DefaultMaskValue,
	})

	type TestStruct struct {
		Username string // exported
		password string // unexported
	}

	input := TestStruct{
		Username: testUsername,
		password: "secret", // This should be ignored since it's unexported
	}

	result := filter.FilterValue("user", input)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal("Expected result to be a map[string]any")
	}

	// Only exported fields should be in the result
	if resultMap["Username"] != testUsername {
		t.Errorf("Expected Username to be '%s', got '%v'", testUsername, resultMap["Username"])
	}

	// Unexported field should not be in the result
	if _, exists := resultMap["password"]; exists {
		t.Error("Unexported field 'password' should not be in the filtered result")
	}
}

func TestFilterValueJSONTags(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"secret_key"},
		MaskValue:       DefaultMaskValue,
	})

	type TestStruct struct {
		PublicField  string `json:"public_field"`
		SecretField  string `json:"secret_key"`
		IgnoredField string `json:"-"`
		CommaField   string `json:"comma_field,omitempty"`
	}

	input := TestStruct{
		PublicField:  "public",
		SecretField:  "private",
		IgnoredField: "ignored",
		CommaField:   "comma",
	}

	result := filter.FilterValue("data", input)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal("Expected result to be a map[string]any")
	}

	// Check JSON tag names are used
	if resultMap["public_field"] != "public" {
		t.Errorf("Expected public_field to be 'public', got '%v'", resultMap["public_field"])
	}
	if resultMap["secret_key"] != DefaultMaskValue {
		t.Errorf("Expected secret_key to be masked, got '%v'", resultMap["secret_key"])
	}
	if resultMap["comma_field"] != "comma" {
		t.Errorf("Expected comma_field to be 'comma', got '%v'", resultMap["comma_field"])
	}

	if resultMap["IgnoredField"] != "ignored" {
		t.Error("Field with json:\"-\" uses field name, should be 'ignored'")
	}
	if _, exists := resultMap["-"]; exists {
		t.Error("Field with json:\"-\" should not use '-' as key")
	}
}

func TestFilterValueNonStructType(t *testing.T) {
	filter := NewSensitiveDataFilter(nil)

	// Test with simple non-struct types that should pass through unchanged
	testCases := []struct {
		name  string
		input any
	}{
		{"integer", 42},
		{"float", 3.14},
		{"boolean", true},
		{"string", "hello"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := filter.FilterValue("field", tc.input)
			if result != tc.input {
				t.Errorf("Expected non-struct type to pass through unchanged, input: %v, got: %v", tc.input, result)
			}
		})
	}

	// Test slice and map separately due to comparison limitations
	slice := []string{"one", "two"}
	sliceResult := filter.FilterValue("field", slice)
	sliceResultTyped, ok := sliceResult.([]string)
	if !ok || len(sliceResultTyped) != 2 || sliceResultTyped[0] != "one" || sliceResultTyped[1] != "two" {
		t.Errorf("Expected slice to pass through unchanged")
	}

	stringMap := map[string]string{"key": "value"}
	mapResult := filter.FilterValue("field", stringMap)
	mapResultTyped, ok := mapResult.(map[string]string)
	if !ok || mapResultTyped["key"] != "value" {
		t.Errorf("Expected string map to pass through unchanged")
	}
}

func TestMaskURLErrorHandling(t *testing.T) {
	filter := NewSensitiveDataFilter(nil)

	// Test with malformed URL that causes parsing error
	malformedURL := "ht!@#$%tp://invalid-url-format"
	result := filter.maskURL(malformedURL)

	// Should fallback to generic masking when URL parsing fails
	expected := DefaultMaskValue
	if result != expected {
		t.Errorf("Expected fallback to generic masking for malformed URL, got '%s'", result)
	}
}

func TestMaskURLNoUserInfo(t *testing.T) {
	filter := NewSensitiveDataFilter(nil)

	// Test URLs without user info
	testCases := []string{
		"https://example.com/path",
		"http://localhost:8080",
		"amqp://host/vhost",
		"amqps://secure.example.com",
	}

	for _, url := range testCases {
		result := filter.maskURL(url)
		if result != url {
			t.Errorf("Expected URL without user info to remain unchanged: %s, got: %s", url, result)
		}
	}
}

func TestMaskURLUserWithoutPassword(t *testing.T) {
	filter := NewSensitiveDataFilter(nil)

	// Test URL with username but no password
	url := "amqp://username@host/vhost"
	result := filter.maskURL(url)

	// Should remain unchanged since there's no password to mask
	if result != url {
		t.Errorf("Expected URL with username but no password to remain unchanged: %s, got: %s", url, result)
	}
}

func TestIsSensitiveFieldCaseInsensitive(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"Password", "API_KEY"},
		MaskValue:       DefaultMaskValue,
	})

	testCases := []struct {
		fieldName string
		expected  bool
	}{
		{"password", true},
		{"PASSWORD", true},
		{"Password", true},
		{"user_password", true},
		{"mypassword", true},
		{"api_key", true},
		{"API_KEY", true},
		{"MY_API_KEY", true},
		{"username", false},
		{"email", false},
		{"name", false},
	}

	for _, tc := range testCases {
		result := filter.isSensitiveField(tc.fieldName)
		if result != tc.expected {
			t.Errorf("For field '%s', expected %v, got %v", tc.fieldName, tc.expected, result)
		}
	}
}

func TestFilterConfigEmptyMaskValue(t *testing.T) {
	// Test that empty MaskValue gets defaulted
	config := &FilterConfig{
		SensitiveFields: []string{"password"},
		MaskValue:       "",
	}

	filter := NewSensitiveDataFilter(config)
	if filter.config.MaskValue != "***" {
		t.Errorf("Expected empty MaskValue to be defaulted to '***', got '%s'", filter.config.MaskValue)
	}
}

func TestFilterStringEmptyValue(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password"},
		MaskValue:       DefaultMaskValue,
	})

	// Test empty string handling
	result := filter.FilterString("password", "")
	if result != "" {
		t.Errorf("Expected empty sensitive string to remain empty, got '%s'", result)
	}
}

func TestFilterValueNestedMaps(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password", "secret"},
		MaskValue:       DefaultMaskValue,
	})

	// Test deeply nested maps
	input := map[string]any{
		"user": map[string]any{
			"name":     testUsername,
			"password": testPassword,
			"config": map[string]any{
				"theme":  "dark",
				"secret": "api_secret",
			},
		},
		"public_info": "visible",
	}

	result := filter.FilterValue("data", input)
	resultMap := result.(map[string]any)

	// Check top level
	if resultMap["public_info"] != "visible" {
		t.Error("Expected public_info to remain visible")
	}

	// Check nested user map
	userMap := resultMap["user"].(map[string]any)
	if userMap["name"] != testUsername {
		t.Errorf("Expected nested name to remain '%s'", testUsername)
	}
	if userMap["password"] != DefaultMaskValue {
		t.Error("Expected nested password to be masked")
	}

	// Check deeply nested config map
	configMap := userMap["config"].(map[string]any)
	if configMap["theme"] != "dark" {
		t.Error("Expected nested theme to remain 'dark'")
	}
	if configMap["secret"] != DefaultMaskValue {
		t.Error("Expected deeply nested secret to be masked")
	}
}

// TestFilterStruct_CompleteFieldCoverage covers the remaining filterStruct edge cases
func TestFilterStructCompleteFieldCoverage(t *testing.T) {
	const testName = "test"
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password", "secret", "token"},
		MaskValue:       DefaultMaskValue,
	})

	t.Run("struct_with_interface_field", func(t *testing.T) {
		type TestStruct struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Data     any    `json:"data"`
		}

		input := TestStruct{
			Username: "john",
			Password: "secret123",
			Data:     "some interface data",
		}

		result := filter.FilterValue(testName, input)
		resultMap, ok := result.(map[string]any)
		if !ok {
			t.Fatal("Expected result to be a map")
		}

		if resultMap["username"] != "john" {
			t.Error("Expected username to be preserved")
		}
		if resultMap["password"] != DefaultMaskValue {
			t.Error("Expected password to be masked")
		}
		if resultMap["data"] != "some interface data" {
			t.Error("Expected interface data to be preserved")
		}
	})

	t.Run("struct_with_non_interface_field", func(t *testing.T) {
		type TestStruct struct {
			Name    string `json:"name"`
			Secret  string `json:"secret"`
			private string // Unexported, can't interface
		}

		input := TestStruct{
			Name:    testName,
			Secret:  "hidden",
			private: "invisible",
		}

		result := filter.FilterValue(testName, input)
		resultMap, ok := result.(map[string]any)
		if !ok {
			t.Fatal("Expected result to be a map")
		}

		if resultMap["name"] != testName {
			t.Error("Expected name to be preserved")
		}
		if resultMap["secret"] != DefaultMaskValue {
			t.Error("Expected secret to be masked")
		}
		// private field should not appear as it's unexported
		if _, exists := resultMap["private"]; exists {
			t.Error("Unexported field should not appear in result")
		}
	})

	t.Run("pointer_to_nil_struct", func(t *testing.T) {
		type TestStruct struct {
			Name string `json:"name"`
		}

		var input *TestStruct // nil pointer

		result := filter.FilterValue(testName, input)
		// Should return the original nil pointer
		if result != input {
			t.Error("Expected nil pointer to be returned unchanged")
		}
	})

	t.Run("pointer_to_valid_struct", func(t *testing.T) {
		type TestStruct struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		}

		input := &TestStruct{
			Name:     "john",
			Password: "secret",
		}

		result := filter.FilterValue(testName, input)
		// Based on FilterValue implementation, pointers pass through unchanged
		// since they're not struct type (they're pointer type)
		if result != input {
			t.Error("Expected pointer to struct to pass through unchanged")
		}
	})

	t.Run("pointer_handling_in_filterStruct", func(t *testing.T) {
		// Test the pointer handling within filterStruct directly
		type TestStruct struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		}

		input := &TestStruct{
			Name:     "john",
			Password: "secret",
		}

		// Call filterStruct directly to test pointer dereferencing
		result := filter.filterStruct(input)
		resultMap, ok := result.(map[string]any)
		if !ok {
			t.Fatal("Expected result to be a map")
		}

		if resultMap["name"] != "john" {
			t.Error("Expected name to be preserved")
		}
		if resultMap["password"] != DefaultMaskValue {
			t.Error("Expected password to be masked")
		}
	})

	t.Run("struct_with_embedded_struct", func(t *testing.T) {
		type EmbeddedStruct struct {
			Secret string `json:"secret"`
		}

		type TestStruct struct {
			Name string         `json:"name"`
			Auth EmbeddedStruct `json:"auth"`
		}

		input := TestStruct{
			Name: testName,
			Auth: EmbeddedStruct{Secret: "hidden"},
		}

		result := filter.FilterValue(testName, input)
		resultMap, ok := result.(map[string]any)
		if !ok {
			t.Fatal("Expected result to be a map")
		}

		if resultMap["name"] != testName {
			t.Error("Expected name to be preserved")
		}

		// The embedded struct should be recursively filtered
		authMap, ok := resultMap["auth"].(map[string]any)
		if !ok {
			t.Fatal("Expected auth to be a map")
		}
		if authMap["secret"] != DefaultMaskValue {
			t.Error("Expected embedded secret to be masked")
		}
	})

	t.Run("struct_with_slice_field", func(t *testing.T) {
		type TestStruct struct {
			Name  string   `json:"name"`
			Items []string `json:"items"`
		}

		input := TestStruct{
			Name:  testName,
			Items: []string{"item1", "item2"},
		}

		result := filter.FilterValue(testName, input)
		resultMap, ok := result.(map[string]any)
		if !ok {
			t.Fatal("Expected result to be a map")
		}

		if resultMap["name"] != testName {
			t.Error("Expected name to be preserved")
		}

		items, ok := resultMap["items"].([]string)
		if !ok {
			t.Fatal("Expected items to be a slice")
		}
		if len(items) != 2 || items[0] != "item1" || items[1] != "item2" {
			t.Error("Expected items slice to be preserved")
		}
	})

	t.Run("struct_field_that_cannot_interface", func(t *testing.T) {
		// Create a struct with a field that cannot be anyd
		type TestStruct struct {
			Name     string `json:"name"`
			Password string `json:"password"`
			// Note: In Go, all exported fields can be anyd, so this
			// test is mainly for the CanInterface() check coverage
		}

		input := TestStruct{
			Name:     testName,
			Password: "secret",
		}

		// All exported fields should be processable
		result := filter.FilterValue(testName, input)
		resultMap, ok := result.(map[string]any)
		if !ok {
			t.Fatal("Expected result to be a map")
		}

		if resultMap["name"] != testName {
			t.Error("Expected name to be preserved")
		}
		if resultMap["password"] != DefaultMaskValue {
			t.Error("Expected password to be masked")
		}
	})
}
