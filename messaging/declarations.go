package messaging

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"maps"
	"math"
	"reflect"
	"slices"
)

// consumerKey uniquely identifies a consumer declaration.
// The combination of Queue + Consumer tag + EventType must be unique across all registrations.
type consumerKey struct {
	Queue     string
	Consumer  string
	EventType string
}

// queueConflict records a queue name declared twice with shapes that cannot merge.
type queueConflict struct {
	Queue    string
	Field    string // flag name, or `Args["<key>"]`
	Kept     string // the incumbent's value, which stays in effect
	Rejected string
}

// Declarations stores messaging infrastructure declarations made by modules at startup.
// This is a pure data structure (no client dependencies) that can be validated once
// and replayed to multiple per-tenant registries.
type Declarations struct {
	Exchanges      map[string]*ExchangeDeclaration
	Queues         map[string]*QueueDeclaration
	Bindings       []*BindingDeclaration
	Publishers     []*PublisherDeclaration
	consumerIndex  map[consumerKey]*ConsumerDeclaration // Deduplication + O(1) lookup
	consumerOrder  []consumerKey                        // Deterministic iteration order
	queueConflicts []queueConflict                      // Incompatible queue re-declarations, reported by Validate
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
//
// Re-declaring a name keeps the last declaration, unlike RegisterQueue: no
// framework helper builds a divergent exchange shape for a name a caller would
// also declare by hand. The one to watch is DeclareQueueWithDLQ's fanout DLX,
// which several primary queues may share — today every repeat is identical.
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
//
// Re-declaring a name merges when the shapes are compatible, so
// DeclareQueueWithDLQ and DeclareQueue on one name compose instead of whichever
// ran last silently dropping the other's dead-letter args. An incompatible
// re-declaration keeps the incumbent and records a conflict that Validate
// reports, because declaration order across modules is invisible at any single
// call site. That report prints the contested Args values verbatim into a
// startup error, which the logger's key-based filter cannot mask — queue Args
// are broker topology and must not carry secrets.
func (d *Declarations) RegisterQueue(q *QueueDeclaration) {
	if q == nil {
		return
	}

	if incumbent, exists := d.Queues[q.Name]; exists {
		if conflict, incompatible := queueMergeConflict(incumbent, q); incompatible {
			d.recordQueueConflict(conflict)
			return
		}
		// The incumbent is already our own deep copy, so merging in place
		// cannot reach a caller's map.
		maps.Copy(incumbent.Args, q.Args)
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

// queueMergeConflict reports the first difference that blocks merging two
// declarations of one queue name: any differing flag, or one Args key carrying
// two values. Args keys are visited in sorted order so the recorded conflict —
// and therefore the startup error — is identical across runs.
func queueMergeConflict(incumbent, next *QueueDeclaration) (conflict queueConflict, incompatible bool) {
	switch {
	case incumbent.Durable != next.Durable:
		return newQueueConflict(incumbent.Name, "Durable", incumbent.Durable, next.Durable), true
	case incumbent.AutoDelete != next.AutoDelete:
		return newQueueConflict(incumbent.Name, "AutoDelete", incumbent.AutoDelete, next.AutoDelete), true
	case incumbent.Exclusive != next.Exclusive:
		return newQueueConflict(incumbent.Name, "Exclusive", incumbent.Exclusive, next.Exclusive), true
	case incumbent.NoWait != next.NoWait:
		return newQueueConflict(incumbent.Name, "NoWait", incumbent.NoWait, next.NoWait), true
	}

	for _, key := range slices.Sorted(maps.Keys(next.Args)) {
		kept, shared := incumbent.Args[key]
		rejected := next.Args[key]
		// reflect.DeepEqual, not ==: Args values are `any`, and == panics on an
		// uncomparable dynamic type such as a slice or an amqp.Table.
		if !shared || reflect.DeepEqual(kept, rejected) {
			continue
		}
		return newQueueConflict(incumbent.Name, fmt.Sprintf("Args[%q]", key), kept, rejected), true
	}

	return queueConflict{}, false
}

func newQueueConflict(queue, field string, kept, rejected any) queueConflict {
	return queueConflict{
		Queue:    queue,
		Field:    field,
		Kept:     fmt.Sprintf("%v", kept),
		Rejected: fmt.Sprintf("%v", rejected),
	}
}

// recordQueueConflict drops repeats of a disagreement already recorded, so the
// aggregate error counts distinct problems rather than rejected declarations.
// queueConflict is all-string, hence comparable.
func (d *Declarations) recordQueueConflict(c queueConflict) {
	if slices.Contains(d.queueConflicts, c) {
		return
	}
	d.queueConflicts = append(d.queueConflicts, c)
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
	if err := d.validateQueueConflicts(); err != nil {
		return err
	}

	// Check that all binding queues and exchanges exist
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

// validateQueueConflicts aggregates every incompatible queue re-declaration into
// one error, so an app with several conflicts sees them all in a single boot.
func (d *Declarations) validateQueueConflicts() error {
	if len(d.queueConflicts) == 0 {
		return nil
	}

	errs := []error{fmt.Errorf(
		"conflicting queue declarations (%d conflict(s)) — declarations merge only when compatible; align the call sites (DeclareQueueWithDLQ and DeclareQueue on one name must agree)",
		len(d.queueConflicts))}
	for _, c := range d.queueConflicts {
		errs = append(errs, fmt.Errorf("queue %q: %s kept %q vs rejected %q", c.Queue, c.Field, c.Kept, c.Rejected))
	}
	return errors.Join(errs...)
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

// DeclarationStats holds counts of each declaration type.
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

// IsEmpty reports whether no declarations have been registered.
func (d *Declarations) IsEmpty() bool {
	s := d.Stats()
	return s.Exchanges == 0 && s.Queues == 0 && s.Bindings == 0 &&
		s.Publishers == 0 && s.Consumers == 0
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

	// A clone that passed a validation its source failed would be a trap.
	clone.queueConflicts = slices.Clone(d.queueConflicts)

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
		writeString(h, ex.Name)
		writeString(h, ex.Type)
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
		writeString(h, q.Name)
		writeBool(h, q.Durable)
		writeBool(h, q.AutoDelete)
		writeBool(h, q.Exclusive)
		writeBool(h, q.NoWait)
		writeMapArgs(h, q.Args)
	}

	// Hash bindings (already in insertion order)
	for _, b := range d.Bindings {
		writeString(h, b.Queue)
		writeString(h, b.Exchange)
		writeString(h, b.RoutingKey)
		writeBool(h, b.NoWait)
		writeMapArgs(h, b.Args)
	}

	// Hash publishers (already in insertion order)
	for _, p := range d.Publishers {
		writeString(h, p.Exchange)
		writeString(h, p.RoutingKey)
		writeString(h, p.EventType)
		writeString(h, p.Description)
		writeBool(h, p.Mandatory)
		writeBool(h, p.Immediate)
		writeMapArgs(h, p.Headers)
	}

	// Hash consumers (use consumerOrder for deterministic iteration)
	for _, key := range d.consumerOrder {
		c := d.consumerIndex[key]
		writeString(h, c.Queue)
		writeString(h, c.Consumer)
		writeString(h, c.EventType)
		writeString(h, c.Description)
		writeBool(h, c.AutoAck)
		writeBool(h, c.Exclusive)
		writeBool(h, c.NoLocal)
		writeBool(h, c.NoWait)
		// NOTE: Handler functions are NOT hashable (intentional)
	}

	return h.Sum64()
}

// writeString writes s to the hash. hash.Hash.Write never returns an error.
func writeString(h hash.Hash, s string) {
	_, _ = h.Write([]byte(s))
}

// writeBool writes a boolean value to the hash as a single byte
func writeBool(h hash.Hash, value bool) {
	var b byte
	if value {
		b = 1
	}
	_, _ = h.Write([]byte{b})
}

// writeMapArgs writes a map[string]any to the hash in deterministic order
func writeMapArgs(h hash.Hash, args map[string]any) {
	if len(args) == 0 {
		return
	}

	// Sort keys for deterministic hashing
	keys := slices.Sorted(maps.Keys(args))
	for _, key := range keys {
		writeString(h, key)
		// Hash the value based on its type
		switch v := args[key].(type) {
		case string:
			writeString(h, v)
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
	binary.LittleEndian.PutUint64(buf, uint64(value)) //#nosec G115 -- intentional bit reinterpretation for hash
	_, _ = h.Write(buf)
}

// writeFloat64 writes a float64 to the hash
func writeFloat64(h hash.Hash, value float64) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, math.Float64bits(value))
	_, _ = h.Write(buf)
}
