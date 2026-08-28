package logger

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		// A mixed list: the empty entry is dropped at construction, so the bucket
		// index and the linear oracle must still agree on the surviving needle.
		// A list of ONLY empty entries would normalize to nothing and make both
		// sides unconditionally false — the same vacuous case as no_needles.
		{name: "empty_needle_beside_a_real_one", config: &FilterConfig{SensitiveFields: []string{"", sensitiveFieldPassword}}},
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

// newFilteredEventLogger builds a logger whose events are filtered by config and
// whose output lands in a buffer, so a document can be observed exactly as a
// consumer's log sink receives it.
func newFilteredEventLogger(t *testing.T, config *FilterConfig) (*ZeroLogger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	zl := zerolog.New(&buf)
	return &ZeroLogger{zlog: &zl, filter: NewSensitiveDataFilter(config)}, &buf
}

// loggedField decodes the captured line and returns the named top-level field.
func loggedField(t *testing.T, buf *bytes.Buffer, key string) any {
	t.Helper()
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), "log line should be valid JSON")
	value, ok := line[key]
	require.True(t, ok, "log line should carry field %q", key)
	return value
}

// walkerUser is a typed struct element, the shape the walker's struct branch
// already handled and must keep handling.
type walkerUser struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// filterDocumentCase pins what the filter emits for one document shape. Every
// expected value is hand-written from the JSON the shape stands for, never
// produced by running the walker over the input.
type filterDocumentCase struct {
	name     string
	document any
	wantJSON string
}

// filterDocumentCases enumerates the document shapes that reach the walker from
// a real HTTP or broker payload. Everything below the top level is []any of
// map[string]any — the shape encoding/json always produces — which no typed
// struct fixture exercises.
func filterDocumentCases() []filterDocumentCase {
	return []filterDocumentCase{
		{
			name:     "slice_of_maps",
			document: map[string]any{"data": []any{map[string]any{"name": testNameJohn, "password": testPassword}}},
			wantJSON: `{"data":[{"name":"john","password":"***"}]}`,
		},
		{
			name:     "root_slice_of_maps",
			document: []any{map[string]any{"name": testNameJohn, "password": testPassword}},
			wantJSON: `[{"name":"john","password":"***"}]`,
		},
		{
			name:     "nested_slices",
			document: map[string]any{"items": []any{[]any{1}, []any{2}}},
			wantJSON: `{"items":[[1],[2]]}`,
		},
		{
			name:     "slice_of_scalars",
			document: map[string]any{"ids": []any{"a", "b"}},
			wantJSON: `{"ids":["a","b"]}`,
		},
		{
			name:     "typed_string_slice",
			document: map[string]any{"ids": []string{"a", "b"}},
			wantJSON: `{"ids":["a","b"]}`,
		},
		{
			name:     "typed_struct_slice",
			document: map[string]any{"users": []walkerUser{{Name: testNameJohn, Password: testPassword}}},
			wantJSON: `{"users":[{"name":"john","password":"***"}]}`,
		},
		{
			name:     "needle_inside_slice_element",
			document: map[string]any{"keys": []any{map[string]any{"kid": "k1", "private_key": testAPIKey}}},
			wantJSON: `{"keys":[{"kid":"k1","private_key":"***"}]}`,
		},
		{
			// A typed nil slice is null on the wire, and stays null. The walker
			// preserves this for maps already; a rebuilt slice would emit [].
			name:     "nil_any_slice",
			document: map[string]any{"data": []any(nil)},
			wantJSON: `{"data":null}`,
		},
		{
			name:     "nil_struct_slice",
			document: map[string]any{"users": []walkerUser(nil)},
			wantJSON: `{"users":null}`,
		},
		{
			name:     "nil_nested_slice",
			document: map[string]any{"items": [][]int(nil)},
			wantJSON: `{"items":null}`,
		},
		{
			name:     "empty_non_nil_any_slice",
			document: map[string]any{"data": []any{}},
			wantJSON: `{"data":[]}`,
		},
		{
			name:     "needle_below_a_slice_of_maps_of_slices",
			document: map[string]any{"data": []any{map[string]any{"nested": []any{map[string]any{"password": testPassword}}}}},
			wantJSON: `{"data":[{"nested":[{"password":"***"}]}]}`,
		},
	}
}

// assertLoggedFieldJSON re-marshals the captured field and compares it with a
// hand-written JSON literal.
func assertLoggedFieldJSON(t *testing.T, buf *bytes.Buffer, key, wantJSON string) {
	t.Helper()
	got, err := json.Marshal(loggedField(t, buf, key))
	require.NoError(t, err)
	assert.JSONEq(t, wantJSON, string(got))
}

// TestFilterValueDocumentShapesThroughInterface drives every shape through the
// .Interface() door.
func TestFilterValueDocumentShapesThroughInterface(t *testing.T) {
	for _, tc := range filterDocumentCases() {
		t.Run(tc.name, func(t *testing.T) {
			log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
			require.NotPanics(t, func() {
				log.Info().Interface("body", tc.document).Msg("payload")
			})
			assertLoggedFieldJSON(t, buf, "body", tc.wantJSON)
		})
	}
}

// TestFilterValueDocumentShapesThroughWithFields drives every shape through the
// second door, WithFields -> FilterFields, which reaches the same walker by a
// different call path.
func TestFilterValueDocumentShapesThroughWithFields(t *testing.T) {
	for _, tc := range filterDocumentCases() {
		t.Run(tc.name, func(t *testing.T) {
			log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
			require.NotPanics(t, func() {
				log.WithFields(map[string]any{"body": tc.document}).Info().Msg("payload")
			})
			assertLoggedFieldJSON(t, buf, "body", tc.wantJSON)
		})
	}
}

// TestNewSensitiveDataFilterDropsEmptyNeedles pins that a needle list carrying an
// empty or whitespace-only entry masks exactly the fields it names, and not the
// whole log stream. strings.Contains(x, "") is true for every x, so one such
// entry reaching the matcher masks every field there is.
func TestNewSensitiveDataFilterDropsEmptyNeedles(t *testing.T) {
	for _, needles := range [][]string{
		{sensitiveFieldPassword, ""},
		{sensitiveFieldPassword, "   "},
		{"", sensitiveFieldPassword},
	} {
		t.Run(strings.Join(needles, "|"), func(t *testing.T) {
			log, buf := newFilteredEventLogger(t, &FilterConfig{SensitiveFields: needles})

			log.Info().Interface("body", map[string]any{
				"name":     testNameJohn,
				"password": testPassword,
			}).Msg("payload")

			assertLoggedFieldJSON(t, buf, "body", `{"name":"john","password":"***"}`)
		})
	}
}

// TestNewSensitiveDataFilterTrimsAndDedupsNeedles pins the other half of
// construction-time normalization, which moved here from app.resolveLoggerFilterConfig
// so that every construction door gets it. Without trimming, "  cvv  " never
// matches the field "cvv" — strings.Contains("cvv", "  cvv  ") is false — and an
// operator pasting from a CSV would see silent NON-masking, the failure this
// filter exists to prevent.
func TestNewSensitiveDataFilterTrimsAndDedupsNeedles(t *testing.T) {
	filter := NewSensitiveDataFilter(&FilterConfig{
		SensitiveFields: []string{"  cvv  ", "\tPAN\n", "pan", "PAN"},
	})

	assert.True(t, filter.isSensitiveField("cvv"), "a padded needle must match the field it names")
	assert.True(t, filter.isSensitiveField("pan"), "a padded needle must match regardless of case")
	assert.False(t, filter.isSensitiveField("name"), "an unrelated field must not match")
	// Independent count: four entries naming two distinct needles.
	assert.Equal(t, []string{"cvv", "pan"}, filter.needles.fields)
}

// TestFilterSlicePassthroughMasksAtExhaustedDepth pins the depth half of the
// passthrough decision. Elements receive maxDepth-1 and are masked at 0, so a
// slice reached with maxDepth 1 must be rebuilt as masks even though its element
// type is one the walker never rewrites. Relaxing the guard to `maxDepth > 0`
// passes every other test in this package and logs the values unmasked.
// The public door cannot reach depth 1 without eight levels of nesting.
func TestFilterSlicePassthroughMasksAtExhaustedDepth(t *testing.T) {
	filter := NewSensitiveDataFilter(DefaultFilterConfig())

	atExhausted := filter.filterValueWithProtection("tags", []string{"a"}, map[uintptr]struct{}{}, 1)
	assert.Equal(t, []any{DefaultMaskValue}, atExhausted,
		"a slice one step from the depth limit must emit masks, not its values")

	withBudget := filter.filterValueWithProtection("tags", []string{"a"}, map[uintptr]struct{}{}, 2)
	assert.Equal(t, []string{"a"}, withBudget,
		"with budget to spare the concrete slice type survives untouched")
}

// TestRewritesTypeHasNoFalseNegatives pins the invariant that keeps rewritesType
// honest. It is a static kind table restating what the walker decides at
// runtime, and nothing links the two: adding a rewriting arm to
// filterByTypeWithProtection without extending the table would silently stop
// masking inside slices of that kind.
//
// The property is ONE-DIRECTIONAL on purpose. A false positive is safe — the
// slice is rebuilt as []any when it need not have been, costing a copy. A false
// negative is the leak: the slice is passed through and its elements are never
// filtered. So: if the walker changes a value, rewritesType MUST say so.
// Each case carries a sensitive field where its kind can hold one, because a
// value the walker has no reason to touch proves nothing about whether it would.
func TestRewritesTypeHasNoFalseNegatives(t *testing.T) {
	filter := NewSensitiveDataFilter(DefaultFilterConfig())

	cases := []struct {
		name string
		// typ is the type a SLICE would carry for this element, given explicitly
		// because reflect.TypeOf on an `any` always yields the DYNAMIC type and can
		// therefore never produce Kind Interface — the arm that matters most, since
		// []any is what encoding/json produces for every JSON list of objects.
		typ      reflect.Type
		value    any
		leafKind bool // the walker returns these untouched; rewritesType must say false
	}{
		{
			name: "interface_element", typ: reflect.TypeOf([]any{}).Elem(),
			value: map[string]any{"password": testPassword},
		},
		{name: "string", value: "plain", leafKind: true},
		{name: "int", value: 7, leafKind: true},
		{name: "bool", value: true, leafKind: true},
		{name: "float64", value: 1.5, leafKind: true},
		{name: "byte", value: byte(3), leafKind: true},
		{name: "struct_with_needle", value: walkerUser{Password: testPassword}},
		{name: "pointer_to_struct_with_needle", value: &walkerUser{Password: testPassword}},
		{name: "map_string_any_with_needle", value: map[string]any{"password": testPassword}},
		{name: "map_string_string_with_needle", value: map[string]string{"password": testPassword}},
		{name: "slice_with_needle", value: []any{map[string]any{"password": testPassword}}},
		{name: "array_with_needle", value: [1]any{map[string]any{"password": testPassword}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.value)
			require.NoError(t, err)
			filtered := filter.filterValueWithProtection("plain", tc.value, map[uintptr]struct{}{}, DefaultMaxDepth)
			after, err := json.Marshal(filtered)
			require.NoError(t, err)

			typ := tc.typ
			if typ == nil {
				typ = reflect.TypeOf(tc.value)
			}
			predicted := rewritesType(typ)
			if !bytes.Equal(before, after) {
				assert.True(t, predicted,
					"the walker rewrote %s but rewritesType says it cannot — a slice of these would pass through unfiltered", tc.name)
			}
			if tc.leafKind {
				assert.False(t, predicted, "%s is a leaf the walker returns untouched", tc.name)
				assert.Equal(t, string(before), string(after), "test premise: a leaf must come back unchanged")
			}
		})
	}
}

func TestDefaultFilterConfigHasNoErrorRedactor(t *testing.T) {
	// The framework ships no scrubbing pattern of its own: the seam is inert
	// until a consumer sets it.
	assert.Nil(t, DefaultFilterConfig().ErrorRedactor)
}

// TestFilterMasksInsideJSONRawMessage pins the leak reported in #1133: a
// json.RawMessage is bytes, so the NAME filter sees one opaque leaf called
// "body" and the password inside it ships in clear through every door that
// accepts a payload. The filter must parse what looks like JSON, walk it with
// the same needles, and re-encode.
func TestFilterMasksInsideJSONRawMessage(t *testing.T) {
	payload := json.RawMessage(`{"password":"pw","user":"alice"}`)
	want := `{"password":"***","user":"alice"}`

	t.Run("interface", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", payload).Msg("payload")
		assertLoggedFieldJSON(t, buf, "body", want)
	})

	t.Run("with_fields", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.WithFields(map[string]any{"body": payload}).Info().Msg("payload")
		assertLoggedFieldJSON(t, buf, "body", want)
	})
}

// TestFilterMasksInsideBytesDoor covers the third door from #1133. Bytes() had
// no filtering beyond the field NAME, so a payload logged through it leaked
// whatever its own field names hid.
func TestFilterMasksInsideBytesDoor(t *testing.T) {
	log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

	log.Info().Bytes("body", []byte(`{"password":"pw","user":"alice"}`)).Msg("payload")

	assertLoggedFieldJSON(t, buf, "body", `{"password":"***","user":"alice"}`)
}

// TestFilterLeavesCleanPayloadByteExact is the other half of the contract: a
// payload with nothing to mask must ship EXACTLY as it arrived. Re-encoding
// every payload would silently rewrite key order, number spelling and
// whitespace for every consumer who logs one, so the filter re-encodes only
// when it actually masked something.
func TestFilterLeavesCleanPayloadByteExact(t *testing.T) {
	// Key order and number spelling are the observable discriminators. Passing
	// the payload through a decode/re-encode round trip would emit the keys
	// alphabetically (alpha, big, zeta) because a Go map marshals sorted, so
	// the ORIGINAL order surviving is proof the filter returned the input bytes
	// rather than rebuilding them. The number literals are proof too: 1e3 comes
	// back 1000 and the 20-digit integer rounds, unless the bytes are untouched.
	//
	// Interior whitespace is NOT asserted: zerolog's own encoder compacts a
	// json.RawMessage on its way to the sink, which is the sink's rendering and
	// not something this filter chose. What the filter owes is that it does not
	// re-serialize a payload it had no reason to touch.
	payload := json.RawMessage(`{"zeta":1e3,  "alpha":  [1,2],"big":12345678901234567890}`)

	log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
	log.Info().Interface("body", payload).Msg("payload")

	assert.Contains(t, buf.String(), `"body":{"zeta":1e3,"alpha":[1,2],"big":12345678901234567890}`,
		"a payload with nothing to mask keeps its key order and number literals")
}

// TestFilterMasksInsideJSONLookingString covers the string door: a string whose
// first non-space byte opens an object or array is a payload wearing a string's
// clothes, and the name filter sees straight past it.
func TestFilterMasksInsideJSONLookingString(t *testing.T) {
	log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

	log.Info().Str("body", `{"password":"pw","user":"alice"}`).Msg("payload")

	assertLoggedFieldJSON(t, buf, "body", `{"password":"***","user":"alice"}`)
}

// TestFilterMasksInsideRawMessageSlice covers []json.RawMessage, the shape that
// panicked the walker before #1131 and has leaked since.
func TestFilterMasksInsideRawMessageSlice(t *testing.T) {
	log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
	payload := []json.RawMessage{
		json.RawMessage(`{"password":"pw"}`),
		json.RawMessage(`{"user":"alice"}`),
	}

	require.NotPanics(t, func() {
		log.Info().Interface("body", payload).Msg("payload")
	})

	assertLoggedFieldJSON(t, buf, "body", `[{"password":"***"},{"user":"alice"}]`)
}

// TestFilterMasksJWKPrivateMembers pins the shape rule: a JWK's private members
// are named d, p, q, dp, dq, qi, k and oth — none of which any name needle
// matches, and all of which are the private key. The marker is `kty`, and the
// rule applies wherever the object sits: at the root, inside a JWKS `keys`
// array, or nested under an unrelated field. Public members stay readable so
// the line remains useful for debugging.
func TestFilterMasksJWKPrivateMembers(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		wantJSON string
	}{
		{
			name:     "root_jwk",
			payload:  `{"kty":"RSA","kid":"k1","n":"modulus","e":"AQAB","d":"PRIVATE","p":"P","q":"Q"}`,
			wantJSON: `{"kty":"RSA","kid":"k1","n":"modulus","e":"AQAB","d":"***","p":"***","q":"***"}`,
		},
		{
			name:     "jwks_keys_array",
			payload:  `{"keys":[{"kty":"RSA","n":"modulus","e":"AQAB","d":"PRIVATE"}]}`,
			wantJSON: `{"keys":[{"kty":"RSA","n":"modulus","e":"AQAB","d":"***"}]}`,
		},
		{
			name:     "nested_jwk",
			payload:  `{"config":{"signing":{"kty":"oct","k":"SYMMETRIC","kid":"k2"}}}`,
			wantJSON: `{"config":{"signing":{"kty":"oct","k":"***","kid":"k2"}}}`,
		},
		{
			name: "all_private_members",
			payload: `{"kty":"RSA","d":"D","p":"P","q":"Q","dp":"DP","dq":"DQ","qi":"QI",` +
				`"k":"K","oth":[{"r":"R"}]}`,
			wantJSON: `{"kty":"RSA","d":"***","p":"***","q":"***","dp":"***","dq":"***",` +
				`"qi":"***","k":"***","oth":"***"}`,
		},
		{
			// Without the kty marker these are ordinary short field names and
			// must NOT be masked — d, p and k appear in plenty of documents.
			name:     "no_kty_marker_leaves_short_names_alone",
			payload:  `{"d":"day","p":"page","k":"key-count","q":"query"}`,
			wantJSON: `{"d":"day","p":"page","k":"key-count","q":"query"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
			log.Info().Interface("body", json.RawMessage(tt.payload)).Msg("payload")
			assertLoggedFieldJSON(t, buf, "body", tt.wantJSON)
		})
	}
}

// TestFilterMasksPEMPrivateKeyBlocks pins the second shape rule. A PEM private
// key arrives as one long string under a field name like "material" that no
// needle matches; a certificate in the same position is public and must stay,
// because redacting it would remove the thing an operator reads to diagnose a
// TLS problem.
func TestFilterMasksPEMPrivateKeyBlocks(t *testing.T) {
	privateKey := "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAK\n-----END RSA PRIVATE KEY-----"
	certificate := "-----BEGIN CERTIFICATE-----\nMIIBkTCB+wIJAK\n-----END CERTIFICATE-----"

	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "rsa_private_key_masked_whole",
			payload: `{"material":` + mustJSONString(t, privateKey) + `}`,
			want:    `{"material":"***"}`,
		},
		{
			name:    "pkcs8_private_key_masked_whole",
			payload: `{"material":"-----BEGIN PRIVATE KEY-----\nMIIBOg\n-----END PRIVATE KEY-----"}`,
			want:    `{"material":"***"}`,
		},
		{
			name:    "certificate_left_readable",
			payload: `{"material":` + mustJSONString(t, certificate) + `}`,
			want:    `{"material":` + mustJSONString(t, certificate) + `}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
			log.Info().Interface("body", json.RawMessage(tt.payload)).Msg("payload")
			assertLoggedFieldJSON(t, buf, "body", tt.want)
		})
	}
}

// mustJSONString renders s as a JSON string literal, so a test payload carrying
// newlines stays readable in the source instead of being hand-escaped.
func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	encoded, err := json.Marshal(s)
	require.NoError(t, err)
	return string(encoded)
}

// TestFilterMasksUnreadablePayloadsWhole covers the fail-closed arm: a payload
// that looks like JSON and is not readable — truncated, or bigger than the cap
// — is masked entirely, because the filter cannot tell what is inside it and an
// opaque payload leaking secrets is the defect this door exists to close.
func TestFilterMasksUnreadablePayloadsWhole(t *testing.T) {
	t.Run("truncated_json", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", json.RawMessage(`{"password":"pw`)).Msg("payload")
		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "body"))
	})

	t.Run("over_the_byte_cap", func(t *testing.T) {
		config := DefaultFilterConfig()
		config.MaxPayloadBytes = 32
		log, buf := newFilteredEventLogger(t, config)

		oversized := `{"note":"` + strings.Repeat("x", 64) + `"}`
		log.Info().Interface("body", json.RawMessage(oversized)).Msg("payload")

		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "body"))
	})

	t.Run("at_the_byte_cap_is_still_parsed", func(t *testing.T) {
		config := DefaultFilterConfig()
		payload := `{"password":"pw"}`
		config.MaxPayloadBytes = len(payload)
		log, buf := newFilteredEventLogger(t, config)

		log.Info().Interface("body", json.RawMessage(payload)).Msg("payload")

		assertLoggedFieldJSON(t, buf, "body", `{"password":"***"}`)
	})

	t.Run("non_json_bytes_are_untouched", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", []byte("not json at all")).Msg("payload")
		assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("not json at all")),
			loggedField(t, buf, "body"))
	})
}

// TestFilterMasksOverlyNestedPayloadWhole pins the third fail-closed arm, and
// the reason it is an error rather than a masked subtree: the DEPTH of a
// payload is chosen by whoever produced it, not by the code logging it, so an
// arbitrarily nested body must not be able to walk the filter down an unbounded
// stack. Too deep to walk is too deep to vouch for, so the whole payload is
// masked — the same answer an unparseable payload gets.
func TestFilterMasksOverlyNestedPayloadWhole(t *testing.T) {
	// One level deeper than the walker's budget, built as nested objects.
	deep := "null"
	for range DefaultMaxDepth + 1 {
		deep = `{"a":` + deep + `}`
	}

	t.Run("beyond_the_budget_masks_whole", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", json.RawMessage(deep)).Msg("payload")
		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "body"))
	})

	t.Run("within_the_budget_is_walked_normally", func(t *testing.T) {
		shallow := `{"a":{"b":{"password":"pw"}}}`
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", json.RawMessage(shallow)).Msg("payload")
		assertLoggedFieldJSON(t, buf, "body", `{"a":{"b":{"password":"***"}}}`)
	})
}

// TestFilterMasksJSONStringThroughEveryDoor closes the gap the Str-only hook
// left: a JSON-looking STRING is a payload whichever door carries it. Bytes
// already inherited the check from the shared type dispatch, so `Interface`,
// `WithFields` and a nested struct field all masked a []byte payload while the
// identical text typed as a string shipped in clear unless it went through Str.
func TestFilterMasksJSONStringThroughEveryDoor(t *testing.T) {
	const payload = `{"password":"pw","user":"alice"}`
	const want = `{"password":"***","user":"alice"}`

	t.Run("str", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Str("body", payload).Msg("payload")
		assertLoggedFieldJSON(t, buf, "body", want)
	})

	t.Run("interface", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", payload).Msg("payload")
		assertLoggedFieldJSON(t, buf, "body", want)
	})

	t.Run("with_fields", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.WithFields(map[string]any{"body": payload}).Info().Msg("payload")
		assertLoggedFieldJSON(t, buf, "body", want)
	})

	t.Run("nested_in_a_map", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("envelope", map[string]any{"body": payload}).Msg("payload")
		assertLoggedFieldJSON(t, buf, "envelope", `{"body":`+want+`}`)
	})

	t.Run("a_plain_string_is_left_alone", func(t *testing.T) {
		// Not keyed "message": that is zerolog's own key for the Msg text, so
		// the assertion would read the message back rather than the field.
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Str("detail", "user alice signed in").Msg("payload")
		assert.Equal(t, "user alice signed in", loggedField(t, buf, "detail"))
	})
}

// TestFilterMasksBarePEMPrivateKey pins the shape a PEM key actually takes in a
// log call. The PEM rule used to sit behind the JSON gate, reachable only for a
// key already embedded as a string inside a JSON document — but a PEM block
// begins `-----BEGIN`, never `{` or `[`, so a key logged on its own, which is
// the ordinary way one shows up, never reached the rule at all.
func TestFilterMasksBarePEMPrivateKey(t *testing.T) {
	privateKey := "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAK\n-----END RSA PRIVATE KEY-----"
	certificate := "-----BEGIN CERTIFICATE-----\nMIIBkTCB+wIJAK\n-----END CERTIFICATE-----"

	t.Run("str_door", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Str("material", privateKey).Msg("payload")
		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "material"))
	})

	t.Run("interface_door", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("material", privateKey).Msg("payload")
		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "material"))
	})

	t.Run("bytes_door", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Bytes("material", []byte(privateKey)).Msg("payload")
		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "material"))
	})

	t.Run("certificate_stays_readable_at_every_door", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Str("material", certificate).Msg("payload")
		assert.Equal(t, certificate, loggedField(t, buf, "material"))
	})
}

// TestFilterMasksPayloadWithTrailingContent pins the leak CodeRabbit found in
// the trailing-content check. decoder.More() answers "is there another element
// in the current context", which is a different question from "did the payload
// end": after `{}` the next byte of `{}]{"password":"pw"}` is a `]`, read as a
// closing delimiter rather than another value, so More() said no. The walk then
// masked the empty object it had decoded, found nothing to mask, and — a clean
// payload shipping byte-exact being the rule — emitted the ORIGINAL bytes, with
// the password still in them. One document or nothing.
func TestFilterMasksPayloadWithTrailingContent(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "closing_delimiter_then_a_second_document", payload: `{}]{"password":"pw"}`},
		{name: "two_documents", payload: `{"a":1}{"password":"pw"}`},
		{name: "document_then_garbage", payload: `{"a":1} not json`},
		{name: "array_then_a_document", payload: `[1,2]{"password":"pw"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

			log.Info().Interface("body", json.RawMessage(tt.payload)).Msg("payload")

			line := buf.String()
			assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "body"),
				"a payload that is not exactly one document must be masked whole")
			assert.NotContains(t, line, "pw", "the trailing document must not reach the sink")
		})
	}

	t.Run("trailing_whitespace_is_still_one_document", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

		log.Info().Interface("body", json.RawMessage("{\"password\":\"pw\"}\n  ")).Msg("payload")

		assertLoggedFieldJSON(t, buf, "body", `{"password":"***"}`)
	})
}

// TestFilterMasksEveryPayloadShapeThroughEveryDoor is the acceptance matrix
// from #1133 stated as one table: every payload SHAPE through every DOOR that
// can carry it. The individual tests above pin behavior per shape; this pins
// that no door was wired differently from its siblings, which is exactly the
// gap that let a JSON-looking string mask through Str and ship in clear through
// Interface.
func TestFilterMasksEveryPayloadShapeThroughEveryDoor(t *testing.T) {
	const want = `{"password":"***","user":"alice"}`
	const raw = `{"password":"pw","user":"alice"}`

	shapes := map[string]any{
		"raw_message":       json.RawMessage(raw),
		"byte_slice":        []byte(raw),
		"json_string":       raw,
		"raw_message_slice": []json.RawMessage{json.RawMessage(raw)},
	}
	// The list shape renders as an array, so it wants its own expectation.
	wantFor := func(shape string) string {
		if shape == "raw_message_slice" {
			return `[` + want + `]`
		}
		return want
	}

	for shape, payload := range shapes {
		t.Run("interface_"+shape, func(t *testing.T) {
			log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
			log.Info().Interface("body", payload).Msg("payload")
			assertLoggedFieldJSON(t, buf, "body", wantFor(shape))
		})

		t.Run("with_fields_"+shape, func(t *testing.T) {
			log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
			log.WithFields(map[string]any{"body": payload}).Info().Msg("payload")
			assertLoggedFieldJSON(t, buf, "body", wantFor(shape))
		})

		t.Run("nested_in_a_map_"+shape, func(t *testing.T) {
			log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
			log.Info().Interface("envelope", map[string]any{"body": payload}).Msg("payload")
			assertLoggedFieldJSON(t, buf, "envelope", `{"body":`+wantFor(shape)+`}`)
		})
	}

	// The two doors that take a concrete type rather than an any.
	t.Run("bytes_door", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Bytes("body", []byte(raw)).Msg("payload")
		assertLoggedFieldJSON(t, buf, "body", want)
	})

	t.Run("str_door", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Str("body", raw).Msg("payload")
		assertLoggedFieldJSON(t, buf, "body", want)
	})
}

// TestPayloadDoorGuardBoundaries pins the BOUNDARIES of the guards the payload
// door is built from, not just the behaviors they implement. The mutation gate
// found six survivors on these lines: every test above passed with `limit < 0`
// flipped to `<= 0`, with the marker scan's bound moved by one, and with the
// depth test shifted — because nothing exercised any guard AT its edge. These
// cases are chosen so that moving any one of those comparisons by one breaks
// exactly one of them.
func TestPayloadDoorGuardBoundaries(t *testing.T) {
	const payload = `{"password":"pw"}`

	t.Run("cap_of_zero_means_the_default_not_disabled", func(t *testing.T) {
		// The `limit < 0` boundary. With `<= 0` this payload would ship in clear,
		// because zero would read as the opt-out instead of as "unset".
		config := DefaultFilterConfig()
		config.MaxPayloadBytes = 0
		log, buf := newFilteredEventLogger(t, config)

		log.Info().Interface("body", json.RawMessage(payload)).Msg("payload")

		assertLoggedFieldJSON(t, buf, "body", `{"password":"***"}`)
	})

	t.Run("a_negative_cap_disables_the_door", func(t *testing.T) {
		// The other side of the same comparison: -1 must opt out.
		config := DefaultFilterConfig()
		config.MaxPayloadBytes = -1
		log, buf := newFilteredEventLogger(t, config)

		log.Info().Interface("body", json.RawMessage(payload)).Msg("payload")

		assertLoggedFieldJSON(t, buf, "body", payload)
	})

	t.Run("a_payload_exactly_at_the_cap_is_parsed", func(t *testing.T) {
		config := DefaultFilterConfig()
		config.MaxPayloadBytes = len(payload)
		log, buf := newFilteredEventLogger(t, config)

		log.Info().Interface("body", json.RawMessage(payload)).Msg("payload")

		assertLoggedFieldJSON(t, buf, "body", `{"password":"***"}`)
	})

	t.Run("a_payload_one_byte_over_the_cap_is_masked_whole", func(t *testing.T) {
		config := DefaultFilterConfig()
		config.MaxPayloadBytes = len(payload) - 1
		log, buf := newFilteredEventLogger(t, config)

		log.Info().Interface("body", json.RawMessage(payload)).Msg("payload")

		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "body"))
	})

	t.Run("a_value_that_is_exactly_the_marker_is_not_a_private_key", func(t *testing.T) {
		// The two-step: the marker gate admits this value, and the pattern then
		// declines it, because a header needs a `PRIVATE KEY` label after the
		// marker. Pins that the gate is a cheap PRE-filter and not the decision.
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

		log.Info().Str("material", "-----BEGIN").Msg("payload")

		assert.Equal(t, "-----BEGIN", loggedField(t, buf, "material"))
	})

	t.Run("a_marker_at_the_very_end_of_a_value_is_found", func(t *testing.T) {
		// The marker sits flush against the end of the value, so the gate finds
		// it and the pattern still declines: no label, no key. The value stays
		// readable, which is what a certificate-adjacent string must do.
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

		log.Info().Str("material", "trailing -----BEGIN").Msg("payload")

		assert.Equal(t, "trailing -----BEGIN", loggedField(t, buf, "material"))
	})

	t.Run("a_private_key_at_the_very_end_of_a_value_is_masked", func(t *testing.T) {
		// A complete header preceded by other text: the gate finds the marker
		// mid-value and the pattern matches, so the whole value is masked.
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

		log.Info().Str("material", "key: -----BEGIN EC PRIVATE KEY-----").Msg("payload")

		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "material"))
	})

	t.Run("a_near_miss_marker_is_not_matched", func(t *testing.T) {
		// One byte different inside the marker, so the gate never admits it and
		// the pattern is never consulted — a near miss stays readable.
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

		log.Info().Str("material", "-----BEGiN RSA PRIVATE KEY-----").Msg("payload")

		assert.Equal(t, "-----BEGiN RSA PRIVATE KEY-----", loggedField(t, buf, "material"))
	})

	t.Run("a_document_exactly_at_the_depth_budget_is_walked", func(t *testing.T) {
		// The `depth <= 0` boundary and the `depth-1` step together: this nests
		// exactly to the budget, so masking must still happen. One less depth
		// spent per level, or a shifted test, and this masks whole instead.
		deep := `{"password":"pw"}`
		for range DefaultMaxDepth - 1 {
			deep = `{"a":` + deep + `}`
		}
		want := `{"password":"***"}`
		for range DefaultMaxDepth - 1 {
			want = `{"a":` + want + `}`
		}

		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", json.RawMessage(deep)).Msg("payload")

		assertLoggedFieldJSON(t, buf, "body", want)
	})

	t.Run("arrays_spend_the_depth_budget_too", func(t *testing.T) {
		// The ARRAY branch has its own `depth-1`, and every depth test above
		// nests OBJECTS, so that step was never exercised — the mutation gate
		// found it by flipping it to `depth+1` and watching every test pass.
		// Nested one level past the budget through arrays alone.
		deep := `{"password":"pw"}`
		for range DefaultMaxDepth {
			deep = `[` + deep + `]`
		}

		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", json.RawMessage(deep)).Msg("payload")

		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "body"))
	})

	t.Run("arrays_within_the_budget_are_still_walked", func(t *testing.T) {
		// The other side: a masked leaf reached THROUGH arrays proves the
		// branch recurses at all, so the case above cannot pass for the wrong
		// reason (a budget never spent would walk it; a budget spent twice per
		// level would mask this one too).
		deep := `{"password":"pw"}`
		want := `{"password":"***"}`
		for range DefaultMaxDepth - 1 {
			deep = `[` + deep + `]`
			want = `[` + want + `]`
		}

		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", json.RawMessage(deep)).Msg("payload")

		assertLoggedFieldJSON(t, buf, "body", want)
	})

	t.Run("a_document_one_level_past_the_budget_is_masked_whole", func(t *testing.T) {
		deep := `{"password":"pw"}`
		for range DefaultMaxDepth {
			deep = `{"a":` + deep + `}`
		}

		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", json.RawMessage(deep)).Msg("payload")

		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "body"))
	})
}

// blobPayload is a NAMED byte-slice type, the way a service usually spells a
// stored or pre-encoded body. It is a payload by exactly the argument []byte is
// — the name filter sees one leaf whatever the type is called.
type blobPayload []byte

// TestFilterMasksInsideNamedByteSliceTypes pins what opaqueBytes' doc always
// claimed and its type switch did not do: matching only `json.RawMessage` and
// `[]byte` sent every other byte slice down the reflect walk, which reads it as
// a list of numbers and masks nothing inside it.
func TestFilterMasksInsideNamedByteSliceTypes(t *testing.T) {
	payload := blobPayload(`{"password":"pw","user":"alice"}`)

	t.Run("interface", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", payload).Msg("payload")
		assertLoggedFieldJSON(t, buf, "body", `{"password":"***","user":"alice"}`)
	})

	t.Run("with_fields", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.WithFields(map[string]any{"body": payload}).Info().Msg("payload")
		assertLoggedFieldJSON(t, buf, "body", `{"password":"***","user":"alice"}`)
	})

	t.Run("a_named_byte_slice_carrying_a_pem_key_is_masked", func(t *testing.T) {
		key := blobPayload("-----BEGIN RSA PRIVATE KEY-----\nMIIBOg\n-----END RSA PRIVATE KEY-----")
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("material", key).Msg("payload")
		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "material"))
	})

	t.Run("a_named_byte_slice_that_is_not_a_payload_is_untouched", func(t *testing.T) {
		plain := blobPayload("not json at all")
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", plain).Msg("payload")
		assert.Equal(t, base64.StdEncoding.EncodeToString(plain), loggedField(t, buf, "body"))
	})
}

// TestPEMScanIsBoundedByTheCap pins that the cap bounds the PEM header scan on a
// payload that is NOT JSON-shaped. Such a payload passes through rather than
// being masked — an oversized blob is not made secret by being large — but past
// the cap it is not scanned for a key either, so an arbitrarily long value
// cannot be walked end to end on the logging path.
func TestPEMScanIsBoundedByTheCap(t *testing.T) {
	key := "-----BEGIN RSA PRIVATE KEY-----\nMIIBOg\n-----END RSA PRIVATE KEY-----"

	t.Run("within_the_cap_a_key_is_still_masked", func(t *testing.T) {
		config := DefaultFilterConfig()
		config.MaxPayloadBytes = len(key)
		log, buf := newFilteredEventLogger(t, config)

		log.Info().Str("material", key).Msg("payload")

		assert.Equal(t, DefaultMaskValue, loggedField(t, buf, "material"))
	})

	t.Run("past_the_cap_it_is_not_scanned_and_passes_through", func(t *testing.T) {
		config := DefaultFilterConfig()
		config.MaxPayloadBytes = len(key) - 1
		log, buf := newFilteredEventLogger(t, config)

		log.Info().Str("material", key).Msg("payload")

		assert.Equal(t, key, loggedField(t, buf, "material"))
	})
}

// nestedMap is a NAMED map type, so it takes the reflect.Map arm rather than
// the map[string]any fast path, and its values can be more of itself — which is
// what makes depth observable through that walker.
type nestedMap map[string]any

// TestReflectMapWalkerKeysAndDepth covers the map walker the payload extraction
// turned into changed lines. Neither behavior was pinned before: the package
// had no test for a non-string-keyed map, and none that exhausts the depth
// budget through this arm rather than through the map[string]any fast path.
func TestReflectMapWalkerKeysAndDepth(t *testing.T) {
	t.Run("a_non_string_key_is_stringified", func(t *testing.T) {
		// `k.String()` on a non-string reflect.Value renders `<int Value>`, not
		// the number, so the Kind test is what makes an int-keyed map readable.
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

		log.Info().Interface("counts", map[int]string{7: "seven"}).Msg("payload")

		assert.Equal(t, map[string]any{"7": "seven"}, loggedField(t, buf, "counts"))
	})

	t.Run("a_string_key_is_unchanged", func(t *testing.T) {
		// The other side of the same branch, so the test pins the condition
		// rather than just one arm of it.
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

		log.Info().Interface("headers", map[string]string{"x-trace": "abc"}).Msg("payload")

		assert.Equal(t, map[string]any{"x-trace": "abc"}, loggedField(t, buf, "headers"))
	})

	t.Run("a_sensitive_key_is_masked_through_this_arm", func(t *testing.T) {
		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())

		log.Info().Interface("headers", map[string]string{"authorization": "Bearer x"}).Msg("payload")

		assert.Equal(t, map[string]any{"authorization": DefaultMaskValue}, loggedField(t, buf, "headers"))
	})

	t.Run("depth_is_spent_per_level_through_this_arm", func(t *testing.T) {
		// Nested one level past the budget. The walk must exhaust and cut the
		// subtree to the mask; if the step stopped decrementing, the structure
		// would survive intact all the way down instead.
		deep := nestedMap{"leaf": "bottom"}
		for range DefaultMaxDepth + 1 {
			deep = nestedMap{"a": deep}
		}

		log, buf := newFilteredEventLogger(t, DefaultFilterConfig())
		log.Info().Interface("body", deep).Msg("payload")

		// Walk down the logged structure: somewhere at or before the budget the
		// value must become the mask string rather than another map.
		current := loggedField(t, buf, "body")
		levels := 0
		for {
			m, isMap := current.(map[string]any)
			if !isMap {
				break
			}
			next, ok := m["a"]
			if !ok {
				break
			}
			current = next
			levels++
		}
		assert.Equal(t, DefaultMaskValue, current,
			"the walk must cut the subtree at the budget, not descend forever")
		assert.LessOrEqual(t, levels, DefaultMaxDepth,
			"the cut must happen at or before the depth budget")
	})
}
