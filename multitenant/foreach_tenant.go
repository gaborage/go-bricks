package multitenant

import (
	"context"
	"errors"

	"github.com/gaborage/go-bricks/internal/leasescope"
)

// ForEachTenant runs fn once per tenant, in the given order, each inside that tenant's own lease
// scope (ADR-032): the tenant's context carries the tenant (SetTenant; "" runs fn once with no
// tenant) and a fresh scope that shadows any enclosing request, message or job scope, drained when
// fn returns or panics. A tenant-sweeping job then holds about one tenant's leased handles at a
// time instead of every tenant's until the job returns.
//
// ctx is checked before each tenant: once it is done no further tenant starts and ctx.Err() joins
// the result. Errors from fn are joined unwrapped (fn receives the tenant ID and owns its text) and
// the sweep continues. A panic in fn is not recovered; it propagates after that tenant's scope has
// drained. A nil or empty list returns nil.
//
// Every borrow inside fn must use fn's ctx or a context derived from it: a borrow on the outer
// context lands in the enclosing scope and is not bounded. A handle, database.Session or
// transaction obtained inside fn must not be used after fn returns. Releasing a lease does not
// close a cached handle, so the bound matters only when a sweep covers more tenants than the
// manager's max size. The caller supplies the list: Config.PerTenantJobKeys() for static tenants,
// or its own list for dynamic sources.
func ForEachTenant(ctx context.Context, tenants []string, fn func(ctx context.Context, tenantID string) error) error {
	var errs []error
	for _, tenantID := range tenants {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := runInTenantScope(ctx, tenantID, fn); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func runInTenantScope(ctx context.Context, tenantID string, fn func(context.Context, string) error) error {
	tenantCtx, scope := leasescope.Install(SetTenant(ctx, tenantID))
	defer scope.ReleaseAll()
	return fn(tenantCtx, tenantID)
}
