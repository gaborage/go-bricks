package sealed_test

import (
	"crypto/rsa"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bricksjose "github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/jose/sealed"
	jositest "github.com/gaborage/go-bricks/jose/testing"
)

// publicOnlyResolver serves public keys and fails the test on any private-key request.
type publicOnlyResolver struct {
	t    *testing.T
	keys bricksjose.KeyResolver
}

func (r publicOnlyResolver) PublicKey(kid string) (*rsa.PublicKey, error) {
	return r.keys.PublicKey(kid)
}

func (r publicOnlyResolver) PrivateKey(kid string) (*rsa.PrivateKey, error) {
	r.t.Errorf("Verify requested the private key of %q", kid)
	return nil, errors.New("private key requested")
}

// producerOptions is the producer's view: both generations as public keys, no tid rule.
func producerOptions(t *testing.T) *sealed.OpenOptions {
	t.Helper()
	return &sealed.OpenOptions{EventType: eventType, Keys: publicOnlyResolver{t: t, keys: testKeys(t).resolver}}
}

func TestVerifyAcceptsWhatSealProduced(t *testing.T) {
	spec, opts := testSpec(t), testOptions(t)
	opts.TenantID = "tenant-a"
	wire, err := sealed.Seal(sampleEvent(), spec, opts)
	require.NoError(t, err)

	env, err := sealed.Verify(wire, spec, producerOptions(t))
	require.NoError(t, err)
	hdr, _ := decodeSegment0(t, string(wire))
	assert.Equal(t, hdr["jti"], env.JTI)
	assert.Equal(t, eventType, env.EventType)
	assert.Equal(t, "tenant-a", env.TenantID)
	assert.Equal(t, signKid, env.SignKid)
	assert.Equal(t, "svc-payments-sign", env.SignFamily)
	assert.Equal(t, encKid, env.EncKid)
}

func TestVerifyResolvesTheEncryptKidAsPublic(t *testing.T) {
	spec := testSpec(t)
	wire, err := sealed.Seal(sampleEvent(), spec, testOptions(t))
	require.NoError(t, err)
	k := testKeys(t)
	opts := &sealed.OpenOptions{EventType: eventType, Keys: publicOnlyResolver{t: t, keys: jositest.NewTestResolver(map[string]any{
		signKid: &k.signPriv.PublicKey,
	})}}

	env, err := sealed.Verify(wire, spec, opts)
	assert.Nil(t, env)
	var oe *sealed.OpenError
	require.ErrorAs(t, err, &oe)
	assert.Equal(t, sealed.CodeKidUnknownGeneration, oe.Err.Code)
	assert.Equal(t, 10, oe.Rule)
	assert.Equal(t, "jwe", oe.Details[sealed.DetailLayer])
	assert.Equal(t, encKid, oe.Err.Kid)
	assert.ErrorIs(t, err, sealed.ErrKidUnknownGeneration)
}

func TestVerifyJudgesTidOnlyByTheCallersRule(t *testing.T) {
	spec, opts := testSpec(t), testOptions(t)
	opts.TenantID = "tenant-a"
	wire, err := sealed.Seal(sampleEvent(), spec, opts)
	require.NoError(t, err)
	cases := []struct {
		name     string
		rule     sealed.TenantExpectation
		wantCode string
	}{
		{name: "zero_rule_surfaces_the_tid"},
		{name: "matching_expectation", rule: sealed.TenantExpectation{Required: true, Expected: "tenant-a"}},
		{name: "different_expectation", rule: sealed.TenantExpectation{Expected: "tenant-b"}, wantCode: sealed.CodeTenantMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vopts := producerOptions(t)
			vopts.Tenant = tc.rule
			env, err := sealed.Verify(wire, spec, vopts)
			if tc.wantCode == "" {
				require.NoError(t, err)
				assert.Equal(t, "tenant-a", env.TenantID)
				return
			}
			var oe *sealed.OpenError
			require.ErrorAs(t, err, &oe)
			assert.Equal(t, tc.wantCode, oe.Err.Code)
			assert.Equal(t, 8, oe.Rule)
		})
	}
}

func TestVerifyAcceptsADocumentSpec(t *testing.T) {
	spec := documentSpec(t)
	wire, err := sealed.SealDocument(sampleDocument(), spec, testOptions(t))
	require.NoError(t, err)

	env, err := sealed.Verify(wire, spec, producerOptions(t))
	require.NoError(t, err)
	assert.Equal(t, encKid, env.EncKid)
}

func TestVerifyRejectsWiringMistakes(t *testing.T) {
	for _, tc := range wiringMistakes(testSpec(t), testKeys(t).resolver) {
		t.Run(tc.name, func(t *testing.T) {
			env, err := sealed.Verify([]byte("a.b.c"), tc.spec, tc.opts)
			assert.Nil(t, env)
			requirePreflightRefusal(t, err, "Verify")
		})
	}
}

// verifyResiduals are the published vectors Verify accepts and Open refuses: nothing before the
// decrypt can tell a wrong key under the right kid, or a document of the wrong shape.
var verifyResiduals = map[string]bool{"wrong_key_same_name": true, "opened_document_wrong_shape": true}

func verifyOptions(t *testing.T, k *vectorKeys, tenant *tenantRule) *sealed.OpenOptions {
	t.Helper()
	opts := vectorOptions(k, tenant)
	opts.Keys = publicOnlyResolver{t: t, keys: k.consumer}
	return opts
}

func TestVerifyPositiveVector(t *testing.T) {
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)
	env, err := sealed.Verify([]byte(vf.Positive), testSpec(t), verifyOptions(t, k, nil))
	require.NoError(t, err)
	assert.Equal(t, positiveEnvelope(), env)
}

func TestVerifyNegativeVectors(t *testing.T) {
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)
	require.NotEmpty(t, vf.Vectors)
	for _, tc := range vf.Vectors {
		t.Run(tc.Name, func(t *testing.T) {
			env, err := sealed.Verify([]byte(tc.Body), testSpec(t), verifyOptions(t, k, tc.Tenant))
			if verifyResiduals[tc.Name] {
				require.NoError(t, err, "a documented residual: the consumer refuses it")
				assert.Equal(t, vecJTI, env.JTI)
				return
			}
			assert.Nil(t, env)
			requireVectorRefusal(t, err, &tc)
		})
	}
}

// TestVerifyResidualsAreTheDecryptAndDecodeVectors keeps a vector added later out of the residual set.
func TestVerifyResidualsAreTheDecryptAndDecodeVectors(t *testing.T) {
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)
	got := map[string]bool{}
	for _, v := range vf.Vectors {
		if v.Code == sealed.CodeDecryptFailed || v.Rule == 11 {
			got[v.Name] = true
		}
	}
	assert.Equal(t, verifyResiduals, got)
}
