package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtesting "github.com/gaborage/go-bricks/auth/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/server"
)

const tenantClaimName = "tid"

// claimResolution is what one TenantClaim policy run observed: the context and
// recorder it ran on, the faults its Deny received, and ResolveForTest's results.
type claimResolution struct {
	c      server.HandlerContext
	rec    *httptest.ResponseRecorder
	faults []error
	id     string
	denied bool
	err    error
}

// resolveTenantClaim runs a TenantClaim policy on req through ResolveForTest. Its Deny
// records each fault, then answers bodyless.
func resolveTenantClaim(req *http.Request, authenticate ...server.MiddlewareFunc) *claimResolution {
	r := &claimResolution{rec: httptest.NewRecorder()}
	r.c = server.NewHandlerContextForTest(r.rec, req, &config.Config{})
	policy := server.TenantPolicy{
		Authenticate: authenticate,
		Tenant:       TenantClaim(tenantClaimName),
		Deny: func(c server.HandlerContext, fault error) error {
			r.faults = append(r.faults, fault)
			return server.DenyBodyless(c, fault)
		},
	}
	r.id, r.denied, r.err = policy.ResolveForTest(r.c)
	return r
}

func principalWith(ctx context.Context, claims map[string]any) context.Context {
	p := testPrincipal()
	p.Claims = claims
	return ContextWithPrincipal(ctx, p)
}

func TestTenantClaimResolvesTenant(t *testing.T) {
	t.Run("claim_present", func(t *testing.T) {
		r := resolveTenantClaim(newAuthRequest(principalWith(t.Context(), map[string]any{tenantClaimName: "acme"}), "", false))

		require.NoError(t, r.err)
		assert.False(t, r.denied)
		assert.Equal(t, "acme", r.id)
		published, ok := multitenant.GetTenant(r.c.RequestContext())
		assert.True(t, ok)
		assert.Equal(t, "acme", published)
		assert.Empty(t, r.faults)
	})

	t.Run("no_principal_is_server_fault", func(t *testing.T) {
		r := resolveTenantClaim(newAuthRequest(t.Context(), "", false))

		require.ErrorIs(t, r.err, errNoPrincipal)
		require.NotErrorIs(t, r.err, multitenant.ErrTenantResolutionFailed)
		require.NotErrorIs(t, r.err, multitenant.ErrInvalidTenantID)
		assert.False(t, r.denied)
		assert.Empty(t, r.id)
		assert.Empty(t, r.faults)
	})

	resolutionFailed, invalidID := multitenant.ErrTenantResolutionFailed, multitenant.ErrInvalidTenantID
	over64 := strings.Repeat("a", 65)
	const grammar = "does not match the tenant-ID grammar"
	clientFaults := []struct {
		name      string
		claims    map[string]any
		wantIs    error
		wantNotIs error
		reason    string
		value     string
	}{
		{
			name: "claim_absent_is_client_fault", claims: map[string]any{"sub": "client-1"},
			wantIs: resolutionFailed, wantNotIs: invalidID, reason: "is absent",
		},
		{
			name: "claim_empty_is_client_fault", claims: map[string]any{tenantClaimName: ""},
			wantIs: resolutionFailed, wantNotIs: invalidID, reason: "is empty",
		},
		{
			name: "claim_not_string_is_client_fault", claims: map[string]any{tenantClaimName: float64(4242)},
			wantIs: resolutionFailed, wantNotIs: invalidID, reason: "is a float64, not a string", value: "4242",
		},
		{
			name: "claim_fails_grammar_is_client_fault", claims: map[string]any{tenantClaimName: "Acme_Corp"},
			wantIs: invalidID, wantNotIs: resolutionFailed, reason: grammar, value: "Acme_Corp",
		},
		{
			name: "claim_null_is_client_fault", claims: map[string]any{tenantClaimName: nil},
			wantIs: resolutionFailed, wantNotIs: invalidID, reason: "is a <nil>, not a string",
		},
		{
			name: "claim_object_is_client_fault", claims: map[string]any{tenantClaimName: map[string]any{"id": "acme"}},
			wantIs: resolutionFailed, wantNotIs: invalidID, reason: "is a map[string]interface {}, not a string", value: "acme",
		},
		{
			name: "claim_with_surrounding_space_is_not_trimmed", claims: map[string]any{tenantClaimName: " acme "},
			wantIs: invalidID, wantNotIs: resolutionFailed, reason: grammar, value: " acme ",
		},
		{
			name: "claim_with_trailing_newline_is_not_trimmed", claims: map[string]any{tenantClaimName: "acme\n"},
			wantIs: invalidID, wantNotIs: resolutionFailed, reason: grammar, value: "acme\n",
		},
		{
			name: "claim_over_64_bytes_is_client_fault", claims: map[string]any{tenantClaimName: over64},
			wantIs: invalidID, wantNotIs: resolutionFailed, reason: grammar, value: over64,
		},
	}
	for _, tc := range clientFaults {
		t.Run(tc.name, func(t *testing.T) {
			r := resolveTenantClaim(newAuthRequest(principalWith(t.Context(), tc.claims), "", false))

			require.NoError(t, r.err)
			assert.True(t, r.denied)
			assert.Empty(t, r.id)
			require.Len(t, r.faults, 1)
			fault := r.faults[0]
			require.ErrorIs(t, fault, tc.wantIs)
			require.NotErrorIs(t, fault, tc.wantNotIs)
			assert.Contains(t, fault.Error(), tc.reason)
			if tc.value != "" {
				assert.NotContains(t, fault.Error(), tc.value, "a fault never carries the claim value")
			}
			assert.Equal(t, http.StatusForbidden, r.rec.Code)
			assert.Empty(t, r.rec.Body.String())
			_, published := multitenant.GetTenant(r.c.RequestContext())
			assert.False(t, published)
		})
	}
}

func TestTenantClaimReadsPrincipalAttachedByMiddleware(t *testing.T) {
	iss := authtesting.NewIssuer()
	v, err := NewVerifierWithResolver(verifierConfig(iss), nil, NewStaticKeyResolver(iss.PublicKeys()))
	require.NoError(t, err)

	t.Run("claim_in_token", func(t *testing.T) {
		token := iss.Mint(authtesting.Claims{Extra: map[string]any{tenantClaimName: "acme"}})

		r := resolveTenantClaim(newAuthRequest(t.Context(), "Bearer "+token, true), Middleware(v))

		require.NoError(t, r.err)
		assert.False(t, r.denied)
		assert.Equal(t, "acme", r.id)
	})

	t.Run("token_without_claim_is_client_fault", func(t *testing.T) {
		token := iss.Mint(authtesting.Claims{})

		r := resolveTenantClaim(newAuthRequest(t.Context(), "Bearer "+token, true), Middleware(v))

		require.NoError(t, r.err)
		assert.True(t, r.denied)
		require.Len(t, r.faults, 1)
		require.ErrorIs(t, r.faults[0], multitenant.ErrTenantResolutionFailed)
		assert.Equal(t, http.StatusForbidden, r.rec.Code)
	})
}

func TestTenantClaimPanicsOnEmptyClaimName(t *testing.T) {
	assert.PanicsWithValue(t, "auth.TenantClaim: empty claim name", func() { TenantClaim("") })
}
