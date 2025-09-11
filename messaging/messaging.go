// Package messaging provides a unified interface for message queue operations.
// It abstracts the underlying messaging implementation to allow for easy testing and future extensibility.
package messaging

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Client defines the interface for messaging operations.
// It provides a simple API for publishing and consuming messages while hiding
// the complexity of connection management, retries, and protocol-specific details.
type Client interface {
	// Publish sends a message to the specified destination.
	// destination can be a queue name, exchange, or topic depending on the implementation.
	// Returns an error if the publish operation fails.
	Publish(ctx context.Context, destination string, data []byte) error

	// Consume starts consuming messages from the specified destination.
	// Returns a channel that delivers messages and an error if consumption setup fails.
	// Messages should be acknowledged by the consumer.
	Consume(ctx context.Context, destination string) (<-chan amqp.Delivery, error)

	// Close gracefully shuts down the messaging client.
	// It should clean up all connections and resources.
	Close() error

	// IsReady returns true if the client is connected and ready to send/receive messages.
	IsReady() bool
}

// PublishOptions contains options for publishing messages with AMQP-specific features.
type PublishOptions struct {
	Exchange   string                 // AMQP exchange name
	RoutingKey string                 // AMQP routing key
	Headers    map[string]interface{} // Message headers
	Mandatory  bool                   // AMQP mandatory flag
	Immediate  bool                   // AMQP immediate flag
}

// ConsumeOptions contains options for consuming messages with AMQP-specific features.
type ConsumeOptions struct {
	Queue     string // Queue name to consume from
	Consumer  string // Consumer tag
	AutoAck   bool   // Auto-acknowledge messages
	Exclusive bool   // Exclusive consumer
	NoLocal   bool   // No-local flag
	NoWait    bool   // No-wait flag
}

// AMQPClient extends the basic Client interface with AMQP-specific functionality.
// This allows for more advanced AMQP features while maintaining the simple interface.
type AMQPClient interface {
	Client

	// PublishToExchange publishes a message to a specific exchange with routing key.
	PublishToExchange(ctx context.Context, options PublishOptions, data []byte) error

	// ConsumeFromQueue consumes messages from a queue with specific options.
	ConsumeFromQueue(ctx context.Context, options ConsumeOptions) (<-chan amqp.Delivery, error)

	// DeclareQueue declares a queue with the given parameters.
	DeclareQueue(name string, durable, autoDelete, exclusive, noWait bool) error

	// DeclareExchange declares an exchange with the given parameters.
	DeclareExchange(name, kind string, durable, autoDelete, internal, noWait bool) error

	// BindQueue binds a queue to an exchange with a routing key.
	BindQueue(queue, exchange, routingKey string, noWait bool) error
}

// MessageHandler defines the interface for processing consumed messages.
// Handlers should implement this interface to process specific message types.
type MessageHandler interface {
	// Handle processes a message and returns an error if processing fails.
	// If an error is returned, the message will be negatively acknowledged (nack).
	// If no error is returned, the message will be acknowledged (ack).
	// The delivery is passed by pointer for performance reasons.
	Handle(ctx context.Context, delivery *amqp.Delivery) error

	// EventType returns the event type this handler can process.
	// This is used for routing messages to the correct handler.
	EventType() string
}
