package jose

import (
	"slices"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
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
	// SigAlg is unused, and must stay unset, in SealModeBareJWE; every other mode signs and
	// requires it.
	//
	// KeyAlg and Enc are read on BOTH sides in every mode: outbound they are what Seal
	// writes; inbound they are what Open accepts, narrowing the mode's allowlist to
	// exactly the declared value. Validate refuses a value off the mode's allowlist, so
	// declaring one can only narrow. Leaving one unset keeps the mode-wide allowlist on
	// the way in — relevant only to a hand-built policy, since the tag parser and
	// Validate both insist on a value.
	SigAlg jose.SignatureAlgorithm
	KeyAlg jose.KeyAlgorithm
	Enc    jose.ContentEncryption
	// Cty is not written by Seal in SealModeJWSofJWE: that shape's inner JWE carries no cty.
	Cty string

	// Typ is the JWE protected `typ` header written by Seal. SealModeBareJWE and
	// SealModeJWSofJWE (inner JWE) OUTBOUND only; Visa Message Level Encryption expects "JOSE".
	Typ string

	// ProtectedHeaders are copied verbatim into the JWE protected header by Seal.
	// SealModeBareJWE and SealModeJWSofJWE (inner JWE) OUTBOUND only. Naming a param the
	// framework owns (alg, enc, kid, cty, typ) or one JOSE reserves is a validation error,
	// never an overwrite.
	ProtectedHeaders map[string]any

	// IATMillis makes Seal stamp an `iat` protected header holding Unix epoch
	// MILLISECONDS at seal time — the Visa MLE convention, not the seconds-based JWT
	// claim of the same name. SealModeBareJWE and SealModeJWSofJWE (inner JWE) OUTBOUND
	// only. jose never judges its freshness on the way in; that is the caller's policy.
	IATMillis bool
}

// hasInnerJWEHeaderFields reports whether the policy carries any of the inner-JWE header fields
// an outbound Seal writes in bare-JWE and JWS-of-JWE modes.
func (p *Policy) hasInnerJWEHeaderFields() bool {
	return p.Typ != "" || p.ProtectedHeaders != nil || p.IATMillis
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
// keeps the inner-JWE header fields out of a JWE-of-JWS policy so the default posture stays
// byte-identical to what it produced before bare mode existed.
func (p *Policy) validateMode() error {
	switch p.Mode {
	case SealModeJWEofJWS:
		if p.hasInnerJWEHeaderFields() {
			return &Error{
				Sentinel: ErrPolicyMismatch,
				Code:     codePolicyModeMismatch,
				Message:  "typ, protected headers and iat stamping require bare-JWE or JWS-of-JWE mode",
			}
		}
		return nil
	case SealModeBareJWE, SealModeJWSofJWE:
		return p.validateInnerJWEHeaders()
	default:
		return errUnknownMode(p.Mode)
	}
}

func errUnknownMode(m SealMode) *Error {
	return &Error{
		Sentinel: ErrPolicyMismatch,
		Code:     codePolicyModeUnknown,
		Message:  "unknown seal mode " + m.String(),
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

// validateDirection runs the kid checks, then keeps the seal header fields off an inbound
// policy: nothing would ever read them, and silently ignoring them would let a consumer
// believe a header was enforced on the way in.
func (p *Policy) validateDirection() error {
	if err := p.validateKids(); err != nil {
		return err
	}
	if p.Direction == DirectionInbound && p.hasInnerJWEHeaderFields() {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyDirectionMismatch,
			Message:  "typ, protected headers and iat stamping are outbound-only",
		}
	}
	return nil
}

// validateKids dispatches to the per-mode, per-direction kid checks.
func (p *Policy) validateKids() error {
	if p.Mode == SealModeBareJWE {
		return p.validateBareDirection()
	}
	switch p.Direction {
	case DirectionInbound:
		return p.validateInbound()
	case DirectionOutbound:
		return p.validateOutbound()
	default:
		return errUnknownDirection()
	}
}

func errUnknownDirection() *Error {
	return &Error{
		Sentinel: ErrPolicyMismatch,
		Code:     "JOSE_POLICY_DIRECTION_UNKNOWN",
		Message:  "unknown direction",
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
	// SealModeJWSofJWE encrypts first and signs the resulting compact JWE: a JWS outer
	// (cty: JWE) over the same inner JWE bare mode builds — the Visa Token Service
	// Issuer shape.
	SealModeJWSofJWE
)

func (m SealMode) String() string {
	switch m {
	case SealModeJWEofJWS:
		return "jwe-of-jws"
	case SealModeBareJWE:
		return "bare-jwe"
	case SealModeJWSofJWE:
		return "jws-of-jwe"
	default:
		return "unknown"
	}
}

// validateInnerJWEHeaders checks the protected-header map Seal would write on the inner
// JWE: no param the framework or JOSE itself owns, and no hand-written iat while Seal is
// stamping one.
func (p *Policy) validateInnerJWEHeaders() error {
	if err := cryptoadapter.CheckExtra(p.ProtectedHeaders); err != nil {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyHeaderCollision,
			Message:  "protected header collides with a reserved param",
			Cause:    err,
		}
	}
	if _, ok := p.ProtectedHeaders["iat"]; ok && p.IATMillis {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyHeaderCollision,
			Message:  "protected header iat collides with iat stamping",
		}
	}
	return nil
}

// validateBareDirection requires exactly the one kid its direction uses: there is no inner
// JWS, so any other kid or a signature algorithm signals a policy written for the wrong mode.
func (p *Policy) validateBareDirection() error {
	switch p.Direction {
	case DirectionInbound:
		return p.validateBareKids(p.DecryptKid,
			"inbound bare-JWE policy requires a decrypt kid",
			"inbound bare-JWE policy must declare only a decrypt kid",
			p.VerifyKid, p.SignKid, p.EncryptKid)
	case DirectionOutbound:
		return p.validateBareKids(p.EncryptKid,
			"outbound bare-JWE policy requires an encrypt kid",
			"outbound bare-JWE policy must declare only an encrypt kid",
			p.SignKid, p.VerifyKid, p.DecryptKid)
	default:
		return errUnknownDirection()
	}
}

// validateBareKids checks that required is set and that neither a forbidden kid nor a
// signature algorithm is declared.
func (p *Policy) validateBareKids(required, missingMsg, forbiddenMsg string, forbidden ...string) error {
	if required == "" {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyIncomplete,
			Message:  missingMsg,
		}
	}
	if p.SigAlg != "" || slices.ContainsFunc(forbidden, func(kid string) bool { return kid != "" }) {
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     codePolicyDirectionMismatch,
			Message:  forbiddenMsg,
		}
	}
	return nil
}
