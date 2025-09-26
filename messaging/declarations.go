package messaging

import (
	"fmt"
	"maps"
)

// Declarations stores messaging infrastructure declarations made by modules at startup.
// This is a pure data structure (no client dependencies) that can be validated once
// and replayed to multiple per-tenant registries.
type Declarations struct {
	Exchanges  map[string]*ExchangeDeclaration
	Queues     map[string]*QueueDeclaration
	Bindings   []*BindingDeclaration
	Publishers []*PublisherDeclaration
	Consumers  []*ConsumerDeclaration
}

// NewDeclarations creates a new empty declarations store.
func NewDeclarations() *Declarations {
	return &Declarations{
		Exchanges:  make(map[string]*ExchangeDeclaration),
		Queues:     make(map[string]*QueueDeclaration),
		Bindings:   make([]*BindingDeclaration, 0),
		Publishers: make([]*PublisherDeclaration, 0),
		Consumers:  make([]*ConsumerDeclaration, 0),
	}
}

// RegisterExchange adds an exchange declaration to the store.
func (d *Declarations) RegisterExchange(e *ExchangeDeclaration) {
	if e == nil {
		return
	}

	// Deep copy to prevent shared mutable state
	decl := &ExchangeDeclaration{
		Name:       e.Name,
		Type:       e.Type,
		Durable:    e.Durable,
		AutoDelete: e.AutoDelete,
		Internal:   e.Internal,
		NoWait:     e.NoWait,
		Args:       make(map[string]any),
	}

	// Deep copy args map
	if e.Args != nil {
		maps.Copy(decl.Args, e.Args)
	}

	d.Exchanges[e.Name] = decl
}

// RegisterQueue adds a queue declaration to the store.
func (d *Declarations) RegisterQueue(q *QueueDeclaration) {
	if q == nil {
		return
	}

	// Deep copy to prevent shared mutable state
	decl := &QueueDeclaration{
		Name:       q.Name,
		Durable:    q.Durable,
		AutoDelete: q.AutoDelete,
		Exclusive:  q.Exclusive,
		NoWait:     q.NoWait,
		Args:       make(map[string]any),
	}

	// Deep copy args map
	if q.Args != nil {
		maps.Copy(decl.Args, q.Args)
	}

	d.Queues[q.Name] = decl
}

// RegisterBinding adds a binding declaration to the store.
func (d *Declarations) RegisterBinding(b *BindingDeclaration) {
	if b == nil {
		return
	}

	// Deep copy to prevent shared mutable state
	decl := &BindingDeclaration{
		Queue:      b.Queue,
		Exchange:   b.Exchange,
		RoutingKey: b.RoutingKey,
		NoWait:     b.NoWait,
		Args:       make(map[string]any),
	}

	// Deep copy args map
	if b.Args != nil {
		maps.Copy(decl.Args, b.Args)
	}

	d.Bindings = append(d.Bindings, decl)
}

// RegisterPublisher adds a publisher declaration to the store.
func (d *Declarations) RegisterPublisher(p *PublisherDeclaration) {
	if p == nil {
		return
	}

	// Deep copy to prevent shared mutable state
	decl := &PublisherDeclaration{
		Exchange:    p.Exchange,
		RoutingKey:  p.RoutingKey,
		EventType:   p.EventType,
		Description: p.Description,
		Mandatory:   p.Mandatory,
		Immediate:   p.Immediate,
		Headers:     make(map[string]any),
	}

	// Deep copy headers map
	if p.Headers != nil {
		maps.Copy(decl.Headers, p.Headers)
	}

	d.Publishers = append(d.Publishers, decl)
}

// RegisterConsumer adds a consumer declaration to the store.
func (d *Declarations) RegisterConsumer(c *ConsumerDeclaration) {
	if c == nil {
		return
	}

	// Deep copy to prevent shared mutable state
	decl := &ConsumerDeclaration{
		Queue:       c.Queue,
		Consumer:    c.Consumer,
		AutoAck:     c.AutoAck,
		Exclusive:   c.Exclusive,
		NoLocal:     c.NoLocal,
		NoWait:      c.NoWait,
		EventType:   c.EventType,
		Description: c.Description,
		Handler:     c.Handler, // Handlers are typically stateless, so no deep copy needed
	}

	d.Consumers = append(d.Consumers, decl)
}

// Validate checks the integrity of all declarations.
// It ensures that references between declarations are valid.
func (d *Declarations) Validate() error {
	// Check that all binding queues exist
	for _, binding := range d.Bindings {
		if _, exists := d.Queues[binding.Queue]; !exists {
			return fmt.Errorf("binding references non-existent queue: %s", binding.Queue)
		}
		if _, exists := d.Exchanges[binding.Exchange]; !exists {
			return fmt.Errorf("binding references non-existent exchange: %s", binding.Exchange)
		}
	}

	// Check that all consumer queues exist
	for _, consumer := range d.Consumers {
		if _, exists := d.Queues[consumer.Queue]; !exists {
			return fmt.Errorf("consumer references non-existent queue: %s", consumer.Queue)
		}
	}

	// Check that all publisher exchanges exist
	for _, publisher := range d.Publishers {
		if _, exists := d.Exchanges[publisher.Exchange]; !exists {
			return fmt.Errorf("publisher references non-existent exchange: %s", publisher.Exchange)
		}
	}

	return nil
}

// ReplayToRegistry applies all declarations to a runtime registry.
// The order is important: exchanges first, then queues, then bindings, then publishers/consumers.
func (d *Declarations) ReplayToRegistry(reg RegistryInterface) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}

	// Register exchanges first
	for _, exchange := range d.Exchanges {
		reg.RegisterExchange(exchange)
	}

	// Register queues
	for _, queue := range d.Queues {
		reg.RegisterQueue(queue)
	}

	// Register bindings (depend on exchanges and queues)
	for _, binding := range d.Bindings {
		reg.RegisterBinding(binding)
	}

	// Register publishers (depend on exchanges)
	for _, publisher := range d.Publishers {
		reg.RegisterPublisher(publisher)
	}

	// Register consumers (depend on queues)
	for _, consumer := range d.Consumers {
		reg.RegisterConsumer(consumer)
	}

	return nil
}

// Stats returns statistics about the declarations.
type DeclarationStats struct {
	Exchanges  int
	Queues     int
	Bindings   int
	Publishers int
	Consumers  int
}

// Stats returns counts of each declaration type.
func (d *Declarations) Stats() DeclarationStats {
	return DeclarationStats{
		Exchanges:  len(d.Exchanges),
		Queues:     len(d.Queues),
		Bindings:   len(d.Bindings),
		Publishers: len(d.Publishers),
		Consumers:  len(d.Consumers),
	}
}

// Clone creates a deep copy of the declarations.
// This is useful for creating per-tenant copies during replay.
func (d *Declarations) Clone() *Declarations {
	clone := NewDeclarations()

	// Clone exchanges
	for name, exchange := range d.Exchanges {
		clone.Exchanges[name] = &ExchangeDeclaration{
			Name:       exchange.Name,
			Type:       exchange.Type,
			Durable:    exchange.Durable,
			AutoDelete: exchange.AutoDelete,
			Internal:   exchange.Internal,
			NoWait:     exchange.NoWait,
			Args:       make(map[string]any),
		}
		if exchange.Args != nil {
			maps.Copy(clone.Exchanges[name].Args, exchange.Args)
		}
	}

	// Clone queues
	for name, queue := range d.Queues {
		clone.Queues[name] = &QueueDeclaration{
			Name:       queue.Name,
			Durable:    queue.Durable,
			AutoDelete: queue.AutoDelete,
			Exclusive:  queue.Exclusive,
			NoWait:     queue.NoWait,
			Args:       make(map[string]any),
		}
		if queue.Args != nil {
			maps.Copy(clone.Queues[name].Args, queue.Args)
		}
	}

	// Clone bindings
	for _, binding := range d.Bindings {
		cloneBinding := &BindingDeclaration{
			Queue:      binding.Queue,
			Exchange:   binding.Exchange,
			RoutingKey: binding.RoutingKey,
			NoWait:     binding.NoWait,
			Args:       make(map[string]any),
		}
		if binding.Args != nil {
			maps.Copy(cloneBinding.Args, binding.Args)
		}
		clone.Bindings = append(clone.Bindings, cloneBinding)
	}

	// Clone publishers
	for _, publisher := range d.Publishers {
		clonePublisher := &PublisherDeclaration{
			Exchange:    publisher.Exchange,
			RoutingKey:  publisher.RoutingKey,
			EventType:   publisher.EventType,
			Description: publisher.Description,
			Mandatory:   publisher.Mandatory,
			Immediate:   publisher.Immediate,
			Headers:     make(map[string]any),
		}
		if publisher.Headers != nil {
			maps.Copy(clonePublisher.Headers, publisher.Headers)
		}
		clone.Publishers = append(clone.Publishers, clonePublisher)
	}

	// Clone consumers
	for _, consumer := range d.Consumers {
		cloneConsumer := &ConsumerDeclaration{
			Queue:       consumer.Queue,
			Consumer:    consumer.Consumer,
			AutoAck:     consumer.AutoAck,
			Exclusive:   consumer.Exclusive,
			NoLocal:     consumer.NoLocal,
			NoWait:      consumer.NoWait,
			EventType:   consumer.EventType,
			Description: consumer.Description,
			Handler:     consumer.Handler, // Handlers are stateless, so shallow copy is fine
		}
		clone.Consumers = append(clone.Consumers, cloneConsumer)
	}

	return clone
}
