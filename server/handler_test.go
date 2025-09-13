package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

// Basic request/response types for tests
type helloReq struct {
	Name string `query:"name" validate:"required"`
}

type helloResp struct {
	Message string `json:"message"`
}

func TestWrapHandler_Success_DefaultStatus(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	require.NotNil(t, v)
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "Hello " + req.Name}, nil
	}

	h := WrapHandler[helloReq, helloResp](handler, binder, cfg)

	req := httptest.NewRequest(http.MethodGet, "/hello?name=John", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// Set a request ID to verify trace propagation
	req.Header.Set(echo.HeaderXRequestID, "test-trace-123")

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	// Validate envelope
	require.NotNil(t, resp.Data)
	assert.Nil(t, resp.Error)
	assert.Equal(t, "test-trace-123", resp.Meta["traceId"]) // request header first
}

func TestWrapHandler_Success_CustomStatusWithResult(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (Result[helloResp], IAPIError) {
		return NewResult(http.StatusCreated, helloResp{Message: "Hello " + req.Name}), nil
	}

	h := WrapHandler[helloReq, Result[helloResp]](handler, binder, cfg)

	req := httptest.NewRequest(http.MethodGet, "/hello?name=Jane", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Nil(t, resp.Error)
}

func TestWrapHandler_ValidationError(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "Hello " + req.Name}, nil
	}

	h := WrapHandler[helloReq, helloResp](handler, binder, cfg)

	// Missing required query parameter "name"
	req := httptest.NewRequest(http.MethodGet, "/hello", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, "BAD_REQUEST", resp.Error.Code)
	assert.Equal(t, "Request validation failed", resp.Error.Message)
	// should include details in dev env
	require.NotNil(t, resp.Error.Details)
}

type advancedBindReq struct {
	ID         int       `param:"id" validate:"min=1"`
	Names      []string  `query:"names"`
	Active     *bool     `query:"active"`
	When       time.Time `query:"when"`
	HeaderVals []string  `header:"X-Items"`
}

func TestRequestBinder_AdvancedBinding(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req advancedBindReq, _ HandlerContext) (advancedBindReq, IAPIError) {
		return req, nil
	}

	h := WrapHandler[advancedBindReq, advancedBindReq](handler, binder, cfg)

	req := httptest.NewRequest(http.MethodGet, "/users/5?names=a&names=b&active=true&when=2025-01-01T00:00:00Z", http.NoBody)
	req.Header.Set("X-Items", "a, b , c")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("5")

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// decode response
	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	// re-marshal data to the same struct
	bytes, _ := json.Marshal(resp.Data)
	var got advancedBindReq
	require.NoError(t, json.Unmarshal(bytes, &got))

	assert.Equal(t, 5, got.ID)
	assert.Equal(t, []string{"a", "b"}, got.Names)
	require.NotNil(t, got.Active)
	assert.Equal(t, true, *got.Active)
	assert.Equal(t, []string{"a", "b", "c"}, got.HeaderVals)
	// time parsed correctly (in UTC)
	assert.Equal(t, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), got.When.UTC())
}
