// Package tenantstamp owns the tenant stamp: the carrier entry that carries a
// tenant identity between a producer and a consumer. Both lanes share it — AMQP
// 0.9.1 headers on the classic lane, AMQP 1.0 application properties on the
// streams lane — so the header name, the write, the read and the conflict
// sentinel cannot drift apart.
package tenantstamp

import (
	"context"
	"errors"
	"fmt"

	"github.com/gaborage/go-bricks/multitenant"
)

// Header is the carrier entry the tenant stamp travels in.
const Header = "x-tenant-id"

// ErrConflict reports a caller that supplied the stamp itself. The framework is
// its only writer: a caller-supplied value would be an unauthenticated claim to
// act for a tenant, so the publish fails rather than being silently overwritten.
var ErrConflict = errors.New("messaging: " + Header +
	" is written by the framework from the context tenant; remove it from the caller's headers")

// ResolveForPublish is the whole publish-side rule in one call: refuse a
// caller-supplied stamp, then decide which tenant the framework writes.
//
// The order is the point. Refusing the caller's header first means the answer
// never depends on what the framework happened to resolve — a caller cannot
// discover the resolved tenant by guessing, and cannot have a lucky guess
// accepted. Both lanes call this rather than sequencing the two steps
// themselves, so the order cannot drift between them.
func ResolveForPublish(ctx context.Context, callerHeaders map[string]any, replayKey string) (string, error) {
	if err := CheckCallerHeaders(callerHeaders); err != nil {
		return "", err
	}
	return Resolve(ctx, replayKey)
}

// Resolve decides which tenant a publish is stamped with, from the two sources
// that can know one: the context tenant, and the replay key the client was
// pooled under (empty for a control-plane client, so under shared tenancy the
// context is the only source).
//
// The key is not a weaker source than the context — it was itself resolved from
// an authenticated context when the client was created — so a publish made on a
// per-tenant client from a context carrying no tenant is still stamped. What is
// refused is the two disagreeing: that is a caller publishing for one tenant on
// another's client, which no precedence can make correct.
//
// An empty return with a nil error means no tenant is in play at all: a
// control-plane event, which carries no stamp.
func Resolve(ctx context.Context, replayKey string) (string, error) {
	ctxID, ok := multitenant.GetTenant(ctx)
	switch {
	case !ok:
		return replayKey, nil
	case replayKey == "" || replayKey == ctxID:
		return ctxID, nil
	default:
		return "", ErrConflict
	}
}

// Write stamps id through set. An empty id writes nothing.
func Write(id string, set func(key string, value any)) {
	if id != "" {
		set(Header, id)
	}
}

// CheckCallerHeaders rejects any caller-supplied stamp, whatever its value.
//
// A value identical to the one the framework would write is refused too: the
// framework is the stamp's only writer, and a caller that sets the header is
// claiming a field it does not own even when it happens to guess right. Accepting
// the match would also make the rule depend on what the framework resolved, which
// a caller cannot see.
func CheckCallerHeaders(headers map[string]any) error {
	if _, ok := headers[Header]; ok {
		return ErrConflict
	}
	return nil
}

// Reasons a stamp cannot be used. ReasonMissing is the only one a tenant-optional
// consumer may run through: a stamp that is present but unusable is a defect
// whoever the consumer is.
const (
	ReasonMissing   = "missing"
	ReasonNotString = "not a string"
	ReasonInvalid   = "invalid"
)

// ReadError reports why a delivery's stamp is unusable.
//
// SECURITY: the stamp is producer-written and reaches the consumer unauthenticated,
// so this names the reason and the byte length only — never the value.
type ReadError struct {
	Reason string
	Len    int
	// Type is the carrier value's Go type, set only for ReasonNotString where a
	// byte length would be meaningless. Rendered with %T at the point of capture,
	// never the value itself.
	Type string
}

func (e *ReadError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("tenant stamp %s (%s)", e.Reason, e.Type)
	}
	return fmt.Sprintf("tenant stamp %s (%d bytes)", e.Reason, e.Len)
}

// Read returns the delivery's tenant stamp, or a *ReadError saying why it cannot
// be used. lookup resolves one carrier entry by name AND reports whether the entry
// was there at all, which lets each lane keep its own carrier type.
//
// Presence is separate from the value because only an ABSENT stamp is optional. A
// producer that writes the header with a nil value has written a stamp — a
// malformed one — and TenantOptional must not admit it, so the two cases cannot be
// collapsed into "the lookup returned nil".
func Read(lookup func(key string) (value any, present bool)) (string, error) {
	value, present := lookup(Header)
	if !present {
		return "", &ReadError{Reason: ReasonMissing}
	}

	id, ok := value.(string)
	if !ok {
		// The length of a non-string says nothing (it has none), and the value is
		// producer-written, so the type is the only safe diagnostic — ADR-081's rule
		// for a value whose provenance is not ours.
		return "", &ReadError{Reason: ReasonNotString, Type: fmt.Sprintf("%T", value)}
	}

	if err := multitenant.ValidateTenantID(id); err != nil {
		return "", &ReadError{Reason: ReasonInvalid, Len: len(id)}
	}

	return id, nil
}
