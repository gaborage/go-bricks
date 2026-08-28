package messaging

import (
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// maxShortStrBytes is the AMQP shortstr ceiling. amqp091's writeShortstr refuses
// anything longer, and the Connection — not the publish, not the channel — is
// what dies when a frame write fails: it answers with `go c.shutdown(...)`, and
// every publisher in the process shares that connection.
const maxShortStrBytes = 255

// ErrInvalidPublishDestination is returned by Publish and PublishToExchange when
// a caller-supplied field of the basic.publish frame cannot fit an AMQP
// shortstr. Match it with errors.Is; the wrapped message names the FIELD and its
// byte length, never the value — an over-long destination is usually built from
// request data, and this error reaches logs and spans.
//
// The publish is refused outright rather than retried: the frame is unwritable
// whatever the broker's state, so a retry only re-tears the connection it just
// brought back. ErrPublishRetriesExhausted is deliberately NOT involved.
var ErrInvalidPublishDestination = errors.New("amqp: publish destination exceeds the AMQP shortstr limit")

// validatePublishDestination checks every caller-supplied shortstr in the
// basic.publish frame: the exchange, the routing key, and every header KEY
// (nested tables included — a table's keys are shortstrs at every depth).
//
// Length only. The charset is deliberately NOT checked: unlike the consume side
// (ADR-070, `[C60.17]`), where the value is a foreign publisher's, these are the
// service's OWN destinations, and a broker that dislikes one answers with a
// CHANNEL error — recoverable, and not the connection-wide failure this guard
// exists to prevent. Empty is legal: the default exchange and a fanout binding
// both use it.
func validatePublishDestination(options *PublishOptions) error {
	if err := checkShortStr("exchange", options.Exchange); err != nil {
		return err
	}
	if err := checkShortStr("routing key", options.RoutingKey); err != nil {
		return err
	}
	return checkTableKeys(options.Headers)
}

// checkTableKeys walks a header table, and any table nested inside it, judging
// the KEYS. Values are longstrs and carry their own much larger bound, so they
// are not this guard's business.
func checkTableKeys(headers map[string]any) error {
	for key, value := range headers {
		if err := checkShortStr("header key", key); err != nil {
			return err
		}
		switch nested := value.(type) {
		case amqp.Table:
			if err := checkTableKeys(nested); err != nil {
				return err
			}
		case map[string]any:
			if err := checkTableKeys(nested); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkShortStr reports the field and its SIZE, never its content.
func checkShortStr(field, value string) error {
	if len(value) <= maxShortStrBytes {
		return nil
	}
	return fmt.Errorf("%w: %s is %d bytes, limit is %d",
		ErrInvalidPublishDestination, field, len(value), maxShortStrBytes)
}

// validateDeclaredShortStrs applies the SAME length rule at declaration time, so
// a name the frame can never carry fails at startup instead of on the first
// publish — the failure mode the runtime guard turns into an error is one an
// operator would rather never reach. It is the same rule and the same sentinel
// deliberately: a second constant would let the two doors drift.
//
// Declared names are first-party config, not caller input, so the failure names
// which declaration kind is at fault; the offending value is the name itself and
// repeating a 256-byte string into a startup error helps nobody.
func validateDeclaredShortStrs(d *Declarations) error {
	for name := range d.Exchanges {
		if err := checkShortStr("declared exchange name", name); err != nil {
			return err
		}
	}
	for name := range d.Queues {
		if err := checkShortStr("declared queue name", name); err != nil {
			return err
		}
	}
	for _, binding := range d.Bindings {
		if err := checkShortStr("binding routing key", binding.RoutingKey); err != nil {
			return err
		}
	}
	for _, publisher := range d.Publishers {
		if err := checkShortStr("publisher routing key", publisher.RoutingKey); err != nil {
			return err
		}
		if err := checkTableKeys(publisher.Headers); err != nil {
			return err
		}
	}
	return nil
}
