package multitenant

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetTenant(t *testing.T) {
	ctx := context.Background()

	// Set tenant ID in context
	newCtx := SetTenant(ctx, "test-tenant")

	// Verify context is different
	assert.NotEqual(t, ctx, newCtx)

	// Verify we can retrieve the tenant ID
	tenantID, ok := GetTenant(newCtx)
	assert.True(t, ok)
	assert.Equal(t, "test-tenant", tenantID)
}

func TestGetTenant(t *testing.T) {
	ctx := context.Background()

	t.Run("no_tenant_in_context", func(t *testing.T) {
		tenantID, ok := GetTenant(ctx)
		assert.False(t, ok)
		assert.Empty(t, tenantID)
	})

	t.Run("tenant_in_context", func(t *testing.T) {
		ctxWithTenant := SetTenant(ctx, "my-tenant")
		tenantID, ok := GetTenant(ctxWithTenant)
		assert.True(t, ok)
		assert.Equal(t, "my-tenant", tenantID)
	})

	t.Run("empty_tenant_id", func(t *testing.T) {
		ctxWithTenant := SetTenant(ctx, "")
		tenantID, ok := GetTenant(ctxWithTenant)
		assert.False(t, ok)
		assert.Empty(t, tenantID)
	})

	t.Run("overwrite_tenant", func(t *testing.T) {
		ctxWithTenant1 := SetTenant(ctx, "tenant1")
		ctxWithTenant2 := SetTenant(ctxWithTenant1, "tenant2")

		// Should return the most recent tenant ID
		tenantID, ok := GetTenant(ctxWithTenant2)
		assert.True(t, ok)
		assert.Equal(t, "tenant2", tenantID)

		// Original context should still have tenant1
		tenantID1, ok1 := GetTenant(ctxWithTenant1)
		assert.True(t, ok1)
		assert.Equal(t, "tenant1", tenantID1)
	})
}
