package messaging

import (
	"context"
	"fmt"
	"reflect"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/messaging/internal/payloaderr"
	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
	"github.com/gaborage/go-bricks/multitenant"
)

// Aliases of the consume-side seam types, so messaging/sealed and a test double
// name them from here (the producer-side aliases live in sealing.go).
type (
	// SealSpec is a codec's scanned declaration as messaging sees it: the two Logical kids.
	SealSpec = sealruntime.Spec
	// SealOpenerProvider is the OPTIONAL consume side of a SealCodec.
	SealOpenerProvider = sealruntime.OpenerProvider
	// SealOpener opens one sealed delivery.
	SealOpener = sealruntime.Opener
	// SealTenantRule is the tid expectation the sealed door derives per delivery.
	SealTenantRule = sealruntime.TenantRule
	// SealEnvelope is the seam's envelope; SealedEnvelope is its Metadata twin.
	SealEnvelope = sealruntime.Envelope
	// SealOpenRefusedError is the seam's refusal, found in a PayloadError's chain.
	SealOpenRefusedError = sealruntime.OpenRefusedError
)

// sealedHandler is the consume-side mirror of the sealed publisher: the
// MessageHandler DeclareTypedConsumerWithMeta installs for a seal-tagged T.
// Every field is set once at declaration and read-only afterwards — one
// instance serves every worker goroutine and every tenant replaying the same
// declarations — and the opener it holds is immutable by the codec's contract.
type sealedHandler[T any] struct {
	eventType string
	opener    sealruntime.Opener
	// tenancy and tenantOptional fix the tid rule for this consumer at
	// declaration time (ADR-097, G2/G10): the deployment's tenancy comes from the
	// runtime the app configured, TenantOptional from the declaration.
	tenancy        sealruntime.Tenancy
	tenantOptional bool
	fn             func(context.Context, T, Metadata) error
}

// newSealedHandler builds the sealed handler for one declaration, or reports why
// the consumer cannot start: codec not linked, runtime not configured, no key
// store, a refused declaration, or a family the consumer cannot open with. Every
// error is recorded on the Declarations and surfaces from Validate.
func newSealedHandler[T any](t reflect.Type, opts *ConsumerOptions, fn func(context.Context, T, Metadata) error) (*sealedHandler[T], error) {
	opener, tenancy, err := newOpener(t, opts.EventType)
	if err != nil {
		return nil, err
	}
	return &sealedHandler[T]{
		eventType:      opts.EventType,
		opener:         opener,
		tenancy:        tenancy,
		tenantOptional: opts.TenantOptional,
		fn:             fn,
	}, nil
}

// misplacedConsumerSealTag is the consumer-side twin of the publisher's nested-tag
// refusal: a seal tag on or under a named nested member is one the codec would never
// open, so the declaration is refused at startup rather than decoding ciphertext.
func misplacedConsumerSealTag(t reflect.Type, eventType string) error {
	path := misplacedSealTag(t)
	if path == "" {
		return nil
	}
	return fmt.Errorf("messaging: %v carries a seal tag on nested member %s; seal tags belong on the event type's own fields (consumer event type %q)", t, path, eventType)
}

// newOpener resolves the consume side of the registered codec for a seal-tagged
// type. It mirrors newSealer step for step and then asks the codec for its
// OPTIONAL consume side: a codec without one is the same class of failure as no
// codec at all.
func newOpener(t reflect.Type, eventType string) (sealruntime.Opener, sealruntime.Tenancy, error) {
	codec := sealruntime.Registered()
	if codec == nil {
		return nil, 0, fmt.Errorf("%w (consumer event type %q, Go type %v)", ErrSealingNotLinked, eventType, t)
	}
	rt := sealruntime.Configured()
	if rt == nil {
		return nil, 0, fmt.Errorf("%w (consumer event type %q)", sealruntime.ErrNotConfigured, eventType)
	}
	if rt.KeyStore == nil {
		return nil, 0, fmt.Errorf("%w (consumer event type %q)", sealruntime.ErrKeyStoreMissing, eventType)
	}
	factory, ok := codec.(sealruntime.OpenerProvider)
	if !ok {
		return nil, 0, fmt.Errorf("%w: the registered codec has no consume side (consumer event type %q)", ErrSealingNotLinked, eventType)
	}
	spec, err := codec.ScanType(t)
	if err != nil {
		return nil, 0, fmt.Errorf("messaging: seal declaration of %v (consumer event type %q): %w", t, eventType, err)
	}
	if spec == nil {
		return nil, 0, fmt.Errorf("messaging: %v carries seal tags the codec did not recognize (consumer event type %q)", t, eventType)
	}
	opener, err := factory.NewOpener(spec, eventType, rt)
	if err != nil {
		return nil, 0, fmt.Errorf("messaging: sealed consumer for %v (event type %q): %w", t, eventType, err)
	}
	return opener, rt.Tenancy, nil
}

// Handle opens the delivery (verify, judge tid, decrypt, splice, decode into a
// fresh T), validates the plaintext, and runs fn with the verified envelope on
// its Metadata. A refusal is a *PayloadError at PayloadStageOpen carrying the
// opener's error in its chain; the worker loop nacks it without requeue like
// any other payload failure.
func (h *sealedHandler[T]) Handle(ctx context.Context, delivery *amqp.Delivery) error {
	if delivery == nil {
		return newPayloadError(h.eventType, payloaderr.NewDecode(errNilDelivery, nilDeliverySummary))
	}

	var payload T
	env, err := h.opener.Open(ctx, delivery.Body, h.tenantRule(ctx, delivery), &payload)
	if err != nil {
		return newPayloadError(h.eventType, payloaderr.NewOpen(err))
	}
	if body := payloaderr.ValidateStruct(payload); body != nil {
		return newPayloadError(h.eventType, body)
	}

	sealed := SealedEnvelope(env)
	ctx = context.WithValue(ctx, sealedDeliveryKey{}, true)
	return h.fn(ctx, payload, Metadata{delivery: delivery, sealed: &sealed})
}

func (h *sealedHandler[T]) EventType() string {
	return h.eventType
}

// tenantRule derives the tid expectation for one delivery from the tenancy the
// consumer was declared under (#1309 G2/G10, #1307):
//
//   - shared: a signed tid is REQUIRED unless the consumer declared
//     TenantOptional, and a present tid must equal the x-tenant-id carrier the
//     delivery pipeline already admitted;
//   - per-tenant: absent is accepted, present-and-different from the context
//     tenant (the pool key) is poison;
//   - disabled: no rule; the value is surfaced on the envelope only.
//
// The carrier is read with the same reader the pipeline used, so an unusable
// stamp (which the pipeline refused before this ran) and an absent one both
// leave Expected empty here.
func (h *sealedHandler[T]) tenantRule(ctx context.Context, delivery *amqp.Delivery) sealruntime.TenantRule {
	switch h.tenancy {
	case sealruntime.TenancyShared:
		carrier, _ := tenantstamp.Read(func(key string) (any, bool) {
			value, present := delivery.Headers[key]
			return value, present
		})
		return sealruntime.TenantRule{Required: !h.tenantOptional, Expected: carrier}
	case sealruntime.TenancyPerTenant:
		expected, _ := multitenant.GetTenant(ctx)
		return sealruntime.TenantRule{Expected: expected}
	default:
		return sealruntime.TenantRule{}
	}
}
