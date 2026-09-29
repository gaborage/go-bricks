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

func TestBearerTokenFileRefusesRedirectDowngradeWithoutRetry(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-redirect"))
	plain, seen := authServer(t)
	secure, hits := redirectTo(t, plain.URL+"/landing")

	c, err := NewBuilder(quietLogger()).
		WithHTTPClient(secure.Client()).
		WithRetries(2, time.Millisecond).
		WithBearerTokenFile(path).
		Build()
	require.NoError(t, err)

	_, err = c.Get(context.Background(), &Request{URL: secure.URL})
	require.ErrorIs(t, err, errBearerRedirectDowngrade)
	assert.NotContains(t, err.Error(), "tok-redirect")
	assert.Empty(t, seen(), "the http hop must never be requested")
	assert.Equal(t, int64(1), hits.Load(), "a refused downgrade is terminal, not retried")
}

func TestBearerCheckRedirectAllowsDowngradeWithoutAuthorization(t *testing.T) {
	first := &nethttp.Request{URL: &url.URL{Scheme: "https", Host: "api.example.com"}}
	hop := &nethttp.Request{URL: &url.URL{Scheme: "http", Host: "cdn.example.net"}, Header: nethttp.Header{}}
	require.NoError(t, bearerCheckRedirect(hop, []*nethttp.Request{first}), "net/http stripped Authorization, so nothing leaks")

	hop.Header.Set(headerAuthorization, "Bearer x")
	require.ErrorIs(t, bearerCheckRedirect(hop, []*nethttp.Request{first}), errBearerRedirectDowngrade)
}

func TestBearerTokenFileFollowsSameSchemeRedirect(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-redirect"))
	var landed atomic.Value
	secure := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/start" {
			nethttp.Redirect(w, r, "/landing", nethttp.StatusFound)
			return
		}
		landed.Store(r.Header.Get(headerAuthorization))
	}))
	t.Cleanup(secure.Close)

	c, err := NewBuilder(quietLogger()).WithHTTPClient(secure.Client()).WithBearerTokenFile(path).Build()
	require.NoError(t, err)

	resp, err := c.Get(context.Background(), &Request{URL: secure.URL + "/start"})
	require.NoError(t, err)
	assert.Equal(t, nethttp.StatusOK, resp.StatusCode)
	assert.Equal(t, "Bearer tok-redirect", landed.Load())
}

func TestBearerTokenFileKeepsCallerRedirectPolicy(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "token", []byte("tok-redirect"))
	plain, seen := authServer(t)
	secure, _ := redirectTo(t, plain.URL+"/landing")
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
