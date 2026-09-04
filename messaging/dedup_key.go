package messaging

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
// which is the point. An unsealed publisher writing x-outbox-event-id can
// therefore never pre-insert a sealed message's key and make the legitimate
// delivery skip+ACK.
const maxEventIDBytes = 128

// ErrInvalidEventID is returned when an event id headed for the inbox ledger is
// outside ^[A-Za-z0-9_-]{1,128}$ — absent, empty, over 128 bytes, or carrying
// any other byte. Match it with errors.Is. The wrapped message names the byte
// LENGTH, never the id: the value is publisher-controlled and this error reaches
// logs and spans. A handler returning it takes the standard poison path
// (nack without requeue → DLQ); the remedy is to re-mint conforming ids or to
// move the producer to the sealed typed door.
var ErrInvalidEventID = errors.New("messaging: event id is outside the ledger grammar [A-Za-z0-9_-]{1,128}")

// ValidateEventID checks id against the ledger grammar. Every framework path
// that turns a header into a ledger id runs it — Metadata.DedupKey here and
// inbox.ProcessOnce at the ledger door — so consumer code never has to.
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

// sealedDedupKeyPattern is the sealed dedup key grammar `<SignFamily>:<jti>`:
// a Logical kid (the jose kid alphabet, at most 64 characters) and a signed
// jti in the header-id grammar, joined by the one byte neither side may
// contain. Spelled here rather than imported: the grammar must be checkable
// by a build that never links the codec.
var sealedDedupKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}:[A-Za-z0-9_-]{1,128}$`)

// IsSealedDedupKey reports whether key has the sealed `<SignFamily>:<jti>`
// shape. A header-sourced id can never satisfy it: `:` is outside the header
// grammar ValidateEventID enforces.
func IsSealedDedupKey(key string) bool {
	return sealedDedupKeyPattern.MatchString(key)
}

// sealedDeliveryKey marks a handler context as running under the sealed typed
// door. Only the sealed handler sets it, so a sealed-shaped key reaching the
// ledger from any other context is a header forgery, not a sealed message.
type sealedDeliveryKey struct{}

// IsSealedDelivery reports whether ctx belongs to a delivery the sealed typed
// door opened — the framework's own marker, unreachable from a header or from
// consumer code, which is what lets the ledger door admit a sealed key from a
// sealed consumer while still refusing the same spelling from a header.
//
// The marker travels with the handler's context: a handler that calls
// inbox.ProcessOnce from a goroutine or with a context NOT derived from the one
// it was handed (context.Background() instead of context.WithoutCancel(ctx))
// loses it and gets ErrInvalidEventID — fail closed. Derive the context.
func IsSealedDelivery(ctx context.Context) bool {
	marked, _ := ctx.Value(sealedDeliveryKey{}).(bool)
	return marked
}

// ValidateDedupKey checks a key at the ledger door, whichever door produced
// it: under a sealed delivery (IsSealedDelivery) a sealed `<SignFamily>:<jti>`
// key passes; everywhere, a header id passes under ValidateEventID's grammar.
// Anything else wraps ErrInvalidEventID and names the byte length only — so a
// publisher spelling a sealed key into x-outbox-event-id on an unsealed
// consumer is refused before the ledger, exactly as before sealing existed.
func ValidateDedupKey(ctx context.Context, key string) error {
	if IsSealedDelivery(ctx) && IsSealedDedupKey(key) {
		return nil
	}
	return ValidateEventID(key)
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

// DedupKey returns the id the inbox ledger should be keyed on for this
// delivery. For a sealed consumer it is `<SignFamily>:<jti>` — the Logical sign
// family, never the concrete Generation, so a rotation does not re-open the
// replay window — composed from the verified envelope and never an error. For
// a plain typed consumer it is the grammar-validated x-outbox-event-id header;
// the error wraps ErrInvalidEventID when the header is absent, empty, over 128
// bytes, or carries a byte outside [A-Za-z0-9_-]. Return it from the handler:
// the delivery is nacked without requeue, like any other poison message. AMQP
// header values arrive as string or []byte depending on the broker and client,
// so both are accepted.
func (m Metadata) DedupKey() (string, error) {
	if m.sealed != nil {
		return m.sealed.SignFamily + ":" + m.sealed.JTI, nil
	}
	var id string
	switch v := m.Headers()[HeaderEventID].(type) {
	case string:
		id = v
	case []byte:
		id = string(v)
	}
	if err := ValidateEventID(id); err != nil {
		return "", err
	}
	return id, nil
}
