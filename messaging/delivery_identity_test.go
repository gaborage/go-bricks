package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"

	"github.com/gaborage/go-bricks/messaging/internal/tracking"
)

const poisonedID = "corr\r\ninjected=1"

func TestValidateRoutingKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "dotted_topic_key", key: "user.created", want: "user.created"},
		{name: "topic_wildcards", key: "user.*.created.#", want: "user.*.created.#"},
		{name: "default_exchange_queue_name", key: "test-queue", want: "test-queue"},
		{name: "space_is_printable_and_kept", key: "two words", want: "two words"},
		{name: "empty", key: ""},
		{name: "carriage_return_line_feed", key: "user.created\r\ninjected=1"},
		{name: "bare_newline", key: "user.\ncreated"},
		{name: "nul_byte", key: "user.\x00created"},
		{name: "ansi_escape", key: "user.\x1b[31mcreated"},
		{name: "non_ascii", key: "user.créated"},
		{name: "tab", key: "user\tcreated"},
		{name: "del", key: "user.\x7fcreated"},
		{name: "invalid_utf8", key: "user.\xffcreated"},
		{name: "at_the_shortstr_ceiling", key: strings.Repeat("k", 255), want: strings.Repeat("k", 255)},
		{name: "one_over_the_shortstr_ceiling", key: strings.Repeat("k", 256)},
		// The bound must be BYTES, not runes: 200 two-byte runes is 400 bytes, which
		// would overflow the shortstr the length half of the rule exists to respect.
		// [[:print:]] is ASCII-only, so every match is one byte and {1,255} bounds
		// both — this pins that, since a rune-counting class would not.
		{name: "multibyte_runes_exceeding_the_byte_bound", key: strings.Repeat("é", 200)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, validateRoutingKey(tt.key))
		})
	}
}

func TestIdentifyDeliveryVouchesForEachIdentifierIndependently(t *testing.T) {
	tests := []struct {
		name     string
		delivery amqp.Delivery
		want     deliveryIdentity
	}{
		{
			name:     "all_four_valid",
			delivery: amqp.Delivery{CorrelationId: "corr-1", MessageId: "msg-1", RoutingKey: "user.created", Exchange: testExchangeName},
			want:     deliveryIdentity{correlationID: "corr-1", messageID: "msg-1", routingKey: "user.created", exchange: testExchangeName},
		},
		{
			name:     "an_injected_exchange_does_not_take_the_others_down",
			delivery: amqp.Delivery{CorrelationId: "corr-1", MessageId: "msg-1", RoutingKey: "user.created", Exchange: "ex\nchange"},
			want:     deliveryIdentity{correlationID: "corr-1", messageID: "msg-1", routingKey: "user.created", rejected: true},
		},
		{
			name: "an_absent_field_is_not_a_rejected_one",
			// The marker must separate "the delivery carried none" from "the delivery
			// carried one and we refused it" — otherwise it fires on every message a
			// publisher sends without a correlation id.
			delivery: amqp.Delivery{RoutingKey: "user.created"},
			want:     deliveryIdentity{routingKey: "user.created"},
		},
		{
			name:     "an_invalid_correlation_id_does_not_take_the_others_down",
			delivery: amqp.Delivery{CorrelationId: poisonedID, MessageId: "msg-1", RoutingKey: "user.created"},
			want:     deliveryIdentity{messageID: "msg-1", routingKey: "user.created", rejected: true},
		},
		{
			name:     "an_invalid_message_id_does_not_take_the_others_down",
			delivery: amqp.Delivery{CorrelationId: "corr-1", MessageId: poisonedID, RoutingKey: "user.created"},
			want:     deliveryIdentity{correlationID: "corr-1", routingKey: "user.created", rejected: true},
		},
		{
			name:     "an_invalid_routing_key_does_not_take_the_others_down",
			delivery: amqp.Delivery{CorrelationId: "corr-1", MessageId: "msg-1", RoutingKey: "user.\ncreated"},
			want:     deliveryIdentity{correlationID: "corr-1", messageID: "msg-1", rejected: true},
		},
		{
			name: "the_request_id_charset_governs_the_two_identifiers_but_not_the_routing_key",
			// A dotted value is a legal routing key and an illegal request id: the two
			// answer to different rules on purpose.
			delivery: amqp.Delivery{CorrelationId: "corr.1", MessageId: "msg.1", RoutingKey: "user.created"},
			want:     deliveryIdentity{routingKey: "user.created", rejected: true},
		},
		{
			name:     "an_over_length_correlation_id_is_discarded_not_truncated",
			delivery: amqp.Delivery{CorrelationId: strings.Repeat("c", 129)},
			want:     deliveryIdentity{rejected: true},
		},
		{name: "an_empty_delivery_vouches_for_nothing", delivery: amqp.Delivery{}, want: deliveryIdentity{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, identifyDelivery(&tt.delivery))
		})
	}
}

// TestProcessMessageSinksNeverEmitAnUnvouchedIdentifier pins the consume-side
// half of ADR-070 across BOTH seams a delivery's AMQP properties reach — the
// eager span attributes and the lazy log lines — from a single processMessage
// run per case. One computed verdict feeds both, so a case that passes here
// cannot pass on one seam and fail on the other.
func TestProcessMessageSinksNeverEmitAnUnvouchedIdentifier(t *testing.T) {
	tests := []struct {
		name            string
		correlationID   string
		messageID       string
		routingKey      string
		exchange        string
		wantCorrelation string
		wantMessageID   string
		wantRoutingKey  string
		wantExchange    string
		wantRejected    bool
	}{
		{
			name:            "valid_identifiers_reach_every_sink_unchanged",
			correlationID:   "corr-1",
			messageID:       testMessageID,
			routingKey:      testRoutingKey,
			exchange:        testExchangeName,
			wantCorrelation: "corr-1",
			wantMessageID:   testMessageID,
			wantRoutingKey:  testRoutingKey,
			wantExchange:    testExchangeName,
		},
		{
			name:           "an_injected_correlation_id_is_omitted_everywhere",
			correlationID:  poisonedID,
			messageID:      testMessageID,
			routingKey:     testRoutingKey,
			exchange:       testExchangeName,
			wantMessageID:  testMessageID,
			wantRoutingKey: testRoutingKey,
			wantExchange:   testExchangeName,
			wantRejected:   true,
		},
		{
			name:            "an_injected_message_id_is_omitted_everywhere",
			correlationID:   "corr-1",
			messageID:       poisonedID,
			routingKey:      testRoutingKey,
			exchange:        testExchangeName,
			wantCorrelation: "corr-1",
			wantRoutingKey:  testRoutingKey,
			wantExchange:    testExchangeName,
			wantRejected:    true,
		},
		{
			name:            "an_injected_routing_key_is_omitted_everywhere",
			correlationID:   "corr-1",
			messageID:       testMessageID,
			routingKey:      "user.\ncreated",
			exchange:        testExchangeName,
			wantCorrelation: "corr-1",
			wantMessageID:   testMessageID,
			wantExchange:    testExchangeName,
			wantRejected:    true,
		},
		{
			name:            "an_injected_exchange_is_omitted_everywhere",
			correlationID:   "corr-1",
			messageID:       testMessageID,
			routingKey:      testRoutingKey,
			exchange:        "ex\nchange",
			wantCorrelation: "corr-1",
			wantMessageID:   testMessageID,
			wantRoutingKey:  testRoutingKey,
			wantRejected:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter, cleanup := setupTestTracing(t)
			defer cleanup()

			registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
			consumer := &ConsumerDeclaration{
				Queue:     testQueueName,
				EventType: testEventType,
				Handler:   &testHandler{retErr: errors.New("handler said no")},
			}
			delivery := &amqp.Delivery{
				CorrelationId: tt.correlationID,
				MessageId:     tt.messageID,
				RoutingKey:    tt.routingKey,
				Exchange:      tt.exchange,
				DeliveryTag:   7,
				Body:          []byte(testMessageBody),
				Headers:       amqp.Table{},
				Acknowledger:  &mockAcknowledger{},
			}

			rec := newRecordingLogger()
			registry.processMessage(context.Background(), consumer, delivery, rec)

			// Seam 1: the consume span's attributes.
			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			attrs := spans[0].Attributes
			assertOptionalAttribute(t, attrs, string(semconv.MessagingMessageConversationIDKey), tt.wantCorrelation)
			assertOptionalAttribute(t, attrs, string(semconv.MessagingMessageIDKey), tt.wantMessageID)
			assertOptionalAttribute(t, attrs, string(semconv.MessagingRabbitMQDestinationRoutingKeyKey), tt.wantRoutingKey)
			assertOptionalAttribute(t, attrs, attrMessagingRabbitMQExchange, tt.wantExchange)

			// Seam 2: the failure line's fields.
			failure := rec.Line(t, "Message processing failed - discarding without requeue")
			assertOptionalField(t, failure, "amqp_correlation_id", tt.wantCorrelation)
			assertOptionalField(t, failure, "message_id", tt.wantMessageID)
			assertOptionalField(t, failure, "routing_key", tt.wantRoutingKey)
			assertOptionalField(t, failure, "exchange", tt.wantExchange)
			// Omitting a value must not also erase WHICH delivery failed, and the
			// operator needs something to search for where the value used to be.
			assert.Equal(t, []string{"7"}, failure.Values("delivery_tag"),
				"the failure line must stay attributable to one delivery")
			assertRejectionMarker(t, failure, tt.wantRejected)

			// The DEBUG line reads the same verdict — `message_id` has the wider blast
			// radius of the three, so it is pinned wherever it lands.
			processing := rec.Line(t, "Processing message")
			assertOptionalField(t, processing, "message_id", tt.wantMessageID)
			assertOptionalField(t, processing, "routing_key", tt.wantRoutingKey)
			assertRejectionMarker(t, processing, tt.wantRejected)

			// The unvouched bytes never appear under ANY key, on any line.
			for _, ln := range rec.Lines() {
				for _, pair := range ln.Pairs {
					assert.NotContains(t, pair[1], "\n", "a log field carries a raw newline: %q=%q", pair[0], pair[1])
				}
			}
		})
	}
}

// TestProcessMessageSuccessLineOmitsAnUnvouchedMessageID covers the one sink the
// failure path cannot reach: `message_id` on the success line.
func TestProcessMessageSuccessLineOmitsAnUnvouchedMessageID(t *testing.T) {
	for _, tt := range []struct {
		name      string
		messageID string
		want      string
	}{
		{name: "valid", messageID: testMessageID, want: testMessageID},
		{name: "injected", messageID: poisonedID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
			consumer := &ConsumerDeclaration{
				Queue:     testQueueName,
				EventType: testEventType,
				Handler:   &countingTestHandler{},
			}
			delivery := &amqp.Delivery{
				MessageId:    tt.messageID,
				RoutingKey:   testRoutingKey,
				Exchange:     testExchangeName,
				Body:         []byte(testMessageBody),
				Headers:      amqp.Table{},
				Acknowledger: &mockAcknowledger{},
			}

			rec := newRecordingLogger()
			registry.processMessage(context.Background(), consumer, delivery, rec)

			assertOptionalField(t, rec.Line(t, "Message processed successfully"), "message_id", tt.want)
		})
	}
}

// TestConsumeMetricsDropAnUnvouchedRoutingKey pins the third reader of the
// routing key in processMessage. It is the sink with the least forgiving failure
// mode: a metric attribute takes the value into a time series, so an unbounded
// one is a cardinality problem rather than one bad log line.
func TestConsumeMetricsDropAnUnvouchedRoutingKey(t *testing.T) {
	assert.Equal(t,
		tracking.AMQPConsumeAttributes(testExchangeName, testRoutingKey, testQueueName),
		consumeMetrics(testQueueName, identify("", "", testRoutingKey, testExchangeName)))

	assert.Equal(t,
		tracking.AMQPConsumeAttributes(testExchangeName, "", testQueueName),
		consumeMetrics(testQueueName, identify("", "", "user.\ncreated", testExchangeName)),
		"an unvouched routing key must reach neither the routing-key attribute nor the destination name")

	assert.Equal(t,
		tracking.AMQPConsumeAttributes("", testRoutingKey, testQueueName),
		consumeMetrics(testQueueName, identify("", "", testRoutingKey, "ex\nchange")),
		"an unvouched exchange must reach neither the exchange attribute nor the destination name")
}

// assertOptionalAttribute asserts that key carries want, or is absent when want
// is empty — the span's own rule for a field the delivery did not carry. The
// present branch defers to the package's assertAttribute.
func assertOptionalAttribute(t *testing.T, attrs []attribute.KeyValue, key, want string) {
	t.Helper()
	if want != "" {
		assertAttribute(t, attrs, key, want)
		return
	}
	for _, attr := range attrs {
		assert.NotEqual(t, key, string(attr.Key),
			"attribute %q should have been omitted, got %q", key, attr.Value.AsString())
	}
}

// assertOptionalField asserts that key is stamped exactly once with want, or not
// stamped at all when want is empty.
func assertOptionalField(t *testing.T, line recordedLine, key, want string) {
	t.Helper()
	got := line.Values(key)
	if want == "" {
		assert.Empty(t, got, "field %q should have been omitted from %q", key, line.Msg)
		return
	}
	assert.Equal(t, []string{want}, got, "field %q on %q", key, line.Msg)
}

// assertRejectionMarker asserts identity_rejected is stamped exactly when the
// delivery carried a value validation refused, and absent otherwise — a marker
// that fired on every delivery would be no signal at all.
func assertRejectionMarker(t *testing.T, line recordedLine, want bool) {
	t.Helper()
	got := line.Values("identity_rejected")
	if !want {
		assert.Empty(t, got, "identity_rejected fired on %q with nothing rejected", line.Msg)
		return
	}
	assert.Equal(t, []string{"true"}, got, "identity_rejected missing from %q", line.Msg)
}
