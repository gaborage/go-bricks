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
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

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
