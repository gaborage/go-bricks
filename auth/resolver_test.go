package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"math"
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
	want := new(big.Int).Set(testRSAPublicKey(t).N)
	original := &rsa.PublicKey{N: new(big.Int).Set(want), E: 65537}
	src := NewStaticKeyResolver(map[string]*rsa.PublicKey{"k1": original})

	original.N.SetInt64(1)
	original.E = 3

	got, err := src.PublicKey(context.Background(), "k1")

	require.NoError(t, err)
	assert.Zero(t, got.N.Cmp(want))
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

// oddModulus returns a positive odd integer whose bit length is exactly bits, so
// a fixture exercises one structural rule at a time. Hand-built moduli are even
// by accident far too easily, hence the explicit low bit.
func oddModulus(bits int) *big.Int {
	n := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
	return n.SetBit(n, 0, 1)
}

// TestStaticKeyResolverDropsAStructurallyInvalidKey pins the fail-closed half of
// construction: a key that cannot be a working RSA public key never reaches the
// verifier, where its failure would be reported as a bad signature on the
// caller's credential rather than as an unusable key set.
func TestStaticKeyResolverDropsAStructurallyInvalidKey(t *testing.T) {
	evenModulus := oddModulus(minModulusBits)
	evenModulus.SetBit(evenModulus, 0, 0)

	cases := map[string]*rsa.PublicKey{
		"nil_modulus":           {E: 65537},
		"zero_modulus":          {N: big.NewInt(0), E: 65537},
		"negative_modulus":      {N: new(big.Int).Neg(oddModulus(minModulusBits)), E: 65537},
		"even_modulus":          {N: evenModulus, E: 65537},
		"modulus_below_floor":   {N: oddModulus(minModulusBits - 1), E: 65537},
		"modulus_above_ceiling": {N: oddModulus(maxModulusBits + 1), E: 65537},
		"zero_exponent":         {N: oddModulus(minModulusBits), E: 0},
		"exponent_one":          {N: oddModulus(minModulusBits), E: 1},
		"exponent_two":          {N: oddModulus(minModulusBits), E: 2},
		"negative_exponent":     {N: oddModulus(minModulusBits), E: -3},
		"even_exponent":         {N: oddModulus(minModulusBits), E: 65536},
	}

	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			src := NewStaticKeyResolver(map[string]*rsa.PublicKey{"bad": bad, "good": testRSAPublicKey(t)})

			got, err := src.PublicKey(context.Background(), "bad")
			require.ErrorIs(t, err, ErrKidUnknown)
			require.NotErrorIs(t, err, ErrKeySetUnavailable)
			assert.Nil(t, got)

			// The second entry proves only the offending one was dropped.
			usable, err := src.PublicKey(context.Background(), "good")
			require.NoError(t, err)
			assert.NotNil(t, usable)
		})
	}
}

// TestStaticKeyResolverDropsAnExponentAboveTheRepresentableCeiling covers the
// upper exponent bound, which only exists on platforms whose int is wider than
// the ceiling itself.
func TestStaticKeyResolverDropsAnExponentAboveTheRepresentableCeiling(t *testing.T) {
	if math.MaxInt == math.MaxInt32 {
		t.Skip("int cannot hold an exponent above the ceiling on this platform")
	}
	// MaxInt32 is odd, so +2 stays odd and fails on size alone.
	oversized := int(int64(math.MaxInt32) + 2)

	src := NewStaticKeyResolver(map[string]*rsa.PublicKey{
		"bad":  {N: oddModulus(minModulusBits), E: oversized},
		"good": testRSAPublicKey(t),
	})

	got, err := src.PublicKey(context.Background(), "bad")

	require.ErrorIs(t, err, ErrKidUnknown)
	assert.Nil(t, got)
}

// TestStaticKeyResolverAcceptsKeysAtTheStructuralBounds sits exactly on every
// inclusive bound, so tightening any of them by one is visible.
func TestStaticKeyResolverAcceptsKeysAtTheStructuralBounds(t *testing.T) {
	cases := map[string]*rsa.PublicKey{
		"smallest_modulus":  {N: oddModulus(minModulusBits), E: 65537},
		"largest_modulus":   {N: oddModulus(maxModulusBits), E: 65537},
		"smallest_exponent": {N: oddModulus(minModulusBits), E: minPublicExponent},
		"largest_exponent":  {N: oddModulus(minModulusBits), E: maxPublicExponent},
	}

	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			src := NewStaticKeyResolver(map[string]*rsa.PublicKey{"k1": key})

			got, err := src.PublicKey(context.Background(), "k1")

			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Zero(t, got.N.Cmp(key.N))
			assert.Equal(t, key.E, got.E)
		})
	}
}

// TestStaticKeyResolverWithOnlyInvalidKeysIsUnavailable pins that dropping every
// entry leaves the documented empty-key-set behavior, not an unknown kid.
func TestStaticKeyResolverWithOnlyInvalidKeysIsUnavailable(t *testing.T) {
	src := NewStaticKeyResolver(map[string]*rsa.PublicKey{
		"k1": {N: oddModulus(minModulusBits - 1), E: 65537},
		"k2": {N: oddModulus(minModulusBits), E: 65536},
	})

	got, err := src.PublicKey(context.Background(), "k1")

	require.ErrorIs(t, err, ErrKeySetUnavailable)
	require.NotErrorIs(t, err, ErrKidUnknown)
	assert.Nil(t, got)
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
