package multitenant

import "context"

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
