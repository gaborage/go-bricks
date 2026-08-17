package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	cachetesting "github.com/gaborage/go-bricks/cache/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

const (
	// The shape pgconn actually returns on a failed dial: it redacts the password but
	// not the username, the database name, or the resolved internal address.
	pgconnIdentityError = "failed to connect to `user=app database=payments`: 10.0.0.5:5432 (10.0.0.5): dial error"

	// The strings /ready actually serves. Spelled out here rather than imported from a
	// production constant so the assertions pin the wire format instead of restating it.
	databaseUnavailableBody = "database unavailable"
	cacheUnavailableBody    = "cache unavailable"
)

// TestDatabaseProbeRendersFixedPublicError pins what the database description contributes
// to the unauthenticated 503 body: it is critical, so readyCheck renders it, and the
// rendered string is the synthesized default rather than the pgconn identity string
// (`user=… database=…` plus the resolved host:port). The description declares no
// publicErr — that it is safe anyway is the whole point of the inverted default.
func TestDatabaseProbeRendersFixedPublicError(t *testing.T) {
	st := databaseProbe(newRealConnectorDBManager(&config.Config{}), false).Run(context.Background())
	require.True(t, st.Critical, "a non-critical probe would never reach the 503 render path")

	st.Err = errors.New(pgconnIdentityError)
	assert.Equal(t, databaseUnavailableBody, publicProbeError(&st))
}

// TestDatabaseProbePublicErrorHidesConnectionIdentity pins the split /ready performs: the
// sanitized string is what the unauthenticated body gets, while the full identity-bearing
// driver error stays on HealthStatus.Err for the app log and the IP-allowlisted
// /_sys/health-debug.
func TestDatabaseProbePublicErrorHidesConnectionIdentity(t *testing.T) {
	driverErr := errors.New(pgconnIdentityError)
	probe := probeDescription{
		name:     componentDatabase,
		critical: true,
		live:     func(context.Context) error { return driverErr },
	}

	result := probe.Run(context.Background())

	assert.Equal(t, databaseUnavailableBody, publicProbeError(&result))
	// /_sys/health-debug renders Err verbatim and must keep the detail operators need.
	require.ErrorIs(t, result.Err, driverErr)
	assert.Contains(t, result.Err.Error(), "user=app")
}

func TestDatabaseProbeReportsNotConfigured(t *testing.T) {
	result := databaseProbe(newRealConnectorDBManager(&config.Config{}), false).Run(context.Background())

	assert.Equal(t, notConfiguredStatus, result.Status)
	assert.NoError(t, result.Err, "an absent database is not a readiness failure")
	assert.Equal(t, notConfiguredStatus, result.Details[statusKey])
	// Criticality is retained deliberately: a database that IS configured and down must
	// still fail readiness. Absence is handled by the status, never by demoting the probe.
	assert.True(t, result.Critical)
}

func TestDatabaseProbeStaysUnhealthyForUnsupportedType(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.Type = "mysql"
	cfg.Database.Host = "db.internal"

	result := databaseProbe(newRealConnectorDBManager(cfg), false).Run(context.Background())

	assert.Equal(t, unhealthyStatus, result.Status)
	require.Error(t, result.Err)
	// The other half of the fix: a type the operator actually asked for is a
	// misconfiguration, and must never be softened into "intentionally absent".
	assert.False(t, config.IsNotConfigured(result.Err))
}

func TestDatabaseProbeReportsPerTenantWhenDefaultKeyIsUnconfigured(t *testing.T) {
	cfg := &config.Config{}
	cfg.Multitenant.Enabled = true // no root database block: tenants carry their own

	result := databaseProbe(newRealConnectorDBManager(cfg), true).Run(context.Background())

	// not_configured would claim the service has no database — false when it has N
	// tenant databases that this fixed-key probe simply never covered.
	assert.Equal(t, perTenantStatus, result.Status)
	assert.NoError(t, result.Err)
}

func TestDatabaseProbeStillProbesPerTenantControlPlaneDatabase(t *testing.T) {
	// Multi-tenancy does NOT imply the "" key is unconfigured: a shared-ledger
	// deployment (outbox.tenancy: shared, ADR-041) resolves a real control-plane
	// database through exactly that key. Relabeling to per_tenant before resolving
	// would leave that database unprobed while /ready reported 200 — this test is what
	// catches that.
	cfg := &config.Config{}
	cfg.Multitenant.Enabled = true
	cfg.Database.Type = "mysql" // resolves, then fails to connect
	cfg.Database.Host = "control-plane.internal"

	result := databaseProbe(newRealConnectorDBManager(cfg), true).Run(context.Background())

	assert.Equal(t, unhealthyStatus, result.Status, "a resolvable control-plane database must be probed, not relabeled")
	require.Error(t, result.Err)
	assert.True(t, result.Critical, "and it must still gate readiness")
}

// TestCacheProbePingHonorsCallerContext pins that the ping derives from the caller's context:
// a probe rooted at context.Background() would ignore an already-spent request budget.
func TestCacheProbePingHonorsCallerContext(t *testing.T) {
	mc := cachetesting.NewMockCache().WithDelay(10 * time.Millisecond)
	probe := cacheProbe(cacheManagerServing(t, mc), false, false, false)

	// Warm the pool so the canceled context reaches Health rather than the create path.
	require.Equal(t, healthyStatus, probe.Run(context.Background()).Status)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := probe.Run(ctx)

	assert.Equal(t, unhealthyStatus, result.Status)
	assert.ErrorIs(t, result.Err, context.Canceled)
}

func TestGetStatsOrEmpty(t *testing.T) {
	t.Run("nil stats", func(t *testing.T) {
		result := getStatsOrEmpty(nil)

		assert.NotNil(t, result)
		assert.Empty(t, result)
	})

	t.Run("non-nil stats", func(t *testing.T) {
		input := map[string]any{"key": "value"}
		result := getStatsOrEmpty(input)

		assert.Equal(t, input, result)
	})
}

// Helper functions for creating test managers with various error scenarios

// newDBManagerFor builds a DbManager whose connector always serves db.
func newDBManagerFor(t *testing.T, db database.Interface) *database.DbManager {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "localhost",
			Port: 5432,
		},
	}
	manager := database.NewDbManager(config.NewTenantStore(cfg), logger.New("error", false),
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return db, nil
		},
	)
	t.Cleanup(func() { assert.NoError(t, manager.Close()) })
	return manager
}

// newRealConnectorDBManager builds a DbManager over the REAL config.TenantStore and the
// REAL database.NewConnection (nil connector). Every other app-level fixture injects a
// stub connector, which is exactly how #872 shipped: the defect lived in the seam
// between the config resolver and the connection factory, and a stub replaces that seam.
func newRealConnectorDBManager(cfg *config.Config) *database.DbManager {
	return database.NewDbManager(
		config.NewTenantStore(cfg),
		logger.New("info", false),
		database.DbManagerOptions{},
		nil,
	)
}

// createTestMessagingManagerWithNotReadyClient creates a messaging manager with a client that reports not ready
func createTestMessagingManagerWithNotReadyClient(t *testing.T) *messaging.Manager {
	t.Helper()
	cfg := &config.Config{
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("error", false)

	// Create a mock client that reports not ready
	mockClient := testmocks.NewMockAMQPClient()
	mockClient.SetReady(false)

	return messaging.NewMessagingManager(resourceSource, log,
		messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return mockClient
		},
	)
}

// createTestMessagingManagerWithStats creates a messaging manager that returns specific stats
func createTestMessagingManagerWithStats(t *testing.T, stats map[string]any) *messaging.Manager {
	t.Helper()
	cfg := &config.Config{
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("error", false)

	// Create a manager and pre-populate it to get the desired stats
	manager := messaging.NewMessagingManager(resourceSource, log,
		messaging.ManagerOptions{MaxPublishers: 10, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
	)

	// If we need active publishers, create them. Hold the leases until the test ends (via
	// t.Cleanup) so the publishers stay active for assertions, then release them — discarding
	// the ReleaseFunc would pin refs and could mask the lease lifecycle this PR validates.
	if activePublishers, ok := stats["active_publishers"].(int); ok && activePublishers > 0 {
		releases := make([]func(), 0, activePublishers)
		for i := 0; i < activePublishers; i++ {
			_, release, err := manager.Publisher(context.Background(), "")
			require.NoError(t, err)
			releases = append(releases, release)
		}
		t.Cleanup(func() {
			for _, release := range releases {
				release()
			}
		})
	}

	return manager
}

// cacheManagerServing returns a cache manager whose connector always serves c, registering
// t.Cleanup to close it so callers don't repeat the connector + cleanup boilerplate.
func cacheManagerServing(t *testing.T, c cache.Cache) *cache.CacheManager {
	t.Helper()
	manager := createTestCacheManagerWithConnector(t, func(context.Context, string) (cache.Cache, error) {
		return c, nil
	})
	t.Cleanup(func() { assert.NoError(t, manager.Close()) })
	return manager
}

// createTestCacheManagerWithGetError creates a cache manager that returns an error on Get()
func createTestCacheManagerWithGetError(t *testing.T, err error) *cache.CacheManager {
	t.Helper()
	return createTestCacheManagerWithConnector(t, func(context.Context, string) (cache.Cache, error) {
		return nil, err
	})
}

// createWarmCacheManagerWithHungPing returns a manager whose pooled instance answers PING
// only once the ping context expires — the hung-Redis case the probe's sub-budget bounds.
func createWarmCacheManagerWithHungPing(t *testing.T) *cache.CacheManager {
	t.Helper()
	manager := cacheManagerServing(t, cachetesting.NewMockCache().WithDelay(time.Minute))

	_, release, err := manager.Get(context.Background(), "")
	require.NoError(t, err)
	release()

	return manager
}

// createWarmCacheManagerWithOutage returns a manager whose instance was already created and
// pooled before the backend went down — the #860 case a lease-only probe cannot see.
func createWarmCacheManagerWithOutage(t *testing.T) *cache.CacheManager {
	t.Helper()
	mc := cachetesting.NewMockCache()
	manager := cacheManagerServing(t, mc)

	_, release, err := manager.Get(context.Background(), "")
	require.NoError(t, err)
	release()

	// The shape redis.Client.Health actually returns on a live outage — it names the address,
	// which is what the sanitized /ready body must withhold.
	mc.WithHealthFailure(cache.NewConnectionError("ping", redisProbeAddress, errors.New(errorRedisDown)))
	return manager
}
