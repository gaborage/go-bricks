// Package credurl is the one credential-URL renderer: it masks a URL's password in
// error text without echoing the input url.Parse rejected. A connection URL that
// carries inline credentials reaches startup errors — hence logs and pod
// termination messages — through net/url's %q rendering, so every driver that
// dials one shares these masking rules instead of re-deriving them.
package credurl

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// redactionPlaceholder is the placeholder substituted for a URL password, matching
// pgx's redactPW so every vendor redacts identically.
const redactionPlaceholder = "xxxxx"

// redactedError carries a sanitized message while keeping the original error
// reachable through errors.Is / errors.As.
type redactedError struct {
	err error
	msg string
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// RedactError returns err with every rendering of raw's password masked. It masks
// what url.Parse would read as the password, by scanning the string rather than
// parsing it. Shapes url.Parse accepts never yield a *url.Error, so they are masked
// only where raw itself is rendered in the message. A *url.Error's rendering is
// rebuilt rather than scrubbed, and a failure reason that quotes part of the URL is
// replaced by reason. Scheme, username and host survive so an operator can still
// tell which connection failed; only the password is masked (pgx's redactPW
// precedent). The result unwraps to err; when nothing needs masking, err itself is
// returned.
func RedactError(err error, raw, reason string) error {
	msg := err.Error()
	redacted := msg
	// Rebuild the net/url rendering before masking the raw URL in general: masking
	// first would rewrite the URL inside that rendering, leaving nothing here to match.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		redacted = maskURLError(redacted, urlErr, reason)
	}
	redacted = maskRawIn(redacted, raw)
	if redacted == msg {
		return err
	}
	return &redactedError{err: err, msg: redacted}
}

// maskRawIn replaces every rendering of raw in msg with its masked form.
func maskRawIn(msg, raw string) string {
	masked := maskPassword(raw)
	if masked == raw {
		return msg
	}
	// %q escapes non-printable bytes, so the raw URL can appear either verbatim or quoted.
	msg = strings.ReplaceAll(msg, raw, masked)
	return strings.ReplaceAll(msg, strconv.Quote(raw), strconv.Quote(masked))
}

// SECURITY: net/url does not only render the URL — it also echoes the component it
// rejected, and an unescaped '/', '?' or '#' in the password lands part of the password
// where a port belongs ("invalid port \":pa\" after host"). Masking the URL alone leaves
// that copy behind, so rebuild the whole rendering instead of scrubbing it. urlErr.URL
// is what url.Parse actually saw — the raw URL with any '#' fragment already stripped —
// so it, not the raw URL the caller dialed, is the string that reached the message.
func maskURLError(msg string, urlErr *url.Error, reason string) string {
	masked := maskPassword(urlErr.URL)
	if masked == urlErr.URL {
		return msg
	}
	// net/url embeds URL-derived text only via %q, so a reason that quotes nothing
	// cannot carry password material; anything else is dropped wholesale.
	if urlErr.Err != nil && !strings.Contains(urlErr.Err.Error(), `"`) {
		reason = urlErr.Err.Error()
	}
	return strings.ReplaceAll(msg, urlErr.Error(), fmt.Sprintf("%s %q: %s", urlErr.Op, masked, reason))
}

// maskPassword masks the password of a URL by scanning the string rather than
// parsing it — the URLs that leak are precisely the ones url.Parse rejects.
// Non-URL strings (no "://") and URLs without a password pass through unchanged:
// the leak is url.Parse's %q rendering of what it was handed, which only fires for
// the URL form.
func maskPassword(raw string) string {
	const schemeSep = "://"
	sep := strings.Index(raw, schemeSep)
	if sep == -1 {
		return raw
	}
	authority := sep + len(schemeSep)

	rest := raw[authority:]
	end := strings.IndexAny(rest, "/?#")
	if end == -1 {
		end = len(rest)
	}
	// net/url splits userinfo at the last '@' of the authority, so an unescaped '@'
	// inside the password stays part of the userinfo.
	at := strings.LastIndex(rest[:end], "@")
	if at == -1 {
		// An unescaped '/', '?' or '#' in the password hides the '@' behind the
		// authority terminator, and url.Parse drops everything from '#' on before it
		// reports, so the '@' can be missing from the string entirely. Read the segment
		// as userinfo only when it cannot be a host[:port] — that is exactly the shape
		// url.Parse rejects, and so exactly the shape whose error echoes the raw URL.
		if looksLikeHostPort(rest[:end]) {
			return raw
		}
		if at = strings.LastIndex(rest, "@"); at == -1 {
			at = len(rest)
		}
	}
	// Everything after the first ':' is the password, so an unescaped ':' inside it
	// is masked too.
	colon := strings.Index(rest[:at], ":")
	if colon == -1 {
		return raw
	}

	return raw[:authority+colon+1] + redactionPlaceholder + raw[authority+at:]
}

// looksLikeHostPort reports whether an authority segment is a plausible host[:port],
// i.e. carries no credentials to mask. A true answer also means url.Parse accepts the
// URL, and an accepted URL is never rendered into an error — so the ambiguous shape
// "amqp://user:1234/secret@broker" is left alone on purpose: masking it would buy
// nothing and would mangle its mirror image, a passwordless URL whose path holds an '@'.
func looksLikeHostPort(authority string) bool {
	if strings.HasPrefix(authority, "[") {
		// Skip an IPv6 literal so only a trailing ":port" is examined; an unterminated
		// literal yields index 0 and leaves the segment as it stands.
		authority = authority[strings.LastIndex(authority, "]")+1:]
	}
	colon := strings.LastIndex(authority, ":")
	if colon == -1 {
		return true
	}
	port := authority[colon+1:]
	return port != "" && strings.TrimLeft(port, "0123456789") == ""
}
