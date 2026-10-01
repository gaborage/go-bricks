package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/secretfile"
)

// buildServerTLSConfig loads the configured PEM material into a *tls.Config
// with a TLS 1.2 floor and, when server.tls.clientauth is set, client
// certificates verified against the client-CA bundle (ADR-130). It is only
// called when cfg.Enabled is true. Reading the filesystem happens here, at
// Start() time — not in config validation — so a bad or unreadable path still
// fails fast, just one hop later than a purely structural check could.
func buildServerTLSConfig(cfg *config.ServerTLSConfig) (*tls.Config, error) {
	certPEM, err := loadPEM(cfg.CertFile, cfg.CertValue, "cert")
	if err != nil {
		return nil, err
	}
	keyPEM, err := loadPEM(cfg.KeyFile, cfg.KeyValue, "key")
	if err != nil {
		return nil, err
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("server: tls: cert/key: %w", err)
	}

	minVersion, err := secretfile.ParseTLSMinVersion("server: tls:", cfg.MinVersion)
	if err != nil {
		return nil, err
	}

	clientAuth, err := parseClientAuth(cfg.ClientAuth)
	if err != nil {
		return nil, err
	}

	tlsCfg := &tls.Config{
		MinVersion:   minVersion,
		Certificates: []tls.Certificate{pair},
		ClientAuth:   clientAuth,
	}
	if clientAuth == tls.NoClientCert {
		return tlsCfg, nil
	}

	caPEM, err := loadPEM(cfg.ClientCAFile, cfg.ClientCAValue, "client ca")
	if err != nil {
		return nil, err
	}
	pool, err := secretfile.CertPool("server: tls: client", caPEM)
	if err != nil {
		return nil, err
	}
	tlsCfg.ClientCAs = pool
	return tlsCfg, nil
}

// inertLeafHookWarnMsg is the WARN Start logs for a leaf-validation hook on a
// plaintext listener.
const inertLeafHookWarnMsg = "a TLS leaf-validation hook is set but server.tls.enabled is false; the hook is inert"

// leafVerifier is the stdlib VerifyPeerCertificate signature a leaf-validation
// hook takes (Options.TLSVerifyPeerCertificate).
type leafVerifier = func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error

// attachLeafHook installs hook on tlsCfg as VerifyPeerCertificate and, because
// the stdlib skips VerifyPeerCertificate on a resumed session, runs it again
// from VerifyConnection whenever the session resumed. A nil hook is a no-op.
// A hook under a non-verifying policy fails closed: it would guard nothing.
func attachLeafHook(tlsCfg *tls.Config, clientAuth string, hook leafVerifier) error {
	if hook == nil {
		return nil
	}
	if tlsCfg.ClientAuth != tls.VerifyClientCertIfGiven && tlsCfg.ClientAuth != tls.RequireAndVerifyClientCert {
		return fmt.Errorf("server: tls: clientauth %s never verifies client certificates, so the leaf-validation hook would be inert: use %q or %q",
			secretfile.SafeRef(clientAuth), clientAuthVerify, clientAuthRequireVerify)
	}
	verify := guardLeafHook(hook)
	tlsCfg.VerifyPeerCertificate = verify
	// SECURITY: VerifyPeerCertificate is skipped on resumed sessions, so the
	// same policy must also run via VerifyConnection, which always fires.
	tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if !cs.DidResume {
			return nil // full handshake: already checked by VerifyPeerCertificate
		}
		raw := make([][]byte, 0, len(cs.PeerCertificates))
		for _, c := range cs.PeerCertificates {
			raw = append(raw, c.Raw)
		}
		return verify(raw, cs.VerifiedChains)
	}
	return nil
}

// guardLeafHook skips hook for a client that presented no certificate — whether
// one may do that is server.tls.clientauth's decision alone, and the probe
// listener's certless self-check must pass under verify — and turns a hook
// panic into a handshake error that names the panic's type, never its value
// (ADR-081): net/http's own recover would log the value.
func guardLeafHook(hook leafVerifier) leafVerifier {
	return func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) (err error) {
		if len(rawCerts) == 0 {
			return nil
		}
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("server: tls: leaf-validation hook panicked (type: %T)", r)
			}
		}()
		return hook(rawCerts, verifiedChains)
	}
}

// server.tls.clientauth values; "" turns client verification off. The
// stdlib's unverified modes (request/require) are deliberately unreachable.
const (
	clientAuthVerify        = "verify"
	clientAuthRequireVerify = "require-verify"
)

// parseClientAuth maps server.tls.clientauth to the handshake policy. Both
// non-empty values verify the chain; they differ only in whether a client
// may omit its certificate. It repeats config validation's enum check so a
// config that skipped validation cannot start with verification silently off.
func parseClientAuth(v string) (tls.ClientAuthType, error) {
	if v == "" {
		return tls.NoClientCert, nil
	}
	if v == clientAuthVerify {
		return tls.VerifyClientCertIfGiven, nil
	}
	if v == clientAuthRequireVerify {
		return tls.RequireAndVerifyClientCert, nil
	}
	return tls.NoClientCert, fmt.Errorf("server: tls: clientauth %s: accepted values are %q and %q",
		secretfile.SafeRef(v), clientAuthVerify, clientAuthRequireVerify)
}

// loadPEM reads one piece of PEM material from a file path or a
// base64-encoded value, delegating to secretfile.LoadPEM (shared with
// httpclient's loader, httpclient/tls.go). Every piece it loads is required
// (the cert, the key, and the client CA under a verifying policy), so —
// unlike the client's optional-CA case — neither source set is an error here
// rather than a valid nil state.
func loadPEM(file, value, what string) ([]byte, error) {
	data, err := secretfile.LoadPEM("server: tls:", file, value, what)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("server: tls: %s: no material provided", what)
	}
	return data, nil
}

// hasStagedServerTLSMaterial reports whether any TLS material or client-auth
// field is set while TLS is disabled — the shape a staged-ahead-of-a-flip
// rollout takes, but also what a mistyped server.tls.enabled produces.
// Start() uses this to decide whether to WARN.
func hasStagedServerTLSMaterial(cfg *config.ServerTLSConfig) bool {
	return cfg.CertFile != "" || cfg.CertValue != "" || cfg.KeyFile != "" || cfg.KeyValue != "" ||
		cfg.ClientAuth != "" || cfg.ClientCAFile != "" || cfg.ClientCAValue != ""
}
