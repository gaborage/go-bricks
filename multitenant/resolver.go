package multitenant

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// TenantResolver resolves the tenant identifier from an incoming request.
type TenantResolver interface {
	ResolveTenant(ctx context.Context, req *http.Request) (string, error)
}

// HeaderResolver extracts the tenant identifier from a configured request header.
type HeaderResolver struct {
	HeaderName string
}

// ResolveTenant implements TenantResolver.
func (r *HeaderResolver) ResolveTenant(ctx context.Context, req *http.Request) (string, error) {
	_ = ctx
	if r == nil || req == nil {
		return "", ErrTenantResolutionFailed
	}

	headerName := r.HeaderName
	if headerName == "" {
		headerName = "X-Tenant-ID"
	}

	tenantID := req.Header.Get(headerName)
	if tenantID == "" {
		return "", ErrTenantResolutionFailed
	}

	return tenantID, nil
}

// SubdomainResolver extracts the tenant identifier from the request host.
type SubdomainResolver struct {
	RootDomain   string
	TrustProxies bool
}

// ResolveTenant implements TenantResolver.
func (r *SubdomainResolver) ResolveTenant(ctx context.Context, req *http.Request) (string, error) {
	_ = ctx
	if r == nil || req == nil {
		return "", ErrTenantResolutionFailed
	}

	if r.RootDomain == "" {
		return "", ErrTenantResolutionFailed
	}

	// Get host from request, prefer X-Forwarded-Host if trust_proxies is enabled
	host := req.Host
	if r.TrustProxies {
		if forwardedHost := req.Header.Get("X-Forwarded-Host"); forwardedHost != "" {
			host = forwardedHost
		}
	}

	// Strip port if present
	if colonIndex := strings.LastIndex(host, ":"); colonIndex > 0 {
		// Only strip if it looks like host:port (not IPv6)
		if !strings.Contains(host[:colonIndex], ":") {
			host = host[:colonIndex]
		}
	}

	// Ensure host ends with our root domain
	if !strings.HasSuffix(host, r.RootDomain) {
		return "", ErrTenantResolutionFailed
	}

	// Extract tenant part (everything before the root domain)
	tenantPart := host[:len(host)-len(r.RootDomain)]
	if tenantPart == "" {
		return "", ErrTenantResolutionFailed
	}

	// Handle multiple subdomain levels (e.g., "client.tenant" from "client.tenant.api.example.com")
	// For simplicity, use the entire subdomain part as tenant ID
	return tenantPart, nil
}

// CompositeResolver tries multiple resolvers until one succeeds.
type CompositeResolver struct {
	Resolvers   []TenantResolver
	TenantRegex *regexp.Regexp // Optional validation pattern
}

// ResolveTenant implements TenantResolver.
func (r *CompositeResolver) ResolveTenant(ctx context.Context, req *http.Request) (string, error) {
	if r == nil {
		return "", ErrTenantResolutionFailed
	}
	for _, resolver := range r.Resolvers {
		if resolver == nil {
			continue
		}
		tenantID, err := resolver.ResolveTenant(ctx, req)
		if err == nil && tenantID != "" {
			// Validate tenant ID against pattern if configured
			if r.TenantRegex != nil && !r.TenantRegex.MatchString(tenantID) {
				continue // Try next resolver
			}
			return tenantID, nil
		}
	}
	return "", ErrTenantResolutionFailed
}

// ValidatingResolver wraps any resolver with tenant ID validation.
type ValidatingResolver struct {
	Resolver    TenantResolver
	TenantRegex *regexp.Regexp
}

// ResolveTenant implements TenantResolver with validation.
func (r *ValidatingResolver) ResolveTenant(ctx context.Context, req *http.Request) (string, error) {
	if r == nil || r.Resolver == nil {
		return "", ErrTenantResolutionFailed
	}

	tenantID, err := r.Resolver.ResolveTenant(ctx, req)
	if err != nil {
		return "", err
	}

	// Validate tenant ID format if regex is provided
	if r.TenantRegex != nil && !r.TenantRegex.MatchString(tenantID) {
		return "", ErrTenantResolutionFailed
	}

	return tenantID, nil
}
