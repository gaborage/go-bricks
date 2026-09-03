// module_shape.go is the code a MODULE AUTHOR would write under #1305: a DeclareMessaging
// block with DeclareTypedPublisher[T], an h.Publish call site, DeclareTypedConsumerWithMeta
// with AcceptUnsealed, and a handler body with ONE meta.DedupKey() call. The framework side
// (Declarations, Publisher[T], the consumer door) is a thin stand-in over Producer/Consumer;
// the SHAPE is the decided one. S0 embeds this file verbatim into the report.
package main

import (
	"context"
	"fmt"
	"reflect"
)

// ---------------------------------------------------------------------------
// Framework stand-ins (what messaging/sealed would provide)
// ---------------------------------------------------------------------------

// Declarations is the DeclareMessaging registry stand-in.
type Declarations struct {
	producer  *Producer
	consumer  *Consumer
	consumers []consumerDecl
}

type consumerDecl struct {
	decl    ConsumerDecl
	handler func(ctx context.Context, frame Frame) error
}

// Publisher is the typed publish handle: the ONLY module-facing publish door.
type Publisher[T any] struct {
	decls       *Declarations
	Destination string
	EventType   string
}

// Client is the messaging client stand-in passed explicitly at the call site.
type Client struct{ Published []Frame }

// DeclareTypedPublisher declares a destination for T; sealing engages from T's tags.
func DeclareTypedPublisher[T any](d *Declarations, destination, eventType string) *Publisher[T] {
	var zero T
	if _, err := ScanType(reflect.TypeOf(zero)); err != nil {
		panic("seal-tagged T without a codec / bad tags: " + err.Error())
	}
	return &Publisher[T]{decls: d, Destination: destination, EventType: eventType}
}

// Publish marshals → seals (T is seal-tagged) → publishes. Seal runs once, before any retry.
func (h *Publisher[T]) Publish(_ context.Context, client *Client, evt T) error {
	frame, err := h.Seal(evt)
	if err != nil {
		return err
	}
	client.Published = append(client.Published, frame)
	return nil
}

// Seal returns the wire frame (bytes for the outbox lane, persisted-sealed).
func (h *Publisher[T]) Seal(evt T) (Frame, error) {
	p := *h.decls.producer
	p.EventType = h.EventType
	frame, _, err := p.Seal(evt)
	return frame, err
}

// ConsumerDecl is the typed consumer declaration; AcceptUnsealed is a CODE field.
type ConsumerDecl struct {
	Queue          string
	EventType      string
	AcceptUnsealed bool
}

// DeclareTypedConsumerWithMeta registers the handler behind the WithMeta door (required for
// seal-tagged T: the meta-less door would make jti unreachable).
func DeclareTypedConsumerWithMeta[T any](d *Declarations, decl ConsumerDecl, handler func(ctx context.Context, evt T, meta *Meta) error) {
	var zero T
	c := *d.consumer
	c.EventType, c.AcceptUnsealed = decl.EventType, decl.AcceptUnsealed
	if err := c.Startup(reflect.TypeOf(zero)); err != nil {
		panic(err) // startup error, never per-message poison
	}
	d.consumers = append(d.consumers, consumerDecl{decl: decl, handler: func(ctx context.Context, frame Frame) error {
		var evt T
		meta, _, oerr := c.Open(frame, &evt)
		if oerr != nil {
			return oerr // → PayloadStageOpen, nack without requeue
		}
		return handler(ctx, evt, meta)
	}})
}

// ---------------------------------------------------------------------------
// The module author's code
// ---------------------------------------------------------------------------

type paymentsModule struct {
	publisher *Publisher[PaymentAuthorized]
	ledger    *Ledger
	log       []string
}

func (m *paymentsModule) DeclareMessaging(d *Declarations) {
	m.publisher = DeclareTypedPublisher[PaymentAuthorized](d, "payments.events", "payment.authorized")

	DeclareTypedConsumerWithMeta(d, ConsumerDecl{
		Queue:          "acme-core.payments",
		EventType:      "payment.authorized",
		AcceptUnsealed: false, // temporary migration knob; startup WARN while true
	}, m.onPaymentAuthorized)
}

// AuthorizePayment is the business call site: one Publish, sealing implied by the tags.
func (m *paymentsModule) AuthorizePayment(ctx context.Context, client *Client, evt PaymentAuthorized) error {
	return m.publisher.Publish(ctx, client, evt)
}

func (m *paymentsModule) onPaymentAuthorized(_ context.Context, evt PaymentAuthorized, meta *Meta) error {
	key, err := meta.DedupKey() // one call in every migration state
	if err != nil {
		return err
	}
	if m.ledger.ProcessOnce(key) == VerdictDuplicate {
		m.log = append(m.log, fmt.Sprintf("duplicate skipped: %s", key))
		return nil
	}
	if env, ok := meta.Sealed(); ok {
		m.log = append(m.log, fmt.Sprintf("processed order %s sealed under %s (jti len %d)", evt.OrderID, env.SignKid, len(env.JTI)))
	} else {
		m.log = append(m.log, fmt.Sprintf("processed order %s UNSEALED (accept-unsealed)", evt.OrderID))
	}
	return nil
}
