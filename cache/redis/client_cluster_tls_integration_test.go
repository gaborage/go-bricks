//go:build integration

package redis

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// clusterTLSServerName is the only host the cluster-TLS fixture's certificate
// covers, and the name the client under test pins as its SNI.
const clusterTLSServerName = "localhost"

// setupClusterTLSRedis returns a cluster-mode client reaching the package-wide
// cluster-enabled, TLS-only container over a verified handshake, with the
// keyspace emptied first. As in setupClusterRedis there is no logical-database
// split to isolate with: the cluster protocol has only database 0.
//
// The pinned ServerName is the whole point. go-redis builds every node it
// discovers in the slot map from the ONE tls.Config it was handed, and this
// fixture's slot map advertises the announced IPv4 (127.0.0.1), which the
// fixture's certificate deliberately does not cover. So a driver that re-derived
// each node's expected name from the address it dialed would fail the handshake
// here rather than round-trip, and a certificate covering both names would be
// accepted either way — proving nothing about which name was verified.
//
// Unlike setupTLSRedis there is no DefaultCertHosts membership guard: the name
// verified is the pinned one, so whatever Docker hands back as Host() no longer
// has to be a name the certificate covers. What does have to hold is the SAN
// set, which requireLeafCoversOnlyPinnedName asserts before anything dials.
func setupClusterTLSRedis(t *testing.T) (*Client, context.Context) {
	t.Helper()

	ctx := t.Context()
	container := pkgRedisClusterTLS.Get(t)
	requireLeafCoversOnlyPinnedName(t, container.certPEM)

	client, err := NewClient(&Config{
		Host:     container.Host(),
		Port:     container.Port(),
		Mode:     ModeCluster,
		PoolSize: 10,
		TLS: TLSConfig{
			Enabled:    true,
			CAValue:    base64.StdEncoding.EncodeToString(container.caPEM),
			ServerName: clusterTLSServerName,
		},
	})
	require.NoError(t, err, "cluster-mode client must connect to the TLS cluster endpoint")
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.client.FlushDB(ctx).Err(), "failed to flush the cluster keyspace")
	requireDialedNodesAvoidPinnedName(ctx, t, client)
	requireDialedNodesAvoidSeedAddress(ctx, t, client)

	return client, ctx
}

// requireLeafCoversOnlyPinnedName holds the premise this whole file rests on.
//
// The round trip proves that go-redis reuses the seed's pinned ServerName for
// every node it discovers ONLY while the advertised address sits OUTSIDE the
// certificate: a leaf that also covered the announced 127.0.0.1 would be
// accepted whichever name the driver verified, and every test here would go on
// passing while proving nothing (see setupClusterTLSRedis). So the SAN set is
// the premise rather than a detail of the fixture, and it is asserted exactly —
// adding one more host to the mint call has to fail here, not pass quietly.
//
// It pairs with requireDialedNodesAvoidPinnedName over the two halves of that
// premise: this one that the certificate excludes the advertised address, that
// one that every node address the client dials is one the certificate excludes.
func requireLeafCoversOnlyPinnedName(t *testing.T, certPEM []byte) {
	t.Helper()

	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block, "the fixture's leaf must decode as PEM")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err, "the fixture's leaf must parse as a certificate")

	require.Equal(t, []string{clusterTLSServerName}, leaf.DNSNames,
		"the leaf must cover the pinned name and no other DNS name")
	require.Empty(t, leaf.IPAddresses,
		"an IP SAN would cover the advertised address, and the handshake would then pass whichever name was verified")
}

// requireDialedNodesAvoidPinnedName keeps the handshake assertion honest: it
// judges the node addresses the client dials, and if one of them ever carried
// the pinned name itself, both derivations of a node's expected name would agree
// and every test in this file would pass without proving anything (see
// setupClusterTLSRedis).
func requireDialedNodesAvoidPinnedName(ctx context.Context, t *testing.T, client *Client) {
	t.Helper()

	for _, addr := range clusterRoutedNodeAddrs(ctx, t, client) {
		host, _, splitErr := net.SplitHostPort(addr)
		require.NoError(t, splitErr, "a node address the client dials must split into host and port")
		require.NotEqual(t, clusterTLSServerName, host,
			"pinned-name guard: the client must dial a node host the pinned SNI name does not cover")
	}
}

// requireDialedNodesAvoidSeedAddress holds the premise every discovered-node
// re-dial claim rests on, and it is a DIFFERENT premise from the one
// requireDialedNodesAvoidPinnedName holds.
//
// That guard compares each dialed node's host against the pinned SNI NAME, which
// is what makes the handshake evidence work. This one compares each node address
// the client dials against the SEED ADDRESS the client was handed — and only that
// comparison decides whether a second node client exists at all. go-redis keys
// its node clients by address string: it dials the seed, reads the slot map,
// and builds a client per advertised address. When the fixture announces the
// address the client already seeded with, go-redis reuses the seed's connection
// and no second dial happens, while every assertion around it still passes.
//
// The seed is read back off the client rather than re-derived: buildRedisOptions
// seeds Addrs with cfg.Address() and NewClient keeps that same *Config as
// client.config, so client.config.Address() is the literal string go-redis was
// handed.
//
// The two guards are easy to conflate because both are true today for the same
// underlying reason — the fixture seeds with whatever Docker reports as the host
// and announces the IPv4 literal resolveAnnounceIP returns. But "a dialed host
// is not the pinned name" stays true in exactly the case where the seed and a
// dialed node address have collapsed onto one string, so the pinned-name
// guard cannot notice it. This is a hard require rather than a skip: on a
// configuration where they collide the test is not proving what it claims, and a
// loud failure naming the collision is diagnosable where a silent pass is not.
func requireDialedNodesAvoidSeedAddress(ctx context.Context, t *testing.T, client *Client) {
	t.Helper()

	seedAddr := client.config.Address()
	for _, addr := range clusterRoutedNodeAddrs(ctx, t, client) {
		require.NotEqual(t, seedAddr, addr,
			"seed-address guard: the client must dial a node address it did not already seed with, or go-redis reuses the seed connection and nothing dials a discovered node")
	}
}

// clusterRoutedNodeAddrs returns the address of every node the client routes
// to, so the two guards above judge the addresses the client dials.
//
// ForEachShard reloads the slot map but serves the cached one when that reload
// fails, and ReloadState only schedules a background reload that reports no
// error. A client built from the same options holds no cached map, so its first
// load either succeeds or fails the test here; requiring client's node set to
// match it keeps a stale list from being judged.
func clusterRoutedNodeAddrs(ctx context.Context, t *testing.T, client *Client) []string {
	t.Helper()

	cluster, ok := client.client.(*redis.ClusterClient)
	require.True(t, ok, "a ModeCluster client wraps a *redis.ClusterClient, got %T", client.client)

	opt := *cluster.Options()
	fresh := redis.NewClusterClient(&opt)
	defer func() { _ = fresh.Close() }()

	want := clusterShardAddrs(ctx, t, fresh)
	require.NotEmpty(t, want, "a bootstrapped cluster serves at least one node")
	got := clusterShardAddrs(ctx, t, cluster)
	require.ElementsMatch(t, want, got, "the client under test must dial the nodes a fresh slot-map load names")
	return got
}

// clusterShardAddrs returns the address of every node c routes to: the online
// masters and replicas ForEachShard visits, which need not include the seed.
func clusterShardAddrs(ctx context.Context, t *testing.T, c *redis.ClusterClient) []string {
	t.Helper()

	var mu sync.Mutex
	var addrs []string
	require.NoError(t, c.ForEachShard(ctx, func(_ context.Context, node *redis.Client) error {
		mu.Lock()
		defer mu.Unlock()
		addrs = append(addrs, node.Options().Addr)
		return nil
	}), "the slot map is what the client's per-node dials follow")
	return addrs
}

// TestRealRedisClusterTLSRoundTrips drives keys in four different slots over a
// verified TLS handshake. Reaching a key means the client followed the slot map
// to the node that serves it and completed a handshake against that node — the
// pair a client of an ElastiCache Serverless endpoint has to speak (cluster
// protocol plus encryption in transit), and the pair no other test covers.
func TestRealRedisClusterTLSRoundTrips(t *testing.T) {
	client, ctx := setupClusterTLSRedis(t)

	assertClusterRoundTrips(ctx, t, client)
}

// TestRealRedisClusterTLSGetOrSet pins the SET NX GET path over the same
// transport: it is the one operation whose result depends on the server's own
// atomicity rather than on the client, so it is worth seeing answered through a
// TLS connection to a discovered node.
func TestRealRedisClusterTLSGetOrSet(t *testing.T) {
	client, ctx := setupClusterTLSRedis(t)

	assertClusterGetOrSet(ctx, t, client, "cluster:tls:getorset")
}
