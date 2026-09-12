package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPrincipal() Principal {
	return Principal{
		Subject:   "user-42",
		Issuer:    "https://issuer.example.com",
		Audience:  []string{"api://orders"},
		ExpiresAt: time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC),
		IssuedAt:  time.Date(2029, time.January, 1, 0, 0, 0, 0, time.UTC),
		Claims:    map[string]any{"scope": "orders.read", "tid": "acme"},
	}
}

func TestPrincipalRoundTripsThroughContext(t *testing.T) {
	want := testPrincipal()

	got, ok := PrincipalFromContext(withPrincipal(context.Background(), want))

	require.True(t, ok)
	assert.Equal(t, want, got)
}

func TestPrincipalFromContextReportsAbsence(t *testing.T) {
	got, ok := PrincipalFromContext(context.Background())

	assert.False(t, ok)
	assert.Equal(t, Principal{}, got)
}

func TestPrincipalFromContextIgnoresAForeignValue(t *testing.T) {
	type otherKey int
	ctx := context.WithValue(context.Background(), otherKey(0), testPrincipal())

	_, ok := PrincipalFromContext(ctx)

	assert.False(t, ok)
}

func TestPrincipalClaimReturnsAPresentClaim(t *testing.T) {
	value, ok := testPrincipal().Claim("scope")

	require.True(t, ok)
	assert.Equal(t, "orders.read", value)
}

func TestPrincipalClaimReportsAMissingClaim(t *testing.T) {
	value, ok := testPrincipal().Claim("groups")

	assert.False(t, ok)
	assert.Nil(t, value)
}

func TestPrincipalClaimOnANilClaimsMapReportsAbsence(t *testing.T) {
	value, ok := Principal{}.Claim("scope")

	assert.False(t, ok)
	assert.Nil(t, value)
}

func TestPrincipalClaimReturnsAPresentNilValue(t *testing.T) {
	p := Principal{Claims: map[string]any{"act": nil}}

	value, ok := p.Claim("act")

	assert.True(t, ok)
	assert.Nil(t, value)
}

func TestPrincipalClaimsAreSharedNotCopied(t *testing.T) {
	// Documented contract: Claims is read-only and aliased by every reader, so this
	// pins the sharing that the doc comment forbids callers from exploiting.
	want := testPrincipal()
	ctx := withPrincipal(context.Background(), want)

	first, ok := PrincipalFromContext(ctx)
	require.True(t, ok)
	second, ok := PrincipalFromContext(ctx)
	require.True(t, ok)

	first.Claims["scope"] = "mutated"
	value, ok := second.Claim("scope")
	require.True(t, ok)
	assert.Equal(t, "mutated", value)
}

// TestPrincipalStringElidesTheSubjectAndClaims pins the elision seam: a
// Principal reaching fmt or a structured logger must never render the subject or
// a claim value. Only the verbs that consult fmt.Stringer are covered; %#v
// bypasses Stringer by design and is documented as such.
func TestPrincipalStringElidesTheSubjectAndClaims(t *testing.T) {
	p := Principal{
		Subject:   "super-secret-subject",
		Issuer:    "https://issuer.example.com",
		Audience:  []string{"api://orders"},
		ExpiresAt: time.Unix(1767268800, 0).UTC(),
		IssuedAt:  time.Unix(1767265200, 0).UTC(),
		Claims: map[string]any{
			"sub":   "super-secret-subject",
			"email": "person@example.com",
			"scope": "orders:read",
		},
	}

	// render goes through a variable format so the verb under test survives:
	// spelling fmt.Sprintf("%v", p) inline is rewritten to p.String() by
	// gocritic's redundantSprint, which is the very substitution this test must
	// not make.
	render := func(format string, value any) string { return fmt.Sprintf(format, value) }
	renderings := map[string]string{
		"%v":        render("%v", p),
		"%s":        render("%s", p),
		"%q":        render("%q", p),
		"%+v":       render("%+v", p),
		"String":    p.String(),
		"pointer%v": render("%v", &p),
		"pointer%s": render("%s", &p),
	}

	for verb, rendered := range renderings {
		assert.NotContains(t, rendered, "super-secret-subject", verb)
		assert.NotContains(t, rendered, "person@example.com", verb)
		assert.NotContains(t, rendered, "orders:read", verb)
		assert.Contains(t, rendered, "https://issuer.example.com", verb)
		assert.Contains(t, rendered, "elided", verb)
	}
}

func TestPrincipalStringRendersTheNonSensitiveFields(t *testing.T) {
	p := Principal{
		Issuer:    "https://issuer.example.com",
		Audience:  []string{"api://orders", "api://billing"},
		ExpiresAt: time.Unix(1767268800, 0).UTC(),
		Claims:    map[string]any{"a": 1, "b": 2},
	}

	rendered := p.String()

	assert.Contains(t, rendered, "api://orders")
	assert.Contains(t, rendered, "api://billing")
	assert.Contains(t, rendered, "2026-01-01T12:00:00Z")
	assert.Contains(t, rendered, "claims: <elided:2>")
	assert.Contains(t, rendered, "subject: <elided>")
}

// TestPrincipalStringOnTheZeroValue pins that the elision seam never panics on
// an empty Principal, which is what a rejected verification returns.
func TestPrincipalStringOnTheZeroValue(t *testing.T) {
	assert.NotPanics(t, func() { _ = Principal{}.String() })
	assert.Contains(t, Principal{}.String(), "claims: <elided:0>")
}
