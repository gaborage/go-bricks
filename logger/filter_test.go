package logger

import (
	"testing"
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
		found := false
		for _, field := range config.SensitiveFields {
			if field == expected {
				found = true
				break
			}
		}
		if !found {
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
	result = filter.FilterString("username", "john_doe")
	if result != "john_doe" {
		t.Errorf("Expected 'john_doe', got '%s'", result)
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
	result = filter.FilterValue("username", "john")
	if result != "john" {
		t.Errorf("Expected 'john', got '%v'", result)
	}

	// Test map filtering
	input := map[string]interface{}{
		"username": "john",
		"password": "secret123",
		"email":    "john@example.com",
	}
	result = filter.FilterValue("user_data", input)
	resultMap := result.(map[string]interface{})

	if resultMap["username"] != "john" {
		t.Errorf("Expected username to remain 'john', got '%v'", resultMap["username"])
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

	input := map[string]interface{}{
		"username": "john_doe",
		"password": "secret123",
		"api_key":  "sk_live_1234567890",
		"email":    "john@example.com",
	}

	result := filter.FilterFields(input)

	if result["username"] != "john_doe" {
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
