package server

import (
	"crypto/tls"
	"fmt"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/secretfile"
)

// buildServerTLSConfig loads the configured PEM material into a *tls.Config
// with a TLS 1.2 floor and no client-certificate verification (deferred; see
// ADR-042). It is only called when cfg.Enabled is true. Reading the
// filesystem happens here, at Start() time — not in config validation — so a
// bad or unreadable path still fails fast, just one hop later than a purely
// structural check could.
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

	minVersion, err := serverTLSMinVersion(cfg.MinVersion)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		MinVersion:   minVersion,
		Certificates: []tls.Certificate{pair},
	}, nil
}

// loadPEM reads one piece of PEM material from a file path or a
// base64-encoded value, delegating to secretfile.LoadPEM (shared with
// httpclient's loader, httpclient/tls.go). The server always requires both
// cert and key, so — unlike the client's optional-CA case — neither source
// set is an error here rather than a valid nil state.
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

// hasStagedServerTLSMaterial reports whether any TLS material field is set
// while TLS is disabled — the shape a staged-ahead-of-a-flip rollout takes,
// but also what a mistyped server.tls.enabled produces. Start() uses this to
// decide whether to WARN.
func hasStagedServerTLSMaterial(cfg *config.ServerTLSConfig) bool {
	return cfg.CertFile != "" || cfg.CertValue != "" || cfg.KeyFile != "" || cfg.KeyValue != ""
}

// serverTLSMinVersion maps the config enum to a tls.Config version constant.
// The default (empty string) and "1.2" share a floor; "1.3" raises it.
// Anything else is defense in depth — config validation already rejects it.
func serverTLSMinVersion(v string) (uint16, error) {
	switch v {
	case "", "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("server: tls: minversion %s: accepted values are \"1.2\" and \"1.3\"", secretfile.SafeRef(v))
	}
}
