package multitenant

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrUnknownTenantWrapsResolutionFailed(t *testing.T) {
	require.ErrorIs(t, ErrUnknownTenant, ErrTenantResolutionFailed)
	require.EqualError(t, ErrUnknownTenant, "multitenant: unknown tenant")

	wrapped := fmt.Errorf("%w: %w", ErrUnknownTenant, errors.New("x"))
	require.ErrorIs(t, wrapped, ErrUnknownTenant)
	require.ErrorIs(t, wrapped, ErrTenantResolutionFailed)
}
