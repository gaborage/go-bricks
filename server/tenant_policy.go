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

// TenantPolicy is the per-family tenant policy of a tenant-owned route family. Its routes
// skip the root tenant seat: the framework runs Authenticate, then Tenant, then checks the
// ID with multitenant.ValidateTenantID and publishes it on the request context, then the
// rest of the route.
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
	return c.ectx.NoContent(status)
}

// ResolveForTest runs steps 1-3 and the fault rule on c (from NewHandlerContextForTest)
// without a router. denied is true only when Deny ran; the backstop's error stays unexported.
func (p TenantPolicy) ResolveForTest(c HandlerContext) (tenantID string, denied bool, err error) {
	return p.run(c, nil, func() error { return nil })
}

const (
	reasonGrammarBackstop   = "tenant ID fails the tenant-ID grammar"
	reasonNoTenantPublished = "authenticate chain returned without publishing a tenant"
	reasonNoTenantAccessor  = "per-tenant accessor called before the tenant exists"
	reasonDenyError         = "deny returned an error"
	reasonDenyNothing       = "deny returned without committing a response"
)

type tenantStage uint8

const (
	stageAuthenticate tenantStage = iota
	stageTenant
	stageBackstop
	stagePublished
)

// run runs Authenticate, Tenant, the grammar check and the publish, then next innermost,
// so an error from next passes back up through the steps and is returned untouched.
func (p TenantPolicy) run(c HandlerContext, l logger.Logger, next func() error) (tenantID string, denied bool, err error) {
	stage := stageAuthenticate
	var published string
	var backstop *tenantFaultError
	chainErr := runSteps(c, p.Authenticate, func() error {
		stage = stageTenant
		id, tenantErr := p.Tenant(c)
		if tenantErr != nil {
			return tenantErr
		}
		// Validate before publishing: SetTenant leaves the context unchanged for "".
		if multitenant.ValidateTenantID(id) != nil {
			stage, backstop = stageBackstop, newTenantFaultError(reasonGrammarBackstop, nil)
			return backstop
		}
		c.SetRequestContext(multitenant.SetTenant(c.RequestContext(), id))
		stage, published = stagePublished, id
		return next()
	})
	if stage == stagePublished {
		return published, false, chainErr
	}
	if stage == stageBackstop {
		return "", false, logTenantFault(c, l, backstop)
	}
	if chainErr == nil {
		return "", false, logTenantFault(c, l, newTenantFaultError(reasonNoTenantPublished, nil))
	}
	denied, err = p.answer(c, l, chainErr)
	return "", denied, err
}

func runSteps(c HandlerContext, steps []MiddlewareFunc, inner func() error) error {
	var call func(i int) error
	call = func(i int) error {
		if i == len(steps) {
			return inner()
		}
		return steps[i](c, func() error { return call(i + 1) })
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
	if status != 0 {
		if isClientStatus(status) {
			return faultClient
		}
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
		return false, logTenantFault(c, l, newTenantFaultError(reasonNoTenantAccessor, fault))
	}
	return false, fault
}

func (p TenantPolicy) deny(c HandlerContext, l logger.Logger, fault error) error {
	ensureTraceParentHeader(c.ectx)
	denyFn := p.Deny
	if denyFn == nil {
		denyFn = DenyBodyless
	}
	if denyErr := denyFn(c, fault); denyErr != nil {
		status := errorStatus(denyErr)
		if status == 0 {
			status = http.StatusInternalServerError
		}
		logTenantRefusal(c, l, status, reasonDenyError)
		return denyErr
	}
	if !isResponseCommitted(c.ectx) {
		return logTenantFault(c, l, newTenantFaultError(reasonDenyNothing, nil))
	}
	return nil
}

// errorStatus reads err's HTTP status in classifyError's order: IAPIError, then
// *echo.HTTPError, then echo.HTTPStatusCoder; 0 when it carries none.
func errorStatus(err error) int {
	var apiErr IAPIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatus()
	}
	return extractStatusFromError(err)
}

func isClientStatus(status int) bool {
	return status >= http.StatusBadRequest && status < http.StatusInternalServerError
}

// tenantFaultError is a family fault the framework renders as an opaque 500. Its cause is
// set only for ErrNoTenant, so the grammar backstop never reads as ErrInvalidTenantID.
type tenantFaultError struct {
	*BaseAPIError
	reason string
	cause  error
}

func newTenantFaultError(reason string, cause error) *tenantFaultError {
	return &tenantFaultError{
		BaseAPIError: NewBaseAPIError(errCodeInternalError, msgInternalErrorProd, http.StatusInternalServerError),
		reason:       reason,
		cause:        cause,
	}
}

func (f *tenantFaultError) Error() string { return "server: tenant-owned family fault: " + f.reason }

func (f *tenantFaultError) Unwrap() error { return f.cause }

func logTenantFault(c HandlerContext, l logger.Logger, f *tenantFaultError) *tenantFaultError {
	logTenantRefusal(c, l, http.StatusInternalServerError, f.reason)
	return f
}

// logTenantRefusal emits one ERROR line per family refusal, carrying the reason class and
// the matched route template, never a tenant ID, a header value or an error message. With
// a nil logger it falls back to stdlib log, like logTenantRejection.
func logTenantRefusal(c HandlerContext, l logger.Logger, status int, reason string) {
	msg := fmt.Sprintf("[server.tenant] family request refused: method=%s path=%s status=%d reason=%q",
		logSafeValue(c.Request().Method), logSafeValue(matchedRouteTemplate(c.ectx)), status, reason)
	if l == nil {
		log.Printf("ERROR %s", msg)
		return
	}
	l.Error().Msg(msg)
}
