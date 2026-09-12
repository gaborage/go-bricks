package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
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

func TestStaticKeySourceReturnsARegisteredKey(t *testing.T) {
	want := testRSAPublicKey(t)
	src := NewStaticKeySource(map[string]*rsa.PublicKey{"k1": want})

	got, err := src.PublicKey(context.Background(), "k1")

	require.NoError(t, err)
	assert.Same(t, want, got)
}

func TestStaticKeySourceRejectsAnUnknownKid(t *testing.T) {
	src := NewStaticKeySource(map[string]*rsa.PublicKey{"k1": testRSAPublicKey(t)})

	got, err := src.PublicKey(context.Background(), "k2")

	require.ErrorIs(t, err, ErrKidUnknown)
	assert.Nil(t, got)
	assert.NotErrorIs(t, err, ErrKeySetUnavailable)
}

func TestStaticKeySourceWithNoKeysIsUnavailable(t *testing.T) {
	for name, src := range map[string]*StaticKeySource{
		"nil_map":   NewStaticKeySource(nil),
		"empty_map": NewStaticKeySource(map[string]*rsa.PublicKey{}),
		"nil_entry": NewStaticKeySource(map[string]*rsa.PublicKey{"k1": nil}),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := src.PublicKey(context.Background(), "k1")

			require.ErrorIs(t, err, ErrKeySetUnavailable)
			require.NotErrorIs(t, err, ErrKidUnknown)
			assert.Nil(t, got)
		})
	}
}

func TestStaticKeySourceDropsNilEntriesButKeepsTheRest(t *testing.T) {
	src := NewStaticKeySource(map[string]*rsa.PublicKey{"k1": testRSAPublicKey(t), "k2": nil})

	got, err := src.PublicKey(context.Background(), "k1")
	require.NoError(t, err)
	assert.NotNil(t, got)

	_, err = src.PublicKey(context.Background(), "k2")
	require.ErrorIs(t, err, ErrKidUnknown)
}

func TestStaticKeySourceCopiesTheKeyMap(t *testing.T) {
	keys := map[string]*rsa.PublicKey{"k1": testRSAPublicKey(t)}
	src := NewStaticKeySource(keys)

	delete(keys, "k1")
	keys["k2"] = testRSAPublicKey(t)

	got, err := src.PublicKey(context.Background(), "k1")
	require.NoError(t, err)
	assert.NotNil(t, got)

	_, err = src.PublicKey(context.Background(), "k2")
	assert.ErrorIs(t, err, ErrKidUnknown)
}

func TestStaticKeySourceIsSafeForConcurrentReads(t *testing.T) {
	src := NewStaticKeySource(map[string]*rsa.PublicKey{"k1": testRSAPublicKey(t)})

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

func TestStaticKeySourceSatisfiesKeySource(t *testing.T) {
	var src KeySource = NewStaticKeySource(map[string]*rsa.PublicKey{"k1": testRSAPublicKey(t)})

	_, err := src.PublicKey(context.Background(), "k1")

	assert.NoError(t, err)
}
