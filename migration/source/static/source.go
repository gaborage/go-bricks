// Package static provides a TenantLister that enumerates tenant IDs from a
// config-backed source (typically the YAML-driven multitenant.tenants block).
package static

import (
	"context"
	"errors"
	"sort"

	"github.com/gaborage/go-bricks/config"
)

// ErrNilStore is returned when ListTenants is called on a TenantSource whose
// underlying TenantStoreLister is nil.
var ErrNilStore = errors.New("migration/source/static: TenantStoreLister is nil")

// ErrEmptyTenantID is returned when the underlying tenant store contains an
// empty-string tenant ID. Callers can branch on this with errors.Is.
var ErrEmptyTenantID = errors.New("migration/source/static: tenant store contains empty tenant id")

// TenantStoreLister is the subset of *config.TenantStore that TenantSource
// requires. Defined as an interface so tests can substitute a fake without
// constructing a full store.
type TenantStoreLister interface {
	Tenants() map[string]config.TenantEntry
}

// TenantSource implements migration.TenantLister against a TenantStoreLister.
type TenantSource struct {
	store TenantStoreLister
}

// FromConfigStore wraps a *config.TenantStore (or any type that exposes
// Tenants()) as a TenantLister.
func FromConfigStore(store TenantStoreLister) *TenantSource {
	return &TenantSource{store: store}
}

// ListTenants returns the tenant IDs known to the underlying store, sorted
// alphabetically for deterministic CI output. An empty-string tenant ID is
// rejected with an error rather than silently leaking downstream — the
// migration runner would otherwise compose a malformed secret name like
// "gobricks/migrate/" and fail with a confusing fetch error.
func (s *TenantSource) ListTenants(_ context.Context) ([]string, error) {
	if s == nil || s.store == nil {
		return nil, ErrNilStore
	}
	tenants := s.store.Tenants()
	out := make([]string, 0, len(tenants))
	for id := range tenants {
		if id == "" {
			return nil, ErrEmptyTenantID
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
