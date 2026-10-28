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
//
// err must be non-nil, and a *url.Error in it must be reachable through errors.As and
// rendered as its own Error() text. A later errors.As on the result reaches the
// original *url.Error, so RedactError must be the last transform before rendering.
func RedactError(err error, raw, reason string) error {
	msg := err.Error()
	redacted := msg
	// Rebuild the net/url rendering before masking the raw URL in general: masking
	// first would rewrite the URL inside that rendering, leaving nothing here to match.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		redacted = maskURLError(redacted, urlErr, raw, reason)
	}
	redacted = maskRawIn(redacted, raw)
	if redacted == msg {
		return err
	}
	return &redactedError{err: err, msg: redacted}
}

// maskRawIn replaces every rendering of raw in msg with its masked form.
func maskRawIn(msg, raw string) string {
	masked := maskPassword(raw, -1)
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
// is what url.Parse actually saw — raw with any '#' fragment already cut — so it, not
// raw, is the string that reached the message. raw is read only for the last '@',
// which ends the userinfo, and only when it extends urlErr.URL: a *url.Error naming
// another URL, such as a dialer's own, is judged alone.
func maskURLError(msg string, urlErr *url.Error, raw, reason string) string {
	src := urlErr.URL
	if strings.HasPrefix(raw, src) {
		src = raw // url.Parse cut raw's '#' tail before reporting
	}
	_, after, _ := strings.Cut(src, "://")
	masked := maskPassword(urlErr.URL, strings.LastIndex(after, "@"))
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
// the URL form. userinfoEnd, unless -1, locates the '@' that ends the userinfo,
// counted from the authority's start; past raw's end, that '@' sat in a tail
// url.Parse cut, and the userinfo runs to raw's end. -1 leaves the end to the scan.
func maskPassword(raw string, userinfoEnd int) string {
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
	if userinfoEnd != -1 {
		at = min(userinfoEnd, len(rest))
	} else if at == -1 {
		// An unescaped '/', '?' or '#' in the password hides the '@' behind the
		// authority terminator, if raw holds it at all. A segment that cannot be a
		// host[:port] is userinfo; one that can, as a digit-led password does, is
		// left as written.
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
// i.e. carries no credentials to mask. url.Parse accepts the ambiguous shape
// "amqps://user:1234/secret@broker" as written, reading "user:1234" as the host, so
// no *url.Error renders it; the raw-URL pass leaves it alone on purpose, since
// masking it would mangle its mirror image, a passwordless URL whose path holds an
// '@'. The accepted cost: a driver that echoes an accepted URL, or its host or port
// (amqp091's port parse, a DNS lookup), shows that shape as written.
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
