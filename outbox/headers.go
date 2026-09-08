package outbox

import (
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/messaging"
)

// AMQP delivery header names stamped by the relay for consumer idempotency.
// The relay references these when publishing, so consumers can dedupe without
// re-declaring the literals.
const (
	// HeaderEventID carries the unique outbox event id (a UUID) used for
	// consumer-side deduplication. The literal lives in messaging so
	// Metadata.DedupKey can read it; this is the same constant.
	HeaderEventID = messaging.HeaderEventID

	// HeaderEventType carries the event type of the published outbox record.
	HeaderEventType = "x-outbox-event-type"
)

// headerContentTypeStamp names the encoding of a row's payload, resolved at
// enqueue (marshalPayload is the only point that knows it) and carried in the
// persisted headers so the relay can set the AMQP content_type property
// (ADR-105) without a schema change. The x-gobricks- namespace makes a caller
// collision implausible, not impossible: nothing validates caller header keys,
// so a caller header spelled exactly this way is dropped at enqueue — before
// the framework writes its own value, and whether or not there is one to write
// — and its value never reaches a consumer.
const headerContentTypeStamp = "x-gobricks-content-type"

// takeFrameworkStamps removes the framework's own bookkeeping headers and
// returns what they said: the tenant stamp and the payload's encoding. Both
// lanes call it, because a header left behind reaches the wire — the tenant
// stamp additionally fails the publish, since the conflict check keys on the
// header existing at all rather than on a non-empty value. One seam so a third
// stamp is one edit rather than two silent omissions.
func takeFrameworkStamps(headers map[string]any) (stamp, contentType string) {
	stamp, _ = headers[messaging.TenantStampHeader].(string)
	delete(headers, messaging.TenantStampHeader)
	contentType, _ = headers[headerContentTypeStamp].(string)
	delete(headers, headerContentTypeStamp)
	return stamp, contentType
}

// EventIDFromHeaders extracts the outbox event id from AMQP delivery headers,
// returning ok=false when the header is absent, empty, or not a string/[]byte.
// AMQP header values can arrive as either string or []byte depending on the
// broker and client, so both are normalized. The value is extracted, not
// validated: inbox.ProcessOnce refuses an id outside the ledger grammar at the
// ledger door, and messaging.Metadata.DedupKey validates as it extracts.
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
