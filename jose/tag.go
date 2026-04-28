package jose

import (
	"fmt"
	"regexp"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
)

// kidPattern restricts key identifiers to ASCII alphanumerics, underscore, and hyphen.
// Mirrors the validation strictness of database/internal/columns/parser.go to prevent
// any character that could be misinterpreted by header processing or logging sinks.
var kidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

const TagName = "jose"

// known tag keys; anything else is a registration error (typo guard).
var knownTagKeys = map[string]bool{
	"decrypt": true,
	"verify":  true,
	"sign":    true,
	"encrypt": true,
	"sig_alg": true,
	"key_alg": true,
	"enc":     true,
	"cty":     true,
}

// ParseTag parses a `jose:` struct tag value into a Policy with the given direction.
// Direction is supplied by the caller (the scanner knows whether the type is request or
// response from its position in HandlerFunc[T, R]). Returns an *Error wrapping
// ErrTagInvalid on any parse failure; the caller should treat this as a registration
// failure (panic at startup).
func ParseTag(tagValue string, dir Direction) (*Policy, error) {
	policy := &Policy{
		Direction: dir,
		SigAlg:    DefaultSigAlg,
		KeyAlg:    DefaultKeyAlg,
		Enc:       DefaultEnc,
		Cty:       DefaultCty,
	}

	if strings.TrimSpace(tagValue) == "" {
		return nil, &Error{
			Sentinel: ErrTagInvalid,
			Code:     "JOSE_TAG_EMPTY",
			Message:  "jose tag is empty",
		}
	}

	seen := map[string]bool{}
	for _, raw := range strings.Split(tagValue, ",") {
		key, val, err := splitKV(raw)
		if err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, &Error{
				Sentinel: ErrTagInvalid,
				Code:     "JOSE_TAG_DUPLICATE_KEY",
				Message:  fmt.Sprintf("jose tag key %q specified more than once", key),
			}
		}
		seen[key] = true
		if err := applyTagPair(policy, key, val); err != nil {
			return nil, err
		}
	}

	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return policy, nil
}

func splitKV(raw string) (key, val string, err error) {
	kv := strings.SplitN(strings.TrimSpace(raw), "=", 2)
	if len(kv) != 2 {
		return "", "", &Error{
			Sentinel: ErrTagInvalid,
			Code:     "JOSE_TAG_MALFORMED",
			Message:  fmt.Sprintf("expected key=value, got %q", raw),
		}
	}
	key = strings.TrimSpace(kv[0])
	val = strings.TrimSpace(kv[1])
	if !knownTagKeys[key] {
		return "", "", &Error{
			Sentinel: ErrTagInvalid,
			Code:     "JOSE_TAG_UNKNOWN_KEY",
			Message:  fmt.Sprintf("unknown jose tag key %q", key),
		}
	}
	if val == "" {
		return "", "", &Error{
			Sentinel: ErrTagInvalid,
			Code:     "JOSE_TAG_EMPTY_VALUE",
			Message:  fmt.Sprintf("jose tag key %q has empty value", key),
		}
	}
	return key, val, nil
}

func applyTagPair(p *Policy, key, val string) error {
	switch key {
	case "decrypt", "verify", "sign", "encrypt":
		return applyKid(p, key, val)
	case "sig_alg":
		alg := jose.SignatureAlgorithm(val)
		if !IsAllowedSigAlg(alg) {
			return algorithmDisallowed("signature algorithm", val, "")
		}
		p.SigAlg = alg
	case "key_alg":
		alg := jose.KeyAlgorithm(val)
		if !IsAllowedKeyAlg(alg) {
			return algorithmDisallowed("key algorithm", val, "")
		}
		p.KeyAlg = alg
	case "enc":
		enc := jose.ContentEncryption(val)
		if !IsAllowedEnc(enc) {
			return algorithmDisallowed("content encryption", "", val)
		}
		p.Enc = enc
	case "cty":
		p.Cty = val
	}
	return nil
}

func applyKid(p *Policy, key, val string) error {
	if !kidPattern.MatchString(val) {
		return &Error{
			Sentinel: ErrTagInvalid,
			Code:     "JOSE_TAG_KID_INVALID",
			Message:  fmt.Sprintf("kid %q contains disallowed characters", val),
			Kid:      val,
		}
	}
	switch key {
	case "decrypt":
		p.DecryptKid = val
	case "verify":
		p.VerifyKid = val
	case "sign":
		p.SignKid = val
	case "encrypt":
		p.EncryptKid = val
	}
	return nil
}

func algorithmDisallowed(kind, alg, enc string) *Error {
	target := alg
	if target == "" {
		target = enc
	}
	return &Error{
		Sentinel: ErrAlgorithmDisallowed,
		Code:     "JOSE_ALGORITHM_DISALLOWED",
		Message:  fmt.Sprintf("%s %q not in allowlist", kind, target),
		Alg:      alg,
		Enc:      enc,
	}
}
