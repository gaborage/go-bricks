package messaging

import (
	"regexp"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// routingKeyPattern bounds a routing key on its way into a log field, a span
// attribute and a metric attribute: printable ASCII, no control bytes, at most
// the AMQP shortstr ceiling.
//
// The charset is the load-bearing half. The length half is belt only — a
// CONSUMED routing key arrives through amqp091's readShortstr and is ≤255 bytes
// by construction, so it can never fire here; it is stated because the bound is
// part of the rule, not because this door enforces it. The PUBLISH side, where
// the bound does bite and a violation tears down the shared connection, has no
// equivalent guard and cannot reuse this one — omitting the service's own
// destination from a log line hides what an operator needs, and only refusing
// the publish prevents the teardown. Tracked in #1123.
//
// It is deliberately NOT the request-id charset. Every routing key this framework
// publishes is dot-delimited (`user.created`) and a topic binding legally carries
// `*` and `#`, so `^[A-Za-z0-9_-]{1,128}$` would discard the routing key of
// essentially every real deployment. What must not survive is what ADR-070
// refuses everywhere else: CR/LF, NUL and ANSI escapes on their way into a log
// line, and an over-long value on its way into an AMQP shortstr. The accepted
// cost is that a routing key outside printable ASCII loses its log field and its
// metric attribute — never its delivery.
//
// It bounds each value's LENGTH, not how many distinct values there are. A topic
// binding lets a publisher pick a new routing key per message, and each distinct
// one is a fresh attribute set on the consume instruments — bounded only by the
// OTel SDK's default 2000-series cardinality limit, after which the series
// overflow. That exposure predates this rule and this rule does not close it.
var routingKeyPattern = regexp.MustCompile(`^[[:print:]]{1,255}$`)

// deliveryIdentity is the publisher-controlled half of a delivery's identity,
// resolved once and vouched for.
//
// `trace.ExtractFromHeaders` guards a delivery's `headers` TABLE. CorrelationId and
// MessageId are content-header properties; RoutingKey and Exchange are
// basic.deliver envelope metadata. No header extractor reaches either kind, and
// all four land in framework sinks verbatim. A foreign publisher — our own
// publisher's CorrelationId is validated at its assignment site — can put any
// bytes the wire format accepts in them.
//
// A field that fails is "" and every FRAMEWORK sink OMITS it rather than reporting it
// empty, which is what the receive span has always done for a field the delivery
// did not carry. Nothing is substituted and nothing is truncated: a truncated
// identifier is a plausible identifier, so it forges correlation silently
// (ADR-070).
//
// `Exchange` is here too. It is not publisher-controlled in the same direct way —
// a consumer only sees exchanges bound to its own queue, and creating one needs
// configure permission — but that is a property of the DEPLOYMENT, not a
// guarantee the code holds, and RabbitMQ bounds an exchange name by length and
// the `amq.` reservation, not by charset. It reaches the same three sinks under
// the same rule, so it is judged by it rather than by an assumption about who
// holds which permission on a shared vhost.
//
// `ConsumerTag` is deliberately absent: it is the tag THIS process handed to
// basic.consume and the broker echoed back, so it is first-party config, not
// publisher input.
type deliveryIdentity struct {
	correlationID string
	messageID     string
	routingKey    string
	exchange      string

	// rejected records that at least one field the delivery DID carry failed
	// validation. Omitting a value silently would leave the operator nothing to
	// search for — ADR-070's stated detect is a log search — so the sinks stamp
	// this instead. It is a bool, so it costs one bounded attribute rather than
	// re-introducing the unbounded value it replaces.
	rejected bool
}

// identify resolves the delivery's four identity fields, so the eager
// span-attribute seam and the lazy log seams read one decision instead of each
// re-judging the raw value.
//
// It takes strings rather than a delivery so the rule is not welded to amqp091:
// an AMQP 1.0 message carries Properties.MessageID and Properties.CorrelationID
// too, and the streams lane surfaces neither TODAY — the day it does, the rule is
// already reachable.
func identify(correlationID, messageID, routingKey, exchange string) deliveryIdentity {
	id := deliveryIdentity{
		correlationID: gobrickstrace.ValidateRequestID(correlationID),
		messageID:     gobrickstrace.ValidateRequestID(messageID),
		routingKey:    validateRoutingKey(routingKey),
		exchange:      validateRoutingKey(exchange),
	}
	id.rejected = dropped(correlationID, id.correlationID) ||
		dropped(messageID, id.messageID) ||
		dropped(routingKey, id.routingKey) ||
		dropped(exchange, id.exchange)
	return id
}

// dropped reports that the delivery carried a value and validation refused it,
// as distinct from the delivery carrying none.
func dropped(raw, vouched string) bool { return raw != "" && vouched == "" }

// identifyDelivery adapts a classic-lane delivery onto identify.
func identifyDelivery(delivery *amqp.Delivery) deliveryIdentity {
	return identify(delivery.CorrelationId, delivery.MessageId, delivery.RoutingKey, delivery.Exchange)
}

// validateRoutingKey returns key when it is a safe AMQP routing key, otherwise "".
func validateRoutingKey(key string) string {
	if routingKeyPattern.MatchString(key) {
		return key
	}
	return ""
}

// strIfSet stamps key only when value survived validation, so a field the
// delivery did not carry — or carried unusably — is absent from the line rather
// than present and empty.
func strIfSet(e logger.LogEvent, key, value string) logger.LogEvent {
	if value == "" {
		return e
	}
	return e.Str(key, value)
}

// consumeMetrics identifies a delivery on the receive instruments. It takes no
// delivery, so no raw property is in scope to reach for: both values it forwards
// land in a metric attribute AND in the destination name every consume metric is
// stamped with.
func consumeMetrics(queue string, id deliveryIdentity) tracking.ConsumeAttributes {
	return tracking.AMQPConsumeAttributes(id.exchange, id.routingKey, queue)
}

// flagRejected stamps the marker when this delivery carried a value validation
// refused, so an operator has something to search for where the value itself is
// now absent.
func flagRejected(e logger.LogEvent, id deliveryIdentity) logger.LogEvent {
	if !id.rejected {
		return e
	}
	return e.Bool("identity_rejected", true)
}
