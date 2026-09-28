package httpclient

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redirectTo answers every request with a 302 to target.
func redirectTo(t *testing.T, target string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		nethttp.Redirect(w, r, target, nethttp.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBearerTokenFileRefusesRedirectDowngrade(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-redirect"))
	plain, seen := authServer(t)
	secure := redirectTo(t, plain.URL+"/landing")

	c, err := NewBuilder(quietLogger()).WithHTTPClient(secure.Client()).WithBearerTokenFile(path).Build()
	require.NoError(t, err)

	_, err = c.Get(context.Background(), &Request{URL: secure.URL})
	require.ErrorIs(t, err, errBearerRedirectDowngrade)
	assert.NotContains(t, err.Error(), "tok-redirect")
	assert.Empty(t, seen(), "the http hop must never be requested")
}

func TestBearerTokenFileFollowsSameSchemeRedirect(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-redirect"))
	var landed []string
	secure := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/start" {
			nethttp.Redirect(w, r, "/landing", nethttp.StatusFound)
			return
		}
		landed = append(landed, r.Header.Get(headerAuthorization))
	}))
	t.Cleanup(secure.Close)

	c, err := NewBuilder(quietLogger()).WithHTTPClient(secure.Client()).WithBearerTokenFile(path).Build()
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), &Request{URL: secure.URL + "/start"})
	require.NoError(t, err)
	assert.Equal(t, nethttp.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"Bearer tok-redirect"}, landed)
}

func TestBearerTokenFileKeepsCallerRedirectPolicy(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-redirect"))
	plain, seen := authServer(t)
	secure := redirectTo(t, plain.URL+"/landing")
	custom := secure.Client()
	custom.CheckRedirect = func(*nethttp.Request, []*nethttp.Request) error { return nil }

	c, err := NewBuilder(quietLogger()).WithHTTPClient(custom).WithBearerTokenFile(path).Build()
	require.NoError(t, err)

	_, err = c.Get(context.Background(), &Request{URL: secure.URL})
	require.NoError(t, err)
	assert.Equal(t, []string{"Bearer tok-redirect"}, seen(), "the caller's policy governs, downgrade included")
}

func TestBearerTokenFileCapsRedirectsAtTen(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-redirect"))
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if n < maxBearerRedirects {
			nethttp.Redirect(w, r, "/"+strconv.Itoa(n+1), nethttp.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := NewBuilder(quietLogger()).WithBearerTokenFile(path).Build()
	require.NoError(t, err)

	_, err = c.Get(context.Background(), &Request{URL: srv.URL + "/1"})
	require.NoError(t, err, "nine redirects are followed")
	_, err = c.Get(context.Background(), &Request{URL: srv.URL + "/0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped after 10 redirects")
}
