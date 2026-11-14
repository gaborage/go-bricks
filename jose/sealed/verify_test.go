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

// faultyResolver answers (nil, err) for one kid's public or private lookup and delegates every other lookup; a nil err is the (nil, nil) hand-back.
type faultyResolver struct {
	keys         bricksjose.KeyResolver
	faultPublic  string
	faultPrivate string
	err          error
}

func (r faultyResolver) PublicKey(kid string) (*rsa.PublicKey, error) {
	if kid == r.faultPublic {
		return nil, r.err
	}
	return r.keys.PublicKey(kid)
}

func (r faultyResolver) PrivateKey(kid string) (*rsa.PrivateKey, error) {
	if kid == r.faultPrivate {
		return nil, r.err
	}
	return r.keys.PrivateKey(kid)
}

// lookupTimeout stands in for a custom resolver's own failure.
type lookupTimeout struct{}

func (*lookupTimeout) Error() string { return "key lookup timed out" }

// producerOptions is the producer's view: both generations as public keys, no tid rule.
func producerOptions(t *testing.T) *sealed.OpenOptions {
	t.Helper()
	return &sealed.OpenOptions{EventType: eventType, Keys: publicOnlyResolver{t: t, keys: testKeys(t).resolver}}
}

func TestVerifyAcceptsWhatSealProduced(t *testing.T) {
	spec, opts := testSpec(t), testOptions(t)
	opts.TenantID = "tenant-a"
	wire, jti, err := sealed.Seal(sampleEvent(), spec, opts)
	require.NoError(t, err)

	env, err := sealed.Verify(wire, spec, producerOptions(t))
	require.NoError(t, err)
	hdr, _ := decodeSegment0(t, string(wire))
	assert.Equal(t, hdr["jti"], env.JTI)
	assert.Equal(t, jti, env.JTI)
	assert.Equal(t, eventType, env.EventType)
	assert.Equal(t, "tenant-a", env.TenantID)
	assert.Equal(t, signKid, env.SignKid)
	assert.Equal(t, "svc-payments-sign", env.SignFamily)
	assert.Equal(t, encKid, env.EncKid)
}

func TestVerifyResolvesTheEncryptKidAsPublic(t *testing.T) {
	spec := testSpec(t)
	wire, _, err := sealed.Seal(sampleEvent(), spec, testOptions(t))
	require.NoError(t, err)
	k := testKeys(t)
	opts := &sealed.OpenOptions{EventType: eventType, Keys: publicOnlyResolver{t: t, keys: jositest.NewTestResolver(map[string]any{
		signKid: &k.signPriv.PublicKey,
	})}}

	env, err := sealed.Verify(wire, spec, opts)
	requireUnprovisionedEncryptRefusal(t, env, err)
}

func TestVerifyFaultsOnANilEncryptKey(t *testing.T) {
	spec := testSpec(t)
	wire, _, err := sealed.Seal(sampleEvent(), spec, testOptions(t))
	require.NoError(t, err)
	opts := &sealed.OpenOptions{EventType: eventType, Keys: faultyResolver{keys: publicOnlyResolver{t: t, keys: testKeys(t).resolver}, faultPublic: encKid}}

	env, err := sealed.Verify(wire, spec, opts)
	assert.Nil(t, env)
	var oe *sealed.OpenError
	assert.False(t, errors.As(err, &oe), "a nil key is a resolver fault, not a refusal")
	var je *bricksjose.Error
	require.ErrorAs(t, err, &je)
	assert.Equal(t, sealed.CodeKeyResolution, je.Code)
	assert.Equal(t, encKid, je.Kid)
	assert.ErrorIs(t, err, bricksjose.ErrKeyResolution)
}

func TestOpenKeepsTheConsumerWordingForAnUnprovisionedEncryptKey(t *testing.T) {
	spec := testSpec(t)
	wire, _, err := sealed.Seal(sampleEvent(), spec, testOptions(t))
	require.NoError(t, err)
	k := testKeys(t)
	opts := &sealed.OpenOptions{EventType: eventType, Keys: jositest.NewTestResolver(map[string]any{
		signKid: &k.signPriv.PublicKey,
	})}
	const want = "encrypt kid generation is not provisioned on this consumer"

	var out paymentAuthorized
	_, err = sealed.Open(wire, spec, opts, &out)
	var oe *sealed.OpenError
	require.ErrorAs(t, err, &oe)
	assert.Equal(t, want, oe.Err.Message)

	_, err = sealed.OpenDocument(wire, spec, opts)
	require.ErrorAs(t, err, &oe)
	assert.Equal(t, want, oe.Err.Message)
}

// keyLookup is one of the three resolver calls a door can reach, with the doors that reach it.
type keyLookup struct {
	name    string
	missing bricksjose.KeyResolver
	fault   func(err error) bricksjose.KeyResolver
	rule    int
	kid     string
	role    string
	details map[string]string
	doors   map[string]string // door -> its unknown-generation message
}

func keyLookups(t *testing.T) []keyLookup {
	t.Helper()
	k := testKeys(t)
	consumer := jositest.NewTestResolver(map[string]any{signKid: &k.signPriv.PublicKey, encKid: k.encPriv})
	return []keyLookup{
		{
			name:    "sign_public",
			missing: jositest.NewTestResolver(map[string]any{encKid: k.encPriv}),
			fault: func(err error) bricksjose.KeyResolver {
				return faultyResolver{keys: consumer, faultPublic: signKid, err: err}
			},
			rule: 4,
			kid:  signKid,
			role: "sign",
			doors: map[string]string{
				"open":          "sign kid generation is not provisioned on this consumer",
				"open_document": "sign kid generation is not provisioned on this consumer",
				"verify":        "sign kid generation is not provisioned in this key set",
			},
		},
		{
			name:    "encrypt_private",
			missing: jositest.NewTestResolver(map[string]any{signKid: &k.signPriv.PublicKey}),
			fault: func(err error) bricksjose.KeyResolver {
				return faultyResolver{keys: consumer, faultPrivate: encKid, err: err}
			},
			rule:    10,
			kid:     encKid,
			role:    "encrypt",
			details: map[string]string{sealed.DetailLayer: "jwe"},
			doors: map[string]string{
				"open":          "encrypt kid generation is not provisioned on this consumer",
				"open_document": "encrypt kid generation is not provisioned on this consumer",
			},
		},
		{
			name:    "encrypt_public",
			missing: jositest.NewTestResolver(map[string]any{signKid: &k.signPriv.PublicKey}),
			fault: func(err error) bricksjose.KeyResolver {
				return faultyResolver{keys: consumer, faultPublic: encKid, err: err}
			},
			rule:    10,
			kid:     encKid,
			role:    "encrypt",
			details: map[string]string{sealed.DetailLayer: "jwe"},
			doors: map[string]string{
				"verify": "encrypt kid generation is not provisioned in this key set",
			},
		},
	}
}

// lookupDoors runs one sealed body through each door that resolves keys.
func lookupDoors(t *testing.T) map[string]func(opts *sealed.OpenOptions) error {
	t.Helper()
	spec := testSpec(t)
	wire, _, err := sealed.Seal(sampleEvent(), spec, testOptions(t))
	require.NoError(t, err)
	return map[string]func(opts *sealed.OpenOptions) error{
		"open": func(opts *sealed.OpenOptions) error {
			var out paymentAuthorized
			_, err := sealed.Open(wire, spec, opts, &out)
			return err
		},
		"open_document": func(opts *sealed.OpenOptions) error {
			_, err := sealed.OpenDocument(wire, spec, opts)
			return err
		},
		"verify": func(opts *sealed.OpenOptions) error {
			_, err := sealed.Verify(wire, spec, opts)
			return err
		},
	}
}

func TestEveryKeyLookupRefusesAnUnknownKidAsAnUnknownGeneration(t *testing.T) {
	doors := lookupDoors(t)
	for _, lk := range keyLookups(t) {
		for door, msg := range lk.doors {
			t.Run(lk.name+"/"+door, func(t *testing.T) {
				err := doors[door](&sealed.OpenOptions{EventType: eventType, Keys: lk.missing})
				var oe *sealed.OpenError
				require.ErrorAs(t, err, &oe)
				assert.Equal(t, sealed.CodeKidUnknownGeneration, oe.Err.Code)
				assert.Equal(t, lk.rule, oe.Rule)
				assert.Equal(t, lk.details, oe.Details)
				assert.Equal(t, lk.kid, oe.Err.Kid)
				assert.Equal(t, msg, oe.Err.Message)
				assert.ErrorIs(t, err, sealed.ErrKidUnknownGeneration)
			})
		}
	}
}

// TestEveryKeyLookupReportsAResolverFault pins that a lookup failing for any reason but an unknown kid,
// including a (nil, nil) hand-back (#1893), is a resolver fault: never a refusal, never a crypto call on a nil key.
func TestEveryKeyLookupReportsAResolverFault(t *testing.T) {
	doors := lookupDoors(t)
	_, noKeystore := bricksjose.NewKeyStoreResolver(nil).PublicKey(signKid)
	require.Error(t, noKeystore)
	faults := map[string]error{
		"nil_key":              nil,
		"keystore_unavailable": noKeystore,
		"resolver_timeout":     &lookupTimeout{},
	}
	for _, lk := range keyLookups(t) {
		for shape, cause := range faults {
			for door := range lk.doors {
				t.Run(lk.name+"/"+shape+"/"+door, func(t *testing.T) {
					err := doors[door](&sealed.OpenOptions{EventType: eventType, Keys: lk.fault(cause)})
					require.Error(t, err)
					var oe *sealed.OpenError
					assert.False(t, errors.As(err, &oe), "a resolver fault is not a refusal")
					assert.NotErrorIs(t, err, sealed.ErrKidUnknownGeneration)
					assert.ErrorIs(t, err, bricksjose.ErrKeyResolution)
					var je *bricksjose.Error
					require.ErrorAs(t, err, &je)
					assert.Equal(t, sealed.CodeKeyResolution, je.Code)
					assert.Equal(t, lk.kid, je.Kid)
					assert.Equal(t, lk.role+" kid lookup failed in the key resolver", je.Message)
					if cause != nil {
						assert.ErrorIs(t, err, cause)
						assert.Equal(t, cause, je.Cause)
					}
					if shape == "resolver_timeout" {
						var timeout *lookupTimeout
						assert.ErrorAs(t, err, &timeout)
					}
				})
			}
		}
	}
}

// requireUnprovisionedEncryptRefusal pins Verify's rule-10 refusal for an encrypt kid its key set cannot serve.
func requireUnprovisionedEncryptRefusal(t *testing.T, env *sealed.Envelope, err error) {
	t.Helper()
	assert.Nil(t, env)
	var oe *sealed.OpenError
	require.ErrorAs(t, err, &oe)
	assert.Equal(t, sealed.CodeKidUnknownGeneration, oe.Err.Code)
	assert.Equal(t, 10, oe.Rule)
	assert.Equal(t, "jwe", oe.Details[sealed.DetailLayer])
	assert.Equal(t, encKid, oe.Err.Kid)
	assert.Equal(t, "encrypt kid generation is not provisioned in this key set", oe.Err.Message)
	assert.ErrorIs(t, err, sealed.ErrKidUnknownGeneration)
}

func TestVerifyJudgesTidOnlyByTheCallersRule(t *testing.T) {
	spec, opts := testSpec(t), testOptions(t)
	opts.TenantID = "tenant-a"
	wire, _, err := sealed.Seal(sampleEvent(), spec, opts)
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
	wire, jti, err := sealed.SealDocument(sampleDocument(), spec, testOptions(t))
	require.NoError(t, err)

	env, err := sealed.Verify(wire, spec, producerOptions(t))
	require.NoError(t, err)
	assert.Equal(t, encKid, env.EncKid)
	hdr, _ := decodeSegment0(t, string(wire))
	assert.Equal(t, jti, hdr["jti"])
	assert.Equal(t, jti, env.JTI)
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
