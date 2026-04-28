package jose

import (
	"crypto/rsa"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectionString(t *testing.T) {
	assert.Equal(t, "inbound", DirectionInbound.String())
	assert.Equal(t, "outbound", DirectionOutbound.String())
	assert.Equal(t, "unknown", Direction(99).String())
}

func TestPolicyValidateNil(t *testing.T) {
	var p *Policy
	err := p.Validate()
	require.Error(t, err)
	assert.Equal(t, "JOSE_POLICY_NIL", err.(*Error).Code)
}

func TestPolicyValidateUnknownDirection(t *testing.T) {
	p := &Policy{
		Direction: Direction(99),
		SigAlg:    DefaultSigAlg,
		KeyAlg:    DefaultKeyAlg,
		Enc:       DefaultEnc,
	}
	err := p.Validate()
	require.Error(t, err)
	assert.Equal(t, "JOSE_POLICY_DIRECTION_UNKNOWN", err.(*Error).Code)
}

func TestPolicyValidateBadKeyAlg(t *testing.T) {
	p := &Policy{
		Direction:  DirectionInbound,
		DecryptKid: "k", VerifyKid: "p",
		SigAlg: DefaultSigAlg,
		KeyAlg: "RSA1_5", // disallowed
		Enc:    DefaultEnc,
	}
	err := p.Validate()
	require.Error(t, err)
	assert.Equal(t, "JOSE_ALGORITHM_DISALLOWED", err.(*Error).Code)
}

func TestPolicyValidateBadEnc(t *testing.T) {
	p := &Policy{
		Direction:  DirectionInbound,
		DecryptKid: "k", VerifyKid: "p",
		SigAlg: DefaultSigAlg,
		KeyAlg: DefaultKeyAlg,
		Enc:    "A128CBC-HS256", // disallowed
	}
	err := p.Validate()
	require.Error(t, err)
	assert.Equal(t, "JOSE_ALGORITHM_DISALLOWED", err.(*Error).Code)
}

func TestResolvePolicyOutbound(t *testing.T) {
	priv, _ := generateKeyPair(t)
	r := &fixtureResolver{
		priv: map[string]*rsa.PrivateKey{"sign-kid": priv},
		pub:  map[string]*rsa.PublicKey{"enc-kid": &priv.PublicKey},
	}
	p := &Policy{Direction: DirectionOutbound, SignKid: "sign-kid", EncryptKid: "enc-kid"}
	require.NoError(t, ResolvePolicy(r, p))
}

func TestResolvePolicyOutboundMissingEncrypt(t *testing.T) {
	priv, _ := generateKeyPair(t)
	r := &fixtureResolver{
		priv: map[string]*rsa.PrivateKey{"sign-kid": priv},
		pub:  map[string]*rsa.PublicKey{},
	}
	p := &Policy{Direction: DirectionOutbound, SignKid: "sign-kid", EncryptKid: "missing-enc"}
	err := ResolvePolicy(r, p)
	require.Error(t, err)
	assert.Equal(t, "JOSE_KID_UNKNOWN", err.(*Error).Code)
}

func TestResolvePolicyNil(t *testing.T) {
	r := &fixtureResolver{}
	require.NoError(t, ResolvePolicy(r, nil))
}

func TestParseUnixSecsAllArms(t *testing.T) {
	// JSON decoded numbers are float64, but parseUnixSecs accepts int64/int too for
	// callers that pre-parse the claim map. Cover all type-switch arms.
	tests := []struct {
		name string
		in   any
		zero bool
	}{
		{name: "float64", in: float64(1700000000), zero: false},
		{name: "int64", in: int64(1700000000), zero: false},
		{name: "int", in: int(1700000000), zero: false},
		{name: "string_value", in: "not-a-number", zero: true},
		{name: "nil_value", in: nil, zero: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseUnixSecs(tt.in)
			assert.Equal(t, tt.zero, got.IsZero())
		})
	}
}
