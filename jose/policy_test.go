package jose

import (
	"crypto/rsa"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireJOSEErrorCode unwraps err to *Error via errors.As and asserts its Code.
// require.ErrorAs is preferred over require.True+errors.As because it produces a
// failure message naming the expected target type if unwrapping fails.
func requireJOSEErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	var jerr *Error
	require.ErrorAs(t, err, &jerr)
	assert.Equal(t, wantCode, jerr.Code)
}

func TestDirectionString(t *testing.T) {
	assert.Equal(t, "inbound", DirectionInbound.String())
	assert.Equal(t, "outbound", DirectionOutbound.String())
	assert.Equal(t, "unknown", Direction(99).String())
}

func TestPolicyValidateNil(t *testing.T) {
	var p *Policy
	requireJOSEErrorCode(t, p.Validate(), "JOSE_POLICY_NIL")
}

func TestPolicyValidateUnknownDirection(t *testing.T) {
	p := &Policy{
		Direction: Direction(99),
		SigAlg:    DefaultSigAlg,
		KeyAlg:    DefaultKeyAlg,
		Enc:       DefaultEnc,
	}
	requireJOSEErrorCode(t, p.Validate(), "JOSE_POLICY_DIRECTION_UNKNOWN")
}

func TestPolicyValidateBadKeyAlg(t *testing.T) {
	p := &Policy{
		Direction:  DirectionInbound,
		DecryptKid: "k", VerifyKid: "p",
		SigAlg: DefaultSigAlg,
		KeyAlg: "RSA1_5", // disallowed
		Enc:    DefaultEnc,
	}
	requireJOSEErrorCode(t, p.Validate(), "JOSE_ALGORITHM_DISALLOWED")
}

func TestPolicyValidateBadEnc(t *testing.T) {
	p := &Policy{
		Direction:  DirectionInbound,
		DecryptKid: "k", VerifyKid: "p",
		SigAlg: DefaultSigAlg,
		KeyAlg: DefaultKeyAlg,
		Enc:    "A128CBC-HS256", // disallowed
	}
	requireJOSEErrorCode(t, p.Validate(), "JOSE_ALGORITHM_DISALLOWED")
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
	requireJOSEErrorCode(t, ResolvePolicy(r, p), "JOSE_KID_UNKNOWN")
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
