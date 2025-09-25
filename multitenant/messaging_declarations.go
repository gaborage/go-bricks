package multitenant

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// MessagingDeclarations captures all messaging infrastructure declarations
// from modules at startup time. This allows replaying the exact same
// declarations for each tenant's vhost without re-running module registration.
//
// This serves as a temporal bridge between compile-time intent (what modules
// declare they need) and runtime execution (per-tenant vhost infrastructure).
type MessagingDeclarations struct {
	// Exchanges declared by modules
	Exchanges map[string]*messaging.ExchangeDeclaration

	// Queues declared by modules
	Queues map[string]*messaging.QueueDeclaration

	// Bindings between queues and exchanges
	Bindings []*messaging.BindingDeclaration

	// Publishers declared by modules (for documentation/validation)
	Publishers []*messaging.PublisherDeclaration

	// Consumers declared by modules with their handlers
	Consumers []*messaging.ConsumerDeclaration
}

// Declarations is an alias for backward compatibility
type Declarations = MessagingDeclarations

// NewMessagingDeclarations creates an empty declarations container
func NewMessagingDeclarations() *MessagingDeclarations {
	return &MessagingDeclarations{
		Exchanges:  make(map[string]*messaging.ExchangeDeclaration),
		Queues:     make(map[string]*messaging.QueueDeclaration),
		Bindings:   make([]*messaging.BindingDeclaration, 0),
		Publishers: make([]*messaging.PublisherDeclaration, 0),
		Consumers:  make([]*messaging.ConsumerDeclaration, 0),
	}
}

// CaptureFromRegistry extracts all declarations from a populated registry.
// This is used after modules have called RegisterMessaging to capture their
// infrastructure requirements without actually declaring to a broker.
//
// The registry remains unchanged - we use its public getters to extract
// the declarations that were registered via the mock client.
func (d *MessagingDeclarations) CaptureFromRegistry(registry messaging.RegistryInterface) {
	// Extract exchanges - deep copy to avoid mutations
	for name, exchange := range registry.GetExchanges() {
		// Create a copy to ensure immutability
		exchangeCopy := &messaging.ExchangeDeclaration{
			Name:       exchange.Name,
			Type:       exchange.Type,
			Durable:    exchange.Durable,
			AutoDelete: exchange.AutoDelete,
			Internal:   exchange.Internal,
			NoWait:     exchange.NoWait,
			Args:       make(map[string]any),
		}
		// Deep copy args map
		if exchange.Args != nil {
			for k, v := range exchange.Args {
				exchangeCopy.Args[k] = v
			}
		}
		d.Exchanges[name] = exchangeCopy
	}

	// Extract queues - deep copy to avoid mutations
	for name, queue := range registry.GetQueues() {
		queueCopy := &messaging.QueueDeclaration{
			Name:       queue.Name,
			Durable:    queue.Durable,
			AutoDelete: queue.AutoDelete,
			Exclusive:  queue.Exclusive,
			NoWait:     queue.NoWait,
			Args:       make(map[string]any),
		}
		// Deep copy args map
		if queue.Args != nil {
			for k, v := range queue.Args {
				queueCopy.Args[k] = v
			}
		}
		d.Queues[name] = queueCopy
	}

	// Extract bindings - copy slice to avoid mutations
	bindings := registry.GetBindings()
	for _, binding := range bindings {
		bindingCopy := &messaging.BindingDeclaration{
			Queue:      binding.Queue,
			Exchange:   binding.Exchange,
			RoutingKey: binding.RoutingKey,
			NoWait:     binding.NoWait,
			Args:       make(map[string]any),
		}
		// Deep copy args map
		if binding.Args != nil {
			for k, v := range binding.Args {
				bindingCopy.Args[k] = v
			}
		}
		d.Bindings = append(d.Bindings, bindingCopy)
	}

	// Extract publishers - copy slice to avoid mutations
	publishers := registry.GetPublishers()
	for _, publisher := range publishers {
		publisherCopy := &messaging.PublisherDeclaration{
			Exchange:    publisher.Exchange,
			RoutingKey:  publisher.RoutingKey,
			EventType:   publisher.EventType,
			Description: publisher.Description,
			Mandatory:   publisher.Mandatory,
			Immediate:   publisher.Immediate,
			Headers:     make(map[string]any),
		}
		// Deep copy headers map
		if publisher.Headers != nil {
			for k, v := range publisher.Headers {
				publisherCopy.Headers[k] = v
			}
		}
		d.Publishers = append(d.Publishers, publisherCopy)
	}

	// Extract consumers - copy slice but preserve handler references
	consumers := registry.GetConsumers()
	for _, consumer := range consumers {
		consumerCopy := &messaging.ConsumerDeclaration{
			Queue:       consumer.Queue,
			Consumer:    consumer.Consumer,
			AutoAck:     consumer.AutoAck,
			Exclusive:   consumer.Exclusive,
			NoLocal:     consumer.NoLocal,
			NoWait:      consumer.NoWait,
			EventType:   consumer.EventType,
			Description: consumer.Description,
			Handler:     consumer.Handler, // Keep original handler reference
		}
		d.Consumers = append(d.Consumers, consumerCopy)
	}
}

// ReplayToRegistry applies all captured declarations to a new registry.
// This is used to set up per-tenant infrastructure with the same declarations
// that were captured at startup.
//
// Replay maintains dependency order: Exchanges → Queues → Bindings → Publishers → Consumers
func (d *MessagingDeclarations) ReplayToRegistry(registry messaging.RegistryInterface) error {
	// Step 1: Register exchanges first (no dependencies)
	for _, exchange := range d.Exchanges {
		registry.RegisterExchange(exchange)
	}

	// Step 2: Register queues (depend on exchanges existing)
	for _, queue := range d.Queues {
		registry.RegisterQueue(queue)
	}

	// Step 3: Register bindings (depend on exchanges and queues existing)
	for _, binding := range d.Bindings {
		registry.RegisterBinding(binding)
	}

	// Step 4: Register publishers (depend on exchanges existing)
	for _, publisher := range d.Publishers {
		registry.RegisterPublisher(publisher)
	}

	// Step 5: Register consumers (depend on queues existing)
	for _, consumer := range d.Consumers {
		registry.RegisterConsumer(consumer)
	}

	return nil
}

// Replay applies the recorded declarations to the provided registry.
// The registry is expected to be tenant-specific.
// This method maintains backward compatibility with the existing API.
func (d *MessagingDeclarations) Replay(_ context.Context, reg *messaging.Registry) {
	if d == nil || reg == nil {
		return
	}
	// Use the new ReplayToRegistry method
	_ = d.ReplayToRegistry(reg)
}

// Stats returns statistics about the captured declarations
func (d *MessagingDeclarations) Stats() DeclarationStats {
	return DeclarationStats{
		Exchanges:  len(d.Exchanges),
		Queues:     len(d.Queues),
		Bindings:   len(d.Bindings),
		Publishers: len(d.Publishers),
		Consumers:  len(d.Consumers),
	}
}

// DeclarationStats contains counts of different declaration types
type DeclarationStats struct {
	Exchanges  int
	Queues     int
	Bindings   int
	Publishers int
	Consumers  int
}

// String returns a human-readable summary of the stats
func (s DeclarationStats) String() string {
	return fmt.Sprintf("Exchanges: %d, Queues: %d, Bindings: %d, Publishers: %d, Consumers: %d",
		s.Exchanges, s.Queues, s.Bindings, s.Publishers, s.Consumers)
}

// IsEmpty returns true if no declarations were captured
func (d *MessagingDeclarations) IsEmpty() bool {
	stats := d.Stats()
	return stats.Exchanges == 0 && stats.Queues == 0 && stats.Bindings == 0 &&
		stats.Publishers == 0 && stats.Consumers == 0
}

// Validate checks if the declarations are internally consistent.
// This ensures that all references between declarations are valid.
func (d *MessagingDeclarations) Validate() error {
	// Check that all bindings reference declared queues and exchanges
	for _, binding := range d.Bindings {
		if _, ok := d.Queues[binding.Queue]; !ok {
			return fmt.Errorf("binding references undefined queue: %s", binding.Queue)
		}
		if _, ok := d.Exchanges[binding.Exchange]; !ok {
			return fmt.Errorf("binding references undefined exchange: %s", binding.Exchange)
		}
	}

	// Check that all consumers reference declared queues
	for _, consumer := range d.Consumers {
		if _, ok := d.Queues[consumer.Queue]; !ok {
			return fmt.Errorf("consumer references undefined queue: %s", consumer.Queue)
		}
	}

	// Check that all publishers reference declared exchanges
	for _, publisher := range d.Publishers {
		if _, ok := d.Exchanges[publisher.Exchange]; !ok {
			return fmt.Errorf("publisher references undefined exchange: %s", publisher.Exchange)
		}
	}

	return nil
}

// LogSummary logs a summary of the captured declarations
func (d *MessagingDeclarations) LogSummary(log logger.Logger) {
	stats := d.Stats()

	if d.IsEmpty() {
		log.Info().Msg("No messaging declarations captured (modules registered no messaging infrastructure)")
		return
	}

	log.Info().
		Int("exchanges", stats.Exchanges).
		Int("queues", stats.Queues).
		Int("bindings", stats.Bindings).
		Int("publishers", stats.Publishers).
		Int("consumers", stats.Consumers).
		Msg("Captured messaging declarations for per-tenant replay")

	// Log detailed breakdown for debugging
	log.Debug().
		Str("exchanges", d.getExchangeNames()).
		Str("queues", d.getQueueNames()).
		Msg("Messaging declaration details")
}

// getExchangeNames returns a comma-separated list of exchange names for logging
func (d *MessagingDeclarations) getExchangeNames() string {
	if len(d.Exchanges) == 0 {
		return "none"
	}

	names := make([]string, 0, len(d.Exchanges))
	for name := range d.Exchanges {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf("[%s]", strings.Join(names, ", "))
}

// getQueueNames returns a comma-separated list of queue names for logging
func (d *MessagingDeclarations) getQueueNames() string {
	if len(d.Queues) == 0 {
		return "none"
	}

	names := make([]string, 0, len(d.Queues))
	for name := range d.Queues {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf("[%s]", strings.Join(names, ", "))
}
