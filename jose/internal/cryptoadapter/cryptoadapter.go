// Package cryptoadapter wraps go-jose/v4 with strict allowlist enforcement and a
// constant-time generic error surface. All upstream-library specifics (header types,
// option structs) are kept inside this package so a future library swap touches only one file.
package cryptoadapter

import (
	"crypto/rsa"
	"errors"

	jose "github.com/go-jose/go-jose/v4"
)

// Sentinel errors for the adapter layer; the parent jose package wraps these in
// *jose.Error with full diagnostic context.
var (
	ErrParseEncrypted    = errors.New("cryptoadapter: parse encrypted failed")
	ErrParseSigned       = errors.New("cryptoadapter: parse signed failed")
	ErrKidMissing        = errors.New("cryptoadapter: header missing kid")
	ErrKidMismatch       = errors.New("cryptoadapter: header kid does not match expected")
	ErrTypRejected       = errors.New("cryptoadapter: header typ not in allowlist")
	ErrDecryptFailed     = errors.New("cryptoadapter: decrypt failed")
	ErrVerifyFailed      = errors.New("cryptoadapter: signature verification failed")
	ErrSignFailed        = errors.New("cryptoadapter: sign failed")
	ErrEncryptFailed     = errors.New("cryptoadapter: encrypt failed")
	ErrEncrypterCreation = errors.New("cryptoadapter: encrypter creation failed")
	ErrSignerCreation    = errors.New("cryptoadapter: signer creation failed")
)

// Header contains the fields we extract from a parsed JOSE object for diagnostic
// logging. Never includes plaintext.
type Header struct {
	Kid string
	Alg string
	Enc string
	Typ string
}

// DecryptOptions controls strict header validation during JWE decrypt.
type DecryptOptions struct {
	ExpectedKid       string
	AllowedKeyAlgs    []jose.KeyAlgorithm
	AllowedContentEnc []jose.ContentEncryption
	AllowedTyps       []string // empty = no typ enforcement
}

// Decrypt parses a compact JWE, validates its protected header against the allowlists,
// and decrypts using the supplied private key.
func Decrypt(compact string, key *rsa.PrivateKey, opts *DecryptOptions) ([]byte, Header, error) {
	jwe, err := jose.ParseEncrypted(compact, opts.AllowedKeyAlgs, opts.AllowedContentEnc)
	if err != nil {
		return nil, Header{}, ErrParseEncrypted
	}

	hdr := Header{
		Kid: jwe.Header.KeyID,
		Alg: jwe.Header.Algorithm,
		Enc: extractStringExtra(jwe.Header.ExtraHeaders, "enc"),
		Typ: extractStringExtra(jwe.Header.ExtraHeaders, jose.HeaderType),
	}

	if hdr.Kid == "" {
		return nil, hdr, ErrKidMissing
	}
	if opts.ExpectedKid != "" && hdr.Kid != opts.ExpectedKid {
		return nil, hdr, ErrKidMismatch
	}
	if len(opts.AllowedTyps) > 0 && !contains(opts.AllowedTyps, hdr.Typ) {
		return nil, hdr, ErrTypRejected
	}

	plaintext, err := jwe.Decrypt(key)
	if err != nil {
		return nil, hdr, ErrDecryptFailed
	}
	return plaintext, hdr, nil
}

// VerifyOptions controls strict header validation during JWS verify.
type VerifyOptions struct {
	ExpectedKid    string
	AllowedSigAlgs []jose.SignatureAlgorithm
	AllowedTyps    []string
}

// Verify parses a compact JWS, validates the protected header, and verifies the signature
// using the supplied public key. Reads the Protected header (signed) rather than the
// merged Header (which mixes unsigned values).
func Verify(compact string, key *rsa.PublicKey, opts *VerifyOptions) ([]byte, Header, error) {
	jws, err := jose.ParseSigned(compact, opts.AllowedSigAlgs)
	if err != nil {
		return nil, Header{}, ErrParseSigned
	}
	if len(jws.Signatures) == 0 {
		return nil, Header{}, ErrParseSigned
	}

	sig := jws.Signatures[0]
	hdr := Header{
		Kid: sig.Protected.KeyID,
		Alg: sig.Protected.Algorithm,
		Typ: extractStringExtra(sig.Protected.ExtraHeaders, jose.HeaderType),
	}

	if hdr.Kid == "" {
		return nil, hdr, ErrKidMissing
	}
	if opts.ExpectedKid != "" && hdr.Kid != opts.ExpectedKid {
		return nil, hdr, ErrKidMismatch
	}
	if len(opts.AllowedTyps) > 0 && !contains(opts.AllowedTyps, hdr.Typ) {
		return nil, hdr, ErrTypRejected
	}

	payload, err := jws.Verify(key)
	if err != nil {
		return nil, hdr, ErrVerifyFailed
	}
	return payload, hdr, nil
}

// SignOptions controls JWS production.
type SignOptions struct {
	Kid    string
	SigAlg jose.SignatureAlgorithm
	Cty    string
}

// Sign produces a compact JWS over payload using the private key.
func Sign(payload []byte, key *rsa.PrivateKey, opts *SignOptions) (string, error) {
	signerOpts := (&jose.SignerOptions{}).
		WithHeader(jose.HeaderKey("kid"), opts.Kid)
	if opts.Cty != "" {
		signerOpts = signerOpts.WithContentType(jose.ContentType(opts.Cty))
	}
	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: opts.SigAlg,
		Key:       key,
	}, signerOpts)
	if err != nil {
		return "", ErrSignerCreation
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		return "", ErrSignFailed
	}
	compact, err := obj.CompactSerialize()
	if err != nil {
		return "", ErrSignFailed
	}
	return compact, nil
}

// EncryptOptions controls JWE production.
type EncryptOptions struct {
	Kid    string
	KeyAlg jose.KeyAlgorithm
	Enc    jose.ContentEncryption
	Cty    string
}

// Encrypt produces a compact JWE over payload using the public key.
func Encrypt(payload []byte, key *rsa.PublicKey, opts *EncryptOptions) (string, error) {
	encrypterOpts := (&jose.EncrypterOptions{}).
		WithHeader(jose.HeaderKey("kid"), opts.Kid)
	if opts.Cty != "" {
		encrypterOpts = encrypterOpts.WithContentType(jose.ContentType(opts.Cty))
	}
	encrypter, err := jose.NewEncrypter(opts.Enc, jose.Recipient{
		Algorithm: opts.KeyAlg,
		Key:       key,
		KeyID:     opts.Kid,
	}, encrypterOpts)
	if err != nil {
		return "", ErrEncrypterCreation
	}
	obj, err := encrypter.Encrypt(payload)
	if err != nil {
		return "", ErrEncryptFailed
	}
	compact, err := obj.CompactSerialize()
	if err != nil {
		return "", ErrEncryptFailed
	}
	return compact, nil
}

func extractStringExtra(extras map[jose.HeaderKey]interface{}, key jose.HeaderKey) string {
	if extras == nil {
		return ""
	}
	v, ok := extras[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
