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

// clusterKeys hash to different slots, so a client that reaches all of them has
// followed the slot map rather than pinned one connection.
var clusterKeys = []string{"cluster:alpha", "cluster:beta", "cluster:gamma", "cluster:delta"}

// setupClusterRedis returns a cluster-mode client on the package-wide
// cluster-enabled container (see integration_main_test.go), with the keyspace
// emptied first. There is no logical-database split to isolate with: the cluster
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

// TestRealRedisClusterModeRoundTrips drives every cache.Cache operation across
// keys in different slots. It is the proof that the mode switch is a transport
// change only: the Lua scripts, the SET NX GET path and the miss semantics all
// behave as they do on a single node.
func TestRealRedisClusterModeRoundTrips(t *testing.T) {
	client, ctx := setupClusterRedis(t)

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

// TestRealRedisClusterModeGetOrSet pins the SET NX GET path, which needs Redis
// 7.0+ and is the operation the version floor exists for.
func TestRealRedisClusterModeGetOrSet(t *testing.T) {
	client, ctx := setupClusterRedis(t)

	const key = "cluster:getorset"
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

// TestRealRedisClusterModeCompareAndSwap drives the two Lua scripts. A script
// routes by its single KEYS entry, so this is where a slot-routing mistake would
// surface as a CROSSSLOT or MOVED error rather than a wrong answer.
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
// instead. The mode key is what tells that reader how to read the rest:
// redis_info comes from whichever single node answered INFO, and the pool
// counters are the aggregate across every node's pool — replicas included, since
// ClusterClient.PoolStats accumulates over Masters and then over Slaves.
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
