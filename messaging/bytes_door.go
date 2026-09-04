package messaging

import (
	"context"
	"errors"
	"fmt"

	"github.com/gaborage/go-bricks/internal/publishdoor"
)

// bytePublisher is the framework's ONLY byte publish door (ADR-096). It is
// unexported, so only types in this package implement it: AMQPClientImpl and
// the stamping wrapper the manager puts in front of every pooled client. A
// module cannot name it, cannot implement it, and cannot reach it through
// AMQPClient — Publisher[T].Publish asserts it on the client it is handed, and
// the outbox relay reaches it through internal/publishdoor.
type bytePublisher interface {
	publishBytes(ctx context.Context, options publishOptions, data []byte) error
}

var _ bytePublisher = (*AMQPClientImpl)(nil)

// ErrPublishDoorUnavailable is returned when a publish is attempted through a
// client that carries no byte door: one built by app.Options.MessagingClientFactory,
// a hand-written AMQPClient, or a testing/mocks double. Match it with errors.Is.
// The remedy is to publish through a framework-built client (deps.Messaging)
// or, in a test, to swap the handle for messaging/testing's capture publisher.
var ErrPublishDoorUnavailable = errors.New("messaging: client carries no byte publish door; publish through a framework-built client")

// publishThroughDoor asserts the door on client and publishes. The typed handle
// and the relay dispatcher funnel here; the stamping wrapper asserts once at
// construction instead (its client is fixed) and spells the same error.
func publishThroughDoor(ctx context.Context, client any, options publishOptions, data []byte) error {
	door, ok := client.(bytePublisher)
	if !ok {
		return fmt.Errorf("%w: %T", ErrPublishDoorUnavailable, client)
	}
	return door.publishBytes(ctx, options, data)
}

// init registers the relay's dispatcher: internal/publishdoor.Publish lands here
// with the relay's client and destination. The Options shape is restated in that
// package because it cannot import this one.
//
//nolint:gochecknoinits // linking messaging is what arms the relay's byte door; same seam shape as messaging/streams (ADR-091, ADR-096)
func init() {
	publishdoor.Register(func(ctx context.Context, client any, opts publishdoor.Options, data []byte) error {
		return publishThroughDoor(ctx, client, publishOptions{
			Exchange:   opts.Exchange,
			RoutingKey: opts.RoutingKey,
			Headers:    opts.Headers,
			Mandatory:  opts.Mandatory,
			Immediate:  opts.Immediate,
		}, data)
	})
}
