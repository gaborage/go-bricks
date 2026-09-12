package auth

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCredential = "eyJhbGciOiJSUzI1NiIsImtpZCI6ImsxIn0.eyJzdWIiOiJ1c2VyLTQyIn0.c2lnbmF0dXJl"

func TestKeySetUnavailableIsNotAnInvalidCredential(t *testing.T) {
	require.NotErrorIs(t, ErrKeySetUnavailable, ErrInvalidCredential)
	assert.NotErrorIs(t, ErrInvalidCredential, ErrKeySetUnavailable)
}

func TestVerificationErrorIsAnInvalidCredential(t *testing.T) {
	err := NewVerificationError(ClassSignature, errors.New("crypto/rsa: verification error"))

	require.ErrorIs(t, err, ErrInvalidCredential)
	assert.NotErrorIs(t, err, ErrKeySetUnavailable)
}

func TestVerificationErrorCarriesItsClass(t *testing.T) {
	err := NewVerificationError(ClassAudience, nil)

	var target *VerificationError
	require.ErrorAs(t, error(err), &target)
	assert.Equal(t, ClassAudience, target.Class)
	assert.Contains(t, err.Error(), string(ClassAudience))
}

func TestVerificationErrorUnwrapReturnsTheSentinel(t *testing.T) {
	cause := errors.New("boom")
	err := NewVerificationError(ClassMalformed, cause)

	assert.Equal(t, ErrInvalidCredential, err.Unwrap())
	assert.Equal(t, cause, err.Cause)
	// The cause is diagnostic only: it deliberately stays out of the errors.Is chain
	// so a cause can never reclassify a 401 into something else.
	assert.NotErrorIs(t, err, cause)
}

func TestVerificationErrorNeverRendersTheCredential(t *testing.T) {
	subject := "user-42"
	err := NewVerificationError(ClassSignature, fmt.Errorf("token %s rejected for sub %s", testCredential, subject))

	rendered := err.Error()
	assert.NotContains(t, rendered, testCredential)
	assert.NotContains(t, rendered, subject)
	assert.NotContains(t, rendered, "c2lnbmF0dXJl")
	assert.NotContains(t, fmt.Sprintf("%v", err), testCredential)
	assert.NotContains(t, fmt.Sprintf("%+v", err), testCredential)
}

func TestConfigErrorRendersFieldAndMessage(t *testing.T) {
	err := NewConfigError("auth.jwt.issuer", "issuer is required", nil)

	assert.Equal(t, "auth.jwt.issuer", err.Field)
	assert.Equal(t, "issuer is required", err.Message)
	assert.Contains(t, err.Error(), "auth.jwt.issuer")
	assert.Contains(t, err.Error(), "issuer is required")
	assert.NoError(t, err.Unwrap())
}

func TestConfigErrorWrapsItsCause(t *testing.T) {
	cause := errors.New("parse failure")
	err := NewConfigError("auth.jwt.jwksuri", "jwks uri is invalid", cause)

	require.ErrorIs(t, err, cause)
	assert.Equal(t, cause, err.Unwrap())
	assert.Contains(t, err.Error(), "parse failure")
}
