package jose

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryLoadOrScanCachesUntagged(t *testing.T) {
	r := NewPolicyRegistry()
	t1, err := r.LoadOrScan(reflect.TypeOf(untaggedRequest{}), DirectionInbound)
	require.NoError(t, err)
	assert.Nil(t, t1)

	// Second call returns the cached nil result (not a re-scan).
	t2, err := r.LoadOrScan(reflect.TypeOf(untaggedRequest{}), DirectionInbound)
	require.NoError(t, err)
	assert.Nil(t, t2)
}

func TestRegistryLoadOrScanCachesTagged(t *testing.T) {
	r := NewPolicyRegistry()
	p1, err := r.LoadOrScan(reflect.TypeOf(taggedRequest{}), DirectionInbound)
	require.NoError(t, err)
	require.NotNil(t, p1)

	p2, err := r.LoadOrScan(reflect.TypeOf(taggedRequest{}), DirectionInbound)
	require.NoError(t, err)
	// Pointer equality: cached entries should not be re-allocated.
	assert.Same(t, p1, p2)
}

func TestRegistryStoreOverridesCache(t *testing.T) {
	r := NewPolicyRegistry()
	custom := &Policy{Direction: DirectionInbound, DecryptKid: "explicit", VerifyKid: "explicit"}
	r.Store(reflect.TypeOf(taggedRequest{}), custom)

	got, err := r.LoadOrScan(reflect.TypeOf(taggedRequest{}), DirectionInbound)
	require.NoError(t, err)
	assert.Same(t, custom, got)
}
