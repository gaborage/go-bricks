package messaging

import (
	"context"
	"fmt"
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
	// door is the wrapped client's byte door, asserted ONCE at construction; nil
	// for a client the framework did not build (an app.Options.MessagingClientFactory
	// product), which then fails every publish with ErrPublishDoorUnavailable
	// rather than bypassing the stamp.
	door bytePublisher
	// key is the pool key this client was created for: a tenant, or "" for the
	// control-plane client. It is a stamp source, not a label — see
	// tenantstamp.Resolve.
	key string
	// stopObserver ends the redeclare observer the manager attached to the wrapped
	// client, and observerDone closes when that goroutine has exited. They live on
	// the pooled value because the pool's closer receives exactly this value on
	// every retirement path, which makes the wrapper the client's own lifetime
	// record — a side map would be a second one to keep in step. Both are nil for
	// a client that announces no channels.
	//
	// Shutdown CANCELS the observer and does not join observerDone. Joining would
	// put Manager.Close behind a declare pass already past its guard, and that pass
	// runs broker RPCs no context cancels; letting it end on its own costs at most
	// one WARN from a connection that is closing anyway. observerDone is therefore
	// a termination signal for tests to wait on deterministically, not a shutdown
	// barrier — if it ever becomes one, Close is the place, with that cost priced in.
	stopObserver func()
	observerDone <-chan struct{}
}

func newStampingPublisher(base AMQPClient, key string) *stampingPublisher {
	door, _ := base.(bytePublisher)
	return &stampingPublisher{AMQPClient: base, door: door, key: key}
}

// replayKeyProvider is what a client exposes so the typed door can resolve the tenant the
// stamp will carry BEFORE it seals: the signed tid and the header must come from the
// same two sources (context and pool key), or a per-tenant client used from a context
// without a tenant would stamp the header and sign no tid.
type replayKeyProvider interface {
	ReplayKey() string
}

func (p *stampingPublisher) ReplayKey() string { return p.key }

// publishBytes is the wrapper's byte door: stamp, then hand the frame to the
// wrapped client's own door (see the door field for the no-door case).
func (p *stampingPublisher) publishBytes(ctx context.Context, options publishOptions, data []byte) error {
	base := p.door
	if base == nil {
		return fmt.Errorf("%w: %T", ErrPublishDoorUnavailable, p.AMQPClient)
	}
	stamp, err := tenantstamp.ResolveForPublish(ctx, options.Headers, p.key)
	if err != nil {
		return err
	}
	if stamp == "" {
		return base.publishBytes(ctx, options, data)
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

	return base.publishBytes(ctx, stamped, data)
}

// compile-time proof the wrapper still satisfies the full client surface AND
// the byte door.
var (
	_ AMQPClient    = (*stampingPublisher)(nil)
	_ bytePublisher = (*stampingPublisher)(nil)
)
