package messaging

import (
	"net/url"
	"strings"
)

// #nosec G101 -- Placeholder text for redacted URLs, not actual credentials
const redactedAMQPPlaceholder = "amqp://****:****@<host>:<port>/<vhost>"

// redactAMQPURL removes sensitive credentials from AMQP URLs for safe logging.
// It preserves the URL structure (scheme, host, port, vhost) while masking the password.
// The username is preserved for debugging purposes.
//
// Supported schemes: amqp://, amqps://
//
// Examples:
//   - Input:  "amqp://guest:secret@localhost:5672/prod"
//   - Output: "amqp://guest:****@localhost:5672/prod"
//
// If the URL cannot be parsed, returns a generic placeholder to avoid leaking credentials.
func redactAMQPURL(amqpURL string) string {
	// Empty URL edge case
	if amqpURL == "" {
		return redactedAMQPPlaceholder
	}

	u, err := url.Parse(amqpURL)
	if err != nil {
		// If parsing fails, return generic message without credentials
		return redactedAMQPPlaceholder
	}

	// Validate AMQP scheme
	if u.Scheme != "amqp" && u.Scheme != "amqps" {
		// Not an AMQP URL - return placeholder to be safe
		return redactedAMQPPlaceholder
	}

	if u.Host == "" {
		return redactedAMQPPlaceholder
	}

	username := ""
	if u.User != nil {
		username = u.User.Username()
	}

	return buildRedactedURL(u, username)
}

// buildRedactedURL reconstructs the URL with masked credentials while preserving path/query.
func buildRedactedURL(u *url.URL, username string) string {
	// Build redacted URL manually to avoid URL encoding of asterisks
	var userInfo string
	if username != "" {
		userInfo = username + ":****"
	} else {
		userInfo = "****:****"
	}

	var result strings.Builder
	result.WriteString(u.Scheme)
	result.WriteString("://")
	result.WriteString(userInfo)
	result.WriteString("@")
	result.WriteString(u.Host)

	if u.RawPath != "" {
		result.WriteString(u.RawPath)
	} else if u.Path != "" {
		result.WriteString(u.Path)
	}

	if u.RawQuery != "" {
		result.WriteString("?")
		result.WriteString(u.RawQuery)
	}

	return result.String()
}
