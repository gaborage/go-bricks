package jose

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
)

// Compile-time proof that the real cache surface satisfies ReplayRecorder. The
// import is test-only, so jose's production dependency set is unchanged.
var _ ReplayRecorder = cache.Cache(nil)

// recordingStore is a hand-rolled fake, not cache/testing.MockCache: MockCache
// stores an absolute expiration time and never exposes the ttl argument it was
// given, so it cannot pin the stored TTL, which is exactly what these tests assert.
type recordingStore struct {
	key      string
	value    []byte
	ttl      time.Duration
	calls    int
	existing bool // when true, report the key as already present
	err      error
}

func (s *recordingStore) GetOrSet(_ context.Context, key string, value []byte, ttl time.Duration) (storedValue []byte, wasSet bool, err error) {
	s.calls++
	s.key, s.value, s.ttl = key, value, ttl
	if s.err != nil {
		return nil, false, s.err
	}
	if s.existing {
		return []byte{1}, false, nil
	}
	return value, true, nil
}

func TestCheckJTIReplayFirstUseIsFresh(t *testing.T) {
	store := &recordingStore{}
	claims := &Claims{JTI: "abc", Issuer: "visa"}

	fresh, err := CheckJTIReplay(context.Background(), store, claims, 5*time.Minute)

	require.NoError(t, err)
	assert.True(t, fresh)
	assert.Equal(t, 1, store.calls)
	assert.Equal(t, "jose:jti:4:visa:abc", store.key)
	// Pins the "GetOrSet requires a non-empty value" contract documented on
	// replayMarker: the stored value is the marker, not something derived
	// from the claims.
	assert.Equal(t, []byte{1}, store.value)
}

func TestCheckJTIReplayDuplicateIsNotFresh(t *testing.T) {
	store := &recordingStore{existing: true}
	claims := &Claims{JTI: "abc", Issuer: "visa"}

	fresh, err := CheckJTIReplay(context.Background(), store, claims, 5*time.Minute)

	// A replay is a normal outcome, not an error.
	require.NoError(t, err)
	assert.False(t, fresh)
}

// TestCheckJTIReplayTTLAlwaysWindow pins the stored TTL to the caller's
// declared window regardless of the claim's exp. Deriving the TTL from exp
// would forget the jti at exp while a caller that allows clock skew still
// accepts the token until exp+skew — that gap is a replay hole, so exp must
// never shorten (or otherwise influence) the stored TTL.
func TestCheckJTIReplayTTLAlwaysWindow(t *testing.T) {
	const window = 5 * time.Minute

	tests := []struct {
		name      string
		expiresAt time.Time
	}{
		{
			name:      "exp_absent",
			expiresAt: time.Time{},
		},
		{
			name:      "exp_in_past",
			expiresAt: time.Now().Add(-time.Hour),
		},
		{
			name:      "exp_far_future",
			expiresAt: time.Now().Add(time.Hour),
		},
		{
			name:      "exp_within_window",
			expiresAt: time.Now().Add(30 * time.Second),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &recordingStore{}
			claims := &Claims{JTI: "abc", Issuer: "visa", ExpiresAt: tc.expiresAt}

			_, err := CheckJTIReplay(context.Background(), store, claims, window)
			require.NoError(t, err)

			// The stored TTL is exactly the caller's declared window in every row —
			// exp never shortens it, whatever its value. Equality also carries the
			// immortal-key guard: window is positive, and a zero TTL would mean
			// "no expiry" at the cache backend.
			assert.Equal(t, window, store.ttl)
		})
	}
}

func TestCheckJTIReplayRejectsInvalidInput(t *testing.T) {
	t.Run("nil_claims", func(t *testing.T) {
		store := &recordingStore{}

		_, err := CheckJTIReplay(context.Background(), store, nil, 5*time.Minute)

		require.ErrorIs(t, err, ErrClaimsMissing)
		assert.Equal(t, 0, store.calls)
	})

	t.Run("nil_recorder", func(t *testing.T) {
		claims := &Claims{JTI: "abc", Issuer: "visa"}

		_, err := CheckJTIReplay(context.Background(), nil, claims, 5*time.Minute)

		require.ErrorIs(t, err, ErrReplayRecorderMissing)
	})

	t.Run("empty_jti", func(t *testing.T) {
		store := &recordingStore{}
		claims := &Claims{JTI: "", Issuer: "visa"}

		_, err := CheckJTIReplay(context.Background(), store, claims, 5*time.Minute)

		require.ErrorIs(t, err, ErrJTIMissing)
		assert.Equal(t, 0, store.calls)
	})

	t.Run("empty_issuer", func(t *testing.T) {
		store := &recordingStore{}
		claims := &Claims{JTI: "abc", Issuer: ""}

		_, err := CheckJTIReplay(context.Background(), store, claims, 5*time.Minute)

		require.ErrorIs(t, err, ErrIssuerMissing)
		assert.Equal(t, 0, store.calls)
	})

	t.Run("zero_window", func(t *testing.T) {
		store := &recordingStore{}
		claims := &Claims{JTI: "abc", Issuer: "visa"}

		_, err := CheckJTIReplay(context.Background(), store, claims, 0)

		require.ErrorIs(t, err, ErrReplayWindowInvalid)
		assert.Equal(t, 0, store.calls)
	})

	t.Run("negative_window", func(t *testing.T) {
		store := &recordingStore{}
		claims := &Claims{JTI: "abc", Issuer: "visa"}

		_, err := CheckJTIReplay(context.Background(), store, claims, -time.Minute)

		require.ErrorIs(t, err, ErrReplayWindowInvalid)
		assert.Equal(t, 0, store.calls)
	})
}

func TestCheckJTIReplayWrapsStoreError(t *testing.T) {
	boom := errors.New("boom")
	store := &recordingStore{err: boom}
	claims := &Claims{JTI: "abc", Issuer: "visa"}

	fresh, err := CheckJTIReplay(context.Background(), store, claims, 5*time.Minute)

	assert.False(t, fresh)
	require.ErrorIs(t, err, boom)
	// Pins the %w wrapping itself — ErrorIs alone would pass even if the
	// helper returned the store error unchanged.
	assert.Contains(t, err.Error(), "jose: replay recorder:")
}

func TestCheckJTIReplayNamespacesByIssuer(t *testing.T) {
	storeA := &recordingStore{}
	storeB := &recordingStore{}

	_, err := CheckJTIReplay(context.Background(), storeA, &Claims{JTI: "x", Issuer: "a"}, 5*time.Minute)
	require.NoError(t, err)
	_, err = CheckJTIReplay(context.Background(), storeB, &Claims{JTI: "x", Issuer: "bb"}, 5*time.Minute)
	require.NoError(t, err)

	assert.NotEqual(t, storeA.key, storeB.key)

	// The length prefix proves the issuer boundary is unambiguous: without it,
	// issuer "a:b" + jti "c" and issuer "a" + jti "b:c" would collide on the
	// same concatenated string.
	storeC := &recordingStore{}
	storeD := &recordingStore{}

	_, err = CheckJTIReplay(context.Background(), storeC, &Claims{JTI: "c", Issuer: "a:b"}, 5*time.Minute)
	require.NoError(t, err)
	_, err = CheckJTIReplay(context.Background(), storeD, &Claims{JTI: "b:c", Issuer: "a"}, 5*time.Minute)
	require.NoError(t, err)

	assert.NotEqual(t, storeC.key, storeD.key)
}

func TestCheckJTIReplayInNamespaceFirstUseIsFresh(t *testing.T) {
	store := &recordingStore{}
	claims := &Claims{JTI: "abc"}

	fresh, err := CheckJTIReplayInNamespace(context.Background(), store, "visa-vts-verify", claims, 5*time.Minute)

	require.NoError(t, err)
	assert.True(t, fresh)
	assert.Equal(t, 1, store.calls)
	assert.Equal(t, "jose:jti:15:visa-vts-verify:abc", store.key)
}

func TestCheckJTIReplayInNamespaceDuplicateIsNotFresh(t *testing.T) {
	store := &recordingStore{}
	claims := &Claims{JTI: "abc"}

	first, err := CheckJTIReplayInNamespace(context.Background(), store, "visa-vts-verify", claims, 5*time.Minute)
	require.NoError(t, err)
	assert.True(t, first)

	// Simulate the key now being present, as a real cache backend would report
	// on a second GetOrSet for the same namespace+jti.
	store.existing = true
	second, err := CheckJTIReplayInNamespace(context.Background(), store, "visa-vts-verify", claims, 5*time.Minute)
	require.NoError(t, err)
	assert.False(t, second)
}

func TestCheckJTIReplayInNamespaceSeparatesNamespaces(t *testing.T) {
	storeA := &recordingStore{}
	storeB := &recordingStore{}
	claims := &Claims{JTI: "abc"}

	freshA, err := CheckJTIReplayInNamespace(context.Background(), storeA, "a", claims, 5*time.Minute)
	require.NoError(t, err)
	freshB, err := CheckJTIReplayInNamespace(context.Background(), storeB, "b", claims, 5*time.Minute)
	require.NoError(t, err)

	assert.True(t, freshA)
	assert.True(t, freshB)
	assert.NotEqual(t, storeA.key, storeB.key)
}

// TestCheckJTIReplayInNamespaceKeyAliasing extends the aliasing pin from
// TestCheckJTIReplayNamespacesByIssuer to the namespace variant: without the
// length prefix, namespace "a:" + jti "b" and namespace "a" + jti ":b" would
// both concatenate to "a:b" and collide.
func TestCheckJTIReplayInNamespaceKeyAliasing(t *testing.T) {
	storeA := &recordingStore{}
	storeB := &recordingStore{}

	_, err := CheckJTIReplayInNamespace(context.Background(), storeA, "a:", &Claims{JTI: "b"}, 5*time.Minute)
	require.NoError(t, err)
	_, err = CheckJTIReplayInNamespace(context.Background(), storeB, "a", &Claims{JTI: ":b"}, 5*time.Minute)
	require.NoError(t, err)

	assert.NotEqual(t, storeA.key, storeB.key)
}

func TestCheckJTIReplayInNamespaceRejectsInvalidInput(t *testing.T) {
	t.Run("nil_claims", func(t *testing.T) {
		store := &recordingStore{}

		_, err := CheckJTIReplayInNamespace(context.Background(), store, "visa-vts-verify", nil, 5*time.Minute)

		require.ErrorIs(t, err, ErrClaimsMissing)
		assert.Equal(t, 0, store.calls)
	})

	t.Run("nil_recorder", func(t *testing.T) {
		claims := &Claims{JTI: "abc"}

		_, err := CheckJTIReplayInNamespace(context.Background(), nil, "visa-vts-verify", claims, 5*time.Minute)

		require.ErrorIs(t, err, ErrReplayRecorderMissing)
	})

	t.Run("empty_jti", func(t *testing.T) {
		store := &recordingStore{}
		claims := &Claims{JTI: ""}

		_, err := CheckJTIReplayInNamespace(context.Background(), store, "visa-vts-verify", claims, 5*time.Minute)

		require.ErrorIs(t, err, ErrJTIMissing)
		assert.Equal(t, 0, store.calls)
	})

	t.Run("empty_namespace", func(t *testing.T) {
		store := &recordingStore{}
		claims := &Claims{JTI: "abc"}

		_, err := CheckJTIReplayInNamespace(context.Background(), store, "", claims, 5*time.Minute)

		require.ErrorIs(t, err, ErrIssuerMissing)
		assert.Equal(t, 0, store.calls)
	})

	t.Run("zero_window", func(t *testing.T) {
		store := &recordingStore{}
		claims := &Claims{JTI: "abc"}

		_, err := CheckJTIReplayInNamespace(context.Background(), store, "visa-vts-verify", claims, 0)

		require.ErrorIs(t, err, ErrReplayWindowInvalid)
		assert.Equal(t, 0, store.calls)
	})

	t.Run("negative_window", func(t *testing.T) {
		store := &recordingStore{}
		claims := &Claims{JTI: "abc"}

		_, err := CheckJTIReplayInNamespace(context.Background(), store, "visa-vts-verify", claims, -time.Minute)

		require.ErrorIs(t, err, ErrReplayWindowInvalid)
		assert.Equal(t, 0, store.calls)
	})
}

func TestCheckJTIReplayInNamespaceWrapsStoreError(t *testing.T) {
	boom := errors.New("boom")
	store := &recordingStore{err: boom}
	claims := &Claims{JTI: "abc"}

	fresh, err := CheckJTIReplayInNamespace(context.Background(), store, "visa-vts-verify", claims, 5*time.Minute)

	assert.False(t, fresh)
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "jose: replay recorder:")
}
