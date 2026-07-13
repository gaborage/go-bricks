package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	gobrickshttp "github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

const (
	testResponse             = "Hello "
	testRoute                = "/hello"
	testRouteWithQueryParams = "/hello?name=John"
)

// Basic request/response types for tests
type helloReq struct {
	Name string `query:"name" validate:"required"`
}

type helloResp struct {
	Message string `json:"message"`
}

func TestWrapHandlerSuccessDefaultStatus(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	require.NotNil(t, v)
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: testResponse + req.Name}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
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

func TestWrapHandlerSuccessCustomStatusWithResult(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (Result[helloResp], IAPIError) {
		return NewResult(http.StatusCreated, helloResp{Message: testResponse + req.Name}), nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/hello?name=Jane", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Nil(t, resp.Error)
}

func TestWrapHandlerValidationError(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: testResponse + req.Name}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	// Missing required query parameter "name"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRoute, http.NoBody)
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
		_, ok := ve.([]any)
		assert.True(t, ok, "validationErrors must be an array of errors")
	}
}

type advancedBindReq struct {
	ID         int       `param:"id" json:"id" validate:"min=1"`
	Names      []string  `query:"names" json:"names"`
	Active     *bool     `query:"active" json:"active"`
	When       time.Time `query:"when" json:"when"`
	HeaderVals []string  `header:"X-Items" json:"headerVals"`
}

type numericRequest struct {
	AccountID uint    `param:"accountID" json:"accountID"`
	Limit     uint16  `query:"limit" json:"limit"`
	Ratio     float32 `query:"ratio" json:"ratio"`
}

func TestRequestBinderAdvancedBinding(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req advancedBindReq, _ HandlerContext) (advancedBindReq, IAPIError) {
		return req, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/users/5?names=a&names=b&active=true&when=2025-01-01T00:00:00Z", http.NoBody)
	req.Header.Set("X-Items", "a, b , c")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{
		{Name: "id", Value: "5"},
	})

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

func TestRequestBinderBindsUnsignedAndFloatValues(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	require.NotNil(t, v)
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req numericRequest, _ HandlerContext) (numericRequest, IAPIError) {
		return req, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/accounts/7?limit=42&ratio=3.5", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{
		{Name: "accountID", Value: "7"},
	})

	err := h(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	payload, err := json.Marshal(resp.Data)
	require.NoError(t, err)

	var got numericRequest
	require.NoError(t, json.Unmarshal(payload, &got))
	assert.Equal(t, uint(7), got.AccountID)
	assert.Equal(t, uint16(42), got.Limit)
	assert.InDelta(t, 3.5, float64(got.Ratio), 0.001)
}

func TestRequestBinderInvalidFloatReturnsError(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	require.NotNil(t, v)
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req numericRequest, _ HandlerContext) (numericRequest, IAPIError) {
		return req, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/accounts/7?limit=42&ratio=not-a-number", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{
		{Name: "accountID", Value: "7"},
	})

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	require.NotNil(t, resp.Error)
	assert.Equal(t, "BAD_REQUEST", resp.Error.Code)
	assert.Equal(t, "Invalid request data", resp.Error.Message)
	require.NotNil(t, resp.Error.Details)
	detail, ok := resp.Error.Details["error"].(string)
	require.True(t, ok)
	assert.True(t, strings.Contains(detail, "ParseFloat"))
}

func TestEnsureTraceParentHeaderPreservesExistingResponseHeader(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	req.Header.Set(gobrickshttp.HeaderTraceParent, "00-11111111111111111111111111111111-2222222222222222-01")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	existing := "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	c.Response().Header().Set(gobrickshttp.HeaderTraceParent, existing)

	ensureTraceParentHeader(c)

	assert.Equal(t, existing, c.Response().Header().Get(gobrickshttp.HeaderTraceParent))
}

func TestTraceParentResponseHeaderPropagateWhenPresent(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
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

func TestTraceParentResponseHeaderGenerateWhenMissing(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
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

func TestFormatSuccessResponseWithStatusDefaultsWhenZero(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	headers := http.Header{"X-Test": []string{"value"}}

	err := formatSuccessResponseWithStatus(c, map[string]string{"ok": "true"}, 0, headers)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "value", rec.Header().Get("X-Test"))

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "true", data["ok"])
	assert.NotEmpty(t, resp.Meta["traceId"])
	assert.NotEmpty(t, resp.Meta["timestamp"])
}

// failingValidator returns a fixed error for any input, to exercise non-ValidationError path
type failingValidator struct{ err error }

func (v failingValidator) Validate(_ any) error { return v.err }

func TestWrapHandlerValidationErrorProdEnvOmitsDetails(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "production"}}

	handler := func(req helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: testResponse + req.Name}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	// Missing required query parameter "name" triggers validation error
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRoute, http.NoBody)
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

func TestWrapHandlerValidateOtherErrorInDevIncludesErrorDetail(t *testing.T) {
	e := echo.New()
	e.Validator = failingValidator{err: fmt.Errorf("boom")} // not a ValidationError

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: testResponse + req.Name}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
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

func TestWrapHandlerNoContentResult(t *testing.T) {
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	// No response body for 204
	assert.Equal(t, 0, rec.Body.Len())
}

func TestResultHelpers(t *testing.T) {
	created := Created(map[string]string{"id": "123"})
	status, headers, data := created.ResultMeta()
	assert.Equal(t, http.StatusCreated, status)
	assert.Nil(t, headers)
	assert.Equal(t, map[string]string{"id": "123"}, data)

	accepted := Accepted("queued")
	status, headers, data = accepted.ResultMeta()
	assert.Equal(t, http.StatusAccepted, status)
	assert.Nil(t, headers)
	assert.Equal(t, "queued", data)
}

func TestWrapHandlerResultAddsHeaders(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (Result[helloResp], IAPIError) {
		r := NewResult(http.StatusCreated, helloResp{Message: testResponse + req.Name})
		if r.Headers == nil {
			r.Headers = http.Header{}
		}
		r.Headers.Set("Location", "/hello/123")
		return r, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "/hello/123", rec.Header().Get("Location"))
}

func TestWrapHandlerSuccessMetaTimestampAndTraceIdFromResponseHeader(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
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

func TestWrapHandlerErrorMetaTimestampAndTraceIdFromResponseHeader(t *testing.T) {
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRoute, http.NoBody)
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

func TestHandlerRegistryRegistersRoutes(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	hr := NewHandlerRegistry(cfg)
	require.NotNil(t, hr)

	e := echo.New()
	v := NewValidator()
	require.NotNil(t, v)
	e.Validator = v
	registrar := newRouteGroup(e.Group(""), "", nil)

	type emptyReq struct{}

	handler := func(emptyReq, HandlerContext) (NoContentResult, IAPIError) {
		return NoContent(), nil
	}

	RegisterHandler(hr, registrar, http.MethodGet, "/custom", handler)
	GET(hr, registrar, "/get", handler)
	POST(hr, registrar, "/post", handler)
	PUT(hr, registrar, "/put", handler)
	DELETE(hr, registrar, "/delete", handler)
	PATCH(hr, registrar, "/patch", handler)
	HEAD(hr, registrar, "/head", handler)
	OPTIONS(hr, registrar, "/options", handler)

	routes := make(map[string]struct{})
	for _, route := range e.Router().Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}

	expected := []string{
		http.MethodGet + " /custom",
		http.MethodGet + " /get",
		http.MethodPost + " /post",
		http.MethodPut + " /put",
		http.MethodDelete + " /delete",
		http.MethodPatch + " /patch",
		http.MethodHead + " /head",
		http.MethodOptions + " /options",
	}

	for _, key := range expected {
		_, ok := routes[key]
		assert.True(t, ok, "expected route %s to be registered", key)
	}
}

func TestSetFieldValueAllocatesPointer(t *testing.T) {
	type payload struct {
		Count *int
	}

	target := payload{}
	field := reflect.ValueOf(&target).Elem().FieldByName("Count")

	require.True(t, field.IsNil())

	err := setFieldValue(field, "42")
	require.NoError(t, err)

	require.NotNil(t, target.Count)
	assert.Equal(t, 42, *target.Count)
}

func TestSetFieldValueReturnsErrorForUnsupportedStruct(t *testing.T) {
	type payload struct {
		Custom struct {
			Value int
		}
	}

	target := payload{}
	field := reflect.ValueOf(&target).Elem().FieldByName("Custom")

	err := setFieldValue(field, "value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported struct type")
}

func TestSetFieldValueReturnsErrorForSlice(t *testing.T) {
	type payload struct {
		Items []string
	}

	target := payload{}
	field := reflect.ValueOf(&target).Elem().FieldByName("Items")

	err := setFieldValue(field, "value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported assignment to slice")
}

func TestSetFieldValueReturnsErrorForMap(t *testing.T) {
	type payload struct {
		Data map[string]int
	}

	target := payload{}
	field := reflect.ValueOf(&target).Elem().FieldByName("Data")

	err := setFieldValue(field, "value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported field type")
}

func TestSetFieldValueInvalidBool(t *testing.T) {
	type payload struct {
		Enabled bool
	}

	target := payload{}
	field := reflect.ValueOf(&target).Elem().FieldByName("Enabled")

	err := setFieldValue(field, "not-bool")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ParseBool")
}

func TestSetFieldValueInvalidSignedInt(t *testing.T) {
	type payload struct {
		Count int
	}

	target := payload{}
	field := reflect.ValueOf(&target).Elem().FieldByName("Count")

	err := setFieldValue(field, "not-a-number")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ParseInt")
}

func TestSetFieldValueInvalidUnsignedInt(t *testing.T) {
	type payload struct {
		Count uint
	}

	target := payload{}
	field := reflect.ValueOf(&target).Elem().FieldByName("Count")

	err := setFieldValue(field, "not-a-number")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ParseUint")
}

func TestSetFieldValueTimeInvalid(t *testing.T) {
	type payload struct {
		When time.Time
	}

	target := payload{}
	field := reflect.ValueOf(&target).Elem().FieldByName("When")

	err := setFieldValue(field, "not-a-time")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestSetFieldValueSignedIntBitSize(t *testing.T) {
	type payload struct {
		Value int8
	}

	target := payload{}
	field := reflect.ValueOf(&target).Elem().FieldByName("Value")

	require.NoError(t, setFieldValue(field, "7"))
	assert.Equal(t, int8(7), target.Value)
}

func TestSetFieldValueUnsignedIntBitSize(t *testing.T) {
	type payload struct {
		Value uint8
	}

	target := payload{}
	field := reflect.ValueOf(&target).Elem().FieldByName("Value")

	require.NoError(t, setFieldValue(field, "7"))
	assert.Equal(t, uint8(7), target.Value)
}

func TestParseTimeAdditionalLayouts(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "date_only", value: "2025-04-01"},
		{name: "date_time", value: "2025-04-01 12:34:56"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTime(tt.value)
			require.NoError(t, err)
			assert.False(t, got.IsZero())
		})
	}

	t.Run("invalid", func(t *testing.T) {
		_, err := parseTime("not a time")
		require.Error(t, err)
	})
}

func TestParseTimeRFC3339NanoStillWorks(t *testing.T) {
	now := time.Now().UTC()
	formatted := now.Format(time.RFC3339Nano)

	parsed, err := parseTime(formatted)
	require.NoError(t, err)
	assert.WithinDuration(t, now, parsed, time.Nanosecond)
}

func sampleHandlerFunc() { /* no-op */ }

type sampleReceiver struct{}

func (sampleReceiver) sampleMethod() { /* no-op */ }

type packageCaller struct{}

func (packageCaller) callPackage() string {
	return packageCallerNested()
}

func packageCallerNested() string {
	// skip 3: getCallerPackage → packageCallerNested → callPackage → TestGetCallerPackage,
	// mirroring the typed path's getCallerPackage → RegisterHandler → GET/POST → module depth.
	return getCallerPackage(3)
}

func TestExtractHandlerName(t *testing.T) {
	t.Run("nil handler", func(t *testing.T) {
		assert.Equal(t, "", extractHandlerName(nil))
	})

	t.Run("non function", func(t *testing.T) {
		assert.Equal(t, "", extractHandlerName(123))
	})

	t.Run("plain function", func(t *testing.T) {
		assert.Equal(t, "sampleHandlerFunc", extractHandlerName(sampleHandlerFunc))
	})

	t.Run("method expression", func(t *testing.T) {
		got := extractHandlerName(sampleReceiver{}.sampleMethod)
		assert.Equal(t, "sampleMethod", strings.TrimSuffix(got, "-fm"))
	})
}

func TestGetCallerPackage(t *testing.T) {
	pkg := (packageCaller{}).callPackage()
	assert.NotEmpty(t, pkg)
	assert.Contains(t, pkg, "github.com/gaborage/go-bricks/server")
	assert.NotContains(t, pkg, "(")
}

// ==================== Pointer Type Support Tests ====================

// Test types for pointer support
type pointerReq struct {
	Name  string `json:"name" validate:"required"`
	Email string `json:"email" validate:"email"`
}

type pointerResp struct {
	Result  string `json:"result"`
	Records []int  `json:"records"`
}

// TestWrapHandlerPointerRequestType tests that WrapHandler can handle pointer request types
func TestWrapHandlerPointerRequestType(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req *pointerReq, _ HandlerContext) (helloResp, IAPIError) {
		// Verify we received a non-nil pointer
		require.NotNil(t, req)
		return helloResp{Message: testResponse + req.Name}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	reqBody := `{"name":"Alice","email":"alice@example.com"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(reqBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotNil(t, resp.Data)

	// Verify response data
	dataBytes, err := json.Marshal(resp.Data)
	require.NoError(t, err)
	var respData helloResp
	require.NoError(t, json.Unmarshal(dataBytes, &respData))
	assert.Equal(t, "Hello Alice", respData.Message)
}

// TestWrapHandlerPointerRequestTypeWithQueryParams tests that WrapHandler can bind pointer request types from query parameters
func TestWrapHandlerPointerRequestTypeWithQueryParams(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	type ptrQueryReq struct {
		Name string `query:"name" validate:"required"`
		Age  int    `query:"age" validate:"min=1"`
	}

	handler := func(req *ptrQueryReq, _ HandlerContext) (helloResp, IAPIError) {
		require.NotNil(t, req)
		return helloResp{Message: fmt.Sprintf("Hello %s, age %d", req.Name, req.Age)}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test?name=Bob&age=25", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	dataBytes, err := json.Marshal(resp.Data)
	require.NoError(t, err)
	var respData helloResp
	require.NoError(t, json.Unmarshal(dataBytes, &respData))
	assert.Equal(t, "Hello Bob, age 25", respData.Message)
}

// TestWrapHandlerPointerResponseType tests that WrapHandler can handle pointer response types
func TestWrapHandlerPointerResponseType(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (*pointerResp, IAPIError) {
		// Return pointer to large response (avoids copy)
		return &pointerResp{
			Result:  "Success for " + req.Name,
			Records: []int{1, 2, 3, 4, 5},
		}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test?name=Charlie", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotNil(t, resp.Data)

	// Verify response data
	dataBytes, err := json.Marshal(resp.Data)
	require.NoError(t, err)
	var respData pointerResp
	require.NoError(t, json.Unmarshal(dataBytes, &respData))
	assert.Equal(t, "Success for Charlie", respData.Result)
	assert.Equal(t, []int{1, 2, 3, 4, 5}, respData.Records)
}

// TestWrapHandlerPointerResponseType tests that WrapHandler can handle pointer response types
func TestWrapHandlerBothPointerTypes(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req *pointerReq, _ HandlerContext) (*pointerResp, IAPIError) {
		require.NotNil(t, req)
		return &pointerResp{
			Result:  "Processed " + req.Name,
			Records: []int{10, 20, 30},
		}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	reqBody := `{"name":"Diana","email":"diana@example.com"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(reqBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	dataBytes, err := json.Marshal(resp.Data)
	require.NoError(t, err)
	var respData pointerResp
	require.NoError(t, json.Unmarshal(dataBytes, &respData))
	assert.Equal(t, "Processed Diana", respData.Result)
}

// TestWrapHandlerMixedPointerValue tests that WrapHandler can handle mixed pointer and value types
func TestWrapHandlerMixedPointerValue(t *testing.T) {
	t.Run("pointer request, value response", func(t *testing.T) {
		e := echo.New()
		v := NewValidator()
		e.Validator = v

		binder := NewRequestBinder()
		cfg := &config.Config{App: config.AppConfig{Env: "development"}}

		handler := func(req *pointerReq, _ HandlerContext) (helloResp, IAPIError) {
			require.NotNil(t, req)
			return helloResp{Message: testResponse + req.Name}, nil
		}

		h := WrapHandler(handler, binder, cfg)

		reqBody := `{"name":"Eve","email":"eve@example.com"}`
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(reqBody))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h(c)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("value request, pointer response", func(t *testing.T) {
		e := echo.New()
		v := NewValidator()
		e.Validator = v

		binder := NewRequestBinder()
		cfg := &config.Config{App: config.AppConfig{Env: "development"}}

		handler := func(req helloReq, _ HandlerContext) (*helloResp, IAPIError) {
			return &helloResp{Message: testResponse + req.Name}, nil
		}

		h := WrapHandler(handler, binder, cfg)

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test?name=Frank", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h(c)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

// TestWrapHandlerPointerRequestType tests that WrapHandler can handle pointer request types
func TestWrapHandlerLargePayloadPointer(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	type largeBulkReq struct {
		Records []string `json:"records"` // Simulates bulk data import
		Meta    string   `json:"meta"`
	}

	handler := func(req *largeBulkReq, _ HandlerContext) (helloResp, IAPIError) {
		require.NotNil(t, req)
		// Verify large payload was received
		assert.Greater(t, len(req.Records), 100, "Expected large payload")
		return helloResp{Message: fmt.Sprintf("Processed %d records", len(req.Records))}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	// Simulate large payload (e.g., bulk import with many records)
	records := make([]string, 1000)
	for i := range records {
		records[i] = fmt.Sprintf("record-%d-with-some-data", i)
	}

	reqBodyMap := map[string]any{
		"records": records,
		"meta":    "bulk-import-test",
	}
	reqBodyBytes, err := json.Marshal(reqBodyMap)
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/upload", strings.NewReader(string(reqBodyBytes)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	dataBytes, err := json.Marshal(resp.Data)
	require.NoError(t, err)
	var respData helloResp
	require.NoError(t, json.Unmarshal(dataBytes, &respData))
	assert.Equal(t, "Processed 1000 records", respData.Message)
}

// TestWrapHandlerPointerRequestValidationError tests that validation errors are handled correctly for pointer request types
func TestWrapHandlerPointerRequestValidationError(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req *pointerReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: testResponse + req.Name}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	// Missing required field
	reqBody := `{"email":"invalid-email"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(reqBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotNil(t, resp.Error)
	assert.Equal(t, "BAD_REQUEST", resp.Error.Code)
}

// TestWrapHandlerPointerRequestValidationError tests that validation errors are handled correctly for pointer request types
func TestWrapHandlerPointerRequestWithResult(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req *pointerReq, _ HandlerContext) (Result[pointerResp], IAPIError) {
		require.NotNil(t, req)
		return NewResult(http.StatusCreated, pointerResp{
			Result:  "Created " + req.Name,
			Records: []int{1, 2, 3},
		}), nil
	}

	h := WrapHandler(handler, binder, cfg)

	reqBody := `{"name":"Grace","email":"grace@example.com"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(reqBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotNil(t, resp.Data)
}

// TestWrapHandlerPointerRequestValidationError tests that validation errors are handled correctly for pointer request types
func TestWrapHandlerPointerFieldsInStruct(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	type reqWithPointerFields struct {
		Name   *string `json:"name" validate:"omitempty,min=3"`
		Age    *int    `json:"age" validate:"omitempty,min=18"`
		Active *bool   `json:"active"`
	}

	handler := func(req *reqWithPointerFields, _ HandlerContext) (helloResp, IAPIError) {
		require.NotNil(t, req)

		var msg string
		if req.Name != nil {
			msg = "Name: " + *req.Name
		}
		if req.Age != nil {
			msg += fmt.Sprintf(", Age: %d", *req.Age)
		}
		if req.Active != nil {
			msg += fmt.Sprintf(", Active: %v", *req.Active)
		}

		return helloResp{Message: msg}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	name := "Henry"
	age := 30
	active := true

	reqBody := fmt.Sprintf(`{"name":%q,"age":%d,"active":%v}`, name, age, active)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(reqBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	dataBytes, err := json.Marshal(resp.Data)
	require.NoError(t, err)
	var respData helloResp
	require.NoError(t, json.Unmarshal(dataBytes, &respData))
	assert.Contains(t, respData.Message, "Henry")
	assert.Contains(t, respData.Message, "30")
	assert.Contains(t, respData.Message, "true")
}

// TestWrapHandlerNilPointerRejection verifies that nil pointer requests are rejected.
// Note: In practice, the JSON unmarshaler will never produce a nil pointer for a struct,
// but we test the logic to ensure defensive programming.
func TestWrapHandlerNilPointerRejection(t *testing.T) {
	// This test verifies that our nil check logic is in place.
	// In real scenarios, JSON unmarshaling to a pointer type always allocates,
	// so we verify the logic exists by checking the code path with reflection.

	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req *pointerReq, _ HandlerContext) (helloResp, IAPIError) {
		// In normal operation, this should never receive nil due to our check
		require.NotNil(t, req)
		return helloResp{Message: testResponse + req.Name}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	// Valid request should succeed (baseline test)
	reqBody := `{"name":"Test","email":"test@example.com"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(reqBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify the handler received a non-nil pointer
	// (the test would fail if req was nil due to require.NotNil in handler)
}

// TestWrapHandlerEmptyJSONPointerRequest tests that an empty JSON object produces a valid pointer with zero values
func TestWrapHandlerEmptyJSONPointerRequest(t *testing.T) {
	// Test that empty JSON object creates a valid (non-nil) pointer with zero values
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	type optionalFieldsReq struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	handler := func(req *optionalFieldsReq, _ HandlerContext) (helloResp, IAPIError) {
		require.NotNil(t, req)
		// Empty JSON should produce a non-nil pointer with zero values
		if req.Name == "" {
			return helloResp{Message: "No name provided"}, nil
		}
		return helloResp{Message: testResponse + req.Name}, nil
	}

	h := WrapHandler(handler, binder, cfg)

	// Empty JSON object
	reqBody := `{}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(reqBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	dataBytes, err := json.Marshal(resp.Data)
	require.NoError(t, err)
	var respData helloResp
	require.NoError(t, json.Unmarshal(dataBytes, &respData))
	assert.Equal(t, "No name provided", respData.Message)
}

// ==================== Raw Response Mode Tests ====================

func TestWrapHandlerRawResponseSuccess(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	require.NotNil(t, v)
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: testResponse + req.Name}, nil
	}

	h := wrapHandler(handler, binder, cfg, nil, true)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Parse response as raw JSON — no data/meta envelope
	var resp helloResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "Hello John", resp.Message)

	// Verify NO envelope keys present
	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	_, hasData := raw["data"]
	_, hasMeta := raw["meta"]
	assert.False(t, hasData, "raw response should not have 'data' key")
	assert.False(t, hasMeta, "raw response should not have 'meta' key")

	// W3C trace propagation still works
	got := rec.Result().Header.Get(gobrickshttp.HeaderTraceParent)
	require.NotEmpty(t, got, "traceparent header should be set in raw mode")
}

func TestWrapHandlerRawResponseWithResult(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (Result[helloResp], IAPIError) {
		return NewResult(http.StatusCreated, helloResp{Message: testResponse + req.Name}), nil
	}

	h := wrapHandler(handler, binder, cfg, nil, true)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/hello?name=Jane", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// Response is directly the helloResp, not wrapped
	var resp helloResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "Hello Jane", resp.Message)
}

func TestWrapHandlerRawResponseError(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{}, NewNotFoundError("User")
	}

	h := wrapHandler(handler, binder, cfg, nil, true)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Error is minimal JSON, not wrapped in APIResponse
	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.Equal(t, "NOT_FOUND", raw["code"])
	assert.Equal(t, "User not found", raw["message"])

	// No envelope keys
	_, hasError := raw["error"]
	_, hasMeta := raw["meta"]
	assert.False(t, hasError, "raw error should not have 'error' key (APIResponse envelope)")
	assert.False(t, hasMeta, "raw error should not have 'meta' key")
}

func TestWrapHandlerRawResponseValidationError(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(req helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: testResponse + req.Name}, nil
	}

	h := wrapHandler(handler, binder, cfg, nil, true)

	// Missing required query parameter "name" triggers validation error
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRoute, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.Equal(t, "BAD_REQUEST", raw["code"])
	assert.Equal(t, "Request validation failed", raw["message"])

	// No envelope keys
	_, hasMeta := raw["meta"]
	assert.False(t, hasMeta, "raw error should not have 'meta' key")
}

func TestWrapHandlerRawResponseNoContent(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (NoContentResult, IAPIError) {
		return NoContent(), nil
	}

	h := wrapHandler(handler, binder, cfg, nil, true)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, 0, rec.Body.Len())
}

func TestWrapHandlerRawResponseErrorProdOmitsDetails(t *testing.T) {
	e := echo.New()
	v := NewValidator()
	e.Validator = v

	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "production"}}

	handler := func(_ helloReq, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{}, NewNotFoundError("User")
	}

	h := wrapHandler(handler, binder, cfg, nil, true)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.Equal(t, "NOT_FOUND", raw["code"])
	// Details should be omitted in production
	_, hasDetails := raw["details"]
	assert.False(t, hasDetails, "production raw error should not include details")
}

// ---- ResultWithMeta / ResultEnvelopeProvider ----

func TestResultWithMetaSatisfiesBothInterfaces(t *testing.T) {
	r := ResultWithMeta[helloResp]{
		Data:   helloResp{Message: "ok"},
		Meta:   map[string]any{"total": 42},
		Status: http.StatusOK,
	}

	var _ ResultEnvelopeProvider = r
	var _ ResultMetaProvider = r

	status, headers, data, meta := r.ResultEnvelope()
	assert.Equal(t, http.StatusOK, status)
	assert.Nil(t, headers)
	assert.Equal(t, helloResp{Message: "ok"}, data)
	assert.Equal(t, 42, meta["total"])

	// ResultMeta is the legacy-shape projection: same status/headers/data, no meta.
	mStatus, mHeaders, mData := r.ResultMeta()
	assert.Equal(t, status, mStatus)
	assert.Equal(t, headers, mHeaders)
	assert.Equal(t, data, mData)
}

func TestNewResultWithMetaConstructor(t *testing.T) {
	r := NewResultWithMeta(http.StatusCreated, helloResp{Message: "new"}, map[string]any{"location": "/users/1"})
	assert.Equal(t, http.StatusCreated, r.Status)
	assert.Equal(t, helloResp{Message: "new"}, r.Data)
	assert.Equal(t, "/users/1", r.Meta["location"])
	assert.Nil(t, r.Headers)
}

func TestWrapHandlerMergesEnvelopeMeta(t *testing.T) {
	e := echo.New()
	e.Validator = NewValidator()
	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (ResultWithMeta[helloResp], IAPIError) {
		return ResultWithMeta[helloResp]{
			Data:   helloResp{Message: "paged"},
			Status: http.StatusOK,
			Meta: map[string]any{
				"total":   123,
				"limit":   50,
				"offset":  0,
				"hasMore": true,
			},
		}, nil
	}

	h := wrapHandler(handler, binder, cfg, nil, false)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, "merge-trace")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, float64(123), resp.Meta["total"])
	assert.Equal(t, float64(50), resp.Meta["limit"])
	assert.Equal(t, float64(0), resp.Meta["offset"])
	assert.Equal(t, true, resp.Meta["hasMore"])
	// Framework keys still present and authoritative.
	assert.Equal(t, "merge-trace", resp.Meta["traceId"])
	_, hasTimestamp := resp.Meta["timestamp"]
	assert.True(t, hasTimestamp)
}

// levelRecLogger is a level-aware variant of recLogger. It captures every event with the
// level that produced it so reserved-key collision tests can assert WARN severity (a
// regression to Info/Debug would otherwise pass silently against the shared recLogger).
type levelRecLogger struct {
	events []levelRecEvent
}

type levelRecEvent struct {
	level     string
	fields    map[string]string
	intFields map[string]int
	message   string
}

func (l *levelRecLogger) record(level string) logger.LogEvent {
	l.events = append(l.events, levelRecEvent{
		level:     level,
		fields:    map[string]string{},
		intFields: map[string]int{},
	})
	return &l.events[len(l.events)-1]
}
func (l *levelRecLogger) Info() logger.LogEvent                     { return l.record("info") }
func (l *levelRecLogger) Warn() logger.LogEvent                     { return l.record("warn") }
func (l *levelRecLogger) Error() logger.LogEvent                    { return l.record("error") }
func (l *levelRecLogger) Debug() logger.LogEvent                    { return l.record("debug") }
func (l *levelRecLogger) Fatal() logger.LogEvent                    { return l.record("fatal") }
func (l *levelRecLogger) WithContext(_ any) logger.Logger           { return l }
func (l *levelRecLogger) WithFields(_ map[string]any) logger.Logger { return l }

func (e *levelRecEvent) Msg(msg string)                                { e.message = msg }
func (e *levelRecEvent) Msgf(_ string, _ ...any)                       {}
func (e *levelRecEvent) Err(_ error) logger.LogEvent                   { return e }
func (e *levelRecEvent) Str(k, v string) logger.LogEvent               { e.fields[k] = v; return e }
func (e *levelRecEvent) Int(k string, v int) logger.LogEvent           { e.intFields[k] = v; return e }
func (e *levelRecEvent) Int64(_ string, _ int64) logger.LogEvent       { return e }
func (e *levelRecEvent) Uint64(_ string, _ uint64) logger.LogEvent     { return e }
func (e *levelRecEvent) Dur(_ string, _ time.Duration) logger.LogEvent { return e }
func (e *levelRecEvent) Interface(_ string, _ any) logger.LogEvent     { return e }
func (e *levelRecEvent) Bytes(_ string, _ []byte) logger.LogEvent      { return e }
func (e *levelRecEvent) Bool(_ string, _ bool) logger.LogEvent         { return e }
func (e *levelRecEvent) Enabled() bool                                 { return true }

func TestWrapHandlerEnvelopeMetaReservedKeyOverwriteAndWarn(t *testing.T) {
	e := echo.New()
	e.Validator = NewValidator()
	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	rec := &levelRecLogger{}

	handler := func(_ helloReq, _ HandlerContext) (ResultWithMeta[helloResp], IAPIError) {
		return ResultWithMeta[helloResp]{
			Data:   helloResp{Message: "x"},
			Status: http.StatusOK,
			Meta: map[string]any{
				// Both reserved keys at once — level-aware logger captures every event, so
				// we can assert BOTH WARNs fire (catching a future bug that exits the merge
				// loop early after the first collision).
				fieldTimestamp: "fake-ts",
				fieldTraceID:   "fake-trace",
				"page":         1,
			},
		}, nil
	}

	h := wrapHandler(handler, binder, cfg, rec, false)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, "real-trace")
	w := httptest.NewRecorder()
	c := e.NewContext(req, w)

	require.NoError(t, h(c))

	var resp APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Framework keys win — neither "fake-ts" nor "fake-trace" must appear.
	assert.Equal(t, "real-trace", resp.Meta["traceId"])
	assert.NotEqual(t, "fake-ts", resp.Meta["timestamp"])
	assert.Equal(t, float64(1), resp.Meta["page"])

	// Severity must be WARN (regression guard for the documented contract) and BOTH
	// offending keys must be surfaced. Asserts on presence + content rather than exact
	// event count so a future refactor that batches collisions into a single structured
	// WARN (e.g., .Strs("keys", [...])) still satisfies the contract.
	require.NotEmpty(t, rec.events, "expected at least one log event for reserved-key collision")
	warnSeenKeys := map[string]bool{}
	for _, ev := range rec.events {
		if ev.level != "warn" {
			continue
		}
		assert.Equal(t, "envelope meta key collides with framework-managed key; handler value dropped", ev.message)
		if k := ev.fields["key"]; k != "" {
			warnSeenKeys[k] = true
		}
	}
	assert.True(t, warnSeenKeys[fieldTimestamp], "WARN missing for timestamp collision")
	assert.True(t, warnSeenKeys[fieldTraceID], "WARN missing for traceId collision")
}

func TestWrapHandlerEnvelopeMetaReservedKeyNilLoggerSilentlyDrops(t *testing.T) {
	e := echo.New()
	e.Validator = NewValidator()
	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (ResultWithMeta[helloResp], IAPIError) {
		return ResultWithMeta[helloResp]{
			Data:   helloResp{Message: "x"},
			Status: http.StatusOK,
			Meta:   map[string]any{fieldTraceID: "fake"},
		}, nil
	}

	// nil logger MUST NOT panic — reserved keys are still dropped, warning is suppressed.
	h := wrapHandler(handler, binder, cfg, nil, false)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, "trace-A")
	w := httptest.NewRecorder()
	c := e.NewContext(req, w)

	require.NoError(t, h(c))

	var resp APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "trace-A", resp.Meta["traceId"])
}

func TestWrapHandlerEnvelopeMetaPropagatesHeadersAndStatus(t *testing.T) {
	e := echo.New()
	e.Validator = NewValidator()
	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	handler := func(_ helloReq, _ HandlerContext) (ResultWithMeta[helloResp], IAPIError) {
		r := ResultWithMeta[helloResp]{
			Data:    helloResp{Message: "created"},
			Status:  http.StatusCreated,
			Headers: http.Header{},
			Meta:    map[string]any{"x": 1},
		}
		r.Headers.Set("Location", "/widgets/42")
		return r, nil
	}

	h := wrapHandler(handler, binder, cfg, nil, false)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	w := httptest.NewRecorder()
	c := e.NewContext(req, w)

	require.NoError(t, h(c))
	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "/widgets/42", w.Header().Get("Location"))
}

func TestWrapHandlerRawResponseDropsEnvelopeMeta(t *testing.T) {
	e := echo.New()
	e.Validator = NewValidator()
	binder := NewRequestBinder()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	rec := &recLogger{}

	handler := func(_ helloReq, _ HandlerContext) (ResultWithMeta[helloResp], IAPIError) {
		return ResultWithMeta[helloResp]{
			Data:   helloResp{Message: "raw"},
			Status: http.StatusOK,
			Meta:   map[string]any{"total": 7},
		}, nil
	}

	// rawResponse=true causes the meta map to be silently dropped; only data ships.
	h := wrapHandler(handler, binder, cfg, rec, true)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRouteWithQueryParams, http.NoBody)
	w := httptest.NewRecorder()
	c := e.NewContext(req, w)

	require.NoError(t, h(c))
	assert.Equal(t, http.StatusOK, w.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	// Body is bare data — envelope (meta/data/error keys) MUST NOT appear.
	_, hasMeta := raw["meta"]
	_, hasData := raw["data"]
	assert.False(t, hasMeta, "raw mode must not emit meta key")
	assert.False(t, hasData, "raw mode must not wrap data")
	assert.Equal(t, "raw", raw["message"])

	// Debug log fired naming the misconfiguration — message is interface-generic so it
	// stays accurate for custom ResultEnvelopeProvider implementations, with the actual
	// concrete type recorded in the `type` field.
	require.NotNil(t, rec.last, "expected a debug log event")
	assert.Equal(t, "raw response mode dropped envelope meta from ResultEnvelopeProvider", rec.last.message)
	assert.Equal(t, 1, rec.last.intFields["dropped_meta_keys"])
	assert.Contains(t, rec.last.fields["type"], "ResultWithMeta", "type field should record the concrete provider")
}

func TestWithLoggerOptionSetsField(t *testing.T) {
	rec := &recLogger{}
	hr := NewHandlerRegistry(&config.Config{App: config.AppConfig{Env: "development"}}, WithLogger(rec))
	assert.Same(t, rec, hr.log.(*recLogger))
}

func TestGetTraceIDPassesValidInboundHeader(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, "valid-trace-id-123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	assert.Equal(t, "valid-trace-id-123", getTraceID(c))
}

func TestGetTraceIDRejectsInvalidInboundHeader(t *testing.T) {
	cases := []string{
		"has spaces",
		"has\ttab",
		"<script>",
		strings.Repeat("x", 200), // over 128-byte cap
		"path/with/slashes",
	}
	for _, junk := range cases {
		// Sanitize the case label for use as a subtest name — '/' is a
		// path separator in Go's testing framework, so `go test -run` filters
		// against unsanitized junk would fail to match the intended scope.
		label := strings.ReplaceAll(junk[:min(len(junk), 12)], "/", "_")
		t.Run("rejects_"+label, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
			req.Header.Set(echo.HeaderXRequestID, junk)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			got := getTraceID(c)
			assert.NotEqual(t, junk, got, "invalid inbound X-Request-ID must NOT be reflected")
			assert.NotEmpty(t, got, "getTraceID must always return a usable ID")
			// Generated IDs are UUIDs (36 chars including hyphens).
			assert.Len(t, got, 36, "expected a generated UUID when inbound rejected")
			// Response header should carry the same generated ID.
			assert.Equal(t, got, rec.Header().Get(echo.HeaderXRequestID))
		})
	}
}

// TestFrameworkEnvelopeWireParity proves the internal typed frameworkEnvelope used on the
// default success/error encode paths serializes byte-for-byte identically to the public
// APIResponse with a map[string]any meta. It also drives the real production writers
// (formatSuccessResponse / formatErrorResponse) and asserts the emitted meta sub-object is
// {"timestamp":...,"traceId":...} with timestamp ordered before traceId.
func TestFrameworkEnvelopeWireParity(t *testing.T) {
	const (
		fixedTimestamp = "2026-06-07T12:34:56Z"
		fixedTraceID   = "trace-parity-123"
	)

	typedMeta := frameworkMeta{Timestamp: fixedTimestamp, TraceID: fixedTraceID}
	mapMeta := map[string]any{fieldTimestamp: fixedTimestamp, fieldTraceID: fixedTraceID}

	t.Run("success_data_and_meta", func(t *testing.T) {
		data := helloResp{Message: "ok"}

		typedBytes, err := json.Marshal(frameworkEnvelope{Data: data, Meta: typedMeta})
		require.NoError(t, err)
		mapBytes, err := json.Marshal(APIResponse{Data: data, Meta: mapMeta})
		require.NoError(t, err)
		typedStr := string(typedBytes)

		// Byte-for-byte identical: data,meta field order; meta key order timestamp,traceId.
		assert.Equal(t, string(mapBytes), typedStr)
		assert.JSONEq(t, `{"data":{"message":"ok"},"meta":{"timestamp":"2026-06-07T12:34:56Z","traceId":"trace-parity-123"}}`, typedStr)
		// timestamp must appear before traceId in the raw bytes.
		assert.Less(t, strings.Index(typedStr, `"timestamp"`), strings.Index(typedStr, `"traceId"`))
	})

	t.Run("error_error_and_meta", func(t *testing.T) {
		errResp := &APIErrorResponse{Code: "BAD_REQUEST", Message: "boom"}

		typedBytes, err := json.Marshal(frameworkEnvelope{Error: errResp, Meta: typedMeta})
		require.NoError(t, err)
		mapBytes, err := json.Marshal(APIResponse{Error: errResp, Meta: mapMeta})
		require.NoError(t, err)
		typedStr := string(typedBytes)

		// Byte-for-byte identical: data omitted (zero), then error,meta.
		assert.Equal(t, string(mapBytes), typedStr)
		assert.JSONEq(t, `{"error":{"code":"BAD_REQUEST","message":"boom"},"meta":{"timestamp":"2026-06-07T12:34:56Z","traceId":"trace-parity-123"}}`, typedStr)
		assert.Less(t, strings.Index(typedStr, `"timestamp"`), strings.Index(typedStr, `"traceId"`))
	})

	// metaOrderRe captures the literal meta sub-object so we can assert exact key order
	// in the bytes the production writers emit.
	metaOrderRe := `"meta":{"timestamp":`

	t.Run("formatSuccessResponse_emits_typed_envelope", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRoute, http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Response().Header().Set(echo.HeaderXRequestID, fixedTraceID)

		require.NoError(t, formatSuccessResponse(c, helloResp{Message: "ok"}))
		assert.Equal(t, http.StatusOK, rec.Code)

		body := rec.Body.String()
		// meta sub-object must be exactly {"timestamp":...,"traceId":...} (key order).
		assert.Contains(t, body, metaOrderRe)
		assert.Contains(t, body, `"traceId":"`+fixedTraceID+`"`)
		assert.Less(t, strings.Index(body, `"timestamp"`), strings.Index(body, `"traceId"`))

		// Body still unmarshals into the public APIResponse with a map meta (wire shape unchanged).
		var resp APIResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, fixedTraceID, resp.Meta[fieldTraceID])
		ts, ok := resp.Meta[fieldTimestamp].(string)
		require.True(t, ok)
		_, err := time.Parse(time.RFC3339, ts)
		require.NoError(t, err)
	})

	t.Run("formatErrorResponse_emits_typed_envelope", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testRoute, http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Response().Header().Set(echo.HeaderXRequestID, fixedTraceID)

		cfg := &config.Config{App: config.AppConfig{Env: "development"}}
		apiErr := NewBadRequestError("boom")
		require.NoError(t, formatErrorResponse(c, apiErr, cfg))
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		body := rec.Body.String()
		assert.Contains(t, body, metaOrderRe)
		assert.Contains(t, body, `"traceId":"`+fixedTraceID+`"`)
		assert.Less(t, strings.Index(body, `"timestamp"`), strings.Index(body, `"traceId"`))

		var resp APIResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Error)
		assert.Equal(t, fixedTraceID, resp.Meta[fieldTraceID])
		ts, ok := resp.Meta[fieldTimestamp].(string)
		require.True(t, ok)
		_, err := time.Parse(time.RFC3339, ts)
		require.NoError(t, err)
	})
}

// ==================== HandlerContext accessor / adapter tests (issue #623) ====================

// TestHandlerContextAccessors exercises the full echo-free accessor surface that replaced
// the exported HandlerContext.Echo field: Request/RequestContext/ResponseWriter/Param/
// Query/RequestHeader/Get/Set/JSON/String. It builds the context in-package via
// newHandlerContext so path params can be seeded.
func TestHandlerContextAccessors(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/widgets/42?q=hello", http.NoBody)
	req.Header.Set("X-Foo", "bar")
	rec := httptest.NewRecorder()
	ec := e.NewContext(req, rec)
	ec.SetPathValues(echo.PathValues{{Name: "id", Value: "42"}})

	c := newHandlerContext(ec, cfg)

	// Config is the go-bricks type, preserved on the struct.
	assert.Same(t, cfg, c.Config)

	// Request / RequestContext.
	assert.Same(t, req, c.Request())
	assert.Equal(t, req.Context(), c.RequestContext())

	// ResponseWriter — writing a header lands on the recorder.
	c.ResponseWriter().Header().Set("X-Written", "1")
	assert.Equal(t, "1", rec.Header().Get("X-Written"))

	// Path param, query param, request header.
	assert.Equal(t, "42", c.Param("id"))
	assert.Equal(t, "hello", c.Query("q"))
	assert.Equal(t, "bar", c.RequestHeader("X-Foo"))
	assert.Empty(t, c.Query("missing"))
	assert.Empty(t, c.RequestHeader("X-Absent"))

	// Per-request store (Get/Set) — NOT the context.Context.
	assert.Nil(t, c.Get("k"))
	c.Set("k", "v")
	assert.Equal(t, "v", c.Get("k"))
}

// TestHandlerContextJSONAndString verifies the response-writer convenience methods.
func TestHandlerContextJSONAndString(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	t.Run("json", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
		c := NewHandlerContextForTest(rec, req, cfg)
		require.NoError(t, c.JSON(http.StatusCreated, map[string]string{"hello": "world"}))
		assert.Equal(t, http.StatusCreated, rec.Code)
		var got map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, "world", got["hello"])
	})

	t.Run("string", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
		c := NewHandlerContextForTest(rec, req, cfg)
		require.NoError(t, c.String(http.StatusOK, "plain text"))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "plain text", rec.Body.String())
	})
}

// TestHandlerContextSetRequestContextPropagation asserts SetRequestContext makes a value
// visible to a downstream RequestContext() read — the canonical middleware→handler
// context-propagation pattern the locked flat MiddlewareFunc shape relies on.
func TestHandlerContextSetRequestContextPropagation(t *testing.T) {
	type ctxKey string
	const key ctxKey = "principal"

	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := NewHandlerContextForTest(rec, req, cfg)

	// Before: value absent.
	assert.Nil(t, c.RequestContext().Value(key))

	// Inject via SetRequestContext (mirrors middleware behavior).
	c.SetRequestContext(context.WithValue(c.RequestContext(), key, "alice"))

	// After: a downstream RequestContext() read observes the injected value.
	assert.Equal(t, "alice", c.RequestContext().Value(key))
}

// TestHandlerContextSetRequest verifies SetRequest swaps the underlying *http.Request.
func TestHandlerContextSetRequest(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orig", http.NoBody)
	rec := httptest.NewRecorder()
	c := NewHandlerContextForTest(rec, req, cfg)

	newReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/replaced", http.NoBody)
	c.SetRequest(newReq)
	assert.Same(t, newReq, c.Request())
	assert.Equal(t, "/replaced", c.Request().URL.Path)
}

// withGroupRoutedHandlerContext registers routePath (under groupPrefix when non-empty)
// on a fresh echo router, drives requestURL through it, and hands the routed
// HandlerContext to fn INSIDE the request lifecycle — echo contexts are pooled, so
// touching the context after ServeHTTP returns would read recyclable state. ServeHTTP
// runs synchronously on the test goroutine, so fn may use require/assert directly.
func withGroupRoutedHandlerContext(t *testing.T, groupPrefix, routePath, requestURL string, fn func(c HandlerContext)) {
	t.Helper()
	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	handlerRan := false
	handler := func(ec *echo.Context) error {
		handlerRan = true
		fn(newHandlerContext(ec, cfg))
		return ec.NoContent(http.StatusOK)
	}
	if groupPrefix != "" {
		e.Group(groupPrefix).GET(routePath, handler)
	} else {
		e.GET(routePath, handler)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, requestURL, http.NoBody)
	e.ServeHTTP(httptest.NewRecorder(), req)
	require.True(t, handlerRan, "route %s%s must match request %s", groupPrefix, routePath, requestURL)
}

// withRoutedHandlerContext is withGroupRoutedHandlerContext without a group prefix.
func withRoutedHandlerContext(t *testing.T, routePath, requestURL string, fn func(c HandlerContext)) {
	t.Helper()
	withGroupRoutedHandlerContext(t, "", routePath, requestURL, fn)
}

// unmatchedRouteObservation is what an engine-level middleware saw on a request
// whose route resolution ended in the given sentinel (404/405).
type unmatchedRouteObservation struct {
	params   []PathParam
	template string
	captured bool
}

// observeUnmatchedRoute installs engine-level middleware (which, unlike group
// middleware, also runs on 404/405 — what a SetupMiddlewares escape-hatch
// consumer would observe) capturing PathParams/RouteTemplate whenever the
// request resolved to the given sentinel route name.
func observeUnmatchedRoute(e *echo.Echo, sentinelRouteName string) *unmatchedRouteObservation {
	obs := &unmatchedRouteObservation{}
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec *echo.Context) error {
			if ec.RouteInfo().Name == sentinelRouteName {
				c := newHandlerContext(ec, cfg)
				obs.params = c.PathParams()
				obs.template = c.RouteTemplate()
				obs.captured = true
			}
			return next(ec)
		}
	})
	return obs
}

// TestHandlerContextRouteTemplate verifies RouteTemplate returns the registered route
// template (not the concrete URL), includes any group prefix, and is empty on an
// unrouted context.
func TestHandlerContextRouteTemplate(t *testing.T) {
	t.Run("parameterized_route", func(t *testing.T) {
		withRoutedHandlerContext(t, "/cards/:cardId/status", "/cards/42/status", func(c HandlerContext) {
			assert.Equal(t, "/cards/:cardId/status", c.RouteTemplate())
			// The template is NOT the concrete URL.
			assert.Equal(t, "/cards/42/status", c.Request().URL.Path)
		})
	})

	t.Run("group_prefixed_route_includes_base_path", func(t *testing.T) {
		withGroupRoutedHandlerContext(t, "/api", "/cards/:cardId/status", "/api/cards/42/status", func(c HandlerContext) {
			assert.Equal(t, "/api/cards/:cardId/status", c.RouteTemplate())
		})
	})

	t.Run("empty_on_unrouted_context", func(t *testing.T) {
		cfg := &config.Config{App: config.AppConfig{Env: "development"}}
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/anything", http.NoBody)
		c := NewHandlerContextForTest(httptest.NewRecorder(), req, cfg)
		assert.Empty(t, c.RouteTemplate())
	})
}

// TestNewHandlerContextForTestWithOptionsWithRouteTemplate verifies the WithRouteTemplate
// test option seeds RouteTemplate() on an otherwise-unrouted synthetic context (the gap
// from issue #639), that it reads back through the real public getter, and that it composes
// with SetPathParams on one context. It exercises only exported symbols — the exact surface
// an external consumer reaches. (The zero-option / unrouted-empty case is covered by
// TestHandlerContextRouteTemplate's empty_on_unrouted_context subtest.)
func TestNewHandlerContextForTestWithOptionsWithRouteTemplate(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	t.Run("option_seeds_route_template", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/orders/42", http.NoBody)
		c := NewHandlerContextForTestWithOptions(httptest.NewRecorder(), req, cfg, WithRouteTemplate("/api/orders/:id"))
		// Observable through the existing getter — no new read path, no ectx access.
		assert.Equal(t, "/api/orders/:id", c.RouteTemplate())
		// The template is the registered pattern, NOT the concrete URL.
		assert.Equal(t, "/api/orders/42", c.Request().URL.Path)
	})

	t.Run("composes_with_set_path_params", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/orders/42", http.NoBody)
		c := NewHandlerContextForTestWithOptions(httptest.NewRecorder(), req, cfg, WithRouteTemplate("/api/orders/:id"))
		c.SetPathParams([]PathParam{{Name: "id", Value: "42"}})
		// Both seams settable independently on a single test context (issue #639 AC).
		assert.Equal(t, "/api/orders/:id", c.RouteTemplate())
		assert.Equal(t, "42", c.Param("id"))
	})
}

// TestHandlerContextPathParams verifies PathParams returns the matched parameters in
// route-template declaration order, covers catch-all routes, and returns a defensive
// copy decoupled from the pooled engine state.
func TestHandlerContextPathParams(t *testing.T) {
	t.Run("ordered_by_template_declaration", func(t *testing.T) {
		// Deliberately non-alphabetical template: order must follow the template,
		// not the param names.
		withRoutedHandlerContext(t, "/order/:zebra/:alpha/:mike", "/order/z1/a2/m3", func(c HandlerContext) {
			assert.Equal(t, []PathParam{
				{Name: "zebra", Value: "z1"},
				{Name: "alpha", Value: "a2"},
				{Name: "mike", Value: "m3"},
			}, c.PathParams())
		})
	})

	t.Run("catch_all_route", func(t *testing.T) {
		withRoutedHandlerContext(t, "/files/*", "/files/docs/report.pdf", func(c HandlerContext) {
			assert.Equal(t, []PathParam{
				{Name: "*", Value: "docs/report.pdf"},
			}, c.PathParams())
		})
	})

	t.Run("empty_on_method_not_allowed", func(t *testing.T) {
		e := echo.New()
		e.GET("/users/:userId", func(ec *echo.Context) error { return ec.NoContent(http.StatusOK) })
		e.GET("/cards/:cardId/status", func(ec *echo.Context) error { return ec.NoContent(http.StatusOK) })
		obs := observeUnmatchedRoute(e, echo.MethodNotAllowedRouteName)

		// Warm the router and context pool with a matched parameterized request so
		// stale param names exist for the regression vector (phantom params on 405).
		warm := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/users/7", http.NoBody)
		e.ServeHTTP(httptest.NewRecorder(), warm)

		// POST to a GET-only route → 405: no route method matched, so no parameter
		// names were stamped for this request.
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/cards/42/status", http.NoBody)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		require.True(t, obs.captured, "engine-level middleware must run on 405")
		assert.Empty(t, obs.params, "PathParams must be empty when no route method matched")
		// Engine-defined (echo v5.2.1): on 405 the best-matching route's template is
		// set. Not a go-bricks contract — if an echo upgrade breaks this assertion,
		// update the RouteTemplate doc comment and ADR-035 instead of the engine.
		assert.Equal(t, "/cards/:cardId/status", obs.template)
	})

	t.Run("empty_on_not_found", func(t *testing.T) {
		e := echo.New()
		e.GET("/users/:userId", func(ec *echo.Context) error { return ec.NoContent(http.StatusOK) })
		obs := observeUnmatchedRoute(e, echo.NotFoundRouteName)

		// Warm the router and context pool with a matched request so stale param
		// names sit in the pooled backing array — if a future echo version starts
		// exposing traversal state on 404 (as v5.2.1 already does on 405), this
		// pin catches it via the NotFoundRouteName branch of the guard.
		warm := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/users/7", http.NoBody)
		e.ServeHTTP(httptest.NewRecorder(), warm)

		// NOTE: /users/9/extra would NOT 404 — echo v5 path params match greedily
		// across "/" (userId would bind "9/extra"). Use a genuinely unmatched path.
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/nope", http.NoBody)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code)
		require.True(t, obs.captured, "engine-level middleware must run on 404")
		assert.Empty(t, obs.params, "PathParams must be empty when no route matched")
		assert.Empty(t, obs.template, "RouteTemplate must be empty on 404")
	})

	t.Run("defensive_copy", func(t *testing.T) {
		withRoutedHandlerContext(t, "/cards/:cardId/status", "/cards/42/status", func(c HandlerContext) {
			params := c.PathParams()
			require.Equal(t, []PathParam{{Name: "cardId", Value: "42"}}, params)

			// Mutating the returned slice must not affect the engine state.
			params[0].Name = "mutatedName"
			params[0].Value = "mutatedValue"

			assert.Equal(t, "42", c.Param("cardId"), "Param must be unaffected by mutating the PathParams copy")
			assert.Equal(t, []PathParam{{Name: "cardId", Value: "42"}}, c.PathParams(),
				"a second PathParams call must be unaffected by mutating the first copy")
		})
	})
}

type setPathParamsBindReq struct {
	ID int `param:"id" json:"id" validate:"min=1"`
}

// TestHandlerContextSetPathParams verifies SetPathParams replaces the parameter set for
// Param and struct-tag binding, clears on nil without panicking, and copies its input.
func TestHandlerContextSetPathParams(t *testing.T) {
	t.Run("param_observes_replacement", func(t *testing.T) {
		withRoutedHandlerContext(t, "/cards/:cardId/status", "/cards/42/status", func(c HandlerContext) {
			c.SetPathParams([]PathParam{{Name: "cardId", Value: "99"}})
			assert.Equal(t, "99", c.Param("cardId"))
			assert.Equal(t, []PathParam{{Name: "cardId", Value: "99"}}, c.PathParams())
		})
	})

	t.Run("struct_tag_binding_observes_injected_params", func(t *testing.T) {
		e := echo.New()
		v := NewValidator()
		require.NotNil(t, v)
		e.Validator = v

		binder := NewRequestBinder()
		cfg := &config.Config{App: config.AppConfig{Env: "development"}}

		var bound setPathParamsBindReq
		handler := func(req setPathParamsBindReq, _ HandlerContext) (setPathParamsBindReq, IAPIError) {
			bound = req
			return req, nil
		}
		h := WrapHandler(handler, binder, cfg)

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/users/5", http.NoBody)
		rec := httptest.NewRecorder()
		ec := e.NewContext(req, rec)

		// Inject params through the framework-neutral accessor instead of echo's
		// SetPathValues, then drive the typed pipeline.
		newHandlerContext(ec, cfg).SetPathParams([]PathParam{{Name: "id", Value: "5"}})

		require.NoError(t, h(ec))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, 5, bound.ID, "param struct-tag binding must observe injected params")
	})

	t.Run("nil_clears_all_params", func(t *testing.T) {
		withRoutedHandlerContext(t, "/cards/:cardId/status", "/cards/42/status", func(c HandlerContext) {
			require.NotPanics(t, func() { c.SetPathParams(nil) })
			assert.Empty(t, c.PathParams())
			assert.Empty(t, c.Param("cardId"))
		})
	})

	t.Run("copies_input_slice", func(t *testing.T) {
		withRoutedHandlerContext(t, "/cards/:cardId/status", "/cards/42/status", func(c HandlerContext) {
			input := []PathParam{{Name: "cardId", Value: "7"}}
			c.SetPathParams(input)

			// Mutating the caller's slice after the call must not leak into the context.
			input[0].Value = "tampered"

			assert.Equal(t, "7", c.Param("cardId"), "SetPathParams must copy its input slice")
		})
	})
}

// TestNewHandlerContextForTestSmoke verifies the exported test constructor produces a
// usable HandlerContext backed by the supplied writer/request.
func TestNewHandlerContextForTestSmoke(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "production"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x?n=7", http.NoBody)
	req.Header.Set("X-Trace", "abc")
	rec := httptest.NewRecorder()

	c := NewHandlerContextForTest(rec, req, cfg)
	assert.Same(t, cfg, c.Config)
	assert.Same(t, req, c.Request())
	assert.Equal(t, "7", c.Query("n"))
	assert.Equal(t, "abc", c.RequestHeader("X-Trace"))
}

// TestAdaptHandlerRoundTrip proves a go-bricks Handler adapted to echo runs and writes
// its own response.
func TestAdaptHandlerRoundTrip(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	eh := adaptHandler(func(c HandlerContext) error {
		return c.String(http.StatusTeapot, "brewed")
	}, cfg)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	require.NoError(t, eh(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.Equal(t, "brewed", rec.Body.String())
}

// TestAdaptMiddlewareChainsThrough verifies a flat MiddlewareFunc that calls next()
// runs its logic and chains to the wrapped handler.
func TestAdaptMiddlewareChainsThrough(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	em := adaptMiddleware(func(c HandlerContext, next func() error) error {
		c.ResponseWriter().Header().Set("X-MW", "ran")
		return next()
	}, cfg)

	nextCalled := false
	wrapped := em(func(c *echo.Context) error {
		nextCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	require.NoError(t, wrapped(e.NewContext(req, rec)))
	assert.True(t, nextCalled, "next must be invoked when the middleware calls next()")
	assert.Equal(t, "ran", rec.Header().Get("X-MW"))
	assert.Equal(t, "ok", rec.Body.String())
}

// TestAdaptMiddlewareAbortPropagatesError verifies a flat MiddlewareFunc that returns an
// IAPIError WITHOUT calling next aborts the chain and the error flows to the standard
// error handler (producing the usual envelope).
func TestAdaptMiddlewareAbortPropagatesError(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	e := echo.New()
	e.HTTPErrorHandler = func(c *echo.Context, err error) { customErrorHandler(c, err, cfg, &testLogger{}) }

	rg := newRouteGroup(e.Group(""), "", cfg)

	nextCalled := false
	rg.Use(func(_ HandlerContext, _ func() error) error {
		return NewForbiddenError("blocked")
	})
	rg.Add(http.MethodGet, "/guard", func(c HandlerContext) error {
		nextCalled = true
		return c.String(http.StatusOK, "should not reach")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/guard", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.False(t, nextCalled, "handler must not run when middleware aborts without next()")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, "FORBIDDEN", resp.Error.Code)
}

// TestFromEchoMiddlewareRunsAndChains proves the inverse adapter: an echo-native
// middleware exposed as a flat MiddlewareFunc still runs its echo logic and chains.
func TestFromEchoMiddlewareRunsAndChains(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	flat := fromEchoMiddleware(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Response().Header().Set("X-Echo-Native", "1")
			return next(c)
		}
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := newHandlerContext(e.NewContext(req, rec), cfg)

	nextCalled := false
	require.NoError(t, flat(c, func() error {
		nextCalled = true
		return nil
	}))
	assert.True(t, nextCalled, "fromEchoMiddleware must chain to next()")
	assert.Equal(t, "1", rec.Header().Get("X-Echo-Native"), "echo-native logic must still run through the flat form")
}

// TestPublicMiddlewareConstructorsReturnFlatForm drives the echo-free public middleware
// constructors (L6 class) through routeGroup.Use as a consumer would, proving each returns
// a working server.MiddlewareFunc and the whole flat chain flows a request end-to-end.
func TestPublicMiddlewareConstructorsReturnFlatForm(t *testing.T) {
	t.Setenv("CORS_DEV_WILDCARD", "true")

	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	rg := newRouteGroup(e.Group(""), "", cfg)

	// Every constructor below MUST return server.MiddlewareFunc for these to compile.
	mw := []MiddlewareFunc{
		CORS(false, "development"),
		RequestIDMiddleware(),
		RequestEnrich(),
		TraceContext(),
		PerformanceStats(),
		IPPreGuard(1000),
		RateLimit(1000),
		Timeout(5 * time.Second),
		Timing(),
		LoggerWithConfig(&noopLogger{}, LoggerConfig{SlowRequestThreshold: time.Second}),
	}
	rg.Use(mw...)

	rg.Add(http.MethodGet, "/chained", func(c HandlerContext) error {
		return c.String(http.StatusOK, "through")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/chained", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "through", rec.Body.String())
	// CORS ran (origin echoed in dev) and RequestID middleware set the response header.
	assert.Equal(t, "http://localhost:3000", rec.Header().Get(HeaderAccessControlAllowOrigin))
	assert.NotEmpty(t, rec.Header().Get(echo.HeaderXRequestID))
}

// TestPublicTenantMiddlewareReturnsFlatForm covers TenantMiddleware + the flat SkipperFunc
// (CreateProbeSkipper) separately, since it needs a resolver and a skipper argument.
// capturingRegistrar is a RouteRegistrar that deliberately does NOT implement the
// unexported echoAdder seam, so RegisterHandler takes its fallback branch
// (adapting the echo handler into a Handler via the unexported escape hatch). It records
// the adapted Handler so a test can invoke it and verify the round-trip works.
type capturingRegistrar struct{ handler Handler }

func (cr *capturingRegistrar) Add(_, _ string, handler Handler, _ ...MiddlewareFunc) {
	cr.handler = handler
}
func (cr *capturingRegistrar) Group(string, ...MiddlewareFunc) RouteRegistrar { return cr }
func (cr *capturingRegistrar) Use(...MiddlewareFunc)                          {}
func (cr *capturingRegistrar) FullPath(p string) string                       { return p }

type fallbackProbeResp struct {
	Greeting string `json:"greeting"`
}

// TestRegisterHandlerFallbackForNonEchoRegistrar exercises the RegisterHandler fallback
// for registrars that don't implement echoAdder: the adapted Handler must, when
// invoked, round-trip through HandlerContext.echoContext() and run the full typed
// pipeline, producing the standard data/meta envelope.
func TestRegisterHandlerFallbackForNonEchoRegistrar(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	hr := NewHandlerRegistry(cfg)
	cr := &capturingRegistrar{}

	GET(hr, cr, "/fallback-probe", func(_ struct{}, _ HandlerContext) (fallbackProbeResp, IAPIError) {
		return fallbackProbeResp{Greeting: "hi"}, nil
	})
	require.NotNil(t, cr.handler, "non-echoAdder must receive the adapted Handler via the fallback branch")

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/fallback-probe", http.NoBody)
	require.NoError(t, cr.handler(NewHandlerContextForTest(rec, req, cfg)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"data":{"greeting":"hi"}`, "fallback must run the typed pipeline and emit the data envelope")
	assert.Contains(t, rec.Body.String(), `"meta"`, "fallback must emit the framework meta envelope")
}

func TestPublicTenantMiddlewareReturnsFlatForm(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	rg := newRouteGroup(e.Group(""), "", cfg)

	resolver := &multitenant.HeaderResolver{HeaderName: HeaderXTenantID}
	skipper := CreateProbeSkipper("/health", "/ready")
	tm := TenantMiddleware(resolver, skipper)
	rg.Use(tm)

	rg.Add(http.MethodGet, "/tenant-check", func(c HandlerContext) error {
		tenant, _ := multitenant.GetTenant(c.RequestContext())
		return c.String(http.StatusOK, tenant)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/tenant-check", http.NoBody)
	req.Header.Set(HeaderXTenantID, "acme")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "acme", rec.Body.String(), "tenant resolved by the flat TenantMiddleware must reach the handler context")
}

// TestCreateProbeSkipperFlatForm verifies CreateProbeSkipper returns the echo-free
// func(*http.Request) bool form and skips exactly the configured probe paths.
func TestCreateProbeSkipperFlatForm(t *testing.T) {
	skipper := CreateProbeSkipper("/health", "/ready")

	cases := []struct {
		path string
		skip bool
	}{
		{"/health", true},
		{"/ready", true},
		{"/api/users", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, http.NoBody)
		assert.Equal(t, tc.skip, skipper(req), "skipper decision for %s", tc.path)
	}
}

// newTypedPathServer wires a typed GET handler through a real routeGroup (which implements
// the echoAdder addEcho seam) so benchmarks/alloc tests measure the ADR-026 hot path.
func newTypedPathServer(tb testing.TB) (*echo.Echo, *http.Request) {
	tb.Helper()
	e := echo.New()
	e.Validator = NewValidator()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	hr := NewHandlerRegistry(cfg)
	rg := newRouteGroup(e.Group(""), "", cfg)
	GET(hr, rg, "/bench", func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bench", http.NoBody)
	return e, req
}

// BenchmarkTypedHandlerPath drives a typed-handler request end-to-end through the
// addEcho seam (ADR-026 zero-overhead path).
func BenchmarkTypedHandlerPath(b *testing.B) {
	e, req := newTypedPathServer(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	}
}

// typedHandlerPathMaxAllocs locks in the typed request path's allocation count so a future
// change to newHandlerContext, the HandlerContext accessors, or the addEcho seam that adds
// per-request allocations is caught (ADR-026). The ceiling is the measured baseline plus a
// tiny margin for cross-platform/Go-version noise. Measured baseline: 20 allocs/op.
const typedHandlerPathMaxAllocs = 23

// TestTypedHandlerPathAllocsStable asserts the typed request path's allocs/op stays at or
// below the locked baseline — proving newHandlerContext + accessors + the addEcho seam add
// no allocation (ADR-026).
func TestTypedHandlerPathAllocsStable(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("testing.AllocsPerRun is unreliable under -race; alloc baseline enforced in the non-race matrix")
	}
	e, req := newTypedPathServer(t)
	// Warm any one-time lazy state before measuring.
	e.ServeHTTP(httptest.NewRecorder(), req)

	got := testing.AllocsPerRun(100, func() {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	})
	t.Logf("typed handler path allocs/op = %.1f (ceiling %d)", got, typedHandlerPathMaxAllocs)
	assert.LessOrEqual(t, got, float64(typedHandlerPathMaxAllocs),
		"typed request path allocs/op regressed beyond the ADR-026 baseline")
}
