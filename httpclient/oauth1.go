package httpclient

import (
	"crypto"
	crand "crypto/rand"
	"crypto/rsa"
	_ "crypto/sha1" //#nosec G505 -- RSA-SHA1 OAuth1 variant is partner-mandated legacy (#766); default stays RSA-SHA256
	_ "crypto/sha256"
	_ "crypto/sha512"
	"encoding/base64"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// OAuth1SignatureMethod selects the RSA digest for OAuth 1.0a signing.
type OAuth1SignatureMethod string

const (
	// OAuth1RSASHA256 is the default signature method.
	OAuth1RSASHA256 OAuth1SignatureMethod = "RSA-SHA256"
	// OAuth1RSASHA1 is a legacy signature method some partners still require.
	OAuth1RSASHA1 OAuth1SignatureMethod = "RSA-SHA1"
	// OAuth1RSASHA512 is a stronger signature method some partners accept.
	OAuth1RSASHA512 OAuth1SignatureMethod = "RSA-SHA512"
)

// oauth_* parameter names, extracted to constants: the test vectors push
// oauth_nonce past goconst's min-occurrences threshold, and the vendor
// reference (which this implementation mirrors) uses named constants too.
const (
	oauthConsumerKeyParam     = "oauth_consumer_key"
	oauthNonceParam           = "oauth_nonce"
	oauthSignatureParam       = "oauth_signature"
	oauthSignatureMethodParam = "oauth_signature_method"
	oauthTimestampParam       = "oauth_timestamp"
	oauthVersionParam         = "oauth_version"
	oauthBodyHashParam        = "oauth_body_hash"
)

// oauth1Version is the fixed OAuth 1.0a protocol version emitted in every
// generated Authorization header.
const oauth1Version = "1.0"

// PrivateKeyResolver supplies the signing key by name. app.KeyStore and
// jose.KeyResolver both satisfy it structurally.
type PrivateKeyResolver interface {
	PrivateKey(name string) (*rsa.PrivateKey, error)
}

// OAuth1Config configures Mastercard-style OAuth 1.0a request signing.
type OAuth1Config struct {
	// ConsumerKey identifies the caller to the partner (oauth_consumer_key).
	ConsumerKey string
	// KeyName is resolved per request via Keys, so key rotation applies
	// without rebuilding the client.
	KeyName string
	// Keys resolves KeyName to the RSA private key used to sign each request.
	Keys PrivateKeyResolver
	// Method selects the signing digest. The zero value normalizes to
	// OAuth1RSASHA256.
	Method OAuth1SignatureMethod
}

// validate rejects an OAuth1Config that would fail at request time, so
// WithOAuth1 can fail fast at build time instead. Method is checked via
// oauth1HashForMethod — the same single source of truth RoundTrip and
// oauth1Params use — so the two can never disagree on what counts as valid.
func (c OAuth1Config) validate() error {
	if c.Keys == nil {
		return fmt.Errorf("httpclient: WithOAuth1 requires Config.Keys (PrivateKeyResolver)")
	}
	if c.KeyName == "" {
		return fmt.Errorf("httpclient: WithOAuth1 requires a non-empty Config.KeyName")
	}
	if c.ConsumerKey == "" {
		return fmt.Errorf("httpclient: WithOAuth1 requires a non-empty Config.ConsumerKey")
	}
	if _, err := oauth1HashForMethod(c.Method); err != nil {
		return fmt.Errorf("httpclient: WithOAuth1: %w", err)
	}
	return nil
}

// oauth1PercentEncode percent-encodes s per RFC 3986/RFC 5849: unreserved
// characters pass through unchanged, everything else becomes uppercase %XX,
// byte-wise.
func oauth1PercentEncode(s string) string {
	const upperHex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		// RFC 3986 unreserved set — the same test net/url.shouldEscape uses.
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0xF])
		}
	}
	return b.String()
}

// oauth1ExtractQueryParams builds a name -> values map from u's query string.
// It fails closed on a malformed query (fmt.Errorf-wrapped) rather than
// silently signing a subset of the transmitted parameters: url.ParseQuery
// skips any segment it cannot parse while still returning the rest, and the
// wire still sends u.RawQuery verbatim, so signing whatever ParseQuery
// salvaged would let the signed parameter set silently diverge from what is
// actually transmitted.
//
// When u.RawQuery contains percent-escapes (decoded != raw), every key and
// value is re-encoded via oauth1PercentEncode so the OAuth base-string
// construction sees the exact on-the-wire representation. Otherwise keys and
// values are left decoded. This asymmetric rule is what makes both
// ?param=token1%3Atoken2 and ?param=token1:token2 sign correctly.
//
// The needsEncode flag MUST be derived with the same unescaping url.ParseQuery
// used to produce `query` above — url.QueryUnescape, which maps '+' to a
// space. url.PathUnescape does NOT map '+' to a space, so using it here would
// disagree with `query`'s actual values on any raw query containing '+'
// (e.g. from url.Values.Encode(), which emits '+' for spaces): the flag would
// read "no escapes" while `query` already holds a decoded space, so that
// literal space — not the wire's '+' — would get signed, and any RFC 5849
// verifier reconstructing the base string from the wire bytes computes a
// different signature and 401s.
func oauth1ExtractQueryParams(u *url.URL) (map[string][]string, error) {
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("httpclient: oauth1: parse query: %w", err)
	}
	decoded, decErr := url.QueryUnescape(u.RawQuery)
	needsEncode := decErr == nil && decoded != u.RawQuery
	if !needsEncode {
		// url.ParseQuery already returns a freshly-allocated, unaliased map,
		// and oauth1ParamString clones value slices before sorting, so a
		// defensive re-copy here would buy nothing.
		return query, nil
	}

	result := make(map[string][]string, len(query))
	for k, values := range query {
		vals := make([]string, len(values))
		for i, v := range values {
			vals[i] = oauth1PercentEncode(v)
		}
		result[oauth1PercentEncode(k)] = vals
	}
	return result, nil
}

// oauth1ParamString merges queryParams and oauthParams into the normalized
// parameter string used by the OAuth 1.0a signature base string: keys sorted
// lexicographically, multi-values per key sorted, joined as k=v&k=v with no
// trailing separator.
func oauth1ParamString(queryParams map[string][]string, oauthParams map[string]string) string {
	merged := make(map[string][]string, len(queryParams)+len(oauthParams))
	for k, v := range queryParams {
		// Copy the slice: the multi-value sort below mutates in place, and the
		// caller's slice must not be aliased.
		merged[k] = slices.Clone(v)
	}
	for k, v := range oauthParams {
		merged[k] = append(merged[k], v)
	}

	keys := slices.Sorted(maps.Keys(merged))

	var pairs []string
	for _, k := range keys {
		values := merged[k]
		sort.Strings(values)
		for _, v := range values {
			pairs = append(pairs, k+"="+v)
		}
	}
	return strings.Join(pairs, "&")
}

// oauth1BaseURL normalizes u per RFC 5849 §3.4.1.2 (scheme-agnostic port
// stripping, matching the reference implementation).
func oauth1BaseURL(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := oauth1StripDefaultPort(strings.ToLower(u.Host))

	path := u.EscapedPath()
	if path == "" {
		path = "/"
	} else if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return scheme + "://" + host + path
}

// oauth1StripDefaultPort removes a trailing ":80" or ":443" from host,
// scheme-agnostically (matching the vendor reference). IPv6 authorities
// (e.g. "[::1]:443") are handled via a rightmost-colon search rather than a
// naive split on every ":", which would mangle the address's own colons.
func oauth1StripDefaultPort(host string) string {
	idx := strings.LastIndex(host, ":")
	if idx < 0 {
		return host
	}
	port := host[idx+1:]
	if port == "80" || port == "443" {
		return host[:idx]
	}
	return host
}

// oauth1SignatureBaseString builds the RFC 5849 §3.4.1 signature base string
// from the HTTP method, a normalized base URL, and the normalized parameter
// string. Each of the latter two is percent-encoded exactly once here.
func oauth1SignatureBaseString(method, baseURL, paramString string) string {
	return strings.ToUpper(method) + "&" + oauth1PercentEncode(baseURL) + "&" + oauth1PercentEncode(paramString)
}

// oauth1NormalizeMethod normalizes the empty OAuth1SignatureMethod to
// OAuth1RSASHA256. This is the single source of truth shared by
// oauth1HashForMethod and oauth1Params' emitted oauth_signature_method
// string, so the two can never disagree.
func oauth1NormalizeMethod(m OAuth1SignatureMethod) OAuth1SignatureMethod {
	if m == "" {
		return OAuth1RSASHA256
	}
	return m
}

// oauth1HashForMethod maps an OAuth1SignatureMethod to its crypto.Hash.
func oauth1HashForMethod(m OAuth1SignatureMethod) (crypto.Hash, error) {
	switch oauth1NormalizeMethod(m) {
	case OAuth1RSASHA256:
		return crypto.SHA256, nil
	case OAuth1RSASHA1:
		return crypto.SHA1, nil
	case OAuth1RSASHA512:
		return crypto.SHA512, nil
	default:
		return 0, fmt.Errorf("httpclient: oauth1: unknown signature method %q", m)
	}
}

// oauth1BodyHash digests payload with h and base64-encodes (StdEncoding) the
// result. A nil payload hashes as empty bytes.
func oauth1BodyHash(payload []byte, h crypto.Hash) string {
	digest := h.New()
	digest.Write(payload)
	return base64.StdEncoding.EncodeToString(digest.Sum(nil))
}

// oauth1Sign hashes sbs with the method's digest, signs it with key via
// PKCS#1 v1.5, and base64-encodes (StdEncoding) the signature. PKCS#1 v1.5 is
// RFC 5849 §3.4.3's mandated signature padding for OAuth 1.0a, not a locally chosen one.
func oauth1Sign(sbs string, key *rsa.PrivateKey, m OAuth1SignatureMethod) (string, error) {
	h, err := oauth1HashForMethod(m)
	if err != nil {
		return "", err
	}
	digest := h.New()
	digest.Write([]byte(sbs))
	// S5542 targets RSAES-PKCS1-v1_5 (encryption, Bleichenbacher); this is RSASSA-PKCS1-v1_5 (signature), which RFC 5849 §3.4.3 mandates for OAuth 1.0a — PSS/OAEP would be rejected by every verifier
	signature, err := rsa.SignPKCS1v15(crand.Reader, key, h, digest.Sum(nil)) // NOSONAR: signature scheme (RSASSA-PKCS1-v1_5), not encryption S5542 targets
	if err != nil {
		return "", fmt.Errorf("httpclient: oauth1: sign: %w", err)
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

const (
	oauth1NonceAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	oauth1NonceLength   = 16
)

// oauth1Nonce generates a 16-character nonce from crypto/rand, indexed into
// oauth1NonceAlphabet. Modulo bias from the 256%62 remainder is irrelevant
// for nonce uniqueness.
func oauth1Nonce() (string, error) {
	raw := make([]byte, oauth1NonceLength)
	if _, err := crand.Read(raw); err != nil {
		return "", fmt.Errorf("httpclient: oauth1: generate nonce: %w", err)
	}
	out := make([]byte, oauth1NonceLength)
	for i, b := range raw {
		out[i] = oauth1NonceAlphabet[int(b)%len(oauth1NonceAlphabet)]
	}
	return string(out), nil
}

// oauth1AuthorizationHeader builds the "OAuth ..." Authorization header value
// from params, with keys sorted lexicographically for determinism (RFC 5849
// §3.5.1 allows any order). strconv.Quote escapes a stray quote/backslash in
// a value (byte-identical to fmt's %q); a no-op for every value this code
// produces.
func oauth1AuthorizationHeader(params map[string]string) string {
	keys := slices.Sorted(maps.Keys(params))

	var b strings.Builder
	b.WriteString("OAuth ")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(params[k]))
	}
	return b.String()
}

// oauth1Params builds the oauth_* parameter set for a single request.
// oauth_signature_method is always the normalized method string — an empty
// cfg.Method emits "RSA-SHA256", never "".
func oauth1Params(cfg *OAuth1Config, bodyHash, nonce, timestamp string) map[string]string {
	return map[string]string{
		oauthConsumerKeyParam:     cfg.ConsumerKey,
		oauthNonceParam:           nonce,
		oauthSignatureMethodParam: string(oauth1NormalizeMethod(cfg.Method)),
		oauthTimestampParam:       timestamp,
		oauthVersionParam:         oauth1Version,
		oauthBodyHashParam:        bodyHash,
	}
}
