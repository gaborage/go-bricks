package multitenant

import (
	"context"
	"errors"
)

// ErrNoTenant reports a context that carries no tenant. It is the error form of
// GetTenant's ok result, so every lane answers "no tenant" the same way.
var ErrNoTenant = errors.New("multitenant: no tenant in context")

// ctxKey ensures tenant context keys do not collide with external packages.
type ctxKey string

const tenantKey ctxKey = "tenant_id"

// SetTenant stores the tenant identifier in the provided context.
func SetTenant(ctx context.Context, tenantID string) context.Context {
	if tenantID == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantKey, tenantID)
}

// GetTenant extracts the tenant identifier from the context.
func GetTenant(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	value := ctx.Value(tenantKey)
	tenantID, ok := value.(string)
	if !ok || tenantID == "" {
		return "", false
	}
	return tenantID, true
}

// TenantID is GetTenant in error form: the tenant identifier, or ErrNoTenant.
func TenantID(ctx context.Context) (string, error) {
	tenantID, ok := GetTenant(ctx)
	if !ok {
		return "", ErrNoTenant
	}
	return tenantID, nil
}
