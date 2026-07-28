package jose_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

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

// ttlRecordingStore is a local recording fake for the property below —
// replay_test.go's recordingStore lives in package jose and is not visible
// from this black-box test package.
type ttlRecordingStore struct {
	ttl time.Duration
}

func (s *ttlRecordingStore) GetOrSet(_ context.Context, _ string, value []byte, ttl time.Duration) (storedValue []byte, wasSet bool, err error) {
	s.ttl = ttl
	return value, true, nil
}

// The stored TTL always equals window exactly, whatever exp is. No reachable
// exp changes the TTL: deriving it from exp would forget the jti at exp while
// a caller that allows clock skew still accepts the token until exp+skew, and
// that gap is a replay hole. A zero TTL ("no expiry" at the cache backend) and
// a negative one (rejected outright) are excluded a fortiori by the equality.
func TestCheckJTIReplayTTLStaysInWindowProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		windowSecs := rapid.IntRange(1, 86400).Draw(rt, "window_secs")
		// Offset spans well past and well future, and includes exactly-now.
		offsetSecs := rapid.IntRange(-100000, 100000).Draw(rt, "exp_offset_secs")
		hasExp := rapid.Bool().Draw(rt, "has_exp")

		window := time.Duration(windowSecs) * time.Second
		claims := &jose.Claims{JTI: "jti-value", Issuer: "issuer"}
		if hasExp {
			claims.ExpiresAt = time.Now().Add(time.Duration(offsetSecs) * time.Second)
		}

		store := &ttlRecordingStore{}
		_, err := jose.CheckJTIReplay(context.Background(), store, claims, window)
		if err != nil {
			rt.Fatalf("CheckJTIReplay: %v", err)
		}

		if ttl := store.ttl; ttl != window {
			rt.Fatalf("ttl %v != window %v: exp_offset=%ds has_exp=%v", ttl, window, offsetSecs, hasExp)
		}
	})
}
