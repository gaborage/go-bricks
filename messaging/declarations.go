package messaging

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"maps"
	"math"
	"slices"
)

// consumerKey uniquely identifies a consumer declaration.
// The combination of Queue + Consumer tag + EventType must be unique across all registrations.
type consumerKey struct {
	Queue     string
	Consumer  string
	EventType string
}

// Declarations stores messaging infrastructure declarations made by modules at startup.
// This is a pure data structure (no client dependencies) that can be validated once
// and replayed to multiple per-tenant registries.
type Declarations struct {
	Exchanges     map[string]*ExchangeDeclaration
	Queues        map[string]*QueueDeclaration
	Bindings      []*BindingDeclaration
	Publishers    []*PublisherDeclaration
	consumerIndex map[consumerKey]*ConsumerDeclaration // Deduplication + O(1) lookup
	consumerOrder []consumerKey                        // Deterministic iteration order
}

// NewDeclarations creates a new empty declarations store.
func NewDeclarations() *Declarations {
	return &Declarations{
		Exchanges:     make(map[string]*ExchangeDeclaration),
		Queues:        make(map[string]*QueueDeclaration),
		Bindings:      make([]*BindingDeclaration, 0),
		Publishers:    make([]*PublisherDeclaration, 0),
		consumerIndex: make(map[consumerKey]*ConsumerDeclaration),
		consumerOrder: make([]consumerKey, 0),
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
// Panics if a consumer with the same queue+consumer+event_type already exists.
func (d *Declarations) RegisterConsumer(c *ConsumerDeclaration) {
	if c == nil {
		return
	}

	key := consumerKey{
		Queue:     c.Queue,
		Consumer:  c.Consumer,
		EventType: c.EventType,
	}

	// Deduplication: panic if duplicate registration detected
	if d.consumerIndex == nil {
		d.consumerIndex = make(map[consumerKey]*ConsumerDeclaration)
	}

	if _, exists := d.consumerIndex[key]; exists {
		panic(fmt.Sprintf(
			"messaging: duplicate consumer declaration detected\n"+
				"  queue=%s consumer=%s event_type=%s\n"+
				"  Ensure each DeclareConsumer call is unique within DeclareMessaging",
			c.Queue, c.Consumer, c.EventType,
		))
	}

	// Deep copy to prevent shared mutable state
	decl := &ConsumerDeclaration{
		Queue:         c.Queue,
		Consumer:      c.Consumer,
		AutoAck:       c.AutoAck,
		Exclusive:     c.Exclusive,
		NoLocal:       c.NoLocal,
		NoWait:        c.NoWait,
		EventType:     c.EventType,
		Description:   c.Description,
		Handler:       c.Handler, // Handlers are typically stateless, so no deep copy needed
		Workers:       c.Workers,
		PrefetchCount: c.PrefetchCount,
	}

	d.consumerIndex[key] = decl
	d.consumerOrder = append(d.consumerOrder, key)
}

// Consumers returns all consumer declarations in registration order.
// Used internally by ReplayToRegistry and for observability/metrics.
func (d *Declarations) Consumers() []*ConsumerDeclaration {
	result := make([]*ConsumerDeclaration, 0, len(d.consumerOrder))
	for _, key := range d.consumerOrder {
		result = append(result, d.consumerIndex[key])
	}
	return result
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
	for _, consumer := range d.Consumers() {
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
	for _, consumer := range d.Consumers() {
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
		Consumers:  len(d.consumerIndex),
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

	// Clone consumers (preserve registration order)
	for _, key := range d.consumerOrder {
		consumer := d.consumerIndex[key]
		cloneConsumer := &ConsumerDeclaration{
			Queue:         consumer.Queue,
			Consumer:      consumer.Consumer,
			AutoAck:       consumer.AutoAck,
			Exclusive:     consumer.Exclusive,
			NoLocal:       consumer.NoLocal,
			NoWait:        consumer.NoWait,
			EventType:     consumer.EventType,
			Description:   consumer.Description,
			Handler:       consumer.Handler, // Handlers are stateless, so shallow copy is fine
			Workers:       consumer.Workers,
			PrefetchCount: consumer.PrefetchCount,
		}
		cloneKey := consumerKey{
			Queue:     consumer.Queue,
			Consumer:  consumer.Consumer,
			EventType: consumer.EventType,
		}
		clone.consumerIndex[cloneKey] = cloneConsumer
		clone.consumerOrder = append(clone.consumerOrder, cloneKey)
	}

	return clone
}

// Hash generates a deterministic hash of all declarations.
// This is used by Manager to detect duplicate replay attempts (idempotency).
// The hash is stable across multiple calls and does not include handler functions.
func (d *Declarations) Hash() uint64 {
	h := fnv.New64a()

	// Hash exchanges (deterministic via sorted keys)
	exchangeKeys := slices.Sorted(maps.Keys(d.Exchanges))
	for _, name := range exchangeKeys {
		ex := d.Exchanges[name]
		h.Write([]byte(ex.Name))
		h.Write([]byte(ex.Type))
		writeBool(h, ex.Durable)
		writeBool(h, ex.AutoDelete)
		writeBool(h, ex.Internal)
		writeBool(h, ex.NoWait)
		writeMapArgs(h, ex.Args)
	}

	// Hash queues (deterministic via sorted keys)
	queueKeys := slices.Sorted(maps.Keys(d.Queues))
	for _, name := range queueKeys {
		q := d.Queues[name]
		h.Write([]byte(q.Name))
		writeBool(h, q.Durable)
		writeBool(h, q.AutoDelete)
		writeBool(h, q.Exclusive)
		writeBool(h, q.NoWait)
		writeMapArgs(h, q.Args)
	}

	// Hash bindings (already in insertion order)
	for _, b := range d.Bindings {
		h.Write([]byte(b.Queue))
		h.Write([]byte(b.Exchange))
		h.Write([]byte(b.RoutingKey))
		writeBool(h, b.NoWait)
		writeMapArgs(h, b.Args)
	}

	// Hash publishers (already in insertion order)
	for _, p := range d.Publishers {
		h.Write([]byte(p.Exchange))
		h.Write([]byte(p.RoutingKey))
		h.Write([]byte(p.EventType))
		h.Write([]byte(p.Description))
		writeBool(h, p.Mandatory)
		writeBool(h, p.Immediate)
		writeMapArgs(h, p.Headers)
	}

	// Hash consumers (use consumerOrder for deterministic iteration)
	for _, key := range d.consumerOrder {
		c := d.consumerIndex[key]
		h.Write([]byte(c.Queue))
		h.Write([]byte(c.Consumer))
		h.Write([]byte(c.EventType))
		h.Write([]byte(c.Description))
		writeBool(h, c.AutoAck)
		writeBool(h, c.Exclusive)
		writeBool(h, c.NoLocal)
		writeBool(h, c.NoWait)
		// NOTE: Handler functions are NOT hashable (intentional)
	}

	return h.Sum64()
}

// writeBool writes a boolean value to the hash as a single byte
func writeBool(h hash.Hash, value bool) {
	var b byte
	if value {
		b = 1
	}
	h.Write([]byte{b})
}

// writeMapArgs writes a map[string]any to the hash in deterministic order
func writeMapArgs(h hash.Hash, args map[string]any) {
	if len(args) == 0 {
		return
	}

	// Sort keys for deterministic hashing
	keys := slices.Sorted(maps.Keys(args))
	for _, key := range keys {
		h.Write([]byte(key))
		// Hash the value based on its type
		switch v := args[key].(type) {
		case string:
			h.Write([]byte(v))
		case int:
			writeInt64(h, int64(v))
		case int64:
			writeInt64(h, v)
		case bool:
			writeBool(h, v)
		case float64:
			writeFloat64(h, v)
		default:
			// For other types, use fmt.Fprintf (not perfect but stable)
			fmt.Fprintf(h, "%v", v)
		}
	}
}

// writeInt64 writes an int64 to the hash
func writeInt64(h hash.Hash, value int64) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(value)) //nolint:gosec // G115: Intentional int64->uint64 for hashing
	h.Write(buf)
}

// writeFloat64 writes a float64 to the hash
func writeFloat64(h hash.Hash, value float64) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, math.Float64bits(value))
	h.Write(buf)
}
