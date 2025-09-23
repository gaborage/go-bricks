package messaging

import (
	"context"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/logger"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// Registry manages messaging infrastructure declarations across modules.
// It ensures queues, exchanges, and bindings are properly declared before use.
// It also manages consumer lifecycle and handles message routing to handlers.
type Registry struct {
	client          AMQPClient
	logger          logger.Logger
	exchanges       map[string]*ExchangeDeclaration
	queues          map[string]*QueueDeclaration
	bindings        []*BindingDeclaration
	publishers      []*PublisherDeclaration
	consumers       []*ConsumerDeclaration
	mu              sync.RWMutex
	declared        bool
	consumersActive bool
	cancelConsumers context.CancelFunc
}

// ExchangeDeclaration defines an exchange to be declared
type ExchangeDeclaration struct {
	Name       string         // Exchange name
	Type       string         // Exchange type (direct, topic, fanout, headers)
	Durable    bool           // Survive server restart
	AutoDelete bool           // Delete when no longer used
	Internal   bool           // Internal exchange
	NoWait     bool           // Do not wait for server confirmation
	Args       map[string]any // Additional arguments
}

// QueueDeclaration defines a queue to be declared
type QueueDeclaration struct {
	Name       string         // Queue name
	Durable    bool           // Survive server restart
	AutoDelete bool           // Delete when no consumers
	Exclusive  bool           // Only accessible by declaring connection
	NoWait     bool           // Do not wait for server confirmation
	Args       map[string]any // Additional arguments
}

// BindingDeclaration defines a queue-to-exchange binding
type BindingDeclaration struct {
	Queue      string         // Queue name
	Exchange   string         // Exchange name
	RoutingKey string         // Routing key pattern
	NoWait     bool           // Do not wait for server confirmation
	Args       map[string]any // Additional arguments
}

// PublisherDeclaration defines what a module publishes
type PublisherDeclaration struct {
	Exchange    string         // Target exchange
	RoutingKey  string         // Default routing key
	EventType   string         // Event type identifier
	Description string         // Human-readable description
	Mandatory   bool           // Message must be routed to a queue
	Immediate   bool           // Message must be delivered immediately
	Headers     map[string]any // Default headers
}

// ConsumerDeclaration defines what a module consumes and how to handle messages
type ConsumerDeclaration struct {
	Queue       string         // Queue to consume from
	Consumer    string         // Consumer tag
	AutoAck     bool           // Automatically acknowledge messages
	Exclusive   bool           // Exclusive consumer
	NoLocal     bool           // Do not deliver to the connection that published
	NoWait      bool           // Do not wait for server confirmation
	EventType   string         // Event type identifier
	Description string         // Human-readable description
	Handler     MessageHandler // Message handler (optional for documentation-only declarations)
}

// NewRegistry creates a new messaging registry
func NewRegistry(client AMQPClient, log logger.Logger) *Registry {
	return &Registry{
		client:     client,
		logger:     log,
		exchanges:  make(map[string]*ExchangeDeclaration),
		queues:     make(map[string]*QueueDeclaration),
		bindings:   make([]*BindingDeclaration, 0),
		publishers: make([]*PublisherDeclaration, 0),
		consumers:  make([]*ConsumerDeclaration, 0),
	}
}

// RegisterExchange registers an exchange for declaration
func (r *Registry) RegisterExchange(declaration *ExchangeDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.declared {
		r.logger.Warn().
			Str("exchange", declaration.Name).
			Msg("Cannot register exchange after infrastructure has been declared")
		return
	}

	r.exchanges[declaration.Name] = declaration
	r.logger.Debug().
		Str("exchange", declaration.Name).
		Str("type", declaration.Type).
		Msg("Registered exchange for declaration")
}

// RegisterQueue registers a queue for declaration
func (r *Registry) RegisterQueue(declaration *QueueDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.declared {
		r.logger.Warn().
			Str("queue", declaration.Name).
			Msg("Cannot register queue after infrastructure has been declared")
		return
	}

	r.queues[declaration.Name] = declaration
	r.logger.Debug().
		Str("queue", declaration.Name).
		Msg("Registered queue for declaration")
}

// RegisterBinding registers a binding for declaration
func (r *Registry) RegisterBinding(declaration *BindingDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.declared {
		r.logger.Warn().
			Str("queue", declaration.Queue).
			Str("exchange", declaration.Exchange).
			Msg("Cannot register binding after infrastructure has been declared")
		return
	}

	r.bindings = append(r.bindings, declaration)
	r.logger.Debug().
		Str("queue", declaration.Queue).
		Str("exchange", declaration.Exchange).
		Str("routing_key", declaration.RoutingKey).
		Msg("Registered binding for declaration")
}

// RegisterPublisher registers a publisher declaration
func (r *Registry) RegisterPublisher(declaration *PublisherDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.publishers = append(r.publishers, declaration)
	r.logger.Debug().
		Str("exchange", declaration.Exchange).
		Str("routing_key", declaration.RoutingKey).
		Str("event_type", declaration.EventType).
		Msg("Registered publisher")
}

// RegisterConsumer registers a consumer declaration
func (r *Registry) RegisterConsumer(declaration *ConsumerDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.consumers = append(r.consumers, declaration)
	r.logger.Debug().
		Str("queue", declaration.Queue).
		Str("event_type", declaration.EventType).
		Msg("Registered consumer")
}

// DeclareInfrastructure declares all registered messaging infrastructure
func (r *Registry) DeclareInfrastructure(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.declared {
		return nil // Already declared
	}

	if r.client == nil {
		return fmt.Errorf("AMQP client is not available")
	}

	// Wait for AMQP client to be ready with timeout
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for AMQP client: %w", ctx.Err())
		case <-timeout.C:
			return fmt.Errorf("timeout waiting for AMQP client to be ready")
		case <-ticker.C:
			if r.client.IsReady() {
				r.logger.Info().Msg("AMQP client is ready, proceeding with infrastructure declaration")
				goto ready
			}
			r.logger.Debug().Msg("Waiting for AMQP client to be ready...")
		}
	}

ready:

	r.logger.Info().
		Int("exchanges", len(r.exchanges)).
		Int("queues", len(r.queues)).
		Int("bindings", len(r.bindings)).
		Msg("Declaring messaging infrastructure")

	// Declare exchanges first
	for name, exchange := range r.exchanges {
		if err := r.client.DeclareExchange(
			name,
			exchange.Type,
			exchange.Durable,
			exchange.AutoDelete,
			exchange.Internal,
			exchange.NoWait,
		); err != nil {
			return fmt.Errorf("failed to declare exchange %s: %w", name, err)
		}
		r.logger.Info().
			Str("exchange", name).
			Str("type", exchange.Type).
			Msg("Exchange declared successfully")
	}

	// Declare queues
	for name, queue := range r.queues {
		if err := r.client.DeclareQueue(
			name,
			queue.Durable,
			queue.AutoDelete,
			queue.Exclusive,
			queue.NoWait,
		); err != nil {
			return fmt.Errorf("failed to declare queue %s: %w", name, err)
		}
		r.logger.Info().
			Str("queue", name).
			Msg("Queue declared successfully")
	}

	// Create bindings
	for _, binding := range r.bindings {
		if err := r.client.BindQueue(
			binding.Queue,
			binding.Exchange,
			binding.RoutingKey,
			binding.NoWait,
		); err != nil {
			return fmt.Errorf("failed to bind queue %s to exchange %s: %w", binding.Queue, binding.Exchange, err)
		}
		r.logger.Info().
			Str("queue", binding.Queue).
			Str("exchange", binding.Exchange).
			Str("routing_key", binding.RoutingKey).
			Msg("Queue binding created successfully")
	}

	r.declared = true
	r.logger.Info().Msg("All messaging infrastructure declared successfully")

	return nil
}

// StartConsumers starts all registered consumers with handlers.
// This should be called after DeclareInfrastructure and before starting the main application.
func (r *Registry) StartConsumers(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.consumersActive {
		return nil // Already started
	}

	if r.client == nil || !r.client.IsReady() {
		return fmt.Errorf("AMQP client is not ready")
	}

	// Count consumers with handlers
	consumersWithHandlers := 0
	for _, consumer := range r.consumers {
		if consumer.Handler != nil {
			consumersWithHandlers++
		}
	}

	r.logger.Info().
		Int("total_consumers", len(r.consumers)).
		Int("consumers_with_handlers", consumersWithHandlers).
		Msg("Starting message consumers")

	// Create cancellation context for all consumers
	consumerCtx, cancel := context.WithCancel(ctx)
	r.cancelConsumers = cancel

	// Start each consumer with a handler
	for _, consumer := range r.consumers {
		if consumer.Handler == nil {
			r.logger.Debug().
				Str("queue", consumer.Queue).
				Str("event_type", consumer.EventType).
				Msg("Consumer has no handler, skipping (documentation only)")
			continue
		}

		r.logger.Info().
			Str("queue", consumer.Queue).
			Str("consumer", consumer.Consumer).
			Str("event_type", consumer.EventType).
			Msg("Starting consumer")

		if err := r.startSingleConsumer(consumerCtx, consumer); err != nil {
			cancel() // Cancel all consumers on error
			return fmt.Errorf("failed to start consumer for queue %s: %w", consumer.Queue, err)
		}
	}

	r.consumersActive = true
	r.logger.Info().Msg("All consumers started successfully")
	return nil
}

// StopConsumers gracefully stops all running consumers.
func (r *Registry) StopConsumers() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.consumersActive {
		return
	}

	r.logger.Info().Msg("Stopping all consumers")

	if r.cancelConsumers != nil {
		r.cancelConsumers()
		r.cancelConsumers = nil
	}

	r.consumersActive = false
	r.logger.Info().Msg("All consumers stopped")
}

// startSingleConsumer starts a consumer for a specific queue and routes messages to the handler.
func (r *Registry) startSingleConsumer(ctx context.Context, consumer *ConsumerDeclaration) error {
	deliveries, err := r.client.ConsumeFromQueue(ctx, ConsumeOptions{
		Queue:     consumer.Queue,
		Consumer:  consumer.Consumer,
		AutoAck:   consumer.AutoAck,
		Exclusive: consumer.Exclusive,
		NoLocal:   consumer.NoLocal,
		NoWait:    consumer.NoWait,
	})
	if err != nil {
		return fmt.Errorf("failed to start consuming from queue %s: %w", consumer.Queue, err)
	}

	// Start goroutine to handle messages
	go r.handleMessages(ctx, consumer, deliveries)

	return nil
}

// handleMessages processes messages from a consumer and routes them to the handler.
func (r *Registry) handleMessages(ctx context.Context, consumer *ConsumerDeclaration, deliveries <-chan amqp.Delivery) {
	log := r.logger.WithFields(map[string]any{
		"queue":      consumer.Queue,
		"consumer":   consumer.Consumer,
		"event_type": consumer.EventType,
	})

	log.Info().Msg("Message handler started")

	defer func() {
		log.Info().Msg("Message handler stopped")
	}()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("Consumer context cancelled, stopping message handler")
			return

		case delivery, ok := <-deliveries:
			if !ok {
				log.Warn().Msg("Delivery channel closed, stopping message handler")
				return
			}

			r.processMessage(ctx, consumer, &delivery, log)
		}
	}
}

// processMessage processes a single message using the consumer's handler.
func (r *Registry) processMessage(ctx context.Context, consumer *ConsumerDeclaration, delivery *amqp.Delivery, log logger.Logger) {
	startTime := time.Now()

	// Extract trace context using centralized trace package
	accessor := &amqpDeliveryAccessor{headers: delivery.Headers}
	msgCtx := gobrickstrace.ExtractFromHeaders(ctx, accessor)
	traceID := gobrickstrace.EnsureTraceID(msgCtx)
	tlog := log.WithFields(map[string]any{"trace_id": traceID})

	tlog.Debug().
		Str("message_id", delivery.MessageId).
		Str("routing_key", delivery.RoutingKey).
		Str("exchange", delivery.Exchange).
		Uint64("delivery_tag", delivery.DeliveryTag).
		Int("body_size", len(delivery.Body)).
		Msg("Processing message")

	// Process message with handler
	err := consumer.Handler.Handle(msgCtx, delivery)
	processingTime := time.Since(startTime)

	if err != nil {
		tlog.Error().
			Err(err).
			Str("message_id", delivery.MessageId).
			Dur("processing_time", processingTime).
			Msg("Message processing failed")

		// Negative acknowledgment - requeue the message (only when AutoAck is false)
		if !consumer.AutoAck {
			if nackErr := delivery.Nack(false, true); nackErr != nil {
				tlog.Error().
					Err(nackErr).
					Uint64("delivery_tag", delivery.DeliveryTag).
					Msg("Failed to nack message")
			}
		}
		return
	}

	tlog.Info().
		Str("message_id", delivery.MessageId).
		Dur("processing_time", processingTime).
		Msg("Message processed successfully")

	// Positive acknowledgment (only when AutoAck is false)
	if !consumer.AutoAck {
		if ackErr := delivery.Ack(false); ackErr != nil {
			tlog.Error().
				Err(ackErr).
				Uint64("delivery_tag", delivery.DeliveryTag).
				Msg("Failed to ack message")
		}
	}
}

// amqpDeliveryAccessor implements trace.HeaderAccessor for AMQP delivery headers (read-only)
type amqpDeliveryAccessor struct {
	headers amqp.Table
}

func (a *amqpDeliveryAccessor) Get(key string) any {
	if a.headers == nil {
		return nil
	}
	return a.headers[key]
}

func (a *amqpDeliveryAccessor) Set(_ string, _ any) {
	// Read-only accessor for delivery headers
	// This is intentionally a no-op as we don't modify incoming message headers
}

// GetPublishers returns all registered publishers (for documentation/monitoring)
func (r *Registry) GetPublishers() []*PublisherDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	publishers := make([]*PublisherDeclaration, len(r.publishers))
	copy(publishers, r.publishers)
	return publishers
}

// GetConsumers returns all registered consumers (for documentation/monitoring)
func (r *Registry) GetConsumers() []*ConsumerDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	consumers := make([]*ConsumerDeclaration, len(r.consumers))
	copy(consumers, r.consumers)
	return consumers
}

// ValidatePublisher checks if a publisher is registered for the given exchange/routing key
func (r *Registry) ValidatePublisher(exchange, routingKey string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, pub := range r.publishers {
		if pub.Exchange == exchange && pub.RoutingKey == routingKey {
			return true
		}
	}
	return false
}

// ValidateConsumer checks if a consumer is registered for the given queue
func (r *Registry) ValidateConsumer(queue string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, cons := range r.consumers {
		if cons.Queue == queue {
			return true
		}
	}
	return false
}
