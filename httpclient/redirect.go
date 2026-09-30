package httpclient

import (
	"errors"
	"fmt"
	nethttp "net/http"
	"net/url"
	"strings"
)

// maxRedirects restates net/http's default cap, which a CheckRedirect replaces.
const maxRedirects = 10

// ErrRedirectDowngrade reports a refused redirect from https to http whose next
// hop would carry Authorization, Cookie or Proxy-Authorization. It is terminal:
// the client never retries it. The error never carries the header's value.
var ErrRedirectDowngrade = errors.New("httpclient: refusing a redirect from https to http: it would send a credential header in cleartext")

// redirectCredentialHeaders are the request-side credentials among the six
// headers net/http treats as sensitive on a redirect.
var redirectCredentialHeaders = [...]string{headerAuthorization, "Cookie", "Proxy-Authorization"}

// guardRedirects installs checkRedirect on a client that has no redirect policy
// of its own; a caller's CheckRedirect governs entirely.
func guardRedirects(c *nethttp.Client) {
	if c.CheckRedirect == nil {
		c.CheckRedirect = checkRedirect
	}
}

// checkRedirect refuses a hop from https to http that would carry a credential
// header. net/http copies the headers it forwards onto req before calling
// CheckRedirect, and forwards them to the same hostname whatever the scheme or port.
func checkRedirect(req *nethttp.Request, via []*nethttp.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("httpclient: stopped after %d redirects", maxRedirects)
	}
	if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" && carriesCredential(req) {
		return ErrRedirectDowngrade
	}
	return nil
}

// carriesCredential reports a credential in the hop's URL userinfo, which net/http turns
// into Authorization after CheckRedirect returns, or any non-empty value under a
// credential header, whatever the key's casing and however many values it holds.
func carriesCredential(req *nethttp.Request) bool {
	if req.URL.User != nil {
		return true
	}
	for key, values := range req.Header {
		if isCredentialHeader(key) && hasNonEmpty(values) {
			return true
		}
	}
	return false
}

func isCredentialHeader(key string) bool {
	for _, name := range redirectCredentialHeaders {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func hasNonEmpty(values []string) bool {
	for _, v := range values {
		if v != "" {
			return true
		}
	}
	return false
}

// stripURLUserinfo drops userinfo from the *url.Error net/http returns. On a refused
// redirect it carries the raw Location, which net/http does not strip of its password
// the way it strips the request's own URL.
func stripURLUserinfo(err error) {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return
	}
	u, parseErr := url.Parse(urlErr.URL)
	if parseErr != nil || u.User == nil {
		return
	}
	u.User = nil
	urlErr.URL = u.String()
}
