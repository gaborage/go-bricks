package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/auth"
	authtesting "github.com/gaborage/go-bricks/auth/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/server"
)

type familyOrdersReq struct{}

type familyOrdersResp struct {
	Tenant string `json:"tenant"`
}

// TestTenantOwnedGroupServesSmallestFamily pins the smallest family from ADR-164, end to end on
// a real listener. A bearer token's tid claim names the tenant through auth.Middleware and
// auth.TenantClaim, while the root chain keeps its header tenant seat for every other route.
func TestTenantOwnedGroupServesSmallestFamily(t *testing.T) {
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	iss := authtesting.NewIssuer()
	verifier, err := auth.NewVerifierWithResolver(auth.Config{
		Issuer:     iss.IssuerURL(),
		Audience:   []string{iss.Audience()},
		Algorithms: []string{auth.AlgRS256},
		Leeway:     30 * time.Second,
	}, nil, auth.NewStaticKeyResolver(iss.PublicKeys()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = verifier.Close() })

	cfg := &config.Config{App: config.AppConfig{Name: "tenant-family", Env: "test", Version: "1.0.0"}}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Timeout.Read = 5 * time.Second
	cfg.Server.Timeout.Write = 5 * time.Second
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypeHeader
	srv := server.NewWithOptions(cfg, logger.New("disabled", true), server.Options{TenantChecker: testutil.TenantSet{"acme": {}}})

	hr := server.NewHandlerRegistry(cfg)
	whoami := func(_ familyOrdersReq, c server.HandlerContext) (familyOrdersResp, server.IAPIError) {
		tenantID, _ := multitenant.GetTenant(c.RequestContext())
		return familyOrdersResp{Tenant: tenantID}, nil
	}
	partner := server.TenantOwnedGroup(srv.ModuleGroup(), "/partner/v2", server.TenantPolicy{
		Authenticate: []server.MiddlewareFunc{auth.Middleware(verifier)},
		Tenant:       auth.TenantClaim("tid"),
	})
	server.GET(hr, partner, "/orders", whoami)
	server.GET(hr, srv.ModuleGroup(), "/orders", whoami)
	base := testutil.StartListener(t, srv)

	acme := "Bearer " + iss.Mint(authtesting.Claims{Extra: map[string]any{"tid": "acme"}})
	tests := []struct {
		name       string
		path       string
		bearer     string
		seatTenant string
		wantStatus int
		wantTenant string
	}{
		{name: "claim_names_tenant", path: "/partner/v2/orders", bearer: acme, wantStatus: http.StatusOK, wantTenant: "acme"},
		{name: "seat_header_ignored", path: "/partner/v2/orders", bearer: acme, seatTenant: "globex", wantStatus: http.StatusOK, wantTenant: "acme"},
		{name: "no_credential_denied", path: "/partner/v2/orders", wantStatus: http.StatusUnauthorized},
		{
			name: "claim_absent_denied", path: "/partner/v2/orders", wantStatus: http.StatusForbidden,
			bearer: "Bearer " + iss.Mint(authtesting.Claims{}),
		},
		{name: "unknown_sub_path_after_tenant", path: "/partner/v2/nowhere", bearer: acme, wantStatus: http.StatusNotFound},
		{name: "unknown_sub_path_anonymous_denied", path: "/partner/v2/nowhere", wantStatus: http.StatusUnauthorized},
		{name: "root_seat_resolves_other_routes", path: "/orders", seatTenant: "globex", wantStatus: http.StatusOK, wantTenant: "globex"},
		{name: "root_seat_refuses_other_routes", path: "/orders", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+tc.path, http.NoBody)
			require.NoError(t, err)
			if tc.bearer != "" {
				req.Header.Set("Authorization", tc.bearer)
			}
			if tc.seatTenant != "" {
				req.Header.Set(server.HeaderXTenantID, tc.seatTenant)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, tc.wantStatus, resp.StatusCode, string(body))
			switch {
			case tc.wantTenant != "":
				var envelope struct {
					Data familyOrdersResp `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &envelope), string(body))
				assert.Equal(t, tc.wantTenant, envelope.Data.Tenant)
			case tc.wantStatus == http.StatusUnauthorized || tc.wantStatus == http.StatusForbidden:
				assert.Empty(t, body, "Deny's default answer is bodyless")
			}
			if tc.wantStatus == http.StatusUnauthorized {
				assert.NotEmpty(t, resp.Header.Get("WWW-Authenticate"), "DenyBodyless keeps auth.Middleware's challenge")
			}
		})
	}
}
