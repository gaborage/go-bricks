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

// JOSE struct-tag keys.
const (
	tagKeyDecrypt = "decrypt"
	tagKeyVerify  = "verify"
	tagKeySign    = "sign"
	tagKeyEncrypt = "encrypt"
	tagKeySigAlg  = "sig_alg"
	tagKeyKeyAlg  = "key_alg"
	tagKeyEnc     = "enc"
	tagKeyCty     = "cty"
)

// known tag keys; anything else is a registration error (typo guard).
var knownTagKeys = map[string]bool{
	tagKeyDecrypt: true,
	tagKeyVerify:  true,
	tagKeySign:    true,
	tagKeyEncrypt: true,
	tagKeySigAlg:  true,
	tagKeyKeyAlg:  true,
	tagKeyEnc:     true,
	tagKeyCty:     true,
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
				Code:     codeTagDuplicateKey,
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
	case tagKeyDecrypt, tagKeyVerify, tagKeySign, tagKeyEncrypt:
		return applyKid(p, key, val)
	case tagKeySigAlg:
		alg := jose.SignatureAlgorithm(val)
		if !IsAllowedSigAlg(alg) {
			return algorithmDisallowed("signature algorithm", val, "")
		}
		p.SigAlg = alg
	case tagKeyKeyAlg:
		alg := jose.KeyAlgorithm(val)
		if !IsAllowedKeyAlg(alg) {
			return algorithmDisallowed("key algorithm", val, "")
		}
		p.KeyAlg = alg
	case tagKeyEnc:
		enc := jose.ContentEncryption(val)
		if !IsAllowedEnc(enc) {
			return algorithmDisallowed("content encryption", "", val)
		}
		p.Enc = enc
	case tagKeyCty:
		p.Cty = val
	}
	return nil
}

func applyKid(p *Policy, key, val string) error {
	if !kidPattern.MatchString(val) {
		return &Error{
			Sentinel: ErrTagInvalid,
			Code:     codeTagKidInvalid,
			Message:  fmt.Sprintf("kid %q contains disallowed characters", val),
			Kid:      val,
		}
	}
	switch key {
	case tagKeyDecrypt:
		p.DecryptKid = val
	case tagKeyVerify:
		p.VerifyKid = val
	case tagKeySign:
		p.SignKid = val
	case tagKeyEncrypt:
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
		Code:     codeAlgorithmDisallowed,
		Message:  fmt.Sprintf("%s %q not in allowlist", kind, target),
		Alg:      alg,
		Enc:      enc,
	}
}
