package streams

import "github.com/gaborage/go-bricks/messaging/internal/tenantstamp"

// TenantStampProperty is the application property the framework writes the
// publishing context's tenant into, and the one a consumer reads it back from.
// It is the same entry the classic lane uses as an AMQP 0.9.1 header.
const TenantStampProperty = tenantstamp.Header

// ErrTenantStampConflict reports a publish whose tenant stamp was supplied by the
// caller. The framework is the stamp's only writer on both lanes, so this is the
// same error value messaging.ErrTenantStampConflict names — errors.Is holds
// whichever lane raised it.
var ErrTenantStampConflict = tenantstamp.ErrConflict
