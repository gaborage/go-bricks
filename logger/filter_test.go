package logger

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

const (
	testUsername             = "test_user_john"
	testPassword             = "test_password_123"
	testUserDoe              = "test_user_john_doe"
	testNameJohn             = "john"
	testEmail                = "john@example.com"
	testAPIKey               = "REDACTED_API_KEY_FOR_TESTING"
	testAPIKeyShort          = "REDACTED_KEY"
	expectedMaskedPwdMsg     = "Expected password to be masked, got '%v'"
	expectedMaskedMapMsg     = "Expected result to be a map"
	expectedPreservedNameMsg = "Expected name field to remain unfiltered"
	unexpectedValueMsg       = "Expected '%s', got '%s'"
)

func TestDefaultFilterConfig(t *testing.T) {
	config := DefaultFilterConfig()

	if config == nil {
		t.Fatal("DefaultFilterConfig should not return nil")
	} else if config.MaskValue != DefaultMaskValue {
		t.Errorf("Expected default mask value '%s', got '%s'", DefaultMaskValue, config.MaskValue)
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
	} else if filter.config.MaskValue != DefaultMaskValue {
		t.Errorf("Expected default mask value '%s', got '%s'", DefaultMaskValue, filter.config.MaskValue)
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
		t.Errorf(unexpectedValueMsg, DefaultMaskValue, result)
	}

	// Test non-sensitive field
	result = filter.FilterString("username", testUserDoe)
	if result != testUserDoe {
		t.Errorf(unexpectedValueMsg, testUserDoe, result)
	}

	// Test URL-valued sensitive field: full masking, not structure-preserving
	result = filter.FilterString("broker_url", "amqp://user:pass@host/vhost")
	if result != DefaultMaskValue {
		t.Errorf(unexpectedValueMsg, DefaultMaskValue, result)
	}
}

func TestFilterMasksURLValuedSensitiveFieldWithQuerySecret(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"secret"},
		MaskValue:       DefaultMaskValue,
	})

	result := filter.FilterString("client_secret", "https://idp.example.com/oauth?client_secret=abc123")
	if result != DefaultMaskValue {
		t.Errorf(unexpectedValueMsg, DefaultMaskValue, result)
	}
}

func TestFilterMasksURLValuedSensitiveFieldWithoutUserinfo(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"apikey"},
		MaskValue:       DefaultMaskValue,
	})

	result := filter.FilterString("apikey", "https://api.example.com/v1/keys?apikey=zzz")
	if result != DefaultMaskValue {
		t.Errorf(unexpectedValueMsg, DefaultMaskValue, result)
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
		t.Errorf("Expected '%s', got '%v'", DefaultMaskValue, result)
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
		"email":    testEmail,
	}
	result = filter.FilterValue("user_data", input)
	resultMap := result.(map[string]any)

	if resultMap["username"] != testUsername {
		t.Errorf("Expected username to remain '%s', got '%v'", testUsername, resultMap["username"])
	}
	if resultMap["password"] != DefaultMaskValue {
		t.Errorf(expectedMaskedPwdMsg, resultMap["password"])
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
		"api_key":  testAPIKey,
		"email":    testEmail,
	}

	result := filter.FilterFields(input)

	if result["username"] != testUserDoe {
		t.Errorf("Expected username to remain unchanged")
	}
	if result["password"] != DefaultMaskValue {
		t.Errorf(expectedMaskedPwdMsg, result["password"])
	}
	if result["api_key"] != DefaultMaskValue {
		t.Errorf("Expected api_key to be masked")
	}
	if result["email"] != testEmail {
		t.Errorf("Expected email to remain unchanged")
	}
}

// =============================================================================
// Enhanced Sensitive Data Filtering Tests
// =============================================================================

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
		Email:    testEmail,
		APIKey:   testAPIKeyShort,
	}

	result := filter.FilterValue("user", input)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal(expectedMaskedMapMsg)
	}

	// Check that non-sensitive fields are preserved
	if resultMap["username"] != "test_user_john_doe" {
		t.Errorf("Expected username to remain 'test_user_john_doe', got '%v'", resultMap["username"])
	}
	if resultMap["email"] != testEmail {
		t.Errorf("Expected email to remain unchanged, got '%v'", resultMap["email"])
	}

	// Check that sensitive fields are masked
	if resultMap["password"] != DefaultMaskValue {
		t.Errorf(expectedMaskedPwdMsg, resultMap["password"])
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

	// With the updated implementation, pointers to structs should be filtered
	result := filter.FilterValue("user", input)

	// The pointer should now be filtered and return a map
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Errorf("Expected pointer to struct to be filtered and return a map, got %T", result)
	}

	// Check that username is preserved and password is masked
	if resultMap["username"] != testUsername {
		t.Errorf("Expected username to be preserved, got '%v'", resultMap["username"])
	}

	if resultMap["password"] != DefaultMaskValue {
		t.Errorf(expectedMaskedPwdMsg, resultMap["password"])
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
		t.Fatal(expectedMaskedMapMsg)
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
		t.Fatal(expectedMaskedMapMsg)
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

	// Field with json:"-" should be completely excluded from the result
	if _, exists := resultMap["IgnoredField"]; exists {
		t.Error("Field with json:\"-\" should be completely excluded from result")
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

	// Non-sensitive key: should pass through (now returns map[string]any after reflect.Map fix)
	stringMap := map[string]string{"username": "alice"}
	mapResult := filter.FilterValue("field", stringMap)
	mapResultTyped, ok := mapResult.(map[string]any)
	if !ok || mapResultTyped["username"] != "alice" {
		t.Errorf("Expected non-sensitive string map field to pass through unchanged")
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

// completeFieldCoverageFilter is the SensitiveDataFilter shared by the
// filterStruct edge-case tests below. The filter is immutable after
// construction so a single instance is safe to share across all tests.
var completeFieldCoverageFilter = NewSensitiveDataFilter(&FilterConfig{
	SensitiveFields: []string{"password", "secret", "token"},
	MaskValue:       DefaultMaskValue,
})

const completeCoverageTestName = "test"

func TestFilterValueMapStringStringSensitiveKeyMasked(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password", "token"},
		MaskValue:       DefaultMaskValue,
	})

	cases := []struct {
		name     string
		in       map[string]string
		key      string
		wantMask bool
	}{
		{name: "sensitive_key_masked", in: map[string]string{"password": "s3cr3t", "user": "bob"}, key: "password", wantMask: true},
		{name: "non_sensitive_preserved", in: map[string]string{"username": "bob"}, key: "username", wantMask: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := filter.FilterValue("data", tc.in)
			resultMap, ok := result.(map[string]any)
			if !ok {
				t.Fatalf("Expected map[string]any, got %T", result)
			}
			got := resultMap[tc.key]
			if tc.wantMask && got != DefaultMaskValue {
				t.Errorf("Expected %q to be masked, got %v", tc.key, got)
			}
			if !tc.wantMask && got != tc.in[tc.key] {
				t.Errorf("Expected %q to be %q, got %v", tc.key, tc.in[tc.key], got)
			}
		})
	}
}

func TestFilterValueNilMapPreservesNil(t *testing.T) {
	filter := NewSensitiveDataFilter(DefaultFilterConfig())

	// A typed-nil map must remain nil in the output, not become an empty {}.
	// Without an rv.IsNil() guard, typed nils bypass the interface nil check.
	var h http.Header // nil map of type map[string][]string
	result := filter.FilterValue("headers", h)
	if result != nil {
		t.Errorf("Expected typed-nil http.Header to filter as nil, got %T %v", result, result)
	}

	var m map[string]string // nil map of type map[string]string
	result2 := filter.FilterValue("data", m)
	if result2 != nil {
		t.Errorf("Expected typed-nil map[string]string to filter as nil, got %T %v", result2, result2)
	}

	// map[string]any is handled by the fast-path type assertion (not reflect.Map),
	// so it needs its own nil guard. Locks in regression coverage for the fast path.
	var ma map[string]any
	result3 := filter.FilterValue("payload", ma)
	if result3 != nil {
		t.Errorf("Expected typed-nil map[string]any to filter as nil, got %T %v", result3, result3)
	}
}

func TestFilterValueHTTPHeaderMasksSensitiveKeys(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"authorization", "token"},
		MaskValue:       DefaultMaskValue,
	})

	headers := map[string][]string{
		"Authorization": {"Bearer eyJhbGciOiJSUzI1NiJ9"},
		"Content-Type":  {"application/json"},
	}
	result := filter.FilterValue("headers", headers)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("Expected map[string]any, got %T", result)
	}
	if resultMap["Authorization"] != DefaultMaskValue {
		t.Errorf("Expected Authorization header to be masked, got %v", resultMap["Authorization"])
	}
	if resultMap["Content-Type"] == DefaultMaskValue {
		t.Error("Expected Content-Type header to be preserved")
	}
}

func TestFilterValueDepthExhaustionMasks(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"password"},
		MaskValue:       DefaultMaskValue,
	})

	// Build a map nested deeper than DefaultMaxDepth (8).
	// The "password" field lives at depth 10 — previously leaked due to fail-open;
	// after the fix, the entire subtree at depth 0 is replaced with the mask.
	var buildDeep func(depth int) map[string]any
	buildDeep = func(depth int) map[string]any {
		if depth == 0 {
			return map[string]any{"password": "s3cr3t", "name": "leaf"}
		}
		return map[string]any{"next": buildDeep(depth - 1)}
	}
	deep := buildDeep(DefaultMaxDepth + 2)

	result := filter.FilterValue("data", deep)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("Expected map[string]any, got %T", result)
	}
	// Top-level key is non-sensitive; the nested subtree exceeds depth limit.
	// The subtree is replaced with the mask rather than leaking its contents.
	if resultMap["next"] == nil {
		t.Error("Expected 'next' key to be present")
	}
	// Walk as deep as possible and confirm we never get a plain "s3cr3t" string.
	var checkNoLeak func(v any)
	checkNoLeak = func(v any) {
		switch val := v.(type) {
		case string:
			if val == "s3cr3t" {
				t.Error("Sensitive password leaked despite depth exhaustion")
			}
		case map[string]any:
			for _, inner := range val {
				checkNoLeak(inner)
			}
		}
	}
	checkNoLeak(result)
}

func TestFilterStructWithInterfaceField(t *testing.T) {
	type TestStruct struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Data     any    `json:"data"`
	}

	input := TestStruct{
		Username: testNameJohn,
		Password: "secret123",
		Data:     "some interface data",
	}

	result := completeFieldCoverageFilter.FilterValue(completeCoverageTestName, input)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal(expectedMaskedMapMsg)
	}

	if resultMap["username"] != testNameJohn {
		t.Error("Expected username to be preserved")
	}
	if resultMap["password"] != DefaultMaskValue {
		t.Errorf(expectedMaskedPwdMsg, resultMap["password"])
	}
	if resultMap["data"] != "some interface data" {
		t.Error("Expected interface data to be preserved")
	}
}

func TestFilterStructWithNonInterfaceField(t *testing.T) {
	type TestStruct struct {
		Name    string `json:"name"`
		Secret  string `json:"secret"`
		private string // Unexported, can't interface
	}

	input := TestStruct{
		Name:    completeCoverageTestName,
		Secret:  "hidden",
		private: "invisible",
	}

	result := completeFieldCoverageFilter.FilterValue(completeCoverageTestName, input)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal(expectedMaskedMapMsg)
	}

	if resultMap["name"] != completeCoverageTestName {
		t.Error(expectedPreservedNameMsg)
	}
	if resultMap["secret"] != DefaultMaskValue {
		t.Error("Expected secret to be masked")
	}
	// private field should not appear as it's unexported
	if _, exists := resultMap["private"]; exists {
		t.Error("Unexported field should not appear in result")
	}
}

func TestFilterStructPointerToNilStruct(t *testing.T) {
	type TestStruct struct {
		Name string `json:"name"`
	}

	var input *TestStruct // nil pointer

	result := completeFieldCoverageFilter.FilterValue(completeCoverageTestName, input)
	// Should return the original nil pointer
	if result != input {
		t.Error("Expected nil pointer to be returned unchanged")
	}
}

func TestFilterStructPointerToValidStruct(t *testing.T) {
	type TestStruct struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}

	input := &TestStruct{
		Name:     testNameJohn,
		Password: "secret",
	}

	result := completeFieldCoverageFilter.FilterValue(completeCoverageTestName, input)
	// Pointer to struct should now be filtered like a regular struct
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Error("Expected pointer to struct to be filtered and return a map")
	}

	if resultMap["name"] != testNameJohn {
		t.Error("Expected name field to remain unfiltered")
	}

	if resultMap["password"] != "***" {
		t.Error("Expected password field to be masked")
	}
}

func TestFilterStructDirectPointerHandling(t *testing.T) {
	type TestStruct struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}

	input := &TestStruct{
		Name:     testNameJohn,
		Password: "secret",
	}

	result := completeFieldCoverageFilter.FilterValue(completeCoverageTestName, input)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal(expectedMaskedMapMsg)
	}

	if resultMap["name"] != testNameJohn {
		t.Error(expectedPreservedNameMsg)
	}
	if resultMap["password"] != DefaultMaskValue {
		t.Error("Expected password to be masked")
	}
}

func TestFilterStructWithEmbeddedStruct(t *testing.T) {
	type EmbeddedStruct struct {
		Secret string `json:"secret"`
	}

	type TestStruct struct {
		Name string         `json:"name"`
		Auth EmbeddedStruct `json:"auth"`
	}

	input := TestStruct{
		Name: completeCoverageTestName,
		Auth: EmbeddedStruct{Secret: "hidden"},
	}

	result := completeFieldCoverageFilter.FilterValue(completeCoverageTestName, input)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal(expectedMaskedMapMsg)
	}

	if resultMap["name"] != completeCoverageTestName {
		t.Error(expectedPreservedNameMsg)
	}

	// The embedded struct should be recursively filtered
	authMap, ok := resultMap["auth"].(map[string]any)
	if !ok {
		t.Fatal("Expected auth to be a map")
	}
	if authMap["secret"] != DefaultMaskValue {
		t.Error("Expected embedded secret to be masked")
	}
}

func TestFilterStructWithSliceField(t *testing.T) {
	type TestStruct struct {
		Name  string   `json:"name"`
		Items []string `json:"items"`
	}

	input := TestStruct{
		Name:  completeCoverageTestName,
		Items: []string{"item1", "item2"},
	}

	result := completeFieldCoverageFilter.FilterValue(completeCoverageTestName, input)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal(expectedMaskedMapMsg)
	}

	if resultMap["name"] != completeCoverageTestName {
		t.Error(expectedPreservedNameMsg)
	}

	items, ok := resultMap["items"].([]string)
	if !ok {
		t.Fatal("Expected items to be a slice")
	}
	if len(items) != 2 || items[0] != "item1" || items[1] != "item2" {
		t.Error("Expected items slice to be preserved")
	}
}

func TestFilterStructFieldThatCannotInterface(t *testing.T) {
	// In Go, all exported fields can be anyd, so this test mainly exercises
	// the CanInterface() check for coverage.
	type TestStruct struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}

	input := TestStruct{
		Name:     completeCoverageTestName,
		Password: "secret",
	}

	result := completeFieldCoverageFilter.FilterValue(completeCoverageTestName, input)
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal(expectedMaskedMapMsg)
	}

	if resultMap["name"] != completeCoverageTestName {
		t.Error(expectedPreservedNameMsg)
	}
	if resultMap["password"] != DefaultMaskValue {
		t.Error("Expected password to be masked")
	}
}

func TestIsSensitiveFieldCardDataAndPII(t *testing.T) {
	filter := NewSensitiveDataFilter(DefaultFilterConfig())

	fieldNames := []string{
		"cardholder", "card_number", "CardNumber", "primary_account_number",
		"cvv2", "cvc2", "track2_data", "track_data", "iban", "otp_code",
	}

	for _, fieldName := range fieldNames {
		if !filter.isSensitiveField(fieldName) {
			t.Errorf("Expected field %q to be masked as card-data/PII", fieldName)
		}
	}
}

// TestIsSensitiveFieldNoOverMaskingRegression pins the collision decisions
// documented in DefaultFilterConfig: bare "pan"/"card"/"pin"/"track" are
// deliberately absent from the default list because substring matching would
// otherwise mask these benign field names. Any failure here is an
// over-masking regression, not a missing feature.
func TestIsSensitiveFieldNoOverMaskingRegression(t *testing.T) {
	filter := NewSensitiveDataFilter(DefaultFilterConfig())

	fieldNames := []string{
		"span_id", "company", "expand", "pinned_at",
		"discard_reason", "tracking_id", "card_type", "expiry_date",
	}

	for _, fieldName := range fieldNames {
		if filter.isSensitiveField(fieldName) {
			t.Errorf("Expected field %q to NOT be masked (over-masking regression)", fieldName)
		}
	}
}

func TestIsSensitiveFieldOTPAcceptedCollision(t *testing.T) {
	filter := NewSensitiveDataFilter(DefaultFilterConfig())

	// snapshotPath contains "otP" as a substring — a known, accepted over-mask
	// of the "otp" default, not a bug. See DefaultFilterConfig's comment.
	if !filter.isSensitiveField("snapshotPath") {
		t.Error("Expected snapshotPath to be masked via the accepted otp substring collision")
	}
}

func TestNewSensitiveDataFilterEmptySensitiveFieldsDisablesMasking(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{MaskValue: "X"})

	if filter.isSensitiveField("password") {
		t.Error("Expected an explicitly empty SensitiveFields to disable masking entirely")
	}
}

// TestNewSensitiveDataFilterSnapshotsSensitiveFields pins the snapshot
// semantics documented on SensitiveDataFilter.needles: the lowered needle
// list is captured once at construction, so mutating the caller's
// FilterConfig.SensitiveFields afterward — by appending or by replacing it
// outright — has no effect on matching.
func TestNewSensitiveDataFilterSnapshotsSensitiveFields(t *testing.T) {
	config := &FilterConfig{SensitiveFields: []string{"original_term"}}
	filter := NewSensitiveDataFilter(config)

	config.SensitiveFields = append(config.SensitiveFields, "appended_after_construction")
	config.SensitiveFields = []string{"replaced_entirely"}

	if !filter.isSensitiveField("original_term") {
		t.Error("Expected original_term to still be masked via the snapshot taken at construction")
	}
	if filter.isSensitiveField("appended_after_construction") {
		t.Error("Expected appended_after_construction to NOT be masked — it was added after the snapshot")
	}
	if filter.isSensitiveField("replaced_entirely") {
		t.Error("Expected replaced_entirely to NOT be masked — config.SensitiveFields was replaced, not the snapshot")
	}
}

// isSensitiveFieldLinearReference is the pre-dispatch implementation, kept
// verbatim as the differential oracle for the byFirstByte matcher.
func isSensitiveFieldLinearReference(needles []string, fieldName string) bool {
	lowerFieldName := strings.ToLower(fieldName)
	for _, sensitiveField := range needles {
		if strings.Contains(lowerFieldName, sensitiveField) {
			return true
		}
	}
	return false
}

// alphabetSweep enumerates every string of length 0-3 over the given
// alphabet. Chosen so it spells out all four 3-byte default needles ("otp",
// "cvv", "cvc", "pwd") and their one-byte-off neighbors, plus a separator and a
// byte that begins no needle. It still spells "key", which is no longer a needle
// (#1037) — the corpus derives from the live list, so the extra coverage costs
// nothing and pins that "key" stays absent.
func alphabetSweep(alphabet []byte) []string {
	n := len(alphabet)
	out := make([]string, 0, 1+n+n*n+n*n*n)
	out = append(out, "")
	for _, a := range alphabet {
		out = append(out, string([]byte{a}))
	}
	for _, a := range alphabet {
		for _, b := range alphabet {
			out = append(out, string([]byte{a, b}))
		}
	}
	for _, a := range alphabet {
		for _, b := range alphabet {
			for _, c := range alphabet {
				out = append(out, string([]byte{a, b, c}))
			}
		}
	}
	return out
}

// buildDifferentialCorpus derives the differential test's input set
// mechanically from a config's raw SensitiveFields, per plan 121 Step 3.
func buildDifferentialCorpus(rawNeedles, sweep []string) []string {
	seen := make(map[string]struct{})
	add := func(s string) { seen[s] = struct{}{} }

	// 1. empty field name and "x".
	add("")
	add("x")

	// 2. per-needle derived inputs: verbatim, lowered, uppercased forms, each
	// at start/mid/end placement plus every proper prefix and suffix.
	for _, n := range rawNeedles {
		for _, variant := range []string{n, strings.ToLower(n), strings.ToUpper(n)} {
			add(variant)
			add("x" + variant)
			add(variant + "x")
			add("xy" + variant + "yx")
			for k := 1; k < len(variant); k++ {
				add(variant[:k])
				add(variant[k:])
			}
		}
	}

	// 3. the 8 documented non-matches.
	for _, s := range []string{
		"span_id", "company", "expand", "pinned_at",
		"discard_reason", "tracking_id", "card_type", "expiry_date",
	} {
		add(s)
	}

	// 4. the documented accepted over-match.
	add("snapshotPath")
	add("SnapshotPath")
	add("SNAPSHOTPATH")

	// 5. multi-byte and invalid-UTF-8 cases.
	for _, s := range []string{
		"ключ", "xключy", "КЛЮЧ", "Grüße", "xGrüßey", "straße", "\xff",
	} {
		add(s)
	}

	// 6. the deterministic length-0..3 alphabet sweep.
	for _, s := range sweep {
		add(s)
	}

	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out
}

// TestIsSensitiveFieldDifferentialAgainstLinearReference proves the
// byFirstByte matcher is bit-identical to the pre-dispatch linear scan across
// four filter configurations and a mechanically derived input corpus. See
// plan 121 for the corpus-construction rationale.
func TestIsSensitiveFieldDifferentialAgainstLinearReference(t *testing.T) {
	sweep := alphabetSweep([]byte("otpcvkeywd_asx"))

	type namedConfig struct {
		name   string
		config *FilterConfig
	}

	defaultWithExtras := DefaultFilterConfig()
	defaultWithExtras.SensitiveFields = append(
		slices.Clone(defaultWithExtras.SensitiveFields),
		"PAN", "CVV2", "OTP", "ключ", "Grüße",
	)

	configs := []namedConfig{
		{name: "default_config", config: DefaultFilterConfig()},
		{name: "default_plus_mixed_case_multibyte_extras", config: defaultWithExtras},
		{name: "single_empty_needle", config: &FilterConfig{SensitiveFields: []string{""}}},
		{name: "no_needles", config: &FilterConfig{SensitiveFields: []string{}}},
	}

	// Sanity-check the sweep is doing work under the default config: it must
	// produce at least one match, or the alphabet proves nothing.
	sawSweepMatch := false
	defaultNeedles := NewSensitiveDataFilter(DefaultFilterConfig()).needles.fields
	for _, in := range sweep {
		if isSensitiveFieldLinearReference(defaultNeedles, in) {
			sawSweepMatch = true
			break
		}
	}
	if !sawSweepMatch {
		t.Fatal("alphabet sweep produced zero matches under the default config's reference oracle — the alphabet is wrong")
	}

	for _, nc := range configs {
		t.Run(nc.name, func(t *testing.T) {
			filter := NewSensitiveDataFilter(nc.config)
			refNeedles := filter.needles.fields
			corpus := buildDifferentialCorpus(nc.config.SensitiveFields, sweep)

			for _, in := range corpus {
				got := filter.isSensitiveField(in)
				want := isSensitiveFieldLinearReference(refNeedles, in)
				if got != want {
					t.Errorf("config %s, input %q: isSensitiveField() = %v, want %v (linear reference)", nc.name, in, got, want)
				}
			}
		})
	}
}

// TestDefaultFilterMasksSecretKeyShapesButNotIdentifiers pins both halves of
// dropping the bare "key" needle (#1037). Matching is case-insensitive substring,
// so "key" masked every field whose name merely contained it — "keys",
// "tenant_key", the framework's own "key" identifier — and no consumer could
// unmask one short of replacing the whole default list. The secret-bearing
// spellings it incidentally caught are now named explicitly instead.
func TestDefaultFilterMasksSecretKeyShapesButNotIdentifiers(t *testing.T) {
	filter := NewSensitiveDataFilter(DefaultFilterConfig())

	tests := []struct {
		name       string
		fieldName  string
		wantMasked bool
	}{
		// Explicit needles, in the two spellings the substring matcher treats as
		// unrelated: an underscore-separated name does not contain the
		// concatenated needle, nor the reverse.
		{name: "api_key", fieldName: "api_key", wantMasked: true},
		{name: "apikey_concatenated", fieldName: "apikey", wantMasked: true},
		{name: "apikey_camel", fieldName: "apiKey", wantMasked: true},
		{name: "private_key", fieldName: "private_key", wantMasked: true},
		{name: "privatekey_concatenated", fieldName: "privatekey", wantMasked: true},
		{name: "privatekey_camel", fieldName: "privateKey", wantMasked: true},
		{name: "signing_key", fieldName: "signing_key", wantMasked: true},
		{name: "signingkey_concatenated", fieldName: "signingkey", wantMasked: true},
		{name: "encryption_key", fieldName: "encryption_key", wantMasked: true},
		{name: "encryptionkey_concatenated", fieldName: "encryptionkey", wantMasked: true},
		{name: "uppercase_variant", fieldName: "PRIVATE_KEY", wantMasked: true},

		// Hyphenated spellings, which no underscore needle matches. httpclient
		// logs whole http.Header maps through this filter under LogPayloads, and
		// a header is spelled this way.
		{name: "http_header_api_key", fieldName: "X-Api-Key", wantMasked: true},
		{name: "http_header_api_key_upper", fieldName: "X-API-KEY", wantMasked: true},
		{name: "hyphenated_private_key", fieldName: "private-key", wantMasked: true},
		{name: "hyphenated_signing_key", fieldName: "signing-key", wantMasked: true},
		{name: "hyphenated_encryption_key", fieldName: "encryption-key", wantMasked: true},

		// The hyphen needles name shapes, not the bare word: an identifier
		// spelled with hyphens stays in clear, exactly as its underscore twin does.
		{name: "idempotency_key_header", fieldName: "Idempotency-Key", wantMasked: false},
		{name: "hyphenated_routing_key", fieldName: "routing-key", wantMasked: false},
		{name: "hyphenated_partition_key", fieldName: "partition-key", wantMasked: false},
		{name: "embedded_in_a_longer_name", fieldName: "tenant_private_key_pem", wantMasked: true},

		// Carried by the "secret" needle rather than a key-specific one, which is
		// why neither spelling is listed twice.
		{name: "secret_key", fieldName: "secret_key", wantMasked: true},
		{name: "secretkey_concatenated", fieldName: "secretkey", wantMasked: true},

		// Identifiers. Every one of these was masked before.
		{name: "bare_key", fieldName: "key", wantMasked: false},
		{name: "plural_keys", fieldName: "keys", wantMasked: false},
		{name: "tenant_key", fieldName: "tenant_key", wantMasked: false},
		{name: "cache_key", fieldName: "cache_key", wantMasked: false},
		{name: "routing_key", fieldName: "routing_key", wantMasked: false},
		{name: "uppercase_identifier", fieldName: "KEY", wantMasked: false},

		// Untouched by this change, asserted so a needle list edit cannot quietly
		// drop one on its way past.
		{name: "password", fieldName: "password", wantMasked: true},
		{name: "token", fieldName: "token", wantMasked: true},
		{name: "authorization", fieldName: "authorization", wantMasked: true},
		{name: "cvv", fieldName: "cvv", wantMasked: true},
		{name: "plain_name", fieldName: "name", wantMasked: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filter.isSensitiveField(tt.fieldName); got != tt.wantMasked {
				t.Errorf("isSensitiveField(%q) = %v, want %v", tt.fieldName, got, tt.wantMasked)
			}

			// The value path is what a caller actually sees, so assert there too
			// rather than trusting the predicate alone.
			gotValue := filter.FilterString(tt.fieldName, "v")
			if (gotValue == DefaultMaskValue) != tt.wantMasked {
				t.Errorf("FilterString(%q, \"v\") = %q, want masked=%v", tt.fieldName, gotValue, tt.wantMasked)
			}
		})
	}
}
