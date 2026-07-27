package jose_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/gaborage/go-bricks/jose"
	josetest "github.com/gaborage/go-bricks/jose/testing"
)

func TestSealOpenRoundTripProperty(t *testing.T) {
	fx := josetest.NewBidirectionalFixture(t) // keygen once; iterations reuse
	rapid.Check(t, func(rt *rapid.T) {
		payload := rapid.SliceOfN(rapid.Byte(), 1, 4096).Draw(rt, "payload")
		sealed, err := jose.Seal(payload, fx.ClientOutbound, fx.Resolver)
		if err != nil {
			rt.Fatalf("Seal: %v", err)
		}
		plain, _, _, err := jose.Open(sealed, fx.PeerInbound, fx.Resolver)
		if err != nil {
			rt.Fatalf("Open: %v", err)
		}
		if !bytes.Equal(plain, payload) {
			rt.Fatalf("round-trip mismatch: %d bytes in, %d out", len(payload), len(plain))
		}
	})
}

// The security invariant is NOT "any tamper errors" — a base64 trailing-bit
// swap can decode identically. It is: Open never succeeds with plaintext
// different from the original.
func TestOpenTamperNeverAltersPayloadProperty(t *testing.T) {
	fx := josetest.NewBidirectionalFixture(t)
	payload := []byte(`{"amount":"100.00","currency":"USD"}`)
	sealed, err := jose.Seal(payload, fx.ClientOutbound, fx.Resolver)
	require.NoError(t, err)

	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	rapid.Check(t, func(rt *rapid.T) {
		pos := rapid.IntRange(0, len(sealed)-1).Draw(rt, "pos")
		repl := alphabet[rapid.IntRange(0, len(alphabet)-1).Draw(rt, "chr")]
		if sealed[pos] == repl {
			return
		}
		tampered := []byte(sealed)
		tampered[pos] = repl
		plain, _, _, err := jose.Open(string(tampered), fx.PeerInbound, fx.Resolver)
		if err == nil && !bytes.Equal(plain, payload) {
			rt.Fatalf("tampered token at pos %d opened with ALTERED plaintext", pos)
		}
	})
}
