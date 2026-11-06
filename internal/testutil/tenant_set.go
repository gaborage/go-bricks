package testutil

// TenantSet is a test multitenant.TenantChecker holding exactly its keys: HasTenant answers
// true for a listed key and false for every other one, "" included.
type TenantSet map[string]struct{}

// HasTenant reports whether tenantID is one of s's keys.
func (s TenantSet) HasTenant(tenantID string) bool {
	_, ok := s[tenantID]
	return ok
}
