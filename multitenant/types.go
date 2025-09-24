package multitenant

import "errors"

var (
	// ErrTenantNotFound indicates the tenant configuration was not found in the provider.
	ErrTenantNotFound = errors.New("tenant configuration not found")
	// ErrTenantResolutionFailed indicates the resolver could not determine the tenant ID.
	ErrTenantResolutionFailed = errors.New("tenant resolution failed")
)

// TenantMessagingConfig holds the minimal messaging configuration resolved per tenant.
type TenantMessagingConfig struct {
	URL string
}
