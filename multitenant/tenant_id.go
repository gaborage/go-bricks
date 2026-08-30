package multitenant

import (
	"errors"
	"fmt"
	"regexp"
)

// defaultTenantIDPattern is the framework's tenant identifier grammar: lowercase
// alphanumerics and hyphens, 1-64 bytes. It lives here rather than in server
// because messaging reads the same rule off a tenant stamp and cannot import
// server (import cycle).
//
// SECURITY: unexported behind DefaultTenantIDPattern so no consumer can loosen
// tenant validation process-wide by reassigning it (or nil it into a panic).
var defaultTenantIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// DefaultTenantIDPattern returns the framework's tenant identifier grammar.
func DefaultTenantIDPattern() *regexp.Regexp {
	return defaultTenantIDPattern
}

// ErrInvalidTenantID reports a tenant identifier that does not match
// DefaultTenantIDPattern.
var ErrInvalidTenantID = errors.New("multitenant: tenant id does not match the default grammar")

// ValidateTenantID checks an identifier against DefaultTenantIDPattern.
//
// SECURITY: the identifier is caller-written (an HTTP header, a message stamp),
// so the error carries its byte length only — never the value itself.
func ValidateTenantID(id string) error {
	if defaultTenantIDPattern.MatchString(id) {
		return nil
	}
	return fmt.Errorf("%w: %d bytes", ErrInvalidTenantID, len(id))
}
