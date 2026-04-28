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

func TestRegistrySeparatesByDirection(t *testing.T) {
	// Same struct type scanned for both directions must produce independent cache entries.
	// Without direction in the cache key, the second LoadOrScan would return the first
	// scan's policy (with wrong required keys) — a real correctness bug if a shared
	// envelope type is used as both a request and a response.
	type sharedEnvelope struct {
		_ struct{} `jose:"decrypt=our-key,verify=peer-key"`
	}
	r := NewPolicyRegistry()
	in, err := r.LoadOrScan(reflect.TypeOf(sharedEnvelope{}), DirectionInbound)
	require.NoError(t, err)
	require.NotNil(t, in)
	assert.Equal(t, DirectionInbound, in.Direction)

	// The outbound scan SHOULD fail (the tag has decrypt/verify keys, which are inbound-only)
	// — proving the second scan actually re-runs rather than returning the cached inbound policy.
	_, outErr := r.LoadOrScan(reflect.TypeOf(sharedEnvelope{}), DirectionOutbound)
	require.Error(t, outErr, "outbound scan of inbound-tagged type must fail; cached inbound policy must not leak")
}

func TestRegistryStoreOverridesCache(t *testing.T) {
	r := NewPolicyRegistry()
	custom := &Policy{Direction: DirectionInbound, DecryptKid: "explicit", VerifyKid: "explicit"}
	r.Store(reflect.TypeOf(taggedRequest{}), custom)

	got, err := r.LoadOrScan(reflect.TypeOf(taggedRequest{}), DirectionInbound)
	require.NoError(t, err)
	assert.Same(t, custom, got)
}
