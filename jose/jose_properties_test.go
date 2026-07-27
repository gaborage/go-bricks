package jose_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

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
	sealed := josetest.SealForTest(t, payload, fx.ClientOutbound, fx.Resolver)

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

// Semantic complement to the raw-character property above: flipping a bit of
// a segment's DECODED bytes (then re-encoding canonically) always changes the
// cryptographic input — header is AAD, key/IV/ciphertext/tag feed OAEP or
// GCM — so Open must always fail.
func TestOpenSemanticTamperAlwaysFailsProperty(t *testing.T) {
	fx := josetest.NewBidirectionalFixture(t)
	payload := []byte(`{"amount":"100.00","currency":"USD"}`)
	sealed := josetest.SealForTest(t, payload, fx.ClientOutbound, fx.Resolver)
	parts := strings.Split(sealed, ".")

	rapid.Check(t, func(rt *rapid.T) {
		seg := rapid.IntRange(0, len(parts)-1).Draw(rt, "seg")
		raw, err := base64.RawURLEncoding.DecodeString(parts[seg])
		if err != nil || len(raw) == 0 {
			rt.Fatalf("segment %d not decodable base64url (%v) — fixture broke", seg, err)
		}
		idx := rapid.IntRange(0, len(raw)-1).Draw(rt, "idx")
		bit := byte(1) << rapid.IntRange(0, 7).Draw(rt, "bit")

		mutated := append([]byte(nil), raw...)
		mutated[idx] ^= bit
		tamperedParts := append([]string(nil), parts...)
		tamperedParts[seg] = base64.RawURLEncoding.EncodeToString(mutated)

		if _, _, _, err := jose.Open(strings.Join(tamperedParts, "."), fx.PeerInbound, fx.Resolver); err == nil {
			rt.Fatalf("semantic tamper accepted: segment %d, byte %d, bit flip %#x", seg, idx, bit)
		}
	})
}
