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

// Manager fixtures shared by the readiness, lifecycle, app and debug-health tests.

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
