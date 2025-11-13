package messaging

// Shared test constants for the messaging package.
// This file consolidates commonly used string literals across all test files
// to eliminate duplication and prevent redeclaration errors.

const (
	// Modified value for testing deep copies
	modifiedValue = "modified"

	// Core test identifiers (used across multiple test files)
	testExchange      = "test-exchange"
	testQueue         = "test-queue"
	testEventType     = "test-event"
	testRoutingKey    = "test.key"
	testConsumer      = "test-consumer"
	testBenchConsumer = "bench-consumer"
	testRoute         = "test.route"
	testMessage       = "test message"
	testMessageID     = "test-message-id"
	testMessageBody   = "test message body"

	// Common test values
	testName   = "test"
	testKey    = "key"
	testValue  = "value"
	testValue1 = "value1"
	testValue2 = "value2"
	testArg    = "arg"

	// Exchange types
	exchangeTypeTopic  = "topic"
	exchangeTypeDirect = "direct"

	// Map keys (commonly used in Args/Headers)
	mapKeyTTL     = "ttl"
	mapKeyBinding = "binding"
	mapKeyVersion = "version"

	// Multi-entity test identifiers (for tests with multiple queues/exchanges)
	testQueue1     = "queue1"
	testQueue2     = "queue2"
	testQueue3     = "queue3"
	testBenchQueue = "bench-queue"
	testExchange1  = "exchange1"
	testExchange2  = "exchange2"
	testConsumer1  = "consumer1"
	testConsumer2  = "consumer2"
	testConsumer3  = "consumer3"
	testEvent1     = "event1"
	testEvent2     = "event2"
	testEvent3     = "event3"
	testBenchEvent = "bench-event"

	// Short names for compact test scenarios
	shortExchange1 = "ex1"
	shortExchange2 = "ex2"
	shortQueue1    = "q1"

	// Version strings
	version10 = "1.0"
	version20 = "2.0"

	// TTL/numeric values
	ttlValue3600 = 3600
	ttlValue7200 = 7200

	// Missing/error scenarios
	missingQueue    = "missing-queue"
	missingExchange = "missing-exchange"

	// Named test entities (for registry tests)
	testExchangeName  = "test-exchange"
	testQueueName     = "test-queue"
	testKeyValue      = "test.key"
	testExchange1Name = "test-exchange-1"
	testExchange2Name = "test-exchange-2"
	testQueue1Name    = "test-queue-1"
	testQueue2Name    = "test-queue-2"
	lateExchangeName  = "late-exchange"
	lateQueueName     = "late-queue"
	testKeyName       = "test-key"
	testValueContent  = "test-value"
	newKeyName        = "new-key"

	// Description strings
	descUserCreation = "User creation event"
	descProcessUser  = "Process user creation"

	// AMQP URLs (for manager/connection tests)
	amqpURLA       = "amqp://a/"
	amqpURLB       = "amqp://b/"
	amqpURLC       = "amqp://c/"
	amqpURLIdle    = "amqp://idle/"
	amqpURLTenant1 = "amqp://tenant1/"
	amqpURLTenant2 = "amqp://tenant2/"

	// Tenant identifiers
	tenantID     = "tenant"
	tenant1ID    = "tenant1"
	tenant2ID    = "tenant2"
	testTenantID = "test-tenant"

	// Generic identifiers (short forms)
	genericQueue    = "queue"
	genericConsumer = "consumer"
	genericError    = "error"
	genericEx       = "ex"

	// Event types
	eventTestEvent = "TestEvent"
	eventA         = "EventA"

	testMessageIDFmt = "msg-%d"
)
