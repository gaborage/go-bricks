package multitenant

import (
	"context"

	"github.com/gaborage/go-bricks/messaging"
)

// Declarations mirrors messaging.Registry declarations for replay per tenant.
type Declarations struct {
	Exchanges  map[string]*messaging.ExchangeDeclaration
	Queues     map[string]*messaging.QueueDeclaration
	Bindings   []*messaging.BindingDeclaration
	Publishers []*messaging.PublisherDeclaration
	Consumers  []*messaging.ConsumerDeclaration
}

// Replay applies the recorded declarations to the provided registry.
// The registry is expected to be tenant-specific.
func (d *Declarations) Replay(_ context.Context, reg *messaging.Registry) {
	if d == nil || reg == nil {
		return
	}
	for _, ex := range d.Exchanges {
		reg.RegisterExchange(ex)
	}
	for _, q := range d.Queues {
		reg.RegisterQueue(q)
	}
	for _, b := range d.Bindings {
		reg.RegisterBinding(b)
	}
	for _, p := range d.Publishers {
		reg.RegisterPublisher(p)
	}
	for _, c := range d.Consumers {
		reg.RegisterConsumer(c)
	}
}
