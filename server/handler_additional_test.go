package server

import (
	"encoding/json"
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

type numericRequest struct {
	AccountID uint    `param:"accountID"`
	Limit     uint16  `query:"limit"`
	Ratio     float32 `query:"ratio"`
}

func TestRequestBinder_BindsUnsignedAndFloatValues(t *testing.T) {
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

func TestRequestBinder_InvalidFloatReturnsError(t *testing.T) {
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

	data, ok := resp.Data.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "true", data["ok"])
	assert.NotEmpty(t, resp.Meta["traceId"])
	assert.NotEmpty(t, resp.Meta["timestamp"])
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

func TestHandlerRegistryRegistersRoutes(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	hr := NewHandlerRegistry(cfg)
	require.NotNil(t, hr)

	e := echo.New()
	v := NewValidator()
	require.NotNil(t, v)
	e.Validator = v

	type emptyReq struct{}

	handler := func(emptyReq, HandlerContext) (NoContentResult, IAPIError) {
		return NoContent(), nil
	}

	RegisterHandler(hr, e, http.MethodGet, "/custom", handler)
	GET(hr, e, "/get", handler)
	POST(hr, e, "/post", handler)
	PUT(hr, e, "/put", handler)
	DELETE(hr, e, "/delete", handler)
	PATCH(hr, e, "/patch", handler)

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
	}

	for _, key := range expected {
		_, ok := routes[key]
		assert.True(t, ok, "expected route %s to be registered", key)
	}
}

func TestParseTimeRFC3339NanoStillWorks(t *testing.T) {
	now := time.Now().UTC()
	formatted := now.Format(time.RFC3339Nano)

	parsed, err := parseTime(formatted)
	require.NoError(t, err)
	assert.WithinDuration(t, now, parsed, time.Nanosecond)
}
