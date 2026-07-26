package httpclient

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	nethttp "net/http"
	"os"
	"strings"
	"time"
)

const pemTypeCertificate = "CERTIFICATE"

// pemHeaderPrefix opens every PEM block, so a *File value containing it is PEM
// content pasted into the wrong field rather than a path.
const pemHeaderPrefix = "-----BEGIN"

// maxPathEcho bounds what a read error quotes back. A *File value longer than
// this is elided: over-long values are the shape mis-filed material takes, and
// this error reaches startup logs.
const maxPathEcho = 256

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
	minVersion, err := parseTLSMinVersion(cfg.MinVersion)
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
	b.transport = base
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
// value, returning (nil, nil) when neither source is set.
func loadPEM(file, value, what string) ([]byte, error) {
	switch {
	case file != "" && value != "":
		return nil, fmt.Errorf("httpclient: tls: %s: set file or value, not both", what)
	case file != "":
		// Never let mis-filed material reach os.ReadFile: both our wrap and the
		// *os.PathError it wraps would echo it into a startup error.
		if looksLikeKeyMaterial(file) {
			return nil, fmt.Errorf("httpclient: tls: %s: file looks like key material, not a path (use the value field for inline PEM)", what)
		}
		// #nosec G304 -- the path is deployment configuration, not request input:
		// reading an operator-named file IS this function. Inline material is
		// rejected above. Same shape as keystore.go's loadKeyBytes, which gosec
		// leaves alone only because it reads a struct field rather than a parameter.
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("httpclient: tls: %s: read file %s: %w", what, safeFileRef(file), errnoOf(err))
		}
		return data, nil
	case value != "":
		data, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("httpclient: tls: %s: base64 decode: %w", what, err)
		}
		return data, nil
	default:
		return nil, nil
	}
}

// looksLikeKeyMaterial reports whether a *File value is inline key material
// rather than a path. It covers a raw PEM body, its base64 form (what a secret
// manager hands you, and so the likelier mix-up), and base64 of raw DER — which
// carries no PEM header at any level and, for EC and Ed25519 keys, is short
// enough to slip maxPathEcho too. Decoding unpadded accepts both base64 forms:
// whether a value arrives padded otherwise depends on len(input)%3.
func looksLikeKeyMaterial(file string) bool {
	if strings.Contains(file, pemHeaderPrefix) {
		return true
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(file, "="))
	if err != nil {
		return false
	}
	// 0x30 is the ASN.1 SEQUENCE tag that opens PKCS#8, SEC1, PKCS#12 and X.509.
	return strings.Contains(string(decoded), pemHeaderPrefix) || (len(decoded) > 0 && decoded[0] == 0x30)
}

// safeFileRef renders a *File value for an error message, eliding anything too
// long to be a plausible path.
func safeFileRef(file string) string {
	if len(file) > maxPathEcho {
		return fmt.Sprintf("<%d-char value elided>", len(file))
	}
	return fmt.Sprintf("%q", file)
}

// errnoOf strips the *os.PathError wrapper, which carries its own copy of the
// path and would re-echo a value safeFileRef elided. errors.Is is unaffected:
// the bare errno still matches fs.ErrNotExist and friends.
func errnoOf(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
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

func parseTLSMinVersion(v string) (uint16, error) {
	switch v {
	case "", "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("httpclient: tls: minversion %s: accepted values are \"1.2\" and \"1.3\"", safeFileRef(v))
	}
}
