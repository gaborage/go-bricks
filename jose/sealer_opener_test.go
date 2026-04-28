package jose

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureResolver is a test-only KeyResolver backed by an in-memory map.
// Production code uses KeyStoreResolver wrapping app.KeyStore.
type fixtureResolver struct {
	priv map[string]*rsa.PrivateKey
	pub  map[string]*rsa.PublicKey
}

func (r *fixtureResolver) PrivateKey(kid string) (*rsa.PrivateKey, error) {
	if k, ok := r.priv[kid]; ok {
		return k, nil
	}
	return nil, &Error{Sentinel: ErrKidUnknown, Code: "JOSE_KID_UNKNOWN", Kid: kid}
}

func (r *fixtureResolver) PublicKey(kid string) (*rsa.PublicKey, error) {
	if k, ok := r.pub[kid]; ok {
		return k, nil
	}
	return nil, &Error{Sentinel: ErrKidUnknown, Code: "JOSE_KID_UNKNOWN", Kid: kid}
}

func generateKeyPair(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return priv, &priv.PublicKey
}

type testFixture struct {
	resolver *fixtureResolver
	inbound  *Policy
	outbound *Policy
}

// newTestFixture sets up a symmetric kid namespace ("our-key" and "peer-key") shared
// across both sides of the channel — production VTS-style integrations also use
// matching kid names on both ends, so the JOSE headers carry the same kid the receiver
// expects. The same test process plays both roles by holding both private keys.
// openErr discards the success-path return values from Open and returns only the error,
// for negative tests that don't care about plaintext / claims / headers.
//
//nolint:dogsled // intentional: helper's purpose is to centralize the discards
func openErr(compact string, p *Policy, r KeyResolver) error {
	_, _, _, err := Open(compact, p, r)
	return err
}

func newTestFixture(t *testing.T) *testFixture {
	t.Helper()
	ourPriv, ourPub := generateKeyPair(t)
	peerPriv, peerPub := generateKeyPair(t)

	r := &fixtureResolver{
		priv: map[string]*rsa.PrivateKey{
			"our-key":  ourPriv,  // for inbound decrypt
			"peer-key": peerPriv, // for outbound sign (we play peer in Seal)
		},
		pub: map[string]*rsa.PublicKey{
			"our-key":  ourPub,  // for outbound encrypt (we play peer encrypting to us)
			"peer-key": peerPub, // for inbound verify
		},
	}
	return &testFixture{
		resolver: r,
		inbound: &Policy{
			Direction:  DirectionInbound,
			DecryptKid: "our-key",
			VerifyKid:  "peer-key",
			SigAlg:     DefaultSigAlg,
			KeyAlg:     DefaultKeyAlg,
			Enc:        DefaultEnc,
			Cty:        DefaultCty,
		},
		outbound: &Policy{
			Direction:  DirectionOutbound,
			SignKid:    "peer-key",
			EncryptKid: "our-key",
			SigAlg:     DefaultSigAlg,
			KeyAlg:     DefaultKeyAlg,
			Enc:        DefaultEnc,
			Cty:        DefaultCty,
		},
	}
}

func TestSealOpenRoundtrip(t *testing.T) {
	f := newTestFixture(t)
	payload := []byte(`{"pan":"4111111111111111","iat":1700000000}`)

	compact, err := Seal(payload, f.outbound, f.resolver)
	require.NoError(t, err)
	assert.NotEmpty(t, compact)

	plaintext, claims, hdr, err := Open(compact, f.inbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, payload, plaintext)
	assert.Equal(t, "our-key", hdr.JWE.Kid)
	assert.Equal(t, "peer-key", hdr.JWS.Kid)
	assert.Equal(t, time.Unix(1700000000, 0).UTC(), claims.IssuedAt)
}

func TestOpenTamperedCiphertextFails(t *testing.T) {
	f := newTestFixture(t)
	payload := []byte(`{"pan":"4111111111111111"}`)
	compact, err := Seal(payload, f.outbound, f.resolver)
	require.NoError(t, err)

	tampered := []byte(compact)
	tampered[len(tampered)/2] ^= 0x01

	err = openErr(string(tampered), f.inbound, f.resolver)
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.True(t,
		jerr.Code == "JOSE_DECRYPT_FAILED" || jerr.Code == "JOSE_MALFORMED",
		"unexpected error code: %s", jerr.Code,
	)
	assert.Contains(t, []int{400, 401}, jerr.Status)
}

func TestOpenWrongVerifyKeyFails(t *testing.T) {
	f := newTestFixture(t)
	payload := []byte(`{"pan":"4111111111111111"}`)
	compact, err := Seal(payload, f.outbound, f.resolver)
	require.NoError(t, err)

	// Swap the verify public key for an unrelated one so signature verification fails
	// (without changing the kid header, which still matches VerifyKid policy).
	_, otherPub := generateKeyPair(t)
	f.resolver.pub["peer-key"] = otherPub

	err = openErr(compact, f.inbound, f.resolver)
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_SIGNATURE_INVALID", jerr.Code)
	assert.Equal(t, 401, jerr.Status)
}

func TestSealRequiresOutboundPolicy(t *testing.T) {
	f := newTestFixture(t)
	_, err := Seal([]byte("x"), f.inbound, f.resolver)
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_POLICY_DIRECTION_MISMATCH", jerr.Code)
}

func TestOpenRequiresInboundPolicy(t *testing.T) {
	f := newTestFixture(t)
	err := openErr("x", f.outbound, f.resolver)
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_POLICY_DIRECTION_MISMATCH", jerr.Code)
}

func TestOpenMalformedInputFails(t *testing.T) {
	f := newTestFixture(t)
	err := openErr("not.a.compact.jose", f.inbound, f.resolver)
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_MALFORMED", jerr.Code)
	assert.Equal(t, 400, jerr.Status)
}

func TestOpenExtractsClaims(t *testing.T) {
	f := newTestFixture(t)
	now := time.Now().Unix()
	payload, err := json.Marshal(map[string]any{
		"iss": "visa",
		"sub": "merchant-1",
		"aud": []string{"acceptor-1", "acceptor-2"},
		"jti": "txn-12345",
		"iat": now,
		"exp": now + 300,
	})
	require.NoError(t, err)

	compact, err := Seal(payload, f.outbound, f.resolver)
	require.NoError(t, err)

	_, claims, _, err := Open(compact, f.inbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, "visa", claims.Issuer)
	assert.Equal(t, "merchant-1", claims.Subject)
	assert.Equal(t, []string{"acceptor-1", "acceptor-2"}, claims.Audience)
	assert.Equal(t, "txn-12345", claims.JTI)
	assert.Equal(t, time.Unix(now, 0).UTC(), claims.IssuedAt)
	assert.Equal(t, time.Unix(now+300, 0).UTC(), claims.ExpiresAt)
}

func TestSealRejectsNilResolver(t *testing.T) {
	f := newTestFixture(t)
	_, err := Seal([]byte("x"), f.outbound, nil)
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_KEYSTORE_UNAVAILABLE", jerr.Code)
}

func TestOpenRejectsNilResolver(t *testing.T) {
	f := newTestFixture(t)
	err := openErr("not-used", f.inbound, nil)
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_KEYSTORE_UNAVAILABLE", jerr.Code)
}

func TestSealMissingKid(t *testing.T) {
	f := newTestFixture(t)
	bad := *f.outbound
	bad.SignKid = "does-not-exist"

	_, err := Seal([]byte("x"), &bad, f.resolver)
	require.Error(t, err)
	var jerr *Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "JOSE_KID_UNKNOWN", jerr.Code)
}
