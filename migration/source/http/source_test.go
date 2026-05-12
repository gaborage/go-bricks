package http

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// envelope mirrors the production envelope but is defined locally so tests
// don't reach into unexported types.
type writeEnvelope struct {
	Data  map[string]any `json:"data,omitempty"`
	Error map[string]any `json:"error,omitempty"`
	Meta  map[string]any `json:"meta"`
}

func TestHTTPTenantSourceNewValidation(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		opts    Options
		wantErr bool
		errIs   error
	}{
		{name: "valid_https", base: "https://api.example.com"},
		{name: "https_with_insecure_flag_still_works", base: "https://api.example.com", opts: Options{AllowInsecureScheme: true}},
		{name: "http_rejected_by_default", base: "http://localhost:8080/", wantErr: true, errIs: ErrInsecureScheme},
		{name: "http_allowed_with_opt_out", base: "http://localhost:8080/", opts: Options{AllowInsecureScheme: true}},
		{name: "empty_url", base: "", wantErr: true},
		{name: "missing_scheme", base: "api.example.com", wantErr: true},
		{name: "missing_host", base: "https://", wantErr: true},
		{name: "ftp_scheme_rejected", base: "ftp://api.example.com", wantErr: true, errIs: ErrUnsupportedScheme},
		{name: "file_scheme_rejected", base: "file://server/path", wantErr: true, errIs: ErrUnsupportedScheme},
		// url.Parse lowercases the scheme per RFC 3986; document that here so a
		// future Go change that drops normalization fails this test.
		{name: "uppercase_http_rejected", base: "HTTP://localhost", wantErr: true, errIs: ErrInsecureScheme},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.base, tt.opts)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errIs != nil {
					assert.ErrorIs(t, err, tt.errIs)
				}
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestHTTPTenantSourceListTenantsSinglePage(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		assert.Equal(t, "/tenants", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("limit"))
		assert.Equal(t, "", r.URL.Query().Get("cursor"))
		assert.Empty(t, r.Header.Get("Authorization"))

		writeJSON(w, stdhttp.StatusOK, writeEnvelope{
			Data: map[string]any{
				"tenants":     []map[string]string{{"id": "t1"}, {"id": "t2"}},
				"next_cursor": "",
			},
			Meta: map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339)},
		})
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{AllowInsecureScheme: true})
	require.NoError(t, err)

	got, err := src.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"t1", "t2"}, got)
}

func TestHTTPTenantSourceListTenantsPagination(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		n := atomic.AddInt32(&calls, 1)
		switch n {
		case 1:
			assert.Equal(t, "", r.URL.Query().Get("cursor"))
			writeJSON(w, stdhttp.StatusOK, writeEnvelope{
				Data: map[string]any{
					"tenants":     []map[string]string{{"id": "a"}, {"id": "b"}},
					"next_cursor": "page2",
				},
				Meta: map[string]any{},
			})
		case 2:
			assert.Equal(t, "page2", r.URL.Query().Get("cursor"))
			writeJSON(w, stdhttp.StatusOK, writeEnvelope{
				Data: map[string]any{
					"tenants":     []map[string]string{{"id": "c"}},
					"next_cursor": "",
				},
				Meta: map[string]any{},
			})
		default:
			t.Fatalf("unexpected extra page request: %d", n)
		}
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{AllowInsecureScheme: true})
	require.NoError(t, err)

	got, err := src.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, got)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestHTTPTenantSourceBearerTokenForwarded(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		assert.Equal(t, "Bearer secret-token", r.Header.Get("Authorization"))
		writeJSON(w, stdhttp.StatusOK, writeEnvelope{
			Data: map[string]any{"tenants": []any{}, "next_cursor": ""},
			Meta: map[string]any{},
		})
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{BearerToken: "secret-token", AllowInsecureScheme: true})
	require.NoError(t, err)

	_, err = src.ListTenants(context.Background())
	require.NoError(t, err)
}

func TestHTTPTenantSourceContractErrorWithEnvelope(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeJSON(w, stdhttp.StatusUnauthorized, writeEnvelope{
			Error: map[string]any{"code": "AUTH_FAILED", "message": "invalid token"},
			Meta:  map[string]any{},
		})
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{AllowInsecureScheme: true})
	require.NoError(t, err)

	_, err = src.ListTenants(context.Background())
	require.Error(t, err)

	var ce *ContractError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, stdhttp.StatusUnauthorized, ce.StatusCode)
	assert.Equal(t, "AUTH_FAILED", ce.Code)
	assert.Equal(t, "invalid token", ce.Message)
}

func TestHTTPTenantSourceContractErrorWithoutEnvelope(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.WriteHeader(stdhttp.StatusInternalServerError)
		_, _ = w.Write([]byte("oops"))
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{AllowInsecureScheme: true})
	require.NoError(t, err)

	_, err = src.ListTenants(context.Background())
	require.Error(t, err)

	var ce *ContractError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, stdhttp.StatusInternalServerError, ce.StatusCode)
}

func TestHTTPTenantSourceContextCancellation(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(_ stdhttp.ResponseWriter, _ *stdhttp.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{
		Client:              &stdhttp.Client{Timeout: 5 * time.Second},
		AllowInsecureScheme: true,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = src.ListTenants(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled),
		"expected context error, got %v", err)
}

func TestHTTPTenantSourcePageLimitOverride(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		assert.Equal(t, 25, limit)
		writeJSON(w, stdhttp.StatusOK, writeEnvelope{
			Data: map[string]any{"tenants": []any{}, "next_cursor": ""},
			Meta: map[string]any{},
		})
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{PageLimit: 25, AllowInsecureScheme: true})
	require.NoError(t, err)

	_, err = src.ListTenants(context.Background())
	require.NoError(t, err)
}

func TestHTTPTenantSourceMissingDataField(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeJSON(w, stdhttp.StatusOK, writeEnvelope{Meta: map[string]any{}})
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{AllowInsecureScheme: true})
	require.NoError(t, err)

	_, err = src.ListTenants(context.Background())
	assert.Error(t, err)
}

func TestHTTPTenantSourceSkipsEmptyIDs(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeJSON(w, stdhttp.StatusOK, writeEnvelope{
			Data: map[string]any{
				"tenants":     []map[string]string{{"id": "good"}, {"id": ""}, {"id": "  "}, {"id": "also-good"}},
				"next_cursor": "",
			},
			Meta: map[string]any{},
		})
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{AllowInsecureScheme: true})
	require.NoError(t, err)

	got, err := src.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"good", "also-good"}, got)
}

func writeJSON(w stdhttp.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
