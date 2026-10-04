package sealed_test

import (
	"context"
	"crypto/rsa"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/sealruntime"
	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	"github.com/gaborage/go-bricks/keystore"
	kstest "github.com/gaborage/go-bricks/keystore/testing"
	"github.com/gaborage/go-bricks/messaging/sealed"
	"github.com/gaborage/go-bricks/multitenant"
)

// withPair provisions an RSA generation the way the real keystore serves it: both halves.
func withPair(s *kstest.MockKeyStore, logical, version string, k *rsa.PrivateKey) *kstest.MockKeyStore {
	return withPrivate(s, logical, version, k).WithPublicKey(logical+"-"+version, &k.PublicKey)
}

// pairStore is the canonical producer (sign v1 pair, encrypt v1 public) as the real keystore serves it.
func pairStore(t *testing.T) *kstest.MockKeyStore {
	t.Helper()
	keys(t)
	return producerStore(t).WithPublicKey(signFamily+"-v1", &signPriv.PublicKey)
}

func configureStore(t *testing.T, store *kstest.MockKeyStore, active map[string]string) {
	t.Helper()
	sealruntime.Configure(&sealruntime.Runtime{KeyStore: store, Active: active, Tenancy: sealruntime.TenancyDisabled})
}

// vectorProducerStore is the vector keys as the producer holds them: sign v1 PUBLIC, sign v2 (the
// generation every accepted vector is signed under) as a pair, encrypt v1 PUBLIC.
func vectorProducerStore(t *testing.T) *kstest.MockKeyStore {
	t.Helper()
	store := withPublic(kstest.NewMockKeyStore(), signFamily, "v1", &vectorKey(t, vecSignKidV1).PublicKey)
	store = withPair(store, signFamily, "v2", vectorKey(t, vecSignKid))
	return withPublic(store, encFamily, "v1", &vectorKey(t, vecEncKid).PublicKey)
}

// publicOnlySignStore holds the canonical producer's sign v1 without its private key.
func publicOnlySignStore(t *testing.T) *kstest.MockKeyStore {
	t.Helper()
	keys(t)
	store := withPublic(kstest.NewMockKeyStore(), signFamily, "v1", &signPriv.PublicKey)
	return withPublic(store, encFamily, "v1", &encPriv.PublicKey)
}

// sealOnPair seals one event under the canonical pair store's sign v1.
func sealOnPair(t *testing.T) []byte {
	t.Helper()
	configureStore(t, pairStore(t), nil)
	data, _, err := declare(t).Seal(context.Background(), paymentAuthorized{OrderID: "o7", Card: &cardData{PAN: testPAN}})
	require.NoError(t, err)
	return data
}

// absentSignGenerationMessage is the refusal text for a sign kid with no entry in the producer's key set.
const absentSignGenerationMessage = "sign kid generation is not provisioned in this key set"

// requireNotSignable asserts the refusal of an authentic body under a sign generation the producer
// holds without its private key: the unknown-generation class on the outer layer, with its own text.
func requireNotSignable(t *testing.T, err error, kid string) {
	t.Helper()
	require.ErrorIs(t, err, josesealed.ErrKidUnknownGeneration)
	require.NotErrorIs(t, err, sealed.ErrRoleMismatch)
	var refused *sealruntime.OpenRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, josesealed.CodeKidUnknownGeneration, refused.Code)
	assert.True(t, refused.Recoverable)
	assert.Empty(t, refused.Details, "the refusal is on the outer layer")
	var oe *josesealed.OpenError
	require.ErrorAs(t, err, &oe)
	assert.Equal(t, 4, oe.Rule)
	assert.Nil(t, oe.Details)
	assert.Equal(t, josesealed.CodeKidUnknownGeneration, oe.Err.Code)
	assert.Equal(t, kid, oe.Err.Kid)
	assert.NoError(t, oe.Err.Cause)
	assert.NotEmpty(t, oe.Err.Message)
	assert.NotEqual(t, absentSignGenerationMessage, oe.Err.Message, "held without its private key is not absent")
}

func verifierProvider(t *testing.T) sealruntime.VerifierProvider {
	t.Helper()
	provider, ok := sealruntime.Registered().(sealruntime.VerifierProvider)
	require.True(t, ok, "messaging/sealed's codec implements the producer verification")
	return provider
}

func newVerifier(t *testing.T, store *kstest.MockKeyStore) sealruntime.Verifier {
	t.Helper()
	v, err := verifierProvider(t).NewVerifier(spec(t), eventType, &sealruntime.Runtime{KeyStore: store})
	require.NoError(t, err)
	return v
}

func TestNewVerifierStartupMatrix(t *testing.T) {
	provider := verifierProvider(t)
	store := vectorProducerStore(t)
	cases := []struct {
		name      string
		spec      sealruntime.Spec
		eventType string
		rt        *sealruntime.Runtime
		want      error
		text      string
	}{
		{name: "foreign_spec", spec: foreignSpec{}, eventType: eventType, rt: &sealruntime.Runtime{KeyStore: store}, text: "not produced by this codec"},
		{name: "nil_runtime", spec: spec(t), eventType: eventType, want: sealruntime.ErrKeyStoreMissing},
		{name: "nil_keystore", spec: spec(t), eventType: eventType, rt: &sealruntime.Runtime{}, want: sealruntime.ErrKeyStoreMissing},
		{name: "familyless_keystore", spec: spec(t), eventType: eventType, rt: &sealruntime.Runtime{KeyStore: familyless{s: store}}, want: sealed.ErrKeyStoreNoFamilies},
		{name: "empty_event_type", spec: spec(t), rt: &sealruntime.Runtime{KeyStore: store}, text: "EventType"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(store.Recorded())
			v, err := provider.NewVerifier(tc.spec, tc.eventType, tc.rt)
			assert.Nil(t, v)
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
			if tc.text != "" {
				assert.Contains(t, err.Error(), tc.text)
			}
			assert.Len(t, store.Recorded(), before, "a refused verifier tags nothing")
		})
	}
}

func TestNewVerifierTagsEveryProvisionedGenerationAsSeal(t *testing.T) {
	store := withSecret(vectorProducerStore(t), encFamily, "v2")
	before := len(store.Recorded())
	newVerifier(t, store)
	assert.ElementsMatch(t, [][2]string{
		{signFamily + "-v1", keystore.RoleTagSeal}, {signFamily + "-v2", keystore.RoleTagSeal}, {encFamily + "-v1", keystore.RoleTagSeal},
	}, store.Recorded()[before:], "every RSA generation, active or not; never a secret")
}

// TestVerifierAdmitsOnlyASignGenerationHeldWithItsPrivateKey builds two stores and two verifiers
// for the same bytes: the producer could have signed them only where it holds sign v1's private key.
func TestVerifierAdmitsOnlyASignGenerationHeldWithItsPrivateKey(t *testing.T) {
	data := sealOnPair(t)

	env, err := newVerifier(t, publicOnlySignStore(t)).Verify(t.Context(), data)
	assert.Zero(t, env)
	requireNotSignable(t, err, signFamily+"-v1")

	env, err = newVerifier(t, pairStore(t)).Verify(t.Context(), data)
	require.NoError(t, err)
	assert.Equal(t, signFamily+"-v1", env.SignKid)
}

// TestVerifierAdmitsByIndexRoleNotPrivateMaterial indexes sign v1 as RolePrivate while serving only
// its public key: admission reads the role and never resolves a private key.
func TestVerifierAdmitsByIndexRoleNotPrivateMaterial(t *testing.T) {
	data := sealOnPair(t)
	store := kstest.NewMockKeyStore().
		WithPublicKey(signFamily+"-v1", &signPriv.PublicKey).WithGeneration(signFamily, "v1", keystore.RolePrivate)
	store = withPublic(store, encFamily, "v1", &encPriv.PublicKey)
	_, privErr := store.PrivateKey(signFamily + "-v1")
	require.Error(t, privErr, "the store serves no private material for sign v1")

	env, err := newVerifier(t, store).Verify(t.Context(), data)
	require.NoError(t, err)
	assert.Equal(t, signFamily+"-v1", env.SignKid)
}

// TestVerifierRefusesASignGenerationMissingFromTheIndex serves sign v1's public key by entry name with
// no generation index entry: the verification passes, and the role rule fails closed.
func TestVerifierRefusesASignGenerationMissingFromTheIndex(t *testing.T) {
	data := sealOnPair(t)
	store := withPublic(kstest.NewMockKeyStore().WithPublicKey(signFamily+"-v1", &signPriv.PublicKey), encFamily, "v1", &encPriv.PublicKey)

	env, err := newVerifier(t, store).Verify(t.Context(), data)
	assert.Zero(t, env)
	requireNotSignable(t, err, signFamily+"-v1")
}

func TestVerifierAcceptsWhatTheSealerProduced(t *testing.T) {
	store := pairStore(t)
	configureStore(t, store, nil)
	h := declare(t)
	data, jti, err := h.Seal(multitenant.SetTenant(context.Background(), "tenant-a"), paymentAuthorized{OrderID: "o8", Card: &cardData{PAN: testPAN}})
	require.NoError(t, err)

	env, err := newVerifier(t, store).Verify(context.Background(), data)
	require.NoError(t, err)
	assert.Equal(t, jti, env.JTI)
	assert.Equal(t, "tenant-a", env.TenantID)
	assert.Equal(t, signFamily+"-v1", env.SignKid)
	assert.Equal(t, encFamily+"-v1", env.EncKid)
}

// TestVerifierMapsEveryPublishedVector is the opener's vector test for the producer: the same
// refusal assertions, and four vectors the verification accepts, each with its signed tid surfaced.
func TestVerifierMapsEveryPublishedVector(t *testing.T) {
	v := newVerifier(t, vectorProducerStore(t))
	accepted := map[string]string{
		"wrong_key_same_name": vecTenant, "opened_document_wrong_shape": vecTenant, // nothing before the decrypt sees them
		"tid_mismatch": "tenant-b", "tid_absent_but_required": "", // the door judges the tid itself, by strict equality
	}
	hits := 0
	for _, tc := range loadVectors(t).Vectors {
		t.Run(tc.Name, func(t *testing.T) {
			env, err := v.Verify(t.Context(), []byte(tc.Body))
			if tid, ok := accepted[tc.Name]; ok {
				hits++
				require.NoError(t, err)
				assert.NotEmpty(t, env.JTI)
				assert.Equal(t, tid, env.TenantID, "the signed tid is surfaced, never judged")
				return
			}
			assert.Zero(t, env)
			requireSeamRefusal(t, err, tc.Code, tc.Layer, tc.Slot)
		})
	}
	assert.Equal(t, len(accepted), hits, "every accepted vector is still published")
}
