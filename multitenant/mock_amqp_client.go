package multitenant

import (
	"context"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/messaging"
)

// RecordingAMQPClient is a mock AMQP client that records all operation calls
// but never connects to a real broker. It's used during startup to capture
// messaging declarations from modules without requiring an active RabbitMQ connection.
//
// This client always reports success for all operations and maintains ready state,
// making it transparent to modules - they can't tell it's a mock.
type RecordingAMQPClient struct {
	mu sync.RWMutex

	// Operation tracking for debugging/validation
	declareExchangeCalls []ExchangeCall
	declareQueueCalls    []QueueCall
	bindQueueCalls       []BindingCall
	publishCalls         []PublishCall
	consumeCalls         []ConsumeCall

	// State
	ready  bool
	closed bool
}

// ExchangeCall records a DeclareExchange operation
type ExchangeCall struct {
	Name       string
	Kind       string
	Durable    bool
	AutoDelete bool
	Internal   bool
	NoWait     bool
}

// QueueCall records a DeclareQueue operation
type QueueCall struct {
	Name       string
	Durable    bool
	AutoDelete bool
	Exclusive  bool
	NoWait     bool
}

// BindingCall records a BindQueue operation
type BindingCall struct {
	Queue      string
	Exchange   string
	RoutingKey string
	NoWait     bool
}

// PublishCall records a publish operation
type PublishCall struct {
	Options messaging.PublishOptions
	Data    []byte
}

// ConsumeCall records a consume operation
type ConsumeCall struct {
	Options messaging.ConsumeOptions
}

// NewRecordingAMQPClient creates a new recording AMQP client that captures
// all operations without executing them on a real broker.
func NewRecordingAMQPClient() *RecordingAMQPClient {
	return &RecordingAMQPClient{
		ready:                true, // Always ready - no actual connection needed
		closed:               false,
		declareExchangeCalls: make([]ExchangeCall, 0),
		declareQueueCalls:    make([]QueueCall, 0),
		bindQueueCalls:       make([]BindingCall, 0),
		publishCalls:         make([]PublishCall, 0),
		consumeCalls:         make([]ConsumeCall, 0),
	}
}

// Verify that RecordingAMQPClient implements all required interfaces
var _ messaging.AMQPClient = (*RecordingAMQPClient)(nil)
var _ messaging.Client = (*RecordingAMQPClient)(nil)

// IsReady always returns true - the recording client is always "ready"
// since it doesn't need a real broker connection.
func (r *RecordingAMQPClient) IsReady() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ready && !r.closed
}

// Close marks the client as closed but doesn't actually close anything.
// Returns nil (success) to maintain the illusion of normal operation.
func (r *RecordingAMQPClient) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

// DeclareExchange records the exchange declaration but doesn't execute it.
// Always returns nil (success) to make modules think the operation succeeded.
func (r *RecordingAMQPClient) DeclareExchange(name, kind string, durable, autoDelete, internal, noWait bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	call := ExchangeCall{
		Name:       name,
		Kind:       kind,
		Durable:    durable,
		AutoDelete: autoDelete,
		Internal:   internal,
		NoWait:     noWait,
	}
	r.declareExchangeCalls = append(r.declareExchangeCalls, call)

	return nil // Always succeed
}

// DeclareQueue records the queue declaration but doesn't execute it.
// Always returns nil (success) to make modules think the operation succeeded.
func (r *RecordingAMQPClient) DeclareQueue(name string, durable, autoDelete, exclusive, noWait bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	call := QueueCall{
		Name:       name,
		Durable:    durable,
		AutoDelete: autoDelete,
		Exclusive:  exclusive,
		NoWait:     noWait,
	}
	r.declareQueueCalls = append(r.declareQueueCalls, call)

	return nil // Always succeed
}

// BindQueue records the binding operation but doesn't execute it.
// Always returns nil (success) to make modules think the operation succeeded.
func (r *RecordingAMQPClient) BindQueue(queue, exchange, routingKey string, noWait bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	call := BindingCall{
		Queue:      queue,
		Exchange:   exchange,
		RoutingKey: routingKey,
		NoWait:     noWait,
	}
	r.bindQueueCalls = append(r.bindQueueCalls, call)

	return nil // Always succeed
}

// PublishToExchange records the publish operation but doesn't actually publish.
// Always returns nil (success) since we're just recording operations.
func (r *RecordingAMQPClient) PublishToExchange(_ context.Context, options messaging.PublishOptions, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	call := PublishCall{
		Options: options,
		Data:    make([]byte, len(data)), // Copy to avoid mutations
	}
	copy(call.Data, data)
	r.publishCalls = append(r.publishCalls, call)

	return nil // Always succeed
}

// ConsumeFromQueue records the consume operation but returns a closed channel.
// This is safe because during declaration recording, no actual message processing
// should occur - we're just capturing the infrastructure topology.
func (r *RecordingAMQPClient) ConsumeFromQueue(_ context.Context, options messaging.ConsumeOptions) (<-chan amqp.Delivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	call := ConsumeCall{
		Options: options,
	}
	r.consumeCalls = append(r.consumeCalls, call)

	// Return a closed channel - no messages will be delivered during recording
	ch := make(chan amqp.Delivery)
	close(ch)
	return ch, nil // Always succeed
}

// Publish records basic publish operations (for backward compatibility).
// Always returns nil (success) since we're just recording operations.
func (r *RecordingAMQPClient) Publish(_ context.Context, destination string, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Convert to PublishToExchange format for consistency
	options := messaging.PublishOptions{
		Exchange:   destination, // Assume destination is exchange name
		RoutingKey: "",          // No routing key for basic publish
	}

	call := PublishCall{
		Options: options,
		Data:    make([]byte, len(data)), // Copy to avoid mutations
	}
	copy(call.Data, data)
	r.publishCalls = append(r.publishCalls, call)

	return nil // Always succeed
}

// Consume records basic consume operations (for backward compatibility).
// Returns a closed channel since we're not processing messages during recording.
func (r *RecordingAMQPClient) Consume(_ context.Context, destination string) (<-chan amqp.Delivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Convert to ConsumeFromQueue format for consistency
	options := messaging.ConsumeOptions{
		Queue: destination, // Assume destination is queue name
	}

	call := ConsumeCall{
		Options: options,
	}
	r.consumeCalls = append(r.consumeCalls, call)

	// Return a closed channel - no messages will be delivered during recording
	ch := make(chan amqp.Delivery)
	close(ch)
	return ch, nil // Always succeed
}

// GetCallCounts returns the number of calls made to each operation type.
// This is useful for testing and validation of the recording process.
func (r *RecordingAMQPClient) GetCallCounts() CallCounts {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return CallCounts{
		DeclareExchange: len(r.declareExchangeCalls),
		DeclareQueue:    len(r.declareQueueCalls),
		BindQueue:       len(r.bindQueueCalls),
		Publish:         len(r.publishCalls),
		Consume:         len(r.consumeCalls),
	}
}

// CallCounts contains the number of calls made to each operation
type CallCounts struct {
	DeclareExchange int
	DeclareQueue    int
	BindQueue       int
	Publish         int
	Consume         int
}

// GetExchangeCalls returns a copy of all recorded exchange declaration calls
func (r *RecordingAMQPClient) GetExchangeCalls() []ExchangeCall {
	r.mu.RLock()
	defer r.mu.RUnlock()

	calls := make([]ExchangeCall, len(r.declareExchangeCalls))
	copy(calls, r.declareExchangeCalls)
	return calls
}

// GetQueueCalls returns a copy of all recorded queue declaration calls
func (r *RecordingAMQPClient) GetQueueCalls() []QueueCall {
	r.mu.RLock()
	defer r.mu.RUnlock()

	calls := make([]QueueCall, len(r.declareQueueCalls))
	copy(calls, r.declareQueueCalls)
	return calls
}

// GetBindingCalls returns a copy of all recorded binding calls
func (r *RecordingAMQPClient) GetBindingCalls() []BindingCall {
	r.mu.RLock()
	defer r.mu.RUnlock()

	calls := make([]BindingCall, len(r.bindQueueCalls))
	copy(calls, r.bindQueueCalls)
	return calls
}

// Reset clears all recorded calls. Useful for testing scenarios where you want
// to start with a clean slate.
func (r *RecordingAMQPClient) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.declareExchangeCalls = make([]ExchangeCall, 0)
	r.declareQueueCalls = make([]QueueCall, 0)
	r.bindQueueCalls = make([]BindingCall, 0)
	r.publishCalls = make([]PublishCall, 0)
	r.consumeCalls = make([]ConsumeCall, 0)
}
