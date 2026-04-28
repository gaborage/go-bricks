package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otelnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/logger"
)

func TestWithJOSELoggerSetsField(t *testing.T) {
	log := logger.New("info", false)
	hr := NewHandlerRegistry(&config.Config{App: config.AppConfig{Env: "development"}}, WithJOSELogger(log))
	assert.NotNil(t, hr.joseLogger)
}

func TestWithJOSETracerSetsField(t *testing.T) {
	tracer := tracenoop.NewTracerProvider().Tracer("test")
	hr := NewHandlerRegistry(&config.Config{App: config.AppConfig{Env: "development"}}, WithJOSETracer(tracer))
	assert.NotNil(t, hr.joseTracer)
}

func TestWithJOSEMeterProviderSetsField(t *testing.T) {
	mp := otelnoop.NewMeterProvider()
	hr := NewHandlerRegistry(&config.Config{App: config.AppConfig{Env: "development"}}, WithJOSEMeterProvider(mp))
	assert.NotNil(t, hr.joseMeterProvider)
}

func TestJOSEAPIErrorContract(t *testing.T) {
	e := &joseAPIError{code: "JOSE_DECRYPT_FAILED", message: "Failed to decrypt", status: 401}
	assert.Equal(t, "JOSE_DECRYPT_FAILED", e.ErrorCode())
	assert.Equal(t, "Failed to decrypt", e.Message())
	assert.Equal(t, 401, e.HTTPStatus())
	assert.Nil(t, e.Details())
	// WithDetails MUST be a no-op to preserve constant-time-generic guarantee.
	assert.Same(t, e, e.WithDetails("anything", "value"))
	assert.Nil(t, e.Details())
}

// joseRouteConfig accessors must be nil-safe — wrap() relies on it for non-JOSE routes
// that still flow through the JOSE-aware code paths (e.g., the error formatter switch).
func TestJOSERouteConfigNilSafe(t *testing.T) {
	var j *joseRouteConfig
	assert.Nil(t, j.inbound())
	assert.Nil(t, j.outbound())
	assert.Nil(t, j.resolver())
	assert.Nil(t, j.obs())
}

func TestJOSERouteConfigPopulated(t *testing.T) {
	p := &jose.Policy{Direction: jose.DirectionInbound, DecryptKid: "k", VerifyKid: "p"}
	r := jositest.NewTestResolver(map[string]any{})
	obs := newJOSEObservability(nil, nil, nil)
	j := &joseRouteConfig{Inbound: p, Outbound: nil, Resolver: r, Obs: obs}
	assert.Same(t, p, j.inbound())
	assert.Nil(t, j.outbound())
	assert.NotNil(t, j.resolver())
	assert.Same(t, obs, j.obs())
}

func TestNewJOSEAPIErrorFromNonJOSEError(t *testing.T) {
	got := newJOSEAPIError(assert.AnError)
	assert.Equal(t, "INTERNAL_ERROR", got.ErrorCode())
	assert.Equal(t, http.StatusInternalServerError, got.HTTPStatus())
}

func TestIsJOSEContentTypeVariants(t *testing.T) {
	tests := []struct {
		ct       string
		expected bool
	}{
		{"application/jose", true},
		{"Application/JOSE", true},
		{"application/jose; charset=utf-8", true},
		{"application/json", false},
		{"", false},
		{"application/jose+json", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, isJOSEContentType(tt.ct), "Content-Type=%q", tt.ct)
	}
}

// joseHandleResponse without inbound-verified context is a security-invariant violation
// path. Even though scanRouteJOSE prevents this combination at registration, the
// runtime check is defense-in-depth and must be exercised.
func TestJOSEHandleResponseRefusesWithoutInboundVerification(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	rh := newResponseHandler(cfg)

	// Build a request without WithInboundVerified — simulates the invariant violation.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := rh.joseHandleResponse(c, "anything", nil, nil, nil, nil)
	require.NoError(t, err)
	// Response is plaintext error envelope, NOT JOSE — exact invariant we want.
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotEqual(t, "application/jose", rec.Header().Get(echo.HeaderContentType))
}
