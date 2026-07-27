package multitenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"pgregory.net/rapid"
)

type stubResolver struct {
	tenant string
	err    error
	called bool
}

func (s *stubResolver) ResolveTenant(_ context.Context, _ *http.Request) (string, error) {
	s.called = true
	return s.tenant, s.err
}

// Contract: resolution identifies or errors — never panics, never returns
// ("", nil). rapid surfaces any panic as a failure with a reproducing seed.
func TestResolversNeverPanicOrReturnEmptyProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		host := rapid.StringMatching(`[a-z0-9.-]{1,40}`).Draw(rt, "host")
		path := "/" + rapid.StringMatching(`[a-zA-Z0-9/._-]{0,60}`).Draw(rt, "path")
		hdr := rapid.StringMatching(`[ -~]{0,40}`).Draw(rt, "hdr")

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://placeholder/", http.NoBody)
		req.Host = host
		req.URL.Path = path
		req.Header.Set(tenantIDHeader, hdr)

		resolvers := []TenantResolver{
			&HeaderResolver{HeaderName: tenantIDHeader},
			&SubdomainResolver{RootDomain: "example.com"},
			&PathResolver{Segment: rapid.IntRange(1, 4).Draw(rt, "seg"), Prefix: "itsp"},
			&CompositeResolver{Resolvers: []TenantResolver{
				&SubdomainResolver{RootDomain: "example.com"},
				&HeaderResolver{HeaderName: tenantIDHeader},
			}},
		}
		for _, r := range resolvers {
			tenant, err := r.ResolveTenant(context.Background(), req)
			if err == nil && tenant == "" {
				rt.Fatalf("%T returned empty tenant without error", r)
			}
		}
	})
}

func TestCompositeFirstMatchProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(rt, "n")
		winner := rapid.IntRange(0, n-1).Draw(rt, "winner")
		stubs := make([]*stubResolver, n)
		chain := make([]TenantResolver, n)
		for i := range stubs {
			if i < winner {
				stubs[i] = &stubResolver{err: ErrTenantResolutionFailed}
			} else {
				stubs[i] = &stubResolver{tenant: "tenant-x"}
			}
			chain[i] = stubs[i]
		}
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/", http.NoBody)
		got, err := (&CompositeResolver{Resolvers: chain}).ResolveTenant(context.Background(), req)
		if err != nil {
			rt.Fatalf("composite failed with a succeeding resolver at %d: %v", winner, err)
		}
		if got != "tenant-x" {
			rt.Fatalf("got %q want tenant-x", got)
		}
		for i := winner + 1; i < n; i++ {
			if stubs[i].called {
				rt.Fatalf("resolver %d consulted after winner %d", i, winner)
			}
		}
	})
}

// Contract-completion: TestResolversNeverPanicOrReturnEmptyProperty draws
// hosts/paths uninformed by any resolver's success shape, so its never-empty
// half is vacuous for SubdomainResolver and PathResolver — a random
// `[a-z0-9.-]{1,40}` host essentially never ends in ".example.com", and a
// random path essentially never starts with "itsp". This property
// complements it by constructing guaranteed-success inputs per resolver and
// pinning the exact resolved tenant, so the success path is actually
// exercised rather than only reachable in principle.
//
// PathResolver's Segment is 1-indexed over ALL path parts split from the
// full, unmodified req.URL.Path — resolver.go never strips Prefix before
// splitting; Prefix is purely a gate (via pathutil.StripPathPrefix) on which
// paths are attempted at all. So for Prefix "itsp" and path "/itsp/<tenant>",
// parts is ["itsp", "<tenant>"], and Segment 2 resolves parts[1], the
// tenant — verified by reading resolver.go's ResolveTenant before writing
// this construction.
func TestResolversResolveWellFormedInputsProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		tenant := rapid.StringMatching(`[a-z0-9]{1,20}`).Draw(rt, "tenant")

		headerReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
		headerReq.Header.Set(tenantIDHeader, tenant)
		got, err := (&HeaderResolver{HeaderName: tenantIDHeader}).ResolveTenant(context.Background(), headerReq)
		if err != nil {
			rt.Fatalf("HeaderResolver: unexpected error: %v", err)
		}
		if got != tenant {
			rt.Fatalf("HeaderResolver: got %q want %q", got, tenant)
		}

		subReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
		subReq.Host = tenant + ".example.com"
		got, err = (&SubdomainResolver{RootDomain: "example.com"}).ResolveTenant(context.Background(), subReq)
		if err != nil {
			rt.Fatalf("SubdomainResolver: unexpected error: %v", err)
		}
		if got != tenant {
			rt.Fatalf("SubdomainResolver: got %q want %q", got, tenant)
		}

		pathReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/itsp/"+tenant, http.NoBody)
		got, err = (&PathResolver{Segment: 2, Prefix: "itsp"}).ResolveTenant(context.Background(), pathReq)
		if err != nil {
			rt.Fatalf("PathResolver: unexpected error: %v", err)
		}
		if got != tenant {
			rt.Fatalf("PathResolver: got %q want %q", got, tenant)
		}

		tenant2 := rapid.StringMatching(`[a-z0-9]{1,20}`).Draw(rt, "tenant2")
		if tenant2 == tenant {
			return // degenerate draw: no observable first-match signal
		}
		compReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
		compReq.Host = tenant + ".example.com"
		compReq.Header.Set(tenantIDHeader, tenant2)
		composite := &CompositeResolver{Resolvers: []TenantResolver{
			&SubdomainResolver{RootDomain: "example.com"},
			&HeaderResolver{HeaderName: tenantIDHeader},
		}}
		got, err = composite.ResolveTenant(context.Background(), compReq)
		if err != nil {
			rt.Fatalf("CompositeResolver: unexpected error: %v", err)
		}
		if got != tenant {
			rt.Fatalf("CompositeResolver: got %q want %q (subdomain must win over header)", got, tenant)
		}
	})
}
