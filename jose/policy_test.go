package jose

import (
	"crypto/rsa"
	"errors"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
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
	assert.Equal(t, "jws-of-jwe", SealModeJWSofJWE.String())
	assert.Equal(t, "unknown", SealMode(99).String())
}

// bareOutbound / bareInbound are minimally valid bare-mode policies the mode tests mutate.
func bareOutbound() *Policy {
	return &Policy{
		Direction:  DirectionOutbound,
		Mode:       SealModeBareJWE,
		EncryptKid: "peer-key",
		KeyAlg:     DefaultKeyAlg,
		Enc:        jose.A128GCM,
	}
}

func bareInbound() *Policy {
	return &Policy{
		Direction:  DirectionInbound,
		Mode:       SealModeBareJWE,
		DecryptKid: "our-key",
		KeyAlg:     DefaultKeyAlg,
		Enc:        jose.A128GCM,
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

func TestPolicyValidateRejectsSealHeaderFieldsWhereTheyAreNeverWritten(t *testing.T) {
	tests := []struct {
		name     string
		base     func() *Policy
		mutate   func(p *Policy)
		wantCode string
	}{
		{"nested_typ", nestedOutbound, func(p *Policy) { p.Typ = "JOSE" }, codePolicyModeMismatch},
		{"nested_protected_headers", nestedOutbound, func(p *Policy) {
			p.ProtectedHeaders = map[string]any{"custom": "v"}
		}, codePolicyModeMismatch},
		{"nested_empty_protected_headers_map", nestedOutbound, func(p *Policy) {
			p.ProtectedHeaders = map[string]any{}
		}, codePolicyModeMismatch},
		{"nested_iat_millis", nestedOutbound, func(p *Policy) { p.IATMillis = true }, codePolicyModeMismatch},
		{"bare_inbound_typ", bareInbound, func(p *Policy) { p.Typ = "JOSE" }, codePolicyDirectionMismatch},
		{"bare_inbound_protected_headers", bareInbound, func(p *Policy) {
			p.ProtectedHeaders = map[string]any{"iss": "acme"}
		}, codePolicyDirectionMismatch},
		{"bare_inbound_empty_protected_headers_map", bareInbound, func(p *Policy) {
			p.ProtectedHeaders = map[string]any{}
		}, codePolicyDirectionMismatch},
		{"bare_inbound_iat_millis", bareInbound, func(p *Policy) { p.IATMillis = true }, codePolicyDirectionMismatch},
		{"jws_of_jwe_inbound_typ", jwsOfJWEInbound, func(p *Policy) { p.Typ = "JOSE" }, codePolicyDirectionMismatch},
		{"jws_of_jwe_inbound_protected_headers", jwsOfJWEInbound, func(p *Policy) {
			p.ProtectedHeaders = map[string]any{}
		}, codePolicyDirectionMismatch},
		{"jws_of_jwe_inbound_iat_millis", jwsOfJWEInbound, func(p *Policy) { p.IATMillis = true }, codePolicyDirectionMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.base()
			tt.mutate(p)
			err := p.Validate()
			require.ErrorIs(t, err, ErrPolicyMismatch)
			requireJOSEErrorCode(t, err, tt.wantCode)
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
		enc      jose.ContentEncryption
		wantCode string
	}{
		{"bare_a128gcm", true, jose.A128GCM, ""},
		{"bare_a256gcm", true, jose.A256GCM, ""},
		{"bare_a128cbc_hs256", true, jose.A128CBC_HS256, codeAlgorithmDisallowed},
		{"bare_unset", true, "", codeAlgorithmDisallowed},
		{"nested_a256gcm", false, jose.A256GCM, ""},
		{"nested_a128gcm", false, jose.A128GCM, codeAlgorithmDisallowed},
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

// jwsOfJWEOutbound / jwsOfJWEInbound are minimally valid JWS-of-JWE policies the mode tests mutate.
func jwsOfJWEOutbound() *Policy {
	p := nestedOutbound()
	p.Mode = SealModeJWSofJWE
	return p
}

func jwsOfJWEInbound() *Policy {
	return &Policy{
		Direction:  DirectionInbound,
		Mode:       SealModeJWSofJWE,
		DecryptKid: "our-key", VerifyKid: "peer-key",
		SigAlg: DefaultSigAlg, KeyAlg: DefaultKeyAlg, Enc: DefaultEnc,
	}
}

func TestPolicyValidateJWSofJWEModeRules(t *testing.T) {
	tests := []struct {
		name     string
		base     func() *Policy
		mutate   func(p *Policy)
		wantCode string
	}{
		{"outbound_minimal_is_valid", jwsOfJWEOutbound, func(*Policy) {}, ""},
		{"outbound_without_sign_kid", jwsOfJWEOutbound, func(p *Policy) { p.SignKid = "" }, codePolicyIncomplete},
		{"outbound_without_encrypt_kid", jwsOfJWEOutbound, func(p *Policy) { p.EncryptKid = "" }, codePolicyIncomplete},
		{"outbound_with_decrypt_kid", jwsOfJWEOutbound, func(p *Policy) { p.DecryptKid = "our-key" }, codePolicyDirectionMismatch},
		{"outbound_with_verify_kid", jwsOfJWEOutbound, func(p *Policy) { p.VerifyKid = "peer-key" }, codePolicyDirectionMismatch},
		{"outbound_without_sig_alg", jwsOfJWEOutbound, func(p *Policy) { p.SigAlg = "" }, codeAlgorithmDisallowed},
		{"outbound_a128gcm", jwsOfJWEOutbound, func(p *Policy) { p.Enc = jose.A128GCM }, codeAlgorithmDisallowed},
		{"outbound_typ", jwsOfJWEOutbound, func(p *Policy) { p.Typ = "JOSE" }, ""},
		{"outbound_protected_headers", jwsOfJWEOutbound, func(p *Policy) {
			p.ProtectedHeaders = map[string]any{"iss": "acme"}
		}, ""},
		{"outbound_iat_millis", jwsOfJWEOutbound, func(p *Policy) { p.IATMillis = true }, ""},
		{"outbound_owned_header", jwsOfJWEOutbound, func(p *Policy) {
			p.ProtectedHeaders = map[string]any{"kid": "peer-key"}
		}, codePolicyHeaderCollision},
		{"outbound_iat_header_while_stamping", jwsOfJWEOutbound, func(p *Policy) {
			p.ProtectedHeaders = map[string]any{"iat": 1}
			p.IATMillis = true
		}, codePolicyHeaderCollision},
		{"inbound_minimal_is_valid", jwsOfJWEInbound, func(*Policy) {}, ""},
		{"inbound_without_decrypt_kid", jwsOfJWEInbound, func(p *Policy) { p.DecryptKid = "" }, codePolicyIncomplete},
		{"inbound_without_verify_kid", jwsOfJWEInbound, func(p *Policy) { p.VerifyKid = "" }, codePolicyIncomplete},
		{"inbound_with_sign_kid", jwsOfJWEInbound, func(p *Policy) { p.SignKid = "our-key" }, codePolicyDirectionMismatch},
		{"inbound_with_encrypt_kid", jwsOfJWEInbound, func(p *Policy) { p.EncryptKid = "peer-key" }, codePolicyDirectionMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.base()
			tt.mutate(p)
			err := p.Validate()
			if tt.wantCode == "" {
				require.NoError(t, err)
				return
			}
			requireJOSEErrorCode(t, err, tt.wantCode)
		})
	}
}

// TestPolicyValidateBareModeStillRequiresApprovedKeyAlg pins the one algorithm rule bare
// mode does NOT relax.
func TestPolicyValidateBareModeStillRequiresApprovedKeyAlg(t *testing.T) {
	p := bareOutbound()
	p.KeyAlg = jose.RSA1_5
	err := p.Validate()
	require.ErrorIs(t, err, ErrAlgorithmDisallowed)
	requireJOSEErrorCode(t, err, codeAlgorithmDisallowed)
}

// nestedInbound is a minimally valid JWE-of-JWS inbound policy.
func nestedInbound() *Policy {
	p := jwsOfJWEInbound()
	p.Mode = SealModeJWEofJWS
	return p
}

// refusingResolver fails the test if any key is resolved: a policy refusal must surface
// before key resolution.
type refusingResolver struct{ t *testing.T }

func (r refusingResolver) PrivateKey(kid string) (*rsa.PrivateKey, error) {
	r.t.Errorf("PrivateKey(%q) resolved before the policy was refused", kid)
	return nil, errors.New("unexpected key resolution")
}

func (r refusingResolver) PublicKey(kid string) (*rsa.PublicKey, error) {
	r.t.Errorf("PublicKey(%q) resolved before the policy was refused", kid)
	return nil, errors.New("unexpected key resolution")
}

// throughDoor hands p to the door matching its Direction.
func throughDoor(t *testing.T, p *Policy) error {
	t.Helper()
	if p.Direction == DirectionInbound {
		return openErr("", p, refusingResolver{t})
	}
	_, err := Seal([]byte(`{}`), p, refusingResolver{t})
	return err
}

func TestPolicyValidateKidOptIns(t *testing.T) {
	acceptMissingKid := func(p *Policy) { p.AcceptMissingKid = true }
	omitKid := func(p *Policy) { p.OmitKid = true }
	tests := []policyCodeCase{
		{"jwe_of_jws_inbound_accept_missing_kid", nestedInbound, acceptMissingKid, codePolicyModeMismatch},
		{"jwe_of_jws_outbound_accept_missing_kid", nestedOutbound, acceptMissingKid, codePolicyModeMismatch},
		{"jws_of_jwe_inbound_accept_missing_kid", jwsOfJWEInbound, acceptMissingKid, codePolicyModeMismatch},
		{"jws_of_jwe_outbound_accept_missing_kid", jwsOfJWEOutbound, acceptMissingKid, codePolicyModeMismatch},
		{"jwe_of_jws_inbound_omit_kid", nestedInbound, omitKid, codePolicyModeMismatch},
		{"jwe_of_jws_outbound_omit_kid", nestedOutbound, omitKid, codePolicyModeMismatch},
		{"jws_of_jwe_inbound_omit_kid", jwsOfJWEInbound, omitKid, codePolicyModeMismatch},
		{"jws_of_jwe_outbound_omit_kid", jwsOfJWEOutbound, omitKid, codePolicyModeMismatch},
		{"bare_outbound_accept_missing_kid", bareOutbound, acceptMissingKid, codePolicyDirectionMismatch},
		{"bare_inbound_omit_kid", bareInbound, omitKid, codePolicyDirectionMismatch},
		{"unknown_mode_inbound_accept_missing_kid", bareInbound, func(p *Policy) {
			p.Mode = SealMode(99)
			p.AcceptMissingKid = true
		}, codePolicyModeUnknown},
		{"unknown_mode_outbound_omit_kid", bareOutbound, func(p *Policy) {
			p.Mode = SealMode(99)
			p.OmitKid = true
		}, codePolicyModeUnknown},
		{"bare_inbound_accept_missing_kid_without_decrypt_kid", bareInbound, func(p *Policy) {
			p.AcceptMissingKid = true
			p.DecryptKid = ""
		}, codePolicyIncomplete},
		{"bare_outbound_omit_kid_without_encrypt_kid", bareOutbound, func(p *Policy) {
			p.OmitKid = true
			p.EncryptKid = ""
		}, codePolicyIncomplete},
		{"bare_inbound_accept_missing_kid_is_valid", bareInbound, acceptMissingKid, ""},
		{"bare_outbound_omit_kid_is_valid", bareOutbound, omitKid, ""},
	}
	runPolicyCodeCases(t, tests)
}

// policyCodeCase mutates a fresh base policy; an empty wantCode means Validate accepts it.
type policyCodeCase struct {
	name     string
	base     func() *Policy
	mutate   func(p *Policy)
	wantCode string
}

// runPolicyCodeCases asserts each case through Validate and, when refused, that p's door
// refuses it pre-trust with the same code.
func runPolicyCodeCases(t *testing.T, tests []policyCodeCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.base()
			tt.mutate(p)
			if tt.wantCode == "" {
				require.NoError(t, p.Validate())
				return
			}
			requireJOSEErrorCode(t, p.Validate(), tt.wantCode)

			var doorErr *Error
			require.ErrorAs(t, throughDoor(t, p), &doorErr)
			assert.Equal(t, tt.wantCode, doorErr.Code)
			assert.Zero(t, doorErr.Status)
		})
	}
}

func TestSealModeJWSofJWEMinimalString(t *testing.T) {
	modes := []SealMode{SealModeJWEofJWS, SealModeBareJWE, SealModeJWSofJWE, SealModeJWSofJWEMinimal}
	assert.Equal(t, []SealMode{0, 1, 2, 3}, modes)
	assert.Equal(t, "jws-of-jwe-minimal", SealModeJWSofJWEMinimal.String())
	assert.Equal(t, "unknown", SealMode(99).String())
}

// jwsOfJWEMinimalInbound / jwsOfJWEMinimalOutbound are otherwise-valid minimal JWS-of-JWE
// policies; the outbound one is refused only because the mode is open-only.
func jwsOfJWEMinimalInbound() *Policy {
	p := jwsOfJWEInbound()
	p.Mode = SealModeJWSofJWEMinimal
	p.SigAlg = jose.RS512
	return p
}

func jwsOfJWEMinimalOutbound() *Policy {
	p := jwsOfJWEOutbound()
	p.Mode = SealModeJWSofJWEMinimal
	p.SigAlg = jose.RS512
	return p
}

func TestPolicyValidateJWSofJWEMinimalModeRules(t *testing.T) {
	tests := []policyCodeCase{
		{"inbound_minimal_is_valid", jwsOfJWEMinimalInbound, func(*Policy) {}, ""},
		{"inbound_accept_missing_kid_is_valid", jwsOfJWEMinimalInbound, func(p *Policy) { p.AcceptMissingKid = true }, ""},
		{"inbound_rs256", jwsOfJWEMinimalInbound, func(p *Policy) { p.SigAlg = jose.RS256 }, codeAlgorithmDisallowed},
		{"inbound_ps256", jwsOfJWEMinimalInbound, func(p *Policy) { p.SigAlg = jose.PS256 }, codeAlgorithmDisallowed},
		{"inbound_sig_alg_unset", jwsOfJWEMinimalInbound, func(p *Policy) { p.SigAlg = "" }, codeAlgorithmDisallowed},
		{"inbound_a128gcm", jwsOfJWEMinimalInbound, func(p *Policy) { p.Enc = jose.A128GCM }, codeAlgorithmDisallowed},
		{"inbound_a256cbc_hs512", jwsOfJWEMinimalInbound, func(p *Policy) { p.Enc = jose.A256CBC_HS512 }, codeAlgorithmDisallowed},
		{"inbound_without_decrypt_kid", jwsOfJWEMinimalInbound, func(p *Policy) { p.DecryptKid = "" }, codePolicyIncomplete},
		{"inbound_without_verify_kid", jwsOfJWEMinimalInbound, func(p *Policy) { p.VerifyKid = "" }, codePolicyIncomplete},
		{"inbound_with_sign_kid", jwsOfJWEMinimalInbound, func(p *Policy) { p.SignKid = "our-key" }, codePolicyDirectionMismatch},
		{"inbound_with_encrypt_kid", jwsOfJWEMinimalInbound, func(p *Policy) { p.EncryptKid = "peer-key" }, codePolicyDirectionMismatch},
		{"inbound_typ", jwsOfJWEMinimalInbound, func(p *Policy) { p.Typ = "JOSE" }, codePolicyDirectionMismatch},
		{"inbound_protected_headers", jwsOfJWEMinimalInbound, func(p *Policy) {
			p.ProtectedHeaders = map[string]any{"iss": "acme"}
		}, codePolicyDirectionMismatch},
		{"inbound_iat_millis", jwsOfJWEMinimalInbound, func(p *Policy) { p.IATMillis = true }, codePolicyDirectionMismatch},
		{"inbound_omit_kid", jwsOfJWEMinimalInbound, func(p *Policy) { p.OmitKid = true }, codePolicyModeMismatch},
		{"outbound_is_open_only", jwsOfJWEMinimalOutbound, func(*Policy) {}, codePolicyDirectionMismatch},
	}
	runPolicyCodeCases(t, tests)
}

func TestPolicyValidateRS512StaysOffTheGlobalSigningModes(t *testing.T) {
	for _, base := range []func() *Policy{nestedInbound, nestedOutbound, jwsOfJWEInbound, jwsOfJWEOutbound} {
		p := base()
		p.SigAlg = jose.RS512
		requireJOSEErrorCode(t, p.Validate(), codeAlgorithmDisallowed)
	}
}
