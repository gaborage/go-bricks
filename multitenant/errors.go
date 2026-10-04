package multitenant

import "errors"

// ErrTenantResolutionFailed is returned when a resolver cannot determine the tenant identifier.
var ErrTenantResolutionFailed = errors.New("tenant resolution failed")

// ErrUntrustedForwardedHost is returned by a SubdomainResolver with TrustProxies set when
// a peer it does not trust sends X-Forwarded-Host. CompositeResolver stops on it instead of
// trying the next sub-resolver.
var ErrUntrustedForwardedHost = errors.New("multitenant: X-Forwarded-Host from an untrusted peer")
