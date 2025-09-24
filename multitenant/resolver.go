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

	tenantID := strings.TrimSpace(req.Header.Get(headerName))
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

	// Derive canonical host (optionally trust proxies)
	host := req.Host
	if r.TrustProxies {
		if fwd := req.Header.Get("X-Forwarded-Host"); fwd != "" {
			// Use the first value if comma-separated
			if idx := strings.Index(fwd, ","); idx >= 0 {
				fwd = fwd[:idx]
			}
			host = strings.TrimSpace(fwd)
		}
	}
	// Strip port safely (best-effort): handle host:port and leave IPv6 literals intact
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[:i], ":") {
		host = host[:i]
	}

	// Require dot-delimited root domain to prevent suffix trickery
	if host == r.RootDomain {
		return "", ErrTenantResolutionFailed
	}
	if !strings.HasSuffix(host, "."+r.RootDomain) {
		return "", ErrTenantResolutionFailed
	}

	// Extract subdomain part and drop the separating dot
	tenantPart := strings.TrimSuffix(host, "."+r.RootDomain)
	if tenantPart == "" {
		return "", ErrTenantResolutionFailed
	}
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
