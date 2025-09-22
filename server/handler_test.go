package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestWrapHandlerSuccessDefaultStatus(t *testing.T) {
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

func TestWrapHandlerSuccessCustomStatusWithResult(t *testing.T) {
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

func TestWrapHandlerValidationError(t *testing.T) {
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
		_, ok := ve.([]any)
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

type numericRequest struct {
	AccountID uint    `param:"accountID"`
	Limit     uint16  `query:"limit"`
	Ratio     float32 `query:"ratio"`
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

	req := httptest.NewRequest(http.MethodGet, "/accounts/7?limit=42&ratio=3.5", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("accountID")
	c.SetParamValues("7")

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

	req := httptest.NewRequest(http.MethodGet, "/accounts/7?limit=42&ratio=not-a-number", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("accountID")
	c.SetParamValues("7")

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
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
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

func TestFormatSuccessResponseWithStatusDefaultsWhenZero(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
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

func TestWrapHandlerValidateOtherErrorInDevIncludesErrorDetail(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/hello?name=John", http.NoBody)
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

func TestHandlerRegistryRegistersRoutes(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	hr := NewHandlerRegistry(cfg)
	require.NotNil(t, hr)

	e := echo.New()
	v := NewValidator()
	require.NotNil(t, v)
	e.Validator = v
	registrar := newRouteGroup(e.Group(""), "")

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
	for _, route := range e.Routes() {
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

func sampleHandlerFunc() {}

type sampleReceiver struct{}

func (sampleReceiver) sampleMethod() {}

type packageCaller struct{}

func (packageCaller) callPackage() string {
	return packageCallerNested()
}

func packageCallerNested() string {
	return getCallerPackage()
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
