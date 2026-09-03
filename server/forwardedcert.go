package server

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// ALB verify-mode client-certificate identity headers. See
// https://docs.aws.amazon.com/elasticloadbalancing/latest/application/mutual-authentication.html
// (retrieved 2026-07-27) for the header set; passthrough mode's single
// X-Amzn-Mtls-Clientcert (full chain) header is out of v1 scope — its
// absence here is not an error.
const (
	headerClientCertSubject      = "X-Amzn-Mtls-Clientcert-Subject"
	headerClientCertIssuer       = "X-Amzn-Mtls-Clientcert-Issuer"
	headerClientCertSerialNumber = "X-Amzn-Mtls-Clientcert-Serial-Number"
	headerClientCertLeaf         = "X-Amzn-Mtls-Clientcert-Leaf"
)

// maxForwardedLeafBytes caps the encoded X-Amzn-Mtls-Clientcert-Leaf header
// before any decode attempt. Headers are attacker-influenced in size, so the
// cap is enforced against the raw encoded string, before url.PathUnescape or
// pem.Decode ever touch the bytes.
const maxForwardedLeafBytes = 64 * 1024

// errNoForwardedCert indicates the request carried neither the -Subject nor
// the -Serial-Number verify-mode header — i.e. no ALB-verified identity at
// all. With errDuplicateForwardedHeader below, it is one of the two conditions
// server.forwardedclientcert.require rejects on; a present Subject whose -Leaf
// failed to decode is NOT this error (see parseForwardedClientCert).
var errNoForwardedCert = errors.New("forwarded client certificate not present")

// errDuplicateForwardedHeader indicates a request carried more than one value
// for a single X-Amzn-Mtls-Clientcert-* header. In the documented posture
// (wiki/forwarded_client_cert.md) the ALB sets each header at most once, so a
// duplicate means either the ALB posture changed or a client-supplied copy
// got through — it is never trusted, not even via first-value-wins (see
// forwardedClientCertMiddlewareEcho).
var errDuplicateForwardedHeader = errors.New("duplicated X-Amzn-Mtls header")

// ForwardedClientCert is the partner identity an mTLS-verify-terminating ALB
// verified and forwarded via X-Amzn-Mtls-Clientcert-* headers. Subject,
// Issuer, and SerialNumber are the ALB's header values verbatim; Leaf is the
// parsed leaf certificate when the -Leaf header decoded cleanly, and is nil
// otherwise — always nil-check before use. The -Validity header is
// deliberately not carried: validity is readable from Leaf.NotBefore/NotAfter
// when Leaf parses.
//
// Trust model: enabling server.forwardedclientcert is an explicit operator
// assertion that (a) an mTLS-verify ALB listener fronts this service, (b)
// direct target access is closed (security groups), and (c) the target group
// is reachable only through that listener. AWS does not publicly document
// that the ALB strips or overwrites client-supplied copies of these headers,
// so the guarantee rests entirely on that deployment posture, never on an ALB
// sanitization claim. See wiki/forwarded_client_cert.md for the full trust
// model, including the single-partner-CA authorization boundary.
type ForwardedClientCert struct {
	Subject      string
	Issuer       string
	SerialNumber string
	Leaf         *x509.Certificate
}

// ctxKey is an unexported type to prevent cross-package collisions on context.Value lookups.
type ctxKey int

const keyForwardedClientCert ctxKey = iota

// withForwardedClientCert attaches the parsed identity to ctx.
func withForwardedClientCert(ctx context.Context, cert ForwardedClientCert) context.Context {
	return context.WithValue(ctx, keyForwardedClientCert, cert)
}

// ForwardedClientCertFromContext returns the partner identity the ALB
// verified and forwarded, and whether one was attached for this request.
// Absence (ok == false) means either server.forwardedclientcert.enabled is
// false, the request skipped the middleware (health/ready probes), or the
// request carried no verify-mode identity headers at all.
func ForwardedClientCertFromContext(ctx context.Context) (cert ForwardedClientCert, ok bool) {
	cert, ok = ctx.Value(keyForwardedClientCert).(ForwardedClientCert)
	return cert, ok
}

// parseForwardedClientCert extracts a ForwardedClientCert from a
// multi-valued header getter (net/http's Header.Values, or any equivalent),
// so parsing unit-tests without a real HTTP request.
//
// Any of the four headers present more than once returns
// errDuplicateForwardedHeader wrapping the header name, before any value is
// used — a duplicate is never trusted, not even first-value-wins. Otherwise,
// returns errNoForwardedCert when both -Subject and -Serial-Number are
// absent. Otherwise the identity is present; a -Leaf header that fails to
// decode returns the identity (Leaf == nil) alongside a non-nil, wrapped
// error describing the parse failure — callers must not treat that as
// absence.
func parseForwardedClientCert(h func(string) []string) (ForwardedClientCert, error) {
	for _, name := range []string{headerClientCertSubject, headerClientCertSerialNumber, headerClientCertIssuer, headerClientCertLeaf} {
		if len(h(name)) > 1 {
			return ForwardedClientCert{}, fmt.Errorf("%w: %s", errDuplicateForwardedHeader, name)
		}
	}

	subject := firstHeaderValue(h(headerClientCertSubject))
	serial := firstHeaderValue(h(headerClientCertSerialNumber))
	if subject == "" && serial == "" {
		return ForwardedClientCert{}, errNoForwardedCert
	}

	cert := ForwardedClientCert{
		Subject:      subject,
		Issuer:       firstHeaderValue(h(headerClientCertIssuer)),
		SerialNumber: serial,
	}

	encodedLeaf := firstHeaderValue(h(headerClientCertLeaf))
	if encodedLeaf == "" {
		return cert, nil
	}

	leaf, err := parseForwardedLeaf(encodedLeaf)
	if err != nil {
		return cert, fmt.Errorf("forwarded client cert leaf: %w", err)
	}
	cert.Leaf = leaf
	return cert, nil
}

// firstHeaderValue returns the sole value of a possibly-multi-valued header,
// or "" when absent. Callers must reject len(values) > 1 before calling this
// — see parseForwardedClientCert.
func firstHeaderValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// parseForwardedLeaf decodes and parses the X-Amzn-Mtls-Clientcert-Leaf
// header value into an x509.Certificate. AWS ALB verify mode URL-encodes the
// leaf's PEM with "+=/ as safe characters" — i.e. base64's +, =, and / are
// left literal, never percent-encoded — so url.PathUnescape is the correct
// decoder here: url.QueryUnescape additionally treats a literal + as a space,
// which would silently corrupt the base64 payload.
func parseForwardedLeaf(encoded string) (*x509.Certificate, error) {
	if len(encoded) > maxForwardedLeafBytes {
		return nil, fmt.Errorf("encoded leaf exceeds %d byte cap", maxForwardedLeafBytes)
	}

	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		return nil, fmt.Errorf("url-decode: %w", err)
	}

	block, _ := pem.Decode([]byte(decoded))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}

	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return leaf, nil
}

// forwardedClientCertMiddlewareEcho is the echo-native middleware SetupMiddlewares
// wires behind cfg.Server.ForwardedClientCert.Enabled. skipper bypasses parsing for
// health/ready probes (ALB health checks present no client certificate, so a
// non-exempt Require would take the target group down on deploy). l may be
// nil, in which case rejection/warning falls back to the stdlib log package
// (mirrors logTenantRejection's nil-logger fallback).
func forwardedClientCertMiddlewareEcho(cfg config.ForwardedClientCertConfig, skipper SkipperFunc, l logger.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if skipper != nil && skipper(c.Request()) {
				return next(c)
			}

			req := c.Request()
			identity, err := parseForwardedClientCert(req.Header.Values)

			switch {
			case err == nil:
				c.SetRequest(req.WithContext(withForwardedClientCert(req.Context(), identity)))
			case errors.Is(err, errDuplicateForwardedHeader):
				// A duplicated header is never trusted — same shape as absence
				// (fail closed under Require, fail open without it), never
				// first-value-wins. The WARN always fires, since it is the
				// primary signal that either the ALB posture changed or a
				// client-supplied copy of the header got through.
				logForwardedClientCertDuplicateWarning(l, c, err)
				if cfg.Require {
					return NewUnauthorizedError("Client certificate identity required")
				}
				// !Require: proceed with no identity attached.
			case errors.Is(err, errNoForwardedCert):
				if cfg.Require {
					logForwardedClientCertRejection(l, c)
					return NewUnauthorizedError("Client certificate identity required")
				}
				// !Require: proceed with no identity attached; the accessor returns false.
			default:
				// Leaf failed to decode, but Subject/Issuer/SerialNumber are present and
				// ALB-verified — never a rejection (Step 3/4 semantics). Attach the
				// partial identity (Leaf == nil) and always leave a WARN trail noting
				// the lost enrichment: an identical parse failure must not have
				// flag-dependent observability.
				c.SetRequest(req.WithContext(withForwardedClientCert(req.Context(), identity)))
				logForwardedClientCertLeafWarning(l, c, err, firstHeaderValue(req.Header.Values(headerClientCertLeaf)))
			}

			return next(c)
		}
	}
}

// logForwardedClientCertRejection emits one WARN per request rejected for a
// missing forwarded-client-cert identity (401). This middleware runs outer to
// the access logger (server/middleware.go) and never calls next() on reject,
// so without this WARN a rejected request leaves zero server-side trail —
// mirrors logTenantRejection's rationale (server/tenant_middleware.go).
// Header values are never logged, only the fixed reason string.
func logForwardedClientCertRejection(l logger.Logger, c *echo.Context) {
	req := c.Request()
	warnWithFallback(l, fmt.Sprintf("[server.forwardedclientcert] request rejected: missing identity method=%s path=%s client=%s status=%d reason=%q",
		logSafeValue(req.Method), logSafeValue(req.URL.Path), logSafeValue(req.RemoteAddr), http.StatusUnauthorized, "forwarded_client_cert_missing"))
}

// logForwardedClientCertDuplicateWarning emits one WARN whenever a request
// carries more than one value for a single X-Amzn-Mtls-Clientcert-* header.
// It fires regardless of Require, since a duplicated header is always
// treated as absent identity (fail closed under Require, fail open without
// it) — an identical condition must not have flag-dependent observability.
// dupErr's message names only the offending header, never a header value.
func logForwardedClientCertDuplicateWarning(l logger.Logger, c *echo.Context, dupErr error) {
	req := c.Request()
	warnWithFallback(l, fmt.Sprintf("[server.forwardedclientcert] duplicated header detected method=%s path=%s client=%s reason=%q detail=%q",
		logSafeValue(req.Method), logSafeValue(req.URL.Path), logSafeValue(req.RemoteAddr), "forwarded_client_cert_duplicate", dupErr.Error()))
}

// logForwardedClientCertLeafWarning emits a WARN whenever the -Leaf header
// failed to decode for a present, ALB-verified identity — regardless of
// Require, since an identical parse failure must not have flag-dependent
// observability. The request still passes on its verified Subject. Only the
// parse failure reason and the encoded leaf's byte length are logged — never
// header content.
func logForwardedClientCertLeafWarning(l logger.Logger, c *echo.Context, parseErr error, encodedLeaf string) {
	req := c.Request()
	warnWithFallback(l, fmt.Sprintf("[server.forwardedclientcert] leaf certificate not decoded method=%s path=%s client=%s reason=%q leaf_bytes=%d",
		logSafeValue(req.Method), logSafeValue(req.URL.Path), logSafeValue(req.RemoteAddr), parseErr.Error(), len(encodedLeaf)))
}

// warnWithFallback routes msg through l.Warn, falling back to the stdlib log
// package when l is nil (same fallback shape as logTenantRejection).
func warnWithFallback(l logger.Logger, msg string) {
	if l == nil {
		log.Printf("WARN %s", msg)
		return
	}
	l.Warn().Msg(msg)
}
