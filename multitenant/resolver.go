package multitenant

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"slices"
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

const forwardedHostHeader = "X-Forwarded-Host"

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
	host = foldASCIICase(host)
	rootDomain := foldASCIICase(r.RootDomain)

	// Require dot-delimited root domain to prevent suffix trickery; the apex host fails it too
	tenantPart, ok := strings.CutSuffix(host, "."+rootDomain)
	if !ok || tenantPart == "" {
		return "", ErrTenantResolutionFailed
	}
	return tenantPart, nil
}

// foldASCIICase lowercases A-Z only, returning s itself when it has none. DNS names compare
// case-insensitively in ASCII alone (RFC 4343); Unicode folding would map U+212A to "k".
func foldASCIICase(s string) string {
	i := 0
	for i < len(s) && (s[i] < 'A' || s[i] > 'Z') {
		i++
	}
	if i == len(s) {
		return s
	}
	b := []byte(s)
	for ; i < len(b); i++ {
		if 'A' <= b[i] && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// requestHost returns Host, or with TrustProxies the last X-Forwarded-Host entry from a
// trusted peer: the nearest proxy wrote it, while earlier entries may be caller-authored.
func (r *SubdomainResolver) requestHost(req *http.Request) (string, error) {
	if !r.TrustProxies {
		return req.Host, nil
	}
	values := req.Header.Values(forwardedHostHeader)
	if len(values) == 0 {
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
	tenantID, _, err := r.resolve(ctx, req)
	return tenantID, err
}

// resolve is ResolveTenant plus the winning member's path provenance (see Resolve).
func (r *CompositeResolver) resolve(ctx context.Context, req *http.Request) (tenantID string, fromPath bool, err error) {
	if r == nil {
		return "", false, ErrTenantResolutionFailed
	}
	for _, resolver := range r.Resolvers {
		// A nil member fails in Resolve, so the loop moves past it.
		tenantID, fromPath, err = Resolve(ctx, resolver, req)
		if errors.Is(err, ErrUntrustedForwardedHost) {
			return "", false, err
		}
		if err == nil && tenantID != "" {
			// Validate tenant ID against pattern if configured
			if r.TenantRegex != nil && !r.TenantRegex.MatchString(tenantID) {
				continue
			}
			return tenantID, fromPath, nil
		}
	}
	return "", false, ErrTenantResolutionFailed
}

// ValidatingResolver wraps any resolver with tenant ID validation.
type ValidatingResolver struct {
	Resolver    TenantResolver
	TenantRegex *regexp.Regexp
}

// ResolveTenant implements TenantResolver with validation.
func (r *ValidatingResolver) ResolveTenant(ctx context.Context, req *http.Request) (string, error) {
	tenantID, _, err := r.resolve(ctx, req)
	return tenantID, err
}

// resolve is ResolveTenant plus the wrapped resolver's path provenance (see Resolve).
func (r *ValidatingResolver) resolve(ctx context.Context, req *http.Request) (tenantID string, fromPath bool, err error) {
	if r == nil {
		return "", false, ErrTenantResolutionFailed
	}

	tenantID, fromPath, err = Resolve(ctx, r.Resolver, req) // a nil Resolver fails here
	if err != nil {
		return "", false, err
	}

	// Validate tenant ID format if regex is provided
	if r.TenantRegex != nil && !r.TenantRegex.MatchString(tenantID) {
		return "", false, ErrTenantResolutionFailed
	}

	return tenantID, fromPath, nil
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
	if r == nil || req == nil || r.Segment <= 0 || req.URL == nil { //nolint:forbidigo // tenant resolution reads the raw path to extract the segment; the path never reaches a sink (#2212)
		return "", ErrTenantResolutionFailed
	}

	path := req.URL.Path //nolint:forbidigo // tenant resolution reads the raw path to extract the segment; the path never reaches a sink (#2212)
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

// TenantChecker reports whether a tenant id names a tenant the deployment holds;
// *config.TenantStore implements it. The check is in memory, a stated Context-First
// exception: no context, no I/O, no blocking. A source backed by a remote catalog refreshes
// its own set out of band and answers from it.
//
// The contract every implementation must keep:
//
//   - tenantID may be an unadmitted request value: a caller of HasTenant may pass it before
//     admitting it. An implementation must never log it, echo it, put it in an error,
//     send it outbound, pass it to a deps.* call, or use it as a cache, store or metric key.
//     It may only compare it against the set it holds.
//   - HasTenant answers false for "" and for any key that is not a tenant id, including a
//     config.NamedDatabasePrefix key.
//   - HasTenant is safe for concurrent use.
//   - The complete tenant set is held before app.New: no first call may answer false for a
//     tenant only because a load is still in flight.
//   - On a failed refresh the implementation keeps the last good set; it never shrinks to
//     empty because a refresh failed.
//   - A source that refreshes logs the size of each refresh's result, a count and never the
//     ids, so operators can tell when a pod's set is stale.
//   - HasTenant never panics.
//   - The answer is existence, not authorization: true says the deployment holds the tenant,
//     not that the caller may act for it.
type TenantChecker interface {
	HasTenant(tenantID string) bool
}

// Resolve resolves the tenant as resolver.ResolveTenant does, with the same error and, when
// err is nil, the same id; on error the id is always empty and fromPath false. It reports
// whether the id may have come from the request path: fromPath is true when a *PathResolver
// produced it, or a resolver whose provenance is unknown. Provenance is judged on the concrete
// framework types alone, so any other type, including one embedding or wrapping a framework
// resolver, counts as unknown.
func Resolve(ctx context.Context, resolver TenantResolver, req *http.Request) (tenantID string, fromPath bool, err error) {
	switch r := resolver.(type) {
	case nil:
		return "", false, ErrTenantResolutionFailed
	case *HeaderResolver:
		tenantID, err = r.ResolveTenant(ctx, req)
		return tenantID, false, err
	case *SubdomainResolver:
		tenantID, err = r.ResolveTenant(ctx, req)
		return tenantID, false, err
	case *CompositeResolver:
		return r.resolve(ctx, req)
	case *ValidatingResolver:
		return r.resolve(ctx, req)
	}
	tenantID, err = resolver.ResolveTenant(ctx, req)
	if err != nil {
		return "", false, err
	}
	return tenantID, true, nil
}

// MayReadPath reports whether Resolve can answer fromPath true for resolver: it is false only
// when every member able to produce an id is a *HeaderResolver or a *SubdomainResolver. It
// walks the resolver tree, so call it once at setup and keep the answer. A cycle (a
// *CompositeResolver or *ValidatingResolver reachable from itself) fails closed: the walk
// answers true rather than recurse without end.
func MayReadPath(resolver TenantResolver) bool {
	return mayReadPath(resolver, nil)
}

// mayReadPath is MayReadPath's walk. onPath holds the wrapper resolvers on the current
// descent, so meeting one again is a cycle; a resolver shared by two branches is not.
func mayReadPath(resolver TenantResolver, onPath map[TenantResolver]struct{}) bool {
	switch r := resolver.(type) {
	case nil, *HeaderResolver, *SubdomainResolver:
		return false
	case *CompositeResolver:
		if r == nil {
			return false
		}
		return descend(r, onPath, func(onPath map[TenantResolver]struct{}) bool {
			return slices.ContainsFunc(r.Resolvers, func(m TenantResolver) bool { return mayReadPath(m, onPath) })
		})
	case *ValidatingResolver:
		if r == nil {
			return false
		}
		return descend(r, onPath, func(onPath map[TenantResolver]struct{}) bool {
			return mayReadPath(r.Resolver, onPath)
		})
	}
	return true
}

// descend runs walk with wrapper marked on the path, answering true without walking when
// wrapper is already on it (a cycle).
func descend(wrapper TenantResolver, onPath map[TenantResolver]struct{}, walk func(map[TenantResolver]struct{}) bool) bool {
	if _, cycle := onPath[wrapper]; cycle {
		return true
	}
	if onPath == nil {
		onPath = make(map[TenantResolver]struct{})
	}
	onPath[wrapper] = struct{}{}
	defer delete(onPath, wrapper)
	return walk(onPath)
}
