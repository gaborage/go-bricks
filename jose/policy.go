package jose

import (
	jose "github.com/go-jose/go-jose/v4"
)

// Direction indicates which side of the request/response pipeline a Policy applies to.
type Direction int

const (
	DirectionInbound  Direction = iota // request body: decrypt + verify
	DirectionOutbound                  // response body: sign + encrypt
)

func (d Direction) String() string {
	switch d {
	case DirectionInbound:
		return "inbound"
	case DirectionOutbound:
		return "outbound"
	default:
		return "unknown"
	}
}

// Policy captures the JOSE configuration declared by a request or response struct's
// jose: tag. Inbound policies populate DecryptKid/VerifyKid; outbound populate SignKid/EncryptKid.
// SigAlg/KeyAlg/Enc/Cty fall back to package defaults when unset.
//
// A Policy is constructed once at registration time by the scanner, validated against the
// KeyResolver, and stored on the route descriptor — never re-parsed per request.
type Policy struct {
	Direction Direction

	// Inbound (DirectionInbound) — both required.
	DecryptKid string // our private key kid (jose: decrypt=...)
	VerifyKid  string // peer public key kid (jose: verify=...)

	// Outbound (DirectionOutbound) — both required.
	SignKid    string // our private key kid (jose: sign=...)
	EncryptKid string // peer public key kid (jose: encrypt=...)

	// Mode selects the wire shape. The zero value is SealModeJWEofJWS.
	Mode SealMode

	// Algorithms — defaults applied by the parser if tag omits them.
	// SigAlg is unused, and must stay unset, in SealModeBareJWE.
	SigAlg jose.SignatureAlgorithm
	KeyAlg jose.KeyAlgorithm
	Enc    jose.ContentEncryption
	Cty    string

	// Typ is the JWE protected `typ` header. SealModeBareJWE only; Visa Message Level
	// Encryption expects "JOSE".
	Typ string

	// ProtectedHeaders are copied verbatim into the JWE protected header. SealModeBareJWE
	// only. Naming a param the framework owns (alg, enc, kid, cty, typ) or one JOSE
	// reserves is a validation error, never an overwrite.
	ProtectedHeaders map[string]any

	// IATMillis makes Seal stamp an `iat` protected header holding Unix epoch
	// MILLISECONDS at seal time — the Visa MLE convention, not the seconds-based JWT
	// claim of the same name. SealModeBareJWE only. jose never judges its freshness on
	// the way in; that is the caller's policy.
	IATMillis bool
}

// Validate checks the Policy for internal consistency (correct kids set for the direction,
// algorithms in the allowlist). It does NOT resolve kids against a KeyResolver — that
// happens separately at registration time.
func (p *Policy) Validate() error {
	if p == nil {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     "JOSE_POLICY_NIL",
			Message:  "policy is nil",
		}
	}

	if err := p.validateMode(); err != nil {
		return err
	}
	if err := p.validateAlgorithms(); err != nil {
		return err
	}
	return p.validateDirection()
}

// validateMode rejects an unrecognized Mode before any mode-dependent check runs, and
// keeps the bare-only fields out of a JWE-of-JWS policy so the default posture stays
// byte-identical to what it produced before bare mode existed.
func (p *Policy) validateMode() error {
	switch p.Mode {
	case SealModeJWEofJWS:
		if p.Typ != "" || p.ProtectedHeaders != nil || p.IATMillis {
			return &Error{
				Sentinel: ErrPolicyMismatch,
				Code:     codePolicyModeMismatch,
				Message:  "typ, protected headers and iat stamping require bare-JWE mode",
			}
		}
		return nil
	case SealModeBareJWE:
		return nil
	default:
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyModeUnknown,
			Message:  "unknown seal mode",
		}
	}
}

// validateAlgorithms checks SigAlg/KeyAlg/Enc against the allowlists, in that order.
func (p *Policy) validateAlgorithms() error {
	// Bare mode signs nothing: SigAlg must stay unset, which validateDirection enforces.
	if p.Mode != SealModeBareJWE && !IsAllowedSigAlg(p.SigAlg) {
		return &Error{
			Sentinel: ErrAlgorithmDisallowed,
			Code:     codeAlgorithmDisallowed,
			Message:  "signature algorithm not in allowlist",
			Alg:      string(p.SigAlg),
		}
	}
	if !IsAllowedKeyAlg(p.KeyAlg) {
		return &Error{
			Sentinel: ErrAlgorithmDisallowed,
			Code:     codeAlgorithmDisallowed,
			Message:  "key-wrapping algorithm not in allowlist",
			Alg:      string(p.KeyAlg),
		}
	}
	if !IsAllowedEncFor(p.Mode, p.Enc) {
		return &Error{
			Sentinel: ErrAlgorithmDisallowed,
			Code:     codeAlgorithmDisallowed,
			Message:  "content encryption not in allowlist",
			Enc:      string(p.Enc),
		}
	}
	return nil
}

// validateDirection dispatches to the per-direction kid checks.
func (p *Policy) validateDirection() error {
	switch p.Direction {
	case DirectionInbound:
		if p.Mode == SealModeBareJWE {
			return p.validateBareInbound()
		}
		return p.validateInbound()
	case DirectionOutbound:
		if p.Mode == SealModeBareJWE {
			return p.validateBareOutbound()
		}
		return p.validateOutbound()
	default:
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     "JOSE_POLICY_DIRECTION_UNKNOWN",
			Message:  "unknown direction",
		}
	}
}

// validateInbound requires decrypt+verify kids and forbids sign/encrypt kids.
func (p *Policy) validateInbound() error {
	if p.DecryptKid == "" || p.VerifyKid == "" {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyIncomplete,
			Message:  "inbound policy requires both decrypt and verify kids",
		}
	}
	if p.SignKid != "" || p.EncryptKid != "" {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyDirectionMismatch,
			Message:  "inbound policy must not declare sign/encrypt kids",
		}
	}
	return nil
}

// validateOutbound requires sign+encrypt kids and forbids decrypt/verify kids.
func (p *Policy) validateOutbound() error {
	if p.SignKid == "" || p.EncryptKid == "" {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyIncomplete,
			Message:  "outbound policy requires both sign and encrypt kids",
		}
	}
	if p.DecryptKid != "" || p.VerifyKid != "" {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyDirectionMismatch,
			Message:  "outbound policy must not declare decrypt/verify kids",
		}
	}
	return nil
}

// SealMode selects the wire shape a Policy produces and accepts.
type SealMode int

const (
	// SealModeJWEofJWS is the default: sign-then-encrypt outbound, decrypt-then-verify
	// inbound. The zero value, so a Policy that never mentions Mode keeps this posture.
	SealModeJWEofJWS SealMode = iota
	// SealModeBareJWE encrypts the payload directly, with no inner JWS — the shape Visa
	// Message Level Encryption specifies. There is no signature, so the peer's identity
	// must be established out of band (X-Pay-Token, mTLS); jose authenticates nothing
	// about the sender in this mode.
	SealModeBareJWE
)

func (m SealMode) String() string {
	switch m {
	case SealModeJWEofJWS:
		return "jwe-of-jws"
	case SealModeBareJWE:
		return "bare-jwe"
	default:
		return "unknown"
	}
}

// validateBareInbound requires only the decrypt kid: there is no inner JWS to verify, so
// a verify kid or a signature algorithm signals a policy written for the wrong mode.
func (p *Policy) validateBareInbound() error {
	if p.DecryptKid == "" {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyIncomplete,
			Message:  "inbound bare-JWE policy requires a decrypt kid",
		}
	}
	if p.VerifyKid != "" || p.SignKid != "" || p.EncryptKid != "" || p.SigAlg != "" {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyDirectionMismatch,
			Message:  "inbound bare-JWE policy must declare only a decrypt kid",
		}
	}
	return nil
}

// validateBareOutbound requires only the encrypt kid; the mirror of validateBareInbound.
func (p *Policy) validateBareOutbound() error {
	if p.EncryptKid == "" {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyIncomplete,
			Message:  "outbound bare-JWE policy requires an encrypt kid",
		}
	}
	if p.SignKid != "" || p.VerifyKid != "" || p.DecryptKid != "" || p.SigAlg != "" {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyDirectionMismatch,
			Message:  "outbound bare-JWE policy must declare only an encrypt kid",
		}
	}
	return nil
}
