package messaging

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/multitenant"
)

func TestResolveTenantStampUsesTheContextTenant(t *testing.T) {
	ctx := multitenant.SetTenant(context.Background(), "acme")

	stamp, err := ResolveTenantStamp(ctx, map[string]any{"x-priority": "high"})

	require.NoError(t, err)
	assert.Equal(t, "acme", stamp)
}

func TestResolveTenantStampIsEmptyWithoutATenant(t *testing.T) {
	stamp, err := ResolveTenantStamp(context.Background(), nil)

	require.NoError(t, err)
	assert.Empty(t, stamp, "no tenant in play means nothing to write")
}

func TestResolveTenantStampRefusesACallerSuppliedStamp(t *testing.T) {
	tests := []struct {
		name   string
		tenant string
		value  any
	}{
		{name: "equal_to_the_context_tenant", tenant: "acme", value: "acme"},
		{name: "different_from_the_context_tenant", tenant: "acme", value: "beta"},
		{name: "without_a_context_tenant", tenant: "", value: "acme"},
		{name: "nil_valued", tenant: "acme", value: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := multitenant.SetTenant(context.Background(), tt.tenant)

			stamp, err := ResolveTenantStamp(ctx, map[string]any{TenantStampHeader: tt.value})

			require.ErrorIs(t, err, ErrTenantStampConflict)
			assert.Empty(t, stamp)
		})
	}
}
