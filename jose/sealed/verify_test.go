package sealed_test

import (
	"crypto/rsa"
	"errors"
	"strings"
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
	k := testKeys(t)
	return &sealed.OpenOptions{EventType: eventType, Keys: publicOnlyResolver{t: t, keys: jositest.NewTestResolver(map[string]any{
		signKid: &k.signPriv.PublicKey,
		encKid:  &k.encPriv.PublicKey,
	})}}
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

// wiringCase is one pre-flight mistake a type-free door (OpenDocument, Verify) must refuse.
type wiringCase struct {
	name string
	spec *sealed.Spec
	opts *sealed.OpenOptions
}

// wiringMistakes is the pre-flight table the type-free doors share: each row leaves out one argument.
func wiringMistakes(spec *sealed.Spec, keys bricksjose.KeyResolver) []wiringCase {
	return []wiringCase{
		{name: "nil_spec", opts: &sealed.OpenOptions{EventType: eventType, Keys: keys}},
		{name: "nil_opts", spec: spec},
		{name: "nil_keys", spec: spec, opts: &sealed.OpenOptions{EventType: eventType}},
		{name: "empty_event_type", spec: spec, opts: &sealed.OpenOptions{Keys: keys}},
	}
}

// requirePreflightRefusal asserts a wiring mistake: an *OpenError with no rule, CodeOptionsInvalid,
// a message naming the door the caller called, and ErrSealFailed.
func requirePreflightRefusal(t *testing.T, err error, door string) {
	t.Helper()
	var oe *sealed.OpenError
	require.ErrorAs(t, err, &oe, "every failure of a type-free door is an *OpenError")
	assert.Zero(t, oe.Rule, "pre-flight, no rule fired")
	var je *bricksjose.Error
	require.ErrorAs(t, err, &je)
	assert.Equal(t, sealed.CodeOptionsInvalid, je.Code)
	assert.True(t, strings.HasPrefix(je.Message, door+" requires "), "the message names the door the caller called: %q", je.Message)
	assert.ErrorIs(t, err, sealed.ErrSealFailed)
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
