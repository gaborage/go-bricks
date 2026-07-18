package messaging

import (
	"context"

	"maps"

	amqp "github.com/rabbitmq/amqp091-go"
)

const tenantHeader = "x-tenant-id"

// tenantAwarePublisher wraps an AMQP client to inject the tenant identifier on publish calls.
type tenantAwarePublisher struct {
	base AMQPClient
	key  string
}

func newTenantAwarePublisher(base AMQPClient, key string) AMQPClient {
	// Avoid wrapping when no tenant key is provided.
	if key == "" {
		return base
	}
	return &tenantAwarePublisher{base: base, key: key}
}

func (t *tenantAwarePublisher) Publish(ctx context.Context, destination string, data []byte) error {
	return t.PublishToExchange(ctx, PublishOptions{Exchange: "", RoutingKey: destination}, data)
}

func (t *tenantAwarePublisher) PublishToExchange(ctx context.Context, options PublishOptions, data []byte) error {
	opts := options
	if opts.Headers == nil {
		opts.Headers = map[string]any{}
	} else {
		opts.Headers = maps.Clone(opts.Headers)
	}
	opts.Headers[tenantHeader] = t.key
	return t.base.PublishToExchange(ctx, opts, data)
}

func (t *tenantAwarePublisher) Consume(ctx context.Context, destination string) (<-chan amqp.Delivery, error) {
	return t.base.Consume(ctx, destination)
}

func (t *tenantAwarePublisher) ConsumeFromQueue(ctx context.Context, options ConsumeOptions) (<-chan amqp.Delivery, error) {
	return t.base.ConsumeFromQueue(ctx, options)
}

func (t *tenantAwarePublisher) DeclareQueue(ctx context.Context, queue *QueueDeclaration) error {
	return t.base.DeclareQueue(ctx, queue)
}

func (t *tenantAwarePublisher) DeclareExchange(ctx context.Context, exchange *ExchangeDeclaration) error {
	return t.base.DeclareExchange(ctx, exchange)
}

func (t *tenantAwarePublisher) BindQueue(ctx context.Context, binding *BindingDeclaration) error {
	return t.base.BindQueue(ctx, binding)
}

func (t *tenantAwarePublisher) Close() error {
	return t.base.Close()
}

func (t *tenantAwarePublisher) IsReady() bool {
	return t.base.IsReady()
}
