package messaging

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactAMQPURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "standard_amqp_url_with_credentials",
			input:    "amqp://guest:secret123@localhost:5672/",
			expected: "amqp://guest:****@localhost:5672/",
		},
		{
			name:     "amqps_secure_url",
			input:    "amqps://admin:SuperSecret@rabbitmq.prod:5671/production",
			expected: "amqps://admin:****@rabbitmq.prod:5671/production",
		},
		{
			name:     "url_with_special_chars_in_password",
			input:    "amqp://user:p@ss%20w0rd!@localhost:5672/vhost",
			expected: "amqp://user:****@localhost:5672/vhost",
		},
		{
			name:     "url_with_complex_vhost",
			input:    "amqp://guest:guest@localhost:5672/%2Fproduction",
			expected: "amqp://guest:****@localhost:5672/%2Fproduction",
		},
		{
			name:     "url_without_credentials",
			input:    "amqp://localhost:5672/",
			expected: "amqp://****:****@localhost:5672/",
		},
		{
			name:     "amqps_without_credentials",
			input:    "amqps://rabbitmq.prod:5671/",
			expected: "amqps://****:****@rabbitmq.prod:5671/",
		},
		{
			name:     "url_with_username_only_no_password",
			input:    "amqp://guest@localhost:5672/",
			expected: "amqp://guest:****@localhost:5672/",
		},
		{
			name:     "url_with_query_parameters",
			input:    "amqp://user:pass@localhost:5672/?heartbeat=30&connection_timeout=10",
			expected: "amqp://user:****@localhost:5672/?heartbeat=30&connection_timeout=10",
		},
		{
			name:     "malformed_url_returns_placeholder",
			input:    "not-a-valid-url",
			expected: "amqp://****:****@<host>:<port>/<vhost>",
		},
		{
			name:     "empty_url",
			input:    "",
			expected: "amqp://****:****@<host>:<port>/<vhost>",
		},
		{
			name:     "non_amqp_scheme_returns_placeholder",
			input:    "http://user:pass@localhost:80/",
			expected: "amqp://****:****@<host>:<port>/<vhost>",
		},
		{
			name:     "empty_host_returns_placeholder",
			input:    "amqp://guest:secret@/vhost",
			expected: "amqp://****:****@<host>:<port>/<vhost>",
		},
		{
			name:     "encoded_credentials_are_masked",
			input:    "amqp://user%2Fname:p%40ss@localhost:5672/vh%2Fost",
			expected: "amqp://user/name:****@localhost:5672/vh%2Fost",
		},
		{
			name:     "url_with_ip_address",
			input:    "amqp://admin:secret@192.168.1.100:5672/",
			expected: "amqp://admin:****@192.168.1.100:5672/",
		},
		{
			name:     "url_with_long_password",
			input:    "amqp://user:verylongpasswordwithmanychars123456789@localhost:5672/",
			expected: "amqp://user:****@localhost:5672/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := redactAMQPURL(tt.input)
			assert.Equal(t, tt.expected, result)

			// Additional security assertion: ensure original password is not in result
			if tt.input != "" && tt.input != "not-a-valid-url" {
				// Extract password from input URL for verification
				if len(tt.input) > 10 && tt.input[0:4] == "amqp" {
					// Simple check: result should not contain "secret", "pass", "SuperSecret"
					assert.NotContains(t, result, "secret123")
					assert.NotContains(t, result, "SuperSecret")
					assert.NotContains(t, result, "p@ss%20w0rd!")
					assert.NotContains(t, result, "p@ss")
					assert.NotContains(t, result, "verylongpasswordwithmanychars123456789")
				}
			}
		})
	}
}

func TestRedactAMQPURLPreservesDebuggingInfo(t *testing.T) {
	input := "amqp://admin:secret@rabbitmq.example.com:5672/production"
	result := redactAMQPURL(input)

	// Should preserve host
	assert.Contains(t, result, "rabbitmq.example.com")

	// Should preserve port
	assert.Contains(t, result, "5672")

	// Should preserve vhost
	assert.Contains(t, result, "production")

	// Should preserve username
	assert.Contains(t, result, "admin")

	// Should mask password
	assert.Contains(t, result, "****")
	assert.NotContains(t, result, "secret")
}
