package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"

	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// HeaderEventID is the AMQP header the outbox relay stamps with the event id a
// consumer dedups on. outbox.HeaderEventID aliases it; the constant lives here
// because Metadata.DedupKey reads it and outbox imports this package.
const HeaderEventID = "x-outbox-event-id"

// maxEventIDBytes bounds a ledger id. The grammar is ^[A-Za-z0-9_-]{1,128}$ —
// byte-for-byte the request-id grammar trace.ValidateRequestID enforces, reused
// rather than respelled. A UUID, a ULID, a KSUID and every base64url string fit;
// a sealed dedup key (`<SignFamily>:<jti>`) does not, because `:` is outside it —
// which is the point. An unsealed publisher can therefore never pre-insert a
// sealed message's key and make the legitimate delivery skip+ACK, whichever
// unsealed source it writes: the x-outbox-event-id header and the message_id
// property answer to this one grammar.
const maxEventIDBytes = 128

// ErrInvalidEventID is returned when an event id headed for the inbox ledger is
// outside ^[A-Za-z0-9_-]{1,128}$ — absent, empty, over 128 bytes, or carrying
// any other byte. Match it with errors.Is. The wrapped message names the byte
// LENGTH, never the id: the value is publisher-controlled and this error reaches
// logs and spans. A handler returning it takes the standard poison path
// (nack without requeue → DLQ); the remedy is to re-mint conforming ids or to
// move the producer to the sealed typed door.
var ErrInvalidEventID = errors.New("messaging: event id is outside the ledger grammar [A-Za-z0-9_-]{1,128}")

// ValidateEventID checks id against the ledger grammar. WireDedupKey runs it,
// so every wire key — Metadata.DedupKey's, on the header and on the message_id
// property alike, or one a consumer builds — passed it at construction.
func ValidateEventID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: absent or empty", ErrInvalidEventID)
	}
	if len(id) > maxEventIDBytes {
		return fmt.Errorf("%w: %d bytes, limit is %d", ErrInvalidEventID, len(id), maxEventIDBytes)
	}
	if gobrickstrace.ValidateRequestID(id) == "" {
		return fmt.Errorf("%w: a byte outside [A-Za-z0-9_-] (length %d)", ErrInvalidEventID, len(id))
	}
	return nil
}

// DedupKey is an inbox ledger key carrying which door produced it. A wire key
// comes from WireDedupKey and has passed ValidateEventID's grammar; a sealed key
// is composed by the sealed typed door alone — no exported function mints one.
// The zero value is invalid and inbox.ProcessOnce refuses it.
type DedupKey struct {
	key    string
	sealed bool
}

// String returns the key's persisted spelling: the wire id verbatim, or
// `<SignFamily>:<jti>` for a sealed key.
func (k DedupKey) String() string {
	return k.key
}

// Sealed reports whether the sealed typed door produced this key.
func (k DedupKey) Sealed() bool {
	return k.sealed
}

// WireDedupKey builds a ledger key from a wire-sourced or consumer-composed id,
// applying ValidateEventID's grammar at construction. A refused id returns the
// invalid zero DedupKey and an error wrapping ErrInvalidEventID. Its result is
// never Sealed, whatever the id spells.
func WireDedupKey(id string) (DedupKey, error) {
	if err := ValidateEventID(id); err != nil {
		return DedupKey{}, err
	}
	return DedupKey{key: id}, nil
}

// sealedDedupKey is the one constructor of a Sealed key; only
// Metadata.DedupKey's sealed branch calls it.
func sealedDedupKey(family, jti string) DedupKey {
	return DedupKey{key: family + ":" + jti, sealed: true}
}

// sealedDeliveryKey marks a handler context as running under the sealed typed
// door. Only the sealed handler sets it.
type sealedDeliveryKey struct{}

// IsSealedDelivery reports whether ctx belongs to a delivery the sealed typed
// door opened — the framework's own marker, unreachable from a header or from
// consumer code. The ledger door cross-checks a Sealed DedupKey against it.
//
// The marker travels with the handler's context: a handler that calls
// inbox.ProcessOnce from a goroutine or with a context NOT derived from the one
// it was handed (context.Background() instead of context.WithoutCancel(ctx))
// loses it and gets ErrInvalidEventID — fail closed. Derive the context.
func IsSealedDelivery(ctx context.Context) bool {
	marked, _ := ctx.Value(sealedDeliveryKey{}).(bool)
	return marked
}

// ValidateDedupKey checks a key at the ledger door. Admission is by the key's
// provenance, not its spelling: the zero DedupKey is refused, and a Sealed key
// is refused under a context IsSealedDelivery does not mark (defense in depth —
// only the sealed door mints one, and only inside its own handler). A wire key
// passes under either context; its grammar ran when WireDedupKey built it. Both
// refusals wrap ErrInvalidEventID and never carry the key.
func ValidateDedupKey(ctx context.Context, key DedupKey) error {
	if key.String() == "" {
		return fmt.Errorf("%w: zero DedupKey", ErrInvalidEventID)
	}
	if key.Sealed() && !IsSealedDelivery(ctx) {
		return fmt.Errorf("%w: sealed dedup key outside a sealed delivery", ErrInvalidEventID)
	}
	return nil
}

// SealedEnvelope is what a sealed (JWE-of-JWS) message's protected header
// asserts about itself once the framework has verified it. Plain data: a
// consumer that never seals reads this type without linking go-jose (the jose
// side has its own envelope type; the sealed door maps between them). It is
// reachable only through Metadata.Sealed, filled by the sealed typed door
// (DeclareTypedConsumerWithMeta on a seal-tagged T) and zero everywhere else.
type SealedEnvelope struct {
	// JTI is the token id the sealed dedup key `<SignFamily>:<jti>` is built from.
	JTI string
	// IssuedAt is the protected header's iat claim.
	IssuedAt time.Time
	// EventType is the event type asserted INSIDE the envelope, which the
	// framework has matched against the delivery's wire-level type.
	EventType string
	// TenantID is the tenant asserted inside the envelope (empty single-tenant).
	TenantID string
	// SignKid and SignFamily identify the verifying key and its key family.
	SignKid    string
	SignFamily string
	// EncKid identifies the key the envelope was decrypted with.
	EncKid string
}

// Sealed reports whether this delivery arrived through the sealed typed door
// and, when it did, what its verified envelope asserts. The answer is a property
// of the consumer TYPE, never of the message: a sealed consumer gets (envelope,
// true) for every delivery it runs — the opener refused every other one before
// the handler — and a plain typed consumer gets (zero, false) for every
// delivery, whatever headers the publisher wrote, so a handler branching on ok
// cannot be steered by a caller-written header.
func (m Metadata) Sealed() (SealedEnvelope, bool) {
	if m.sealed == nil {
		return SealedEnvelope{}, false
	}
	return *m.sealed, true
}

// DedupKey returns the key the inbox ledger should be keyed on for this
// delivery. For a sealed consumer it is a Sealed key spelled
// `<SignFamily>:<jti>` — the Logical sign family, never the concrete
// Generation, so a rotation does not re-open the replay window — composed from
// the verified envelope; that branch always returns a nil error.
//
// For a plain typed consumer it is a wire key (WireDedupKey) holding the
// x-outbox-event-id header, or — when the delivery carries no such header at
// all — the AMQP message_id property, so a producer that follows the standard
// without being go-bricks is still processable through inbox.ProcessOnce. The
// stamp is tried
// first and a stamp that is present but malformed errors rather than falling
// through: on a go-bricks producer the stamp is framework-written while the
// property is caller-written, so a caller must not be able to shadow it by
// spoiling it. The error wraps ErrInvalidEventID when both are absent, or the
// chosen one is empty, over 128 bytes, or carries a byte outside
// [A-Za-z0-9_-].
//
// The framework validates the SHAPE of either source, never its uniqueness:
// AMQP obliges no producer to make message_id unique per message, so a producer
// reusing one across distinct events makes the ledger skip them as duplicates.
// A queue whose producer does that wants the stamp, or the consumer's own key.
//
// Return the error from the handler: the delivery is nacked without requeue,
// like any other poison message. AMQP header values arrive as string or []byte
// depending on the broker and client, so both are accepted.
func (m Metadata) DedupKey() (DedupKey, error) {
	if m.sealed != nil {
		return sealedDedupKey(m.sealed.SignFamily, m.sealed.JTI), nil
	}
	id := m.MessageID()
	if stamp, stamped := m.Headers()[HeaderEventID]; stamped {
		id = headerString(stamp)
	}
	return WireDedupKey(id)
}

// headerString renders an AMQP header value that should carry text. Values
// arrive as string or []byte depending on the broker and client; anything else
// is not a spelling of an id.
func headerString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	}
	return ""
}
