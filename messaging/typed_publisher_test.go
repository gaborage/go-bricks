package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	typedPubExchange   = "orders.events"
	typedPubRoutingKey = "order.created"
)

type orderCreated struct {
	OrderID int64 `json:"order_id"`
}

// unmarshalableEvent fails its own MarshalJSON so the marshal error path is
// reachable with a plain value.
type unmarshalableEvent struct{}

var errMarshalRefused = errors.New("marshal refused")

func (unmarshalableEvent) MarshalJSON() ([]byte, error) { return nil, errMarshalRefused }

// capturingPublishClient records every frame handed to publishBytes so a
// test can assert the destination the handle chose, not the one a caller
// might have re-spelled.
type capturingPublishClient struct {
	stubAMQPClient
	mu     sync.Mutex
	frames []capturedFrame
	err    error
}

type capturedFrame struct {
	options publishOptions
	data    []byte
}

func (c *capturingPublishClient) publishBytes(_ context.Context, options publishOptions, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, capturedFrame{options: options, data: data})
	return c.err
}

func (c *capturingPublishClient) captured() []capturedFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedFrame(nil), c.frames...)
}

func declaredOrderPublisher(t *testing.T) *Publisher[orderCreated] {
	t.Helper()
	return DeclareTypedPublisher[orderCreated](NewDeclarations(), &PublisherOptions{
		Exchange:   typedPubExchange,
		RoutingKey: typedPubRoutingKey,
		EventType:  "OrderCreated",
		Headers:    map[string]any{"event_type": "OrderCreated", "schema": "v2"},
		Mandatory:  true,
		Immediate:  true,
	})
}

func TestDeclareTypedPublisherRegistersLikeDeclarePublisher(t *testing.T) {
	opts := func() *PublisherOptions {
		return &PublisherOptions{
			Exchange:    typedPubExchange,
			RoutingKey:  typedPubRoutingKey,
			EventType:   "OrderCreated",
			Description: "orders",
			Headers:     map[string]any{"event_type": "OrderCreated"},
			Mandatory:   true,
		}
	}

	typed := NewDeclarations()
	typed.DeclareTopicExchange(typedPubExchange)
	pub := DeclareTypedPublisher[orderCreated](typed, opts())
	require.NotNil(t, pub)

	raw := NewDeclarations()
	raw.DeclareTopicExchange(typedPubExchange)
	raw.DeclarePublisher(opts(), nil)

	require.Len(t, typed.Publishers, 1)
	assert.Equal(t, raw.Publishers, typed.Publishers, "same registry entry as DeclarePublisher")
	assert.Equal(t, raw.Hash(), typed.Hash(), "same replay/validate/hash path")
	assert.NoError(t, typed.Validate())
}

func TestDeclareTypedPublisherDuplicateMatchesDeclarePublisher(t *testing.T) {
	opts := &PublisherOptions{Exchange: typedPubExchange, RoutingKey: typedPubRoutingKey, EventType: "OrderCreated"}

	raw := NewDeclarations()
	raw.DeclarePublisher(opts, nil)
	raw.DeclarePublisher(opts, nil)

	typed := NewDeclarations()
	require.NotPanics(t, func() {
		DeclareTypedPublisher[orderCreated](typed, opts)
		DeclareTypedPublisher[orderCreated](typed, opts)
	})

	assert.Len(t, typed.Publishers, len(raw.Publishers), "duplicate declaration lands like DeclarePublisher's")
}

func TestDeclareTypedPublisherPanicsOnWiringMistakes(t *testing.T) {
	assert.PanicsWithValue(t, "messaging: DeclareTypedPublisher requires a non-nil *Declarations",
		func() { DeclareTypedPublisher[orderCreated](nil, &PublisherOptions{}) })

	assert.PanicsWithValue(t, "messaging: DeclareTypedPublisher requires non-nil *PublisherOptions",
		func() { DeclareTypedPublisher[orderCreated](NewDeclarations(), nil) })
}

func TestPublisherPublishUsesDeclaredDestination(t *testing.T) {
	pub := declaredOrderPublisher(t)
	client := &capturingPublishClient{}

	// A stale re-spelling a caller might still hold: the handle must ignore it
	// entirely, since Publish never takes publishOptions at all.
	stale := publishOptions{Exchange: "legacy.exchange", RoutingKey: "legacy.key", Headers: map[string]any{"schema": "v1"}}

	require.NoError(t, pub.Publish(t.Context(), client, orderCreated{OrderID: 42}))

	frames := client.captured()
	require.Len(t, frames, 1)
	got := frames[0].options
	assert.NotEqual(t, stale.Exchange, got.Exchange)
	assert.Equal(t, typedPubExchange, got.Exchange)
	assert.Equal(t, typedPubRoutingKey, got.RoutingKey)
	assert.Equal(t, "OrderCreated", got.Headers["event_type"])
	assert.Equal(t, "v2", got.Headers["schema"])
	assert.Len(t, got.Headers, 2)
	assert.True(t, got.Mandatory)
	assert.True(t, got.Immediate)

	var evt orderCreated
	require.NoError(t, json.Unmarshal(frames[0].data, &evt))
	assert.Equal(t, int64(42), evt.OrderID)
}

func TestPublisherPublishHandsOutFreshHeadersPerCall(t *testing.T) {
	pub := declaredOrderPublisher(t)
	client := &capturingPublishClient{}

	require.NoError(t, pub.Publish(t.Context(), client, orderCreated{OrderID: 1}))
	first := client.captured()[0].options.Headers
	first["x-tenant-id"] = "acme" // what a stamping layer downstream does to its copy
	first["schema"] = "v9"

	require.NoError(t, pub.Publish(t.Context(), client, orderCreated{OrderID: 2}))
	second := client.captured()[1].options.Headers

	assert.NotContains(t, second, "x-tenant-id", "a downstream write must not reach the handle")
	assert.Equal(t, "v2", second["schema"])
	assert.Equal(t, "v2", pub.headers["schema"])
}

func TestPublisherPublishIgnoresLaterOptionsMutation(t *testing.T) {
	decls := NewDeclarations()
	opts := &PublisherOptions{Exchange: typedPubExchange, RoutingKey: typedPubRoutingKey, Headers: map[string]any{"schema": "v2"}}
	pub := DeclareTypedPublisher[orderCreated](decls, opts)

	opts.Exchange = "mutated"
	opts.Headers["schema"] = "mutated"

	client := &capturingPublishClient{}
	require.NoError(t, pub.Publish(t.Context(), client, orderCreated{}))

	got := client.captured()[0].options
	assert.Equal(t, typedPubExchange, got.Exchange)
	assert.Equal(t, "v2", got.Headers["schema"])
}

func TestPublisherPublishMarshalErrorPublishesNothing(t *testing.T) {
	decls := NewDeclarations()
	pub := DeclareTypedPublisher[unmarshalableEvent](decls, &PublisherOptions{Exchange: "x", RoutingKey: "k", EventType: "Broken"})
	client := &capturingPublishClient{}

	err := pub.Publish(t.Context(), client, unmarshalableEvent{})

	require.ErrorIs(t, err, errMarshalRefused)
	assert.Contains(t, err.Error(), "marshal Broken event")
	assert.Empty(t, client.captured(), "nothing reaches the client on a marshal failure")
}

func TestPublisherPublishReturnsClientErrorUnwrapped(t *testing.T) {
	pub := declaredOrderPublisher(t)
	client := &capturingPublishClient{err: ErrInvalidPublishDestination}

	err := pub.Publish(t.Context(), client, orderCreated{})

	require.ErrorIs(t, err, ErrInvalidPublishDestination)
	assert.Equal(t, ErrInvalidPublishDestination, err)
}

func TestPublisherPublishConcurrent(t *testing.T) {
	pub := declaredOrderPublisher(t)
	client := &capturingPublishClient{}

	const publishes = 64
	var wg sync.WaitGroup
	for i := range publishes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := pub.Publish(t.Context(), client, orderCreated{OrderID: int64(i)}); err != nil {
				t.Errorf("publish %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	frames := client.captured()
	require.Len(t, frames, publishes)
	for _, frame := range frames {
		assert.Equal(t, typedPubExchange, frame.options.Exchange)
		assert.Equal(t, typedPubRoutingKey, frame.options.RoutingKey)
	}
}
