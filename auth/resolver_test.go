package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"math/big"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testKeyOnce = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
})

func testRSAPublicKey(t *testing.T) *rsa.PublicKey {
	t.Helper()
	return &testKeyOnce().PublicKey
}

func TestStaticKeyResolverReturnsARegisteredKey(t *testing.T) {
	want := testRSAPublicKey(t)
	src := NewStaticKeyResolver(map[string]*rsa.PublicKey{"k1": want})

	got, err := src.PublicKey(context.Background(), "k1")

	require.NoError(t, err)
	// Value equality, not identity: the resolver clones the caller's key material.
	assert.Equal(t, want, got)
	assert.NotSame(t, want, got)
}

// TestStaticKeyResolverClonesTheCallersKeys pins the ownership half of the
// construction contract: writing to the key a caller passed in, modulus
// included, must not reach what the resolver verifies against.
func TestStaticKeyResolverClonesTheCallersKeys(t *testing.T) {
	original := &rsa.PublicKey{N: big.NewInt(0xC0FFEE), E: 65537}
	src := NewStaticKeyResolver(map[string]*rsa.PublicKey{"k1": original})

	original.N.SetInt64(1)
	original.E = 3

	got, err := src.PublicKey(context.Background(), "k1")

	require.NoError(t, err)
	assert.Zero(t, got.N.Cmp(big.NewInt(0xC0FFEE)))
	assert.Equal(t, 65537, got.E)
}

// TestStaticKeyResolverDropsAKeyWithNoModulus pins that a modulus-less key is
// never registered: it cannot verify a signature, so the lookup must answer as
// for any unusable entry rather than hand the verifier a key it will reject.
func TestStaticKeyResolverDropsAKeyWithNoModulus(t *testing.T) {
	t.Run("alongside_a_usable_key", func(t *testing.T) {
		src := NewStaticKeyResolver(map[string]*rsa.PublicKey{
			"k1": {E: 65537},
			"k2": testRSAPublicKey(t),
		})

		got, err := src.PublicKey(context.Background(), "k1")

		require.ErrorIs(t, err, ErrKidUnknown)
		assert.Nil(t, got)
	})

	// Dropping the only key empties the set, so the resolver falls to its
	// documented empty-key-set behavior rather than reporting an unknown kid.
	t.Run("as_the_only_key", func(t *testing.T) {
		src := NewStaticKeyResolver(map[string]*rsa.PublicKey{"k1": {E: 65537}})

		got, err := src.PublicKey(context.Background(), "k1")

		require.ErrorIs(t, err, ErrKeySetUnavailable)
		assert.Nil(t, got)
	})
}

func TestStaticKeyResolverRejectsAnUnknownKid(t *testing.T) {
	src := NewStaticKeyResolver(map[string]*rsa.PublicKey{"k1": testRSAPublicKey(t)})

	got, err := src.PublicKey(context.Background(), "k2")

	require.ErrorIs(t, err, ErrKidUnknown)
	assert.Nil(t, got)
	assert.NotErrorIs(t, err, ErrKeySetUnavailable)
}

func TestStaticKeyResolverWithNoKeysIsUnavailable(t *testing.T) {
	for name, src := range map[string]*StaticKeyResolver{
		"nil_map":   NewStaticKeyResolver(nil),
		"empty_map": NewStaticKeyResolver(map[string]*rsa.PublicKey{}),
		"nil_entry": NewStaticKeyResolver(map[string]*rsa.PublicKey{"k1": nil}),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := src.PublicKey(context.Background(), "k1")

			require.ErrorIs(t, err, ErrKeySetUnavailable)
			require.NotErrorIs(t, err, ErrKidUnknown)
			assert.Nil(t, got)
		})
	}
}

func TestStaticKeyResolverDropsNilEntriesButKeepsTheRest(t *testing.T) {
	src := NewStaticKeyResolver(map[string]*rsa.PublicKey{"k1": testRSAPublicKey(t), "k2": nil})

	got, err := src.PublicKey(context.Background(), "k1")
	require.NoError(t, err)
	assert.NotNil(t, got)

	_, err = src.PublicKey(context.Background(), "k2")
	require.ErrorIs(t, err, ErrKidUnknown)
}

func TestStaticKeyResolverCopiesTheKeyMap(t *testing.T) {
	keys := map[string]*rsa.PublicKey{"k1": testRSAPublicKey(t)}
	src := NewStaticKeyResolver(keys)

	delete(keys, "k1")
	keys["k2"] = testRSAPublicKey(t)

	got, err := src.PublicKey(context.Background(), "k1")
	require.NoError(t, err)
	assert.NotNil(t, got)

	_, err = src.PublicKey(context.Background(), "k2")
	assert.ErrorIs(t, err, ErrKidUnknown)
}

func TestStaticKeyResolverIsSafeForConcurrentReads(t *testing.T) {
	src := NewStaticKeyResolver(map[string]*rsa.PublicKey{"k1": testRSAPublicKey(t)})

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := src.PublicKey(context.Background(), "k1"); err != nil {
				t.Errorf("concurrent PublicKey failed: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestStaticKeyResolverPublicKeyOnANilReceiverFailsClosed pins the exported
// entry point: reached without going through NewVerifierWithResolver, a nil
// resolver reports an unusable key set instead of dereferencing the receiver.
func TestStaticKeyResolverPublicKeyOnANilReceiverFailsClosed(t *testing.T) {
	var src *StaticKeyResolver

	var (
		key *rsa.PublicKey
		err error
	)
	require.NotPanics(t, func() {
		key, err = src.PublicKey(context.Background(), "k1")
	})

	assert.Nil(t, key)
	require.ErrorIs(t, err, ErrKeySetUnavailable)
	require.NotErrorIs(t, err, ErrKidUnknown)
}
