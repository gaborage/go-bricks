package multitenant

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/messaging"
)

// TenantAMQPClient wraps an AMQP client to inject tenant headers on publish.
type TenantAMQPClient struct {
	base     messaging.AMQPClient
	tenantID string
}

func NewTenantAMQPClient(base messaging.AMQPClient, tenantID string) *TenantAMQPClient {
	return &TenantAMQPClient{base: base, tenantID: tenantID}
}

// Publish injects tenant_id and uses default exchange semantics via PublishToExchange.
func (t *TenantAMQPClient) Publish(ctx context.Context, destination string, data []byte) error {
	return t.PublishToExchange(ctx, messaging.PublishOptions{Exchange: "", RoutingKey: destination}, data)
}

// PublishToExchange injects tenant_id header and delegates to base client.
func (t *TenantAMQPClient) PublishToExchange(ctx context.Context, options messaging.PublishOptions, data []byte) error {
	if options.Headers == nil {
		options.Headers = map[string]any{}
	}
	options.Headers["tenant_id"] = t.tenantID
	return t.base.PublishToExchange(ctx, options, data)
}

// Consume delegates to base client.
func (t *TenantAMQPClient) Consume(ctx context.Context, destination string) (<-chan amqp.Delivery, error) {
	return t.base.Consume(ctx, destination)
}

// ConsumeFromQueue delegates to base client.
func (t *TenantAMQPClient) ConsumeFromQueue(ctx context.Context, options messaging.ConsumeOptions) (<-chan amqp.Delivery, error) {
	return t.base.ConsumeFromQueue(ctx, options)
}

// DeclareQueue delegates to base client.
func (t *TenantAMQPClient) DeclareQueue(name string, durable, autoDelete, exclusive, noWait bool) error {
	return t.base.DeclareQueue(name, durable, autoDelete, exclusive, noWait)
}

// DeclareExchange delegates to base client.
func (t *TenantAMQPClient) DeclareExchange(name, kind string, durable, autoDelete, internal, noWait bool) error {
	return t.base.DeclareExchange(name, kind, durable, autoDelete, internal, noWait)
}

// BindQueue delegates to base client.
func (t *TenantAMQPClient) BindQueue(queue, exchange, routingKey string, noWait bool) error {
	return t.base.BindQueue(queue, exchange, routingKey, noWait)
}

// Close delegates to base client.
func (t *TenantAMQPClient) Close() error { return t.base.Close() }

// IsReady delegates to base client.
func (t *TenantAMQPClient) IsReady() bool { return t.base.IsReady() }
