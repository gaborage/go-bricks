package jose

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInboundVerifiedRoundtrip(t *testing.T) {
	ctx := context.Background()
	assert.False(t, IsInboundVerified(ctx))

	verified := WithInboundVerified(ctx)
	assert.True(t, IsInboundVerified(verified))
	// Original ctx must not be mutated.
	assert.False(t, IsInboundVerified(ctx))
}

func TestClaimsRoundtrip(t *testing.T) {
	ctx := context.Background()
	assert.Nil(t, ClaimsFromContext(ctx))

	c := &Claims{Issuer: "visa", Subject: "merchant-1"}
	withClaims := WithClaims(ctx, c)
	got := ClaimsFromContext(withClaims)
	assert.Equal(t, c, got)
}

func TestPolicyRoundtrip(t *testing.T) {
	ctx := context.Background()
	assert.Nil(t, PolicyFromContext(ctx))

	p := &Policy{Direction: DirectionOutbound, SignKid: "ours"}
	withPolicy := WithPolicy(ctx, p)
	assert.Equal(t, p, PolicyFromContext(withPolicy))
}
