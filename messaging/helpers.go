package messaging

import (
	"fmt"
	"runtime"
	"time"
)

const (
	exchangeTypeTopic  = "topic"
	exchangeTypeFanout = "fanout"
)

// NewTopicExchange creates a topic exchange with production-safe defaults.
// Topic exchanges route messages based on routing key patterns (e.g., "order.*", "user.#").
//
// Production defaults:
//   - Durable: true (survives broker restart)
//   - AutoDelete: false (won't delete when unused)
//   - Internal: false (can be published to directly)
//   - NoWait: false (waits for broker confirmation)
func NewTopicExchange(name string) *ExchangeDeclaration {
	return &ExchangeDeclaration{
		Name:       name,
		Type:       exchangeTypeTopic,
		Durable:    true,
		AutoDelete: false,
		Internal:   false,
		NoWait:     false,
		Args:       make(map[string]any),
	}
}

// NewQueue creates a queue with production-safe defaults.
//
// Production defaults:
//   - Durable: true (survives broker restart)
//   - AutoDelete: false (won't delete when consumers disconnect)
//   - Exclusive: false (can be accessed by multiple connections)
//   - NoWait: false (waits for broker confirmation)
func NewQueue(name string) *QueueDeclaration {
	return &QueueDeclaration{
		Name:       name,
		Durable:    true,
		AutoDelete: false,
		Exclusive:  false,
		NoWait:     false,
		Args:       make(map[string]any),
	}
}

// NewBinding creates a binding declaration between a queue and exchange.
//
// Parameters:
//   - queue: Queue name to bind
//   - exchange: Exchange name to bind to
//   - routingKey: Routing key pattern (e.g., "order.*", "user.created")
func NewBinding(queue, exchange, routingKey string) *BindingDeclaration {
	return &BindingDeclaration{
		Queue:      queue,
		Exchange:   exchange,
		RoutingKey: routingKey,
		NoWait:     false,
		Args:       make(map[string]any),
	}
}

// PublisherOptions contains configuration for creating a publisher declaration.
type PublisherOptions struct {
	Exchange    string         // Target exchange name
	RoutingKey  string         // Routing key for messages
	EventType   string         // Event type identifier
	Description string         // Human-readable description
	Headers     map[string]any // Default headers (optional)
	Mandatory   bool           // Message must be routed to a queue (default: false)
	Immediate   bool           // Message must be delivered immediately (default: false)
}

// NewPublisher creates a publisher declaration from options.
// If Headers is nil, an empty map is created.
func NewPublisher(opts *PublisherOptions) *PublisherDeclaration {
	headers := opts.Headers
	if headers == nil {
		headers = make(map[string]any)
	}

	return &PublisherDeclaration{
		Exchange:    opts.Exchange,
		RoutingKey:  opts.RoutingKey,
		EventType:   opts.EventType,
		Description: opts.Description,
		Mandatory:   opts.Mandatory,
		Immediate:   opts.Immediate,
		Headers:     headers,
	}
}

// ConsumerOptions contains configuration for creating a consumer declaration.
type ConsumerOptions struct {
	Queue         string         // Queue name to consume from
	Consumer      string         // Consumer tag
	EventType     string         // Event type identifier
	Description   string         // Human-readable description
	Handler       MessageHandler // Message handler (optional for documentation-only declarations)
	AutoAck       bool           // Automatically acknowledge messages (default: false)
	Exclusive     bool           // Exclusive consumer (default: false)
	NoLocal       bool           // Don't deliver to the connection that published (default: false)
	Workers       int            // Number of concurrent workers (0 = auto-scale to NumCPU*4, >0 = explicit)
	PrefetchCount int            // RabbitMQ prefetch count (0 = auto-scale to Workers*10, capped at 500)
	Args          map[string]any // Per-consumer arguments forwarded to basic.consume (x-stream-offset, x-priority, ...)
}

// NewConsumer creates a consumer declaration from options.
func NewConsumer(opts *ConsumerOptions) *ConsumerDeclaration {
	return &ConsumerDeclaration{
		Queue:         opts.Queue,
		Consumer:      opts.Consumer,
		AutoAck:       opts.AutoAck,
		Exclusive:     opts.Exclusive,
		NoLocal:       opts.NoLocal,
		NoWait:        false,
		EventType:     opts.EventType,
		Description:   opts.Description,
		Handler:       opts.Handler,
		Workers:       opts.Workers,
		PrefetchCount: opts.PrefetchCount,
		Args:          opts.Args,
	}
}

// DeclareTopicExchange creates and registers a topic exchange in one step.
// Returns the created exchange declaration for reference.
func (d *Declarations) DeclareTopicExchange(name string) *ExchangeDeclaration {
	exchange := NewTopicExchange(name)
	d.RegisterExchange(exchange)
	return exchange
}

// DeclareQueue creates and registers a queue in one step.
// Returns the created queue declaration for reference.
func (d *Declarations) DeclareQueue(name string) *QueueDeclaration {
	queue := NewQueue(name)
	d.RegisterQueue(queue)
	return queue
}

// DeclareBinding creates and registers a binding in one step.
// Returns the created binding declaration for reference.
func (d *Declarations) DeclareBinding(queue, exchange, routingKey string) *BindingDeclaration {
	binding := NewBinding(queue, exchange, routingKey)
	d.RegisterBinding(binding)
	return binding
}

// DeadLetterSpec configures the declarative dead-letter opt-in for a queue.
// Zero-value fields get derived defaults; see DeclareQueueWithDLQ.
type DeadLetterSpec struct {
	// Exchange is the dead-letter exchange name. Empty derives "<queue>.dlx".
	// The exchange is declared as a durable fanout so the parking queue
	// receives every dead-lettered message regardless of routing key.
	Exchange string

	// ParkingQueue is the queue that collects dead-lettered messages.
	// Empty derives "<queue>.dlq". Declared with the production defaults of
	// NewQueue (durable, non-exclusive).
	ParkingQueue string

	// RoutingKey, when non-empty, is set as x-dead-letter-routing-key so
	// dead-lettered messages are re-published with it instead of their
	// original routing key. Rarely needed with the fanout DLX default.
	RoutingKey string
}

// DeclareQueueWithDLQ declares a queue whose failed deliveries are parked
// instead of dropped: the framework's nack-without-requeue on handler error
// (see wiki/messaging.md) dead-letters into the spec's exchange, and the
// parking queue bound to it retains the message with the x-death header.
// Lowers to ordinary exchange/queue/binding declarations plus queue Args, so
// per-tenant replay, validation, and topology hashing behave as if declared
// by hand. Returns the primary queue declaration.
func (d *Declarations) DeclareQueueWithDLQ(name string, dl *DeadLetterSpec) *QueueDeclaration {
	if dl == nil {
		dl = &DeadLetterSpec{}
	}
	dlx := dl.Exchange
	if dlx == "" {
		dlx = name + ".dlx"
	}
	parking := dl.ParkingQueue
	if parking == "" {
		parking = name + ".dlq"
	}

	d.RegisterExchange(&ExchangeDeclaration{
		Name:    dlx,
		Type:    exchangeTypeFanout,
		Durable: true,
		Args:    make(map[string]any),
	})
	d.RegisterQueue(NewQueue(parking))
	// Register the parking binding once per DLX. Exchange/queue registration is
	// map-backed (idempotent by name), but Bindings is a slice: several primary
	// queues sharing one DLX (via DeadLetterSpec.Exchange/ParkingQueue) would
	// otherwise append duplicate parking->dlx bindings, making Hash() depend on
	// the queue count and issuing redundant BindQueue calls.
	if !d.hasParkingBinding(parking, dlx) {
		d.RegisterBinding(NewBinding(parking, dlx, ""))
	}

	queue := NewQueue(name)
	queue.Args[argDeadLetterExchange] = dlx
	if dl.RoutingKey != "" {
		queue.Args[argDeadLetterRoutingKey] = dl.RoutingKey
	}
	d.RegisterQueue(queue)
	return queue
}

// hasParkingBinding reports whether a parking->dlx binding (routing key "") is
// already registered, so DeclareQueueWithDLQ does not append a duplicate when
// several primary queues share one dead-letter exchange.
func (d *Declarations) hasParkingBinding(parking, dlx string) bool {
	for _, b := range d.Bindings {
		if b.Queue == parking && b.Exchange == dlx && b.RoutingKey == "" {
			return true
		}
	}
	return false
}

// StreamQueueSpec configures retention for a stream queue. Zero-value fields
// are omitted (broker defaults apply). MaxAge is rendered as whole seconds.
type StreamQueueSpec struct {
	// MaxAge -> x-max-age ("<n>s"), truncated to whole seconds. A non-zero
	// sub-second value floors to "1s": second granularity is RabbitMQ's, so
	// anything briefer is inexpressible and would otherwise render "0s",
	// discarding the retention the caller asked for.
	MaxAge              time.Duration
	MaxLengthBytes      int64 // -> x-max-length-bytes
	MaxSegmentSizeBytes int64 // -> x-stream-max-segment-size-bytes
}

// DeclareStreamQueue declares a RabbitMQ stream queue (x-queue-type: stream):
// an append-only replicated log read non-destructively at a client-chosen
// offset, instead of a classic queue's destructive consume. Consumers pick a
// start position with the x-stream-offset consumer Arg (see
// wiki/messaging.md). A nil spec declares the queue with broker-default
// retention. Returns the registered queue declaration.
func (d *Declarations) DeclareStreamQueue(name string, spec *StreamQueueSpec) *QueueDeclaration {
	queue := NewQueue(name)
	queue.Args[argQueueType] = queueTypeStream

	if spec != nil {
		if spec.MaxAge > 0 {
			queue.Args[argMaxAge] = fmt.Sprintf("%ds", max(int64(spec.MaxAge/time.Second), 1))
		}
		if spec.MaxLengthBytes > 0 {
			queue.Args[argMaxLengthBytes] = spec.MaxLengthBytes
		}
		if spec.MaxSegmentSizeBytes > 0 {
			queue.Args[argMaxSegmentSizeBytes] = spec.MaxSegmentSizeBytes
		}
	}

	d.RegisterQueue(queue)
	return queue
}

// DeclarePublisher creates and registers a publisher in one step.
//
// If exchange is non-nil and not already registered, it will be automatically registered.
// This hybrid approach allows publishers to optionally declare their dependencies.
//
// Usage:
//   - Pass nil if exchange is already registered separately
//   - Pass exchange declaration to auto-register (convenience for simple cases)
func (d *Declarations) DeclarePublisher(opts *PublisherOptions, exchange *ExchangeDeclaration) *PublisherDeclaration {
	// Auto-register exchange if provided and not already registered
	if exchange != nil {
		if _, exists := d.Exchanges[exchange.Name]; !exists {
			d.RegisterExchange(exchange)
		}
	}

	publisher := NewPublisher(opts)
	d.RegisterPublisher(publisher)
	return publisher
}

// DeclareConsumer creates and registers a consumer in one step.
//
// A non-nil queue is registered, merging with any existing declaration of the
// same name; an incompatible shape keeps the incumbent and becomes a startup
// conflict (see RegisterQueue). This hybrid approach allows consumers to
// optionally declare their dependencies.
//
// Usage:
//   - Pass nil if queue is already registered separately
//   - Pass queue declaration to auto-register (convenience for simple cases)
func (d *Declarations) DeclareConsumer(opts *ConsumerOptions, queue *QueueDeclaration) *ConsumerDeclaration {
	if queue != nil {
		d.RegisterQueue(queue)
	}

	// Apply smart defaults for concurrency (v0.17+)
	// Workers: Default to NumCPU * 4 for I/O-bound workloads (database, HTTP, etc.)
	if opts.Workers == 0 {
		opts.Workers = runtime.NumCPU() * 4
	}

	// Resource safeguard: Cap workers per consumer
	if opts.Workers > maxWorkersPerConsumer {
		opts.Workers = maxWorkersPerConsumer
	}

	// PrefetchCount: Default to Workers * multiplier for optimal pipeline, capped at maxDefaultPrefetch
	if opts.PrefetchCount == 0 {
		opts.PrefetchCount = min(opts.Workers*defaultPrefetchMultiplier, maxDefaultPrefetch)
	}

	// Resource safeguard: Cap prefetch to prevent memory exhaustion
	if opts.PrefetchCount > maxPrefetchCount {
		opts.PrefetchCount = maxPrefetchCount
	}

	consumer := NewConsumer(opts)
	d.RegisterConsumer(consumer)
	return consumer
}
