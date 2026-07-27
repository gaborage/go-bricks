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
// hatch for local testing is passing a hand-built *tls.Config to WithTLSConfig.
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

// WithTLSConfig installs tlsCfg on a fresh base transport: a clone of
// http.DefaultTransport, or an equivalently-configured transport when that
// global has been replaced. It fills the same base-transport slot as
// WithTransport: whichever of the two is called last wins. Wrapper layers
// (WithJOSE, for example) always apply on top of this base regardless of call
// order. A nil tlsCfg is a no-op.
//
// The config is cloned, so one loaded config can be shared across clients. The
// clone is required, not defensive style: net/http appends ALPN protocols to
// TLSClientConfig.NextProtos in place on a transport's first request, so two
// clients sharing one config race. The copy is shallow: reference fields such
// as Certificates and RootCAs stay shared with the caller and must not be
// mutated in place — rotate certificates through GetClientCertificate, which
// Clone preserves.
func (b *Builder) WithTLSConfig(tlsCfg *tls.Config) *Builder {
	if tlsCfg == nil {
		return b
	}
	var base *nethttp.Transport
	// Consumers replace nethttp.DefaultTransport (gock, httpmock, APM agents), so
	// a bare type assertion would panic mid-chain. A replaced global cannot be
	// recovered, so the fallback mirrors the stdlib http.DefaultTransport values
	// instead of dropping proxy support and HTTP/2.
	if dt, ok := nethttp.DefaultTransport.(*nethttp.Transport); ok {
		base = dt.Clone()
	} else {
		base = &nethttp.Transport{
			Proxy: nethttp.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}
	// net/http skips its own handshake entirely when a TLS dialer is set, so a
	// cloned one from a replaced global would discard tlsCfg wholesale — pinning,
	// version floor and client certificate included.
	//nolint:staticcheck // SA1019: DialTLS is deprecated but still honored when DialTLSContext is nil, so Clone can carry a live TLS bypass in it — clearing it is the point.
	base.DialTLS = nil
	base.DialTLSContext = nil
	base.TLSClientConfig = tlsCfg.Clone()
	b.fillBaseSlot(base, baseTLS)
	return b
}

// loadClientKeyPair resolves the client certificate material and enforces the
// cert/key pairing rules.
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
