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

	// ErrReservedHeaderPrefix is returned when a caller's event headers claim the
	// x-gobricks- prefix, the framework's own namespace inside a persisted row's
	// headers. The framework is that namespace's only writer — it stamps the
	// payload's encoding there and the relay reads it back off the row — so a
	// caller header under it is refused rather than silently dropped: a drop hides
	// both the mistake and the attempt, and the caller never learns its header
	// went nowhere. Rename the header out of the prefix.
	ErrReservedHeaderPrefix = errors.New("outbox: the x-gobricks- header prefix is reserved for the framework's own stamps")

	// ErrNotLeader is returned by Store.Lead when another relay instance holds the
	// ledger's leader row.
	ErrNotLeader = errors.New("outbox: another relay instance leads this ledger")
)
