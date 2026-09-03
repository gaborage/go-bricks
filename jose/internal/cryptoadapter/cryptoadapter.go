// Package cryptoadapter wraps go-jose/v4's parsing/signing option structs and header
// extraction with strict allowlist enforcement and a constant-time generic error surface.
// Keeping wire-format specifics (header types, option structs) inside this package limits —
// but does not eliminate — a future library swap's blast radius; algorithm-identifier types
// (jose.SignatureAlgorithm, jose.KeyAlgorithm, jose.ContentEncryption) are still referenced
// directly from the parent jose package.
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
	Cty string
	Typ string
	// Extra holds every protected-header param the adapter does not own (alg, enc, kid,
	// cty, typ are excluded). Values keep go-jose's decoded shapes: numbers are float64,
	// arrays are []any — use the typed accessors. From PeekProtectedHeader the contents are
	// UNAUTHENTICATED wire bytes until Verify succeeds: never log them by value.
	Extra map[string]any
}

// DecryptOptions controls strict header validation during JWE decrypt.
type DecryptOptions struct {
	ExpectedKid       string
	AllowedKeyAlgs    []jose.KeyAlgorithm
	AllowedContentEnc []jose.ContentEncryption
}

// Decrypt parses a compact JWE, validates its protected header against the allowlists,
// and decrypts using the supplied private key.
func Decrypt(compact string, key *rsa.PrivateKey, opts *DecryptOptions) ([]byte, Header, error) {
	jwe, err := jose.ParseEncrypted(compact, opts.AllowedKeyAlgs, opts.AllowedContentEnc)
	if err != nil {
		return nil, Header{}, ErrParseEncrypted
	}

	hdr := newHeader(jwe.Header.KeyID, jwe.Header.Algorithm, jwe.Header.ExtraHeaders)

	if hdr.Kid == "" {
		return nil, hdr, ErrKidMissing
	}
	if opts.ExpectedKid != "" && hdr.Kid != opts.ExpectedKid {
		return nil, hdr, ErrKidMismatch
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
	hdr := newHeader(sig.Protected.KeyID, sig.Protected.Algorithm, sig.Protected.ExtraHeaders)

	if hdr.Kid == "" {
		return nil, hdr, ErrKidMissing
	}
	if opts.ExpectedKid != "" && hdr.Kid != opts.ExpectedKid {
		return nil, hdr, ErrKidMismatch
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
	Typ    string
	// Extra is written into the protected header verbatim. Naming an adapter-owned param
	// (alg, enc, kid, cty, typ) or a JOSE-reserved one (crit, b64, zip, jwk, …) is
	// ErrExtraCollision, never an overwrite. The map must not be mutated during the call.
	Extra map[string]any
}

// Sign produces a compact JWS over payload using the private key.
func Sign(payload []byte, key *rsa.PrivateKey, opts *SignOptions) (string, error) {
	if err := checkExtra(opts.Extra); err != nil {
		return "", err
	}
	signerOpts := (&jose.SignerOptions{}).
		WithHeader(jose.HeaderKey("kid"), opts.Kid)
	if opts.Cty != "" {
		signerOpts = signerOpts.WithContentType(jose.ContentType(opts.Cty))
	}
	if opts.Typ != "" {
		signerOpts = signerOpts.WithType(jose.ContentType(opts.Typ))
	}
	for k, v := range opts.Extra {
		signerOpts = signerOpts.WithHeader(jose.HeaderKey(k), v)
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
	Typ    string
	// Extra is written into the protected header verbatim; same collision rule as SignOptions.
	Extra map[string]any
}

// Encrypt produces a compact JWE over payload using the public key.
func Encrypt(payload []byte, key *rsa.PublicKey, opts *EncryptOptions) (string, error) {
	if err := checkExtra(opts.Extra); err != nil {
		return "", err
	}
	encrypterOpts := (&jose.EncrypterOptions{}).
		WithHeader(jose.HeaderKey("kid"), opts.Kid)
	if opts.Cty != "" {
		encrypterOpts = encrypterOpts.WithContentType(jose.ContentType(opts.Cty))
	}
	if opts.Typ != "" {
		encrypterOpts = encrypterOpts.WithType(jose.ContentType(opts.Typ))
	}
	for k, v := range opts.Extra {
		encrypterOpts = encrypterOpts.WithHeader(jose.HeaderKey(k), v)
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
