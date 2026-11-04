package server

import (
	"crypto/x509"

	"github.com/gaborage/go-bricks/multitenant"
)

// Options carries programmatic server extensions that cannot be expressed in
// static config.
type Options struct {
	// TLSVerifyPeerCertificate, when set together with a verifying
	// server.tls.clientauth, runs after standard chain verification with the
	// stdlib crypto/tls signature — use it for SAN/OU allowlists and read
	// verifiedChains, never rawCerts, for identity. Returning an error rejects
	// the handshake. It also runs on resumed sessions (via VerifyConnection),
	// and is skipped for a client that presents no certificate, which only
	// server.tls.clientauth decides (ADR-130). Must be fast and
	// allocation-light: it is on the handshake path of every connection.
	TLSVerifyPeerCertificate func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error

	// TenantChecker vouches for a tenant id read from the request path (ADR-170). Under
	// multitenant.enabled the tenant seat writes a path-read id to the request only when
	// HasTenant answers true; any other path-read id is never written, and the route's handler
	// adapter answers 400 "Invalid tenant". A nil checker vouches for no id, so every path-read
	// tenant is refused. Header- and subdomain-read tenants are never asked. The App always
	// sets it from its tenant resource source, replacing any value given here.
	TenantChecker multitenant.TenantChecker
}
