package httpclient

import (
	"crypto/tls"
	"errors"
	"net"
	nethttp "net/http"
	"time"

	"github.com/gaborage/go-bricks/internal/clienttls"
)

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

	material := clienttls.Material{
		CertFile:   cfg.CertFile,
		CertValue:  cfg.CertValue,
		KeyFile:    cfg.KeyFile,
		KeyValue:   cfg.KeyValue,
		CAFile:     cfg.CAFile,
		CAValue:    cfg.CAValue,
		ServerName: cfg.ServerName,
		MinVersion: cfg.MinVersion,
	}
	// Both guards run before Build because they are httpclient's own rules: the
	// shared loader accepts material-free input (system roots) and knows nothing
	// about mutual-TLS intent. Keeping them here preserves the order in which a
	// misconfiguration is reported.
	if cfg.RequireClientCert && cfg.CertFile == "" && cfg.CertValue == "" && cfg.KeyFile == "" && cfg.KeyValue == "" {
		return nil, errors.New("httpclient: tls: require client cert: cert and key are empty")
	}
	if !clienttls.HasAnyMaterial(&material) {
		return nil, errors.New("httpclient: tls: no material provided: set cert and key, ca, or both")
	}
	return clienttls.Build("httpclient: tls:", &material)
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
// inclusion — add new tls.Config fields here when in doubt.
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

// transportCarriesTLSMaterial reports whether t decides its own TLS: either a
// TLSClientConfig holding real material, or a TLS dialer — net/http skips its
// own handshake, and so ignores TLSClientConfig entirely, whenever one is set.
// Both base-slot directions ask this same question, so they share one predicate
// rather than two lists that can drift apart. Everything it names is something
// WithTLSConfig clears or overwrites — keep the two in sync.
func transportCarriesTLSMaterial(t *nethttp.Transport) bool {
	if t == nil {
		return false
	}
	//nolint:staticcheck // SA1019: DialTLS is deprecated but still honored when DialTLSContext is nil, so a caller-set DialTLS is real security material we must not silently drop.
	return tlsConfigCarriesMaterial(t.TLSClientConfig) || t.DialTLS != nil || t.DialTLSContext != nil
}

// baseTransportForTLS clones an incumbent *nethttp.Transport as the compose
// base when possible.
func (b *Builder) baseTransportForTLS() (base *nethttp.Transport, losslessOrNoMaterial bool) {
	incumbent, isTransport := b.transport.(*nethttp.Transport)
	// The incumbent != nil guard is not redundant with isTransport: fillBaseSlot's
	// nil check is an interface check, so WithTransport((*nethttp.Transport)(nil))
	// fills the slot with a typed nil that satisfies this assertion and would
	// panic on Clone().
	if isTransport && incumbent != nil {
		// Must run before Clone(): Clone's onceSetNextProtoDefaults mutates the
		// receiver, populating an ALPN-only TLSClientConfig (see tlsConfigCarriesMaterial).
		hadNoTLSMaterial := !transportCarriesTLSMaterial(incumbent)
		return incumbent.Clone(), hadNoTLSMaterial
	}
	// Reaching here with isTransport set means that typed nil: it holds nothing to
	// lose, so replacing it is not a reportable displacement. An opaque
	// RoundTripper is, which is why this is not simply true.
	nothingToLose := isTransport
	// Consumers replace nethttp.DefaultTransport (gock, httpmock, APM agents), so
	// a bare type assertion would panic mid-chain. A replaced global cannot be
	// recovered, so the fallback mirrors the stdlib http.DefaultTransport values
	// instead of dropping proxy support and HTTP/2.
	if dt, ok := nethttp.DefaultTransport.(*nethttp.Transport); ok {
		return dt.Clone(), nothingToLose
	}
	return &nethttp.Transport{
		Proxy:                 nethttp.ProxyFromEnvironment,
		DialContext:           fallbackDialer().DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}, nothingToLose
}

// fallbackDialer is factored out so its Timeout/KeepAlive — otherwise opaque
// once bound into a DialContext closure — are directly assertable in a test.
func fallbackDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
}
