package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// idempotencyFixture is a full literal config with a named database, so the
// map write-back path is under the pin. Static tenants join it with PR2's
// fixtures.
func idempotencyFixture() *Config {
	cfg := createValidFullConfig()
	cfg.Databases = map[string]DatabaseConfig{"reporting": createValidDatabaseConfig()}
	return cfg
}

func TestValidateRejectsNil(t *testing.T) {
	err := Validate(nil)
	require.ErrorIs(t, err, errNilConfig)
}

// TestValidateIsIdempotent pins decision 5 of the normalize/check split design:
// every construction path calls Validate, some more than once, so a second
// pass must be a no-op. Two independently built configs are compared rather
// than a *cfg snapshot, because a shallow snapshot would share any map field
// with the value it is compared against and could not catch a mutation.
func TestValidateIsIdempotent(t *testing.T) {
	t.Run("literal_door", func(t *testing.T) {
		a := idempotencyFixture()
		require.NoError(t, Validate(a))

		b := idempotencyFixture()
		require.NoError(t, Validate(b))
		require.NoError(t, Validate(b))

		require.Equal(t, a, b)
	})

	t.Run("koanf_door", func(t *testing.T) {
		clearEnvironmentVariables()
		defer clearEnvironmentVariables()
		t.Chdir(t.TempDir())

		// a is validated once, by Load itself.
		a, err := Load()
		require.NoError(t, err)

		// b is validated twice: once by Load, once more explicitly; the second
		// pass must keep the koanf handle it was given.
		b, err := Load()
		require.NoError(t, err)
		k := b.k
		require.NoError(t, Validate(b))
		require.Same(t, k, b.k)

		// Two Load calls hold two distinct *koanf.Koanf, so compare copies with
		// the handle blanked rather than mutating the values under test.
		ax, bx := *a, *b
		ax.k, bx.k = nil, nil
		require.Equal(t, ax, bx)
	})
}

// TestCheckDoesNotMutate pins decision 3 of the normalize/check split design:
// check rejects a normalized config without changing it. Both cases normalize
// two independently built, identical configs, then call check on only one of
// them — if check mutated its argument, the pair would no longer be equal.
func TestCheckDoesNotMutate(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		a := createValidFullConfig()
		b := createValidFullConfig()
		require.NoError(t, normalize(a))
		require.NoError(t, normalize(b))

		require.NoError(t, check(a))
		require.Equal(t, a, b)
	})

	// The rejection comes from the last check step, so every earlier step runs
	// on the failing path before the pin is taken.
	t.Run("invalid", func(t *testing.T) {
		a := createValidFullConfig()
		a.Debug.TrustedProxies = []string{"not-a-cidr"}
		b := createValidFullConfig()
		b.Debug.TrustedProxies = []string{"not-a-cidr"}
		require.NoError(t, normalize(a))
		require.NoError(t, normalize(b))

		require.Error(t, check(a))
		require.Equal(t, a, b)
	})
}
