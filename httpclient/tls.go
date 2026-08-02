package httpclient

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	nethttp "net/http"
	"time"

	"github.com/gaborage/go-bricks/internal/secretfile"
)

const pemTypeCertificate = "CERTIFICATE"

// pemHeaderPrefix opens every PEM block, so counting it gives the number of
// blocks a CA bundle declares — what certPoolFromPEM checks against the number
// that actually parsed.
const pemHeaderPrefix = "-----BEGIN"

// ClientTLSConfig describes client-side TLS material declaratively. Each piece
// comes from a PEM file path (File) or a base64-encoded PEM string (Value) —
// set exactly one source per provided piece. Cert and Key must be provided
// together (the client certificate); CA is optional and, when set, REPLACES the
// system roots for server verification (private-CA pinning), so a client that
// pins a private CA can no longer verify public-CA endpoints. Providing no
// material at all is an error — use WithTLSConfig with a hand-built *tls.Config
// for setups this does not cover.
//
// Every field is a comparable type on purpose: a slice or map would make the
// exported struct non-comparable, which apidiff reports as INCOMPATIBLE.
type ClientTLSConfig struct {
	CertFile  string
	CertValue string
	KeyFile   string
	KeyValue  string
	CAFile    string
	CAValue   string

	// ServerName overrides SNI / hostname verification (optional).
	ServerName string
	// MinVersion is "1.2" (default when empty) or "1.3".
	MinVersion string

	// RequireClientCert makes a missing client certificate an error instead of
	// silently producing a server-authentication-only config. Set it whenever the
	// deployment intends mutual TLS: a CA-only config is valid (root pinning) but
	// presents no client certificate.
	RequireClientCert bool
}

// NewClientTLSConfig loads the declared material into a *tls.Config with a TLS
// 1.2 floor. It never disables certificate verification; the explicit escape
// NewClientTLSConfig creates a TLS configuration from client certificate, private key,
// and CA material, using TLS 1.2 when no minimum version is specified. It returns an
// error when the configuration or supplied certificate material is invalid or empty.
func NewClientTLSConfig(cfg *ClientTLSConfig) (*tls.Config, error) {
	if cfg == nil {
		return nil, errors.New("httpclient: tls: config is nil")
	}

	certPEM, keyPEM, err := loadClientKeyPair(cfg)
	if err != nil {
		return nil, err
	}
	caPEM, err := loadPEM(cfg.CAFile, cfg.CAValue, "ca")
	if err != nil {
		return nil, err
	}
	if certPEM == nil && caPEM == nil {
		return nil, errors.New("httpclient: tls: no material provided: set cert and key, ca, or both")
	}
	minVersion, err := secretfile.ParseTLSMinVersion("httpclient: tls:", cfg.MinVersion)
	if err != nil {
		return nil, err
	}

	out := &tls.Config{
		MinVersion: minVersion,
		ServerName: cfg.ServerName,
	}
	if certPEM != nil {
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("httpclient: tls: cert/key: %w", err)
		}
		out.Certificates = []tls.Certificate{pair}
	}
	if caPEM != nil {
		pool, err := certPoolFromPEM(caPEM)
		if err != nil {
			return nil, err
		}
		out.RootCAs = pool
	}
	return out, nil
}

// WithTLSConfig fills the base-transport slot: it clones an incumbent
// *nethttp.Transport when present (or DefaultTransport otherwise) and
// replaces — never merges — its TLSClientConfig with tlsCfg. Last call
// between this and WithTransport wins. A nil tlsCfg is a no-op.
// The clone is shallow: don't mutate tlsCfg's Certificates/RootCAs in
// place — rotate via GetClientCertificate.
func (b *Builder) WithTLSConfig(tlsCfg *tls.Config) *Builder {
	if tlsCfg == nil {
		return b
	}
	base, losslessOrNoMaterial := b.baseTransportForTLS()
	// A TLS dialer makes net/http skip its own handshake, silently bypassing tlsCfg.
	//nolint:staticcheck // SA1019: DialTLS is deprecated but still honored when DialTLSContext is nil, so Clone can carry a live TLS bypass in it — clearing it is the point.
	base.DialTLS = nil
	base.DialTLSContext = nil
	base.TLSClientConfig = tlsCfg.Clone()
	b.fillBaseSlot(base, baseTLS)
	if losslessOrNoMaterial {
		// No material lost, so this is composition, not a reportable displacement.
		b.displacedBase = baseNone
	}
	return b
}

// tlsConfigCarriesMaterial is not a nil check: Clone() mutates its receiver
// with an ALPN-only default, so nilness alone is unreliable. Errs toward
// tlsConfigCarriesMaterial reports whether cfg contains meaningful TLS configuration.
// It returns false for a nil configuration or one containing only default settings.
func tlsConfigCarriesMaterial(cfg *tls.Config) bool {
	if cfg == nil {
		return false
	}
	return len(cfg.Certificates) > 0 ||
		cfg.GetClientCertificate != nil ||
		cfg.RootCAs != nil ||
		cfg.InsecureSkipVerify ||
		cfg.MinVersion != 0 ||
		cfg.MaxVersion != 0 ||
		cfg.ServerName != "" ||
		len(cfg.CipherSuites) > 0 ||
		len(cfg.CurvePreferences) > 0 ||
		cfg.Renegotiation != tls.RenegotiateNever ||
		cfg.VerifyPeerCertificate != nil ||
		cfg.VerifyConnection != nil
}

// baseTransportForTLS clones an incumbent *nethttp.Transport as the compose
// base when possible; anything WithTLSConfig clears or overwrites below is
// material — keep both lists in sync.
func (b *Builder) baseTransportForTLS() (base *nethttp.Transport, losslessOrNoMaterial bool) {
	if incumbent, ok := b.transport.(*nethttp.Transport); ok {
		// Must run before Clone(): Clone's onceSetNextProtoDefaults mutates the
		// receiver, populating an ALPN-only TLSClientConfig (see tlsConfigCarriesMaterial).
		//nolint:staticcheck // SA1019: DialTLS is deprecated but still honored when DialTLSContext is nil, so a caller-set DialTLS is real security material we must not silently drop.
		hadNoTLSMaterial := !tlsConfigCarriesMaterial(incumbent.TLSClientConfig) &&
			incumbent.DialTLS == nil && incumbent.DialTLSContext == nil
		return incumbent.Clone(), hadNoTLSMaterial
	}
	// Consumers replace nethttp.DefaultTransport (gock, httpmock, APM agents), so
	// a bare type assertion would panic mid-chain. A replaced global cannot be
	// recovered, so the fallback mirrors the stdlib http.DefaultTransport values
	// instead of dropping proxy support and HTTP/2.
	if dt, ok := nethttp.DefaultTransport.(*nethttp.Transport); ok {
		return dt.Clone(), false
	}
	return &nethttp.Transport{
		Proxy:                 nethttp.ProxyFromEnvironment,
		DialContext:           fallbackDialer().DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}, false
}

// fallbackDialer is factored out so its Timeout/KeepAlive — otherwise opaque
// fallbackDialer returns a network dialer with 30-second connection timeout and keep-alive settings.
func fallbackDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
}

// loadClientKeyPair resolves the client certificate material and enforces the
// loadClientKeyPair loads the configured client certificate and private key PEM data.
// It requires the certificate and key to be provided together and requires both when client authentication is mandatory.
func loadClientKeyPair(cfg *ClientTLSConfig) (certPEM, keyPEM []byte, err error) {
	certPEM, err = loadPEM(cfg.CertFile, cfg.CertValue, "cert")
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = loadPEM(cfg.KeyFile, cfg.KeyValue, "key")
	if err != nil {
		return nil, nil, err
	}
	switch {
	case certPEM != nil && keyPEM == nil:
		return nil, nil, errors.New("httpclient: tls: cert: set without a matching key")
	case certPEM == nil && keyPEM != nil:
		return nil, nil, errors.New("httpclient: tls: key: set without a matching cert")
	case certPEM == nil && cfg.RequireClientCert:
		return nil, nil, errors.New("httpclient: tls: require client cert: cert and key are empty")
	}
	return certPEM, keyPEM, nil
}

// loadPEM reads one piece of PEM material from a file path or a base64-encoded
// value, returning (nil, nil) when neither source is set. Delegates to
// secretfile.LoadPEM, which httpclient shares with the server TLS listener
// (server/tls.go) — the two loaders were maintained as parallel copies until
// this extraction.
func loadPEM(file, value, what string) ([]byte, error) {
	return secretfile.LoadPEM("httpclient: tls:", file, value, what)
}

// certPoolFromPEM refuses to pin fewer roots than the bundle asks for, which
// AppendCertsFromPEM's boolean cannot express: it returns true whenever ANY
// block parsed. Corruption hides at two layers and both are checked — a mangled
// base64 body makes pem.Decode skip the block silently (a YAML block scalar that
// indents the PEM does exactly this), while a well-framed block with bad DER
// fails x509.ParseCertificate. A staged CA rotation that quietly pins only the
// outgoing root is the failure this prevents: startup is clean and every call
// dies the moment the partner cuts over.
func certPoolFromPEM(caPEM []byte) (*x509.CertPool, error) {
	declared := bytes.Count(caPEM, []byte(pemHeaderPrefix))
	pool := x509.NewCertPool()
	rest := caPEM
	decoded, certs := 0, 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		decoded++
		// Non-certificate blocks are legitimate: a bundle may carry a key
		// alongside its chain. Only undecodable ones are an error.
		if block.Type != pemTypeCertificate {
			continue
		}
		crt, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("httpclient: tls: ca: block %d: %w", decoded-1, err)
		}
		pool.AddCert(crt)
		certs++
	}
	if declared != decoded {
		return nil, fmt.Errorf("httpclient: tls: ca: %d PEM blocks declared but only %d decodable — the bundle is corrupt and would pin fewer roots than intended", declared, decoded)
	}
	if certs == 0 {
		return nil, fmt.Errorf("httpclient: tls: ca: no %s block found", pemTypeCertificate)
	}
	return pool, nil
}
