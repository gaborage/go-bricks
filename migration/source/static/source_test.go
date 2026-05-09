package static

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

type fakeStore struct {
	t map[string]config.TenantEntry
}

func (f *fakeStore) Tenants() map[string]config.TenantEntry { return f.t }

func TestStaticTenantSourceListTenantsReturnsSorted(t *testing.T) {
	store := &fakeStore{t: map[string]config.TenantEntry{
		"zeta":  {},
		"alpha": {},
		"mid":   {},
	}}

	src := FromConfigStore(store)
	got, err := src.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "mid", "zeta"}, got)
}

func TestStaticTenantSourceListTenantsEmpty(t *testing.T) {
	src := FromConfigStore(&fakeStore{t: map[string]config.TenantEntry{}})
	got, err := src.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestStaticTenantSourceWrapsRealConfigStore(t *testing.T) {
	cfg := &config.Config{
		Multitenant: config.MultitenantConfig{
			Enabled: true,
			Tenants: map[string]config.TenantEntry{
				"acme":   {Database: config.DatabaseConfig{Type: "postgresql"}},
				"globex": {Database: config.DatabaseConfig{Type: "oracle"}},
			},
		},
	}
	store := config.NewTenantStore(cfg)

	src := FromConfigStore(store)
	got, err := src.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"acme", "globex"}, got)
}
