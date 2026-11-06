package multitenant

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/racedetect"
)

const (
	provenanceHeaderTenant = "acme"
	provenanceHostTenant   = "initech"
	provenancePathTenant   = "globex"
	provenanceTenantPath   = "/tenants/" + provenancePathTenant + "/cards"
)

var errConsumerResolver = errors.New("consumer resolver failed")

// embeddingHeaderResolver is a consumer type embedding a framework resolver: its provenance
// is unknown to Resolve even though every call lands on HeaderResolver.
type embeddingHeaderResolver struct{ *HeaderResolver }

// embeddingPathResolver is a consumer type embedding *PathResolver without an override: every
// call lands on PathResolver, and Resolve still judges it unknown (fromPath true either way).
type embeddingPathResolver struct{ *PathResolver }

// overridingPathResolver embeds *PathResolver but overrides ResolveTenant to read the header
// instead. The id never comes from the path, yet Resolve cannot see that and fails closed.
type overridingPathResolver struct {
	*PathResolver
	header *HeaderResolver
}

func (o overridingPathResolver) ResolveTenant(ctx context.Context, req *http.Request) (string, error) {
	return o.header.ResolveTenant(ctx, req)
}

// wrappingResolver is a consumer type wrapping a framework resolver.
type wrappingResolver struct{ inner TenantResolver }

func (w wrappingResolver) ResolveTenant(ctx context.Context, req *http.Request) (string, error) {
	return w.inner.ResolveTenant(ctx, req)
}

// provenanceRequest carries a header tenant, a subdomain tenant and a path tenant at once, so
// each case shows which member won.
func provenanceRequest(t *testing.T, withHeader bool) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://"+provenanceHostTenant+"."+testDomain+provenanceTenantPath, http.NoBody)
	require.NoError(t, err)
	if withHeader {
		req.Header.Set(tenantIDHeader, provenanceHeaderTenant)
	}
	return req
}

func TestResolveReportsPathProvenance(t *testing.T) {
	header := &HeaderResolver{}
	subdomain := &SubdomainResolver{RootDomain: testDomain}
	path := &PathResolver{Segment: 2}
	untrusted := &SubdomainResolver{RootDomain: testDomain, TrustProxies: true}
	headerThenPath := &CompositeResolver{Resolvers: []TenantResolver{header, path}}
	globexOnly := regexp.MustCompile(`^g`)
	// A consumer resolver answering an id together with an error.
	consumerFailure := &stubResolver{tenant: provenanceHeaderTenant, err: errConsumerResolver}
	embeddedPath := embeddingPathResolver{path}
	overriddenPath := overridingPathResolver{PathResolver: path, header: header}

	tests := []struct {
		name         string
		resolver     TenantResolver
		noHeader     bool
		wantID       string
		wantFromPath bool
		wantErr      error
	}{
		{name: "nil_resolver", resolver: nil, wantErr: ErrTenantResolutionFailed},
		{name: "header", resolver: header, wantID: provenanceHeaderTenant},
		{name: "header_missing", resolver: header, noHeader: true, wantErr: ErrTenantResolutionFailed},
		{name: "typed_nil_header", resolver: (*HeaderResolver)(nil), wantErr: ErrTenantResolutionFailed},
		{name: "subdomain", resolver: subdomain, wantID: provenanceHostTenant},
		{name: "subdomain_untrusted_forwarded_host", resolver: untrusted, wantErr: ErrUntrustedForwardedHost},
		{name: "path", resolver: path, wantID: provenancePathTenant, wantFromPath: true},
		{name: "path_segment_missing", resolver: &PathResolver{Segment: 9}, wantErr: ErrTenantResolutionFailed},
		{name: "path_prefix_miss", resolver: &PathResolver{Segment: 2, Prefix: "/other"}, wantErr: ErrTenantResolutionFailed},
		{name: "composite_header_wins", resolver: headerThenPath, wantID: provenanceHeaderTenant},
		{name: "composite_path_wins_after_header_miss", resolver: headerThenPath, noHeader: true, wantID: provenancePathTenant, wantFromPath: true},
		{name: "composite_path_wins_after_regex_rejects_header", resolver: &CompositeResolver{Resolvers: []TenantResolver{header, path}, TenantRegex: globexOnly}, wantID: provenancePathTenant, wantFromPath: true},
		{name: "composite_path_first_wins", resolver: &CompositeResolver{Resolvers: []TenantResolver{path, header}}, wantID: provenancePathTenant, wantFromPath: true},
		{name: "composite_subdomain_wins", resolver: &CompositeResolver{Resolvers: []TenantResolver{nil, subdomain, path}}, wantID: provenanceHostTenant},
		{name: "composite_untrusted_host_is_terminal", resolver: &CompositeResolver{Resolvers: []TenantResolver{untrusted, path}}, wantErr: ErrUntrustedForwardedHost},
		{name: "composite_nested_path_wins", resolver: &CompositeResolver{Resolvers: []TenantResolver{headerThenPath}}, noHeader: true, wantID: provenancePathTenant, wantFromPath: true},
		{name: "composite_nested_header_wins", resolver: &CompositeResolver{Resolvers: []TenantResolver{headerThenPath}}, wantID: provenanceHeaderTenant},
		{name: "composite_all_members_fail", resolver: &CompositeResolver{Resolvers: []TenantResolver{header, &PathResolver{Segment: 9}}}, noHeader: true, wantErr: ErrTenantResolutionFailed},
		{name: "composite_typed_nil", resolver: (*CompositeResolver)(nil), wantErr: ErrTenantResolutionFailed},
		{name: "composite_consumer_member_wins", resolver: &CompositeResolver{Resolvers: []TenantResolver{embeddingHeaderResolver{header}, path}}, wantID: provenanceHeaderTenant, wantFromPath: true},
		{name: "composite_consumer_member_error_falls_through", resolver: &CompositeResolver{Resolvers: []TenantResolver{consumerFailure, header}}, wantID: provenanceHeaderTenant},
		{name: "validating_header", resolver: &ValidatingResolver{Resolver: header}, wantID: provenanceHeaderTenant},
		{name: "validating_path", resolver: &ValidatingResolver{Resolver: path, TenantRegex: globexOnly}, wantID: provenancePathTenant, wantFromPath: true},
		{name: "validating_regex_rejects_path", resolver: &ValidatingResolver{Resolver: path, TenantRegex: regexp.MustCompile(`^a`)}, wantErr: ErrTenantResolutionFailed},
		{name: "validating_inner_error_propagates", resolver: &ValidatingResolver{Resolver: untrusted}, wantErr: ErrUntrustedForwardedHost},
		{name: "validating_nil_inner", resolver: &ValidatingResolver{}, wantErr: ErrTenantResolutionFailed},
		{name: "validating_typed_nil", resolver: (*ValidatingResolver)(nil), wantErr: ErrTenantResolutionFailed},
		{name: "validating_composite_path_wins", resolver: &ValidatingResolver{Resolver: headerThenPath}, noHeader: true, wantID: provenancePathTenant, wantFromPath: true},
		{name: "validating_embedding_consumer", resolver: &ValidatingResolver{Resolver: embeddingHeaderResolver{header}}, wantID: provenanceHeaderTenant, wantFromPath: true},
		{name: "embedding_header_consumer_type", resolver: embeddingHeaderResolver{header}, wantID: provenanceHeaderTenant, wantFromPath: true},
		{name: "embedding_header_consumer_type_pointer", resolver: &embeddingHeaderResolver{header}, wantID: provenanceHeaderTenant, wantFromPath: true},
		{name: "wrapping_subdomain_consumer_type", resolver: wrappingResolver{subdomain}, wantID: provenanceHostTenant, wantFromPath: true},
		{name: "consumer_type_error_drops_id", resolver: consumerFailure, wantErr: errConsumerResolver},
		{name: "embedding_path_consumer_type", resolver: embeddedPath, wantID: provenancePathTenant, wantFromPath: true},
		{name: "embedding_path_consumer_type_pointer", resolver: &embeddedPath, wantID: provenancePathTenant, wantFromPath: true},
		{name: "embedding_path_consumer_type_segment_missing", resolver: embeddingPathResolver{&PathResolver{Segment: 9}}, wantErr: ErrTenantResolutionFailed},
		{name: "embedding_path_override_reads_header", resolver: overriddenPath, wantID: provenanceHeaderTenant, wantFromPath: true},
		{name: "embedding_path_override_header_missing", resolver: overriddenPath, noHeader: true, wantErr: ErrTenantResolutionFailed},
		{name: "composite_embedding_path_member_wins", resolver: &CompositeResolver{Resolvers: []TenantResolver{header, embeddedPath}}, noHeader: true, wantID: provenancePathTenant, wantFromPath: true},
		{name: "composite_header_beats_embedding_path_member", resolver: &CompositeResolver{Resolvers: []TenantResolver{header, embeddedPath}}, wantID: provenanceHeaderTenant},
		{name: "validating_embedding_path", resolver: &ValidatingResolver{Resolver: embeddedPath, TenantRegex: globexOnly}, wantID: provenancePathTenant, wantFromPath: true},
		{name: "validating_path_override", resolver: &ValidatingResolver{Resolver: overriddenPath}, wantID: provenanceHeaderTenant, wantFromPath: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := provenanceRequest(t, !tt.noHeader)
			if errors.Is(tt.wantErr, ErrUntrustedForwardedHost) {
				req = forwardedHostRequest("198.51.100.7:443", provenanceHostTenant+"."+testDomain)
			}

			gotID, gotFromPath, err := Resolve(context.Background(), tt.resolver, req)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Empty(t, gotID)
				assert.False(t, gotFromPath, "fromPath must be false on error")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantID, gotID)
				assert.Equal(t, tt.wantFromPath, gotFromPath)
			}
			assertResolveParity(t, tt.resolver, req, gotID, gotFromPath, tt.wantErr)
		})
	}
}

// assertResolveParity checks that ResolveTenant answers what Resolve answered, and that
// MayReadPath covers every path-won resolve.
func assertResolveParity(t *testing.T, resolver TenantResolver, req *http.Request, gotID string, gotFromPath bool, wantErr error) {
	t.Helper()
	if gotFromPath {
		assert.True(t, MayReadPath(resolver), "MayReadPath must cover every path-won resolve")
	}
	if resolver == nil {
		return
	}
	parityID, parityErr := resolver.ResolveTenant(context.Background(), req)
	require.ErrorIs(t, parityErr, wantErr, "ResolveTenant parity")
	if parityErr == nil {
		assert.Equal(t, parityID, gotID, "ResolveTenant parity")
	}
}

func TestMayReadPath(t *testing.T) {
	header := &HeaderResolver{}
	subdomain := &SubdomainResolver{RootDomain: testDomain}
	path := &PathResolver{Segment: 1}

	tests := []struct {
		name     string
		resolver TenantResolver
		want     bool
	}{
		{name: "nil_resolver", resolver: nil, want: false},
		{name: "header", resolver: header, want: false},
		{name: "typed_nil_header", resolver: (*HeaderResolver)(nil), want: false},
		{name: "subdomain", resolver: subdomain, want: false},
		{name: "path", resolver: path, want: true},
		{name: "typed_nil_path", resolver: (*PathResolver)(nil), want: true},
		{name: "composite_header_and_subdomain", resolver: &CompositeResolver{Resolvers: []TenantResolver{header, nil, subdomain}}, want: false},
		{name: "composite_with_path_last", resolver: &CompositeResolver{Resolvers: []TenantResolver{header, subdomain, path}}, want: true},
		{name: "composite_with_path_first", resolver: &CompositeResolver{Resolvers: []TenantResolver{path, header, subdomain}}, want: true},
		{name: "composite_empty", resolver: &CompositeResolver{}, want: false},
		{name: "composite_typed_nil", resolver: (*CompositeResolver)(nil), want: false},
		{name: "composite_nested_path", resolver: &CompositeResolver{Resolvers: []TenantResolver{header, &CompositeResolver{Resolvers: []TenantResolver{path}}}}, want: true},
		{name: "composite_nested_header_only", resolver: &CompositeResolver{Resolvers: []TenantResolver{&CompositeResolver{Resolvers: []TenantResolver{header}}}}, want: false},
		{name: "composite_consumer_member", resolver: &CompositeResolver{Resolvers: []TenantResolver{header, wrappingResolver{header}}}, want: true},
		{name: "validating_header", resolver: &ValidatingResolver{Resolver: header}, want: false},
		{name: "validating_path", resolver: &ValidatingResolver{Resolver: path}, want: true},
		{name: "validating_nil_inner", resolver: &ValidatingResolver{}, want: false},
		{name: "validating_typed_nil", resolver: (*ValidatingResolver)(nil), want: false},
		{name: "validating_composite_with_path", resolver: &ValidatingResolver{Resolver: &CompositeResolver{Resolvers: []TenantResolver{subdomain, path}}}, want: true},
		{name: "embedding_header_consumer_type", resolver: embeddingHeaderResolver{header}, want: true},
		{name: "wrapping_subdomain_consumer_type", resolver: wrappingResolver{subdomain}, want: true},
		{name: "embedding_path_consumer_type", resolver: embeddingPathResolver{path}, want: true},
		{name: "embedding_path_override_reads_header", resolver: overridingPathResolver{PathResolver: path, header: header}, want: true},
		{name: "composite_header_and_embedding_path", resolver: &CompositeResolver{Resolvers: []TenantResolver{header, subdomain, embeddingPathResolver{path}}}, want: true},
		{name: "validating_embedding_path", resolver: &ValidatingResolver{Resolver: embeddingPathResolver{path}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MayReadPath(tt.resolver))
		})
	}
}

// TestMayReadPathFailsClosedOnCycle pins that a resolver tree reachable from itself, built
// only from header and subdomain leaves, answers true (fail closed) instead of recursing
// without end, while a member shared by two branches, which is no cycle, keeps its answer.
func TestMayReadPathFailsClosedOnCycle(t *testing.T) {
	header := &HeaderResolver{}
	subdomain := &SubdomainResolver{RootDomain: testDomain}

	selfComposite := &CompositeResolver{Resolvers: []TenantResolver{header}}
	selfComposite.Resolvers = append(selfComposite.Resolvers, selfComposite)

	outer := &CompositeResolver{Resolvers: []TenantResolver{subdomain}}
	validating := &ValidatingResolver{Resolver: outer}
	outer.Resolvers = append(outer.Resolvers, validating)

	selfValidating := &ValidatingResolver{}
	selfValidating.Resolver = selfValidating

	shared := &CompositeResolver{Resolvers: []TenantResolver{header}}
	diamond := &CompositeResolver{Resolvers: []TenantResolver{shared, &ValidatingResolver{Resolver: shared}}}

	tests := []struct {
		name     string
		resolver TenantResolver
		want     bool
	}{
		{name: "composite_contains_itself", resolver: selfComposite, want: true},
		{name: "composite_validating_loop", resolver: outer, want: true},
		{name: "validating_loop_entered_from_validating", resolver: validating, want: true},
		{name: "validating_wraps_itself", resolver: selfValidating, want: true},
		{name: "shared_member_is_not_a_cycle", resolver: diamond, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MayReadPath(tt.resolver))
		})
	}
}

// Tripwire guard (CONTEXT.md § Testing), ADR-026: base == head. Resolve costs exactly what
// its members' leaf ResolveTenant calls cost, run by hand, on a header win and on a composite
// path win, so neither provenance nor the composite's routing through Resolve adds an alloc.
func TestResolveAllocsStable(t *testing.T) {
	if racedetect.Enabled {
		t.Skip("testing.AllocsPerRun is unreliable under -race; alloc baseline enforced by make test-alloc")
	}
	header := &HeaderResolver{}
	path := &PathResolver{Segment: 2}
	tests := []struct {
		name     string
		resolver TenantResolver
		leaves   []TenantResolver
		noHeader bool
	}{
		{name: "header_win", resolver: header, leaves: []TenantResolver{header}},
		{name: "composite_path_win", resolver: &CompositeResolver{Resolvers: []TenantResolver{header, path}}, leaves: []TenantResolver{header, path}, noHeader: true},
		{name: "validating_header_win", resolver: &ValidatingResolver{Resolver: header}, leaves: []TenantResolver{header}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := provenanceRequest(t, !tt.noHeader)
			ctx := context.Background()

			base := testing.AllocsPerRun(100, func() {
				for _, leaf := range tt.leaves {
					_, _ = leaf.ResolveTenant(ctx, req)
				}
			})
			head := testing.AllocsPerRun(100, func() {
				if _, _, err := Resolve(ctx, tt.resolver, req); err != nil {
					t.Fatal(err)
				}
			})

			assert.InDelta(t, base, head, 0, "Resolve allocs/op must equal its leaf resolvers'")
		})
	}
}
