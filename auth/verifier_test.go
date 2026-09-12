package auth

import (
	"context"
	"crypto/rsa"
	"errors"
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

// verifierConfig returns a config aligned with the fake issuer's defaults.
func verifierConfig(iss *authtesting.Issuer) Config {
	return Config{
		Issuer:                 iss.IssuerURL(),
		Audience:               []string{iss.Audience()},
		JWKSURI:                testJWKSURI,
		Algorithms:             []string{AlgRS256, AlgPS256},
		Leeway:                 30 * time.Second,
		JWKSTTL:                15 * time.Minute,
		JWKSStaleCeiling:       time.Hour,
		JWKSMinRefreshInterval: 30 * time.Second,
		JWKSMaxBodyBytes:       1 << 20,
	}
}

// newTestVerifier builds a verifier over the issuer's public keys, with the
// frozen clock installed.
func newTestVerifier(t *testing.T, iss *authtesting.Issuer, mutate func(*Config)) *Verifier {
	t.Helper()
	cfg := verifierConfig(iss)
	if mutate != nil {
		mutate(&cfg)
	}
	v, err := NewVerifierWithKeySource(cfg, nil, NewStaticKeySource(iss.PublicKeys()))
	require.NoError(t, err)
	v.now = fixedClock
	return v
}

// failingKeySource returns a fixed error from every lookup.
type failingKeySource struct{ err error }

func (s failingKeySource) PublicKey(_ context.Context, _ string) (*rsa.PublicKey, error) {
	return nil, s.err
}

// nilKeySource reports success while handing back no key at all.
type nilKeySource struct{}

func (nilKeySource) PublicKey(_ context.Context, _ string) (*rsa.PublicKey, error) {
	return nil, nil
}

func assertRejectedWithClass(t *testing.T, err error, wantClass string) {
	t.Helper()
	require.Error(t, err)
	var verr *VerificationError
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, wantClass, verr.Class)
	require.ErrorIs(t, err, ErrInvalidCredential)
	require.NotErrorIs(t, err, ErrKeySetUnavailable)
}

func TestNewVerifierWithKeySourceRejectsAnInvalidConfig(t *testing.T) {
	iss := newTestIssuer()
	cfg := verifierConfig(iss)
	cfg.Issuer = ""

	v, err := NewVerifierWithKeySource(cfg, nil, NewStaticKeySource(iss.PublicKeys()))

	assert.Nil(t, v)
	var cerr *ConfigError
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, "auth.jwt.issuer", cerr.Field)
}

func TestNewVerifierWithKeySourceRejectsANilKeySource(t *testing.T) {
	v, err := NewVerifierWithKeySource(verifierConfig(newTestIssuer()), nil, nil)

	assert.Nil(t, v)
	var cerr *ConfigError
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, "auth.jwt.keysource", cerr.Field)
}

func TestNewVerifierWithKeySourceAcceptsANilLogger(t *testing.T) {
	iss := newTestIssuer()
	v := newTestVerifier(t, iss, nil)

	_, err := v.Verify(context.Background(), iss.MintExpired())

	assertRejectedWithClass(t, err, ClassExpired)
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
		wantClass  string
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
			name: "non_numeric_expiry",
			credential: func(i *authtesting.Issuer) string {
				return i.MintWith(authtesting.MintOptions{Claims: authtesting.Claims{
					OmitExpiry: true,
					Extra:      map[string]any{"exp": "soon"},
				}})
			},
			wantClass: ClassMalformed,
		},
		{
			name: "non_numeric_not_before",
			credential: func(i *authtesting.Issuer) string {
				return i.MintWith(authtesting.MintOptions{Claims: authtesting.Claims{
					Extra: map[string]any{"nbf": "later"},
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
		wantClass string
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
	sources := map[string]KeySource{
		"empty_static_source":  NewStaticKeySource(nil),
		"failing_source":       failingKeySource{err: errors.New("dial tcp: connection refused")},
		"nil_key_from_source":  nilKeySource{},
		"explicit_unavailable": failingKeySource{err: ErrKeySetUnavailable},
	}

	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			v, err := NewVerifierWithKeySource(cfg, nil, src)
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

func TestVerifierLogsTheRejectionClassOnly(t *testing.T) {
	iss := newTestIssuer()
	v, err := NewVerifierWithKeySource(verifierConfig(iss), logger.New("debug", false), NewStaticKeySource(iss.PublicKeys()))
	require.NoError(t, err)
	v.now = fixedClock

	_, err = v.Verify(context.Background(), iss.MintExpired())

	assertRejectedWithClass(t, err, ClassExpired)
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
