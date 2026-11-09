package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/multitenant"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const (
	tenantPolicyTestTemplate = "/partner/v2/orders/:id"
	tenantPolicyTestPath     = "/partner/v2/orders/ord-secret-9"
	secretTenantValue        = "secret-tenant-value"
)

func newTenantPolicyTestContext(t *testing.T) (HandlerContext, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tenantPolicyTestPath, http.NoBody)
	req.Header.Set(HeaderXTenantID, secretTenantValue)
	rec := httptest.NewRecorder()
	return NewHandlerContextForTest(rec, req, &config.Config{}, WithRouteTemplate(tenantPolicyTestTemplate)), rec
}

func resolvingPolicy(tenantID string, err error) TenantPolicy {
	return TenantPolicy{Tenant: func(HandlerContext) (string, error) { return tenantID, err }}
}

func denyMustNotRun(t *testing.T) DenyFunc {
	return func(HandlerContext, error) error {
		t.Error("Deny ran")
		return nil
	}
}

func TestTenantPolicyClassifiesFaults(t *testing.T) {
	const (
		client = iota
		noTenant
		unchanged
	)
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"status_401_is_client", NewUnauthorizedError("x"), client},
		{"status_403_is_client", NewForbiddenError("x"), client},
		{"status_500_is_server", NewInternalServerError("x"), unchanged},
		{"echo_status_coder_4xx_is_client", echo.ErrNotFound, client},
		{"echo_http_error_413_is_framework", echo.NewHTTPError(http.StatusRequestEntityTooLarge, "x"), unchanged},
		{"api_error_413_is_framework", NewBaseAPIError(statusToErrorCode(http.StatusRequestEntityTooLarge), "x", http.StatusRequestEntityTooLarge), unchanged},
		{"max_bytes_error_wrapped_in_resolution_failed_is_framework", fmt.Errorf("%w: %w", multitenant.ErrTenantResolutionFailed, &http.MaxBytesError{Limit: 1}), unchanged},
		{"wrapped_tenant_resolution_failed_is_client", fmt.Errorf("lookup: %w", multitenant.ErrTenantResolutionFailed), client},
		{"wrapped_invalid_tenant_id_is_client", fmt.Errorf("claim: %w", multitenant.ErrInvalidTenantID), client},
		{"err_unknown_tenant_is_client", multitenant.ErrUnknownTenant, client},
		{"unknown_tenant_error_400_is_client", NewUnknownTenantError(multitenant.ErrUnknownTenant), client},
		{"deadline_wrapped_in_resolution_failed_is_framework", fmt.Errorf("%w: %w", multitenant.ErrTenantResolutionFailed, context.DeadlineExceeded), unchanged},
		{"canceled_wrapped_in_resolution_failed_is_framework", fmt.Errorf("%w: %w", multitenant.ErrTenantResolutionFailed, context.Canceled), unchanged},
		{"err_no_tenant_wrapped_in_resolution_failed_is_server", fmt.Errorf("%w: %w", multitenant.ErrTenantResolutionFailed, multitenant.ErrNoTenant), noTenant},
		{"err_no_tenant_with_4xx_status_is_server", echo.NewHTTPError(http.StatusForbidden, "x").Wrap(multitenant.ErrNoTenant), noTenant},
		{"plain_error_is_server", errors.New("directory down"), unchanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTenantPolicyTestContext(t)
			var got error
			p := resolvingPolicy("", tt.err)
			p.Deny = func(dc HandlerContext, fault error) error {
				got = fault
				return dc.ectx.NoContent(http.StatusTeapot)
			}

			id, denied, err := p.ResolveForTest(c)

			assert.Empty(t, id)
			switch tt.want {
			case client:
				assert.True(t, denied)
				require.Same(t, tt.err, got)
				require.NoError(t, err)
			case noTenant:
				assert.False(t, denied)
				require.NoError(t, got, "Deny must not run")
				var fault *tenantFaultError
				require.ErrorAs(t, err, &fault)
				require.ErrorIs(t, err, multitenant.ErrNoTenant)
				require.NotErrorIs(t, err, multitenant.ErrTenantResolutionFailed)
				assert.Equal(t, http.StatusInternalServerError, fault.HTTPStatus())
			default:
				assert.False(t, denied)
				require.NoError(t, got, "Deny must not run")
				require.Same(t, tt.err, err)
			}
		})
	}
}

func TestDenyBodylessStatus(t *testing.T) {
	const challenge = `Bearer realm="x"`
	tests := []struct {
		name      string
		fault     error
		challenge string
		want      int
	}{
		{"auth_middleware_401_keeps_challenge", NewUnauthorizedError("x"), challenge, http.StatusUnauthorized},
		{"role_step_403_kept", NewForbiddenError("x"), "", http.StatusForbidden},
		{"status_400_kept", NewBadRequestError("x"), "", http.StatusBadRequest},
		{"tenant_resolution_failed_gets_403", fmt.Errorf("lookup: %w", multitenant.ErrTenantResolutionFailed), "", http.StatusForbidden},
		{"invalid_tenant_id_gets_403", multitenant.ValidateTenantID("UPPER"), "", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := newTenantPolicyTestContext(t)
			if tt.challenge != "" {
				c.ResponseWriter().Header().Set(echo.HeaderWWWAuthenticate, tt.challenge)
			}

			require.NoError(t, DenyBodyless(c, tt.fault))

			assert.Equal(t, tt.want, rec.Code)
			assert.Empty(t, rec.Body.String())
			assert.Equal(t, tt.challenge, rec.Header().Get(echo.HeaderWWWAuthenticate))
		})
	}
}

func TestTenantPolicyResolveForTestReportsDenied(t *testing.T) {
	t.Run("resolved_returns_tenant", func(t *testing.T) {
		c, _ := newTenantPolicyTestContext(t)
		id, denied, err := resolvingPolicy("acme", nil).ResolveForTest(c)
		require.NoError(t, err)
		assert.False(t, denied)
		assert.Equal(t, "acme", id)
		published, ok := multitenant.GetTenant(c.RequestContext())
		assert.True(t, ok)
		assert.Equal(t, "acme", published)
	})

	t.Run("client_fault_denied", func(t *testing.T) {
		c, rec := newTenantPolicyTestContext(t)
		_, denied, err := resolvingPolicy("", fmt.Errorf("lookup: %w", multitenant.ErrTenantResolutionFailed)).ResolveForTest(c)
		require.NoError(t, err)
		assert.True(t, denied)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Empty(t, rec.Body.String())
	})

	t.Run("server_fault_not_denied", func(t *testing.T) {
		c, _ := newTenantPolicyTestContext(t)
		plain := errors.New("directory down")
		_, denied, err := resolvingPolicy("", plain).ResolveForTest(c)
		require.Same(t, plain, err)
		assert.False(t, denied)
	})

	t.Run("grammar_backstop_not_denied", func(t *testing.T) {
		c, _ := newTenantPolicyTestContext(t)
		p := resolvingPolicy("Bad_ID", nil)
		p.Deny = denyMustNotRun(t)

		id, denied, err := p.ResolveForTest(c)

		var fault *tenantFaultError
		require.ErrorAs(t, err, &fault)
		require.NotErrorIs(t, err, multitenant.ErrInvalidTenantID)
		require.EqualError(t, err, "server: tenant-owned family fault: tenant ID fails the tenant-ID grammar")
		assert.False(t, denied)
		assert.Empty(t, id)
		_, ok := multitenant.GetTenant(c.RequestContext())
		assert.False(t, ok)
	})
}

func TestTenantPolicyResolveForTestPanicsOnNilTenant(t *testing.T) {
	c, _ := newTenantPolicyTestContext(t)
	stepRan := false
	p := TenantPolicy{Authenticate: []MiddlewareFunc{func(_ HandlerContext, next func() error) error {
		stepRan = true
		return next()
	}}}

	assert.PanicsWithValue(t, "server: TenantPolicy.Tenant is nil", func() { _, _, _ = p.ResolveForTest(c) })
	assert.False(t, stepRan)
}

func TestTenantPolicyRunsAuthenticateBeforeTenant(t *testing.T) {
	type principalKey struct{}
	c, _ := newTenantPolicyTestContext(t)
	var order []string
	var seen any
	var tenantInNext string
	p := TenantPolicy{
		Authenticate: []MiddlewareFunc{
			func(sc HandlerContext, next func() error) error {
				order = append(order, "authenticate_1")
				sc.SetRequestContext(context.WithValue(sc.RequestContext(), principalKey{}, "principal"))
				nextErr := next()
				order = append(order, "authenticate_1_returned")
				return nextErr
			},
			func(_ HandlerContext, next func() error) error {
				order = append(order, "authenticate_2")
				return next()
			},
		},
		Tenant: func(tc HandlerContext) (string, error) {
			order = append(order, "tenant")
			seen = tc.RequestContext().Value(principalKey{})
			return "acme", nil
		},
	}

	id, denied, err := p.run(c, nil, nil, func() error {
		order = append(order, "next")
		tenantInNext, _ = multitenant.GetTenant(c.RequestContext())
		return nil
	})

	require.NoError(t, err)
	assert.False(t, denied)
	assert.Equal(t, "acme", id)
	assert.Equal(t, []string{"authenticate_1", "authenticate_2", "tenant", "next", "authenticate_1_returned"}, order)
	assert.Equal(t, "principal", seen)
	assert.Equal(t, "acme", tenantInNext)
}

func TestTenantPolicyStampsTraceparentBeforeDeny(t *testing.T) {
	c, _ := newTenantPolicyTestContext(t)
	var traceparent string
	p := resolvingPolicy("", multitenant.ErrTenantResolutionFailed)
	p.Deny = func(dc HandlerContext, _ error) error {
		traceparent = dc.ResponseWriter().Header().Get(gobrickstrace.HeaderTraceParent)
		return dc.ectx.NoContent(http.StatusForbidden)
	}

	_, denied, err := p.ResolveForTest(c)

	require.NoError(t, err)
	assert.True(t, denied)
	assert.NotEmpty(t, traceparent)
}

func TestTenantPolicyDenyWiringFaults(t *testing.T) {
	denyBroke := errors.New("deny broke")
	tests := []struct {
		name   string
		deny   DenyFunc
		reason string
		check  func(t *testing.T, err error)
	}{
		{
			name:   "deny_error_returned",
			deny:   func(HandlerContext, error) error { return denyBroke },
			reason: "deny returned an error",
			check:  func(t *testing.T, err error) { require.Same(t, denyBroke, err) },
		},
		{
			name:   "deny_commits_nothing_gets_500",
			deny:   func(HandlerContext, error) error { return nil },
			reason: "deny returned without committing a response",
			check: func(t *testing.T, err error) {
				var fault *tenantFaultError
				require.ErrorAs(t, err, &fault)
				assert.Equal(t, http.StatusInternalServerError, fault.HTTPStatus())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTenantPolicyTestContext(t)
			tl := &testLogger{}
			p := resolvingPolicy("", fmt.Errorf("tenant %s: %w", secretTenantValue, multitenant.ErrTenantResolutionFailed))
			p.Deny = tt.deny

			id, denied, err := p.run(c, tl, nil, func() error { return nil })

			tt.check(t, err)
			assert.True(t, denied)
			assert.Empty(t, id)
			entries := tl.logEntries()
			require.Len(t, entries, 1)
			assert.Equal(t, "error", entries[0].level)
			assert.Contains(t, entries[0].msg, fmt.Sprintf("reason=%q", tt.reason))
			// The path is maskedPath: this test context has no masking engine, so it fails
			// closed to the bare token; the engine-built rendering is pinned in
			// tenant_family_test.go.
			assert.Contains(t, entries[0].msg, fmt.Sprintf("path=%q", maskToken))
			assert.NotContains(t, entries[0].msg, "status=")
			assert.NotContains(t, entries[0].msg, secretTenantValue)
			assert.NotContains(t, entries[0].msg, "ord-secret-9")
		})
	}
}

func TestTenantPolicyFailsClosedWithoutTenant(t *testing.T) {
	c, _ := newTenantPolicyTestContext(t)
	tl := &testLogger{}
	tenantRan := false
	p := TenantPolicy{
		Authenticate: []MiddlewareFunc{func(HandlerContext, func() error) error { return nil }},
		Tenant: func(HandlerContext) (string, error) {
			tenantRan = true
			return "acme", nil
		},
	}

	id, denied, err := p.run(c, tl, nil, func() error { return nil })

	var fault *tenantFaultError
	require.ErrorAs(t, err, &fault)
	assert.False(t, denied)
	assert.Empty(t, id)
	assert.False(t, tenantRan)
	entries := tl.logEntries()
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].msg, `reason="authenticate chain returned without publishing a tenant"`)
}

func TestTenantPolicyReturnsCommittedStepErrorUntouched(t *testing.T) {
	c, rec := newTenantPolicyTestContext(t)
	tl := &testLogger{}
	stepErr := fmt.Errorf("committed then refused: %w", multitenant.ErrTenantResolutionFailed)
	p := TenantPolicy{
		Authenticate: []MiddlewareFunc{func(sc HandlerContext, _ func() error) error {
			return errors.Join(sc.echoContext().NoContent(http.StatusUnauthorized), stepErr)
		}},
		Tenant: func(HandlerContext) (string, error) { return "acme", nil },
		Deny:   denyMustNotRun(t),
	}

	id, denied, err := p.run(c, tl, nil, func() error { return nil })

	require.ErrorIs(t, err, stepErr)
	assert.False(t, denied)
	assert.Empty(t, id)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Len(t, refusalEntries(tl, reasonStepCommitted), 1)
}

func TestTenantPolicyReturnsNextErrorUntouched(t *testing.T) {
	nextErr := NewBadRequestError("x")
	t.Run("next_error_returned", func(t *testing.T) {
		c, _ := newTenantPolicyTestContext(t)
		p := resolvingPolicy("acme", nil)
		p.Deny = denyMustNotRun(t)

		id, denied, err := p.run(c, nil, nil, func() error { return nextErr })

		require.Same(t, nextErr, err)
		assert.False(t, denied)
		assert.Equal(t, "acme", id)
	})

	t.Run("authenticate_step_wraps_next_error", func(t *testing.T) {
		c, _ := newTenantPolicyTestContext(t)
		var stepErr error
		p := resolvingPolicy("acme", nil)
		p.Authenticate = []MiddlewareFunc{func(_ HandlerContext, next func() error) error {
			stepErr = fmt.Errorf("step: %w", next())
			return stepErr
		}}
		p.Deny = denyMustNotRun(t)

		_, denied, err := p.run(c, nil, nil, func() error { return nextErr })

		require.Same(t, stepErr, err)
		assert.False(t, denied)
	})
}
