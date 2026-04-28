package jose

import (
	"errors"
	"strings"
)

// ContentType is the IANA-registered media type for compact JOSE serializations.
// Used as the request and response Content-Type for JOSE-protected HTTP traffic.
const ContentType = "application/jose"

// IsContentType reports whether ct (typically a Content-Type header value) names the
// JOSE compact-serialization media type. Matches application/jose with optional
// parameters (e.g., "application/jose; charset=utf-8") case-insensitively per
// RFC 7231 §3.1.1.1.
func IsContentType(ct string) bool {
	if ct == "" {
		return false
	}
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = ct[:idx]
	}
	return strings.EqualFold(strings.TrimSpace(ct), ContentType)
}

// IsError reports whether err is (or wraps) a *jose.Error — useful for callers that
// need to distinguish JOSE crypto failures (signature invalid, decrypt failed, kid
// unknown, etc.) from network/transport errors. Equivalent to manually doing
// `var jerr *jose.Error; errors.As(err, &jerr)` but reads as a single intent at the
// call site.
func IsError(err error) bool {
	var jerr *Error
	return errors.As(err, &jerr)
}
