package auth

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by credential verification. ErrKeySetUnavailable is
// deliberately outside the ErrInvalidCredential chain: an unreachable issuer key
// set is a server-side fault (503), while every other failure below is a caller
// fault (401).
var (
	// ErrMissingCredential reports that the request presented no bearer credential.
	ErrMissingCredential = errors.New("auth: missing bearer credential")

	// ErrInvalidCredential is the umbrella for every verification-rule failure.
	// Callers match on it with errors.Is and map it to 401.
	ErrInvalidCredential = errors.New("auth: invalid credential")

	// ErrKeySetUnavailable reports that the issuer key set was never fetched or
	// is past its stale ceiling, so no verification decision can be made.
	ErrKeySetUnavailable = errors.New("auth: issuer key set unavailable")

	// ErrKidUnknown reports that the credential's kid is absent from the key set.
	// A KeySource returns it; the verifier folds it into the invalid-credential class.
	ErrKidUnknown = errors.New("auth: kid not present in key set")
)

// Verification failure classes. The class is the only failure detail that may be
// logged or rendered: it names the rule that rejected the credential without
// carrying any part of the credential itself.
const (
	ClassMalformed      = "malformed"
	ClassAlgorithm      = "algorithm"
	ClassKidMissing     = "kid_missing"
	ClassKidUnknown     = "kid_unknown"
	ClassSignature      = "signature"
	ClassIssuer         = "issuer"
	ClassAudience       = "audience"
	ClassExpired        = "expired"
	ClassNotYetValid    = "not_yet_valid"
	ClassIssuedInFuture = "issued_in_future"
	ClassMissingExpiry  = "missing_expiry"
	ClassType           = "type"
)

// VerificationError reports which verification rule rejected a credential.
//
// SECURITY: neither Error() nor any field may carry the credential string, the
// raw token, signature bytes or the "sub" claim. Error() renders the class and
// nothing else — in particular it never renders Cause, because a library cause
// routinely embeds the token it failed on. Cause is kept for DEBUG-level
// inspection by the framework only; callers constructing a VerificationError
// must not pass a cause that embeds the credential.
type VerificationError struct {
	// Class is one of the Class* constants.
	Class string

	// Cause is the underlying failure, for framework DEBUG logging only. It is
	// deliberately absent from the errors.Is chain so no cause can reclassify
	// the 401 that this error represents.
	Cause error
}

// NewVerificationError builds a VerificationError for the given class. Cause may be nil.
func NewVerificationError(class string, cause error) *VerificationError {
	return &VerificationError{Class: class, Cause: cause}
}

// Error renders the failure class only.
func (e *VerificationError) Error() string {
	return fmt.Sprintf("auth: credential rejected (class: %s)", e.Class)
}

// Unwrap returns ErrInvalidCredential so errors.Is(err, ErrInvalidCredential) holds
// for every verification failure, whatever its class.
func (e *VerificationError) Unwrap() error {
	return ErrInvalidCredential
}

// ConfigError represents a configuration error during auth initialization.
// These errors are fail-fast and should abort application startup.
type ConfigError struct {
	Field   string // Section-qualified configuration key that failed validation
	Message string // Human-readable error message
	Err     error  // Underlying error, if any
}

// Error implements the error interface.
func (e *ConfigError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("auth configuration error: %s: %s: %v", e.Field, e.Message, e.Err)
	}
	return fmt.Sprintf("auth configuration error: %s: %s", e.Field, e.Message)
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *ConfigError) Unwrap() error {
	return e.Err
}

// NewConfigError creates a new configuration error.
func NewConfigError(field, message string, err error) *ConfigError {
	return &ConfigError{
		Field:   field,
		Message: message,
		Err:     err,
	}
}
