package server

import "crypto/x509"

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
}
