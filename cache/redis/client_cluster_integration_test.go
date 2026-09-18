//go:build integration

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
)

// clusterKeys hash to four DIFFERENT slots, which
// requireClusterKeysSpanDistinctSlots holds them to.
//
// The spread is not what today's pass demonstrates. One node owns all 16384
// slots here, so a client that pinned a single connection reaches all four keys
// exactly as a client following the slot map does: the two behaviors are the
// same behavior on this fixture. The spread is what BECOMES load-bearing the
// moment the same assertions run against a sharded endpoint, where reaching all
// four means four routing decisions went right — and keeping it falsifiable now
// is what makes that run mean something later.
var clusterKeys = []string{"cluster:alpha", "cluster:epsilon", "cluster:gamma", "cluster:delta"}

// setupClusterRedis returns a cluster-mode client on the package-wide
// cluster-enabled container (see integration_main_test.go), with the keyspace
// emptied first — FlushDB is keyless, so go-redis routes it to one node, and
// with one node that is the whole keyspace. There is no logical-database split
// to isolate with either: the cluster
// protocol has only database 0, which is the whole reason Config.Validate
// refuses a non-zero database under this mode.
func setupClusterRedis(t *testing.T) (*Client, context.Context) {
	t.Helper()

	ctx := t.Context()
	container := pkgRedisCluster.Get(t)

	client, err := NewClient(&Config{
		Host:     container.Host(),
		Port:     container.Port(),
		Mode:     ModeCluster,
		PoolSize: 10,
	})
	require.NoError(t, err, "cluster-mode client must connect to the cluster endpoint")
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.client.FlushDB(ctx).Err(), "failed to flush the cluster keyspace")

	return client, ctx
}

// requireClusterKeysSpanDistinctSlots asks the SERVER which slot each of
// clusterKeys lands in and requires one distinct answer per key.
//
// It exists because nothing else here can fail when they collide: this fixture
// is one node owning all 16384 slots, so keys sharing a slot round-trip exactly
// as happily as keys spanning slots and the suite passes either way. The spread
// is the entire reason the key set exists, which makes it worth four extra
// round-trips on an already-open pool to keep it falsifiable.
func requireClusterKeysSpanDistinctSlots(ctx context.Context, t *testing.T, client *Client) {
	t.Helper()

	slots := make(map[int64]struct{}, len(clusterKeys))
	for _, key := range clusterKeys {
		slot, err := client.client.ClusterKeySlot(ctx, key).Result()
		require.NoError(t, err, "the server owns the key-to-slot mapping, so it is the one asked")
		slots[slot] = struct{}{}
	}

	require.Len(t, slots, len(clusterKeys),
		"these keys must land in distinct slots: the spread is what will make the round trip observable against a sharded endpoint, and nothing on this one-node fixture would notice it collapsing")
}

// assertClusterRoundTrips drives Set, Get and Delete across keys in different
// slots, plus the miss a Get after Delete must report. Shared by the plaintext
// and TLS cluster tests: the operations
// and their expected answers are identical, and what differs is the transport
// the caller already built its client on.
func assertClusterRoundTrips(ctx context.Context, t *testing.T, client *Client) {
	t.Helper()

	requireClusterKeysSpanDistinctSlots(ctx, t, client)

	for _, key := range clusterKeys {
		value := []byte("value-for-" + key)

		require.NoError(t, client.Set(ctx, key, value, time.Minute))

		got, err := client.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, value, got)

		require.NoError(t, client.Delete(ctx, key))
		_, err = client.Get(ctx, key)
		assert.ErrorIs(t, err, cache.ErrNotFound)
	}
}

// assertClusterGetOrSet drives the SET NX GET path: the first call stores and
// reports it, the second finds the value already there and hands back what is
// stored rather than what was offered. The key is the caller's, so the two
// fixtures never write the same one.
func assertClusterGetOrSet(ctx context.Context, t *testing.T, client *Client, key string) {
	t.Helper()

	first := []byte("first")

	stored, wasSet, err := client.GetOrSet(ctx, key, first, time.Minute)
	require.NoError(t, err)
	assert.True(t, wasSet)
	assert.Equal(t, first, stored)

	stored, wasSet, err = client.GetOrSet(ctx, key, []byte("second"), time.Minute)
	require.NoError(t, err)
	assert.False(t, wasSet)
	assert.Equal(t, first, stored)
}

// TestRealRedisClusterModeRoundTrips drives Set, Get and Delete across keys in
// different slots, plus the miss that follows the Delete — four of the seven
// things cache.Cache asks for. GetOrSet, the two compare-and-swap operations and
// Health have their own tests below. Together they are the evidence that the
// mode switch is a transport change only: the stored values and the miss
// semantics are what they are on a single node.
//
// CompareAndSet and CompareAndDelete are not exercised over TLS. That is
// deliberate rather than an omission: the issue's acceptance criteria name
// Set/Get/Delete plus GetOrSet on the encrypted path, and the two Lua scripts
// are pinned here, on the plaintext cluster fixture.
func TestRealRedisClusterModeRoundTrips(t *testing.T) {
	client, ctx := setupClusterRedis(t)

	assertClusterRoundTrips(ctx, t, client)
}

// TestRealRedisClusterModeGetOrSet pins the SET NX GET path, which needs Redis
// 7.0+ and is the operation the version floor exists for.
func TestRealRedisClusterModeGetOrSet(t *testing.T) {
	client, ctx := setupClusterRedis(t)

	assertClusterGetOrSet(ctx, t, client, "cluster:getorset")
}

// TestRealRedisClusterModeCompareAndSwap drives the two Lua scripts through the
// cluster client: that an EVAL reaches a cluster node at all (it routes by its
// single KEYS entry), and that the scripts' integer replies survive that client
// as the same booleans a standalone client reports.
//
// It is NOT where a slot-routing mistake would surface. CROSSSLOT needs two keys
// in different slots and every call here passes exactly one, and MOVED needs a
// second shard, which a one-node fixture has not got.
func TestRealRedisClusterModeCompareAndSwap(t *testing.T) {
	client, ctx := setupClusterRedis(t)

	const key = "cluster:cas"
	acquired, err := client.CompareAndSet(ctx, key, nil, []byte("held"), time.Minute)
	require.NoError(t, err)
	assert.True(t, acquired)

	stolen, err := client.CompareAndSet(ctx, key, []byte("wrong"), []byte("other"), time.Minute)
	require.NoError(t, err)
	assert.False(t, stolen)

	swapped, err := client.CompareAndSet(ctx, key, []byte("held"), []byte("renewed"), time.Minute)
	require.NoError(t, err)
	assert.True(t, swapped)

	released, err := client.CompareAndDelete(ctx, key, []byte("wrong"))
	require.NoError(t, err)
	assert.False(t, released)

	released, err = client.CompareAndDelete(ctx, key, []byte("renewed"))
	require.NoError(t, err)
	assert.True(t, released)

	_, err = client.Get(ctx, key)
	assert.ErrorIs(t, err, cache.ErrNotFound)
}

// TestRealRedisClusterModeStatsAndHealth pins what an operator reading Stats()
// sees — the probe set never calls it, rendering an allowlisted manager map
// instead. The mode key is what tells that reader how to read the rest, and
// redis_info is what it names: whichever single node answered INFO. The pool_*
// counters are not read here, and how they aggregate is documented where it
// holds for the production client rather than for this fixture — cache/redis
// client.go and wiki/cache.md.
func TestRealRedisClusterModeStatsAndHealth(t *testing.T) {
	client, ctx := setupClusterRedis(t)

	require.NoError(t, client.Health(ctx))

	stats, err := client.Stats()
	require.NoError(t, err)
	assert.Equal(t, "cluster", stats["mode"])
	assert.Contains(t, stats["redis_info"], "redis_version:")
}

// TestRealRedisClusterModeServesTheVersionFloor answers the question NewClient
// silently depends on: INFO server is a keyless command, so the cluster client
// routes it to one node and the 7.0 floor keeps working against a cluster
// endpoint rather than failing open.
func TestRealRedisClusterModeServesTheVersionFloor(t *testing.T) {
	client, ctx := setupClusterRedis(t)

	info, err := readServerInfo(ctx, client.client)

	require.NoError(t, err, "INFO server must be answerable through the cluster client")
	tooOld, version := redisVersionTooOld(info)
	assert.False(t, tooOld)
	assert.NotEmpty(t, version, "the floor check must find a version, not fail open")
}

// TestRealRedisStandaloneModeAgainstAClusterEndpoint pins what the wrong mode
// actually does against THIS fixture, which is not what it does against Amazon
// ElastiCache Serverless. One node owns all 16384 slots here, so it serves every
// key itself and never answers MOVED — a single-node client round-trips fine.
// The redirect only appears once slots live on more than one shard, which a
// one-container fixture cannot stage. What the fixture can show is the other
// half of the mismatch: the cluster protocol has no SELECT, so a standalone
// client that also selects a database fails at the dial.
func TestRealRedisStandaloneModeAgainstAClusterEndpoint(t *testing.T) {
	container := pkgRedisCluster.Get(t)

	t.Run("single_key_traffic_is_tolerated", func(t *testing.T) {
		client, err := NewClient(&Config{
			Host:     container.Host(),
			Port:     container.Port(),
			Mode:     ModeStandalone,
			PoolSize: 10,
		})
		require.NoError(t, err)
		defer client.Close()

		ctx := t.Context()
		const key = "cluster:standalone-tolerated"
		require.NoError(t, client.Set(ctx, key, []byte("served"), time.Minute))

		got, err := client.Get(ctx, key)
		require.NoError(t, err, "a single node owning every slot answers a single-node client directly")
		assert.Equal(t, []byte("served"), got)
		require.NoError(t, client.Delete(ctx, key))
	})

	t.Run("selecting_a_database_fails_at_the_dial", func(t *testing.T) {
		client, err := NewClient(&Config{
			Host:     container.Host(),
			Port:     container.Port(),
			Mode:     ModeStandalone,
			Database: 3,
			PoolSize: 10,
		})

		require.Error(t, err, "the cluster protocol has no SELECT, so the connection cannot be set up")
		assert.Nil(t, client)
		assert.ErrorContains(t, err, "SELECT is not allowed in cluster mode",
			"the server states the reason; a generic dial failure would hide it")
	})
}
