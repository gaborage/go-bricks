package messaging

import (
	"context"
	"fmt"
	"maps"
	"runtime/debug"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// RegistryInterface defines the contract for messaging infrastructure management.
// This interface allows for easy mocking and testing of messaging infrastructure.
type RegistryInterface interface {
	// Registration methods
	RegisterExchange(declaration *ExchangeDeclaration)
	RegisterQueue(declaration *QueueDeclaration)
	RegisterBinding(declaration *BindingDeclaration)
	RegisterPublisher(declaration *PublisherDeclaration)
	RegisterConsumer(declaration *ConsumerDeclaration)

	// Infrastructure lifecycle
	DeclareInfrastructure(ctx context.Context) error
	StartConsumers(ctx context.Context) error
	StopConsumers()

	// Accessor methods for testing/monitoring
	Exchanges() map[string]*ExchangeDeclaration
	Queues() map[string]*QueueDeclaration
	Bindings() []*BindingDeclaration
	Publishers() []*PublisherDeclaration
	Consumers() []*ConsumerDeclaration

	// Validation methods
	ValidatePublisher(exchange, routingKey string) bool
	ValidateConsumer(queue string) bool
}

// Registry manages messaging infrastructure declarations across modules.
// It ensures queues, exchanges, and bindings are properly declared before use.
// It also manages consumer lifecycle and handles message routing to handlers.
type Registry struct {
	client     AMQPClient
	logger     logger.Logger
	exchanges  map[string]*ExchangeDeclaration
	queues     map[string]*QueueDeclaration
	bindings   []*BindingDeclaration
	publishers []*PublisherDeclaration
	// Mutex protects: consumerIndex, consumerOrder, consumersActive, declared
	// NOTE: GoBricks startup is single-threaded, but multi-tenant scenarios
	// may have concurrent registry access during tenant initialization.
	mu              sync.RWMutex
	consumerIndex   map[consumerKey]*ConsumerDeclaration // Defense-in-depth deduplication
	consumerOrder   []consumerKey                        // Deterministic iteration order
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
	Queue         string         // Queue to consume from
	Consumer      string         // Consumer tag
	AutoAck       bool           // Automatically acknowledge messages
	Exclusive     bool           // Exclusive consumer
	NoLocal       bool           // Do not deliver to the connection that published
	NoWait        bool           // Do not wait for server confirmation
	EventType     string         // Event type identifier
	Description   string         // Human-readable description
	Handler       MessageHandler // Message handler (optional for documentation-only declarations)
	Workers       int            // Number of concurrent workers (0 = auto-scale to NumCPU*4, >0 = explicit)
	PrefetchCount int            // RabbitMQ prefetch count (0 = auto-scale to Workers*10, capped at 500)
}

// NewRegistry creates a new messaging registry
func NewRegistry(client AMQPClient, log logger.Logger) *Registry {
	return &Registry{
		client:        client,
		logger:        log,
		exchanges:     make(map[string]*ExchangeDeclaration),
		queues:        make(map[string]*QueueDeclaration),
		bindings:      make([]*BindingDeclaration, 0),
		publishers:    make([]*PublisherDeclaration, 0),
		consumerIndex: make(map[consumerKey]*ConsumerDeclaration),
		consumerOrder: make([]consumerKey, 0),
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

	if r.consumerIndex == nil {
		r.consumerIndex = make(map[consumerKey]*ConsumerDeclaration)
	}

	key := consumerKey{
		Queue:     declaration.Queue,
		Consumer:  declaration.Consumer,
		EventType: declaration.EventType,
	}

	// Defense-in-depth: warn and skip if duplicate detected during replay
	if _, exists := r.consumerIndex[key]; exists {
		r.logger.Warn().
			Str("queue", key.Queue).
			Str("consumer", key.Consumer).
			Str("event_type", key.EventType).
			Msg("duplicate consumer encountered during registry replay - skipping second registration")
		return
	}

	r.consumerIndex[key] = declaration
	r.consumerOrder = append(r.consumerOrder, key)

	r.logger.Debug().
		Str("queue", declaration.Queue).
		Str("consumer", declaration.Consumer).
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
	for _, key := range r.consumerOrder {
		consumer := r.consumerIndex[key]
		if consumer.Handler != nil {
			consumersWithHandlers++
		}
	}

	r.logger.Info().
		Int("total_consumers", len(r.consumerIndex)).
		Int("consumers_with_handlers", consumersWithHandlers).
		Msg("Starting message consumers")

	// Diagnostic: Detect duplicate queue consumers (warning only, not blocking)
	queueCounts := make(map[string]int)
	for _, key := range r.consumerOrder {
		queueCounts[key.Queue]++
	}
	for queue, count := range queueCounts {
		if count > 1 {
			r.logger.Warn().
				Str("queue", queue).
				Int("consumer_count", count).
				Msg("Multiple consumers registered for same queue - may indicate duplicate declarations")
		}
	}

	// Create cancellation context for all consumers
	consumerCtx, cancel := context.WithCancel(ctx)
	r.cancelConsumers = cancel

	// Start each consumer with a handler
	for _, key := range r.consumerOrder {
		consumer := r.consumerIndex[key]
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
		Queue:         consumer.Queue,
		Consumer:      consumer.Consumer,
		AutoAck:       consumer.AutoAck,
		Exclusive:     consumer.Exclusive,
		NoLocal:       consumer.NoLocal,
		NoWait:        consumer.NoWait,
		PrefetchCount: consumer.PrefetchCount,
	})
	if err != nil {
		return fmt.Errorf("failed to start consuming from queue %s: %w", consumer.Queue, err)
	}

	// Start goroutine to handle messages
	go r.handleMessages(ctx, consumer, deliveries)

	return nil
}

// handleMessages processes messages from a consumer and routes them to the handler.
// v0.17+: Spawns a worker pool for concurrent message processing.
func (r *Registry) handleMessages(ctx context.Context, consumer *ConsumerDeclaration, deliveries <-chan amqp.Delivery) {
	workers := consumer.Workers
	if workers <= 0 {
		workers = 1 // Fallback (should not happen with smart defaults)
	}

	log := r.logger.WithFields(map[string]any{
		"queue":      consumer.Queue,
		"consumer":   consumer.Consumer,
		"event_type": consumer.EventType,
		"workers":    workers,
		"prefetch":   consumer.PrefetchCount,
	})

	log.Info().Msg("Message handler started with worker pool")

	defer func() {
		log.Info().Msg("Message handler stopped")
	}()

	// Buffered jobs channel for work distribution (size = workers * 2 for backpressure)
	jobs := make(chan *amqp.Delivery, workers*2)

	// Start worker pool
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go r.worker(ctx, consumer, jobs, i, &wg)
	}

	// Main loop: feed jobs to worker pool
	func() {
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

				// Create local copy to avoid pointer capture bug (loop variable reuse)
				d := delivery
				// Send to worker pool (blocks if all workers busy)
				jobs <- &d
			}
		}
	}()

	// Shutdown: close jobs channel and wait for workers to finish
	close(jobs)
	wg.Wait()
	log.Info().Msg("All workers stopped gracefully")
}

// worker processes messages from the jobs channel concurrently.
// Each worker runs in its own goroutine and processes messages independently.
func (r *Registry) worker(ctx context.Context, consumer *ConsumerDeclaration, jobs <-chan *amqp.Delivery, workerID int, wg *sync.WaitGroup) {
	defer wg.Done()

	log := r.logger.WithFields(map[string]any{
		"queue":      consumer.Queue,
		"consumer":   consumer.Consumer,
		"event_type": consumer.EventType,
		"worker_id":  workerID,
	})

	log.Debug().Msg("Worker started")

	defer func() {
		log.Debug().Msg("Worker stopped")
	}()

	for {
		select {
		case <-ctx.Done():
			log.Debug().Msg("Worker context cancelled")
			return

		case delivery, ok := <-jobs:
			if !ok {
				log.Debug().Msg("Jobs channel closed, worker exiting")
				return
			}

			r.processMessage(ctx, consumer, delivery, log)
		}
	}
}

// processMessage processes a single message using the consumer's handler.
func (r *Registry) processMessage(ctx context.Context, consumer *ConsumerDeclaration, delivery *amqp.Delivery, log logger.Logger) {
	startTime := time.Now()

	// Extract trace context using centralized trace package
	accessor := &amqpDeliveryAccessor{headers: delivery.Headers}
	msgCtx := gobrickstrace.ExtractFromHeaders(ctx, accessor)
	contextLog := log.WithContext(msgCtx)
	traceID := gobrickstrace.EnsureTraceID(msgCtx)
	tlog := contextLog.WithFields(map[string]any{"correlation_id": traceID})

	// Panic recovery: prevents handler panics from crashing the entire service.
	// This follows the same pattern as HTTP middleware panic recovery.
	// Panics are treated like errors: logged with stack trace, nacked without requeue, and metrics recorded.
	defer func() {
		if recovered := recover(); recovered != nil {
			r.handlePanicRecovery(msgCtx, consumer, delivery, startTime, tlog, recovered)
		}
	}()

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
		// Enhanced structured logging for failed messages
		r.buildFailureLogEvent(tlog, delivery, consumer, processingTime).
			Err(err).
			Msg("Message processing failed - discarding without requeue")

		// Record failed message metrics (duration with error.type attribute)
		tracking.RecordAMQPConsumeMetrics(msgCtx, delivery, consumer.Queue, processingTime, err)

		// Negative acknowledgment WITHOUT requeue - prevents infinite retry loops
		// Failed messages are dropped (logged above) until DLQ support is implemented
		r.nackMessage(delivery, consumer.AutoAck, tlog)
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

// nackMessage negatively acknowledges a message without requeue.
// Logs any nack errors but does not propagate them (robustness over strict error handling).
func (r *Registry) nackMessage(delivery *amqp.Delivery, autoAck bool, log logger.Logger) {
	if autoAck {
		return // No manual ack/nack needed
	}
	if err := delivery.Nack(false, false); err != nil {
		log.Error().
			Err(err).
			Uint64("delivery_tag", delivery.DeliveryTag).
			Msg("Failed to nack message")
	}
}

// buildFailureLogEvent creates a structured log event for failed message processing.
// Provides consistent error logging across panic and error paths.
func (r *Registry) buildFailureLogEvent(
	log logger.Logger,
	delivery *amqp.Delivery,
	consumer *ConsumerDeclaration,
	processingTime time.Duration,
) logger.LogEvent {
	return log.Error().
		Str("message_id", delivery.MessageId).
		Str("queue", consumer.Queue).
		Str("event_type", consumer.EventType).
		Str("correlation_id", delivery.CorrelationId).
		Str("consumer_tag", delivery.ConsumerTag).
		Str("routing_key", delivery.RoutingKey).
		Str("exchange", delivery.Exchange).
		Dur("processing_time", processingTime)
}

// handlePanicRecovery handles panic recovery for message processing.
// Logs panic with stack trace, records metrics, and nacks message without requeue.
func (r *Registry) handlePanicRecovery(
	msgCtx context.Context,
	consumer *ConsumerDeclaration,
	delivery *amqp.Delivery,
	startTime time.Time,
	log logger.Logger,
	recovered any,
) {
	processingTime := time.Since(startTime)
	stack := debug.Stack()

	// Log panic with full context and stack trace
	r.buildFailureLogEvent(log, delivery, consumer, processingTime).
		Interface("panic", recovered).
		Bytes("stack", stack).
		Msg("Panic recovered in message handler - discarding without requeue")

	// Record failed message metrics (inline recordFailedMessage)
	panicErr := fmt.Errorf("panic in message handler: %v", recovered)
	tracking.RecordAMQPConsumeMetrics(msgCtx, delivery, consumer.Queue, processingTime, panicErr)

	// Nack without requeue
	r.nackMessage(delivery, consumer.AutoAck, log)
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

// Publishers returns all registered publishers (for documentation/monitoring)
func (r *Registry) Publishers() []*PublisherDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	publishers := make([]*PublisherDeclaration, len(r.publishers))
	copy(publishers, r.publishers)
	return publishers
}

// Consumers returns all registered consumers (for documentation/monitoring)
func (r *Registry) Consumers() []*ConsumerDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	consumers := make([]*ConsumerDeclaration, 0, len(r.consumerOrder))
	for _, key := range r.consumerOrder {
		consumers = append(consumers, r.consumerIndex[key])
	}
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

	for _, key := range r.consumerOrder {
		cons := r.consumerIndex[key]
		if cons.Queue == queue {
			return true
		}
	}
	return false
}

// Exchanges returns all registered exchanges (for testing/monitoring)
func (r *Registry) Exchanges() map[string]*ExchangeDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	exchanges := make(map[string]*ExchangeDeclaration, len(r.exchanges))
	maps.Copy(exchanges, r.exchanges)
	return exchanges
}

// Queues returns all registered queues (for testing/monitoring)
func (r *Registry) Queues() map[string]*QueueDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	queues := make(map[string]*QueueDeclaration, len(r.queues))
	maps.Copy(queues, r.queues)
	return queues
}

// Bindings returns all registered bindings (for testing/monitoring)
func (r *Registry) Bindings() []*BindingDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bindings := make([]*BindingDeclaration, len(r.bindings))
	copy(bindings, r.bindings)
	return bindings
}
