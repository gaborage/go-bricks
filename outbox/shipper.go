package outbox

import (
	"context"
	"time"
)

// shipper is the relay's per-lane adapter (ADR-088): it pre-flights its lane, plans one
// record client-free, ships that plan once, and classifies its own failure. Everything the
// relay owns — leadership, the ledger writes, parking, the publish bound, the cycle log —
// stays outside it, so a lane is added by writing one of these and nothing else.
type shipper interface {
	// Ready is the lane's pre-flight, called once per lane per cycle and never per
	// record. A nil error means the lane is usable; the error it returns is the one
	// the cycle reports for a lane-wide outage.
	Ready(ctx context.Context) error
	// Plan turns one record into the shipment Ship will carry. It is total and
	// client-free: every record yields a shipment whose Key is set, and a record the
	// lane can already tell is unshippable comes back with Poison naming why. headers
	// may be nil — the relay asks for a key that way when a row's own headers would not
	// decode.
	Plan(rec *Record, headers map[string]any) shipment
	// Ship makes exactly one delivery attempt on an already-bounded context. It writes
	// nothing to the store and logs nothing. The shipment is passed by pointer because it
	// is 80 bytes and the relay ships one per record; a lane must not mutate it.
	Ship(ctx context.Context, s *shipment) verdict
}

// shipment is one planned delivery. ContentType names the payload's encoding, read
// out of the row's headers by the lane that planned it and empty when the row carries
// no such stamp; Key is the ordering key the row parks under, namespaced
// by its lane and always set, so even an unshippable row holds its key's order; Poison is
// empty for a shippable row and otherwise carries the reason the lane refused it without
// reaching a broker; Scope names the sub-scope a stall holds back, and is set on every
// shipment a lane can report shipScopeDown for ("" on a lane whose failures take the whole
// lane); Stamp is the row's tenant, which the relay moves onto the publish context because
// the framework is the stamp's only header writer (ADR-087).
type shipment struct {
	Record      *Record
	Headers     map[string]any
	ContentType string
	Key         string
	Poison      string
	Scope       string
	Stamp       string
}

// verdictKind is one lane's reading of one delivery attempt.
type verdictKind int

const (
	shipDelivered  verdictKind = iota // the broker took responsibility for the message
	shipRetry                         // connectivity: advance retry_count, park the key
	shipPoison                        // message-intrinsic: dead-letter at MaxRetries
	shipAborted                       // shutdown/cancel: count nothing, stop the batch
	shipBrokerDown                    // the lane itself dropped mid-batch
	shipScopeDown                     // this shipment's Scope is not carrying messages
)

// verdict is what one Ship reports back. Err carries the failure the relay writes into the
// ledger — the connectivity error on retry, brokerDown and scopeDown, and the poison text on
// shipPoison. Waited is the wall time spent inside Ship and is set only on shipBrokerDown and
// shipScopeDown, the two the cycle log reports as its stall.
type verdict struct {
	Kind   verdictKind
	Err    error
	Waited time.Duration
}
