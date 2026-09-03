package messaging

import (
	"context"

	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
)

// TenantStampHeader is the header the framework writes the publishing context's
// tenant into, and the one a consumer reads it back from.
const TenantStampHeader = tenantstamp.Header

// ErrTenantStampConflict reports a publish whose tenant stamp was supplied by the
// caller and disagrees with the one the framework resolved. The framework is the
// stamp's only writer: a caller-supplied value is an unauthenticated claim to act
// for a tenant, so the publish fails rather than being silently overwritten.
//
// The streams lane re-exports this same value, so errors.Is holds across lanes.
var ErrTenantStampConflict = tenantstamp.ErrConflict

// ResolveTenantStamp is the publish-side stamp rule for a writer that persists a
// message for later delivery instead of publishing it now — the outbox, whose
// Publish must snapshot the tenant while the originating context is still live,
// the same way it snapshots the trace keys (ADR-087 §3).
//
// It applies the exact rule stampingPublisher applies: a caller-supplied
// TenantStampHeader in headers is refused with ErrTenantStampConflict (equal
// value included), then the context tenant is the stamp. There is no pool key to
// fall back on, so the context is the only source; an empty stamp with a nil
// error means no tenant is in play and nothing must be written.
func ResolveTenantStamp(ctx context.Context, headers map[string]any) (string, error) {
	return tenantstamp.ResolveForPublish(ctx, headers, "")
}
