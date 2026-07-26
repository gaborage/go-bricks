package httpclient

// Test vectors derived from github.com/mastercard/oauth1-signer-go (MIT),
// the vendor reference implementation for Mastercard OAuth 1.0a signing.

import (
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose"
)

// jose.KeyResolver satisfies PrivateKeyResolver structurally (documented in
// wiki/httpclient.md); this pins that claim at compile time. app.KeyStore
// cannot be asserted the same way — app -> server -> httpclient is an import
// cycle.
var _ PrivateKeyResolver = jose.KeyResolver(nil)

func TestOAuth1BodyHashVectors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{
			name:    "nil_payload",
			payload: nil,
			want:    "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=",
		},
		{
			name:    "utf8_multibyte_payload",
			payload: []byte("{\"foõ\":\"bar\"}"),
			want:    "+Z+PWW2TJDnPvRcTgol+nKO3LT7xm8smnsg+//XMIyI=",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := oauth1BodyHash(tc.payload, crypto.SHA256)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestOAuth1ParamStringVectors(t *testing.T) {
	t.Run("nominal_with_query_and_oauth_params", func(t *testing.T) {
		queryParams := map[string][]string{
			"b5":   {"%3D%253D"},
			"a3":   {"a", "2%20q"},
			"c%40": {""},
			"a2":   {"r%20b"},
			"c2":   {""},
		}
		oauthParams := map[string]string{
			oauthConsumerKeyParam:     "9djdj82h48djs9d2",
			"oauth_token":             "kkk9d7dh3k39sjv7",
			oauthSignatureMethodParam: "HMAC-SHA1",
			oauthTimestampParam:       "137131201",
			oauthNonceParam:           "7d8f3e4a",
		}
		want := "a2=r%20b&a3=2%20q&a3=a&b5=%3D%253D&c%40=&c2=&oauth_consumer_key=9djdj82h48djs9d2&" +
			"oauth_nonce=7d8f3e4a&oauth_signature_method=HMAC-SHA1&oauth_timestamp=137131201&" +
			"oauth_token=kkk9d7dh3k39sjv7"

		got := oauth1ParamString(queryParams, oauthParams)
		assert.Equal(t, want, got)
	})

	t.Run("byte_order_sorting", func(t *testing.T) {
		queryParams := map[string][]string{
			"b": {"b"},
			"A": {"a", "A"},
			"B": {"B"},
			"a": {"A", "a"},
			"0": {"0"},
		}
		want := "0=0&A=A&A=a&B=B&a=A&a=a&b=b"

		got := oauth1ParamString(queryParams, map[string]string{})
		assert.Equal(t, want, got)
	})
}

func TestOAuth1ExtractQueryParamsVectors(t *testing.T) {
	t.Run("bare_and_repeated_keys_no_escapes", func(t *testing.T) {
		u, err := url.Parse("https://sandbox.api.mastercard.com/audiences/v1/getcountries?offset=0&offset=1&length=10&empty&odd=")
		require.NoError(t, err)

		got, err := oauth1ExtractQueryParams(u)
		require.NoError(t, err)
		require.Len(t, got, 4)
		assert.Equal(t, []string{"0", "1"}, got["offset"])
		assert.Equal(t, []string{"10"}, got["length"])
		assert.Equal(t, []string{""}, got["empty"])
		assert.Equal(t, []string{""}, got["odd"])
	})

	t.Run("raw_query_contains_escapes_reencodes", func(t *testing.T) {
		u, err := url.Parse("https://example.com/request?b5=%3D%253D&a3=a&c%40=&a2=r%20b")
		require.NoError(t, err)

		got, err := oauth1ExtractQueryParams(u)
		require.NoError(t, err)
		require.Len(t, got, 4)
		assert.Equal(t, []string{"%3D%253D"}, got["b5"])
		assert.Equal(t, []string{"a"}, got["a3"])
		assert.Equal(t, []string{""}, got["c%40"])
		assert.Equal(t, []string{"r%20b"}, got["a2"])
	})

	// '+' decodes to a space under url.QueryUnescape, so it must trip needsEncode.
	t.Run("no_escapes_values_stay_decoded", func(t *testing.T) {
		u, err := url.Parse("https://example.com/request?colon=:&comma=,")
		require.NoError(t, err)

		got, err := oauth1ExtractQueryParams(u)
		require.NoError(t, err)
		assert.Equal(t, []string{":"}, got["colon"])
		assert.Equal(t, []string{","}, got["comma"])
	})

	t.Run("plus_is_an_escape_and_forces_encoding", func(t *testing.T) {
		u, err := url.Parse("https://example.com/request?plus=+&q=a+b")
		require.NoError(t, err)

		got, err := oauth1ExtractQueryParams(u)
		require.NoError(t, err)
		assert.Equal(t, []string{"%20"}, got["plus"])
		assert.Equal(t, []string{"a%20b"}, got["q"])
	})

	// needsEncode is a WHOLE-QUERY flag, not per-parameter: "a" carries an escape
	// (%3A) but "b" is already literal. url.QueryUnescape("a=x%3Ay&b=x:y") decodes
	// to "a=x:y&b=x:y", which differs from RawQuery, so needsEncode trips for the
	// ENTIRE query — both values get re-encoded to "x%3Ay", even though "b" was
	// never escaped on the wire. This is still RFC-correct: a verifier reading
	// b=x:y off the wire decodes it to x:y and re-encodes to x%3Ay too, landing on
	// the same string — but it pins that a single escaped parameter changes how
	// every sibling parameter is signed.
	t.Run("mixed_escaped_and_literal_encodes_every_value", func(t *testing.T) {
		u, err := url.Parse("https://example.com/request?a=x%3Ay&b=x:y")
		require.NoError(t, err)

		got, err := oauth1ExtractQueryParams(u)
		require.NoError(t, err)
		assert.Equal(t, []string{"x%3Ay"}, got["a"])
		assert.Equal(t, []string{"x%3Ay"}, got["b"])
	})

	t.Run("malformed_query_fails_closed", func(t *testing.T) {
		u, err := url.Parse("https://example.com/request?a=b;c=d")
		require.NoError(t, err)

		got, err := oauth1ExtractQueryParams(u)
		require.Error(t, err, "a semicolon separator must fail the whole extraction, not silently drop the segment")
		assert.Nil(t, got)
	})
}

func TestOAuth1BaseURLVectors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no_path_gets_trailing_slash", in: "https://www.example.net:8080", want: "https://www.example.net:8080/"},
		{name: "default_http_port_stripped_and_lowercased", in: "http://EXAMPLE.COM:80/r%20v/X?id=123", want: "http://example.com/r%20v/X"},
		{name: "default_https_port_stripped", in: "https://api.mastercard.com:443/test?query=param", want: "https://api.mastercard.com/test"},
		{name: "non_default_port_kept", in: "https://api.mastercard.com:17443/test?query=param", want: "https://api.mastercard.com:17443/test"},
		{name: "fragment_dropped", in: "https://api.mastercard.com/test?query=param#fragment", want: "https://api.mastercard.com/test"},
		{name: "scheme_and_host_lowercased_path_preserved", in: "HTTPS://API.MASTERCARD.COM/TEST", want: "https://api.mastercard.com/TEST"},
		// IPv6 authorities pin oauth1StripDefaultPort's rightmost-colon search: a
		// naive strings.Index (first colon) would find one of the address's own
		// colons instead of the port separator.
		{name: "ipv6_default_port_stripped", in: "https://[::1]:443/x", want: "https://[::1]/x"},
		{name: "ipv6_non_default_port_kept", in: "https://[::1]:8443/x", want: "https://[::1]:8443/x"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, oauth1BaseURL(u))
		})
	}
}

// TestOAuth1SignatureBaseStringVectors pins oauth1SignatureBaseString ONLY. The base
// URL is passed as the literal string shown — NOT routed through oauth1BaseURL. The
// vendor's own tests do exactly this, which is why these expected strings have no
// trailing slash while TestOAuth1BaseURLVectors' "no_path_gets_trailing_slash" case
// does: oauth1BaseURL correctly normalizes an empty path to "/", so feeding these
// URLs through it would yield a trailing %2F and fail these vectors. The two sets of
// vectors test different functions and are not in conflict.
func TestOAuth1SignatureBaseStringVectors(t *testing.T) {
	t.Run("nominal_with_oauth_body_hash", func(t *testing.T) {
		queryParams := map[string][]string{
			"param2":      {"hello"},
			"first_param": {"value", "othervalue"},
		}
		oauthParams := map[string]string{
			oauthNonceParam:    "randomnonce",
			oauthBodyHashParam: "body/hash",
		}
		paramString := oauth1ParamString(queryParams, oauthParams)
		want := "POST&https%3A%2F%2Fapi.mastercard.com&first_param%3Dothervalue%26first_param%3Dvalue%26" +
			"oauth_body_hash%3Dbody%2Fhash%26oauth_nonce%3Drandomnonce%26param2%3Dhello"

		got := oauth1SignatureBaseString("POST", "https://api.mastercard.com", paramString)
		assert.Equal(t, want, got)
	})

	// These three share one shape: parse URL -> extract -> paramString -> SBS -> compare.
	tests := []struct {
		name   string
		rawURL string
		want   string
	}{
		{
			name:   "query_value_with_encoded_colon",
			rawURL: "https://example.com/?param=token1%3Atoken2",
			want:   "GET&https%3A%2F%2Fexample.com&param%3Dtoken1%253Atoken2",
		},
		{
			name:   "query_value_with_literal_colon",
			rawURL: "https://example.com/?param=token1:token2",
			want:   "GET&https%3A%2F%2Fexample.com&param%3Dtoken1%3Atoken2",
		},
		// This is the actual interoperability proof: the expected string was
		// hand-computed from what an RFC 5849 verifier derives from the wire bytes
		// (url.Values.Encode() emits '+' for a space, so this is the common path for
		// any query built the idiomatic way).
		{
			name:   "query_value_with_plus_encoded_space",
			rawURL: "https://example.com/?q=a+b",
			want:   "GET&https%3A%2F%2Fexample.com&q%3Da%2520b",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.rawURL)
			require.NoError(t, err)
			queryParams, err := oauth1ExtractQueryParams(u)
			require.NoError(t, err)
			paramString := oauth1ParamString(queryParams, map[string]string{})

			got := oauth1SignatureBaseString("GET", "https://example.com", paramString)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestOAuth1SignVerifiesWithPublicKey(t *testing.T) {
	key := generateOAuth1TestKey(t)

	const sbs = "POST&https%3A%2F%2Fapi.mastercard.com&oauth_nonce%3Dfixed"

	tests := []struct {
		name   string
		method OAuth1SignatureMethod
		hash   crypto.Hash
	}{
		{name: "rsa_sha256", method: OAuth1RSASHA256, hash: crypto.SHA256},
		{name: "rsa_sha1", method: OAuth1RSASHA1, hash: crypto.SHA1},
		{name: "rsa_sha512", method: OAuth1RSASHA512, hash: crypto.SHA512},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sig, err := oauth1Sign(sbs, key, tc.method)
			require.NoError(t, err)

			raw, err := base64.StdEncoding.DecodeString(sig)
			require.NoError(t, err)

			digest := tc.hash.New()
			digest.Write([]byte(sbs))

			err = rsa.VerifyPKCS1v15(&key.PublicKey, tc.hash, digest.Sum(nil), raw)
			assert.NoError(t, err)
		})
	}

	t.Run("unknown_method_errors", func(t *testing.T) {
		_, err := oauth1Sign(sbs, key, OAuth1SignatureMethod("HMAC-SHA1"))
		assert.Error(t, err)
	})
}

func TestOAuth1NonceShape(t *testing.T) {
	const count = 100
	seen := make(map[string]bool, count)

	for i := 0; i < count; i++ {
		nonce, err := oauth1Nonce()
		require.NoError(t, err)
		require.Len(t, nonce, 16)
		for _, c := range nonce {
			assert.True(t,
				(c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'),
				"nonce %q contains non-alphanumeric character %q", nonce, c)
		}
		assert.False(t, seen[nonce], "nonce %q generated more than once", nonce)
		seen[nonce] = true
	}
}

// TestOAuth1PercentEncode is a direct oracle for oauth1PercentEncode. Without it,
// deleting '-' or '~' from its unreserved-character test leaves the whole suite
// green: no existing vector contains a hyphen or tilde in an encoded position, and
// the transport-level signature tests rebuild the base string with the very helper
// under test (self-consistency, not interoperability). Hyphens are ubiquitous in
// real values (ISO dates, UUIDs), so a regression here would only surface against
// a real partner.
func TestOAuth1PercentEncode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "hyphen_passes_through_unchanged", in: "-", want: "-"},
		{name: "period_passes_through_unchanged", in: ".", want: "."},
		{name: "underscore_passes_through_unchanged", in: "_", want: "_"},
		{name: "tilde_passes_through_unchanged", in: "~", want: "~"},
		{name: "space_encodes_to_percent_20", in: " ", want: "%20"},
		{name: "slash_encodes_to_percent_2F", in: "/", want: "%2F"},
		{name: "colon_encodes_to_percent_3A", in: ":", want: "%3A"},
		{name: "percent_encodes_to_percent_25", in: "%", want: "%25"},
		{name: "equals_encodes_to_percent_3D", in: "=", want: "%3D"},
		// UTF-8 multibyte rune encodes byte-wise: 'é' is 0xC3 0xA9 in UTF-8.
		{name: "multibyte_utf8_rune_encodes_byte_wise", in: "é", want: "%C3%A9"},
		{name: "empty_string_stays_empty", in: "", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, oauth1PercentEncode(tc.in))
		})
	}
}
