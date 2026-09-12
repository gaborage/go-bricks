//go:build integration

package redis

import (
	"crypto/x509"
	"encoding/base64"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	gbtesting "github.com/gaborage/go-bricks/testing"
)

// setupTLSRedis leases the package's shared TLS-only Redis container (the
// plaintext pkgRedis cannot exercise a handshake) and returns its address plus
// the CA the client must trust, base64-encoded the way TLSConfig.CAValue wants
// it.
//
// The container's Host() is only known after start, so the certificate covers
// the addresses Docker can hand back in advance (gbtesting.DefaultCertHosts);
// a host outside that set is skipped rather than left to fail as an obscure
// handshake error.
func setupTLSRedis(t *testing.T) (host string, port int, caValue string) {
	t.Helper()

	c := pkgRedisTLS.Get(t)

	hosts := gbtesting.DefaultCertHosts()
	if !slices.Contains(hosts, c.Host()) {
		// Locally this is an environment quirk worth skipping over; in CI it
		// would silently delete the TLS proof, so make it fatal there.
		if os.Getenv("CI") != "" {
			t.Fatalf("container host %q is outside the fixture SANs %v; extend gbtesting.DefaultCertHosts",
				c.Host(), hosts)
		}
		t.Skipf("Docker host %q is not covered by the fixture SANs %v; TLS verification cannot be exercised here",
			c.Host(), hosts)
	}

	return c.Host(), c.Port(), base64.StdEncoding.EncodeToString(c.caPEM)
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

// TestNewClientTLSVerificationFailures pins the negative half of the handshake:
// every way a client can fail to reach the TLS-only server, each failing for a
// distinct x509-level reason rather than a generic dial error.
func TestNewClientTLSVerificationFailures(t *testing.T) {
	host, port, caValue := setupTLSRedis(t)

	tests := []struct {
		name      string
		tls       TLSConfig
		assertErr func(t *testing.T, err error)
	}{
		{
			// The server speaks only TLS, so a plaintext client must fail
			// rather than silently downgrade.
			name: "plaintext_against_tls_only",
			tls:  TLSConfig{},
			assertErr: func(t *testing.T, err error) {
				var connErr *cache.ConnectionError
				assert.ErrorAs(t, err, &connErr, "the PING failure should surface as a cache.ConnectionError")
			},
		},
		{
			// TLS on but no CA falls back to the system roots, which do not
			// contain the fixture's throwaway CA.
			name: "tls_without_ca",
			tls:  TLSConfig{Enabled: true},
			assertErr: func(t *testing.T, err error) {
				var unknownAuthority x509.UnknownAuthorityError
				assert.ErrorAs(t, err, &unknownAuthority, "the chain should fail with an unknown-authority error: %v", err)
			},
		},
		{
			// The CA is trusted, but the SNI override names a host the
			// certificate does not cover.
			name: "tls_wrong_server_name",
			tls:  TLSConfig{Enabled: true, CAValue: caValue, ServerName: "wrong.example"},
			assertErr: func(t *testing.T, err error) {
				var hostnameErr x509.HostnameError
				assert.ErrorAs(t, err, &hostnameErr, "expected a hostname mismatch, got: %v", err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(&Config{
				Host:     host,
				Port:     port,
				PoolSize: 10,
				TLS:      tt.tls,
			})
			require.Error(t, err, "the client must not connect")
			assert.Nil(t, client, "NewClient returns no client alongside an error")
			tt.assertErr(t, err)
		})
	}
}
