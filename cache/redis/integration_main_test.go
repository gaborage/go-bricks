//go:build integration

package redis

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

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

// TestMain terminates the shared container after the whole binary has run. It
// never exits early when Docker is missing: the package's unit tests still run,
// and each integration test skips itself through setupRealRedis.
func TestMain(m *testing.M) {
	code := m.Run()
	pkgRedis.Close()
	os.Exit(code)
}
