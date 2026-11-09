package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// msgInvalidTenant is the 400 message for a malformed tenant and for an unknown one.
const msgInvalidTenant = "Invalid tenant"

// TenantMiddleware resolves the tenant ID and injects it into the request context.
// If a skipper function is provided, certain routes (e.g., health probes) can bypass tenant resolution.
// If skipper is nil, all routes will undergo tenant resolution.
//
// The returned MiddlewareFunc is the framework-neutral (echo-free) form; the
// echo-native logic lives in tenantMiddlewareEcho, which setupMiddlewares wires
// directly on the default request path (ADR-026, no per-request baton).
// Constructed here with a nil logger — see tenantMiddlewareEcho for the
// framework-logger form.
//
// exists may be nil only when the resolver cannot read the request path (multitenant.MayReadPath:
// the framework's header and subdomain resolvers, alone, validated or composed). For any other
// resolver a nil exists panics here, at construction, as does a non-nil exists that cannot answer
// (vetExistence).
func TenantMiddleware(resolver multitenant.TenantResolver, skipper SkipperFunc, exists multitenant.TenantChecker) MiddlewareFunc {
	if exists == nil && multitenant.MayReadPath(resolver) {
		panic("server: TenantMiddleware: the resolver may read the request path, so a tenant existence check is required (ADR-172): " +
			"pass the multitenant.TenantChecker that serves your tenants' resources")
	}
	if exists != nil {
		if err := vetExistence(exists); err != nil {
			panic("server: TenantMiddleware: " + err.Error())
		}
	}
	return fromEchoMiddleware(tenantMiddlewareEcho(resolver, skipperFromRequest(skipper), seatInputs{checker: exists}, nil))
}

// tenantMiddlewareEcho is the echo-native tenant-resolution middleware constructor.
// Public callers use TenantMiddleware (echo-free, nil logger); setupMiddlewares
// passes its framework logger so rejected (400) requests leave a structured WARN
// trail — this middleware is registered outer to the access logger
// (server/middleware.go), so a short-circuited request never reaches it and would
// otherwise leave zero server-side trail when observability is disabled.
// The skipper is the echo-native form (see probeSkipper): the framework passes the
// template-keyed probe predicate, and a public SkipperFunc arrives through
// skipperFromRequest. in.owned reports whether a matched route template belongs to a tenant-owned
// family, whose own steps resolve the tenant: the seat skips it and leaves seatSkippedKey as
// evidence. A nil owned skips nothing. An id read from the path (multitenant.Resolve) is
// written only when in.checker holds it; any other one is never written, and the request
// carries the id-free unknownPathTenantKey instead, for the handler adapter to refuse
// (ADR-170). A nil checker holds no id. l may be nil, in which case the warning falls back
// to the stdlib log package (mirrors corsWarnf).
func tenantMiddlewareEcho(resolver multitenant.TenantResolver, skipper probeSkipper, in seatInputs, l logger.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// Skip tenant resolution for specified routes
			if skipper != nil && skipper(c) {
				return next(c)
			}
			if in.owned != nil && in.owned(c.Path()) {
				c.Set(seatSkippedKey, true)
				return next(c)
			}
			return seatTenant(c, next, resolver, in, l)
		}
	}
}

// seatTenant resolves the request's tenant and writes it, or refuses the request: a resolution
// failure answers 400 here, and a path-read id in.checker does not hold leaves only
// unknownPathTenantKey for the handler adapter (see tenantMiddlewareEcho).
func seatTenant(c *echo.Context, next echo.HandlerFunc, resolver multitenant.TenantResolver, in seatInputs, l logger.Logger) error {
	if resolver == nil {
		return NewInternalServerError("tenant resolver not configured")
	}

	tenantID, fromPath, err := multitenant.Resolve(c.Request().Context(), resolver, c.Request())
	if err != nil || tenantID == "" {
		logTenantRejection(l, c, tenantRejectionReason(err))
		return NewBadRequestError(msgInvalidTenant)
	}
	if fromPath && !in.holds(tenantID) || conflictsWithSeated(c, tenantID) {
		c.Set(unknownPathTenantKey, true)
		return next(c)
	}

	ctx := multitenant.SetTenant(c.Request().Context(), tenantID)
	c.SetRequest(c.Request().WithContext(ctx))
	return next(c)
}

// seatSkippedKey marks a request the tenant seat skipped because a tenant-owned family owns its
// matched route.
const seatSkippedKey = "_tenant_seat_skipped"

// unknownPathTenantKey marks a request whose path-read tenant the seat's TenantChecker does not
// vouch for (ADR-170), or whose resolved tenant disagrees with one already in context (ADR-172).
// It carries no id, and the seat writes none: the request runs on with the context it had until
// the route's handler adapter refuses it (refuseUnknownPathTenant).
const unknownPathTenantKey = "_tenant_unknown_path"

// conflictsWithSeated reports whether the request already carries a tenant other than tenantID:
// two resolvers that disagree are refused like an unknown path tenant, and neither id is written.
func conflictsWithSeated(c *echo.Context, tenantID string) bool {
	seated, ok := multitenant.GetTenant(c.Request().Context())
	return ok && seated != tenantID
}

// holds reports whether in.checker holds tenantID (holdsTenant).
func (in seatInputs) holds(tenantID string) bool {
	return holdsTenant(in.checker, tenantID)
}

// holdsTenant reports whether checker holds tenantID; a nil checker holds none, so path tenancy
// without a checker fails closed. Only a plain tenant key is asked about: a key carrying
// config.NamedDatabasePrefix is no tenant id, whatever the checker would answer.
func holdsTenant(checker multitenant.TenantChecker, tenantID string) bool {
	return checker != nil && !strings.HasPrefix(tenantID, config.NamedDatabasePrefix) && checker.HasTenant(tenantID)
}

// refuseUnknownPathTenant is the handler adapters' refusal of a request the seat marked
// (unknownPathTenantKey): 400 "Invalid tenant" through the unknown-tenant door, whose writer
// logs the tenant WARN with reason="unknown tenant" and the masked path. It runs after
// route-group middleware and JOSE inbound verification, before binding, and answers nil for
// every request the seat did not mark.
func refuseUnknownPathTenant(c *echo.Context) *UnknownTenantError {
	if unknown, _ := c.Get(unknownPathTenantKey).(bool); !unknown {
		return nil
	}
	return NewUnknownTenantError(multitenant.ErrUnknownTenant)
}

// tenantRejectionReason names the seat's failure class (empty tenant, untrusted
// X-Forwarded-Host peer, or resolver error), never the resolver error's message.
func tenantRejectionReason(resolveErr error) string {
	if errors.Is(resolveErr, multitenant.ErrUntrustedForwardedHost) {
		return "X-Forwarded-Host from a peer outside server.trustedproxies"
	}
	if resolveErr != nil {
		return "resolver error"
	}
	return "empty tenant"
}

// logTenantRejection emits one correlated WARN per request refused for its tenant (400),
// carrying the reason class and the masked matched template (maskedPath), never request path
// bytes, a tenant ID or an error message. With a nil logger (public TenantMiddleware
// construction) it falls back to stdlib log, like corsWarnf. A disabled WARN level returns
// before the line is formatted or the client address read.
func logTenantRejection(l logger.Logger, c *echo.Context, reason string) {
	ev, ok := openWarn(l)
	if !ok {
		return
	}
	req := c.Request()
	warnCorrelated(ev, c, fmt.Sprintf("[server.tenant] request rejected: invalid tenant method=%s path=%s client=%s status=%d reason=%q",
		logSafeValue(req.Method), logSafeValue(maskedPath(c)), logSafeValue(clientAddress(c)), http.StatusBadRequest, reason))
}

// logUnknownTenantRefusal writes the one line an UnknownTenantError leaves. A 400
// logs the tenant-rejection WARN with the masked path, since a path resolver
// puts the tenant ID in the URL. A 500 (a cause that is not the sentinel) logs
// classifyError's unhandled-error line, the error type only outside debug.
func logUnknownTenantRefusal(apiErr *UnknownTenantError, c *echo.Context, l logger.Logger, debug bool) {
	if apiErr.HTTPStatus() == http.StatusBadRequest {
		logTenantRejection(l, c, "unknown tenant")
		return
	}
	logUnhandledError(l, c, apiErr.cause, debug)
}

// logUnhandledError writes the 5xx "unhandled error" line, correlated (correlateLine), the
// error type only outside debug; with a nil logger it falls back to stdlib log.
func logUnhandledError(l logger.Logger, c *echo.Context, err error, debug bool) {
	lc := correlateLine(c)
	if l == nil {
		log.Printf("ERROR unhandled error error_type=%T%s", err, lc.text())
		return
	}
	appendErrorDetail(lc.stamp(l.Error()), err, debug).Msg("unhandled error")
}
