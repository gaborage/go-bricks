package cryptoadapter

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedKey is generated once: these tests exercise header handling, not key material.
var sharedKey = sync.OnceValues(func() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
})

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := sharedKey()
	require.NoError(t, err)
	return k
}

// signWithExtra and encryptWithExtra produce the two layers' headers for the same Extra map
// so every accessor case runs against BOTH JWS and JWE.
func signWithExtra(t *testing.T, extra map[string]any) Header {
	t.Helper()
	key := testKey(t)
	compact, err := Sign([]byte(`{}`), key, &SignOptions{Kid: "k", SigAlg: jose.RS256, Extra: extra})
	require.NoError(t, err)
	_, hdr, err := Verify(compact, &key.PublicKey, &VerifyOptions{AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256}})
	require.NoError(t, err)
	return hdr
}

func encryptWithExtra(t *testing.T, extra map[string]any) Header {
	t.Helper()
	key := testKey(t)
	compact, err := Encrypt([]byte(`{}`), &key.PublicKey, &EncryptOptions{
		Kid: "k", KeyAlg: jose.RSA_OAEP_256, Enc: jose.A256GCM, Extra: extra,
	})
	require.NoError(t, err)
	_, hdr, err := Decrypt(compact, key, &DecryptOptions{
		AllowedKeyAlgs:    []jose.KeyAlgorithm{jose.RSA_OAEP_256},
		AllowedContentEnc: []jose.ContentEncryption{jose.A256GCM},
	})
	require.NoError(t, err)
	return hdr
}

var layers = []struct {
	name  string
	build func(*testing.T, map[string]any) Header
}{
	{"jws", signWithExtra},
	{"jwe", encryptWithExtra},
}

func TestExtraRoundTripString(t *testing.T) {
	for _, l := range layers {
		t.Run(l.name, func(t *testing.T) {
			hdr := l.build(t, map[string]any{"etyp": "order.created"})
			s, ok := hdr.ExtraString("etyp")
			assert.True(t, ok)
			assert.Equal(t, "order.created", s)
			_, ok = hdr.ExtraString("missing")
			assert.False(t, ok)
			_, err := hdr.ExtraInt64("etyp")
			assert.ErrorIs(t, err, ErrExtraMalformed)
		})
	}
}

func TestExtraRoundTripInt64(t *testing.T) {
	const large = int64(1700000000)
	for _, l := range layers {
		t.Run(l.name, func(t *testing.T) {
			hdr := l.build(t, map[string]any{"iat": large, "frac": 1.5, "str": "7", "huge": 1e19})
			// go-jose hands numbers back as float64; integrality must survive.
			_, isFloat := hdr.Extra["iat"].(float64)
			assert.True(t, isFloat, "fixture must exercise the float64 path")

			got, err := hdr.ExtraInt64("iat")
			require.NoError(t, err)
			assert.Equal(t, large, got)

			_, err = hdr.ExtraInt64("frac")
			assert.ErrorIs(t, err, ErrExtraMalformed)
			_, err = hdr.ExtraInt64("str")
			assert.ErrorIs(t, err, ErrExtraMalformed)
			_, err = hdr.ExtraInt64("huge")
			assert.ErrorIs(t, err, ErrExtraMalformed)
			_, err = hdr.ExtraInt64("absent")
			assert.ErrorIs(t, err, ErrExtraAbsent)
			assert.NotErrorIs(t, err, ErrExtraMalformed)
		})
	}
}

func TestExtraRoundTripStringSlice(t *testing.T) {
	for _, l := range layers {
		t.Run(l.name, func(t *testing.T) {
			hdr := l.build(t, map[string]any{"sp": []string{"subject", "card"}, "mixed": []any{"a", 1}, "scalar": "x"})
			_, isAny := hdr.Extra["sp"].([]any)
			assert.True(t, isAny, "fixture must exercise the []any path")

			got, err := hdr.ExtraStringSlice("sp")
			require.NoError(t, err)
			assert.Equal(t, []string{"subject", "card"}, got)

			_, err = hdr.ExtraStringSlice("mixed")
			assert.ErrorIs(t, err, ErrExtraMalformed)
			_, err = hdr.ExtraStringSlice("scalar")
			assert.ErrorIs(t, err, ErrExtraMalformed)
			_, err = hdr.ExtraStringSlice("absent")
			assert.ErrorIs(t, err, ErrExtraAbsent)
		})
	}
}

func TestExtraAbsentLeavesHeaderExtraNil(t *testing.T) {
	for _, l := range layers {
		t.Run(l.name, func(t *testing.T) {
			hdr := l.build(t, nil)
			assert.Nil(t, hdr.Extra)
			assert.Equal(t, "k", hdr.Kid)
		})
	}
}

func TestExtraOwnedParamsNeverLeakIntoExtra(t *testing.T) {
	key := testKey(t)
	compact, err := Sign([]byte(`{}`), key, &SignOptions{Kid: "k", SigAlg: jose.RS256, Cty: "application/json", Typ: "vnd.test.v1+json", Extra: map[string]any{"x": "y"}})
	require.NoError(t, err)
	_, hdr, err := Verify(compact, &key.PublicKey, &VerifyOptions{AllowedSigAlgs: []jose.SignatureAlgorithm{jose.RS256}})
	require.NoError(t, err)
	assert.Equal(t, "application/json", hdr.Cty)
	assert.Equal(t, "vnd.test.v1+json", hdr.Typ)
	assert.Equal(t, map[string]any{"x": "y"}, hdr.Extra)
}

func TestExtraCollisionRejected(t *testing.T) {
	key := testKey(t)
	names := []string{"alg", "enc", "kid", "cty", "typ"}
	for reserved := range reservedParams {
		names = append(names, reserved)
	}
	for _, owned := range names {
		t.Run(owned, func(t *testing.T) {
			extra := map[string]any{owned: "evil"}
			_, err := Sign([]byte(`{}`), key, &SignOptions{Kid: "k", SigAlg: jose.RS256, Extra: extra})
			assert.ErrorIs(t, err, ErrExtraCollision)
			_, err = Encrypt([]byte(`{}`), &key.PublicKey, &EncryptOptions{Kid: "k", KeyAlg: jose.RSA_OAEP_256, Enc: jose.A256GCM, Extra: extra})
			assert.ErrorIs(t, err, ErrExtraCollision)
		})
	}
}

func TestPeekProtectedHeaderReturnsHeaderWithoutKey(t *testing.T) {
	key := testKey(t)
	compact, err := Sign([]byte(`{"a":1}`), key, &SignOptions{
		Kid: "sign-1", SigAlg: jose.PS256, Cty: "application/json", Typ: "vnd.test.v1+json",
		Extra: map[string]any{"jti": "abc", "iat": int64(1700000000), "sp": []string{"subject"}},
	})
	require.NoError(t, err)

	hdr, err := PeekProtectedHeader(compact)
	require.NoError(t, err)
	assert.Equal(t, "sign-1", hdr.Kid)
	assert.Equal(t, "PS256", hdr.Alg)
	assert.Equal(t, "application/json", hdr.Cty)
	assert.Equal(t, "vnd.test.v1+json", hdr.Typ)
	assert.Empty(t, hdr.Enc)
	jti, ok := hdr.ExtraString("jti")
	assert.True(t, ok)
	assert.Equal(t, "abc", jti)
	iat, err := hdr.ExtraInt64("iat")
	require.NoError(t, err)
	assert.Equal(t, int64(1700000000), iat)
	sp, err := hdr.ExtraStringSlice("sp")
	require.NoError(t, err)
	assert.Equal(t, []string{"subject"}, sp)
	for _, owned := range []string{"alg", "kid", "cty", "typ"} {
		assert.NotContains(t, hdr.Extra, owned)
	}

	// Peek must agree with what Verify later reads back from the same bytes.
	_, verified, err := Verify(compact, &key.PublicKey, &VerifyOptions{AllowedSigAlgs: []jose.SignatureAlgorithm{jose.PS256}})
	require.NoError(t, err)
	assert.Equal(t, verified, hdr)
}

func TestPeekProtectedHeaderOnJWEReadsEnc(t *testing.T) {
	key := testKey(t)
	compact, err := Encrypt([]byte(`{}`), &key.PublicKey, &EncryptOptions{Kid: "enc-1", KeyAlg: jose.RSA_OAEP_256, Enc: jose.A256GCM})
	require.NoError(t, err)
	hdr, err := PeekProtectedHeader(compact)
	require.NoError(t, err)
	assert.Equal(t, "enc-1", hdr.Kid)
	assert.Equal(t, "RSA-OAEP-256", hdr.Alg)
	assert.Equal(t, "A256GCM", hdr.Enc)
	assert.Nil(t, hdr.Extra)
}

func TestPeekProtectedHeaderRejectsNonCompact(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	cases := map[string]string{
		"json_body":            `{"json":true}`,
		"two_segments":         b64(`{"alg":"PS256"}`) + ".payload",
		"four_segments":        strings.Repeat(b64(`{}`)+".", 3) + b64(`{}`),
		"segment0_not_b64url":  "!!!.payload.sig",
		"segment0_std_b64":     "e+/9.payload.sig",
		"segment0_not_json":    b64(`not json`) + ".payload.sig",
		"segment0_json_array":  b64(`["alg"]`) + ".payload.sig",
		"segment0_json_null":   b64(`null`) + ".payload.sig",
		"segment0_json_string": b64(`"alg"`) + ".payload.sig",
		"empty":                "",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			hdr, err := PeekProtectedHeader(in)
			assert.ErrorIs(t, err, ErrPeekMalformed)
			assert.Equal(t, Header{}, hdr)
		})
	}
}

func TestPeekProtectedHeaderRejectsOversizedSegment0(t *testing.T) {
	// Exactly at the cap decodes; one byte over is rejected before any decode.
	// base64url grows 3 raw bytes to 4; size the raw JSON so the encoded segment lands
	// exactly on the cap.
	rawLen := maxPeekHeaderBytes / 4 * 3
	pad := strings.Repeat("a", rawLen-len(`{"x":""}`))
	inLimit := base64.RawURLEncoding.EncodeToString([]byte(`{"x":"` + pad + `"}`))
	require.Equal(t, maxPeekHeaderBytes, len(inLimit))
	_, err := PeekProtectedHeader(inLimit + ".p.s")
	require.NoError(t, err)

	over := strings.Repeat("A", maxPeekHeaderBytes+1)
	_, err = PeekProtectedHeader(over + ".p.s")
	assert.ErrorIs(t, err, ErrPeekMalformed)
	// Many dots never allocate more than six segments before rejection.
	_, err = PeekProtectedHeader(strings.Repeat(".", 1<<16))
	assert.ErrorIs(t, err, ErrPeekMalformed)
}

func TestPeekProtectedHeaderSurfacesReservedParamsInExtra(t *testing.T) {
	// Reserved names cannot be WRITTEN through Extra, but an opener must still see them.
	seg := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"PS256","kid":"k","crit":["b64"],"b64":false}`))
	hdr, err := PeekProtectedHeader(seg + ".p.s")
	require.NoError(t, err)
	crit, err := hdr.ExtraStringSlice("crit")
	require.NoError(t, err)
	assert.Equal(t, []string{"b64"}, crit)
	assert.Equal(t, false, hdr.Extra["b64"])
}

func TestNilHeaderAccessorsReportAbsent(t *testing.T) {
	var hdr *Header
	_, ok := hdr.ExtraString("x")
	assert.False(t, ok)
	_, err := hdr.ExtraInt64("x")
	assert.ErrorIs(t, err, ErrExtraAbsent)
	_, err = hdr.ExtraStringSlice("x")
	assert.ErrorIs(t, err, ErrExtraAbsent)
}

func TestPeekProtectedHeaderIgnoresNonStringOwnedParams(t *testing.T) {
	// A forged header with the wrong JSON type for an owned param yields the zero string,
	// not a panic; the value is still not copied into Extra.
	seg := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":1,"kid":["k"],"typ":null,"x":2}`))
	hdr, err := PeekProtectedHeader(seg + ".p.s")
	require.NoError(t, err)
	assert.Equal(t, Header{Extra: map[string]any{"x": float64(2)}}, hdr)
}

func TestNilExtraLeavesProtectedHeaderKeysUnchanged(t *testing.T) {
	// HTTP jose never sets Extra/Typ; the protected header must carry exactly the pre-seam
	// param set on both layers.
	key := testKey(t)
	jws, err := Sign([]byte(`{}`), key, &SignOptions{Kid: "k", SigAlg: jose.RS256, Cty: "application/json"})
	require.NoError(t, err)
	jwe, err := Encrypt([]byte(`{}`), &key.PublicKey, &EncryptOptions{Kid: "k", KeyAlg: jose.RSA_OAEP_256, Enc: jose.A256GCM, Cty: "application/json"})
	require.NoError(t, err)

	keysOf := func(compact string) []string {
		raw, err := base64.RawURLEncoding.DecodeString(strings.SplitN(compact, ".", 2)[0])
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	assert.Equal(t, []string{"alg", "cty", "kid"}, keysOf(jws))
	assert.Equal(t, []string{"alg", "cty", "enc", "kid"}, keysOf(jwe))
}
