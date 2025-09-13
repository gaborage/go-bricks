package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	gobrickshttp "github.com/gaborage/go-bricks/http"
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

	h := WrapHandler(handler, binder, cfg)

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

	h := WrapHandler(handler, binder, cfg)

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

	h := WrapHandler(handler, binder, cfg)

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
	// details must use camelCase key: validationErrors
	if resp.Error.Details != nil {
		_, hasSnake := resp.Error.Details["validation_errors"]
		assert.False(t, hasSnake, "details should not use snake_case key validation_errors")
		ve, hasCamel := resp.Error.Details["validationErrors"]
		require.True(t, hasCamel, "details must include validationErrors key")
		// should be a list of field errors
		_, ok := ve.([]interface{})
		assert.True(t, ok, "validationErrors must be an array of errors")
	}
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

	h := WrapHandler(handler, binder, cfg)

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

func TestTraceParentResponseHeader_PropagateWhenPresent(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequest(http.MethodGet, "/hello?name=John", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Provide inbound traceparent header
	traceparent := "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	req.Header.Set(gobrickshttp.HeaderTraceParent, traceparent)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Response must propagate the same traceparent
	got := rec.Result().Header.Get(gobrickshttp.HeaderTraceParent)
	assert.Equal(t, traceparent, got)
}

func TestTraceParentResponseHeader_GenerateWhenMissing(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequest(http.MethodGet, "/hello?name=John", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Response must contain a valid-looking traceparent
	got := rec.Result().Header.Get(gobrickshttp.HeaderTraceParent)
	require.NotEmpty(t, got)
	parts := strings.Split(got, "-")
	require.Len(t, parts, 4)
	assert.Len(t, parts[0], 2)
	assert.Len(t, parts[1], 32)
	assert.Len(t, parts[2], 16)
	assert.Len(t, parts[3], 2)
}

// failingValidator returns a fixed error for any input, to exercise non-ValidationError path
type failingValidator struct{ err error }

func (v failingValidator) Validate(_ interface{}) error { return v.err }

func TestWrapHandler_ValidationError_ProdEnv_OmitsDetails(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "production"}}

	handler := func(req helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "Hello " + req.Name}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	// Missing required query parameter "name" triggers validation error
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
	// In prod env, details must be omitted
	assert.Nil(t, resp.Error.Details)
}

func TestWrapHandler_ValidateOtherError_InDev_IncludesErrorDetail(t *testing.T) {
	e := echo.New()
	e.Validator = failingValidator{err: fmt.Errorf("boom")} // not a ValidationError

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "Hello " + req.Name}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequest(http.MethodGet, "/hello?name=John", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, "BAD_REQUEST", resp.Error.Code)
	// For non-ValidationError, details should contain "error": "boom" (camelCase validationErrors not present)
	require.NotNil(t, resp.Error.Details)
	_, hasValidationErrors := resp.Error.Details["validationErrors"]
	assert.False(t, hasValidationErrors)
	got, ok := resp.Error.Details["error"].(string)
	require.True(t, ok)
	assert.Equal(t, "boom", got)
}

func TestWrapHandler_NoContentResult(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	// Handler returns explicit NoContent result
	handler := func(_ helloReq, _ HandlerContext) (NoContentResult, IAPIError) {
		return NoContent(), nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequest(http.MethodGet, "/hello?name=John", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	// No response body for 204
	assert.Equal(t, 0, rec.Body.Len())
}

func TestWrapHandler_ResultAddsHeaders(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (Result[helloResp], IAPIError) {
		r := NewResult(http.StatusCreated, helloResp{Message: "Hello " + req.Name})
		if r.Headers == nil {
			r.Headers = http.Header{}
		}
		r.Headers.Set("Location", "/hello/123")
		return r, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequest(http.MethodGet, "/hello?name=John", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "/hello/123", rec.Header().Get("Location"))
}

func TestWrapHandler_Success_MetaTimestampAndTraceIdFromResponseHeader(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequest(http.MethodGet, "/hello?name=John", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Set response header before handler to exercise fallback path
	c.Response().Header().Set(echo.HeaderXRequestID, "resp-trace-456")

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// traceId should come from response header
	assert.Equal(t, "resp-trace-456", resp.Meta["traceId"])
	// timestamp should be RFC3339
	ts, ok := resp.Meta["timestamp"].(string)
	require.True(t, ok)
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Fatalf("timestamp not RFC3339: %v (value=%q)", err, ts)
	}
}

func TestWrapHandler_Error_MetaTimestampAndTraceIdFromResponseHeader(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	// Missing required query param triggers validation error
	req := httptest.NewRequest(http.MethodGet, "/hello", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Fallback from response header
	c.Response().Header().Set(echo.HeaderXRequestID, "resp-trace-999")

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)

	// traceId should come from response header
	assert.Equal(t, "resp-trace-999", resp.Meta["traceId"])
	// timestamp should be RFC3339
	ts, ok := resp.Meta["timestamp"].(string)
	require.True(t, ok)
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Fatalf("timestamp not RFC3339: %v (value=%q)", err, ts)
	}
}
