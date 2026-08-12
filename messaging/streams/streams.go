// Package streams consumes RabbitMQ streams over the native stream protocol
// (default port 5552, rabbitmq_stream plugin).
//
// It complements the AMQP 0.9.1 lane in the parent messaging package: streams
// declared as AMQP queues (x-queue-type: stream) can be consumed there, but
// server-side offset tracking and single active consumer need this protocol.
// Publishing is deliberately not part of this surface — see wiki/streams.md.
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
}
