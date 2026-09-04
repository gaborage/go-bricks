package messaging

import (
	"context"
	"maps"

	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
)

// stampingPublisher writes the tenant stamp onto every publish made through a
// pooled client, and refuses a caller that tries to write it itself.
//
// It wraps the client rather than living inside AMQPClientImpl because the
// manager hands out clients a CONSUMER may have built (app.Options.
// MessagingClientFactory): a stamp that depended on the concrete type would be
// silently absent for those deployments, and under messaging.tenancy: shared
// their own consumers would then nack every delivery. Wrapping is what makes
// "the framework is the stamp's only writer" true for every client, whatever
// produced it.
type stampingPublisher struct {
	AMQPClient
	// key is the pool key this client was created for: a tenant, or "" for the
	// control-plane client. It is a stamp source, not a label — see
	// tenantstamp.Resolve.
	key string
}

func newStampingPublisher(base AMQPClient, key string) AMQPClient {
	return &stampingPublisher{AMQPClient: base, key: key}
}

// stampSource is what a client exposes so the typed door can resolve the tenant the
// stamp will carry BEFORE it seals: the signed tid and the header must come from the
// same two sources (context and pool key), or a per-tenant client used from a context
// without a tenant would stamp the header and sign no tid.
type stampSource interface {
	ReplayKey() string
}

func (p *stampingPublisher) ReplayKey() string { return p.key }

// Publish routes through PublishToExchange so one implementation stamps both doors.
func (p *stampingPublisher) Publish(ctx context.Context, destination string, data []byte) error {
	return p.PublishToExchange(ctx, PublishOptions{Exchange: "", RoutingKey: destination}, data)
}

func (p *stampingPublisher) PublishToExchange(ctx context.Context, options PublishOptions, data []byte) error {
	stamp, err := tenantstamp.ResolveForPublish(ctx, options.Headers, p.key)
	if err != nil {
		return err
	}
	if stamp == "" {
		return p.AMQPClient.PublishToExchange(ctx, options, data)
	}

	// The caller's map is never written to: a publish must not mutate the options
	// a caller may reuse or share across goroutines.
	//
	// Sized to the caller's headers only: the map grows itself for the one stamp,
	// and a +1 in the hint is unobservable — the same reason the streams lane sizes
	// its property map this way, and the mutation gate's proof of it (an operator
	// swap in the hint changes nothing any test can see).
	stamped := options
	stamped.Headers = make(map[string]any, len(options.Headers))
	maps.Copy(stamped.Headers, options.Headers)
	tenantstamp.Write(stamp, func(key string, value any) { stamped.Headers[key] = value })

	return p.AMQPClient.PublishToExchange(ctx, stamped, data)
}

// compile-time proof the wrapper still satisfies the full client surface.
var _ AMQPClient = (*stampingPublisher)(nil)
