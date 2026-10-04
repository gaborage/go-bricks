package multitenant

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/gaborage/go-bricks/internal/pathutil"
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
	// TrustedProxies adds peer ranges trusted to send X-Forwarded-Host, on top of
	// loopback, link-local, private and unix-socket peers.
	TrustedProxies []*net.IPNet
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

	host, err := r.requestHost(req)
	if err != nil {
		return "", err
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

	tenantPart := strings.TrimSuffix(host, "."+r.RootDomain)
	if tenantPart == "" {
		return "", ErrTenantResolutionFailed
	}
	return tenantPart, nil
}

// requestHost returns Host, or with TrustProxies the last X-Forwarded-Host entry from a
// trusted peer: the nearest proxy wrote it, while earlier entries may be caller-authored.
func (r *SubdomainResolver) requestHost(req *http.Request) (string, error) {
	values := req.Header.Values("X-Forwarded-Host")
	if !r.TrustProxies || len(values) == 0 {
		return req.Host, nil
	}
	if !r.trustedPeer(req.RemoteAddr) {
		return "", ErrUntrustedForwardedHost
	}
	last := values[len(values)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	return strings.TrimSpace(last), nil
}

// trustedPeer applies the peer rule echo's ExtractSchemeFromHeaders uses for
// X-Forwarded-Proto: unix-socket, loopback, link-local and private peers, plus TrustedProxies.
func (r *SubdomainResolver) trustedPeer(remoteAddr string) bool {
	if remoteAddr == "" || remoteAddr[0] == '@' || remoteAddr[0] == '/' {
		return true
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if i := strings.IndexByte(host, '%'); i != -1 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
		return true
	}
	for _, trusted := range r.TrustedProxies {
		if trusted != nil && trusted.Contains(ip) {
			return true
		}
	}
	return false
}

// CompositeResolver tries multiple resolvers until one succeeds. ErrUntrustedForwardedHost
// is terminal: it is returned at once and no later resolver is tried.
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
		if errors.Is(err, ErrUntrustedForwardedHost) {
			return "", err
		}
		if err == nil && tenantID != "" {
			// Validate tenant ID against pattern if configured
			if r.TenantRegex != nil && !r.TenantRegex.MatchString(tenantID) {
				continue
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

// PathResolver extracts the tenant identifier from a path segment.
//
// Segment is 1-indexed (segment 1 is the first non-empty path part after the
// leading slash). When Prefix is non-empty, the resolver only attempts
// extraction for requests whose path equals Prefix exactly or starts with
// "Prefix/" — non-matching paths return ErrTenantResolutionFailed so the
// composite chain (if any) can fall through to other resolvers.
type PathResolver struct {
	Segment int
	Prefix  string
}

// ResolveTenant implements TenantResolver.
func (r *PathResolver) ResolveTenant(ctx context.Context, req *http.Request) (string, error) {
	_ = ctx
	if r == nil || req == nil || r.Segment <= 0 || req.URL == nil {
		return "", ErrTenantResolutionFailed
	}

	path := req.URL.Path
	normalizedPrefix := pathutil.NormalizePrefix(r.Prefix)
	if normalizedPrefix != "" {
		if _, ok := pathutil.StripPathPrefix(path, normalizedPrefix); !ok {
			return "", ErrTenantResolutionFailed
		}
	}

	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	// Tolerate trailing slashes by dropping trailing empty segments, then
	// reject any intermediate empty segment — malformed inputs like
	// "/foo//bar" must not produce a tenant ID at any segment index, not
	// just the slot pointing at the empty part.
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for _, p := range parts {
		if p == "" {
			return "", ErrTenantResolutionFailed
		}
	}
	if r.Segment > len(parts) {
		return "", ErrTenantResolutionFailed
	}
	tenantID := strings.TrimSpace(parts[r.Segment-1])
	if tenantID == "" {
		return "", ErrTenantResolutionFailed
	}
	return tenantID, nil
}
