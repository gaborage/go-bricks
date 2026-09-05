package config

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// normalizeServer fills the server defaults this package owns. Only the body
// limit today: an unset (zero) server.bodylimit becomes DefaultBodyLimitBytes
// here, so the config seam decides that default rather than the wire-up in
// server.SetupMiddlewares.
//
// The zero/negative rule comes from applyNonNegativeDefault rather than a fourth
// hand-written copy of it: a negative is an operator error and must surface as
// one, never be laundered into the default.
func normalizeServer(cfg *ServerConfig) error {
	return applyNonNegativeDefault(&cfg.BodyLimit, DefaultBodyLimitBytes, "server.bodylimit")
}

// checkServer rejects a server section the server could not start from.
func checkServer(cfg *ServerConfig) error {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return NewInvalidFieldError(fieldServerPort, fmt.Sprintf(errInvalidField, cfg.Port), []string{portRange})
	}

	if cfg.Timeout.Read <= 0 {
		return NewValidationError("server.timeout.read", errMustBePositive)
	}

	if cfg.Timeout.Write <= 0 {
		return NewValidationError("server.timeout.write", errMustBePositive)
	}

	if cfg.Timeout.Middleware <= 0 {
		return NewValidationError("server.timeout.middleware", errMustBePositive)
	}

	// Middleware timeout should be less than write timeout to allow graceful error responses
	// Otherwise the write timeout will trigger first, causing connection drops
	if cfg.Timeout.Middleware >= cfg.Timeout.Write {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    "server.timeout.middleware",
			Message:  fmt.Sprintf("must be less than server.timeout.write (%v)", cfg.Timeout.Write),
			Action:   "reduce server.timeout.middleware or increase server.timeout.write",
		}
	}

	if cfg.Timeout.Shutdown <= 0 {
		return NewValidationError("server.timeout.shutdown", errMustBePositive)
	}

	if cfg.Gzip.MinLength < 0 {
		return NewValidationError("server.gzip.minlength", errMustBeNonNegative)
	}

	// Unreachable on the Validate path: normalizeServer has already filled a zero
	// and refused a negative. Kept for callers that reach checkServer directly.
	if cfg.BodyLimit < 0 {
		return NewValidationError("server.bodylimit", errMustBeNonNegative)
	}

	if err := validateServerTLS(&cfg.TLS); err != nil {
		return err
	}

	if err := validateServerTrustedProxies(cfg.TrustedProxies); err != nil {
		return err
	}

	return validateServerForwardedClientCert(&cfg.ForwardedClientCert)
}

// Rejection reasons from ParseTrustedProxyCIDR. Unexported: only this package
// needs to tell them apart (to pick an Action string); server just skips and logs.
var (
	errTrustedProxyInvalidCIDR  = errors.New("not a valid CIDR range")
	errTrustedProxyHostBits     = errors.New("host bits set, which silently widens the trusted range")
	errTrustedProxyDefaultRoute = errors.New("trusts every address, which restores X-Forwarded-For spoofing")
)

// ParseTrustedProxyCIDR parses one server.trustedproxies entry and rejects every
// shape that would make the list trust more than the operator wrote:
//
//   - anything net.ParseCIDR cannot parse, including a bare address — an operator
//     writing a single host gets an error instead of a silently dropped entry;
//   - an entry whose host bits are set: net.ParseCIDR accepts 10.1.2.3/8 and
//     silently masks it to 10.0.0.0/8, widening the range past what was written;
//   - a default route, which trusts every hop, so echo's walk finds nothing
//     untrusted and returns the caller-authored left-most X-Forwarded-For entry.
//
// Surrounding whitespace is trimmed, matching validateCIDRList and
// server.ParseCIDRs, so a YAML sequence entry with incidental spacing is accepted.
//
// Both config validation and the server's extractor wiring call this, so the rule
// set cannot drift between what startup accepts and what actually gets trusted.
// On the host-bits rejection the returned net is the masked range the entry would
// have silently become, so a caller can name it; every other failure returns nil.
func ParseTrustedProxyCIDR(entry string) (*net.IPNet, error) {
	ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(entry))
	if err != nil {
		return nil, errTrustedProxyInvalidCIDR
	}

	if !ip.Equal(ipNet.IP) {
		return ipNet, errTrustedProxyHostBits
	}

	// Measure the mask the way Contains will: "::ffff:0.0.0.0/96" reads as 96 of 128 bits
	// but matches every IPv4 address, so a raw Mask.Size() test lets a default route
	// through the STRICT door while the lenient ones now reject it.
	if _, ones, _ := NormalizeIPNet(ipNet); ones == 0 {
		return nil, errTrustedProxyDefaultRoute
	}

	return ipNet, nil
}

// validateServerTrustedProxies rejects any entry that would change who the
// client-IP extractor trusts in a way the operator did not write. A trusted
// proxy list is a security control, so a malformed entry aborts startup
// instead of being dropped with a warning: the difference between a trusted
// range and a missing one is invisible in behavior until it is abused.
func validateServerTrustedProxies(entries []string) error {
	// Set-level rule first: per-entry checks cannot see that ["0.0.0.0/1","128.0.0.0/1"]
	// trusts everyone between them. The strict key answers to the same coverage rule as
	// the two lenient ones — that they disagreed at all is the bug ADR-080 closes.
	if err := rejectTotalCoverage(fieldServerTrustedProxies, entries); err != nil {
		return err
	}
	for _, entry := range entries {
		ipNet, err := ParseTrustedProxyCIDR(entry)

		switch {
		case errors.Is(err, errTrustedProxyInvalidCIDR):
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldServerTrustedProxies,
				Message:  fmt.Sprintf("'%s' is not a valid CIDR range", entry),
				Action:   "use CIDR notation with a prefix length (a single host is /32 for IPv4 or /128 for IPv6)",
			}
		case errors.Is(err, errTrustedProxyHostBits):
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldServerTrustedProxies,
				Message:  fmt.Sprintf("'%s' has host bits set, which silently widens the trusted range", entry),
				Action:   fmt.Sprintf("write the masked form '%s' if that is the range you mean", ipNet.String()),
			}
		case errors.Is(err, errTrustedProxyDefaultRoute):
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldServerTrustedProxies,
				Message:  fmt.Sprintf("'%s' %s", entry, errTrustedProxyDefaultRoute),
				Action:   actionListSpecificProxyRanges,
			}
		}
	}

	return nil
}

// validateServerForwardedClientCert rejects a Require-without-Enabled
// configuration: rejecting requests on an identity source that is never
// parsed would be a silent no-op (every request would be treated as missing
// an identity, since ForwardedClientCert.Enabled gates whether the
// middleware ever runs at all).
func validateServerForwardedClientCert(cfg *ForwardedClientCertConfig) error {
	if cfg.Require && !cfg.Enabled {
		return NewValidationError(fieldServerForwardedClientCertRequire, "requires server.forwardedclientcert.enabled")
	}

	return nil
}

// validateServerTLS checks structural TLS material configuration (presence
// and mutual exclusivity of file/value sources, min-version enum). It does
// NOT touch the filesystem — reading and parsing PEM material happens at
// Start() time (see server/tls.go), so a bad path here still fails fast, just
// one hop later.
func validateServerTLS(cfg *ServerTLSConfig) error {
	if !cfg.Enabled {
		return nil
	}

	if err := validateServerTLSMaterial(fieldServerTLSCertFile, fieldServerTLSCertValue, cfg.CertFile, cfg.CertValue); err != nil {
		return err
	}

	if err := validateServerTLSMaterial(fieldServerTLSKeyFile, fieldServerTLSKeyValue, cfg.KeyFile, cfg.KeyValue); err != nil {
		return err
	}

	switch cfg.MinVersion {
	case "", tlsVersion12, tlsVersion13:
		return nil
	default:
		return NewInvalidFieldError(fieldServerTLSMinVersion, fmt.Sprintf(errInvalidField, cfg.MinVersion), []string{tlsVersion12, tlsVersion13})
	}
}

// validateServerTLSMaterial enforces exactly one of a file/value pair is set
// for a single PEM piece (cert or key).
func validateServerTLSMaterial(fileField, valueField, file, value string) error {
	switch {
	case file == "" && value == "":
		return NewValidationError(fileField, "exactly one of "+fileField+" or "+valueField+" is required when server.tls.enabled is true")
	case file != "" && value != "":
		return NewValidationError(fileField, fileField+" and "+valueField+" are mutually exclusive (exactly one)")
	default:
		return nil
	}
}
