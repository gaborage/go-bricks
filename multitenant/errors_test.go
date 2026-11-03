package multitenant

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrUnknownTenantWrapsResolutionFailed(t *testing.T) {
	require.ErrorIs(t, ErrUnknownTenant, ErrTenantResolutionFailed)
	require.EqualError(t, ErrUnknownTenant, "multitenant: unknown tenant")
}
