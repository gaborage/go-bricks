package app

import (
	"context"
	"encoding/json"
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

	// The two bodies /ready serves since ADR-120, whatever the probe set and whichever
	// listener answers. Spelled out here rather than built from the production renderers so
	// every assertion pins the wire format instead of restating a constant.
	readyBodyJSON    = `{"status":"ready"}`
	notReadyBodyJSON = `{"status":"not ready"}`
)

// readyBodyMap and notReadyBodyMap are those same two bodies decoded, for the handler tests
// that unmarshal before asserting. Whole-map equality is the assertion throughout: a kind
// name, a counter or an error text that reappears fails it wherever it is added.
var (
	readyBodyMap    = map[string]any{"status": "ready"}
	notReadyBodyMap = map[string]any{"status": "not ready"}
)

// assertReadyBodyOmits pins that none of the given strings appears ANYWHERE in a /ready body —
// not only under the key it was expected on, so a future field (or one nobody has written yet)
// that reintroduces the value elsewhere is caught too. It asserts nothing about which status code
// produced the body: a leak has no status code. body is the rendered body off the wire, or any
// value to render.
func assertReadyBodyOmits(t *testing.T, body any, forbidden ...string) {
	t.Helper()

	rendered, ok := body.(string)
	if !ok {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rendered = string(raw)
	}
	for _, s := range forbidden {
		assert.NotContainsf(t, rendered, s, "/ready is unauthenticated; %q must not reach its body", s)
	}
}

// judgedComponents re-judges every registered kind and returns the debug view's entries. Since
// ADR-120 /ready answers its verdict alone, so a per-kind status — the fact several tests are
// actually about — is read here rather than out of the body.
func judgedComponents(t *testing.T, a *App) map[string]componentHealth {
	t.Helper()

	return a.judge.full(context.Background()).debugComponents()
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
	mockClient.ExpectClose(nil) // the manager's own Close, below, reaches the pooled client

	manager := messaging.NewMessagingManager(resourceSource, log,
		messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return mockClient
		},
	)
	t.Cleanup(func() { _ = manager.Close() }) // stop the idle-publisher sweep this manager starts

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

// createWarmCacheManagerWithOutage returns a manager whose instance was already created and
// pooled before the backend went down — the #860 case a lease-only probe cannot see.
func createWarmCacheManagerWithOutage(t *testing.T) *cache.CacheManager {
	t.Helper()
	mc := cachetesting.NewMockCache()
	manager := warmCacheManager(t, mc)

	// The shape redis.Client.Health actually returns on a live outage — it names the address,
	// which is why it reaches the log and the access-controlled debug view and never /ready.
	mc.WithHealthFailure(cache.NewConnectionError("ping", redisProbeAddress, errors.New(errorRedisDown)))
	return manager
}

// warmCacheManager returns a manager serving mc whose instance is already pooled: one
// Get+release, so the next probe takes the warm path.
func warmCacheManager(t *testing.T, mc cache.Cache) *cache.CacheManager {
	t.Helper()
	manager := cacheManagerServing(t, mc)

	_, release, err := manager.Get(context.Background(), "")
	require.NoError(t, err)
	release()

	return manager
}
