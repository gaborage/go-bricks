package jose

import (
	"errors"
	"fmt"
)

var (
	ErrBodyRequired        = errors.New("jose: request body required")
	ErrUnsupportedMedia    = errors.New("jose: unsupported media type")
	ErrMalformed           = errors.New("jose: malformed compact serialization")
	ErrAlgorithmDisallowed = errors.New("jose: algorithm not in allowlist")
	ErrNoneAlgRejected     = errors.New("jose: alg=none rejected")
	ErrKidMissing          = errors.New("jose: header missing kid")
	ErrCritUnsupported     = errors.New("jose: unrecognized crit header value")
	ErrTypRejected         = errors.New("jose: header typ not allowed")
	ErrKidUnknown          = errors.New("jose: kid not registered")
	ErrDecryptFailed       = errors.New("jose: decryption failed")
	ErrInnerNotJWS         = errors.New("jose: inner payload is not a JWS")
	ErrSignatureInvalid    = errors.New("jose: signature verification failed")
	ErrPlaintextRejected   = errors.New("jose: plaintext request rejected by policy")
	ErrOutboundFailed      = errors.New("jose: outbound seal failed")
	ErrPolicyMismatch      = errors.New("jose: policy mismatch")
	ErrPolicyAsymmetric    = errors.New("jose: bidirectional policy required (request and response must both declare jose tags or neither)")
	ErrTagInvalid          = errors.New("jose: invalid jose struct tag")
	ErrKeyResolution       = errors.New("jose: key resolution failed")
)

// Error is the structured value returned by every jose package operation that fails.
// It carries diagnostic fields (Kid, Alg, Enc) for logging and an HTTP-mapped Code/Status
// for response shaping. Use errors.Is(err, ErrDecryptFailed) for sentinel comparisons.
//
// The Cause field MUST NOT be exposed to peers — it can leak information about which key
// was tried or which library detected the failure. Server middleware logs Cause; only Code
// and the constant-time generic Message reach the wire.
type Error struct {
	Sentinel error
	Code     string
	Status   int
	Message  string
	Kid      string
	Alg      string
	Enc      string
	Cause    error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (cause: %v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Sentinel
}

func (e *Error) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	return errors.Is(e.Sentinel, target)
}
