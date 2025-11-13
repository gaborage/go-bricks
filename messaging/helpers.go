package messaging

import "runtime"

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
		Type:       "topic",
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
// If queue is non-nil and not already registered, it will be automatically registered.
// This hybrid approach allows consumers to optionally declare their dependencies.
//
// Usage:
//   - Pass nil if queue is already registered separately
//   - Pass queue declaration to auto-register (convenience for simple cases)
func (d *Declarations) DeclareConsumer(opts *ConsumerOptions, queue *QueueDeclaration) *ConsumerDeclaration {
	// Auto-register queue if provided and not already registered
	if queue != nil {
		if _, exists := d.Queues[queue.Name]; !exists {
			d.RegisterQueue(queue)
		}
	}

	// Apply smart defaults for concurrency (v0.17+)
	// Workers: Default to NumCPU * 4 for I/O-bound workloads (database, HTTP, etc.)
	if opts.Workers == 0 {
		opts.Workers = runtime.NumCPU() * 4
	}

	// Resource safeguard: Cap workers at 200 per consumer
	if opts.Workers > 200 {
		opts.Workers = 200
	}

	// PrefetchCount: Default to Workers * 10 for optimal pipeline, capped at 500
	if opts.PrefetchCount == 0 {
		opts.PrefetchCount = min(opts.Workers*10, 500)
	}

	// Resource safeguard: Cap prefetch at 1000 to prevent memory exhaustion
	if opts.PrefetchCount > 1000 {
		opts.PrefetchCount = 1000
	}

	consumer := NewConsumer(opts)
	d.RegisterConsumer(consumer)
	return consumer
}
