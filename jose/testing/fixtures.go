package testing

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"testing"

	"github.com/gaborage/go-bricks/jose"
)

// GenerateTestKeyPair returns a freshly-minted 2048-bit RSA key pair suitable for
// JOSE round-trip tests. The smaller key size is chosen for test speed — production
// keystores should use 3072+ bits.
func GenerateTestKeyPair(t testing.TB) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("jositest: failed to generate key pair: %v", err)
	}
	return priv, &priv.PublicKey
}

// NewTestResolver builds a jose.KeyResolver from a flat kid → key map. Values may be
// *rsa.PrivateKey (used for both PrivateKey and PublicKey lookups via the embedded
// public key) or *rsa.PublicKey (used only for PublicKey lookups; PrivateKey lookup
// against a public-only entry returns ErrKidUnknown).
//
// Unknown kids return a *jose.Error wrapping jose.ErrKidUnknown — the same behavior
// as the production KeyStoreResolver — so tests exercise the same error paths as
// production code.
func NewTestResolver(keys map[string]any) jose.KeyResolver {
	r := &mapResolver{
		priv: map[string]*rsa.PrivateKey{},
		pub:  map[string]*rsa.PublicKey{},
	}
	for kid, k := range keys {
		switch v := k.(type) {
		case *rsa.PrivateKey:
			r.priv[kid] = v
			r.pub[kid] = &v.PublicKey
		case *rsa.PublicKey:
			r.pub[kid] = v
		default:
			panic(fmt.Sprintf("jositest: unsupported key type for kid %q: %T", kid, k))
		}
	}
	return r
}

// BidirectionalFixture is the test-scoped state for exercising both ends of a JOSE
// channel — typical for VTS-style integrations where one side is the system under
// test and the other side is replayed by an in-process httptest server. The fixture
// holds two key pairs and the four matching policies (client outbound/inbound + peer
// outbound/inbound) so a test can seal and open in either direction without rebuilding
// the kid namespace.
type BidirectionalFixture struct {
	ClientPrivate *rsa.PrivateKey
	PeerPrivate   *rsa.PrivateKey
	Resolver      jose.KeyResolver

	ClientOutbound *jose.Policy // client signs with client-key, encrypts to peer-key
	ClientInbound  *jose.Policy // client decrypts with client-key, verifies with peer-key
	PeerOutbound   *jose.Policy // peer signs with peer-key, encrypts to client-key
	PeerInbound    *jose.Policy // peer decrypts with peer-key, verifies with client-key
}

// NewBidirectionalFixture returns a fixture with two freshly generated 2048-bit RSA
// pairs and matching client/peer policies. Kid strings are fixed ("client-key" and
// "peer-key") because production VTS-style integrations use matching kid names on
// both ends — header kids on a sealed payload must match the receiver's expected
// kid for ExpectedKid validation to pass.
func NewBidirectionalFixture(t testing.TB) *BidirectionalFixture {
	t.Helper()
	clientPriv, _ := GenerateTestKeyPair(t)
	peerPriv, _ := GenerateTestKeyPair(t)
	resolver := NewTestResolver(map[string]any{
		"client-key": clientPriv,
		"peer-key":   peerPriv,
	})
	mkOut := func(signKid, encKid string) *jose.Policy {
		return &jose.Policy{
			Direction: jose.DirectionOutbound,
			SignKid:   signKid, EncryptKid: encKid,
			SigAlg: jose.DefaultSigAlg, KeyAlg: jose.DefaultKeyAlg,
			Enc: jose.DefaultEnc, Cty: jose.DefaultCty,
		}
	}
	mkIn := func(decKid, verKid string) *jose.Policy {
		return &jose.Policy{
			Direction:  jose.DirectionInbound,
			DecryptKid: decKid, VerifyKid: verKid,
			SigAlg: jose.DefaultSigAlg, KeyAlg: jose.DefaultKeyAlg,
			Enc: jose.DefaultEnc, Cty: jose.DefaultCty,
		}
	}
	return &BidirectionalFixture{
		ClientPrivate:  clientPriv,
		PeerPrivate:    peerPriv,
		Resolver:       resolver,
		ClientOutbound: mkOut("client-key", "peer-key"),
		ClientInbound:  mkIn("client-key", "peer-key"),
		PeerOutbound:   mkOut("peer-key", "client-key"),
		PeerInbound:    mkIn("peer-key", "client-key"),
	}
}

type mapResolver struct {
	priv map[string]*rsa.PrivateKey
	pub  map[string]*rsa.PublicKey
}

func (r *mapResolver) PrivateKey(kid string) (*rsa.PrivateKey, error) {
	if k, ok := r.priv[kid]; ok {
		return k, nil
	}
	return nil, &jose.Error{
		Sentinel: jose.ErrKidUnknown,
		Code:     "JOSE_KID_UNKNOWN",
		Message:  "test resolver: private key not registered",
		Kid:      kid,
	}
}

func (r *mapResolver) PublicKey(kid string) (*rsa.PublicKey, error) {
	if k, ok := r.pub[kid]; ok {
		return k, nil
	}
	return nil, &jose.Error{
		Sentinel: jose.ErrKidUnknown,
		Code:     "JOSE_KID_UNKNOWN",
		Message:  "test resolver: public key not registered",
		Kid:      kid,
	}
}
