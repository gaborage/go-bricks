package auth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"

	"github.com/gaborage/go-bricks/httpclient"
)

// errJWKSEmptyKeySet reports a key set document that yields no usable key.
var errJWKSEmptyKeySet = errors.New("auth: jwks document carries no usable RSA signing key")

// jwksStatusError reports a non-200 key set response and keeps its status code.
// The body is never carried.
type jwksStatusError struct{ code int }

func (e *jwksStatusError) Error() string {
	return fmt.Sprintf("auth: jwks endpoint returned status %d", e.code)
}

// fetchFailureClass separates a failure an issuer outage produces from one a
// wrong or untrusted endpoint produces every time.
type fetchFailureClass int

const (
	fetchFailureNone fetchFailureClass = iota
	fetchFailureOutage
	fetchFailureConfiguration
)

// Fetch failure stages: short, low-cardinality and safe to log.
const (
	fetchStageStatus    = "status"
	fetchStageConnect   = "connect"
	fetchStageTimeout   = "timeout"
	fetchStageDNS       = "dns"
	fetchStageTLS       = "tls"
	fetchStageRedirect  = "redirect"
	fetchStageOversized = "oversized"
	fetchStageParse     = "parse"
	fetchStageEmpty     = "empty_key_set"
	fetchStageUnknown   = "unknown"
)

// fetchFailure is a classified key set fetch failure.
type fetchFailure struct {
	class fetchFailureClass
	stage string
}

// classifyFetchFailure sorts a fetch error into outage or configuration. The
// rule is positive: only a recognized outage is an outage, and anything else —
// including an error nothing here recognizes — is configuration.
func classifyFetchFailure(err error) fetchFailure {
	if err == nil {
		return fetchFailure{class: fetchFailureNone}
	}
	var statusErr *jwksStatusError
	if errors.As(err, &statusErr) {
		if isOutageStatus(statusErr.code) {
			return fetchFailure{class: fetchFailureOutage, stage: fetchStageStatus}
		}
		return fetchFailure{class: fetchFailureConfiguration, stage: fetchStageStatus}
	}
	if isConnectionRefused(err) {
		return fetchFailure{class: fetchFailureOutage, stage: fetchStageConnect}
	}
	if isFetchTimeout(err) {
		return fetchFailure{class: fetchFailureOutage, stage: fetchStageTimeout}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTemporary && !dnsErr.IsNotFound {
			return fetchFailure{class: fetchFailureOutage, stage: fetchStageDNS}
		}
		return fetchFailure{class: fetchFailureConfiguration, stage: fetchStageDNS}
	}
	return fetchFailure{class: fetchFailureConfiguration, stage: configurationStage(err)}
}

// isOutageStatus reports a status an issuer outage produces: a 5xx or a 429.
func isOutageStatus(code int) bool {
	return code >= 500 && code <= 599 || code == 429
}

// isFetchTimeout recognizes a timeout in every shape it reaches the resolver:
// httpclient's timeout error, which carries no cause; and a net.Error timeout,
// which covers a DNS timeout and the bare context.DeadlineExceeded a
// caller-supplied client that retries returns (it implements net.Error).
func isFetchTimeout(err error) bool {
	if httpclient.IsErrorType(err, httpclient.TimeoutError) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// configurationStage names the stage of a failure already known not to be an
// outage.
func configurationStage(err error) string {
	if isTLSVerificationFailure(err) {
		return fetchStageTLS
	}
	if errors.Is(err, errJWKSRedirectRefused) {
		return fetchStageRedirect
	}
	if errors.Is(err, errBodyTooLarge) {
		return fetchStageOversized
	}
	if errors.Is(err, errJWKSNotJSON) {
		return fetchStageParse
	}
	if errors.Is(err, errJWKSEmptyKeySet) {
		return fetchStageEmpty
	}
	return fetchStageUnknown
}

func isTLSVerificationFailure(err error) bool {
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		return true
	}
	var authorityErr x509.UnknownAuthorityError
	if errors.As(err, &authorityErr) {
		return true
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return true
	}
	var invalidErr x509.CertificateInvalidError
	return errors.As(err, &invalidErr)
}
