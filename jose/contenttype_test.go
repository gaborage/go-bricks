package jose

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsContentTypeVariants(t *testing.T) {
	tests := []struct {
		name     string
		ct       string
		expected bool
	}{
		{name: "exact_match", ct: "application/jose", expected: true},
		{name: "uppercase", ct: "Application/JOSE", expected: true},
		{name: "with_charset_param", ct: "application/jose; charset=utf-8", expected: true},
		{name: "leading_space", ct: " application/jose", expected: true},
		{name: "different_subtype", ct: "application/jose+json", expected: false},
		{name: "json", ct: "application/json", expected: false},
		{name: "empty", ct: "", expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsContentType(tt.ct))
		})
	}
}

func TestIsErrorRecognizesJOSEError(t *testing.T) {
	assert.True(t, IsError(&Error{Sentinel: ErrDecryptFailed, Code: "JOSE_DECRYPT_FAILED"}))

	// Wrapped JOSE error must still classify (the canonical use case for IsError —
	// a transport-level wrap should not hide the underlying classification).
	wrapped := fmt.Errorf("transport: %w", &Error{Sentinel: ErrSignatureInvalid, Code: "JOSE_SIGNATURE_INVALID"})
	assert.True(t, IsError(wrapped))

	// Plain stdlib errors must NOT classify — that's the discrimination
	// callers depend on for retry-policy decisions.
	assert.False(t, IsError(errors.New("tcp reset by peer")))
	assert.False(t, IsError(nil))
}
