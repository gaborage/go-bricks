package multitenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"pgregory.net/rapid"
)

var (
	hostGen   = rapid.StringMatching(`[a-z0-9.-]{1,40}`)
	pathGen   = rapid.StringMatching(`[a-zA-Z0-9/._-]{0,60}`)
	hdrGen    = rapid.StringMatching(`[ -~]{0,40}`)
	tenantGen = rapid.StringMatching(`[a-z0-9]{1,20}`)
)

func newReq(url string) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
}

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
		host := hostGen.Draw(rt, "host")
		path := "/" + pathGen.Draw(rt, "path")
		hdr := hdrGen.Draw(rt, "hdr")

		req := newReq("http://placeholder/")
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
		req := newReq("http://example.com/")
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
		tenant := tenantGen.Draw(rt, "tenant")

		assertResolves := func(name string, r TenantResolver, req *http.Request, want string) {
			got, err := r.ResolveTenant(context.Background(), req)
			if err != nil {
				rt.Fatalf("%s: unexpected error: %v", name, err)
			}
			if got != want {
				rt.Fatalf("%s: got %q want %q", name, got, want)
			}
		}

		headerReq := newReq("/")
		headerReq.Header.Set(tenantIDHeader, tenant)
		assertResolves("HeaderResolver", &HeaderResolver{HeaderName: tenantIDHeader}, headerReq, tenant)

		subReq := newReq("/")
		subReq.Host = tenant + ".example.com"
		assertResolves("SubdomainResolver", &SubdomainResolver{RootDomain: "example.com"}, subReq, tenant)

		pathReq := newReq("/itsp/" + tenant)
		assertResolves("PathResolver", &PathResolver{Segment: 2, Prefix: "itsp"}, pathReq, tenant)

		tenant2 := tenantGen.Draw(rt, "tenant2")
		if tenant2 == tenant {
			return // degenerate draw: no observable first-match signal
		}
		compReq := newReq("/")
		compReq.Host = tenant + ".example.com"
		compReq.Header.Set(tenantIDHeader, tenant2)
		composite := &CompositeResolver{Resolvers: []TenantResolver{
			&SubdomainResolver{RootDomain: "example.com"},
			&HeaderResolver{HeaderName: tenantIDHeader},
		}}
		assertResolves("CompositeResolver (subdomain must win over header)", composite, compReq, tenant)
	})
}
