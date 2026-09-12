package auth

import (
	"context"
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
