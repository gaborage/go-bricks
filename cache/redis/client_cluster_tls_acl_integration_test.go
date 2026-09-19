//go:build integration

package redis

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
)

// clusterTLSACLConfig builds a config for the triple-legged fixture: the
// container's mapped address, the CA it presents and the pinned SNI name, with
// only the credential varying between call sites.
func clusterTLSACLConfig(c *tlsRedisContainer, username, password string) *Config {
	return &Config{
		Host:     c.Host(),
		Port:     c.Port(),
		Mode:     ModeCluster,
		PoolSize: 10,
		Username: username,
		Password: password,
		TLS: TLSConfig{
			Enabled:    true,
			CAValue:    base64.StdEncoding.EncodeToString(c.caPEM),
			ServerName: clusterTLSServerName,
		},
	}
}

// setupClusterTLSACLRedis returns a cluster-mode client reaching the container
// that carries all three legs at once, authenticated as the narrow app user over
// a verified TLS handshake.
//
// There is no FlushDB on entry, unlike setupClusterRedis and
// setupClusterTLSRedis: the narrow app user holds no FLUSHDB, and granting it
// one would widen the credential this file exists to exercise. The shared
// round-trip assertions delete what they write, and the GetOrSet key goes
// through assertGetOrSetFromEmptyKey for the same reason the ACL arms do.
func setupClusterTLSACLRedis(t *testing.T) (*Client, context.Context) {
	t.Helper()

	ctx := t.Context()
	container := pkgRedisClusterTLSACL.Get(t)
	requireLeafCoversOnlyPinnedName(t, container.certPEM)

	client, err := NewClient(clusterTLSACLConfig(container, aclAppUsername, aclAppPassword))
	require.NoError(t, err,
		"the app user must complete a TLS handshake, AUTH, and read the slot map before the first PING")
	t.Cleanup(func() { _ = client.Close() })

	requireSlotMapAvoidsPinnedName(ctx, t, client)
	requireSlotMapAvoidsSeedAddress(ctx, t, client)

	return client, ctx
}

// TestRealRedisClusterTLSACLRoundTrips is what this file exists for: the cache
// operations driven across keys in four slots, over a TLS handshake, as an ACL
// identity, against a node reached from the slot map rather than from the seed.
//
// The three legs are each proven elsewhere. What is proven only here is that the
// dial go-redis makes to a discovered node carries BOTH the transport config and
// the credential — the transport because the leaf covers only the pinned name
// and the advertised address is outside it, the credential because a cluster
// client that failed to copy it through UniversalOptions.Cluster() could not
// have completed construction. The setup's guards keep both halves of that
// falsifiable.
func TestRealRedisClusterTLSACLRoundTrips(t *testing.T) {
	client, ctx := setupClusterTLSACLRedis(t)

	assertClusterRoundTrips(ctx, t, client)
}

// TestRealRedisClusterTLSACLGetOrSet pins SET NX GET across the triple — the
// same server-atomicity argument as TestRealRedisClusterTLSGetOrSet, now over an
// authenticated connection.
func TestRealRedisClusterTLSACLGetOrSet(t *testing.T) {
	client, ctx := setupClusterTLSACLRedis(t)

	assertGetOrSetFromEmptyKey(ctx, t, client, "clustertlsacl:getorset")
}

// TestRealRedisClusterTLSACLRejectsBadCredentials is the negative that licenses
// every positive assertion in this file: without it they would all hold equally
// against a server that never required a credential.
//
// The wantErr strings carry the weight. A bad password on a TLS-only cluster
// endpoint has three plausible failure modes — a refused credential, a failed
// handshake, and a readiness timeout — and only the first means the ACL is what
// rejected it. Asserting the server's own WRONGPASS/NOAUTH reply rather than
// merely requiring an error is what separates them, since a TLS failure would
// satisfy a bare require.Error just as well.
func TestRealRedisClusterTLSACLRejectsBadCredentials(t *testing.T) {
	for _, tt := range aclBadCredentials {
		t.Run(tt.name, func(t *testing.T) {
			// Leased inside the arm rather than in the parent body: the skip a
			// missing Docker produces then lands on the arm that asked for the
			// container instead of on the parent. It buys no boot saving — Go runs
			// the parent body to discover subtests either way — and costs none,
			// because Shared.Get is sync.Once-guarded: one boot, two no-ops.
			container := pkgRedisClusterTLSACL.Get(t)

			client, err := NewClient(clusterTLSACLConfig(container, tt.username, tt.password))

			require.Error(t, err, "the server accepts nothing unauthenticated, TLS notwithstanding")
			assert.Nil(t, client, "NewClient returns no client alongside an error")

			var connErr *cache.ConnectionError
			require.ErrorAs(t, err, &connErr, "a refused credential fails at the dial, not at config validation")
			assert.ErrorContains(t, err, tt.wantErr,
				"the ACL must be what refused this, not the handshake and not a timeout")
		})
	}
}
