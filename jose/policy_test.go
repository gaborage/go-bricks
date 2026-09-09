package jose

import (
	"crypto/rsa"
	"testing"

	joselib "github.com/go-jose/go-jose/v4"
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

func TestSealModeString(t *testing.T) {
	assert.Equal(t, "jwe-of-jws", SealModeJWEofJWS.String())
	assert.Equal(t, "bare-jwe", SealModeBareJWE.String())
	assert.Equal(t, "unknown", SealMode(99).String())
}

// bareOutbound / bareInbound are minimally valid bare-mode policies the mode tests mutate.
func bareOutbound() *Policy {
	return &Policy{
		Direction:  DirectionOutbound,
		Mode:       SealModeBareJWE,
		EncryptKid: "peer-key",
		KeyAlg:     DefaultKeyAlg,
		Enc:        joselib.A128GCM,
	}
}

func bareInbound() *Policy {
	return &Policy{
		Direction:  DirectionInbound,
		Mode:       SealModeBareJWE,
		DecryptKid: "our-key",
		KeyAlg:     DefaultKeyAlg,
		Enc:        joselib.A128GCM,
	}
}

func TestPolicyValidateUnknownMode(t *testing.T) {
	p := bareOutbound()
	p.Mode = SealMode(99)
	requireJOSEErrorCode(t, p.Validate(), "JOSE_POLICY_MODE_UNKNOWN")
}

func TestPolicyValidateBareModeDirectionRules(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(p *Policy)
		base    func() *Policy
		wantErr string
	}{
		{"outbound_minimal_is_valid", func(*Policy) {}, bareOutbound, ""},
		{"outbound_without_encrypt_kid", func(p *Policy) { p.EncryptKid = "" }, bareOutbound, codePolicyIncomplete},
		{"outbound_with_sign_kid", func(p *Policy) { p.SignKid = "our-key" }, bareOutbound, codePolicyDirectionMismatch},
		{"outbound_with_verify_kid", func(p *Policy) { p.VerifyKid = "peer-key" }, bareOutbound, codePolicyDirectionMismatch},
		{"outbound_with_decrypt_kid", func(p *Policy) { p.DecryptKid = "our-key" }, bareOutbound, codePolicyDirectionMismatch},
		{"outbound_with_sig_alg", func(p *Policy) { p.SigAlg = DefaultSigAlg }, bareOutbound, codePolicyDirectionMismatch},
		{"inbound_minimal_is_valid", func(*Policy) {}, bareInbound, ""},
		{"inbound_without_decrypt_kid", func(p *Policy) { p.DecryptKid = "" }, bareInbound, codePolicyIncomplete},
		{"inbound_with_verify_kid", func(p *Policy) { p.VerifyKid = "peer-key" }, bareInbound, codePolicyDirectionMismatch},
		{"inbound_with_sign_kid", func(p *Policy) { p.SignKid = "our-key" }, bareInbound, codePolicyDirectionMismatch},
		{"inbound_with_encrypt_kid", func(p *Policy) { p.EncryptKid = "peer-key" }, bareInbound, codePolicyDirectionMismatch},
		{"inbound_with_sig_alg", func(p *Policy) { p.SigAlg = DefaultSigAlg }, bareInbound, codePolicyDirectionMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.base()
			tt.mutate(p)
			err := p.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrPolicyMismatch)
			requireJOSEErrorCode(t, err, tt.wantErr)
		})
	}
}

// nestedOutbound is a minimally valid JWE-of-JWS policy, the default posture.
func nestedOutbound() *Policy {
	return &Policy{
		Direction: DirectionOutbound,
		SignKid:   "our-key", EncryptKid: "peer-key",
		SigAlg: DefaultSigAlg, KeyAlg: DefaultKeyAlg, Enc: DefaultEnc,
	}
}

func TestPolicyValidateNestedModeRejectsBareOnlyFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(p *Policy)
	}{
		{"typ", func(p *Policy) { p.Typ = "JOSE" }},
		{"protected_headers", func(p *Policy) { p.ProtectedHeaders = map[string]any{"custom": "v"} }},
		{"empty_protected_headers_map", func(p *Policy) { p.ProtectedHeaders = map[string]any{} }},
		{"iat_millis", func(p *Policy) { p.IATMillis = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := nestedOutbound()
			tt.mutate(p)
			err := p.Validate()
			require.ErrorIs(t, err, ErrPolicyMismatch)
			requireJOSEErrorCode(t, err, "JOSE_POLICY_MODE_MISMATCH")
		})
	}
}

func TestPolicyValidateNestedModeStaysValidWithoutBareFields(t *testing.T) {
	require.NoError(t, nestedOutbound().Validate())
}

func TestPolicyValidateBareModeProtectedHeaderCollisions(t *testing.T) {
	tests := []struct {
		name      string
		headers   map[string]any
		iatMillis bool
		wantCode  string
	}{
		{"custom_header_allowed", map[string]any{"iss": "acme"}, false, ""},
		{"iat_allowed_when_not_stamping", map[string]any{"iat": 1}, false, ""},
		{"iat_conflicts_with_stamping", map[string]any{"iat": 1}, true, "JOSE_POLICY_HEADER_COLLISION"},
		{"owned_alg", map[string]any{"alg": "RSA-OAEP-256"}, false, "JOSE_POLICY_HEADER_COLLISION"},
		{"owned_enc", map[string]any{"enc": "A128GCM"}, false, "JOSE_POLICY_HEADER_COLLISION"},
		{"owned_kid", map[string]any{"kid": "peer-key"}, false, "JOSE_POLICY_HEADER_COLLISION"},
		{"owned_cty", map[string]any{"cty": "application/json"}, false, "JOSE_POLICY_HEADER_COLLISION"},
		{"owned_typ", map[string]any{"typ": "JOSE"}, false, "JOSE_POLICY_HEADER_COLLISION"},
		{"reserved_crit", map[string]any{"crit": []string{"exp"}}, false, "JOSE_POLICY_HEADER_COLLISION"},
		{"reserved_zip", map[string]any{"zip": "DEF"}, false, "JOSE_POLICY_HEADER_COLLISION"},
		{"reserved_jwk", map[string]any{"jwk": "x"}, false, "JOSE_POLICY_HEADER_COLLISION"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := bareOutbound()
			p.ProtectedHeaders = tt.headers
			p.IATMillis = tt.iatMillis
			err := p.Validate()
			if tt.wantCode == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrPolicyMismatch)
			requireJOSEErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestPolicyValidateContentEncPerMode(t *testing.T) {
	tests := []struct {
		name     string
		bare     bool
		enc      joselib.ContentEncryption
		wantCode string
	}{
		{"bare_a128gcm", true, joselib.A128GCM, ""},
		{"bare_a256gcm", true, joselib.A256GCM, ""},
		{"bare_a128cbc_hs256", true, joselib.A128CBC_HS256, codeAlgorithmDisallowed},
		{"bare_unset", true, "", codeAlgorithmDisallowed},
		{"nested_a256gcm", false, joselib.A256GCM, ""},
		{"nested_a128gcm", false, joselib.A128GCM, codeAlgorithmDisallowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := nestedOutbound()
			if tt.bare {
				p = bareOutbound()
			}
			p.Enc = tt.enc
			err := p.Validate()
			if tt.wantCode == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrAlgorithmDisallowed)
			requireJOSEErrorCode(t, err, tt.wantCode)
		})
	}
}

// TestPolicyValidateBareModeStillRequiresApprovedKeyAlg pins the one algorithm rule bare
// mode does NOT relax.
func TestPolicyValidateBareModeStillRequiresApprovedKeyAlg(t *testing.T) {
	p := bareOutbound()
	p.KeyAlg = joselib.RSA1_5
	err := p.Validate()
	require.ErrorIs(t, err, ErrAlgorithmDisallowed)
	requireJOSEErrorCode(t, err, codeAlgorithmDisallowed)
}
