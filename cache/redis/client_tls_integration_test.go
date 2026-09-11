//go:build integration

package redis

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	gbtesting "github.com/gaborage/go-bricks/testing"
	"github.com/gaborage/go-bricks/testing/containers"
)

// tlsCertHosts are the SANs the fixture mints for the server certificate. The
// container's Host() is only known after start, so the certificate has to cover
// the addresses Docker can hand back in advance; setupTLSRedis asserts the one
// it actually got is among them rather than letting a handshake fail obscurely.
var tlsCertHosts = []string{"localhost", "127.0.0.1", "::1"}

// setupTLSRedis boots a TLS-only Redis container private to the calling test
// (the package's shared pkgRedis is plaintext) and returns its address plus the
// CA the client must trust, base64-encoded the way TLSConfig.CAValue wants it.
func setupTLSRedis(t *testing.T) (host string, port int, caValue string) {
	t.Helper()

	caPEM, certPEM, keyPEM := gbtesting.CAAndServerCertPEM(t, tlsCertHosts...)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := containers.StartRedisTLSContainer(ctx, t, nil, caPEM, certPEM, keyPEM)
	require.NoError(t, err, "failed to start TLS Redis container")
	container.WithCleanup(t)

	if !slices.Contains(tlsCertHosts, container.Host()) {
		t.Skipf("Docker host %q is not covered by the fixture SANs %v; TLS verification cannot be exercised here",
			container.Host(), tlsCertHosts)
	}

	return container.Host(), container.Port(), base64.StdEncoding.EncodeToString(caPEM)
}

// TestNewClientTLSWithCAConnects is the handshake tracer bullet: a real TLS
// dial against a TLS-only server, trusting only the fixture CA, followed by a
// round-trip to prove the connection carries traffic and not just a handshake.
func TestNewClientTLSWithCAConnects(t *testing.T) {
	host, port, caValue := setupTLSRedis(t)

	client, err := NewClient(&Config{
		Host:     host,
		Port:     port,
		PoolSize: 10,
		TLS:      TLSConfig{Enabled: true, CAValue: caValue},
	})
	require.NoError(t, err, "TLS client should connect to the TLS-only server")
	defer client.Close()

	ctx := t.Context()
	key, value := "test:tls:roundtrip", []byte("over-tls")
	require.NoError(t, client.Set(ctx, key, value, time.Minute), "Set should succeed over TLS")

	got, err := client.Get(ctx, key)
	require.NoError(t, err, "Get should succeed over TLS")
	assert.Equal(t, value, got)
}

// TestNewClientPlaintextAgainstTLSOnlyFails pins the negative half: the server
// speaks only TLS, so a plaintext client must fail rather than silently
// downgrade.
func TestNewClientPlaintextAgainstTLSOnlyFails(t *testing.T) {
	host, port, _ := setupTLSRedis(t)

	client, err := NewClient(&Config{Host: host, Port: port, PoolSize: 10})
	if client != nil {
		defer client.Close()
	}
	require.Error(t, err, "a plaintext client must not connect to a TLS-only server")

	var connErr *cache.ConnectionError
	assert.ErrorAs(t, err, &connErr, "the PING failure should surface as a cache.ConnectionError")
}

// TestNewClientTLSWithoutCAFailsVerification proves the client actually
// verifies the server chain: with TLS on but no CA it falls back to the system
// roots, which do not contain the fixture's throwaway CA.
func TestNewClientTLSWithoutCAFailsVerification(t *testing.T) {
	host, port, _ := setupTLSRedis(t)

	client, err := NewClient(&Config{
		Host:     host,
		Port:     port,
		PoolSize: 10,
		TLS:      TLSConfig{Enabled: true},
	})
	if client != nil {
		defer client.Close()
	}
	require.Error(t, err, "an untrusted server certificate must not be accepted")

	var unknownAuthority x509.UnknownAuthorityError
	assert.ErrorAs(t, err, &unknownAuthority, "the chain should fail with an unknown-authority error: %v", err)
}

// TestNewClientTLSWrongServerNameFailsVerification pins the hostname half of
// verification: the CA is trusted, but the SNI override names a host the
// certificate does not cover.
func TestNewClientTLSWrongServerNameFailsVerification(t *testing.T) {
	host, port, caValue := setupTLSRedis(t)

	client, err := NewClient(&Config{
		Host:     host,
		Port:     port,
		PoolSize: 10,
		TLS:      TLSConfig{Enabled: true, CAValue: caValue, ServerName: "wrong.example"},
	})
	if client != nil {
		defer client.Close()
	}
	require.Error(t, err, "a certificate that does not cover the server name must be rejected")

	var hostnameErr x509.HostnameError
	assert.ErrorAs(t, err, &hostnameErr, "expected a hostname mismatch, got: %v", err)
}
