package auth

import (
	"bytes"
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtesting "github.com/gaborage/go-bricks/auth/testing"
	"github.com/gaborage/go-bricks/logger"
)

// verifierNow is the frozen instant shared by the issuer and the verifier, so no
// case depends on wall-clock timing.
var verifierNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func fixedClock() time.Time { return verifierNow }

func newTestIssuer() *authtesting.Issuer {
	return authtesting.NewIssuer().WithClock(fixedClock)
}

// verifierConfig returns the config_test baseline retargeted at the fake
// issuer, so a field added to validConfig cannot silently give the verifier
// tests a different baseline than the config tests.
func verifierConfig(iss *authtesting.Issuer) Config {
	cfg := validConfig()
	cfg.Issuer = iss.IssuerURL()
	cfg.Audience = []string{iss.Audience()}
	return cfg
}

// newTestVerifier builds a verifier over the issuer's public keys, with the
// frozen clock installed.
func newTestVerifier(t *testing.T, iss *authtesting.Issuer, mutate func(*Config)) *Verifier {
	t.Helper()
	cfg := verifierConfig(iss)
	if mutate != nil {
		mutate(&cfg)
	}
	v, err := NewVerifierWithResolver(cfg, nil, NewStaticKeyResolver(iss.PublicKeys()))
	require.NoError(t, err)
	v.now = fixedClock
	return v
}

// failingPublicKeyResolver returns a fixed error from every lookup.
type failingPublicKeyResolver struct{ err error }

func (s failingPublicKeyResolver) PublicKey(_ context.Context, _ string) (*rsa.PublicKey, error) {
	return nil, s.err
}

// nilPublicKeyResolver reports success while handing back no key at all.
type nilPublicKeyResolver struct{}

func (nilPublicKeyResolver) PublicKey(_ context.Context, _ string) (*rsa.PublicKey, error) {
	return nil, nil
}

func assertRejectedWithClass(t *testing.T, err error, wantClass Class) {
	t.Helper()
	require.Error(t, err)
	var verr *VerificationError
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, wantClass, verr.Class)
	require.ErrorIs(t, err, ErrInvalidCredential)
	require.NotErrorIs(t, err, ErrKeySetUnavailable)
}

func TestNewVerifierWithResolverRejectsAnInvalidConfig(t *testing.T) {
	iss := newTestIssuer()
	cfg := verifierConfig(iss)
	cfg.Issuer = ""

	v, err := NewVerifierWithResolver(cfg, nil, NewStaticKeyResolver(iss.PublicKeys()))

	assert.Nil(t, v)
	var cerr *ConfigError
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, "auth.jwt.issuer", cerr.Field)
}

func TestNewVerifierWithResolverRejectsANilPublicKeyResolver(t *testing.T) {
	v, err := NewVerifierWithResolver(verifierConfig(newTestIssuer()), nil, nil)

	assert.Nil(t, v)
	var cerr *ConfigError
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, "auth.jwt.resolver", cerr.Field)
}

// nilReceiverResolver is a second PublicKeyResolver implementation whose method
// set tolerates a nil receiver, so the typed-nil case below exercises the
// constructor's reflect path rather than *StaticKeyResolver specifically.
type nilReceiverResolver struct{}

func (*nilReceiverResolver) PublicKey(_ context.Context, _ string) (*rsa.PublicKey, error) {
	return nil, ErrKeySetUnavailable
}

// TestNewVerifierWithResolverRejectsATypedNilPublicKeyResolver pins the second
// nil shape: a non-nil interface holding a nil pointer is not == nil, so without
// the reflect guard the verifier constructs and Verify panics inside the
// resolver on the request path.
func TestNewVerifierWithResolverRejectsATypedNilPublicKeyResolver(t *testing.T) {
	tests := []struct {
		name     string
		resolver PublicKeyResolver
	}{
		{name: "typed_nil_static_key_resolver", resolver: (*StaticKeyResolver)(nil)},
		{name: "typed_nil_other_implementation", resolver: (*nilReceiverResolver)(nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := NewVerifierWithResolver(verifierConfig(newTestIssuer()), nil, tt.resolver)

			assert.Nil(t, v)
			var cerr *ConfigError
			require.ErrorAs(t, err, &cerr)
			assert.Equal(t, "auth.jwt.resolver", cerr.Field)
		})
	}
}

func TestNewVerifierWithResolverAcceptsANilLogger(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)

	_, err := v.Verify(context.Background(), iss.MintExpired())

	assertRejectedWithClass(t, err, ClassExpired)
}

// TestNewVerifierWithResolverToleratesATypedNilLogger pins the logger's second
// nil shape. A non-nil logger.Logger holding a nil *ZeroLogger is not == nil, so
// without normalization v.debug calls Debug on a nil receiver and a rejected
// credential panics on the request path. A nil logger is documented as
// supported, so the shape is normalized to absent, never rejected.
func TestNewVerifierWithResolverToleratesATypedNilLogger(t *testing.T) {
	iss := newTestIssuer()

	v, err := NewVerifierWithResolver(verifierConfig(iss), (*logger.ZeroLogger)(nil), NewStaticKeyResolver(iss.PublicKeys()))

	require.NoError(t, err)
	require.NotNil(t, v)
	assert.Nil(t, v.log)
	v.now = fixedClock

	_, err = v.Verify(context.Background(), iss.MintExpired())

	assertRejectedWithClass(t, err, ClassExpired)
}

// TestNewVerifierWithResolverClonesTheConfiguredAudience pins the immutability
// contract: cfg travels by value, so only the slice header is copied, and a
// caller writing to the audience it passed in must not steer a live verifier.
func TestNewVerifierWithResolverClonesTheConfiguredAudience(t *testing.T) {
	iss := newTestIssuer()
	cfg := verifierConfig(iss)
	audience := []string{iss.Audience()}
	cfg.Audience = audience

	v, err := NewVerifierWithResolver(cfg, nil, NewStaticKeyResolver(iss.PublicKeys()))
	require.NoError(t, err)
	v.now = fixedClock

	audience[0] = authtesting.WrongAudience

	_, err = v.Verify(context.Background(), iss.Mint(authtesting.Claims{}))
	require.NoError(t, err)

	_, err = v.Verify(context.Background(), iss.MintWrongAudience())
	assertRejectedWithClass(t, err, ClassAudience)
}

// TestNewVerifierWithResolverClonesTheConfiguredTyp is the same contract on the
// typ allowlist, which the protected-header check reads on every verification.
func TestNewVerifierWithResolverClonesTheConfiguredTyp(t *testing.T) {
	iss := newTestIssuer()
	cfg := verifierConfig(iss)
	typ := []string{"JWT"}
	cfg.Typ = typ

	v, err := NewVerifierWithResolver(cfg, nil, NewStaticKeyResolver(iss.PublicKeys()))
	require.NoError(t, err)
	v.now = fixedClock

	typ[0] = "at+jwt"

	_, err = v.Verify(context.Background(), iss.Mint(authtesting.Claims{}))
	require.NoError(t, err)

	_, err = v.Verify(context.Background(), iss.MintWithType("at+jwt"))
	assertRejectedWithClass(t, err, ClassType)
}

func TestVerifierCloseIsIdempotent(t *testing.T) {
	v := newTestVerifier(t, newTestIssuer(), nil)

	require.NoError(t, v.Close())
	require.NoError(t, v.Close())
}

func TestVerifierAcceptsAnRS256Credential(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)

	principal, err := v.Verify(context.Background(), iss.Mint(authtesting.Claims{}))

	require.NoError(t, err)
	assert.Equal(t, authtesting.DefaultSubject, principal.Subject)
	assert.Equal(t, iss.IssuerURL(), principal.Issuer)
	assert.Equal(t, []string{iss.Audience()}, principal.Audience)
	assert.Equal(t, verifierNow.Add(authtesting.DefaultLifetime).Unix(), principal.ExpiresAt.Unix())
	assert.Equal(t, verifierNow.Unix(), principal.IssuedAt.Unix())
}

func TestVerifierAcceptsAPS256Credential(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)

	principal, err := v.Verify(context.Background(), iss.MintPS256())

	require.NoError(t, err)
	assert.Equal(t, authtesting.DefaultSubject, principal.Subject)
}

func TestVerifierReturnsExtraClaimsOnThePrincipal(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)
	credential := iss.Mint(authtesting.Claims{
		Subject: "user-42",
		Extra:   map[string]any{"scope": "orders:read", "tenant": "acme"},
	})

	principal, err := v.Verify(context.Background(), credential)

	require.NoError(t, err)
	assert.Equal(t, "user-42", principal.Subject)
	scope, ok := principal.Claim("scope")
	require.True(t, ok)
	assert.Equal(t, "orders:read", scope)
	tenant, ok := principal.Claim("tenant")
	require.True(t, ok)
	assert.Equal(t, "acme", tenant)
	assert.Equal(t, iss.IssuerURL(), principal.Claims["iss"])
}

func TestVerifierAcceptsAMultiValuedAudienceThatIntersects(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)
	credential := iss.Mint(authtesting.Claims{Audience: []string{"other-api", iss.Audience()}})

	principal, err := v.Verify(context.Background(), credential)

	require.NoError(t, err)
	assert.Equal(t, []string{"other-api", iss.Audience()}, principal.Audience)
}

func TestVerifierAcceptsACredentialExpiredWithinLeeway(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)

	_, err := v.Verify(context.Background(), iss.MintExpiredWithin(30*time.Second))

	require.NoError(t, err)
}

// TestVerifierTreatsTheLeewayWidenedExpiryAsExclusive pins the exp boundary in
// both directions: RFC 7519 section 4.1.4 requires the current time to be
// strictly before exp, so an exp exactly at now-Leeway is expired while the very
// next second is still accepted.
func TestVerifierTreatsTheLeewayWidenedExpiryAsExclusive(t *testing.T) {
	const leeway = 30 * time.Second
	tests := []struct {
		name       string
		exp        int64
		wantExpiry bool
	}{
		{name: "exactly_at_the_leeway_edge", exp: verifierNow.Add(-leeway).Unix(), wantExpiry: true},
		{name: "one_second_inside_the_leeway_edge", exp: verifierNow.Add(-leeway + time.Second).Unix()},
		{name: "one_second_past_the_leeway_edge", exp: verifierNow.Add(-leeway - time.Second).Unix(), wantExpiry: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestVerifier(t, newTestIssuer(), func(c *Config) { c.Leeway = leeway })
			payload := fmt.Sprintf(`{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":%d}`, tt.exp)

			principal, err := v.principalFromPayload([]byte(payload))

			if tt.wantExpiry {
				assertRejectedWithClass(t, err, ClassExpired)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.exp, principal.ExpiresAt.Unix())
		})
	}
}

func TestVerifierIgnoresTypWhenUnconfigured(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)

	_, err := v.Verify(context.Background(), iss.MintWithType("anything+jwt"))

	require.NoError(t, err)
}

func TestVerifierMatchesTypCaseInsensitively(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, func(c *Config) { c.Typ = []string{"at+jwt"} })

	_, err := v.Verify(context.Background(), iss.MintWithType("AT+JWT"))

	require.NoError(t, err)
}

func TestVerifierRejectsMissingCredentials(t *testing.T) {
	v := newTestVerifier(t, newTestIssuer(), nil)

	for _, credential := range []string{"", "   ", "\t\n"} {
		_, err := v.Verify(context.Background(), credential)
		require.ErrorIs(t, err, ErrMissingCredential)
		assert.NotErrorIs(t, err, ErrInvalidCredential)
	}
}

func TestVerifierRejectsCredentialsByClass(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Config)
		credential func(*authtesting.Issuer) string
		wantClass  Class
	}{
		{
			name:       "alg_not_allowed",
			mutate:     func(c *Config) { c.Algorithms = []string{AlgRS256} },
			credential: func(i *authtesting.Issuer) string { return i.MintPS256() },
			wantClass:  ClassAlgorithm,
		},
		{
			name:       "alg_none",
			credential: func(i *authtesting.Issuer) string { return i.MintAlgNone() },
			wantClass:  ClassAlgorithm,
		},
		{
			name:       "alg_es256",
			credential: func(i *authtesting.Issuer) string { return i.MintES256() },
			wantClass:  ClassAlgorithm,
		},
		{
			name:       "not_a_jws",
			credential: func(*authtesting.Issuer) string { return "not-a-token" },
			wantClass:  ClassMalformed,
		},
		{
			name:       "missing_kid",
			credential: func(i *authtesting.Issuer) string { return i.MintMissingKeyID() },
			wantClass:  ClassKidMissing,
		},
		{
			name:       "unknown_kid",
			credential: func(i *authtesting.Issuer) string { return i.MintUnknownKeyID() },
			wantClass:  ClassKidUnknown,
		},
		{
			name:       "bad_signature",
			credential: func(i *authtesting.Issuer) string { return i.MintBadSignature() },
			wantClass:  ClassSignature,
		},
		{
			name:       "corrupt_signature",
			credential: func(i *authtesting.Issuer) string { return i.MintCorruptSignature() },
			wantClass:  ClassSignature,
		},
		{
			name:       "wrong_issuer",
			credential: func(i *authtesting.Issuer) string { return i.MintWrongIssuer() },
			wantClass:  ClassIssuer,
		},
		{
			name:       "wrong_audience_string_form",
			credential: func(i *authtesting.Issuer) string { return i.MintWrongAudience() },
			wantClass:  ClassAudience,
		},
		{
			name: "wrong_audience_array_form",
			credential: func(i *authtesting.Issuer) string {
				return i.Mint(authtesting.Claims{Audience: []string{authtesting.WrongAudience, "another-api"}})
			},
			wantClass: ClassAudience,
		},
		{
			name:       "missing_expiry",
			credential: func(i *authtesting.Issuer) string { return i.MintMissingExpiry() },
			wantClass:  ClassMissingExpiry,
		},
		{
			name:       "expired_past_leeway",
			credential: func(i *authtesting.Issuer) string { return i.MintExpired() },
			wantClass:  ClassExpired,
		},
		{
			name:       "future_not_before",
			credential: func(i *authtesting.Issuer) string { return i.MintFutureNotBefore() },
			wantClass:  ClassNotYetValid,
		},
		{
			name:       "future_issued_at",
			credential: func(i *authtesting.Issuer) string { return i.MintFutureIssuedAt() },
			wantClass:  ClassIssuedInFuture,
		},
		{
			name:       "typ_mismatch_when_configured",
			mutate:     func(c *Config) { c.Typ = []string{"at+jwt"} },
			credential: func(i *authtesting.Issuer) string { return i.MintWithType("JWT") },
			wantClass:  ClassType,
		},
		{
			name:   "typ_blank_when_configured",
			mutate: func(c *Config) { c.Typ = []string{"at+jwt"} },
			credential: func(i *authtesting.Issuer) string {
				return i.MintWith(authtesting.MintOptions{Type: " "})
			},
			wantClass: ClassType,
		},
		{
			// One end-to-end malformed-claim row: the payload-level table covers
			// the individual rules, this proves the route survives full Verify.
			name: "non_numeric_expiry",
			credential: func(i *authtesting.Issuer) string {
				return i.MintWith(authtesting.MintOptions{Claims: authtesting.Claims{
					OmitExpiry: true,
					Extra:      map[string]any{"exp": "soon"},
				}})
			},
			wantClass: ClassMalformed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iss := newTestIssuer()
			v := newTestVerifier(t, iss, tt.mutate)

			_, err := v.Verify(context.Background(), tt.credential(iss))

			assertRejectedWithClass(t, err, tt.wantClass)
		})
	}
}

// TestVerifierRejectsMalformedClaimShapes covers claim shapes the fake issuer
// cannot mint, because it always overwrites iss/aud/sub/iat with well-formed
// values. The payload is fed straight to the post-signature claim stage.
func TestVerifierRejectsMalformedClaimShapes(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantClass Class
	}{
		{
			name:      "payload_is_not_json",
			payload:   "not-json",
			wantClass: ClassMalformed,
		},
		{
			name:      "issuer_is_not_a_string",
			payload:   `{"iss":42,"aud":"go-bricks-test","exp":1767268800}`,
			wantClass: ClassIssuer,
		},
		{
			name:      "issuer_is_absent",
			payload:   `{"aud":"go-bricks-test","exp":1767268800}`,
			wantClass: ClassIssuer,
		},
		{
			name:      "audience_is_a_number",
			payload:   `{"iss":"https://issuer.test/","aud":7,"exp":1767268800}`,
			wantClass: ClassMalformed,
		},
		{
			name:      "audience_array_carries_a_non_string",
			payload:   `{"iss":"https://issuer.test/","aud":["go-bricks-test",7],"exp":1767268800}`,
			wantClass: ClassMalformed,
		},
		{
			name:      "audience_is_absent",
			payload:   `{"iss":"https://issuer.test/","exp":1767268800}`,
			wantClass: ClassAudience,
		},
		{
			name:      "expiry_is_not_numeric",
			payload:   `{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":"soon"}`,
			wantClass: ClassMalformed,
		},
		{
			name:      "not_before_is_not_numeric",
			payload:   `{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":1767268800,"nbf":"later"}`,
			wantClass: ClassMalformed,
		},
		{
			name:      "issued_at_is_not_numeric",
			payload:   `{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":1767268800,"iat":"yesterday"}`,
			wantClass: ClassMalformed,
		},
		{
			name:      "subject_is_not_a_string",
			payload:   `{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":1767268800,"sub":7}`,
			wantClass: ClassMalformed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestVerifier(t, newTestIssuer(), nil)

			_, err := v.principalFromPayload([]byte(tt.payload))

			assertRejectedWithClass(t, err, tt.wantClass)
		})
	}
}

func TestVerifierAcceptsAPayloadWithoutASubjectOrIssuedAt(t *testing.T) {
	v := newTestVerifier(t, newTestIssuer(), nil)
	payload := `{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":1767268800}`

	principal, err := v.principalFromPayload([]byte(payload))

	require.NoError(t, err)
	assert.Empty(t, principal.Subject)
	assert.True(t, principal.IssuedAt.IsZero())
	assert.Equal(t, int64(1767268800), principal.ExpiresAt.Unix())
}

func TestVerifierTreatsANullNumericDateAsAbsent(t *testing.T) {
	v := newTestVerifier(t, newTestIssuer(), nil)
	payload := `{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":1767268800,"nbf":null,"iat":null}`

	principal, err := v.principalFromPayload([]byte(payload))

	require.NoError(t, err)
	assert.True(t, principal.IssuedAt.IsZero())
}

func TestVerifierReportsAnUnavailableKeySetSeparatelyFromAnInvalidCredential(t *testing.T) {
	iss := newTestIssuer()
	cfg := verifierConfig(iss)
	resolvers := map[string]PublicKeyResolver{
		"empty_static_resolver": NewStaticKeyResolver(nil),
		"failing_resolver":      failingPublicKeyResolver{err: errors.New("dial tcp: connection refused")},
		"nil_key_from_resolver": nilPublicKeyResolver{},
		"explicit_unavailable":  failingPublicKeyResolver{err: ErrKeySetUnavailable},
	}

	for name, resolver := range resolvers {
		t.Run(name, func(t *testing.T) {
			v, err := NewVerifierWithResolver(cfg, nil, resolver)
			require.NoError(t, err)
			v.now = fixedClock

			_, err = v.Verify(context.Background(), iss.Mint(authtesting.Claims{}))

			require.Error(t, err)
			require.ErrorIs(t, err, ErrKeySetUnavailable)
			require.NotErrorIs(t, err, ErrInvalidCredential)
			var verr *VerificationError
			assert.NotErrorAs(t, err, &verr)
		})
	}
}

func TestVerifierNeverRendersTheCredentialOrSubject(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)
	credential := iss.MintWith(authtesting.MintOptions{
		Claims: authtesting.Claims{Subject: "super-secret-subject", Issuer: authtesting.WrongIssuer},
	})

	_, err := v.Verify(context.Background(), credential)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), credential)
	assert.NotContains(t, err.Error(), "super-secret-subject")
}

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written to it. logger.New writes there, so this is how the package reads back
// what a rejection actually logged.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { os.Stdout = original }()
	defer r.Close()
	os.Stdout = w

	var buf bytes.Buffer
	copied := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&buf, r)
		copied <- copyErr
	}()

	fn()

	require.NoError(t, w.Close())
	require.NoError(t, <-copied)
	return buf.String()
}

func TestVerifierLogsTheRejectionClassOnly(t *testing.T) {
	const subject = "log-secret-subject"
	iss := newTestIssuer()
	credential := iss.MintWith(authtesting.MintOptions{Claims: authtesting.Claims{
		Subject:   subject,
		IssuedAt:  verifierNow.Add(-2 * time.Hour),
		ExpiresAt: verifierNow.Add(-time.Hour),
	}})

	// The logger binds os.Stdout at construction, so it has to be built inside
	// the capture for the rejection line to land in the buffer.
	var verifyErr error
	out := captureStdout(t, func() {
		v, err := NewVerifierWithResolver(verifierConfig(iss), logger.New("debug", false), NewStaticKeyResolver(iss.PublicKeys()))
		require.NoError(t, err)
		v.now = fixedClock
		_, verifyErr = v.Verify(context.Background(), credential)
	})

	assertRejectedWithClass(t, verifyErr, ClassExpired)
	assert.Contains(t, out, string(ClassExpired))
	assert.NotContains(t, out, subject)
	assert.NotContains(t, out, credential)
	for i, segment := range strings.Split(credential, ".") {
		assert.NotContainsf(t, out, segment, "log output leaked credential segment %d", i)
	}
}

func TestVerifierRejectsAFractionalExpiryInThePast(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)
	credential := iss.MintWith(authtesting.MintOptions{Claims: authtesting.Claims{
		OmitExpiry: true,
		Extra:      map[string]any{"exp": float64(verifierNow.Add(-time.Hour).Unix()) + 0.5},
	}})

	_, err := v.Verify(context.Background(), credential)

	assertRejectedWithClass(t, err, ClassExpired)
}

func TestVerifierKeepsTheSubSecondPartOfANumericDate(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)
	exp := float64(verifierNow.Add(time.Hour).Unix()) + 0.25
	credential := iss.MintWith(authtesting.MintOptions{Claims: authtesting.Claims{
		OmitExpiry: true,
		Extra:      map[string]any{"exp": exp},
	}})

	principal, err := v.Verify(context.Background(), credential)

	require.NoError(t, err)
	assert.Equal(t, int64(exp), principal.ExpiresAt.Unix())
	assert.Equal(t, 250000000, principal.ExpiresAt.Nanosecond())
}

func TestVerifierAcceptsACredentialSignedByARotatedKey(t *testing.T) {
	iss := newTestIssuer()
	iss.Rotate("test-key-2")
	v := newTestVerifier(t, iss, nil)

	principal, err := v.Verify(context.Background(), iss.Mint(authtesting.Claims{}))

	require.NoError(t, err)
	assert.Equal(t, authtesting.DefaultSubject, principal.Subject)
}

// jsonSerialization rewrites a compact JWS into one of the JWS JSON
// Serialization shapes, which the package must refuse: the JSON form carries an
// attacker-controlled unprotected header and an unbounded signatures array, and
// go-jose's ParseSigned dispatches to it for any input starting with "{".
func jsonSerialization(t *testing.T, compact, shape string) string {
	t.Helper()
	parts := strings.Split(compact, ".")
	require.Len(t, parts, 3)
	header, payload, signature := parts[0], parts[1], parts[2]

	switch shape {
	case "general":
		return fmt.Sprintf(`{"payload":%q,"signatures":[{"protected":%q,"signature":%q}]}`, payload, header, signature)
	case "flattened":
		return fmt.Sprintf(`{"payload":%q,"protected":%q,"signature":%q}`, payload, header, signature)
	case "leading_whitespace":
		return " \n\t" + jsonSerialization(t, compact, "general")
	default:
		t.Fatalf("unknown shape %q", shape)
		return ""
	}
}

// TestVerifierRejectsTheJWSJSONSerialization pins the compact-only rule. The
// credential rewritten here verifies in its compact form, so a revert to
// jose.ParseSigned turns every row green — which is exactly the hole.
func TestVerifierRejectsTheJWSJSONSerialization(t *testing.T) {
	for _, shape := range []string{"general", "flattened", "leading_whitespace"} {
		t.Run(shape, func(t *testing.T) {
			iss := newTestIssuer()
			v := newTestVerifier(t, iss, nil)
			compact := iss.Mint(authtesting.Claims{})
			_, err := v.Verify(context.Background(), compact)
			require.NoError(t, err, "the compact form must verify, or the rejection below proves nothing")

			_, err = v.Verify(context.Background(), jsonSerialization(t, compact, shape))

			assertRejectedWithClass(t, err, ClassMalformed)
		})
	}
}

// TestVerifierRejectsAnOversizedCredential pins the DoS bound: the input is
// refused on length, before any base64 decoding or JSON parsing. Junk of either
// length is ClassMalformed whichever gate catches it, so the cause is what
// separates the length gate from the parser — assert on that, or > and >= look
// alike.
func TestVerifierRejectsAnOversizedCredential(t *testing.T) {
	v := newTestVerifier(t, newTestIssuer(), nil)

	_, err := v.Verify(context.Background(), strings.Repeat("a", maxCredentialBytes+1))

	assertRejectedWithClass(t, err, ClassMalformed)
	assert.Contains(t, causeMessage(t, err), lengthGateMessage, "one byte over the bound is refused by the length gate")
}

// TestVerifierAdmitsACredentialOfExactlyTheMaximumLength pins the bound as
// inclusive: a credential of exactly maxCredentialBytes reaches the parser, so
// its rejection carries a parse cause and not the length gate's.
func TestVerifierAdmitsACredentialOfExactlyTheMaximumLength(t *testing.T) {
	v := newTestVerifier(t, newTestIssuer(), nil)

	_, err := v.Verify(context.Background(), strings.Repeat("a", maxCredentialBytes))

	assertRejectedWithClass(t, err, ClassMalformed)
	assert.NotContains(t, causeMessage(t, err), lengthGateMessage, "exactly the bound is not refused on length")
}

// lengthGateMessage is the cause the maxCredentialBytes guard attaches.
const lengthGateMessage = "credential exceeds the maximum accepted length"

// causeMessage returns the message of the VerificationError's cause.
func causeMessage(t *testing.T, err error) string {
	t.Helper()
	var verr *VerificationError
	require.ErrorAs(t, err, &verr)
	require.Error(t, verr.Cause)
	return verr.Cause.Error()
}

func TestVerifierAcceptsACredentialAtTheLengthBound(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)
	credential := iss.Mint(authtesting.Claims{})
	require.LessOrEqual(t, len(credential), maxCredentialBytes)

	_, err := v.Verify(context.Background(), credential)

	require.NoError(t, err)
}

// TestVerifierRejectsOutOfRangeNumericDates pins that an out-of-range exp, nbf
// or iat is malformed rather than platform-dependent: int64(float64) saturates
// on arm64 and wraps on amd64, so without the bound "nbf": 1e300 silently passes
// its check on one of the two.
func TestVerifierRejectsOutOfRangeNumericDates(t *testing.T) {
	const validExp = 1767268800
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "exp_far_future",
			payload: `{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":1e300}`,
		},
		{
			name:    "exp_far_past",
			payload: `{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":-1e300}`,
		},
		{
			name:    "nbf_far_future",
			payload: fmt.Sprintf(`{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":%d,"nbf":1e300}`, validExp),
		},
		{
			name:    "nbf_far_past",
			payload: fmt.Sprintf(`{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":%d,"nbf":-1e300}`, validExp),
		},
		{
			name:    "iat_far_future",
			payload: fmt.Sprintf(`{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":%d,"iat":1e300}`, validExp),
		},
		{
			name:    "iat_far_past",
			payload: fmt.Sprintf(`{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":%d,"iat":-1e300}`, validExp),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestVerifier(t, newTestIssuer(), nil)
			v.now = func() time.Time { return time.Unix(validExp-3600, 0) }

			_, err := v.principalFromPayload([]byte(tt.payload))

			assertRejectedWithClass(t, err, ClassMalformed)
		})
	}
}

func TestVerifierAcceptsANumericDateAtTheBound(t *testing.T) {
	v := newTestVerifier(t, newTestIssuer(), nil)
	payload := fmt.Sprintf(`{"iss":"https://issuer.test/","aud":"go-bricks-test","exp":%d,"iat":%d}`, int64(maxNumericDate), 0)

	principal, err := v.principalFromPayload([]byte(payload))

	require.NoError(t, err)
	assert.Equal(t, int64(maxNumericDate), principal.ExpiresAt.Unix())
}

// TestNewVerifierWithResolverRejectsAnUnboundedLeeway pins that the leeway cap
// binds at the constructor: an unbounded leeway would make MintExpired verify.
func TestNewVerifierWithResolverRejectsAnUnboundedLeeway(t *testing.T) {
	iss := newTestIssuer()
	cfg := verifierConfig(iss)
	cfg.Leeway = 876000 * time.Hour

	v, err := NewVerifierWithResolver(cfg, nil, NewStaticKeyResolver(iss.PublicKeys()))

	assert.Nil(t, v)
	var cerr *ConfigError
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, "auth.jwt.leeway", cerr.Field)
}

// TestVerifierRejectsAnExpiredCredentialAtTheMaximumLeeway pins the consequence
// of the cap: even the widest configurable window still expires a credential.
func TestVerifierRejectsAnExpiredCredentialAtTheMaximumLeeway(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, func(c *Config) { c.Leeway = maxLeeway })

	_, err := v.Verify(context.Background(), iss.MintExpired())

	assertRejectedWithClass(t, err, ClassExpired)
}

// leakyKidResolver returns an ErrKidUnknown wrapped in text a hostile or careless
// resolver could put there, to prove the verifier does not forward it.
type leakyKidResolver struct{ secret string }

func (r *leakyKidResolver) PublicKey(_ context.Context, _ string) (*rsa.PublicKey, error) {
	return nil, fmt.Errorf("lookup failed for %s: %w", r.secret, ErrKidUnknown)
}

// TestVerifierDoesNotForwardTheResolverErrorText pins that a resolver's own error
// text never reaches the caller through the exported VerificationError.Cause. The
// resolver is consumer-supplied, so its wrapping is not ours to vouch for.
func TestVerifierDoesNotForwardTheResolverErrorText(t *testing.T) {
	const secret = "super-secret-resolver-detail"
	iss := authtesting.NewIssuer()
	v, err := NewVerifierWithResolver(verifierConfig(iss), nil, &leakyKidResolver{secret: secret})
	require.NoError(t, err)

	_, err = v.Verify(context.Background(), iss.Mint(authtesting.Claims{}))
	require.Error(t, err)

	var verr *VerificationError
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, ClassKidUnknown, verr.Class)
	require.ErrorIs(t, err, ErrInvalidCredential)
	require.ErrorIs(t, verr.Cause, ErrKidUnknown)
	// Cause is the exported field a caller reaches through errors.As. The fmt
	// verbs are already redacted by Format, so asserting on them would pass with
	// or without the fix.
	assert.NotContains(t, verr.Cause.Error(), secret)
	assert.Equal(t, ErrKidUnknown, verr.Cause)
}
