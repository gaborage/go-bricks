package outbox

import (
	"strings"

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

// reservedHeaderPrefix is the framework's own namespace inside a row's persisted
// headers: every stamp below is spelled under it, and Publish refuses a caller
// header that claims it (ErrReservedHeaderPrefix). One prefix, so a stamp added
// later inherits the refusal instead of needing its own.
const reservedHeaderPrefix = "x-gobricks-"

// headerContentTypeStamp names the encoding of a row's payload, resolved at
// enqueue (marshalPayload is the only point that knows it) and carried in the
// persisted headers so the relay can set the AMQP content_type property
// (ADR-105) without a schema change. Spelled from reservedHeaderPrefix so the
// stamp and the prefix Publish refuses cannot drift apart.
const headerContentTypeStamp = reservedHeaderPrefix + "content-type"

// isReservedHeaderKey reports whether key claims the framework's namespace. AMQP
// header names are caller-written text, so the prefix is matched case-folded: an
// exact-key check would let X-GoBricks-Content-Type through to the ledger, where
// nothing downstream re-normalizes it.
func isReservedHeaderKey(key string) bool {
	return len(key) >= len(reservedHeaderPrefix) &&
		strings.EqualFold(key[:len(reservedHeaderPrefix)], reservedHeaderPrefix)
}

// firstReservedHeader returns some caller header key claiming the reserved
// prefix, if any. Which one, when several do, is unspecified — one refusal names
// one key, and the caller renames them all.
func firstReservedHeader(headers map[string]any) (key string, found bool) {
	for k := range headers {
		if isReservedHeaderKey(k) {
			return k, true
		}
	}
	return "", false
}

// takeFrameworkStamps removes the framework's own bookkeeping headers and
// returns what they said: the tenant stamp and the payload's encoding. Both
// lanes call it, because a header left behind reaches the wire — the tenant
// stamp additionally fails the publish, since the conflict check keys on the
// header existing at all rather than on a non-empty value. One seam so a third
// stamp is one edit rather than two silent omissions.
//
// It carries the framework's own stamps off the row, and it is also all that
// stands between the wire and a row enqueued BEFORE Publish began refusing the
// reserved prefix, which the refusal cannot reach.
func takeFrameworkStamps(headers map[string]any) (stamp, contentType string) {
	stamp, _ = headers[messaging.TenantStampHeader].(string)
	delete(headers, messaging.TenantStampHeader)
	// Read the encoding from the CANONICAL key only. marshalHeaders is the sole
	// writer of this namespace and always spells it canonically, so any other
	// casing came from a caller — exactly the value the enqueue refusal exists
	// to reject, and reading it would reinstate the mislabelling by the back
	// door. Matching case-insensitively here would also make the result depend
	// on map iteration order for a row carrying two spellings.
	contentType, _ = headers[headerContentTypeStamp].(string)
	// Deleted case-insensitively all the same: a row enqueued BEFORE Publish
	// began refusing the prefix can carry any casing, and a header left behind
	// reaches the wire as one the framework claims to own. Deleting during the
	// range is defined behavior.
	for key := range headers {
		if isReservedHeaderKey(key) {
			delete(headers, key)
		}
	}
	return stamp, contentType
}

// EventIDFromHeaders extracts the outbox event id from AMQP delivery headers,
// returning ok=false when the header is absent, empty, or not a string/[]byte.
// AMQP header values can arrive as either string or []byte depending on the
// broker and client, so both are normalized. The value is extracted, not
// validated: messaging.WireDedupKey applies the ledger grammar when it builds
// the key inbox.ProcessOnce takes, and messaging.Metadata.DedupKey validates as
// it extracts.
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
