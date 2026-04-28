package jose

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrorIsMatchesSentinel(t *testing.T) {
	e := &Error{Sentinel: ErrDecryptFailed, Code: "JOSE_DECRYPT_FAILED"}
	assert.True(t, errors.Is(e, ErrDecryptFailed))
	assert.False(t, errors.Is(e, ErrSignatureInvalid))
}

func TestErrorErrorIncludesCodeAndCause(t *testing.T) {
	cause := errors.New("underlying failure")
	e := &Error{
		Sentinel: ErrDecryptFailed,
		Code:     "JOSE_DECRYPT_FAILED",
		Message:  "Failed to decrypt request payload",
		Cause:    cause,
	}
	s := e.Error()
	assert.Contains(t, s, "JOSE_DECRYPT_FAILED")
	assert.Contains(t, s, "Failed to decrypt")
	assert.Contains(t, s, "underlying failure")
}

func TestErrorNilSafe(t *testing.T) {
	var e *Error
	assert.Equal(t, "<nil>", e.Error())
	assert.Nil(t, e.Unwrap())
}
