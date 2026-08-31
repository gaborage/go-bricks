// Package streams publishes to and consumes RabbitMQ streams over the native
// stream protocol (default port 5552, rabbitmq_stream plugin).
//
// It complements the AMQP 0.9.1 lane in the parent messaging package: streams
// declared as AMQP queues (x-queue-type: stream) can be consumed there, but
// server-side offset tracking and single active consumer need this protocol.
// Publishing is synchronous and confirmed — see wiki/streams.md.
package streams

import (
	"context"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
)

// Handler processes one stream message. An error is terminal for the MESSAGE,
// not for the stream: the failure is logged and counted, the offset is NOT
// committed, and consumption continues with the next message. Streams have no
// nack or redelivery, so handlers must be idempotent.
//
// Calls are sequential within one stream — and within one partition of a super
// stream — but CONCURRENT across the partitions of a super stream, because each
// partition is a separate connection with its own delivery loop. A handler
// registered with DeclareSuperStreamConsumer must therefore be goroutine-safe.
type Handler func(ctx context.Context, msg *Message) error

// Message is the framework view of a stream delivery.
type Message struct {
	// Data is the first data section of the AMQP 1.0 message body.
	Data []byte
	// Offset is this message's offset within Stream.
	Offset int64
	// Stream is the stream (or, for super streams, the partition) it arrived on.
	Stream string
	// Properties carries the AMQP 1.0 application properties, nil when absent.
	Properties map[string]any
}

// PublishMessage is the framework view of an outbound stream message.
type PublishMessage struct {
	// Data becomes the AMQP 1.0 data section of the message body.
	Data []byte
	// Properties carries AMQP 1.0 application properties; nil is fine. The map is
	// copied, so the caller's own map is never written to.
	Properties map[string]any
	// RoutingKey selects the partition of a super stream (murmur3 hash — the
	// RabbitMQ cross-client default). Required non-empty on a publisher declared
	// with DeclareSuperStreamPublisher, and must be empty on one declared with
	// DeclarePublisher, which targets a plain stream with no partitions to pick.
	RoutingKey string
}

// offsetKind enumerates the start positions. The zero value is Next so that a
// zero-value OffsetStart is the safe "only new messages" configuration.
type offsetKind int

const (
	offsetKindNext offsetKind = iota
	offsetKindFirst
	offsetKindLast
	offsetKindAt
	offsetKindSince
)

// OffsetStart is where a consumer begins when the broker holds no stored offset
// for its name. A stored offset always wins over it — see Manager.Start.
// The zero value is OffsetNext().
type OffsetStart struct {
	kind  offsetKind
	value int64
}

// OffsetFirst starts at the oldest message still retained in the stream.
func OffsetFirst() OffsetStart { return OffsetStart{kind: offsetKindFirst} }

// OffsetLast starts at the beginning of the last chunk written to the stream.
func OffsetLast() OffsetStart { return OffsetStart{kind: offsetKindLast} }

// OffsetNext starts at the next message written after the consumer attaches.
func OffsetNext() OffsetStart { return OffsetStart{kind: offsetKindNext} }

// OffsetAt starts at an absolute stream offset.
func OffsetAt(offset int64) OffsetStart {
	return OffsetStart{kind: offsetKindAt, value: offset}
}

// OffsetSince starts at the first message stored at or after t.
func OffsetSince(t time.Time) OffsetStart {
	return OffsetStart{kind: offsetKindSince, value: t.UnixMilli()}
}

// specification renders the start position in the client's vocabulary.
func (o OffsetStart) specification() stream.OffsetSpecification {
	switch o.kind {
	case offsetKindFirst:
		return stream.OffsetSpecification{}.First()
	case offsetKindLast:
		return stream.OffsetSpecification{}.Last()
	case offsetKindAt:
		return stream.OffsetSpecification{}.Offset(o.value)
	case offsetKindSince:
		return stream.OffsetSpecification{}.Timestamp(o.value)
	default:
		return stream.OffsetSpecification{}.Next()
	}
}

// StreamSpec configures retention for a declared stream. Zero-value fields are
// omitted, leaving the broker's own defaults in place; a negative value is a
// declaration error that Declarations.Validate reports.
type StreamSpec struct {
	// MaxAge discards segments older than this duration, truncated to whole
	// seconds. A non-zero sub-second value floors to 1s: second granularity is
	// RabbitMQ's, so anything briefer is inexpressible and would otherwise
	// discard the retention the caller asked for. Renders identically to
	// messaging.StreamQueueSpec.MaxAge, so both lanes treat a given value the
	// same way.
	MaxAge time.Duration
	// MaxLengthBytes caps the total retained size of the stream.
	MaxLengthBytes int64
	// MaxSegmentSizeBytes caps the size of one segment file.
	MaxSegmentSizeBytes int64
}

// ConsumerOptions declares one stream consumer.
type ConsumerOptions struct {
	// Stream is the stream to consume; it must be declared in the same Declarations.
	Stream string
	// Name is the consumer/group name. Required: it is the key the broker stores
	// offsets under and, with SAC, the group identity.
	Name string
	// Start is where to begin when the broker holds no stored offset for Name.
	Start OffsetStart
	// SAC enables single active consumer: only one member of the group receives
	// messages at a time (RabbitMQ 3.11+).
	SAC bool
	// Handler processes each message. Required.
	Handler Handler
	// Retry bounds in-place re-invocation of Handler after it returns an error.
	// Nil is one attempt, unless Hold is set — see DefaultHoldRetry. A policy may
	// ask for at most MaxRetryAttempts attempts and MaxRetryWait of total waiting,
	// because the waits run inside the partition's own delivery callback; a failure
	// that needs more patience than that belongs in the hold.
	Retry *RetryOptions
	// Hold parks a failed delivery per tenant so the tenant's later messages wait
	// behind it while the rest of the partition keeps flowing.
	//
	// SECURITY: the tenant is read from the producer-written stamp, which is
	// identification and not authorization (ADR-039's rule for HTTP resolvers,
	// applied here). A producer that can publish to this stream therefore chooses
	// which tenant a message is held under, and a failure it induces holds THAT
	// tenant until the hold drains. Isolating producers — per-tenant credentials
	// or vhosts — stays the deployment's job, and it matters more here than for a
	// delivery that is merely mis-attributed.
	Hold bool
	// TenantOptional lets this consumer run a delivery that carries no tenant stamp
	// (a control-plane consumer). Default false: a consumer that needs a tenant fails
	// the delivery closed rather than running the handler without one. It never
	// admits a stamp that is present but unusable.
	TenantOptional bool
}

// SuperStreamConsumerOptions declares one consumer over every partition of a
// super stream.
//
// There is deliberately no SAC field: super-stream consumption is ALWAYS a single
// active consumer group. The client attaches every partition with one shared
// offset specification, so the SAC promotion callback — which the broker fires
// once per partition — is the only place a per-partition stored offset can be
// restored. Without it a restart would replay every partition from Start, which
// contradicts the stored-offset-wins contract the plain lane documents. A lone
// member is promoted on every partition, so a single-instance deployment loses
// nothing. See ADR-059.
type SuperStreamConsumerOptions struct {
	// SuperStream is the super stream to consume; it must be declared in the same
	// Declarations.
	SuperStream string
	// Name is the consumer/group name. Required: the broker stores an offset per
	// partition under it, and it is the group identity partitions are distributed by.
	Name string
	// Start is where a partition begins when the broker holds no stored offset for
	// Name on that partition.
	Start OffsetStart
	// Handler processes each message. Required, and called concurrently across
	// partitions — see Handler.
	Handler Handler
	// Retry bounds in-place re-invocation of Handler after it returns an error.
	// Nil is one attempt, unless Hold is set — see DefaultHoldRetry. A policy may
	// ask for at most MaxRetryAttempts attempts and MaxRetryWait of total waiting,
	// because the waits run inside the partition's own delivery callback; a failure
	// that needs more patience than that belongs in the hold.
	Retry *RetryOptions
	// Hold parks a failed delivery per tenant so the tenant's later messages wait
	// behind it while the rest of the partition keeps flowing.
	//
	// SECURITY: the tenant is read from the producer-written stamp, which is
	// identification and not authorization (ADR-039's rule for HTTP resolvers,
	// applied here). A producer that can publish to this stream therefore chooses
	// which tenant a message is held under, and a failure it induces holds THAT
	// tenant until the hold drains. Isolating producers — per-tenant credentials
	// or vhosts — stays the deployment's job, and it matters more here than for a
	// delivery that is merely mis-attributed.
	Hold bool
	// TenantOptional lets this consumer run a delivery that carries no tenant stamp
	// (a control-plane consumer). Default false: fail closed. It never admits a
	// stamp that is present but unusable.
	TenantOptional bool
}
