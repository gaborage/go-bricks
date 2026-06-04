package outbox

import amqp "github.com/rabbitmq/amqp091-go"

// AMQP delivery header names stamped by the relay for consumer idempotency.
// These constants are the single source of truth; the relay references them
// when publishing, so consumers can dedupe without re-declaring the literals.
const (
	// HeaderEventID carries the unique outbox event id (a UUID) used for
	// consumer-side deduplication.
	HeaderEventID = "x-outbox-event-id"

	// HeaderEventType carries the event type of the published outbox record.
	HeaderEventType = "x-outbox-event-type"
)

// EventIDFromHeaders extracts the outbox event id from AMQP delivery headers,
// returning ok=false when the header is absent, empty, or not a string/[]byte.
// AMQP header values can arrive as either string or []byte depending on the
// broker and client, so both are normalized.
func EventIDFromHeaders(h amqp.Table) (string, bool) {
	raw, present := h[HeaderEventID]
	if !present {
		return "", false
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return "", false
		}
		return v, true
	case []byte:
		if len(v) == 0 {
			return "", false
		}
		return string(v), true
	default:
		return "", false
	}
}
