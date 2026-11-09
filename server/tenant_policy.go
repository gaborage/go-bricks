package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// TenantPolicy is the per-family tenant policy of a tenant-owned route family; TenantOwnedGroup
// lists the order its steps run in on each request.
type TenantPolicy struct {
	// Optional; runs first, in order, before any tenant exists.
	//
	// No tenant is in context while these steps run, so a publish from one is no
	// tenant's event. Under messaging.tenancy: shared it goes out as an unstamped
	// control-plane event. Under per-tenant messaging, deps.Messaging fails with
	// multitenant.ErrNoTenant (app.ErrNoTenantInContext); returned as is, that is a
	// server fault: a wiring bug. Under outbox.tenancy: shared, an AMQP-lane outbox
	// publish (RunInSharedTx) also goes out unstamped on the control-plane key,
	// whatever messaging.tenancy says; a stream-targeted one fails with
	// outbox.ErrStreamTargetRequiresTenant.
	Authenticate []MiddlewareFunc
	// Required; returns the tenant ID, never writes the response. It may c.Set values
	// for the handler and may return a 4xx to authorize.
	//
	// The same holds here: the tenant exists only after Tenant returns and the
	// framework publishes it. It holds in Deny too, which answers a fault raised
	// before any tenant exists.
	Tenant TenantFunc
	// Optional; the only writer of client-fault bytes; nil means DenyBodyless. It runs
	// for every client fault, at any status, after traceparent is stamped.
	Deny DenyFunc
}

// TenantFunc returns the tenant ID of a tenant-owned family's request (see TenantPolicy.Tenant).
type TenantFunc func(c HandlerContext) (tenantID string, err error)

// DenyFunc answers a client fault raised before the tenant exists (see TenantPolicy.Deny).
type DenyFunc func(c HandlerContext, fault error) error

// DenyBodyless is the default Deny. It writes no body and keeps a 4xx fault's own status,
// so the WWW-Authenticate header a 401 step set stands. Any other fault gets 403, because a
// 401 without WWW-Authenticate breaks RFC 7235. A bodyless-401 contract wraps it.
func DenyBodyless(c HandlerContext, fault error) error {
	status := errorStatus(fault)
	if !isClientStatus(status) {
		status = http.StatusForbidden
	}
	return c.echoContext().NoContent(status)
}

// ResolveForTest runs steps 1-3 and the fault rule on c (from NewHandlerContextForTest)
// without a router. denied is true only when Deny ran; the backstop's error stays unexported.
// When a step commits a response it returns no tenant, denied false and the chain's error as
// the step returned it (nil when it returned nil). It panics when p.Tenant is nil, before any
// Authenticate step runs.
func (p TenantPolicy) ResolveForTest(c HandlerContext) (tenantID string, denied bool, err error) {
	if p.Tenant == nil {
		panic("server: TenantPolicy.Tenant is nil")
	}
	return p.run(c, nil, nil, func() error { return nil })
}

const (
	reasonGrammarBackstop   = "tenant ID fails the tenant-ID grammar"
	reasonNoTenantPublished = "authenticate chain returned without publishing a tenant"
	reasonStepCommitted     = "a step before the publish committed a response"
	reasonNoTenantAccessor  = "per-tenant accessor called before the tenant exists"
	reasonDenyError         = "deny returned an error"
	reasonDenyNothing       = "deny returned without committing a response"
	reasonSeatNotSkipped    = "family route reached without the tenant seat's skip"
	reasonTenantReplaced    = "tenant replaced after the family published it"
)

// run runs Authenticate, Tenant, the grammar check, the existence check and the publish, then
// next innermost, so an error from next passes back up through the steps and is returned
// untouched. An id checker does not hold is the Tenant step's 400 client fault (ADR-172); a nil
// checker (ResolveForTest) skips the existence check.
func (p TenantPolicy) run(c HandlerContext, l logger.Logger, checker multitenant.TenantChecker, next func() error) (tenantID string, denied bool, err error) {
	var backstop *tenantFaultError
	chainErr := runSteps(c, p.Authenticate, func() error {
		id, tenantErr := p.Tenant(c)
		if tenantErr != nil {
			return tenantErr
		}
		// Validate before publishing: SetTenant leaves the context unchanged for "".
		if multitenant.ValidateTenantID(id) != nil {
			backstop = newTenantFault(c, l, reasonGrammarBackstop, nil)
			return backstop
		}
		if checker != nil && !holdsTenant(checker, id) {
			return NewUnknownTenantError(multitenant.ErrUnknownTenant)
		}
		published := multitenant.SetTenant(c.RequestContext(), id)
		c.SetRequestContext(published)
		c.Set(familyPublishedKey, published) // the leaf guard's reference tenant
		tenantID = id
		return next()
	})
	if tenantID != "" {
		return tenantID, false, chainErr
	}
	// Deny is the only byte writer: committed bytes stand, the fault is logged, and the chain's
	// error is returned untouched (the framework renders nothing over a committed response).
	if isResponseCommitted(c.echoContext()) {
		logTenantRefusal(c, l, reasonStepCommitted)
		return "", false, chainErr
	}
	if backstop != nil {
		return "", false, backstop
	}
	if chainErr == nil {
		return "", false, newTenantFault(c, l, reasonNoTenantPublished, nil)
	}
	denied, err = p.answer(c, l, chainErr)
	return "", denied, err
}

// runSteps runs steps in order, then inner. A step's next runs the rest once: a second call,
// or a call after the step committed a response (a wiring fault), returns nil and runs
// nothing, neither a later step nor inner.
func runSteps(c HandlerContext, steps []MiddlewareFunc, inner func() error) error {
	var call func(i int) error
	call = func(i int) error {
		if i == len(steps) {
			return inner()
		}
		called := false
		return steps[i](c, func() error {
			if called || isResponseCommitted(c.echoContext()) {
				return nil
			}
			called = true
			return call(i + 1)
		})
	}
	return call(0)
}

type tenantFaultClass uint8

const (
	faultRendered tenantFaultClass = iota
	faultNoTenant
	faultClient
)

// classifyTenantFault applies the fault rule to an error raised before the publish; the
// first matching rule wins.
func classifyTenantFault(err error) tenantFaultClass {
	status := errorStatus(err)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		isRequestTooLarge(err) || status == http.StatusRequestEntityTooLarge {
		return faultRendered
	}
	if errors.Is(err, multitenant.ErrNoTenant) {
		return faultNoTenant
	}
	if isClientStatus(status) {
		return faultClient
	}
	if status != 0 {
		return faultRendered
	}
	if errors.Is(err, multitenant.ErrTenantResolutionFailed) || errors.Is(err, multitenant.ErrInvalidTenantID) {
		return faultClient
	}
	return faultRendered
}

func (p TenantPolicy) answer(c HandlerContext, l logger.Logger, fault error) (denied bool, err error) {
	class := classifyTenantFault(fault)
	if class == faultClient {
		return true, p.deny(c, l, fault)
	}
	if class == faultNoTenant {
		return false, newTenantFault(c, l, reasonNoTenantAccessor, multitenant.ErrNoTenant)
	}
	return false, fault
}

func (p TenantPolicy) deny(c HandlerContext, l logger.Logger, fault error) error {
	ensureTraceParentHeader(c.echoContext())
	denyFn := p.Deny
	if denyFn == nil {
		denyFn = DenyBodyless
	}
	if denyErr := denyFn(c, fault); denyErr != nil {
		logTenantRefusal(c, l, reasonDenyError)
		return denyErr
	}
	if !isResponseCommitted(c.echoContext()) {
		return newTenantFault(c, l, reasonDenyNothing, nil)
	}
	return nil
}

// errorStatus returns the HTTP status err carries as an IAPIError or an echo.HTTPStatusCoder
// (*echo.HTTPError included), or 0. It reads an IAPIError as the error handler's
// classification does (apiErrorStatus), so a typed nil carries no status here either.
func errorStatus(err error) int {
	if apiErr, ok := errors.AsType[apiErrorInChain](err); ok {
		return apiErrorStatus(apiErr)
	}
	status, _ := echoErrorStatus(err)
	return status
}

func isClientStatus(status int) bool {
	return status >= http.StatusBadRequest && status < http.StatusInternalServerError
}

// tenantFaultError is a family fault the framework renders as an opaque 500. Its cause is
// multitenant.ErrNoTenant for rule 2 and nil otherwise, so the backstop never reads as
// ErrInvalidTenantID.
type tenantFaultError struct {
	*BaseAPIError
	reason string
	cause  error
}

// newTenantFault builds a tenantFaultError and logs its refusal line.
func newTenantFault(c HandlerContext, l logger.Logger, reason string, cause error) *tenantFaultError {
	logTenantRefusal(c, l, reason)
	return &tenantFaultError{
		BaseAPIError: NewBaseAPIError(errCodeInternalError, msgInternalErrorProd, http.StatusInternalServerError),
		reason:       reason,
		cause:        cause,
	}
}

func (f *tenantFaultError) Error() string { return "server: tenant-owned family fault: " + f.reason }

func (f *tenantFaultError) Unwrap() error { return f.cause }

// logTenantRefusal emits one ERROR line per family refusal, carrying the reason class and
// the masked matched template (maskedPath), never request path bytes, a tenant ID, a header
// value or an error message. With a nil logger it falls back to stdlib log, like
// logTenantRejection.
func logTenantRefusal(c HandlerContext, l logger.Logger, reason string) {
	msg := fmt.Sprintf("[server.tenant] family request refused: method=%s path=%s reason=%q",
		logSafeValue(c.Request().Method), logSafeValue(maskedPath(c.ectx)), reason)
	if l == nil {
		log.Printf("ERROR %s", msg)
		return
	}
	l.Error().Msg(msg)
}
