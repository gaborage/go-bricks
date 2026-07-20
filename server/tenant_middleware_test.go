package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/multitenant"
)

// newTenantTestEcho builds an *echo.Echo wired with customErrorHandler so an
// IAPIError returned by tenantMiddlewareEcho (e.g. *BadRequestError) renders
// its real HTTP status instead of Echo's default 500 for non-*echo.HTTPError
// errors. Mirrors the pattern in errors_test.go.
func newTenantTestEcho() *echo.Echo {
	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: config.EnvDevelopment}}
	e.HTTPErrorHandler = func(c *echo.Context, err error) {
		customErrorHandler(c, err, cfg, &testLogger{})
	}
	return e
}

// fixedTenantResolver is a minimal multitenant.TenantResolver test double that
// always returns the configured (tenantID, err) pair, regardless of the request.
type fixedTenantResolver struct {
	tenantID string
	err      error
}

func (r *fixedTenantResolver) ResolveTenant(_ context.Context, _ *http.Request) (string, error) {
	return r.tenantID, r.err
}

// TestTenantMiddlewareLogsRejection verifies a 400 tenant-resolution rejection
// emits one WARN through the provided framework logger, carrying the path and
// status. This is the audit trail for the observability-off blind spot:
// tenantMiddlewareEcho is registered outer to the access logger
// (server/middleware.go) and never calls next() on reject, so without this
// WARN the request leaves zero server-side trail. capturingLogger is defined
// in cors_test.go.
func TestTenantMiddlewareLogsRejection(t *testing.T) {
	tests := []struct {
		name     string
		resolver multitenant.TenantResolver
	}{
		{name: "empty_tenant", resolver: &fixedTenantResolver{tenantID: "", err: nil}},
		{name: "resolver_error", resolver: &fixedTenantResolver{tenantID: "tenant-canary", err: errors.New("boom")}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capturer := &capturingLogger{}
			e := newTenantTestEcho()
			e.Use(tenantMiddlewareEcho(tc.resolver, nil, capturer))
			e.GET("/tenant-check", func(c *echo.Context) error {
				return c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/tenant-check", http.NoBody)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)

			require.Len(t, capturer.warns, 1, "exactly one WARN per rejected request")
			captured := strings.Join(capturer.warns, "\n")
			assert.Contains(t, captured, "method=GET")
			assert.Contains(t, captured, "path=/tenant-check")
			assert.Contains(t, captured, "client=192.0.2.1")
			assert.Contains(t, captured, "status=400")
			assert.NotContains(t, captured, "tenant=", "no resolved tenant field on the reject path")
			assert.NotContains(t, captured, "tenant-canary", "a resolver-provided tenant must never be logged on reject")
		})
	}
}

// TestTenantMiddlewareNilLoggerDoesNotPanic verifies the nil-logger path
// (public TenantMiddleware construction, which threads nil through to
// tenantMiddlewareEcho) still rejects with 400 and does not panic — guards
// against a future refactor reintroducing an unconditional l.Warn() call.
func TestTenantMiddlewareNilLoggerDoesNotPanic(t *testing.T) {
	resolver := &fixedTenantResolver{tenantID: "", err: nil}
	e := newTenantTestEcho()
	e.Use(tenantMiddlewareEcho(resolver, nil, nil))
	e.GET("/tenant-check", func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/tenant-check", http.NoBody)
	rec := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		e.ServeHTTP(rec, req)
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
