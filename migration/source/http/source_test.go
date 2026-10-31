package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/multitenant"
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
					require.ErrorIs(t, err, tt.errIs)
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
		assert.Empty(t, r.URL.Query().Get("cursor"))
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
			assert.Empty(t, r.URL.Query().Get("cursor"))
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
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(_ stdhttp.ResponseWriter, r *stdhttp.Request) {
		// Outlast the client deadline, but unblock srv.Close() as soon as the
		// client gives up instead of holding the handler for the full 200ms.
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
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

func TestHTTPTenantSourceRefusesOffGrammarIDs(t *testing.T) {
	cases := []struct {
		name   string
		pages  [][]string
		wantAt string
	}{
		{"uppercase", [][]string{{"good", "Acme"}}, "page 1, entry 2"},
		{"underscore", [][]string{{"acme_1"}}, "page 1, entry 1"},
		{"over_64_bytes", [][]string{{strings.Repeat("a", 65)}}, "page 1, entry 1"},
		{"newline", [][]string{{"a\nb"}}, "page 1, entry 1"},
		{"escape_byte", [][]string{{"a\x1b[31mb"}}, "page 1, entry 1"},
		{"second_page", [][]string{{"a", "b"}, {"c", "Bad"}}, "page 2, entry 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := listPages(t, tc.pages...)
			require.ErrorIs(t, err, multitenant.ErrInvalidTenantID)
			assert.Contains(t, err.Error(), tc.wantAt)
		})
	}

	t.Run("error_carries_no_value", func(t *testing.T) {
		_, err := listPages(t, []string{"Secret_Tenant\x1b[31m\nX"})
		require.ErrorIs(t, err, multitenant.ErrInvalidTenantID)
		assert.NotContains(t, err.Error(), "Secret_Tenant")
		assert.NotContains(t, err.Error(), "\x1b")
		assert.NotContains(t, err.Error(), "\n")
	})

	t.Run("no_partial_listing", func(t *testing.T) {
		got, err := listPages(t, []string{"a", "b"}, []string{"Bad"})
		require.ErrorIs(t, err, multitenant.ErrInvalidTenantID)
		assert.Nil(t, got)
	})
}

func TestHTTPTenantSourceAcceptsGrammarIDs(t *testing.T) {
	cases := []struct {
		name  string
		pages [][]string
		want  []string
	}{
		{"lowercase", [][]string{{"acme"}}, []string{"acme"}},
		{"max_64_bytes", [][]string{{strings.Repeat("a", 64)}}, []string{strings.Repeat("a", 64)}},
		{"padded_whitespace_trimmed", [][]string{{"  acme\t"}}, []string{"acme"}},
		{"multi_page", [][]string{{"a", "b"}, {"c"}}, []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listPages(t, tc.pages...)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestContractErrorQuotesControlPlaneText(t *testing.T) {
	cases := []struct {
		name string
		err  ContractError
		want string
	}{
		{
			name: "newline",
			err:  ContractError{StatusCode: 502, Code: "BAD\nCODE", Message: "line1\nline2"},
			want: `control-plane returned HTTP 502: "BAD\nCODE": "line1\nline2"`,
		},
		{
			name: "escape_byte",
			err:  ContractError{StatusCode: 502, Code: "E\x1b[31m", Message: "\x1b]0;title\x07"},
			want: `control-plane returned HTTP 502: "E\x1b[31m": "\x1b]0;title\a"`,
		},
		{
			name: "plain_text_unchanged_inside_quotes",
			err:  ContractError{StatusCode: 401, Code: "AUTH_FAILED", Message: "invalid token"},
			want: `control-plane returned HTTP 401: "AUTH_FAILED": "invalid token"`,
		},
		{
			name: "no_envelope_unchanged",
			err:  ContractError{StatusCode: 503},
			want: "control-plane returned HTTP 503",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.err.Error())
		})
	}
}

// listPages serves each element of pages as one listing page, chained by
// cursor, and returns what ListTenants makes of the whole listing.
func listPages(t *testing.T, pages ...[]string) ([]string, error) {
	t.Helper()
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		idx := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			n, err := strconv.Atoi(c)
			if err != nil || n >= len(pages) {
				t.Errorf("unexpected cursor %q", c)
				return
			}
			idx = n
		}
		tenants := make([]map[string]string, 0, len(pages[idx]))
		for _, id := range pages[idx] {
			tenants = append(tenants, map[string]string{"id": id})
		}
		next := ""
		if idx+1 < len(pages) {
			next = strconv.Itoa(idx + 1)
		}
		writeJSON(w, stdhttp.StatusOK, writeEnvelope{
			Data: map[string]any{"tenants": tenants, "next_cursor": next},
			Meta: map[string]any{},
		})
	}))
	t.Cleanup(srv.Close)

	src, err := New(srv.URL, Options{AllowInsecureScheme: true})
	require.NoError(t, err)
	return src.ListTenants(context.Background())
}

func TestHTTPTenantSourceRefusesSchemeDowngradeRedirect(t *testing.T) {
	srv := httptest.NewTLSServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		stdhttp.Redirect(w, r, "http://"+r.Host+"/tenants", stdhttp.StatusFound) // NOSONAR: test fixture simulates the redirect attack redirectGuard must refuse (S5146 targets production handlers)
	}))
	defer srv.Close()

	hc := srv.Client() // trusts the test cert; its CheckRedirect is nil, so the guard installs
	src, err := New(srv.URL, Options{Client: hc})
	require.NoError(t, err)

	_, err = src.ListTenants(context.Background())
	require.Error(t, err)
	assert.Nil(t, hc.CheckRedirect, "New must not mutate the caller's client")
	// Last, so a wrong error kind still leaves the mutation check above reported.
	assert.ErrorIs(t, err, ErrInsecureScheme) // must be the scheme branch, not the host branch
}

func TestHTTPTenantSourceRefusesOffHostRedirect(t *testing.T) {
	srv := httptest.NewTLSServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		stdhttp.Redirect(w, r, "https://other-host.invalid/tenants", stdhttp.StatusFound)
	}))
	defer srv.Close()

	hc := srv.Client()
	src, err := New(srv.URL, Options{Client: hc})
	require.NoError(t, err)

	_, err = src.ListTenants(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "off-host")
	assert.NotErrorIs(t, err, ErrInsecureScheme)
}

// locationSecrets are the parts of a credentialed URL no error may render; "token="
// because ErrInsecureScheme's own text names the bearer token.
var locationSecrets = []string{"zq9x", "u:", "token=", "frag"}

// TestHTTPTenantSourceRedirectErrorDropsLocationCredentials pins both refusals: net/http
// renders the raw Location in its *url.Error, around the guard's own rendering. It quotes
// a Location it cannot parse in its reason.
func TestHTTPTenantSourceRedirectErrorDropsLocationCredentials(t *testing.T) {
	const hostSlot = "{host}"
	tests := []struct {
		name     string
		location string
		want     string
	}{
		{
			name:     "off_host",
			location: "https://u:zq9x@other-host.invalid/tenants?token=zq9x#frag",
			want:     `request control-plane: Get "https://other-host.invalid/tenants?xxxxx": control-plane redirected off-host to https://other-host.invalid/tenants?xxxxx; refusing to follow`,
		},
		{
			name:     "scheme_downgrade",
			location: "http://u:zq9x@{host}/tenants?token=zq9x#frag",
			want:     `request control-plane: Get "http://{host}/tenants?xxxxx": ` + ErrInsecureScheme.Error() + `: redirect to http://{host}/tenants?xxxxx downgrades to http`,
		},
		{
			name:     "unparseable_scheme_relative_location",
			location: "//u:zq9x%zz@api.example/x",
			want:     `request control-plane: Get "https://{host}/tenants?xxxxx": failed to parse Location header "xxxxx": parse "xxxxx": invalid URL escape "xxxxx"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				stdhttp.Redirect(w, r, strings.ReplaceAll(tc.location, hostSlot, r.Host), stdhttp.StatusFound)
			}))
			defer srv.Close()
			src, err := New(srv.URL, Options{Client: srv.Client()})
			require.NoError(t, err)

			_, err = src.ListTenants(context.Background())
			require.Error(t, err)
			assert.Equal(t, strings.ReplaceAll(tc.want, hostSlot, srv.Listener.Addr().String()), err.Error())
			for _, secret := range locationSecrets {
				assert.NotContains(t, err.Error(), secret)
			}
		})
	}
}

func TestHTTPTenantSourceNewErrorsWithholdCredentials(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "invalid_escape_after_password", base: "https://u:zq9x%zz@api.example/x?token=zq9x#frag", want: `migration/source/http: parse baseURL: parse "https://api.example/x?xxxxx": invalid URL`},
		{name: "missing_host", base: "https://u:zq9x@/x?token=zq9x#frag", want: `migration/source/http: baseURL must include scheme and host: "https:///x?xxxxx"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.base, Options{})
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
			for _, secret := range locationSecrets {
				assert.NotContains(t, err.Error(), secret)
			}
		})
	}
}

func TestHTTPTenantSourceHonorsCallerCheckRedirect(t *testing.T) {
	plainSrv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeJSON(w, stdhttp.StatusOK, writeEnvelope{
			Data: map[string]any{
				"tenants":     []map[string]string{{"id": "t1"}},
				"next_cursor": "",
			},
			Meta: map[string]any{},
		})
	}))
	defer plainSrv.Close()

	tlsSrv := httptest.NewTLSServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		stdhttp.Redirect(w, r, plainSrv.URL+"/tenants", stdhttp.StatusFound)
	}))
	defer tlsSrv.Close()

	hc := tlsSrv.Client()
	hc.CheckRedirect = func(*stdhttp.Request, []*stdhttp.Request) error { return nil }

	src, err := New(tlsSrv.URL, Options{Client: hc})
	require.NoError(t, err)

	got, err := src.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"t1"}, got)
}

func TestHTTPTenantSourceStopsAfterTenRedirects(t *testing.T) {
	var hits int32
	srv := httptest.NewTLSServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		atomic.AddInt32(&hits, 1)
		stdhttp.Redirect(w, r, "https://"+r.Host+"/tenants", stdhttp.StatusFound) // NOSONAR: test fixture simulates the redirect attack redirectGuard must refuse (S5146 targets production handlers)
	}))
	defer srv.Close()

	hc := srv.Client()
	src, err := New(srv.URL, Options{Client: hc})
	require.NoError(t, err)

	_, err = src.ListTenants(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped after 10 redirects")
	assert.Equal(t, int32(10), atomic.LoadInt32(&hits))
}

func TestHTTPTenantSourceRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), 1025))
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{AllowInsecureScheme: true, MaxResponseBytes: 1024})
	require.NoError(t, err)

	_, err = src.ListTenants(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseTooLarge)
}

func TestHTTPTenantSourceAcceptsResponseAtLimit(t *testing.T) {
	const body = `{"data":{"tenants":[{"id":"t1"}],"next_cursor":""},"meta":{}}`
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	src, err := New(srv.URL, Options{AllowInsecureScheme: true, MaxResponseBytes: int64(len(body))})
	require.NoError(t, err)

	got, err := src.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"t1"}, got)
}

func writeJSON(w stdhttp.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
