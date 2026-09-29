package httpclient

import (
	"errors"
	"fmt"
	nethttp "net/http"
)

// maxBearerRedirects restates net/http's default cap, which a CheckRedirect replaces.
const maxBearerRedirects = 10

var errBearerRedirectDowngrade = errors.New("httpclient: refusing a redirect from https to http: it would send the Authorization header in cleartext")

// guardBearerRedirects installs bearerCheckRedirect on a client that carries a
// file bearer and has no redirect policy of its own.
func guardBearerRedirects(c *nethttp.Client, bearer *bearerTokenFile) {
	if bearer != nil && c.CheckRedirect == nil {
		c.CheckRedirect = bearerCheckRedirect
	}
}

// bearerCheckRedirect refuses a hop from https to http that would carry
// Authorization. net/http copies the headers it forwards onto req before calling
// CheckRedirect, and forwards Authorization to the same host whatever the scheme.
func bearerCheckRedirect(req *nethttp.Request, via []*nethttp.Request) error {
	if len(via) >= maxBearerRedirects {
		return fmt.Errorf("httpclient: stopped after %d redirects", maxBearerRedirects)
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" && req.Header.Get(headerAuthorization) != "" {
		return errBearerRedirectDowngrade
	}
	return nil
}
