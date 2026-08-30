package messaging

import "github.com/gaborage/go-bricks/messaging/internal/tenantstamp"

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
