//go:build integration

package sealed_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/testing/containers"
)

// pkgBroker is the one RabbitMQ container this test binary shares (ADR-020); it starts on first
// use, so a unit-only -run filter boots nothing.
var pkgBroker = containers.NewShared("RabbitMQ", 3*time.Minute,
	func(ctx context.Context) (*containers.RabbitMQContainer, bool, error) {
		return containers.StartRabbitMQContainerForTestMain(ctx, nil)
	})

// closeSharedBroker is called by TestMain (main_test.go) after m.Run.
func closeSharedBroker() { pkgBroker.Close() }

func TestPublishSealedFailsUnroutableOnAMandatoryHandleIntegration(t *testing.T) {
	client := messaging.NewAMQPClient(pkgBroker.Get(t).BrokerURL(), logger.New("disabled", true))
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, client.IsReady, 10*time.Second, 200*time.Millisecond)

	configureStore(t, pairStore(t), nil)
	decls := messaging.NewDeclarations()
	exchange := "sealed-unroutable-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	require.NoError(t, client.DeclareExchange(t.Context(), decls.DeclareTopicExchange(exchange)))
	h := messaging.DeclareTypedPublisher[paymentAuthorized](decls, &messaging.PublisherOptions{
		Exchange: exchange, RoutingKey: "payment.nobody-listens", EventType: eventType, Mandatory: true,
	})
	require.NoError(t, decls.Validate())

	data, _, err := h.Seal(t.Context(), doorEvent())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second) // far above 5 attempts x 100ms backoff
	defer cancel()
	err = h.PublishSealed(ctx, client, data)
	require.ErrorIs(t, err, messaging.ErrPublishUnroutable)
	require.ErrorIs(t, err, messaging.ErrPublishRetriesExhausted)
	assert.NotErrorIs(t, err, messaging.ErrSealedBytesRejected)
}
