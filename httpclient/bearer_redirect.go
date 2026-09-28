package httpclient

import (
	"errors"
	"fmt"
	nethttp "net/http"
)

// maxBearerRedirects restates net/http's default cap, which a CheckRedirect replaces.
const maxBearerRedirects = 10

var errBearerRedirectDowngrade = errors.New("httpclient: refusing a redirect from https to http: it would send the bearer token in cleartext")

// bearerCheckRedirect is the redirect policy Build installs with
// WithBearerTokenFile. net/http forwards Authorization to the same host whatever
// the scheme, so a hop from https to http is refused.
func bearerCheckRedirect(req *nethttp.Request, via []*nethttp.Request) error {
	if len(via) >= maxBearerRedirects {
		return fmt.Errorf("httpclient: stopped after %d redirects", maxBearerRedirects)
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errBearerRedirectDowngrade
	}
	return nil
}
