package tracking

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithRepositoryMethodRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := WithRepositoryMethod(context.Background(), "GetCustomer")
	method, ok := RepositoryMethodFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, "GetCustomer", method)
}

func TestWithRepositoryMethodEmptyIsNoOp(t *testing.T) {
	t.Parallel()
	parent := context.Background()
	ctx := WithRepositoryMethod(parent, "")

	// An empty method must not allocate a new context value.
	assert.Equal(t, parent, ctx)
	_, ok := RepositoryMethodFromContext(ctx)
	assert.False(t, ok)
}

func TestWithRepositoryMethodOverwrite(t *testing.T) {
	t.Parallel()
	ctx := WithRepositoryMethod(context.Background(), "First")
	ctx = WithRepositoryMethod(ctx, "Second")
	method, ok := RepositoryMethodFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, "Second", method)
}

func TestRepositoryMethodFromContextAbsent(t *testing.T) {
	t.Parallel()
	method, ok := RepositoryMethodFromContext(context.Background())
	assert.False(t, ok)
	assert.Empty(t, method)
}

func TestRepositoryMethodFromContextNil(t *testing.T) {
	t.Parallel()
	var ctx context.Context // nil context — must be handled defensively
	method, ok := RepositoryMethodFromContext(ctx)
	assert.False(t, ok)
	assert.Empty(t, method)
}
