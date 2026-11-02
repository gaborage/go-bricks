package multitenant

import "errors"

// ErrTenantResolutionFailed is returned when a resolver cannot determine the tenant identifier.
var ErrTenantResolutionFailed = errors.New("tenant resolution failed")

// ErrUntrustedForwardedHost is returned by a SubdomainResolver with TrustProxies set when
// a peer it does not trust sends X-Forwarded-Host. CompositeResolver stops on it instead of
// trying the next sub-resolver.
var ErrUntrustedForwardedHost = errors.New("multitenant: X-Forwarded-Host from an untrusted peer")

// ErrUnknownTenant is returned for a well-formed tenant ID that names no tenant the
// deployment serves. The HTTP layer answers it as a client fault (400). It wraps
// ErrTenantResolutionFailed, so a tenant-owned family's Tenant step that returns it is a
// client fault.
var ErrUnknownTenant error = &unknownTenantError{}

// unknownTenantError is ErrUnknownTenant's type: its own text, unwrapping to
// ErrTenantResolutionFailed.
type unknownTenantError struct{}

func (*unknownTenantError) Error() string { return "multitenant: unknown tenant" }

func (*unknownTenantError) Unwrap() error { return ErrTenantResolutionFailed }
