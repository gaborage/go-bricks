package fixtures

import (
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/testing/mocks"
)

// AMQP message header constants
const (
	ContentTypeHeader     = "content-type"
	ContentEncodingHeader = "content-encoding"
	DeliveryModeHeader    = "delivery-mode"
	PriorityHeader        = "priority"
	CorrelationIDHeader   = "correlation-id"
	ReplyToHeader         = "reply-to"
	ExpirationHeader      = "expiration"
	MessageIDHeader       = "message-id"
	TypeHeader            = "type"
	UserIDHeader          = "user-id"
	AppIDHeader           = "app-id"
	ExchangeHeader        = "exchange"
	RoutingKeyHeader      = "routing-key"
)

// Content type constants
const (
	ApplicationJSONContentType = "application/json"
	TextPlainContentType       = "text/plain"
)

// Test constants
const (
	TestQueueName   = "test.queue"
	TestConsumerTag = "test-consumer"
)

// MessagingFixtures provides helper functions for creating pre-configured messaging mocks
// and message builders for consistent testing.

// NewWorkingMessagingClient creates a mock messaging client that operates successfully.
// This is useful for testing happy path scenarios.
func NewWorkingMessagingClient() *mocks.MockMessagingClient {
	mockClient := mocks.NewMockMessagingClient()

	// Setup successful responses
	mockClient.ExpectIsReady(true)
	mockClient.ExpectPublishAny(nil)
	mockClient.ExpectConsumeAny(nil) // Allow any queue consumption
	mockClient.ExpectClose(nil)

	return mockClient
}

// NewFailingMessagingClient creates a mock messaging client that fails operations after
// a specified number of successful operations. This is useful for testing retry logic
// and error handling.
func NewFailingMessagingClient(failAfter int) *mocks.MockMessagingClient {
	mockClient := mocks.NewMockMessagingClient()

	if failAfter <= 0 {
		// Fail immediately
		mockClient.SetReady(false)
		mockClient.ExpectIsReady(false)
		mockClient.ExpectPublishAny(amqp.ErrClosed).Once()
		mockClient.ExpectConsumeAny(amqp.ErrClosed)
	} else {
		// Succeed initially, then fail
		mockClient.ExpectIsReady(true)
		for i := 0; i < failAfter; i++ {
			mockClient.ExpectPublishAny(nil).Once()
		}
		mockClient.ExpectPublishAny(amqp.ErrClosed).Once()
	}

	return mockClient
}

// NewMessageSimulator creates a mock messaging client that can simulate incoming messages.
// This is useful for testing consumer behavior and message processing.
func NewMessageSimulator(messages ...[]byte) *mocks.MockMessagingClient {
	mockClient := NewWorkingMessagingClient()

	// Pre-load messages for simulation
	for _, msg := range messages {
		mockClient.SimulateMessage(TestQueueName, msg)
	}

	return mockClient
}

// NewDisconnectedMessagingClient creates a mock messaging client that is not ready.
// This is useful for testing connection failure scenarios.
func NewDisconnectedMessagingClient() *mocks.MockMessagingClient {
	mockClient := mocks.NewMockMessagingClient()
	mockClient.SetReady(false)
	mockClient.ExpectIsReady(false)
	return mockClient
}

// AMQP Client Fixtures

// NewWorkingAMQPClient creates a mock AMQP client that operates successfully.
func NewWorkingAMQPClient() *mocks.MockAMQPClient {
	mockClient := mocks.NewMockAMQPClient()

	// Setup successful responses for infrastructure operations
	mockClient.ExpectDeclareExchangeAny(nil) // Allow any exchange
	mockClient.ExpectDeclareQueueAny(nil)    // Allow any queue
	mockClient.ExpectBindQueueAny(nil)       // Allow any binding

	// Setup successful messaging operations
	mockClient.ExpectIsReady(true)
	mockClient.ExpectPublishToExchangeAny(nil)
	mockClient.ExpectClose(nil)

	return mockClient
}

// NewFailingAMQPClient creates a mock AMQP client that fails infrastructure operations.
func NewFailingAMQPClient() *mocks.MockAMQPClient {
	mockClient := mocks.NewMockAMQPClient()
	mockClient.SetReady(false)

	infraErr := amqp.Error{Code: 500, Reason: "INTERNAL_ERROR"}
	mockClient.ExpectDeclareExchangeAny(&infraErr)
	mockClient.ExpectDeclareQueueAny(&infraErr)
	mockClient.ExpectBindQueueAny(&infraErr)

	return mockClient
}

// Registry Fixtures

// NewWorkingRegistry creates a mock registry that operates successfully.
func NewWorkingRegistry() *mocks.MockRegistry {
	mockRegistry := mocks.NewMockRegistry()

	// Setup successful infrastructure operations
	mockRegistry.ExpectRegistrations()

	return mockRegistry
}

// NewFailingRegistry creates a mock registry that fails infrastructure operations.
func NewFailingRegistry() *mocks.MockRegistry {
	mockRegistry := mocks.NewMockRegistry()

	infraErr := amqp.Error{Code: 500, Reason: "INTERNAL_ERROR"}
	mockRegistry.ExpectDeclareInfrastructure(&infraErr)
	mockRegistry.ExpectStartConsumers(&infraErr)

	return mockRegistry
}

// Message Builders

// NewMockDelivery creates an AMQP delivery for testing message handlers.
// This is useful when you need to simulate incoming messages with specific content.
//
// Example:
//
//	delivery := fixtures.NewMockDelivery([]byte(`{"event": "user.created"}`), map[string]any{
//	  fixtures.ContentTypeHeader: fixtures.ApplicationJSONContentType,
//	  fixtures.RoutingKeyHeader:  "user.created",
//	})
func NewMockDelivery(body []byte, headers map[string]any) amqp.Delivery {
	if headers == nil {
		headers = make(map[string]any)
	}

	return amqp.Delivery{
		Headers:         headers,
		ContentType:     getStringHeader(headers, ContentTypeHeader, ApplicationJSONContentType),
		ContentEncoding: getStringHeader(headers, ContentEncodingHeader, ""),
		Body:            body,
		DeliveryMode:    getUint8Header(headers, DeliveryModeHeader, amqp.Persistent),
		Priority:        getUint8Header(headers, PriorityHeader, 0),
		CorrelationId:   getStringHeader(headers, CorrelationIDHeader, ""),
		ReplyTo:         getStringHeader(headers, ReplyToHeader, ""),
		Expiration:      getStringHeader(headers, ExpirationHeader, ""),
		MessageId:       getStringHeader(headers, MessageIDHeader, ""),
		Timestamp:       time.Now(),
		Type:            getStringHeader(headers, TypeHeader, ""),
		UserId:          getStringHeader(headers, UserIDHeader, ""),
		AppId:           getStringHeader(headers, AppIDHeader, ""),
		ConsumerTag:     TestConsumerTag,
		MessageCount:    0,
		DeliveryTag:     1,
		Redelivered:     false,
		Exchange:        getStringHeader(headers, ExchangeHeader, ""),
		RoutingKey:      getStringHeader(headers, RoutingKeyHeader, ""),
	}
}

// NewJSONDelivery creates an AMQP delivery with JSON content type.
func NewJSONDelivery(jsonBody []byte) amqp.Delivery {
	return NewMockDelivery(jsonBody, map[string]any{
		ContentTypeHeader: ApplicationJSONContentType,
	})
}

// NewTextDelivery creates an AMQP delivery with plain text content type.
func NewTextDelivery(textBody []byte) amqp.Delivery {
	return NewMockDelivery(textBody, map[string]any{
		ContentTypeHeader: TextPlainContentType,
	})
}

// Infrastructure Declaration Builders

// NewExchangeDeclaration creates a standard exchange declaration for testing.
func NewExchangeDeclaration(name, exchangeType string) *messaging.ExchangeDeclaration {
	return &messaging.ExchangeDeclaration{
		Name:       name,
		Type:       exchangeType,
		Durable:    true,
		AutoDelete: false,
		Internal:   false,
		NoWait:     false,
		Args:       nil,
	}
}

// NewQueueDeclaration creates a standard queue declaration for testing.
func NewQueueDeclaration(name string) *messaging.QueueDeclaration {
	return &messaging.QueueDeclaration{
		Name:       name,
		Durable:    true,
		AutoDelete: false,
		Exclusive:  false,
		NoWait:     false,
		Args:       nil,
	}
}

// NewBindingDeclaration creates a binding declaration for testing.
func NewBindingDeclaration(queue, exchange, routingKey string) *messaging.BindingDeclaration {
	return &messaging.BindingDeclaration{
		Queue:      queue,
		Exchange:   exchange,
		RoutingKey: routingKey,
		NoWait:     false,
		Args:       nil,
	}
}

// NewPublisherDeclaration creates a publisher declaration for testing.
func NewPublisherDeclaration(exchange, routingKey, eventType string) *messaging.PublisherDeclaration {
	return &messaging.PublisherDeclaration{
		Exchange:    exchange,
		RoutingKey:  routingKey,
		EventType:   eventType,
		Description: "Test publisher for " + eventType,
		Mandatory:   false,
		Immediate:   false,
		Headers:     nil,
	}
}

// NewConsumerDeclaration creates a consumer declaration for testing.
func NewConsumerDeclaration(queue, eventType string, handler messaging.MessageHandler) *messaging.ConsumerDeclaration {
	return &messaging.ConsumerDeclaration{
		Queue:       queue,
		Consumer:    TestConsumerTag,
		AutoAck:     false,
		Exclusive:   false,
		NoLocal:     false,
		NoWait:      false,
		EventType:   eventType,
		Description: "Test consumer for " + eventType,
		Handler:     handler,
	}
}

// Helper functions for header extraction

func getStringHeader(headers map[string]any, key, defaultValue string) string {
	if value, exists := headers[key]; exists {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return defaultValue
}

func getUint8Header(headers map[string]any, key string, defaultValue uint8) uint8 {
	if value, exists := headers[key]; exists {
		if num, ok := value.(uint8); ok {
			return num
		}
	}
	return defaultValue
}
