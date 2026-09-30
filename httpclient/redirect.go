package httpclient

import (
	"errors"
	"fmt"
	nethttp "net/http"
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
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" && carriesCredential(req.Header) {
		return ErrRedirectDowngrade
	}
	return nil
}

func carriesCredential(h nethttp.Header) bool {
	for _, name := range redirectCredentialHeaders {
		if h.Get(name) != "" {
			return true
		}
	}
	return false
}
