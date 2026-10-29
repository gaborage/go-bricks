// Package credurl is the one credential-URL renderer: it masks a URL's credentials in
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
	"unicode"
	"unicode/utf8"
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

// RedactError returns err with every rendering of raw masked as Redact renders it.
// Shapes url.Parse accepts never yield a *url.Error, so they are masked only where
// raw, or text of it Redact drops, is in the message. A *url.Error's rendering is
// rebuilt rather than scrubbed, and a failure reason that quotes part of the URL is
// replaced by reason. The result unwraps to err; when
// nothing needs masking, err itself is returned.
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
	redacted = maskRawTokens(maskQuotedRaw(maskRawIn(redacted, raw), raw), raw)
	if redacted == msg {
		return err
	}
	return &redactedError{err: err, msg: redacted}
}

// maskRawIn replaces every rendering of raw in msg with its masked form.
func maskRawIn(msg, raw string) string {
	masked := Redact(raw)
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
// raw, is the string that reached the message. When raw extends it, or it is a client's
// rebuild of raw cut inside the decoded password (rebuiltFrom), the rebuild renders raw,
// since the '@' ending the userinfo can sit in the cut. A *url.Error naming another URL,
// such as a dialer's own, is judged alone.
func maskURLError(msg string, urlErr *url.Error, raw, reason string) string {
	src := urlErr.URL
	if strings.HasPrefix(raw, src) || rebuiltFrom(raw, src) {
		src = raw // url.Parse cut raw's '#' tail, or the rebuild's, before reporting
	}
	masked := Redact(src)
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

// SECURITY: a vendor reason can quote raw text outside any *url.Error, such as a port or
// query value it failed to parse. Mask every quoted segment longer than one character
// that dropsRawText reports: one character reveals at most itself, and a vendor
// hint quotes reserved characters one by one. A later '"' is tried even when an
// earlier one opened a segment, so a stray quote cannot hide one.
func maskQuotedRaw(msg, raw string) string {
	if masked, ok := maskQuoted(msg, dropsRawText(raw, strings.Contains)); ok {
		return masked
	}
	return msg
}

// maskQuoted replaces with "xxxxx" every quoted segment of msg longer than one character
// whose text masks reports, and reports whether it replaced any.
func maskQuoted(msg string, masks func(text string) bool) (string, bool) {
	masked := false
	for i := 0; i < len(msg); i++ {
		if msg[i] != '"' {
			continue
		}
		quoted, err := strconv.QuotedPrefix(msg[i:])
		if err != nil {
			continue
		}
		if text, _ := strconv.Unquote(quoted); len(text) > 1 && masks(text) {
			placeholder := strconv.Quote(redactionPlaceholder)
			msg = msg[:i] + placeholder + msg[i+len(quoted):]
			i += len(placeholder) - 1
			masked = true
		}
	}
	return msg, masked
}

// SECURITY: vendor text can also echo raw unquoted, as a misparsed vhost or a dial host.
// Mask every token longer than one character, a maximal run between value delimiters, that
// a source holds aligned and Redact drops; a vendor word inside a password word stays. The
// cost: a vendor word equal to an aligned piece of dropped text, a query key too, is masked.
func maskRawTokens(msg, raw string) string {
	masks := dropsRawText(raw, holdsAligned)
	var b strings.Builder
	for _, token := range strings.FieldsFunc(msg, isValueDelimiter) {
		i := strings.Index(msg, token) // only delimiters precede token
		b.WriteString(msg[:i])
		msg = msg[i+len(token):]
		if len(token) > 1 && !strings.Contains(token, redactionPlaceholder) && masks(token) {
			token = redactionPlaceholder
		}
		b.WriteString(token)
	}
	b.WriteString(msg)
	return b.String()
}

// isValueDelimiter reports whitespace or the punctuation vendor text sets around a value.
func isValueDelimiter(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("\"'`:,;()<>", r)
}

// holdsAligned reports text in source whose each end is an edge or touches a value or URL delimiter.
func holdsAligned(source, text string) bool {
	aligned := func(i int) bool {
		before, _ := utf8.DecodeLastRuneInString(source[:i])
		after, _ := utf8.DecodeRuneInString(source[i:])
		return i == 0 || i == len(source) || strings.ContainsFunc(string(before)+string(after), func(r rune) bool {
			return isValueDelimiter(r) || strings.ContainsRune("/@?#&=", r)
		})
	}
	for from := range len(source) {
		if strings.HasPrefix(source[from:], text) && aligned(from) && aligned(from+len(text)) {
			return true
		}
	}
	return false
}

// dropsRawText reports text that holds finds in raw or its decoded password, and Redact drops.
func dropsRawText(raw string, holds func(source, text string) bool) func(text string) bool {
	kept := Redact(raw)
	_, password, _ := decodedUserinfo(raw)
	return func(text string) bool {
		return !strings.Contains(kept, text) && (holds(raw, text) || holds(password, text))
	}
}

// SECURITY: url.Parse cuts a client's rebuild around raw's decoded password at a '#' in it, and
// the cut shows password text. rebuiltFrom reports raw's "scheme://username:" plus a password prefix.
func rebuiltFrom(raw, cut string) bool {
	prefix, password, ok := decodedUserinfo(raw)
	rest, found := strings.CutPrefix(cut, prefix)
	return ok && found && rest != "" && strings.HasPrefix(password, rest)
}

// decodedUserinfo returns raw's "scheme://username:" and the password url.Parse decodes, if set.
func decodedUserinfo(raw string) (prefix, password string, ok bool) {
	if u, err := url.Parse(raw); err == nil {
		prefix = u.Scheme + "://" + u.User.Username() + ":"
		password, ok = u.User.Password()
	}
	return prefix, password, ok
}

// Redact renders raw with its credentials masked. Scheme, host, port and path are
// kept; the password becomes xxxxx, a non-empty query ?xxxxx, and a fragment is
// dropped. The username is kept only beside a password that is non-empty and does
// not start with '@', and only when it holds no '@', '/', '?' or '#'; any other
// userinfo is masked whole. The userinfo runs to the last '@', wherever url.Parse would end
// it; when a '?' precedes that '@', all after the '@' renders as one xxxxx. With no
// '@', a URL url.Parse rejects is masked from the first ':' to the end of its
// authority. Input with no "://", or with a ':', '/', '?', '#' or '@' before its
// first "://", renders as xxxxx, and empty input as "".
func Redact(raw string) string {
	if raw == "" {
		return ""
	}
	const schemeSep = "://"
	sep := strings.Index(raw, schemeSep)
	if sep == -1 || strings.ContainsAny(raw[:sep], ":/?#@") {
		return redactionPlaceholder
	}
	authority := sep + len(schemeSep)
	head, rest := raw[:authority], raw[authority:]
	if at := strings.LastIndex(rest, "@"); at != -1 {
		head += maskUserinfo(rest[:at])
		// Past a '?', that '@' may sit in a query value, so the tail is masked whole.
		if strings.IndexByte(rest[:at], '?') != -1 {
			return head + redactionPlaceholder
		}
		rest = rest[at+1:]
	} else if _, err := url.Parse(raw); err != nil {
		rest = maskRejectedAuthority(rest)
	}
	rest, _, _ = strings.Cut(rest, "#")
	rest, query, _ := strings.Cut(rest, "?")
	if query != "" {
		rest += "?" + redactionPlaceholder
	}
	return head + rest
}

func maskUserinfo(userinfo string) string {
	if user, password, _ := strings.Cut(userinfo, ":"); password != "" && !strings.HasPrefix(password, "@") && !strings.ContainsAny(user, "@/?#") {
		return user + ":" + redactionPlaceholder + "@"
	}
	return redactionPlaceholder + "@"
}

// maskRejectedAuthority masks an authority with no '@' from its first ':' outside an
// IPv6 literal: an unescaped '/', '?' or '#' in a password can hide or cut away the
// '@', so a rejected host:port may be userinfo.
func maskRejectedAuthority(rest string) string {
	end := strings.IndexAny(rest, "/?#")
	if end == -1 {
		end = len(rest)
	}
	from := 0
	if strings.HasPrefix(rest, "[") {
		// An unterminated literal yields 0, so its first ':' is masked from.
		from = strings.Index(rest[:end], "]") + 1
	}
	colon := strings.Index(rest[from:end], ":")
	if colon == -1 {
		return rest
	}
	return rest[:from+colon+1] + redactionPlaceholder + rest[end:]
}
