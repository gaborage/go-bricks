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
