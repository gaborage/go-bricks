package httpclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/internal/secretfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testServerName = "partner.example.com"

// newTestCA returns a self-signed CA and a function minting leaf certs signed by it.
func newTestCA(t *testing.T, cn string) (caCertPEM []byte, caCert *x509.Certificate, issue func(leafCN string, isServer bool) (certPEM, keyPEM []byte)) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err = x509.ParseCertificate(der)
	require.NoError(t, err)
	caCertPEM = pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: der})

	serial := int64(1)
	issue = func(leafCN string, isServer bool) (certPEM, keyPEM []byte) {
		t.Helper()
		serial++

		key, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)

		leaf := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: leafCN},
			NotBefore:    time.Now().Add(-time.Minute),
			NotAfter:     time.Now().Add(time.Hour),
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		if isServer {
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			leaf.DNSNames = []string{"127.0.0.1"}
			leaf.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
		}

		leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
		require.NoError(t, err)
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		require.NoError(t, err)

		return pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: leafDER}),
			pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	}

	return caCertPEM, caCert, issue
}

func b64PEM(material []byte) string {
	return base64.StdEncoding.EncodeToString(material)
}

// ed25519KeyPEM returns a PKCS#8 Ed25519 key: the compact shape whose DER is short
// enough to slip secretfile.MaxPathEcho and whose PEM length is not a multiple of 3.
func ed25519KeyPEM(t *testing.T) []byte {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func writeTestFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func TestNewClientTLSConfigFromValues(t *testing.T) {
	caPEM, _, issue := newTestCA(t, "values-ca")
	certPEM, keyPEM := issue("client", false)

	cfg, err := NewClientTLSConfig(&ClientTLSConfig{
		CertValue:         b64PEM(certPEM),
		KeyValue:          b64PEM(keyPEM),
		CAValue:           b64PEM(caPEM),
		RequireClientCert: true,
	})
	require.NoError(t, err)
	assert.Len(t, cfg.Certificates, 1)
	assert.NotNil(t, cfg.RootCAs)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
}

func TestNewClientTLSConfigFromFiles(t *testing.T) {
	caPEM, _, issue := newTestCA(t, "files-ca")
	certPEM, keyPEM := issue("client", false)

	dir := t.TempDir()
	cfg, err := NewClientTLSConfig(&ClientTLSConfig{
		CertFile: writeTestFile(t, dir, "client.crt", certPEM),
		KeyFile:  writeTestFile(t, dir, "client.key", keyPEM),
		CAFile:   writeTestFile(t, dir, "ca.crt", caPEM),
	})
	require.NoError(t, err)
	assert.Len(t, cfg.Certificates, 1)
	assert.NotNil(t, cfg.RootCAs)
}

func TestNewClientTLSConfigValidation(t *testing.T) {
	caPEM, _, issue := newTestCA(t, "validation-ca")
	certPEM, keyPEM := issue("client", false)
	_, otherKeyPEM := issue("other-client", false)

	corruptCA := pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: []byte("not-a-certificate")})

	tests := []struct {
		name    string
		cfg     *ClientTLSConfig
		wantErr string
	}{
		{
			name:    "nil_config",
			cfg:     nil,
			wantErr: "tls: config is nil",
		},
		{
			name:    "nothing_provided",
			cfg:     &ClientTLSConfig{},
			wantErr: "tls: no material provided",
		},
		{
			name:    "cert_without_key",
			cfg:     &ClientTLSConfig{CertValue: b64PEM(certPEM)},
			wantErr: "tls: cert: set without a matching key",
		},
		{
			name:    "key_without_cert",
			cfg:     &ClientTLSConfig{KeyValue: b64PEM(keyPEM)},
			wantErr: "tls: key: set without a matching cert",
		},
		{
			name:    "both_file_and_value",
			cfg:     &ClientTLSConfig{CertFile: "client.crt", CertValue: b64PEM(certPEM)},
			wantErr: "tls: cert: set file or value, not both",
		},
		{
			name:    "bad_base64",
			cfg:     &ClientTLSConfig{CAValue: "!!! not base64 !!!"},
			wantErr: "tls: ca: base64 decode",
		},
		{
			name:    "bad_pem_keypair",
			cfg:     &ClientTLSConfig{CertValue: b64PEM(certPEM), KeyValue: b64PEM(otherKeyPEM)},
			wantErr: "tls: cert/key:",
		},
		{
			name:    "bad_ca_pem",
			cfg:     &ClientTLSConfig{CAValue: b64PEM(corruptCA)},
			wantErr: "tls: ca: block 0:",
		},
		{
			name:    "ca_pem_without_certificate_block",
			cfg:     &ClientTLSConfig{CAValue: b64PEM(keyPEM)},
			wantErr: "tls: ca: no CERTIFICATE block found",
		},
		{
			name:    "bad_minversion",
			cfg:     &ClientTLSConfig{CAValue: b64PEM(caPEM), MinVersion: "1.1"},
			wantErr: `tls: minversion "1.1"`,
		},
		{
			name:    "require_client_cert_without_material",
			cfg:     &ClientTLSConfig{CAValue: b64PEM(caPEM), RequireClientCert: true},
			wantErr: "tls: require client cert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := NewClientTLSConfig(tt.cfg)
			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNewClientTLSConfigMinVersionAndServerName(t *testing.T) {
	caPEM, _, _ := newTestCA(t, "minversion-ca")

	cfg, err := NewClientTLSConfig(&ClientTLSConfig{
		CAValue:    b64PEM(caPEM),
		MinVersion: "1.3",
		ServerName: testServerName,
	})
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
	assert.Equal(t, testServerName, cfg.ServerName)
	assert.NotNil(t, cfg.RootCAs)
	// CA-only pins roots for server verification; it presents no client
	// certificate, so it is not mutual TLS.
	assert.Empty(t, cfg.Certificates)

	explicit12, err12 := NewClientTLSConfig(&ClientTLSConfig{
		CAValue:    b64PEM(caPEM),
		MinVersion: "1.2",
	})
	require.NoError(t, err12)
	assert.Equal(t, uint16(tls.VersionTLS12), explicit12.MinVersion)
}

func TestNewClientTLSConfigCABundleSkipsNonCertificateBlocks(t *testing.T) {
	caPEM, _, issue := newTestCA(t, "bundle-ca")
	_, keyPEM := issue("client", false)

	bundle := append(append([]byte{}, caPEM...), keyPEM...)
	cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(bundle)})
	require.NoError(t, err)
	assert.NotNil(t, cfg.RootCAs)
}

// A cert-only config must leave RootCAs nil, which is what "verify the server
// against the system roots" means to crypto/tls.
func TestNewClientTLSConfigCertOnlyKeepsSystemRoots(t *testing.T) {
	_, _, issue := newTestCA(t, "cert-only-ca")
	certPEM, keyPEM := issue("client", false)

	cfg, err := NewClientTLSConfig(&ClientTLSConfig{
		CertValue:         b64PEM(certPEM),
		KeyValue:          b64PEM(keyPEM),
		RequireClientCert: true,
	})
	require.NoError(t, err)
	assert.Len(t, cfg.Certificates, 1)
	assert.Nil(t, cfg.RootCAs)
}

func TestNewClientTLSConfigRejectsPEMContentInFileField(t *testing.T) {
	_, _, issue := newTestCA(t, "pem-in-path-ca")
	certPEM, keyPEM := issue("client", false)

	t.Run("raw_pem_body", func(t *testing.T) {
		cfg, err := NewClientTLSConfig(&ClientTLSConfig{
			CertValue: b64PEM(certPEM),
			KeyFile:   string(keyPEM),
		})
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "tls: key: file looks like key material")
		assert.NotContains(t, err.Error(), "PRIVATE KEY", "the error must not echo the key material")
	})

	// The likelier transposition: *Value fields take base64 PEM, which is what a
	// secret manager hands you, so a File/Value mix-up carries the base64 form.
	t.Run("base64_pem_body", func(t *testing.T) {
		encoded := b64PEM(keyPEM)
		cfg, err := NewClientTLSConfig(&ClientTLSConfig{
			CertValue: b64PEM(certPEM),
			KeyFile:   encoded,
		})
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "tls: key: file looks like key material")
		assert.NotContains(t, err.Error(), encoded, "the error must not echo the encoded key material")
	})

	// The quadrant the length bound cannot cover: base64 of raw DER carries no PEM
	// header at any level, and for EC/Ed25519 it is short enough to be echoed in
	// full. Both defenses previously missed this from opposite sides.
	t.Run("base64_der_under_the_echo_bound", func(t *testing.T) {
		_, edKey, genErr := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, genErr)
		der, derErr := x509.MarshalPKCS8PrivateKey(edKey)
		require.NoError(t, derErr)
		encoded := base64.StdEncoding.EncodeToString(der)
		require.Less(t, len(encoded), secretfile.MaxPathEcho, "the point of this case is that eliding cannot save us")

		cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAFile: encoded})
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "looks like key material")
		assert.NotContains(t, err.Error(), encoded, "the DER key must never reach the error string")
	})

	// StdEncoding rejects unpadded input, so whether a RawStdEncoding value is
	// caught used to depend on len(PEM)%3 — luck, not design. RSA-2048 PEM is 1704
	// bytes (%3 == 0), so its padded and unpadded forms are identical and cannot
	// exercise this; Ed25519 PEM is 119 bytes (%3 == 2) and can.
	t.Run("unpadded_base64_pem", func(t *testing.T) {
		edPEM := ed25519KeyPEM(t)
		unpadded := base64.RawStdEncoding.EncodeToString(edPEM)
		require.NotEqual(t, base64.StdEncoding.EncodeToString(edPEM), unpadded,
			"this case is vacuous unless the two encodings actually differ")

		cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAFile: unpadded})
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "looks like key material")
		assert.NotContains(t, err.Error(), unpadded)
	})

	t.Run("over_long_path_is_elided", func(t *testing.T) {
		secret := strings.Repeat("s3cr3t", 100)
		cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAFile: secret})
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.NotContains(t, err.Error(), secret, "an over-long value must not be echoed")
		assert.Contains(t, err.Error(), "char value elided")
	})

	t.Run("ordinary_path_still_reports_errno", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "absent.pem")
		cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAFile: missing})
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "tls: ca: read file")
		// %q-quoted, so on Windows the separators arrive escaped — comparing against
		// the raw path fails there for a reason that has nothing to do with the code.
		assert.Contains(t, err.Error(), fmt.Sprintf("%q", missing),
			"a plausible path stays in the message for diagnosability")
		assert.ErrorIs(t, err, fs.ErrNotExist, "unwrapping the PathError must not break errors.Is")
	})
}

// A bundle whose second root is mangled at the PEM layer must not load: pem.Decode
// skips it silently, so without the declared-vs-decoded check a staged CA rotation
// pins only the outgoing root and fails the moment the partner cuts over.
func TestNewClientTLSConfigRejectsPartiallyCorruptCABundle(t *testing.T) {
	firstPEM, _, _ := newTestCA(t, "root-outgoing")
	secondPEM, _, _ := newTestCA(t, "root-incoming")

	corruptions := map[string]func([]byte) []byte{
		"mangled_base64_body": func(b []byte) []byte {
			return bytes.Replace(b, []byte("\nMII"), []byte("\nM!I"), 1)
		},
		"indented_by_yaml_block_scalar": func(b []byte) []byte {
			return bytes.ReplaceAll(b, []byte("\n"), []byte("\n  "))
		},
		"truncated_tail": func(b []byte) []byte {
			return b[:len(b)-40]
		},
	}

	for name, corrupt := range corruptions {
		t.Run(name, func(t *testing.T) {
			bundle := append(append([]byte{}, firstPEM...), corrupt(secondPEM)...)
			cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(bundle)})
			require.Error(t, err, "a bundle that would pin fewer roots than declared must fail at startup")
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), "tls: ca:")
		})
	}

	t.Run("intact_two_root_bundle_loads", func(t *testing.T) {
		bundle := append(append([]byte{}, firstPEM...), secondPEM...)
		cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(bundle)})
		require.NoError(t, err)
		assert.NotNil(t, cfg.RootCAs)
	})
}

func TestWithTLSConfigSetsBaseTransport(t *testing.T) {
	caPEM, _, issue := newTestCA(t, "transport-ca")
	certPEM, keyPEM := issue("client", false)

	cfg, err := NewClientTLSConfig(&ClientTLSConfig{
		CertValue: b64PEM(certPEM),
		KeyValue:  b64PEM(keyPEM),
		CAValue:   b64PEM(caPEM),
	})
	require.NoError(t, err)

	built := NewBuilder(createTestLogger()).WithTLSConfig(cfg).Build()
	impl, ok := built.(*client)
	require.True(t, ok)
	transport, isTransport := impl.httpClient.Transport.(*nethttp.Transport)
	require.True(t, isTransport)
	assert.NotSame(t, nethttp.DefaultTransport, transport)

	require.NotNil(t, transport.TLSClientConfig)
	assert.NotSame(t, cfg, transport.TLSClientConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	assert.Len(t, transport.TLSClientConfig.Certificates, 1)
	assert.NotNil(t, transport.TLSClientConfig.RootCAs)

	// The base is DefaultTransport-equivalent, not a zero-value transport: losing
	// Proxy alone breaks every call behind a mandatory egress proxy.
	assert.NotNil(t, transport.Proxy)
	assert.True(t, transport.ForceAttemptHTTP2)
	assert.Equal(t, 100, transport.MaxIdleConns)
	assert.Equal(t, 90*time.Second, transport.IdleConnTimeout)

	if defaultTransport, isDefault := nethttp.DefaultTransport.(*nethttp.Transport); isDefault {
		assert.NotSame(t, cfg, defaultTransport.TLSClientConfig)
	}
}

func TestWithTLSConfigCallOrderPrecedence(t *testing.T) {
	caPEM, _, issue := newTestCA(t, "precedence-ca")
	certPEM, keyPEM := issue("client", false)

	cfg, err := NewClientTLSConfig(&ClientTLSConfig{
		CertValue: b64PEM(certPEM),
		KeyValue:  b64PEM(keyPEM),
		CAValue:   b64PEM(caPEM),
	})
	require.NoError(t, err)

	builder := NewBuilder(createTestLogger()).WithTLSConfig(nil)
	assert.Nil(t, builder.transport)

	stub := &stubRoundTripper{name: "tls-base-slot"}

	t.Run("tls_config_after_transport_wins", func(t *testing.T) {
		b := NewBuilder(createTestLogger()).WithTransport(stub).WithTLSConfig(cfg)
		tlsBase, isTLSBase := b.transport.(*nethttp.Transport)
		require.True(t, isTLSBase)
		require.NotNil(t, tlsBase.TLSClientConfig)
		assert.Equal(t, uint16(tls.VersionTLS12), tlsBase.TLSClientConfig.MinVersion)
		assert.Len(t, tlsBase.TLSClientConfig.Certificates, 1)
		assert.NotNil(t, tlsBase.TLSClientConfig.RootCAs)
	})

	t.Run("transport_after_tls_config_wins", func(t *testing.T) {
		b := NewBuilder(createTestLogger()).WithTLSConfig(cfg).WithTransport(stub)
		assert.Same(t, stub, b.transport)
	})

	t.Run("nil_tls_config_keeps_existing_transport", func(t *testing.T) {
		b := NewBuilder(createTestLogger()).WithTransport(stub).WithTLSConfig(nil)
		assert.Same(t, stub, b.transport)
	})
}

func TestWithTLSConfigDefaultTransportReplaced(t *testing.T) {
	caPEM, _, _ := newTestCA(t, "fallback-ca")
	cfg, err := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(caPEM)})
	require.NoError(t, err)

	// Non-parallel on purpose: the package has no t.Parallel() calls, so swapping
	// the global here cannot race another test.
	t.Run("replaced_default_transport_uses_seeded_fallback", func(t *testing.T) {
		original := nethttp.DefaultTransport
		t.Cleanup(func() { nethttp.DefaultTransport = original })
		nethttp.DefaultTransport = &stubRoundTripper{name: "replaced-default"}

		var builder *Builder
		require.NotPanics(t, func() {
			builder = NewBuilder(createTestLogger()).WithTLSConfig(cfg)
		})

		transport, isTransport := builder.transport.(*nethttp.Transport)
		require.True(t, isTransport)
		require.NotNil(t, transport.TLSClientConfig)
		assert.NotNil(t, transport.Proxy)
		assert.NotNil(t, transport.DialContext)
		assert.True(t, transport.ForceAttemptHTTP2)
		assert.Equal(t, 100, transport.MaxIdleConns)
		assert.Equal(t, 90*time.Second, transport.IdleConnTimeout)
		assert.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
		assert.Equal(t, time.Second, transport.ExpectContinueTimeout)
	})

	// The fallback transport's DialContext is a bound method value on a *net.Dialer,
	// which cannot be introspected once built — so the dialer's own Timeout/KeepAlive
	// are pinned directly against the factory the fallback transport actually uses.
	t.Run("fallback_dialer_has_the_documented_timeouts", func(t *testing.T) {
		dialer := fallbackDialer()
		assert.Equal(t, 30*time.Second, dialer.Timeout)
		assert.Equal(t, 30*time.Second, dialer.KeepAlive)
	})

	// net/http consults TLSClientConfig only when no TLS dialer is set, so a dialer
	// cloned from a replaced global would silently void pinning, the version floor
	// and the client certificate.
	t.Run("cloned_tls_dialer_is_cleared", func(t *testing.T) {
		defaultTransport, isDefault := nethttp.DefaultTransport.(*nethttp.Transport)
		require.True(t, isDefault)

		original := defaultTransport.DialTLSContext
		t.Cleanup(func() { defaultTransport.DialTLSContext = original })
		defaultTransport.DialTLSContext = func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("must never be consulted")
		}

		builder := NewBuilder(createTestLogger()).WithTLSConfig(cfg)
		transport, isTransport := builder.transport.(*nethttp.Transport)
		require.True(t, isTransport)
		assert.Nil(t, transport.DialTLSContext, "an inherited TLS dialer would discard TLSClientConfig wholesale")
		//nolint:staticcheck // SA1019: pins that the deprecated field is cleared too — it still bypasses TLSClientConfig when DialTLSContext is nil.
		assert.Nil(t, transport.DialTLS)
		assert.NotNil(t, transport.TLSClientConfig)
	})

	// Without this the clone branch is unpinned: because the fallback is
	// field-equal to the stdlib default, collapsing WithTLSConfig to always use
	// the literal would discard a consumer's customization with a green suite.
	t.Run("intact_default_transport_is_cloned_not_synthesized", func(t *testing.T) {
		defaultTransport, isDefault := nethttp.DefaultTransport.(*nethttp.Transport)
		require.True(t, isDefault)

		original := defaultTransport.MaxIdleConnsPerHost
		t.Cleanup(func() { defaultTransport.MaxIdleConnsPerHost = original })
		defaultTransport.MaxIdleConnsPerHost = 42

		builder := NewBuilder(createTestLogger()).WithTLSConfig(cfg)
		transport, isTransport := builder.transport.(*nethttp.Transport)
		require.True(t, isTransport)
		assert.Equal(t, 42, transport.MaxIdleConnsPerHost, "the live global must be cloned, not re-synthesized")
	})
}

func TestClientTLSMutualAuthentication(t *testing.T) {
	caPEM, caCert, issue := newTestCA(t, "mtls-ca")
	serverCertPEM, serverKeyPEM := issue("127.0.0.1", true)
	clientCertPEM, clientKeyPEM := issue("client", false)

	serverPair, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	require.NoError(t, err)
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(caCert)

	var hits atomic.Int64
	srv := httptest.NewUnstartedServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		hits.Add(1)
		w.WriteHeader(nethttp.StatusOK)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverPair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	defer srv.Close()

	get := func(t *testing.T, cfg *tls.Config) (*Response, error) {
		t.Helper()
		c := NewBuilder(createTestLogger()).WithTLSConfig(cfg).Build()
		return c.Get(context.Background(), &Request{URL: srv.URL})
	}

	t.Run("valid_client_cert_accepted", func(t *testing.T) {
		cfg, cfgErr := NewClientTLSConfig(&ClientTLSConfig{
			CertValue:         b64PEM(clientCertPEM),
			KeyValue:          b64PEM(clientKeyPEM),
			CAValue:           b64PEM(caPEM),
			RequireClientCert: true,
		})
		require.NoError(t, cfgErr)

		before := hits.Load()
		resp, reqErr := get(t, cfg)
		require.NoError(t, reqErr)
		assert.Equal(t, nethttp.StatusOK, resp.StatusCode)
		assert.Equal(t, before+1, hits.Load(), "the valid client certificate must reach the handler")
	})

	t.Run("no_client_cert_rejected", func(t *testing.T) {
		cfg, cfgErr := NewClientTLSConfig(&ClientTLSConfig{CAValue: b64PEM(caPEM)})
		require.NoError(t, cfgErr)

		before := hits.Load()
		_, reqErr := get(t, cfg)
		require.Error(t, reqErr)
		assert.Contains(t, reqErr.Error(), "remote error: tls:", "the server must reject us with a TLS alert")
		assert.NotContains(t, reqErr.Error(), "x509:", "must not be a local server-cert verification failure")
		assert.Equal(t, before, hits.Load(), "a rejected handshake must not reach the handler")
	})

	t.Run("untrusted_server_cert_rejected", func(t *testing.T) {
		otherCAPEM, _, _ := newTestCA(t, "other-ca")
		cfg, cfgErr := NewClientTLSConfig(&ClientTLSConfig{
			CertValue: b64PEM(clientCertPEM),
			KeyValue:  b64PEM(clientKeyPEM),
			CAValue:   b64PEM(otherCAPEM),
		})
		require.NoError(t, cfgErr)

		before := hits.Load()
		_, reqErr := get(t, cfg)
		require.Error(t, reqErr)
		assert.Contains(t, reqErr.Error(), "x509: certificate signed by unknown authority")
		assert.Equal(t, before, hits.Load(), "a rejected handshake must not reach the handler")
	})

	// Exercises the httptest server's ClientCAs check rather than go-bricks code;
	// kept because it is the issue's literal acceptance criterion.
	t.Run("wrong_ca_client_cert_rejected", func(t *testing.T) {
		_, _, otherIssue := newTestCA(t, "rogue-ca")
		rogueCertPEM, rogueKeyPEM := otherIssue("rogue-client", false)

		cfg, cfgErr := NewClientTLSConfig(&ClientTLSConfig{
			CertValue: b64PEM(rogueCertPEM),
			KeyValue:  b64PEM(rogueKeyPEM),
			CAValue:   b64PEM(caPEM),
		})
		require.NoError(t, cfgErr)
		require.Len(t, cfg.Certificates, 1)

		// Go filters Certificates against the server's advertised CAs and would
		// otherwise send an empty Certificate message, making this case
		// indistinguishable from no_client_cert_rejected.
		rogue := cfg.Certificates[0]
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &rogue, nil
		}

		before := hits.Load()
		_, reqErr := get(t, cfg)
		require.Error(t, reqErr)
		assert.Contains(t, reqErr.Error(), "remote error: tls: unknown certificate authority")
		assert.Equal(t, before, hits.Load(), "a rejected handshake must not reach the handler")
	})
}
