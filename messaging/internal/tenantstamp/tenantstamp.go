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

// CheckCallerHeaders rejects caller-supplied headers whose stamp is not the one
// the framework resolved. An identical value is accepted rather than refused:
// it claims nothing the framework was not already going to write.
func CheckCallerHeaders(headers map[string]any, stamp string) error {
	value, ok := headers[Header]
	if !ok {
		return nil
	}
	if id, isString := value.(string); isString && id == stamp && stamp != "" {
		return nil
	}
	return ErrConflict
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
}

func (e *ReadError) Error() string {
	return fmt.Sprintf("tenant stamp %s (%d bytes)", e.Reason, e.Len)
}

// Read returns the delivery's tenant stamp, or a *ReadError saying why it cannot
// be used. get resolves one carrier entry by name, which lets each lane keep its
// own carrier type.
func Read(get func(key string) any) (string, error) {
	value := get(Header)
	if value == nil {
		return "", &ReadError{Reason: ReasonMissing}
	}

	id, ok := value.(string)
	if !ok {
		return "", &ReadError{Reason: ReasonNotString}
	}

	if err := multitenant.ValidateTenantID(id); err != nil {
		return "", &ReadError{Reason: ReasonInvalid, Len: len(id)}
	}

	return id, nil
}
