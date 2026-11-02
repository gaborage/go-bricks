package messaging

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/multitenant"
)

func TestStampingPublisherRefusesOffGrammarTenant(t *testing.T) {
	const tenant = "Acme"
	base := &recordingStubClient{}
	p := newStampingPublisher(base, "")
	ctx := multitenant.SetTenant(context.Background(), tenant)

	err := p.publishBytes(ctx, publishOptions{Exchange: "orders", RoutingKey: "order.created"}, []byte("{}"))

	require.ErrorIs(t, err, multitenant.ErrInvalidTenantID)
	assert.NotContains(t, err.Error(), tenant)
	assert.Zero(t, base.publishes, "a refused publish never reaches the wrapped client")
}
