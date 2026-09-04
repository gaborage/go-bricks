package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"

	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
	"github.com/gaborage/go-bricks/multitenant"
)

// Publisher is the module-facing handle DeclareTypedPublisher returns: a
// publisher bound at declaration time to ONE destination — the declared
// exchange, routing key, default headers and delivery flags — so a call site
// never re-spells them. It is the publish mirror of the typed consumer built
// by DeclareTypedConsumer.
//
// Every field is set once at construction and read-only afterwards: one handle
// is shared by every goroutine of a module AND, under multi-tenant messaging,
// by every tenant's client, so any mutable state here would be a data race.
// Publish therefore hands the client a fresh headers map on every call and
// never writes back into the handle. The copy is one level deep, the same depth
// RegisterPublisher, Declarations.Clone and the stamping wrapper copy at:
// declared default headers are startup config, expected to be scalars (a
// string, a number, a bool), not nested tables a publish would mutate.
type Publisher[T any] struct {
	eventType  string
	exchange   string
	routingKey string
	headers    map[string]any
	mandatory  bool
	immediate  bool
	// sealer is set when T carries seal tags: Publish then seals instead of marshaling,
	// so a plaintext publish of a sealed type is unrepresentable (ADR-097). nil for a
	// plain T.
	sealer Sealer
	// sealErr is why a seal-tagged T could not get its sealer. Validate reports it at
	// startup; the handle keeps it too, so a caller that publishes before or despite that
	// report gets the error rather than plaintext.
	sealErr error
}

// newTypedPublisher is the single construction point for the handle, so every
// exported entry builds it the same way. The copy of decl.Headers is the ONLY
// thing severing the handle from the caller's PublisherOptions.Headers map:
// NewPublisher aliases a non-nil Headers rather than copying it, and
// DeclarePublisher returns that pre-registration value, not the deep copy
// RegisterPublisher stores. Removing this copy would let a later write through
// the caller's map reach every publish.
func newTypedPublisher[T any](decl *PublisherDeclaration) *Publisher[T] {
	headers := make(map[string]any, len(decl.Headers))
	maps.Copy(headers, decl.Headers)

	return &Publisher[T]{
		eventType:  decl.EventType,
		exchange:   decl.Exchange,
		routingKey: decl.RoutingKey,
		headers:    headers,
		mandatory:  decl.Mandatory,
		immediate:  decl.Immediate,
	}
}

// DeclareTypedPublisher registers a publisher exactly as DeclarePublisher does —
// same registry entry, same replay, validation and hash path — and returns a
// Publisher[T] handle bound to the declared destination. Keep the handle on the
// module (there is no deps accessor to look one up again) and publish through
// it from handlers and services.
//
// The exchange is not declared here: pass it to DeclareTopicExchange
// separately, exactly as a DeclarePublisher with a nil exchange does.
//
// It panics on a nil decls or a nil opts — both are declaration-time wiring
// mistakes, and the package already fails startup that way for the typed
// consumer entries.
func DeclareTypedPublisher[T any](decls *Declarations, opts *PublisherOptions) *Publisher[T] {
	if decls == nil {
		panic("messaging: DeclareTypedPublisher requires a non-nil *Declarations")
	}
	if opts == nil {
		panic("messaging: DeclareTypedPublisher requires non-nil *PublisherOptions")
	}

	decl := decls.DeclarePublisher(opts, nil)
	handle := newTypedPublisher[T](decl)

	var zero T
	if t := reflect.TypeOf(zero); hasSealTag(t) {
		sealer, err := newSealer(t, decl.EventType)
		if err != nil {
			decls.recordSealError(err)
		}
		handle.sealer, handle.sealErr = sealer, err
	}
	return handle
}

// Publish encodes evt and publishes it to the DECLARED exchange and routing
// key with the declared default headers, through client — the tenant-aware
// client a handler already holds (the getMessaging(ctx) idiom), so the
// framework's tenant stamping and trace injection apply unchanged.
//
// A plain T is JSON-marshaled. A seal-tagged T is sealed (ADR-097) — once, here,
// before the client's retry loop, so every attempt and every redelivery carries
// the same bytes and the same signed jti. A caller-side retry after this call
// fails is a new seal and a new jti.
//
// An encode or seal failure is returned wrapped and publishes nothing. Every
// other error is the client's own (ErrInvalidPublishDestination,
// ErrPublishRetriesExhausted, ...) and is returned unwrapped.
//
// Safe for concurrent use: the handle is never written after construction and
// the client receives a fresh copy of the declared headers on every call.
func (h *Publisher[T]) Publish(ctx context.Context, client AMQPClient, evt T) error {
	data, err := h.encode(ctx, client, evt)
	if err != nil {
		return err
	}

	return h.publishBytes(ctx, client, data)
}

// Seal returns the sealed wire bytes for evt without publishing them — the outbox
// lane persists them as-is (persisted-sealed, ADR-097) and the relay moves them
// byte-identical. A plain T has nothing to seal: it returns ErrNotSealTagged, and the
// event goes to the outbox as a struct payload the outbox already marshals.
func (h *Publisher[T]) Seal(ctx context.Context, evt T) ([]byte, error) {
	if h.sealer == nil && h.sealErr == nil {
		return nil, fmt.Errorf("%w (event type %q)", ErrNotSealTagged, h.eventType)
	}
	return h.encode(ctx, nil, evt)
}

// encode is the one place the handle turns an event into bytes: seal when T is
// seal-tagged, marshal otherwise. Before sealing it resolves the tenant exactly as the
// stamping wrapper will for client — context first, the client's pool key otherwise,
// a disagreement refused — and carries the answer on the context, so the signed tid
// and the x-tenant-id header always name the same tenant. Seal (no client) sees only
// the context; the outbox lane stamps from the same context later.
func (h *Publisher[T]) encode(ctx context.Context, client AMQPClient, evt T) ([]byte, error) {
	if h.sealErr != nil {
		return nil, h.sealErr
	}
	if h.sealer != nil {
		sealCtx, err := tenantForSeal(ctx, client)
		if err != nil {
			return nil, err
		}
		data, err := h.sealer.Seal(sealCtx, evt)
		if err != nil {
			return nil, fmt.Errorf("messaging: seal %s event: %w", h.eventType, err)
		}
		return data, nil
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return nil, fmt.Errorf("messaging: marshal %s event: %w", h.eventType, err)
	}
	return data, nil
}

// tenantForSeal returns ctx carrying the tenant the stamping wrapper will write for a
// publish through client, or ctx unchanged when no tenant is in play.
func tenantForSeal(ctx context.Context, client AMQPClient) (context.Context, error) {
	key := ""
	if src, ok := client.(stampSource); ok {
		key = src.ReplayKey()
	}
	tenant, err := tenantstamp.Resolve(ctx, key)
	if err != nil {
		return nil, err
	}
	if tenant == "" {
		return ctx, nil
	}
	return multitenant.SetTenant(ctx, tenant), nil
}

// publishBytes is the handle's ONE bytes door. It is the only place the handle
// names the client's raw publish method, so retargeting it — to an internal
// bytes interface reached by type assertion, say — never touches Publish's
// exported signature or its callers.
func (h *Publisher[T]) publishBytes(ctx context.Context, client AMQPClient, data []byte) error {
	headers := make(map[string]any, len(h.headers))
	maps.Copy(headers, h.headers)

	return client.PublishToExchange(ctx, PublishOptions{
		Exchange:   h.exchange,
		RoutingKey: h.routingKey,
		Headers:    headers,
		Mandatory:  h.mandatory,
		Immediate:  h.immediate,
	}, data)
}
