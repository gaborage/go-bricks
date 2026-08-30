package outbox

import "errors"

var (
	// ErrStreamTargetRequiresTenant is returned when an event targets a super stream
	// but the context carries no tenant to take the partition key from.
	ErrStreamTargetRequiresTenant = errors.New("outbox: a stream target takes its partition key from the context tenant, and the context carries none")

	// ErrConflictingTargets is returned when an event names both a stream and an
	// exchange or routing key.
	ErrConflictingTargets = errors.New("outbox: an event targets either an exchange or a stream; a stream target takes no exchange or routing key")

	// ErrStreamNotAnOutboxTarget is returned when an event names a stream the relay
	// was not configured to publish to.
	ErrStreamNotAnOutboxTarget = errors.New("outbox: stream is not listed in outbox.superstreams")
)
