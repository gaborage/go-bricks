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

// faultRecorder is a family Deny that records each fault, then answers bodyless.
type faultRecorder struct{ faults []error }

func (r *faultRecorder) deny(c server.HandlerContext, fault error) error {
	r.faults = append(r.faults, fault)
	return server.DenyBodyless(c, fault)
}

func newTenantClaimContext(ctx context.Context, authorization string) (server.HandlerContext, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/partner/v2/orders", http.NoBody)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return server.NewHandlerContextForTest(rec, req, &config.Config{}), rec
}

func principalWith(ctx context.Context, claims map[string]any) context.Context {
	return ContextWithPrincipal(ctx, Principal{Subject: "client-1", Claims: claims})
}

func TestTenantClaimResolvesTenant(t *testing.T) {
	t.Run("claim_present", func(t *testing.T) {
		deny := &faultRecorder{}
		c, _ := newTenantClaimContext(principalWith(t.Context(), map[string]any{tenantClaimName: "acme"}), "")

		id, denied, err := server.TenantPolicy{Tenant: TenantClaim(tenantClaimName), Deny: deny.deny}.ResolveForTest(c)

		require.NoError(t, err)
		assert.False(t, denied)
		assert.Equal(t, "acme", id)
		published, ok := multitenant.GetTenant(c.RequestContext())
		assert.True(t, ok)
		assert.Equal(t, "acme", published)
		assert.Empty(t, deny.faults)
	})

	t.Run("no_principal_is_server_fault", func(t *testing.T) {
		deny := &faultRecorder{}
		c, _ := newTenantClaimContext(t.Context(), "")

		id, denied, err := server.TenantPolicy{Tenant: TenantClaim(tenantClaimName), Deny: deny.deny}.ResolveForTest(c)

		require.ErrorIs(t, err, errNoPrincipal)
		require.NotErrorIs(t, err, multitenant.ErrTenantResolutionFailed)
		require.NotErrorIs(t, err, multitenant.ErrInvalidTenantID)
		assert.False(t, denied)
		assert.Empty(t, id)
		assert.Empty(t, deny.faults)
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
			deny := &faultRecorder{}
			c, rec := newTenantClaimContext(principalWith(t.Context(), tc.claims), "")

			id, denied, err := server.TenantPolicy{Tenant: TenantClaim(tenantClaimName), Deny: deny.deny}.ResolveForTest(c)

			require.NoError(t, err)
			assert.True(t, denied)
			assert.Empty(t, id)
			require.Len(t, deny.faults, 1)
			fault := deny.faults[0]
			require.ErrorIs(t, fault, tc.wantIs)
			require.NotErrorIs(t, fault, tc.wantNotIs)
			if tc.value != "" {
				assert.NotContains(t, fault.Error(), tc.value, "a fault never carries the claim value")
			}
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Empty(t, rec.Body.String())
			_, published := multitenant.GetTenant(c.RequestContext())
			assert.False(t, published)
		})
	}
}

func TestTenantClaimReadsPrincipalAttachedByMiddleware(t *testing.T) {
	iss := authtesting.NewIssuer()
	v, err := NewVerifierWithResolver(verifierConfig(iss), logger.New("debug", false), NewStaticKeyResolver(iss.PublicKeys()))
	require.NoError(t, err)
	token := iss.Mint(authtesting.Claims{Extra: map[string]any{tenantClaimName: "acme"}})
	c, _ := newTenantClaimContext(t.Context(), "Bearer "+token)

	id, denied, err := server.TenantPolicy{
		Authenticate: []server.MiddlewareFunc{Middleware(v)},
		Tenant:       TenantClaim(tenantClaimName),
	}.ResolveForTest(c)

	require.NoError(t, err)
	assert.False(t, denied)
	assert.Equal(t, "acme", id)
}

func TestTenantClaimPanicsOnEmptyClaimName(t *testing.T) {
	assert.PanicsWithValue(t, "auth.TenantClaim: empty claim name", func() { TenantClaim("") })
}
