//go:build integration

package redis

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	gbtesting "github.com/gaborage/go-bricks/testing"
	"github.com/gaborage/go-bricks/testing/containers"
)

// redisLogicalDatabases is how many logical databases a stock Redis server
// serves (0-15) — the same range Config.Validate accepts.
const redisLogicalDatabases = 16

// redisDBCounter hands out those indices round-robin, one per setupRealRedis
// call, so tests get separate keyspaces on the one shared server.
var redisDBCounter atomic.Int32

// pkgRedis holds the single Redis container this package's test binary shares
// (ADR-020). Each test gets its own logical database, flushed on entry by
// setupRealRedis, rather than a fresh server. Starting lazily keeps a
// unit-test-only run from booting Redis at all.
var pkgRedis = containers.NewShared("Redis", 3*time.Minute,
	func(ctx context.Context) (*containers.RedisContainer, bool, error) {
		return containers.StartRedisContainerForTestMain(ctx, nil)
	})

// pkgRedisCluster holds the single cluster-enabled Redis container this
// package's test binary shares, mirroring pkgRedis. One node owns all 16384
// slots, so a cluster-protocol client has a single endpoint to talk to — the
// client-side shape an Amazon ElastiCache Serverless deployment requires. It is
// not that service's topology: serverless shards and redirects (ADR-117), and a
// one-node fixture reproduces neither. It boots lazily, so a run with no cluster
// test pays nothing for it.
var pkgRedisCluster = containers.NewShared("Redis cluster", 3*time.Minute,
	func(ctx context.Context) (*containers.RedisContainer, bool, error) {
		cfg := containers.DefaultRedisConfig()
		cfg.Cluster = true
		return containers.StartRedisContainerForTestMain(ctx, cfg)
	})

// tlsRedisContainer carries the CA the TLS tests must trust alongside the
// container that presents it: Shared hands back exactly one value, and the CA
// is minted inside the start func, so it travels with the container. The LEAF
// travels too, because a test whose argument rests on which names the
// certificate covers has to be able to read them back — see
// requireLeafCoversOnlyPinnedName.
type tlsRedisContainer struct {
	*containers.RedisContainer
	caPEM   []byte
	certPEM []byte
}

// pkgRedisTLS holds the single TLS-only Redis container this package's test
// binary shares, mirroring pkgRedis. It boots lazily too, so a run with no TLS
// test pays nothing for it.
var pkgRedisTLS = containers.NewShared("Redis TLS", 3*time.Minute,
	func(ctx context.Context) (*tlsRedisContainer, bool, error) {
		return startTLSRedisContainer(ctx, containers.DefaultRedisConfig(), gbtesting.DefaultCertHosts()...)
	})

// pkgRedisClusterTLS holds the single cluster-enabled, TLS-only Redis container
// this package's test binary shares: the cluster protocol with encryption in
// transit turned on, which is the pair an ElastiCache Serverless client has to
// speak. Its sharding and its redirects are still absent (ADR-117); only the
// client-side shape is reproduced. Lazy like the rest, so a run with no
// cluster-TLS test pays nothing.
//
// Its leaf covers clusterTLSServerName and NOTHING else, deliberately — see
// setupClusterTLSRedis for what a certificate covering the announced address
// too would cost the assertion, and requireLeafCoversOnlyPinnedName for the
// guard that holds this line to it.
var pkgRedisClusterTLS = containers.NewShared("Redis cluster TLS", 3*time.Minute,
	func(ctx context.Context) (*tlsRedisContainer, bool, error) {
		cfg := containers.DefaultRedisConfig()
		cfg.Cluster = true
		return startTLSRedisContainer(ctx, cfg, clusterTLSServerName)
	})

// startTLSRedisContainer mints a throwaway CA and a leaf covering certHosts,
// attaches them to the caller's cfg, starts the container and hands the CA and
// the leaf back alongside it — the clients under test have no other way to
// trust it, and the SAN set is a premise a test may need to assert on. The
// caller shapes cfg, so what TLS composes with stays its decision.
func startTLSRedisContainer(ctx context.Context, cfg *containers.RedisContainerConfig, certHosts ...string) (*tlsRedisContainer, bool, error) {
	caPEM, certPEM, keyPEM, err := gbtesting.NewCAAndServerCertPEM(certHosts...)
	if err != nil {
		return nil, true, err
	}

	cfg.TLS = &containers.RedisTLSMaterial{CA: caPEM, Cert: certPEM, Key: keyPEM}

	c, dockerAvailable, err := containers.StartRedisContainerForTestMain(ctx, cfg)
	if c == nil {
		return nil, dockerAvailable, err
	}
	return &tlsRedisContainer{RedisContainer: c, caPEM: caPEM, certPEM: certPEM}, dockerAvailable, err
}

// TestMain terminates the shared containers after the whole binary has run. It
// never exits early when Docker is missing: the package's unit tests still run,
// and each integration test skips itself when it leases its container, inside
// containers.Shared.Get — reached through setupRealRedis for the plaintext
// tests and through the setup helpers of the cluster, TLS and cluster-TLS ones.
func TestMain(m *testing.M) {
	code := m.Run()
	pkgRedis.Close()
	pkgRedisTLS.Close()
	pkgRedisCluster.Close()
	pkgRedisClusterTLS.Close()
	os.Exit(code)
}
