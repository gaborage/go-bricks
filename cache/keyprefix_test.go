package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	cachetest "github.com/gaborage/go-bricks/cache/testing"
)

const (
	kpPrefix  = "orders"
	kpKey     = "user:1"
	kpWireKey = "orders:user:1"
	kpTTL     = time.Minute
)

// kpWrap builds a MockCache and the prefixed view over it, failing the test if the
// wrapper could not be built.
func kpWrap(t *testing.T) (mock *cachetest.MockCache, prefixed cache.Cache) {
	t.Helper()
	mock = cachetest.NewMockCache()
	prefixed, err := cache.WithKeyPrefix(mock, kpPrefix)
	require.NoError(t, err)
	return mock, prefixed
}

func TestWithKeyPrefixPrefixesEveryKey(t *testing.T) {
	ctx := context.Background()

	t.Run("get", func(t *testing.T) {
		mock, prefixed := kpWrap(t)
		require.NoError(t, mock.Set(ctx, kpWireKey, []byte("seeded"), kpTTL))

		got, err := prefixed.Get(ctx, kpKey)

		require.NoError(t, err)
		assert.Equal(t, []byte("seeded"), got)
	})

	t.Run("set", func(t *testing.T) {
		mock, prefixed := kpWrap(t)

		require.NoError(t, prefixed.Set(ctx, kpKey, []byte("fresh"), kpTTL))

		assert.Equal(t, []string{kpWireKey}, mock.AllKeys())
		cachetest.AssertKeyExists(t, mock, kpWireKey)
	})

	t.Run("delete", func(t *testing.T) {
		mock, prefixed := kpWrap(t)
		require.NoError(t, mock.Set(ctx, kpWireKey, []byte("seeded"), kpTTL))

		require.NoError(t, prefixed.Delete(ctx, kpKey))

		cachetest.AssertKeyNotExists(t, mock, kpWireKey)
	})

	t.Run("get_or_set_on_absent_key", func(t *testing.T) {
		mock, prefixed := kpWrap(t)

		stored, wasSet, err := prefixed.GetOrSet(ctx, kpKey, []byte("fresh"), kpTTL)

		require.NoError(t, err)
		assert.True(t, wasSet)
		assert.Equal(t, []byte("fresh"), stored)
		assert.Equal(t, []string{kpWireKey}, mock.AllKeys())
	})

	t.Run("get_or_set_on_seeded_key", func(t *testing.T) {
		mock, prefixed := kpWrap(t)
		require.NoError(t, mock.Set(ctx, kpWireKey, []byte("seeded"), kpTTL))

		stored, wasSet, err := prefixed.GetOrSet(ctx, kpKey, []byte("fresh"), kpTTL)

		require.NoError(t, err)
		assert.False(t, wasSet)
		assert.Equal(t, []byte("seeded"), stored)
	})

	t.Run("compare_and_set", func(t *testing.T) {
		mock, prefixed := kpWrap(t)
		require.NoError(t, mock.Set(ctx, kpWireKey, []byte("old"), kpTTL))

		swapped, err := prefixed.CompareAndSet(ctx, kpKey, []byte("old"), []byte("new"), kpTTL)

		require.NoError(t, err)
		assert.True(t, swapped)
		cachetest.AssertValue(t, mock, kpWireKey, []byte("new"))
	})

	t.Run("compare_and_set_mismatch", func(t *testing.T) {
		mock, prefixed := kpWrap(t)
		require.NoError(t, mock.Set(ctx, kpWireKey, []byte("old"), kpTTL))

		swapped, err := prefixed.CompareAndSet(ctx, kpKey, []byte("stale"), []byte("new"), kpTTL)

		require.NoError(t, err)
		assert.False(t, swapped)
		cachetest.AssertValue(t, mock, kpWireKey, []byte("old"))
	})

	t.Run("compare_and_delete", func(t *testing.T) {
		mock, prefixed := kpWrap(t)
		require.NoError(t, mock.Set(ctx, kpWireKey, []byte("token"), kpTTL))

		deleted, err := prefixed.CompareAndDelete(ctx, kpKey, []byte("token"))

		require.NoError(t, err)
		assert.True(t, deleted)
		cachetest.AssertKeyNotExists(t, mock, kpWireKey)
	})

	t.Run("compare_and_delete_mismatch", func(t *testing.T) {
		mock, prefixed := kpWrap(t)
		require.NoError(t, mock.Set(ctx, kpWireKey, []byte("token"), kpTTL))

		deleted, err := prefixed.CompareAndDelete(ctx, kpKey, []byte("other"))

		require.NoError(t, err)
		assert.False(t, deleted)
		cachetest.AssertKeyExists(t, mock, kpWireKey)
	})
}

func TestWithKeyPrefixEmptyPrefixReturnsTheSameCache(t *testing.T) {
	mock := cachetest.NewMockCache()

	got, err := cache.WithKeyPrefix(mock, "")

	require.NoError(t, err)
	assert.Same(t, mock, got, "an empty prefix must not install a wrapper")
}

func TestWithKeyPrefixRejectsAnInvalidPrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{name: "whitespace", prefix: "bad name"},
		{name: "glob_metacharacter", prefix: "orders*"},
		{name: "hash_tag", prefix: "{orders}"},
		{name: "trailing_separator", prefix: "orders:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cache.WithKeyPrefix(cachetest.NewMockCache(), tt.prefix)

			require.ErrorIs(t, err, cache.ErrInvalidKeyPrefix)
			assert.Nil(t, got)
		})
	}
}

func TestWithKeyPrefixRejectsANilCache(t *testing.T) {
	t.Run("untyped_nil", func(t *testing.T) {
		got, err := cache.WithKeyPrefix(nil, kpPrefix)

		require.ErrorIs(t, err, cache.ErrNilCache)
		assert.Nil(t, got)
	})

	t.Run("typed_nil", func(t *testing.T) {
		var inner *cachetest.MockCache

		got, err := cache.WithKeyPrefix(inner, kpPrefix)

		require.ErrorIs(t, err, cache.ErrNilCache)
		assert.Nil(t, got)
	})
}

func TestWithKeyPrefixForwardsLoadTimeout(t *testing.T) {
	t.Run("inner_provides_one", func(t *testing.T) {
		inner := ltTimedCache{MockCache: cachetest.NewMockCache(), bound: 250 * time.Millisecond}

		prefixed, err := cache.WithKeyPrefix(inner, kpPrefix)
		require.NoError(t, err)

		provider, ok := prefixed.(cache.LoadTimeoutProvider)
		require.True(t, ok, "the wrapper must implement LoadTimeoutProvider")
		assert.Equal(t, 250*time.Millisecond, provider.LoadTimeout())
	})

	t.Run("inner_provides_none", func(t *testing.T) {
		prefixed, err := cache.WithKeyPrefix(cachetest.NewMockCache(), kpPrefix)
		require.NoError(t, err)

		provider, ok := prefixed.(cache.LoadTimeoutProvider)
		require.True(t, ok, "the wrapper must implement LoadTimeoutProvider")
		assert.Equal(t, time.Duration(0), provider.LoadTimeout())
	})
}

func TestWithKeyPrefixPassesInnerErrorsThrough(t *testing.T) {
	_, prefixed := kpWrap(t)

	_, err := prefixed.Get(context.Background(), kpKey)

	require.ErrorIs(t, err, cache.ErrNotFound)
}

func TestWithKeyPrefixPassesThroughLifecycleMethods(t *testing.T) {
	t.Run("health", func(t *testing.T) {
		mock := cachetest.NewMockCache().WithHealthFailure(assert.AnError)
		prefixed, err := cache.WithKeyPrefix(mock, kpPrefix)
		require.NoError(t, err)

		require.ErrorIs(t, prefixed.Health(context.Background()), assert.AnError)
	})

	t.Run("stats", func(t *testing.T) {
		mock, prefixed := kpWrap(t)
		require.NoError(t, mock.Set(context.Background(), kpWireKey, []byte("seeded"), kpTTL))

		stats, err := prefixed.Stats()

		require.NoError(t, err)
		cachetest.AssertStatsContains(t, stats, "entry_count", 1)
	})

	t.Run("close", func(t *testing.T) {
		mock, prefixed := kpWrap(t)

		require.NoError(t, prefixed.Close())

		cachetest.AssertCacheClosed(t, mock)
	})
}

func TestWithKeyPrefixComposesWithLoadThrough(t *testing.T) {
	mock, prefixed := kpWrap(t)
	ctx := context.Background()

	got, err := cache.LoadThrough(ctx, prefixed, kpKey, kpTTL, func(context.Context) (string, error) {
		return "from-origin", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "from-origin", got)
	require.Eventually(t, func() bool { return mock.Has(kpWireKey) }, ltWaitFor, ltWaitTick,
		"the load-through write-back must land under the prefixed key")
	assert.Equal(t, []string{kpWireKey}, mock.AllKeys())
}
