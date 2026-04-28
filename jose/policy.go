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
// KeyResolver, and cached in the registry — never re-parsed per request.
type Policy struct {
	Direction Direction

	// Inbound (DirectionInbound) — both required.
	DecryptKid string // our private key kid (jose: decrypt=...)
	VerifyKid  string // peer public key kid (jose: verify=...)

	// Outbound (DirectionOutbound) — both required.
	SignKid    string // our private key kid (jose: sign=...)
	EncryptKid string // peer public key kid (jose: encrypt=...)

	// Algorithms — defaults applied by the parser if tag omits them.
	SigAlg jose.SignatureAlgorithm
	KeyAlg jose.KeyAlgorithm
	Enc    jose.ContentEncryption
	Cty    string
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

	if !IsAllowedSigAlg(p.SigAlg) {
		return &Error{
			Sentinel: ErrAlgorithmDisallowed,
			Code:     "JOSE_ALGORITHM_DISALLOWED",
			Message:  "signature algorithm not in allowlist",
			Alg:      string(p.SigAlg),
		}
	}
	if !IsAllowedKeyAlg(p.KeyAlg) {
		return &Error{
			Sentinel: ErrAlgorithmDisallowed,
			Code:     "JOSE_ALGORITHM_DISALLOWED",
			Message:  "key-wrapping algorithm not in allowlist",
			Alg:      string(p.KeyAlg),
		}
	}
	if !IsAllowedEnc(p.Enc) {
		return &Error{
			Sentinel: ErrAlgorithmDisallowed,
			Code:     "JOSE_ALGORITHM_DISALLOWED",
			Message:  "content encryption not in allowlist",
			Enc:      string(p.Enc),
		}
	}

	switch p.Direction {
	case DirectionInbound:
		if p.DecryptKid == "" || p.VerifyKid == "" {
			return &Error{
				Sentinel: ErrPolicyMismatch,
				Code:     "JOSE_POLICY_INCOMPLETE",
				Message:  "inbound policy requires both decrypt and verify kids",
			}
		}
		if p.SignKid != "" || p.EncryptKid != "" {
			return &Error{
				Sentinel: ErrPolicyMismatch,
				Code:     "JOSE_POLICY_DIRECTION_MISMATCH",
				Message:  "inbound policy must not declare sign/encrypt kids",
			}
		}
	case DirectionOutbound:
		if p.SignKid == "" || p.EncryptKid == "" {
			return &Error{
				Sentinel: ErrPolicyMismatch,
				Code:     "JOSE_POLICY_INCOMPLETE",
				Message:  "outbound policy requires both sign and encrypt kids",
			}
		}
		if p.DecryptKid != "" || p.VerifyKid != "" {
			return &Error{
				Sentinel: ErrPolicyMismatch,
				Code:     "JOSE_POLICY_DIRECTION_MISMATCH",
				Message:  "outbound policy must not declare decrypt/verify kids",
			}
		}
	default:
		return &Error{
			Sentinel: ErrPolicyMismatch,
			Code:     "JOSE_POLICY_DIRECTION_UNKNOWN",
			Message:  "unknown direction",
		}
	}

	return nil
}
