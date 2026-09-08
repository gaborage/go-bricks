package messaging

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/publishdoor"
)

// oversizedShortStr is one byte past what an AMQP shortstr can carry. amqp091's
// writeShortstr refuses it, and it answers a frame-write failure by shutting
// down the whole Connection every publisher in the process shares (#1123).
var oversizedShortStr = strings.Repeat("k", 256)

// boundedClient caps the retry loop so a REGRESSION here fails instead of
// hanging: without the guard the oversized destination reaches the fake channel,
// which accepts it and never confirms, and an unbounded client would then retry
// until the package timeout rather than reporting anything.
func boundedClient(t *testing.T, ch *fakeChannel) *AMQPClientImpl {
	t.Helper()
	c := newClientWithFakeChannel(t, ch)
	c.maxPublishAttempts = 2
	c.connectionTimeout = 5 * time.Millisecond
	c.resendDelay = time.Millisecond
	return c
}

// TestPublishBytesRefusesAnOversizedRoutingKey pins the whole point: the
// publish is refused BEFORE the channel is touched, so the shared connection is
// never put at risk and the bounded retry loop never re-tears it.
func TestPublishBytesRefusesAnOversizedRoutingKey(t *testing.T) {
	ch := &fakeChannel{}
	c := boundedClient(t, ch)

	err := c.publishBytes(context.Background(), publishOptions{
		Exchange:   "ex",
		RoutingKey: oversizedShortStr,
	}, []byte(testMessageBody))

	require.Error(t, err)
	assert.Zero(t, atomic.LoadUint64(&ch.publishAttempts), "the channel is never touched")
	assert.ErrorIs(t, err, ErrInvalidPublishDestination) //nolint:testifylint // peer sentinel probe; the retry-exhaustion claim follows
	assert.NotErrorIs(t, err, ErrPublishRetriesExhausted, "this is not a retry outcome")
}

// TestPublishBytesRefusesEveryOversizedShortStr covers the other two fields
// of the frame the caller controls. A header table's keys are shortstrs at every
// depth, so a nested table is judged like the top-level one.
func TestPublishBytesRefusesEveryOversizedShortStr(t *testing.T) {
	tests := []struct {
		name    string
		options publishOptions
		field   string
	}{
		{
			name:    "oversized_exchange",
			options: publishOptions{Exchange: oversizedShortStr, RoutingKey: "rk"},
			field:   "exchange",
		},
		{
			name:    "oversized_header_key",
			options: publishOptions{Exchange: "ex", RoutingKey: "rk", Headers: map[string]any{oversizedShortStr: "v"}},
			field:   "header key",
		},
		{
			name: "oversized_key_in_a_table_inside_an_array",
			options: publishOptions{Exchange: "ex", RoutingKey: "rk", Headers: map[string]any{
				"outer": []any{amqp.Table{oversizedShortStr: "v"}},
			}},
			field: "header key",
		},
		{
			name: "oversized_key_in_a_table_two_arrays_deep",
			options: publishOptions{Exchange: "ex", RoutingKey: "rk", Headers: map[string]any{
				"outer": []any{[]any{map[string]any{oversizedShortStr: "v"}}},
			}},
			field: "header key",
		},
		{
			name: "oversized_key_in_a_nested_table",
			options: publishOptions{Exchange: "ex", RoutingKey: "rk", Headers: map[string]any{
				"outer": amqp.Table{oversizedShortStr: "v"},
			}},
			field: "header key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &fakeChannel{}
			c := boundedClient(t, ch)

			err := c.publishBytes(context.Background(), tt.options, []byte(testMessageBody))

			require.ErrorIs(t, err, ErrInvalidPublishDestination)
			assert.Zero(t, atomic.LoadUint64(&ch.publishAttempts))
			assert.Contains(t, err.Error(), tt.field, "the error names the field")
			assert.Contains(t, err.Error(), "256 bytes", "the error names the size")
			assert.NotContains(t, err.Error(), oversizedShortStr, "the error never carries the value")
		})
	}
}

// TestPublishBytesAcceptsTheBoundaryAndEmptyDestinations pins what must
// keep working: 255 bytes is the limit, not one below it, and an empty exchange
// (the default exchange) or routing key is legal AMQP.
func TestPublishBytesAcceptsTheBoundaryAndEmptyDestinations(t *testing.T) {
	tests := []struct {
		name    string
		options publishOptions
	}{
		{name: "routing_key_at_the_limit", options: publishOptions{Exchange: "ex", RoutingKey: strings.Repeat("k", 255)}},
		{name: "empty_exchange_and_routing_key", options: publishOptions{}},
		{name: "header_key_at_the_limit", options: publishOptions{Headers: map[string]any{strings.Repeat("h", 255): "v"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &fakeChannel{}
			c := newClientWithFakeChannel(t, ch)
			sendConfirmsAfterEachAttempt(t, c, ch, amqp.Confirmation{Ack: true, DeliveryTag: 1})

			err := c.publishBytes(context.Background(), tt.options, []byte(testMessageBody))

			require.NoError(t, err)
			assert.Equal(t, uint64(1), atomic.LoadUint64(&ch.publishAttempts), "the publish reached the channel")
		})
	}
}

// TestPublishRefusesAnOversizedDestination proves the queue-name door is guarded
// too: Publish forwards its destination as the routing key.
func TestPublishRefusesAnOversizedDestination(t *testing.T) {
	ch := &fakeChannel{}
	c := boundedClient(t, ch)

	err := c.publishBytes(context.Background(), publishOptions{RoutingKey: oversizedShortStr}, []byte(testMessageBody))

	require.ErrorIs(t, err, ErrInvalidPublishDestination)
	assert.Zero(t, atomic.LoadUint64(&ch.publishAttempts))
}

// TestDeclarationsValidateRefusesAnOversizedName moves the same rule to startup:
// a declared name the frame cannot carry is a deployment defect, and failing the
// boot is strictly better than failing the first publish — the publish path is
// where an unwritable frame costs the shared connection.
func TestDeclarationsValidateRefusesAnOversizedName(t *testing.T) {
	tests := []struct {
		name  string
		build func(*Declarations)
		field string
	}{
		{
			name:  "exchange_name",
			build: func(d *Declarations) { d.Exchanges[oversizedShortStr] = &ExchangeDeclaration{Name: oversizedShortStr} },
			field: "declared exchange name",
		},
		{
			name:  "queue_name",
			build: func(d *Declarations) { d.Queues[oversizedShortStr] = &QueueDeclaration{Name: oversizedShortStr} },
			field: "declared queue name",
		},
		{
			name: "binding_routing_key",
			build: func(d *Declarations) {
				d.Bindings = append(d.Bindings, &BindingDeclaration{Queue: "q", Exchange: "ex", RoutingKey: oversizedShortStr})
			},
			field: "binding routing key",
		},
		{
			name: "publisher_routing_key",
			build: func(d *Declarations) {
				d.Publishers = append(d.Publishers, &PublisherDeclaration{Exchange: "ex", RoutingKey: oversizedShortStr})
			},
			field: "publisher routing key",
		},
		{
			name: "publisher_event_type",
			build: func(d *Declarations) {
				d.Publishers = append(d.Publishers, &PublisherDeclaration{Exchange: "ex", EventType: oversizedShortStr})
			},
			field: "publisher event type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDeclarations()
			d.Exchanges["ex"] = &ExchangeDeclaration{Name: "ex", Type: "topic"}
			d.Queues["q"] = &QueueDeclaration{Name: "q"}
			tt.build(d)

			err := d.Validate()

			require.ErrorIs(t, err, ErrInvalidPublishDestination)
			assert.Contains(t, err.Error(), tt.field)
			assert.Contains(t, err.Error(), "256 bytes")
		})
	}
}

// TestDeclarationsValidateAcceptsNamesAtTheLimit keeps the boundary honest: 255
// bytes is legal AMQP and must still boot.
func TestDeclarationsValidateAcceptsNamesAtTheLimit(t *testing.T) {
	limit := strings.Repeat("e", 255)
	d := NewDeclarations()
	d.Exchanges[limit] = &ExchangeDeclaration{Name: limit, Type: "topic"}
	d.Queues["q"] = &QueueDeclaration{Name: "q"}
	d.Bindings = append(d.Bindings, &BindingDeclaration{Queue: "q", Exchange: limit, RoutingKey: strings.Repeat("r", 255)})
	d.Publishers = append(d.Publishers, &PublisherDeclaration{
		Exchange:   limit,
		RoutingKey: strings.Repeat("r", 255),
		EventType:  strings.Repeat("t", 255),
	})

	assert.NoError(t, d.Validate())
}

// TestDeclarationsValidateReportsEveryOversizedName pins the aggregation the
// file's other startup checks already promise: an operator with two over-long
// names fixes both in one deploy instead of discovering the second after
// redeploying.
func TestDeclarationsValidateReportsEveryOversizedName(t *testing.T) {
	otherOversized := strings.Repeat("q", 300)
	d := NewDeclarations()
	d.Exchanges[oversizedShortStr] = &ExchangeDeclaration{Name: oversizedShortStr, Type: "topic"}
	d.Queues[otherOversized] = &QueueDeclaration{Name: otherOversized}

	err := d.Validate()

	require.ErrorIs(t, err, ErrInvalidPublishDestination)
	assert.Contains(t, err.Error(), "declared exchange name")
	assert.Contains(t, err.Error(), "declared queue name")
	assert.Contains(t, err.Error(), "256 bytes")
	assert.Contains(t, err.Error(), "300 bytes")
}

// TestDeclarationsValidateRefusesAnOversizedArgsKey covers the table keys on the
// declaration kinds, which reach the declare frame as shortstrs exactly as the
// names beside them do.
func TestDeclarationsValidateRefusesAnOversizedArgsKey(t *testing.T) {
	d := NewDeclarations()
	d.Exchanges["ex"] = &ExchangeDeclaration{Name: "ex", Type: "topic"}
	d.Queues["q"] = &QueueDeclaration{Name: "q", Args: map[string]any{oversizedShortStr: "v"}}

	err := d.Validate()

	require.ErrorIs(t, err, ErrInvalidPublishDestination)
	assert.Contains(t, err.Error(), "declared queue argument key",
		"a declaration's Args are not a message's headers, and the error says which it read")
}

// TestDeclarationsValidateRefusesAnOversizedExchangeType covers the field beside
// the exchange name: DeclareExchange forwards Type to channel.ExchangeDeclare,
// where the exchange.declare frame writes it with writeShortstr exactly as it
// writes the name (spec091.go). A 256-byte type is the same connection teardown
// as a 256-byte name.
func TestDeclarationsValidateRefusesAnOversizedExchangeType(t *testing.T) {
	d := NewDeclarations()
	d.Exchanges["ex"] = &ExchangeDeclaration{Name: "ex", Type: oversizedShortStr}
	d.Queues["q"] = &QueueDeclaration{Name: "q"}

	err := d.Validate()

	require.ErrorIs(t, err, ErrInvalidPublishDestination)
	assert.Contains(t, err.Error(), "declared exchange type")
	assert.Contains(t, err.Error(), "256 bytes")
	assert.NotContains(t, err.Error(), oversizedShortStr)
}

// TestDeclarationsValidateReportsANilDeclaration keeps a registration mistake
// from arriving as a nil-map-value panic. Declarations.Exchanges and .Queues are
// exported maps, so a module can put a nil in one; the key that carries it is
// the only thing identifying it, so the error names the key.
func TestDeclarationsValidateReportsANilDeclaration(t *testing.T) {
	tests := []struct {
		name  string
		build func(*Declarations)
		want  string
	}{
		{
			name:  "nil_exchange_entry",
			build: func(d *Declarations) { d.Exchanges["ghost-exchange"] = nil },
			want:  `exchange "ghost-exchange"`,
		},
		{
			name:  "nil_queue_entry",
			build: func(d *Declarations) { d.Queues["ghost-queue"] = nil },
			want:  `queue "ghost-queue"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDeclarations()
			d.Exchanges["ex"] = &ExchangeDeclaration{Name: "ex", Type: "topic"}
			d.Queues["q"] = &QueueDeclaration{Name: "q"}
			tt.build(d)

			var err error
			require.NotPanics(t, func() { err = d.Validate() })

			require.ErrorIs(t, err, errNilDeclaration)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestValidatePublishDestination covers the exported door on its own: it is the rule
// the publish doors run, offered to callers (the outbox) that persist a destination
// now and publish it later, so they can refuse it at its source instead of restating
// the ceiling.
func TestValidatePublishDestination(t *testing.T) {
	maxLengthShortStr := strings.Repeat("k", maxShortStrBytes)

	tests := []struct {
		name    string
		options publishOptions
		field   string
	}{
		{name: "empty_is_legal", options: publishOptions{}},
		{
			name:    "max_length_fields_are_legal",
			options: publishOptions{Exchange: maxLengthShortStr, RoutingKey: maxLengthShortStr, Headers: map[string]any{maxLengthShortStr: "v"}},
		},
		{name: "oversized_exchange", options: publishOptions{Exchange: oversizedShortStr}, field: "exchange"},
		{name: "oversized_routing_key", options: publishOptions{RoutingKey: oversizedShortStr}, field: "routing key"},
		{
			name:    "oversized_nested_header_key",
			options: publishOptions{Headers: map[string]any{"outer": map[string]any{oversizedShortStr: "v"}}},
			field:   "header key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePublishDestination(tt.options.Exchange, tt.options.RoutingKey, tt.options.Headers)
			if tt.field == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.field)
			assert.Contains(t, err.Error(), "256 bytes")
			assert.NotContains(t, err.Error(), oversizedShortStr, "the error reports the length, never the value")
			require.ErrorIs(t, err, ErrInvalidPublishDestination)
		})
	}
}

// TestValidatePublishEventType covers the second exported door: the same ceiling on
// the `type` property, offered to a caller that records an event type now and ships it
// later. The multibyte case is the one the outbox ledger lets through — its column
// bounds 255 CHARACTERS, and 128 two-byte runes fit that while overrunning the frame.
func TestValidatePublishEventType(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		wantBytes string
	}{
		{name: "empty_is_legal"},
		{name: "at_the_limit", eventType: strings.Repeat("k", maxShortStrBytes)},
		{name: "one_byte_past_the_limit", eventType: oversizedShortStr, wantBytes: "256 bytes"},
		{
			name:      "multibyte_under_255_characters_but_over_255_bytes",
			eventType: strings.Repeat("\u00f1", 128),
			wantBytes: "256 bytes",
		},
		{
			name:      "multibyte_at_the_limit_in_bytes",
			eventType: strings.Repeat("\u00f1", 127) + "k",
			wantBytes: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePublishEventType(tt.eventType)
			if tt.wantBytes == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidPublishDestination)
			assert.Contains(t, err.Error(), "event type")
			assert.Contains(t, err.Error(), tt.wantBytes)
			assert.NotContains(t, err.Error(), tt.eventType, "the error reports the size, never the value")
		})
	}
}

// TestPublishBytesRefusesEveryOversizedMessageProp covers the three properties
// the framework writes onto the content-header frame (ADR-105). Each is a
// shortstr on the same frame as the header keys beside it, so each would tear
// down the shared connection — the publish must be refused before the channel
// is touched, and the error must name the field and its size without carrying
// the value into logs, spans and the outbox row.
func TestPublishBytesRefusesEveryOversizedMessageProp(t *testing.T) {
	tests := []struct {
		name  string
		props publishdoor.MessageProps
		field string
	}{
		{name: "oversized_content_type", props: publishdoor.MessageProps{ContentType: oversizedShortStr}, field: "content type"},
		{name: "oversized_event_type", props: publishdoor.MessageProps{EventType: oversizedShortStr}, field: "event type"},
		{name: "oversized_message_id", props: publishdoor.MessageProps{MessageID: oversizedShortStr}, field: "message id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &fakeChannel{}
			c := boundedClient(t, ch)
			props := tt.props

			err := c.publishBytes(context.Background(), publishOptions{
				Exchange:   "ex",
				RoutingKey: "rk",
				props:      &props,
			}, []byte(testMessageBody))

			require.NotErrorIs(t, err, ErrPublishRetriesExhausted, "this is not a retry outcome")
			require.ErrorIs(t, err, ErrInvalidPublishDestination)
			assert.Zero(t, atomic.LoadUint64(&ch.publishAttempts), "the channel is never touched")
			assert.Contains(t, err.Error(), tt.field, "the error names the field")
			assert.Contains(t, err.Error(), "256 bytes", "the error names the size")
			assert.NotContains(t, err.Error(), oversizedShortStr, "the error never carries the value")
		})
	}
}

// TestPublishBytesAcceptsMessagePropsAtTheBoundary keeps both sides honest: 255
// bytes is legal on every property, and nil props — the framework doors that
// know nothing about the payload — must stay valid.
func TestPublishBytesAcceptsMessagePropsAtTheBoundary(t *testing.T) {
	limit := strings.Repeat("p", maxShortStrBytes)

	tests := []struct {
		name  string
		props *publishdoor.MessageProps
	}{
		{name: "nil_props"},
		{
			name: "every_prop_at_the_limit",
			props: &publishdoor.MessageProps{
				ContentType: limit,
				EventType:   limit,
				MessageID:   limit,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &fakeChannel{}
			c := newClientWithFakeChannel(t, ch)
			sendConfirmsAfterEachAttempt(t, c, ch, amqp.Confirmation{Ack: true, DeliveryTag: 1})

			err := c.publishBytes(context.Background(), publishOptions{
				Exchange:   "ex",
				RoutingKey: "rk",
				props:      tt.props,
			}, []byte(testMessageBody))

			require.NoError(t, err)
			assert.Equal(t, uint64(1), atomic.LoadUint64(&ch.publishAttempts), "the publish reached the channel")
		})
	}
}

// TestPublishBytesRefusesAnOversizedAppID pins the half config cannot reach:
// app.name is bounded at startup, but WithAppName is exported, so a client a
// consumer built itself can carry a value no config check ever saw. It must
// fail the pre-flight rather than the frame write, which would tear down the
// connection every publisher in the process shares (ADR-070).
func TestPublishBytesRefusesAnOversizedAppID(t *testing.T) {
	ch := &fakeChannel{}
	c := boundedClient(t, ch)
	c.appID = oversizedShortStr

	err := c.publishBytes(context.Background(), publishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte(testMessageBody))

	require.NotErrorIs(t, err, ErrPublishRetriesExhausted, "this is not a retry outcome")
	require.ErrorIs(t, err, ErrInvalidPublishDestination)
	assert.Zero(t, atomic.LoadUint64(&ch.publishAttempts), "the channel is never touched")
	assert.Contains(t, err.Error(), "app id is 256 bytes")
	assert.NotContains(t, err.Error(), oversizedShortStr, "the error never carries the value")
}
