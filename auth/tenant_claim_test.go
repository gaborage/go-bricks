package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtesting "github.com/gaborage/go-bricks/auth/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
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

	clientFaults := []struct {
		name      string
		claims    map[string]any
		wantIs    error
		wantNotIs error
		value     string
	}{
		{name: "claim_absent_is_client_fault", claims: map[string]any{"sub": "client-1"}, wantIs: multitenant.ErrTenantResolutionFailed, wantNotIs: multitenant.ErrInvalidTenantID},
		{name: "claim_empty_is_client_fault", claims: map[string]any{tenantClaimName: ""}, wantIs: multitenant.ErrTenantResolutionFailed, wantNotIs: multitenant.ErrInvalidTenantID},
		{name: "claim_not_string_is_client_fault", claims: map[string]any{tenantClaimName: float64(4242)}, wantIs: multitenant.ErrTenantResolutionFailed, wantNotIs: multitenant.ErrInvalidTenantID, value: "4242"},
		{name: "claim_fails_grammar_is_client_fault", claims: map[string]any{tenantClaimName: "Acme_Corp"}, wantIs: multitenant.ErrInvalidTenantID, wantNotIs: multitenant.ErrTenantResolutionFailed, value: "Acme_Corp"},
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
	v, err := NewVerifierWithResolver(verifierConfig(iss), logger.New("debug", false), NewStaticKeyResolver(iss.PublicKeys()))
	require.NoError(t, err)
	token := iss.Mint(authtesting.Claims{Extra: map[string]any{tenantClaimName: "acme"}})

	r := resolveTenantClaim(newAuthRequest(t.Context(), "Bearer "+token, true), Middleware(v))

	require.NoError(t, r.err)
	assert.False(t, r.denied)
	assert.Equal(t, "acme", r.id)
}

func TestTenantClaimPanicsOnEmptyClaimName(t *testing.T) {
	assert.PanicsWithValue(t, "auth.TenantClaim: empty claim name", func() { TenantClaim("") })
}
