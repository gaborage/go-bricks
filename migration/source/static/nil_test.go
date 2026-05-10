package static

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
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

func TestFromConfigStoreReturnsNonNilForNilStore(t *testing.T) {
	// Constructor never returns nil, even when given a nil store. The error
	// path on the returned source is exercised by TestListTenantsNilStore.
	assert.NotNil(t, FromConfigStore(nil))
}

func TestListTenantsRejectsEmptyID(t *testing.T) {
	s := FromConfigStore(&fakeStore{
		t: map[string]config.TenantEntry{
			"":        {},
			"valid-1": {},
		},
	})
	_, err := s.ListTenants(context.Background())
	require.ErrorIs(t, err, ErrEmptyTenantID)
}

func TestListTenantsSortedHappyPath(t *testing.T) {
	s := FromConfigStore(&fakeStore{
		t: map[string]config.TenantEntry{
			"c": {},
			"a": {},
			"b": {},
		},
	})
	ids, err := s.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, ids)
}
