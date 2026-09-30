package httpclient

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const redirectSecret = "tok-redirect"

// redirectTo answers every request with a 302 to target and counts the requests.
func redirectTo(t *testing.T, target string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		hits.Add(1)
		nethttp.Redirect(w, r, target, nethttp.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// downgradeServers returns an https server redirecting to a plain http server on
// the same hostname and another port, which records every request it receives.
func downgradeServers(t *testing.T) (secure *httptest.Server, hits *atomic.Int64, seen func() []string) {
	t.Helper()
	plain, seen := authServer(t)
	secure, hits = redirectTo(t, plain.URL+"/landing")
	return secure, hits, seen
}

func TestClientRefusesCredentialedRedirectDowngrade(t *testing.T) {
	interceptor := func(_ context.Context, r *nethttp.Request) error {
		r.Header.Set(headerAuthorization, "Bearer "+redirectSecret)
		return nil
	}
	tests := []struct {
		name      string
		configure func(*Builder) *Builder
		req       Request
	}{
		{
			name:      "default_authorization_header",
			configure: func(b *Builder) *Builder { return b.WithDefaultHeader(headerAuthorization, "Bearer "+redirectSecret) },
		},
		{
			name:      "builder_basic_auth",
			configure: func(b *Builder) *Builder { return b.WithBasicAuth("user", redirectSecret) },
		},
		{
			name: "request_authorization_header",
			req:  Request{Headers: map[string]string{headerAuthorization: "Bearer " + redirectSecret}},
		},
		{
			name: "request_basic_auth",
			req:  Request{Auth: &BasicAuth{Username: "user", Password: redirectSecret}},
		},
		{
			name:      "request_interceptor",
			configure: func(b *Builder) *Builder { return b.WithRequestInterceptor(interceptor) },
		},
		{
			name: "cookie_only",
			req:  Request{Headers: map[string]string{"Cookie": "session=" + redirectSecret}},
		},
		{
			name: "proxy_authorization_only",
			req:  Request{Headers: map[string]string{"Proxy-Authorization": "Basic " + redirectSecret}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			secure, hits, seen := downgradeServers(t)
			b := NewBuilder(quietLogger()).WithTransport(secure.Client().Transport)
			if tc.configure != nil {
				b = tc.configure(b)
			}
			c, err := b.Build()
			require.NoError(t, err)

			req := tc.req
			req.URL = secure.URL
			_, err = c.Get(context.Background(), &req)
			require.ErrorIs(t, err, ErrRedirectDowngrade)
			assert.NotContains(t, err.Error(), redirectSecret)
			assert.Empty(t, seen(), "the http hop must never be requested")
			assert.Equal(t, int64(1), hits.Load())
		})
	}
}

func TestClientRefusesRedirectDowngradeWithoutRetry(t *testing.T) {
	secure, hits, seen := downgradeServers(t)
	c, err := NewBuilder(quietLogger()).
		WithTransport(secure.Client().Transport).
		WithRetries(2, time.Millisecond).
		WithDefaultHeader(headerAuthorization, "Bearer "+redirectSecret).
		Build()
	require.NoError(t, err)

	_, err = c.Get(context.Background(), &Request{URL: secure.URL})
	require.ErrorIs(t, err, ErrRedirectDowngrade)
	assert.NotContains(t, err.Error(), redirectSecret)
	assert.Empty(t, seen(), "the http hop must never be requested")
	assert.Equal(t, int64(1), hits.Load(), "a refused downgrade is terminal, not retried")
}

func TestClientFollowsRedirectDowngradeWithoutCredential(t *testing.T) {
	secure, _, seen := downgradeServers(t)
	c, err := NewBuilder(quietLogger()).WithTransport(secure.Client().Transport).Build()
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), &Request{URL: secure.URL, Headers: map[string]string{"X-Trace": "t"}})
	require.NoError(t, err)
	assert.Equal(t, nethttp.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{""}, seen())
}

func TestCheckRedirectAllowsDowngradeWithoutCredential(t *testing.T) {
	first := &nethttp.Request{URL: &url.URL{Scheme: "https", Host: "api.example.com"}}
	hop := &nethttp.Request{URL: &url.URL{Scheme: "http", Host: "cdn.example.net"}, Header: nethttp.Header{}}
	require.NoError(t, checkRedirect(hop, []*nethttp.Request{first}), "net/http stripped the credentials, so nothing leaks")

	hop.Header.Set(headerAuthorization, "Bearer x")
	require.ErrorIs(t, checkRedirect(hop, []*nethttp.Request{first}), ErrRedirectDowngrade)
}

func TestClientGuardsCallerClientWithoutRedirectPolicy(t *testing.T) {
	secure, _, seen := downgradeServers(t)
	custom := secure.Client()
	require.Nil(t, custom.CheckRedirect)

	c, err := NewBuilder(quietLogger()).
		WithHTTPClient(custom).
		WithDefaultHeader(headerAuthorization, "Bearer "+redirectSecret).
		Build()
	require.NoError(t, err)

	_, err = c.Get(context.Background(), &Request{URL: secure.URL})
	require.ErrorIs(t, err, ErrRedirectDowngrade)
	assert.Empty(t, seen())
	assert.Nil(t, custom.CheckRedirect, "Build must not mutate the caller's client")
}

func TestClientKeepsCallerRedirectPolicy(t *testing.T) {
	secure, _, seen := downgradeServers(t)
	custom := secure.Client()
	custom.CheckRedirect = func(*nethttp.Request, []*nethttp.Request) error { return nil }

	c, err := NewBuilder(quietLogger()).
		WithHTTPClient(custom).
		WithDefaultHeader(headerAuthorization, "Bearer "+redirectSecret).
		Build()
	require.NoError(t, err)

	_, err = c.Get(context.Background(), &Request{URL: secure.URL})
	require.NoError(t, err)
	assert.Equal(t, []string{"Bearer " + redirectSecret}, seen(), "the caller's policy governs, downgrade included")
}

func TestClientFollowsSameSchemeRedirectWithAuthorization(t *testing.T) {
	tests := []struct {
		name   string
		newSrv func(nethttp.Handler) *httptest.Server
	}{
		{name: "https_to_https", newSrv: httptest.NewTLSServer},
		{name: "http_to_http", newSrv: httptest.NewServer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var landed atomic.Value
			srv := tc.newSrv(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
				if r.URL.Path == "/start" {
					nethttp.Redirect(w, r, "/landing", nethttp.StatusFound)
					return
				}
				landed.Store(r.Header.Get(headerAuthorization))
			}))
			t.Cleanup(srv.Close)

			c, err := NewBuilder(quietLogger()).
				WithHTTPClient(srv.Client()).
				WithDefaultHeader(headerAuthorization, "Bearer "+redirectSecret).
				Build()
			require.NoError(t, err)

			resp, err := c.Get(context.Background(), &Request{URL: srv.URL + "/start"})
			require.NoError(t, err)
			assert.Equal(t, nethttp.StatusOK, resp.StatusCode)
			assert.Equal(t, "Bearer "+redirectSecret, landed.Load())
		})
	}
}

func TestClientCapsRedirectsAtTen(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if n < maxRedirects {
			nethttp.Redirect(w, r, "/"+strconv.Itoa(n+1), nethttp.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := NewBuilder(quietLogger()).Build()
	require.NoError(t, err)

	_, err = c.Get(context.Background(), &Request{URL: srv.URL + "/1"})
	require.NoError(t, err, "nine redirects are followed")
	_, err = c.Get(context.Background(), &Request{URL: srv.URL + "/0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "httpclient: stopped after 10 redirects")
}

func TestBearerTokenFileRefusesRedirectDowngradeWithoutRetry(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte(redirectSecret))
	secure, hits, seen := downgradeServers(t)

	c, err := NewBuilder(quietLogger()).
		WithHTTPClient(secure.Client()).
		WithRetries(2, time.Millisecond).
		WithBearerTokenFile(path, BearerTokenFileOptions{}).
		Build()
	require.NoError(t, err)

	_, err = c.Get(context.Background(), &Request{URL: secure.URL})
	require.ErrorIs(t, err, ErrRedirectDowngrade)
	assert.NotContains(t, err.Error(), redirectSecret)
	assert.Empty(t, seen(), "the http hop must never be requested")
	assert.Equal(t, int64(1), hits.Load(), "a refused downgrade is terminal, not retried")
}

func TestBearerTokenFileFollowsSameSchemeRedirect(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte(redirectSecret))
	var landed atomic.Value
	secure := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/start" {
			nethttp.Redirect(w, r, "/landing", nethttp.StatusFound)
			return
		}
		landed.Store(r.Header.Get(headerAuthorization))
	}))
	t.Cleanup(secure.Close)

	c, err := NewBuilder(quietLogger()).WithHTTPClient(secure.Client()).WithBearerTokenFile(path, BearerTokenFileOptions{}).Build()
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), &Request{URL: secure.URL + "/start"})
	require.NoError(t, err)
	assert.Equal(t, nethttp.StatusOK, resp.StatusCode)
	assert.Equal(t, "Bearer "+redirectSecret, landed.Load())
}

func TestBearerTokenFileKeepsCallerRedirectPolicy(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte(redirectSecret))
	secure, _, seen := downgradeServers(t)
	custom := secure.Client()
	custom.CheckRedirect = func(*nethttp.Request, []*nethttp.Request) error { return nil }

	c, err := NewBuilder(quietLogger()).WithHTTPClient(custom).WithBearerTokenFile(path, BearerTokenFileOptions{}).Build()
	require.NoError(t, err)

	_, err = c.Get(context.Background(), &Request{URL: secure.URL})
	require.NoError(t, err)
	assert.Equal(t, []string{"Bearer " + redirectSecret}, seen(), "the caller's policy governs, downgrade included")
}
