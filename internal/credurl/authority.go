package credurl

import (
	"errors"
	"net/url"
	"strings"
)

// ErrAtAfterAuthority is the fixed refusal of a credential URL whose path, query or
// fragment holds an unencoded '@'. It carries no part of the URL.
var ErrAtAfterAuthority = errors.New("carries an unencoded '@' after its authority")

// AuthorityHint is the remedy ErrAtAfterAuthority travels with.
const AuthorityHint = "percent-encode '/', '?', '#' and '@' in the username and password (url.UserPassword), " +
	"and write an '@' in the vhost or a query value as %40"

// CheckAuthority refuses a URL that url.Parse accepts but has probably misread.
// url.Parse ends the authority at the first '/', '?' or '#', so one left unencoded in
// a password moves the rest of the password, and the real "@host", past the authority:
// the host dialed is then password text. An '@' past the authority is the one mark that
// split leaves, and it has no exact other reading ("amqp://h:5672/my@vhost" has the
// same shape), so it is refused wherever it appears; a legitimate one is written %40.
// An '@' inside the userinfo is untouched, and a URL url.Parse rejects is not this
// check's question.
func CheckAuthority(raw string) error {
	u, err := url.Parse(raw)
	if err == nil && hasAtAfterAuthority(u) {
		return ErrAtAfterAuthority
	}
	return nil
}

// hasAtAfterAuthority reads the escaped forms, never the decoded ones: %40 must stay legal.
func hasAtAfterAuthority(u *url.URL) bool {
	return strings.Contains(u.EscapedPath(), "@") || strings.Contains(u.RawQuery, "@") ||
		strings.Contains(u.EscapedFragment(), "@")
}
