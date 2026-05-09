package static

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListTenantsNilReceiver(t *testing.T) {
	var s *TenantSource
	_, err := s.ListTenants(context.Background())
	require.ErrorIs(t, err, ErrNilStore)
}

func TestListTenantsNilStore(t *testing.T) {
	s := &TenantSource{}
	_, err := s.ListTenants(context.Background())
	require.ErrorIs(t, err, ErrNilStore)
}

func TestFromConfigStoreReturnsTenantSource(t *testing.T) {
	s := FromConfigStore(nil)
	assert.NotNil(t, s)
	_, err := s.ListTenants(context.Background())
	require.ErrorIs(t, err, ErrNilStore)
}
