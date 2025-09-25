package multitenant

import "errors"

// ErrTenantResolutionFailed is returned when a resolver cannot determine the tenant identifier.
var ErrTenantResolutionFailed = errors.New("tenant resolution failed")
