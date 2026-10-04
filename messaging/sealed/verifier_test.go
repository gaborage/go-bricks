package sealed_test

import (
	"context"
	"crypto/rsa"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/sealruntime"
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

// vectorProducerStore is the vector keys as a producer verifies them: every generation PUBLIC.
func vectorProducerStore(t *testing.T) *kstest.MockKeyStore {
	t.Helper()
	return withPublic(vectorSignStore(t), encFamily, "v1", &vectorKey(t, vecEncKid).PublicKey)
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
