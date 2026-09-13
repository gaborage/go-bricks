package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/auth"
)

// TestContextWithPrincipalRoundTripsFromOutsideThePackage exercises the attach
// door the way a transport adapter that cannot live in package auth — the
// planned gRPC interceptor — must: verify elsewhere, publish the identity here,
// read it back downstream. It lives in the external test package because an
// in-package test would still compile if the symbol were unexported.
func TestContextWithPrincipalRoundTripsFromOutsideThePackage(t *testing.T) {
	want := auth.Principal{
		Subject:   "user-42",
		Issuer:    "https://issuer.example.com",
		Audience:  []string{"api://orders"},
		ExpiresAt: time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC),
		Claims:    map[string]any{"scope": "orders.read"},
	}

	got, ok := auth.PrincipalFromContext(auth.ContextWithPrincipal(context.Background(), want))

	require.True(t, ok)
	assert.Equal(t, want, got)
}

// TestPrincipalFromContextReportsAbsenceOutsideThePackage pins that a context
// that never passed through the attach door reports absence rather than a zero
// Principal an external caller might mistake for an identity.
func TestPrincipalFromContextReportsAbsenceOutsideThePackage(t *testing.T) {
	got, ok := auth.PrincipalFromContext(context.Background())

	assert.False(t, ok)
	assert.Equal(t, auth.Principal{}, got)
}
