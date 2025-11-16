package config

// Test Configuration Keys
//
// This file defines constants for configuration keys frequently used in tests
// to eliminate string literal duplication and provide type-safe references.
// These constants should ONLY be used in test files (*_test.go).

const (
	// Cache Configuration Keys
	TestKeyCacheEnabled    = "cache.enabled"
	TestKeyCacheType       = "cache.type"
	TestKeyCacheRedisHost  = "cache.redis.host"
	TestKeyCacheRedisPort  = "cache.redis.port"
	TestKeyCacheRedisDB    = "cache.redis.database"
	TestKeyCacheManagerMax = "cache.manager.max_size"
	TestKeyCacheManagerTTL = "cache.manager.idle_ttl"

	// Messaging Configuration Keys
	TestKeyMessagingBrokerURL      = "messaging.broker.url"
	TestKeyMessagingBrokerHost     = "messaging.broker.host"
	TestKeyMessagingBrokerPort     = "messaging.broker.port"
	TestKeyMessagingBrokerUser     = "messaging.broker.username"
	TestKeyMessagingBrokerPassword = "messaging.broker.password"

	// Server Configuration Keys
	TestKeyServerPort            = "server.port"
	TestKeyServerHost            = "server.host"
	TestKeyServerTimeoutRead     = "server.timeout.read"
	TestKeyServerTimeoutWrite    = "server.timeout.write"
	TestKeyServerTimeoutIdle     = "server.timeout.idle"
	TestKeyServerTimeoutShutdown = "server.timeout.shutdown"

	// Database Configuration Keys
	TestKeyDatabaseType             = "database.type"
	TestKeyDatabaseHost             = "database.host"
	TestKeyDatabasePort             = "database.port"
	TestKeyDatabaseUsername         = "database.username"
	TestKeyDatabasePassword         = "database.password"
	TestKeyDatabaseName             = "database.database"
	TestKeyDatabaseConnectionString = "database.connection_string"

	// Observability Configuration Keys
	TestKeyObservabilityEnabled        = "observability.enabled"
	TestKeyObservabilityServiceName    = "observability.service.name"
	TestKeyObservabilityTraceEnabled   = "observability.trace.enabled"
	TestKeyObservabilityMetricsEnabled = "observability.metrics.enabled"
	TestKeyObservabilityLogsEnabled    = "observability.logs.enabled"

	// Custom/Mock Configuration Keys
	// These are used in config injection tests (see config/injection_test.go)
	TestKeyCustomAPIKey     = "custom.api.key"
	TestKeyCustomAPITimeout = "custom.api.timeout"
	TestKeyCustomAPIRetries = "custom.api.retries"
)
