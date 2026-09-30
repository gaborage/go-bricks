package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

// newTestCA mints a self-signed test CA and returns issueServer, a factory for
// server leaf certificates signed by it. issueServer's leaf carries both a
// DNSNames and an IPAddresses SAN for 127.0.0.1 — the IP SAN is what a
// https://127.0.0.1:<port> dial actually validates; omitting it produces
// opaque handshake failures rather than a clean rejection.
func newTestCA(t *testing.T, cn string) (caCertPEM []byte, issueServer func(cn string) (certPEM, keyPEM []byte)) {
	t.Helper()
	caCertPEM, issueLeaf := newTestCAWithSANs(t, cn)
	return caCertPEM, func(leafCN string) (leafCertPEM, leafKeyPEM []byte) {
		return issueLeaf(leafCN, []string{"127.0.0.1"}, []net.IP{net.ParseIP("127.0.0.1")})
	}
}

// newTestCAWithSANs is newTestCA with a leaf factory that takes the leaf's exact SANs,
// none included, for the probe listener's pinned application-listener check; each option
// edits the leaf template before signing.
func newTestCAWithSANs(t *testing.T, cn string) (caCertPEM []byte, issueLeaf func(cn string, dnsNames []string, ips []net.IP, opts ...func(*x509.Certificate)) (certPEM, keyPEM []byte)) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)

	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	caCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	nextSerial := int64(2) // 1 is the CA's own serial

	issueLeaf = func(leafCN string, dnsNames []string, ips []net.IP, opts ...func(*x509.Certificate)) (leafCertPEM, leafKeyPEM []byte) {
		t.Helper()

		leafKey, keyErr := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, keyErr)

		serial := big.NewInt(nextSerial)
		nextSerial++

		leafTemplate := &x509.Certificate{
			SerialNumber: serial,
			Subject:      pkix.Name{CommonName: leafCN},
			NotBefore:    time.Now().Add(-time.Minute),
			NotAfter:     time.Now().Add(time.Hour),
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			DNSNames:     dnsNames,
			IPAddresses:  ips,
		}
		for _, opt := range opts {
			opt(leafTemplate)
		}

		leafDER, certErr := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
		require.NoError(t, certErr)

		keyDER, marshalErr := x509.MarshalPKCS8PrivateKey(leafKey)
		require.NoError(t, marshalErr)

		leafCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
		leafKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
		return leafCertPEM, leafKeyPEM
	}

	return caCertPEM, issueLeaf
}

func TestBuildServerTLSConfigMaterial(t *testing.T) {
	_, issueServer := newTestCA(t, "test-ca")
	certPEM, keyPEM := issueServer("leaf-a")
	_, otherKeyPEM := issueServer("leaf-b")

	t.Run("value_sourced_round_trip", func(t *testing.T) {
		cfg := &config.ServerTLSConfig{
			CertValue: base64.StdEncoding.EncodeToString(certPEM),
			KeyValue:  base64.StdEncoding.EncodeToString(keyPEM),
		}
		tlsCfg, err := buildServerTLSConfig(cfg)
		require.NoError(t, err)
		require.Len(t, tlsCfg.Certificates, 1)
	})

	t.Run("file_sourced_round_trip", func(t *testing.T) {
		dir := t.TempDir()
		certPath := filepath.Join(dir, "server.crt")
		keyPath := filepath.Join(dir, "server.key")
		require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
		require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))

		cfg := &config.ServerTLSConfig{
			CertFile: certPath,
			KeyFile:  keyPath,
		}
		tlsCfg, err := buildServerTLSConfig(cfg)
		require.NoError(t, err)
		require.Len(t, tlsCfg.Certificates, 1)
	})

	t.Run("bad_base64_cert", func(t *testing.T) {
		cfg := &config.ServerTLSConfig{
			CertValue: "not-valid-base64!!!",
			KeyValue:  base64.StdEncoding.EncodeToString(keyPEM),
		}
		_, err := buildServerTLSConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cert")
		assert.NotContains(t, err.Error(), string(certPEM))
	})

	t.Run("bad_pem_key", func(t *testing.T) {
		cfg := &config.ServerTLSConfig{
			CertValue: base64.StdEncoding.EncodeToString(certPEM),
			KeyValue:  base64.StdEncoding.EncodeToString([]byte("not pem data")),
		}
		_, err := buildServerTLSConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cert/key")
	})

	t.Run("mismatched_pair", func(t *testing.T) {
		cfg := &config.ServerTLSConfig{
			CertValue: base64.StdEncoding.EncodeToString(certPEM),
			KeyValue:  base64.StdEncoding.EncodeToString(otherKeyPEM),
		}
		_, err := buildServerTLSConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cert/key")
		assert.NotContains(t, err.Error(), string(otherKeyPEM))
	})
}

func TestBuildServerTLSConfigMinVersion(t *testing.T) {
	_, issueServer := newTestCA(t, "test-ca")
	certPEM, keyPEM := issueServer("leaf-minversion")

	tests := []struct {
		name       string
		minVersion string
		want       uint16
		wantErr    bool
	}{
		{name: "empty_defaults_to_tls12", minVersion: "", want: tls.VersionTLS12},
		{name: "explicit_tls12", minVersion: "1.2", want: tls.VersionTLS12},
		{name: "explicit_tls13", minVersion: "1.3", want: tls.VersionTLS13},
		{name: "unsupported_tls11", minVersion: "1.1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ServerTLSConfig{
				CertValue:  base64.StdEncoding.EncodeToString(certPEM),
				KeyValue:   base64.StdEncoding.EncodeToString(keyPEM),
				MinVersion: tt.minVersion,
			}
			tlsCfg, err := buildServerTLSConfig(cfg)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "minversion")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, tlsCfg.MinVersion)
		})
	}
}

func TestServerStartsTLSAndServesHealth(t *testing.T) {
	caCertPEM, issueServer := newTestCA(t, "test-ca")
	certPEM, keyPEM := issueServer("127.0.0.1")

	cfg := newTestConfig("", "", "")
	// A TLS handshake needs more margin than newTestConfig's 50ms default —
	// the classic Windows-CI flake shape.
	cfg.Server.Timeout.Read = 2 * time.Second
	cfg.Server.Timeout.Write = 2 * time.Second
	cfg.Server.TLS = config.ServerTLSConfig{
		Enabled:   true,
		CertValue: base64.StdEncoding.EncodeToString(certPEM),
		KeyValue:  base64.StdEncoding.EncodeToString(keyPEM),
	}

	srv := New(cfg, &testLogger{})
	require.NotNil(t, srv)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	waitForServerReady(t, srv)
	addr := srv.BoundAddr()
	tcpAddr, ok := addr.(*net.TCPAddr)
	require.True(t, ok, "BoundAddr must be the TLS listener's TCP address")
	assert.NotZero(t, tcpAddr.Port)

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caCertPEM))

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fmt.Sprintf("https://%s/health", addr.String()), http.NoBody)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, resp.TLS)
	assert.GreaterOrEqual(t, resp.TLS.Version, uint16(tls.VersionTLS12))

	shutdownAndDrain(t, srv, errCh)
}

func TestServerTLSBadMaterialFailsStart(t *testing.T) {
	cfg := newTestConfig("", "", "")
	cfg.Server.Timeout.Read = 2 * time.Second
	cfg.Server.Timeout.Write = 2 * time.Second
	cfg.Server.TLS = config.ServerTLSConfig{
		Enabled:   true,
		CertValue: "not-valid-base64!!!",
		KeyValue:  "not-valid-base64!!!",
	}

	srv := New(cfg, &testLogger{})
	require.NotNil(t, srv)

	err := srv.Start()
	require.Error(t, err)
	assert.Nil(t, srv.BoundAddr())
}

// TestServerStaleMaterialWarnsWhenDisabled pins the fail-open-but-not-silent
// contract: staged TLS material with server.tls.enabled false must still
// serve plaintext (a legitimate staged rollout), but must emit exactly the
// WARN that lets a mistyped SERVER_TLS_ENABLED be caught in logs.
func TestServerStaleMaterialWarnsWhenDisabled(t *testing.T) {
	cfg := newTestConfig("", "", "")
	cfg.Server.Timeout.Read = 2 * time.Second
	cfg.Server.Timeout.Write = 2 * time.Second
	cfg.Server.TLS = config.ServerTLSConfig{
		Enabled:   false,
		CertValue: "aGVsbG8=", // staged material, ignored while disabled
	}

	log := &testLogger{}
	srv := New(cfg, log)
	require.NotNil(t, srv)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	waitForServerReady(t, srv)
	addr := srv.BoundAddr()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fmt.Sprintf("http://%s/health", addr.String()), http.NoBody)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	warned := false
	for _, entry := range log.logEntries() {
		if entry.level == "warn" && entry.values["field"] == "server.tls.enabled" {
			warned = true
			break
		}
	}
	assert.True(t, warned, "expected a WARN naming server.tls.enabled for staged-but-disabled material")

	shutdownAndDrain(t, srv, errCh)
}

func clientAuthLeaf(c *x509.Certificate) {
	c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
}

func TestParseClientAuth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want tls.ClientAuthType
	}{
		{name: "empty_is_off", in: "", want: tls.NoClientCert},
		{name: "verify_verifies_if_given", in: clientAuthVerify, want: tls.VerifyClientCertIfGiven},
		{name: "require_verify_requires_and_verifies", in: clientAuthRequireVerify, want: tls.RequireAndVerifyClientCert},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseClientAuth(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	for name, refused := range map[string]string{
		"refuses_request": "request", "refuses_require": "require",
		"refuses_capitalized_verify": "Verify", "refuses_underscore_spelling": "require_verify",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseClientAuth(refused)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `"verify"`)
			assert.Contains(t, err.Error(), `"require-verify"`)
		})
	}
}

func TestBuildServerTLSConfigClientAuth(t *testing.T) {
	_, issueServer := newTestCA(t, "server-ca")
	certPEM, keyPEM := issueServer("127.0.0.1")
	clientCAPEM, _ := newTestCA(t, "client-ca")

	base := func(mut func(*config.ServerTLSConfig)) *config.ServerTLSConfig {
		cfg := &config.ServerTLSConfig{
			Enabled:   true,
			CertValue: base64.StdEncoding.EncodeToString(certPEM),
			KeyValue:  base64.StdEncoding.EncodeToString(keyPEM),
		}
		mut(cfg)
		return cfg
	}

	wantPool := x509.NewCertPool()
	require.True(t, wantPool.AppendCertsFromPEM(clientCAPEM))

	t.Run("off_leaves_client_cas_unset", func(t *testing.T) {
		tlsCfg, err := buildServerTLSConfig(base(func(*config.ServerTLSConfig) {}))
		require.NoError(t, err)
		assert.Equal(t, tls.NoClientCert, tlsCfg.ClientAuth)
		assert.Nil(t, tlsCfg.ClientCAs)
	})

	t.Run("value_sourced_bundle", func(t *testing.T) {
		tlsCfg, err := buildServerTLSConfig(base(func(c *config.ServerTLSConfig) {
			c.ClientAuth = clientAuthRequireVerify
			c.ClientCAValue = base64.StdEncoding.EncodeToString(clientCAPEM)
		}))
		require.NoError(t, err)
		assert.Equal(t, tls.RequireAndVerifyClientCert, tlsCfg.ClientAuth)
		require.NotNil(t, tlsCfg.ClientCAs)
		assert.True(t, wantPool.Equal(tlsCfg.ClientCAs))
	})

	t.Run("file_sourced_bundle", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "client-ca.pem")
		require.NoError(t, os.WriteFile(path, clientCAPEM, 0o600))
		tlsCfg, err := buildServerTLSConfig(base(func(c *config.ServerTLSConfig) {
			c.ClientAuth = clientAuthVerify
			c.ClientCAFile = path
		}))
		require.NoError(t, err)
		assert.Equal(t, tls.VerifyClientCertIfGiven, tlsCfg.ClientAuth)
		require.NotNil(t, tlsCfg.ClientCAs)
		assert.True(t, wantPool.Equal(tlsCfg.ClientCAs))
	})

	t.Run("min_version_preserved_under_require_verify", func(t *testing.T) {
		tlsCfg, err := buildServerTLSConfig(base(func(c *config.ServerTLSConfig) {
			c.MinVersion = "1.3"
			c.ClientAuth = clientAuthRequireVerify
			c.ClientCAValue = base64.StdEncoding.EncodeToString(clientCAPEM)
		}))
		require.NoError(t, err)
		assert.Equal(t, uint16(tls.VersionTLS13), tlsCfg.MinVersion)
	})

	emptyPath := filepath.Join(t.TempDir(), "empty.pem")
	require.NoError(t, os.WriteFile(emptyPath, nil, 0o600))

	failures := []struct {
		name string
		mut  func(*config.ServerTLSConfig)
		want string
	}{
		{
			name: "missing_bundle",
			mut:  func(c *config.ServerTLSConfig) { c.ClientAuth = clientAuthVerify },
			want: "client ca: no material provided",
		},
		{
			name: "unreadable_bundle_file",
			mut: func(c *config.ServerTLSConfig) {
				c.ClientAuth = clientAuthVerify
				c.ClientCAFile = filepath.Join(t.TempDir(), "absent.pem")
			},
			want: "client ca: read file",
		},
		{
			name: "empty_bundle_file",
			mut: func(c *config.ServerTLSConfig) {
				c.ClientAuth = clientAuthRequireVerify
				c.ClientCAFile = emptyPath
			},
			want: "client ca: no CERTIFICATE block found",
		},
		{
			name: "unparseable_bundle",
			mut: func(c *config.ServerTLSConfig) {
				c.ClientAuth = clientAuthRequireVerify
				c.ClientCAValue = base64.StdEncoding.EncodeToString([]byte("-----BEGIN CERTIFICATE-----\nnot a cert\n-----END CERTIFICATE-----\n"))
			},
			want: "client ca:",
		},
		{
			name: "bad_base64_bundle",
			mut: func(c *config.ServerTLSConfig) {
				c.ClientAuth = clientAuthVerify
				c.ClientCAValue = "not-valid-base64!!!"
			},
			want: "client ca: base64 decode failed",
		},
		{
			name: "refused_policy",
			mut: func(c *config.ServerTLSConfig) {
				c.ClientAuth = "require"
				c.ClientCAValue = base64.StdEncoding.EncodeToString(clientCAPEM)
			},
			want: "clientauth",
		},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildServerTLSConfig(base(tt.mut))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestHasStagedServerTLSMaterial(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ServerTLSConfig
		want bool
	}{
		{name: "nothing_set", cfg: config.ServerTLSConfig{}, want: false},
		{name: "cert_file", cfg: config.ServerTLSConfig{CertFile: "/c.pem"}, want: true},
		{name: "cert_value", cfg: config.ServerTLSConfig{CertValue: "aGVsbG8="}, want: true},
		{name: "key_file", cfg: config.ServerTLSConfig{KeyFile: "/k.pem"}, want: true},
		{name: "key_value", cfg: config.ServerTLSConfig{KeyValue: "aGVsbG8="}, want: true},
		{name: "client_auth", cfg: config.ServerTLSConfig{ClientAuth: clientAuthRequireVerify}, want: true},
		{name: "client_ca_file", cfg: config.ServerTLSConfig{ClientCAFile: "/ca.pem"}, want: true},
		{name: "client_ca_value", cfg: config.ServerTLSConfig{ClientCAValue: "aGVsbG8="}, want: true},
		{name: "min_version_alone", cfg: config.ServerTLSConfig{MinVersion: "1.3"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasStagedServerTLSMaterial(&tt.cfg))
		})
	}
}

// mtlsFixture is a server leaf, the client CA the listener trusts, two clients it
// signed and one a second CA signed.
type mtlsFixture struct {
	serverRoots   *x509.CertPool
	certPEM       []byte
	keyPEM        []byte
	clientCAPEM   []byte
	allowedClient tls.Certificate
	otherClient   tls.Certificate
	rogueClient   tls.Certificate
}

const allowedClientCN = "allowed-client"

func newMTLSFixture(t *testing.T) mtlsFixture {
	t.Helper()
	serverCAPEM, issueServer := newTestCA(t, "server-ca")
	certPEM, keyPEM := issueServer("127.0.0.1")
	clientCAPEM, issueClient := newTestCAWithSANs(t, "client-ca")
	_, issueRogue := newTestCAWithSANs(t, "rogue-ca")

	pair := func(certPEM, keyPEM []byte) tls.Certificate {
		p, err := tls.X509KeyPair(certPEM, keyPEM)
		require.NoError(t, err)
		return p
	}
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(serverCAPEM))
	return mtlsFixture{
		serverRoots:   roots,
		certPEM:       certPEM,
		keyPEM:        keyPEM,
		clientCAPEM:   clientCAPEM,
		allowedClient: pair(issueClient(allowedClientCN, nil, nil, clientAuthLeaf)),
		otherClient:   pair(issueClient("other-client", nil, nil, clientAuthLeaf)),
		rogueClient:   pair(issueRogue(allowedClientCN, nil, nil, clientAuthLeaf)),
	}
}

func (f *mtlsFixture) serverTLS(clientAuth string) config.ServerTLSConfig {
	return config.ServerTLSConfig{
		Enabled:       true,
		CertValue:     base64.StdEncoding.EncodeToString(f.certPEM),
		KeyValue:      base64.StdEncoding.EncodeToString(f.keyPEM),
		ClientAuth:    clientAuth,
		ClientCAValue: base64.StdEncoding.EncodeToString(f.clientCAPEM),
	}
}

// client presents cert unconditionally when set. GetClientCertificate forces the
// send: Go's default selection honors the server's advertised CAs and would send no
// certificate at all for a client the listener does not trust.
func (f *mtlsFixture) client(cert *tls.Certificate) *http.Client {
	return &http.Client{Transport: f.transport(cert), Timeout: 5 * time.Second}
}

func (f *mtlsFixture) transport(cert *tls.Certificate) *http.Transport {
	tlsCfg := &tls.Config{RootCAs: f.serverRoots, MinVersion: tls.VersionTLS12}
	if cert != nil {
		tlsCfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return cert, nil
		}
	}
	return &http.Transport{TLSClientConfig: tlsCfg}
}

// startMTLSServer starts a listener with clientAuth and opts and a /tlsprobe route that
// answers 200 only when the request carries a verified client chain.
func startMTLSServer(t *testing.T, f *mtlsFixture, clientAuth string, opts Options) (srv *Server, baseURL string, errCh <-chan error) {
	t.Helper()
	cfg := newTestConfig("", "", "")
	cfg.Server.Timeout.Read = 2 * time.Second
	cfg.Server.Timeout.Write = 2 * time.Second
	cfg.Server.TLS = f.serverTLS(clientAuth)

	srv = NewWithOptions(cfg, &testLogger{}, opts)
	srv.echo.GET("/tlsprobe", func(c *echo.Context) error {
		tlsState := c.Request().TLS
		if tlsState == nil || len(tlsState.VerifiedChains) == 0 {
			return c.NoContent(http.StatusForbidden)
		}
		return c.NoContent(http.StatusOK)
	})
	errCh = startProbeServer(t, srv)
	return srv, "https://" + srv.BoundAddr().String(), errCh
}

// TestServerMutualTLS pins require-verify end to end: a client the CA signed is
// accepted with a verified chain, and a certless client or one a second CA signed is
// rejected at the handshake.
func TestServerMutualTLS(t *testing.T) {
	f := newMTLSFixture(t)
	srv, baseURL, errCh := startMTLSServer(t, &f, clientAuthRequireVerify, Options{})
	defer shutdownAndDrain(t, srv, errCh)

	t.Run("valid_client_cert_accepted", func(t *testing.T) {
		res, err := doRequest(t.Context(), f.client(&f.allowedClient), http.MethodGet, baseURL+"/tlsprobe")
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, res.code)
	})

	t.Run("no_client_cert_rejected", func(t *testing.T) {
		_, err := doRequest(t.Context(), f.client(nil), http.MethodGet, baseURL+"/tlsprobe")
		require.Error(t, err)
	})

	t.Run("wrong_ca_client_cert_rejected", func(t *testing.T) {
		_, err := doRequest(t.Context(), f.client(&f.rogueClient), http.MethodGet, baseURL+"/tlsprobe")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown certificate authority")
	})
}

// allowOnlyCN is a leaf-validation hook admitting only a leaf whose verified CN is cn.
func allowOnlyCN(cn string, calls *atomic.Int32) func([][]byte, [][]*x509.Certificate) error {
	return func(_ [][]byte, verifiedChains [][]*x509.Certificate) error {
		calls.Add(1)
		if len(verifiedChains) == 0 || verifiedChains[0][0].Subject.CommonName != cn {
			return errors.New("client not on the allowlist")
		}
		return nil
	}
}

// TestServerTLSLeafHook pins that the hook runs after chain verification and that its
// error rejects the handshake.
func TestServerTLSLeafHook(t *testing.T) {
	f := newMTLSFixture(t)
	var calls atomic.Int32
	srv, baseURL, errCh := startMTLSServer(t, &f, clientAuthRequireVerify,
		Options{TLSVerifyPeerCertificate: allowOnlyCN(allowedClientCN, &calls)})
	defer shutdownAndDrain(t, srv, errCh)

	t.Run("hook_accepts", func(t *testing.T) {
		res, err := doRequest(t.Context(), f.client(&f.allowedClient), http.MethodGet, baseURL+"/tlsprobe")
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, res.code)
	})

	t.Run("hook_rejects", func(t *testing.T) {
		_, err := doRequest(t.Context(), f.client(&f.otherClient), http.MethodGet, baseURL+"/tlsprobe")
		require.Error(t, err)
	})
}

// TestServerTLSLeafHookOnResumedSession pins the resumption guard: the stdlib skips
// VerifyPeerCertificate on a resumed session, so the hook must run again through
// VerifyConnection. Both the resumption precondition and the call count are asserted.
func TestServerTLSLeafHookOnResumedSession(t *testing.T) {
	f := newMTLSFixture(t)
	var calls atomic.Int32
	srv, baseURL, errCh := startMTLSServer(t, &f, clientAuthRequireVerify,
		Options{TLSVerifyPeerCertificate: allowOnlyCN(allowedClientCN, &calls)})
	defer shutdownAndDrain(t, srv, errCh)

	tr := f.transport(&f.allowedClient)
	tr.TLSClientConfig.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	defer tr.CloseIdleConnections()

	var resumed []bool
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{
		TLSHandshakeDone: func(state tls.ConnectionState, _ error) { resumed = append(resumed, state.DidResume) },
	})
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	for range 2 {
		res, err := doRequest(ctx, client, http.MethodGet, baseURL+"/tlsprobe")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, res.code)
		// Without this the second GET reuses the first connection: one handshake total.
		tr.CloseIdleConnections()
	}

	require.Equal(t, []bool{false, true}, resumed, "precondition: the second handshake resumed")
	assert.Equal(t, int32(2), calls.Load(), "the hook runs on the resumed handshake too")
}

// TestServerTLSHookWithoutVerifyingModeFailsStart pins the fail-closed half: a hook on an
// enabled listener whose policy never verifies would guard nothing, so Start refuses.
func TestServerTLSHookWithoutVerifyingModeFailsStart(t *testing.T) {
	f := newMTLSFixture(t)
	cfg := newTestConfig("", "", "")
	cfg.Server.TLS = f.serverTLS("")
	cfg.Server.TLS.ClientCAValue = ""
	srv := NewWithOptions(cfg, &testLogger{}, Options{
		TLSVerifyPeerCertificate: func([][]byte, [][]*x509.Certificate) error { return nil },
	})

	err := requireStartRefusedBeforeBind(t, srv, "Start bound and served with an inert leaf-validation hook")
	assert.Contains(t, err.Error(), "inert")
	assert.Contains(t, err.Error(), `"verify"`)
	assert.Contains(t, err.Error(), `"require-verify"`)
}

// TestServerTLSHookWithTLSDisabledWarnsAndStarts pins the WARN half: with TLS off a hook
// is staged ahead of a flip, so Start serves plaintext and says the hook is inert.
func TestServerTLSHookWithTLSDisabledWarnsAndStarts(t *testing.T) {
	cfg := newTestConfig("", "", "")
	log := &testLogger{}
	srv := NewWithOptions(cfg, log, Options{
		TLSVerifyPeerCertificate: func([][]byte, [][]*x509.Certificate) error { return nil },
	})
	errCh := startProbeServer(t, srv)
	defer shutdownAndDrain(t, srv, errCh)

	entry := findLogEntry(log.logEntries(), inertLeafHookWarnMsg)
	require.NotNil(t, entry, "expected a WARN for a hook on a plaintext listener")
	assert.Equal(t, "warn", entry.level)
	assert.Equal(t, "server.tls.enabled", entry.values["field"])
}

// TestGuardLeafHook pins the wrapper both hook seams share: a certless client never
// reaches the hook, and a hook panic becomes an error naming the panic's type only.
func TestGuardLeafHook(t *testing.T) {
	t.Run("certless_client_skips_the_hook", func(t *testing.T) {
		var calls atomic.Int32
		guarded := guardLeafHook(allowOnlyCN("nobody", &calls))
		require.NoError(t, guarded(nil, nil))
		assert.Zero(t, calls.Load())
	})

	t.Run("presented_cert_reaches_the_hook", func(t *testing.T) {
		var calls atomic.Int32
		guarded := guardLeafHook(allowOnlyCN("nobody", &calls))
		require.Error(t, guarded([][]byte{{0x30}}, nil))
		assert.Equal(t, int32(1), calls.Load())
	})

	t.Run("panic_reported_by_type_only", func(t *testing.T) {
		const secret = "s3cret-panic-value"
		guarded := guardLeafHook(func([][]byte, [][]*x509.Certificate) error { panic(secret) })
		err := guarded([][]byte{{0x30}}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "type: string")
		assert.NotContains(t, err.Error(), secret)
	})
}
