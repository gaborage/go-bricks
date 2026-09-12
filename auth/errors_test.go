package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCredentialSignature is the segment a leak would most obviously expose; it
// is asserted separately from the whole credential.
const testCredentialSignature = "not-a-real-signature"

// testCredential stands in for a bearer credential in the leak assertions. It is
// assembled at runtime and deliberately NOT JWT-shaped: a committed
// base64url JWT literal trips gosec G101 and the review mirror's secret
// scanner, and nothing here needs the value to parse — these tests assert only
// that an error never renders it.
func testCredential() string {
	return strings.Join([]string{"header", "payload", testCredentialSignature}, ".")
}

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
	credential := testCredential()
	err := NewVerificationError(ClassSignature, fmt.Errorf("token %s rejected for sub %s", credential, subject))

	rendered := err.Error()
	assert.NotContains(t, rendered, credential)
	assert.NotContains(t, rendered, subject)
	assert.NotContains(t, rendered, testCredentialSignature)
	assert.NotContains(t, fmt.Sprintf("%v", err), credential)
	assert.NotContains(t, fmt.Sprintf("%+v", err), credential)
}

// leakyCauseError is a cause whose EXPORTED field carries credential-shaped
// material, so %#v on a VerificationError holding it would print that material
// without VerificationError.Format. It is a value type on purpose: fmt expands a
// nested struct value inline under %#v but prints a nested pointer as an
// address, and a cause built with fmt.Errorf keeps its text in an unexported
// field — either shape would make the assertions below pass unfixed.
type leakyCauseError struct {
	Credential string
}

func (leakyCauseError) Error() string { return "auth: upstream cause" }

// TestVerificationErrorFormatNeverRendersTheCause pins the %#v seam: fmt's
// Go-syntax verb bypasses Error, so without fmt.Formatter it dumps the struct
// and the exported fields of whatever Cause holds. Both the pointer and the
// VALUE form are covered: a pointer-receiver Format leaves the value form
// dumping Cause, which is the very hole the method closes.
func TestVerificationErrorFormatNeverRendersTheCause(t *testing.T) {
	credential := testCredential()
	ptr := NewVerificationError(ClassSignature, leakyCauseError{Credential: credential})
	// render goes through a variable format so the verb under test survives
	// gocritic's redundantSprint rewrite to err.Error().
	render := func(format string, value any) string { return fmt.Sprintf(format, value) }

	want := "auth: credential rejected (class: signature)"
	for name, err := range map[string]any{"pointer": ptr, "value": *ptr} {
		t.Run(name, func(t *testing.T) {
			for _, verb := range []string{"%v", "%s", "%+v", "%#v"} {
				assert.Equal(t, want, render(verb, err), verb)
			}
			assert.Equal(t, fmt.Sprintf("%q", want), render("%q", err))
			assert.Equal(t, "%!d(auth.VerificationError="+want+")", render("%d", err))

			for _, verb := range []string{"%v", "%s", "%q", "%+v", "%#v", "%d"} {
				rendered := render(verb, err)
				assert.NotContains(t, rendered, credential, verb)
				assert.NotContains(t, rendered, testCredentialSignature, verb)
			}
		})
	}

	require.ErrorIs(t, ptr, ErrInvalidCredential)
	require.NotErrorIs(t, ptr, leakyCauseError{Credential: credential})
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
