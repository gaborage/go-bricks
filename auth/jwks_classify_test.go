package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtesting "github.com/gaborage/go-bricks/auth/testing"
	"github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/logger"
)

// viaTransport wraps cause the way net/http, httpclient and fetch stack it on
// the transport path.
func viaTransport(cause error) error {
	urlErr := &url.Error{Op: "Get", URL: "https://idp.test/jwks.json", Err: cause}
	return fmt.Errorf("auth: jwks request failed: %w", httpclient.NewNetworkError("request execution failed", urlErr))
}

// viaFetch wraps err the way fetch does on the transport path, without the
// net/http layers.
func viaFetch(err error) error {
	return fmt.Errorf("auth: jwks request failed: %w", err)
}

func TestClassifyFetchFailureSortsEveryListedFailure(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	tests := []struct {
		name  string
		err   error
		class fetchFailureClass
		stage string
	}{
		{name: "nil_is_not_classified", err: nil, class: fetchFailureNone},
		{name: "connection_refused", err: viaTransport(refused), class: fetchFailureOutage, stage: fetchStageConnect},
		{name: "httpclient_timeout", err: viaFetch(httpclient.NewTimeoutError("request timeout", jwksFetchTimeout)), class: fetchFailureOutage, stage: fetchStageTimeout},
		{name: "bare_deadline_exceeded", err: viaFetch(context.DeadlineExceeded), class: fetchFailureOutage, stage: fetchStageTimeout},
		{name: "net_error_timeout", err: viaTransport(&net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}), class: fetchFailureOutage, stage: fetchStageTimeout},
		{name: "dns_timeout", err: viaTransport(&net.DNSError{Err: "i/o timeout", Name: "idp.test", IsTimeout: true}), class: fetchFailureOutage, stage: fetchStageTimeout},
		{name: "dns_temporary", err: viaTransport(&net.DNSError{Err: "server misbehaving", Name: "idp.test", IsTemporary: true}), class: fetchFailureOutage, stage: fetchStageDNS},
		{name: "dns_temporary_but_not_found", err: viaTransport(&net.DNSError{Err: "no such host", Name: "idp.test", IsTemporary: true, IsNotFound: true}), class: fetchFailureConfiguration, stage: fetchStageDNS},
		{name: "dns_permanent", err: viaTransport(&net.DNSError{Err: "server misbehaving", Name: "idp.test"}), class: fetchFailureConfiguration, stage: fetchStageDNS},
		{name: "dns_not_found", err: viaTransport(&net.DNSError{Err: "no such host", Name: "idp.invalid", IsNotFound: true}), class: fetchFailureConfiguration, stage: fetchStageDNS},
		{name: "status_500", err: &jwksStatusError{code: 500}, class: fetchFailureOutage, stage: fetchStageStatus},
		{name: "status_503", err: &jwksStatusError{code: 503}, class: fetchFailureOutage, stage: fetchStageStatus},
		{name: "status_599", err: &jwksStatusError{code: 599}, class: fetchFailureOutage, stage: fetchStageStatus},
		{name: "status_429", err: &jwksStatusError{code: 429}, class: fetchFailureOutage, stage: fetchStageStatus},
		{name: "status_499", err: &jwksStatusError{code: 499}, class: fetchFailureConfiguration, stage: fetchStageStatus},
		{name: "status_600", err: &jwksStatusError{code: 600}, class: fetchFailureConfiguration, stage: fetchStageStatus},
		{name: "status_428", err: &jwksStatusError{code: 428}, class: fetchFailureConfiguration, stage: fetchStageStatus},
		{name: "status_430", err: &jwksStatusError{code: 430}, class: fetchFailureConfiguration, stage: fetchStageStatus},
		{name: "status_401", err: &jwksStatusError{code: 401}, class: fetchFailureConfiguration, stage: fetchStageStatus},
		{name: "status_404", err: &jwksStatusError{code: 404}, class: fetchFailureConfiguration, stage: fetchStageStatus},
		{name: "status_204", err: &jwksStatusError{code: 204}, class: fetchFailureConfiguration, stage: fetchStageStatus},
		{name: "tls_verification_opaque_cause", err: viaTransport(&tls.CertificateVerificationError{Err: errors.New("x509: certificate has expired")}), class: fetchFailureConfiguration, stage: fetchStageTLS},
		{name: "tls_unknown_authority", err: viaTransport(x509.UnknownAuthorityError{}), class: fetchFailureConfiguration, stage: fetchStageTLS},
		{name: "tls_hostname_mismatch", err: viaTransport(x509.HostnameError{Host: "idp.test"}), class: fetchFailureConfiguration, stage: fetchStageTLS},
		{name: "tls_certificate_invalid", err: viaTransport(x509.CertificateInvalidError{Reason: x509.Expired}), class: fetchFailureConfiguration, stage: fetchStageTLS},
		{name: "redirect_refused", err: viaTransport(fmt.Errorf("%w: host does not match the configured endpoint", errJWKSRedirectRefused)), class: fetchFailureConfiguration, stage: fetchStageRedirect},
		{name: "oversized_body", err: errBodyTooLarge, class: fetchFailureConfiguration, stage: fetchStageOversized},
		{name: "unparseable_document", err: errJWKSNotJSON, class: fetchFailureConfiguration, stage: fetchStageParse},
		{name: "empty_key_set", err: errJWKSEmptyKeySet, class: fetchFailureConfiguration, stage: fetchStageEmpty},
		{name: "context_canceled", err: viaFetch(context.Canceled), class: fetchFailureConfiguration, stage: fetchStageUnknown},
		{name: "unrecognized_error", err: viaFetch(errors.New("boom")), class: fetchFailureConfiguration, stage: fetchStageUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyFetchFailure(tc.err)

			assert.Equal(t, tc.class, got.class)
			assert.Equal(t, tc.stage, got.stage)
		})
	}
}

func TestJWKSStatusErrorKeepsTheExistingMessage(t *testing.T) {
	assert.Equal(t, "auth: jwks endpoint returned status 503", (&jwksStatusError{code: 503}).Error())
}

// statusErrorClient returns a non-200 response together with httpclient's own
// status error, the shape the default client produces.
type statusErrorClient struct {
	httpclient.Client
	code int
}

func (c statusErrorClient) Get(_ context.Context, _ *httpclient.Request) (*httpclient.Response, error) {
	return &httpclient.Response{StatusCode: c.code}, httpclient.NewHTTPError("status", c.code, []byte("secret body"))
}

func TestJWKSResolverFetchKeepsTheStatusCode(t *testing.T) {
	tests := []struct {
		name   string
		client httpclient.Client
		code   int
		class  fetchFailureClass
	}{
		{name: "status_with_error_429", client: statusErrorClient{code: 429}, code: 429, class: fetchFailureOutage},
		{name: "status_with_error_401", client: statusErrorClient{code: 401}, code: 401, class: fetchFailureConfiguration},
		{name: "status_with_error_404", client: statusErrorClient{code: 404}, code: 404, class: fetchFailureConfiguration},
		{name: "status_without_error_500", client: statusOnlyClient{}, code: 500, class: fetchFailureOutage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &jwksResolver{uri: "https://idp.test/jwks.json", client: tc.client, maxBodyBytes: testMaxBody, now: time.Now}

			outcome := r.fetch(context.Background())

			var statusErr *jwksStatusError
			require.ErrorAs(t, outcome.err, &statusErr)
			assert.Equal(t, tc.code, statusErr.code)
			assert.NotContains(t, outcome.err.Error(), "secret body")
			assert.Equal(t, tc.class, classifyFetchFailure(outcome.err).class)
		})
	}
}

func TestJWKSResolverFetchKeepsTheStatusCodeFromTheWire(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)
	r := &jwksResolver{uri: srv.URL(), client: jwksClient(t, srv), maxBodyBytes: testMaxBody, now: time.Now}

	outcome := r.fetch(context.Background())

	var statusErr *jwksStatusError
	require.ErrorAs(t, outcome.err, &statusErr)
	assert.Equal(t, nethttp.StatusServiceUnavailable, statusErr.code)
	assert.Equal(t, fetchFailure{class: fetchFailureOutage, stage: fetchStageStatus}, classifyFetchFailure(outcome.err))
}

func TestJWKSResolverFetchKeepsTheDocumentFailures(t *testing.T) {
	tests := []struct {
		name     string
		mode     authtesting.JWKSMode
		sentinel error
		stage    string
	}{
		{name: "malformed_document", mode: authtesting.JWKSMalformed, sentinel: errJWKSNotJSON, stage: fetchStageParse},
		{name: "no_rsa_key", mode: authtesting.JWKSNonRSAOnly, sentinel: errJWKSEmptyKeySet, stage: fetchStageEmpty},
		{name: "oversized_body", mode: authtesting.JWKSOversized, sentinel: errBodyTooLarge, stage: fetchStageOversized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newJWKSFixture(t)
			srv.SetMode(tc.mode)
			r := &jwksResolver{uri: srv.URL(), client: jwksClient(t, srv), maxBodyBytes: 4096, now: time.Now}

			outcome := r.fetch(context.Background())

			require.ErrorIs(t, outcome.err, tc.sentinel)
			assert.Equal(t, fetchFailure{class: fetchFailureConfiguration, stage: tc.stage}, classifyFetchFailure(outcome.err))
		})
	}
}

func TestJWKSResolverFetchKeepsTheTLSVerificationCause(t *testing.T) {
	srv := newJWKSFixture(t)
	cfg := jwksConfig(srv)
	client, err := defaultJWKSClient(&cfg, logger.New("error", false))
	require.NoError(t, err)
	r := &jwksResolver{uri: srv.URL(), client: client, maxBodyBytes: testMaxBody, now: time.Now}

	outcome := r.fetch(context.Background())

	var verifyErr *tls.CertificateVerificationError
	require.ErrorAs(t, outcome.err, &verifyErr)
	assert.Equal(t, fetchFailure{class: fetchFailureConfiguration, stage: fetchStageTLS}, classifyFetchFailure(outcome.err))
}

func TestJWKSResolverFetchKeepsTheRedirectRefusal(t *testing.T) {
	srv := newJWKSFixture(t)
	elsewhere := newJWKSFixture(t)
	srv.SetRedirectLocation(elsewhere.URL())
	r := &jwksResolver{uri: srv.RedirectURL(), client: jwksRedirectClient(t, srv), maxBodyBytes: testMaxBody, now: time.Now}

	outcome := r.fetch(context.Background())

	require.ErrorIs(t, outcome.err, errJWKSRedirectRefused)
	assert.Equal(t, fetchFailure{class: fetchFailureConfiguration, stage: fetchStageRedirect}, classifyFetchFailure(outcome.err))
}

func TestJWKSResolverFetchRecognizesATimeout(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, r *nethttp.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(release) })
	client, err := httpclient.NewBuilder(logger.New("error", false)).WithTimeout(50 * time.Millisecond).Build()
	require.NoError(t, err)
	r := &jwksResolver{uri: hung.URL, client: client, maxBodyBytes: testMaxBody, now: time.Now}

	outcome := r.fetch(context.Background())

	require.True(t, httpclient.IsErrorType(outcome.err, httpclient.TimeoutError), "got %v", outcome.err)
	assert.Equal(t, fetchFailure{class: fetchFailureOutage, stage: fetchStageTimeout}, classifyFetchFailure(outcome.err))
}

func TestJWKSResolverFetchRecognizesARefusedConnection(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	client, err := httpclient.NewBuilder(logger.New("error", false)).Build()
	require.NoError(t, err)
	r := &jwksResolver{uri: "https://" + addr + "/jwks.json", client: client, maxBodyBytes: testMaxBody, now: time.Now}

	outcome := r.fetch(context.Background())

	require.True(t, isConnectionRefused(outcome.err), "got %v", outcome.err)
	assert.Equal(t, fetchFailure{class: fetchFailureOutage, stage: fetchStageConnect}, classifyFetchFailure(outcome.err))
}
