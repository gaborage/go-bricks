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

// clusterTLSACLKeyPrefix marks this file's own keys. clusterKeys, which the
// shared assertions drive, belongs to client_cluster_integration_test.go.
const clusterTLSACLKeyPrefix = "clustertlsacl:"

// setupClusterTLSACLRedis returns a cluster-mode client reaching the container
// that carries all three legs at once, authenticated as the narrow app user over
// a verified TLS handshake.
//
// This is the intersection no other fixture reaches. The cluster-TLS fixture
// proves the pinned ServerName travels to a discovered node; the ACL cluster
// fixture proves the credential reaches the cluster copy path at construction.
// Neither shows the two arriving together on the dial that matters, because
// neither has both to carry.
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

	// Held as a variable rather than inlined into NewClient: cfg.Address() is
	// what go-redis is seeded with, and requireSlotMapAvoidsSeedAddress has to
	// compare against that exact string rather than one re-derived here.
	cfg := &Config{
		Host:     container.Host(),
		Port:     container.Port(),
		Mode:     ModeCluster,
		PoolSize: 10,
		Username: aclAppUsername,
		Password: aclAppPassword,
		TLS: TLSConfig{
			Enabled:    true,
			CAValue:    base64.StdEncoding.EncodeToString(container.caPEM),
			ServerName: clusterTLSServerName,
		},
	}

	client, err := NewClient(cfg)
	require.NoError(t, err,
		"the app user must complete a TLS handshake, AUTH, and read the slot map before the first PING")
	t.Cleanup(func() { _ = client.Close() })

	requireSlotMapAvoidsPinnedName(ctx, t, client)
	requireSlotMapAvoidsSeedAddress(ctx, t, client, cfg.Address())

	return client, ctx
}

// requireSlotMapAvoidsSeedAddress holds the premise this file's re-dial claim
// rests on, and it is a DIFFERENT premise from the one
// requireSlotMapAvoidsPinnedName holds.
//
// That guard compares the advertised host against the pinned SNI NAME, which is
// what makes the handshake evidence work. This one compares the advertised
// address against the SEED ADDRESS the client was handed — and only that
// comparison decides whether a second node client exists at all. go-redis keys
// its node clients by address string: it dials the seed, reads CLUSTER SLOTS,
// and builds a client per advertised address. When the fixture announces the
// address the client already seeded with, go-redis reuses the seed's connection
// and no second dial happens, while every assertion here still passes.
//
// The two are easy to conflate because both are true today for the same
// underlying reason — the fixture seeds with whatever Docker reports as the host
// and announces the IPv4 literal resolveAnnounceIP returns. But "the advertised
// host is not the pinned name" stays true in exactly the case where the seed and
// the advertised address have collapsed onto one string, so the existing guard
// cannot notice it. This is a hard require rather than a skip: on a
// configuration where they collide the arm is not testing what it claims, and a
// loud failure naming the collision is diagnosable where a silent pass is not.
func requireSlotMapAvoidsSeedAddress(ctx context.Context, t *testing.T, client *Client, seedAddr string) {
	t.Helper()

	slots, err := client.client.ClusterSlots(ctx).Result()
	require.NoError(t, err, "the slot map is what the client's per-node dials follow")
	require.NotEmpty(t, slots, "a bootstrapped cluster serves at least one slot range")

	for _, slot := range slots {
		for _, node := range slot.Nodes {
			require.NotEqual(t, seedAddr, node.Addr,
				"the slot map must advertise an address the client did not already seed with, or go-redis reuses the seed connection and nothing here dials a discovered node")
		}
	}
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
// have completed construction. The setup's two guards keep both halves of that
// falsifiable.
func TestRealRedisClusterTLSACLRoundTrips(t *testing.T) {
	client, ctx := setupClusterTLSACLRedis(t)

	assertClusterRoundTrips(ctx, t, client)
}

// TestRealRedisClusterTLSACLGetOrSet pins SET NX GET across the triple. It is
// the one operation whose answer comes from the server's own atomicity rather
// than from the client, so it is worth seeing answered through an authenticated
// TLS connection to a discovered node.
func TestRealRedisClusterTLSACLGetOrSet(t *testing.T) {
	client, ctx := setupClusterTLSACLRedis(t)

	assertGetOrSetFromEmptyKey(ctx, t, client, clusterTLSACLKeyPrefix+"getorset")
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
	// Leased here rather than in the table below: the table is evaluated by the
	// parent, so a lease there boots the container even for a -run that selects
	// no arm, and a Docker-unavailable skip would land on the parent.
	container := pkgRedisClusterTLSACL.Get(t)

	tests := []struct {
		name     string
		username string
		password string
		wantErr  string
	}{
		{
			name:     "wrong_password_is_rejected",
			username: aclAppUsername,
			password: aclAppPassword + "-tampered",
			wantErr:  "WRONGPASS",
		},
		{
			name:     "unknown_username_is_rejected",
			username: aclAppUsername + "-does-not-exist",
			password: aclAppPassword,
			wantErr:  "WRONGPASS",
		},
		{
			name:    "no_credentials_is_rejected",
			wantErr: "NOAUTH",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(&Config{
				Host:     container.Host(),
				Port:     container.Port(),
				Mode:     ModeCluster,
				PoolSize: 10,
				Username: tt.username,
				Password: tt.password,
				TLS: TLSConfig{
					Enabled:    true,
					CAValue:    base64.StdEncoding.EncodeToString(container.caPEM),
					ServerName: clusterTLSServerName,
				},
			})

			require.Error(t, err, "the server accepts nothing unauthenticated, TLS notwithstanding")
			assert.Nil(t, client, "NewClient returns no client alongside an error")

			var connErr *cache.ConnectionError
			require.ErrorAs(t, err, &connErr, "a refused credential fails at the dial, not at config validation")
			assert.ErrorContains(t, err, tt.wantErr,
				"the ACL must be what refused this, not the handshake and not a timeout")
		})
	}
}
