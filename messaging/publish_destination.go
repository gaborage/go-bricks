package messaging

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	amqp "github.com/rabbitmq/amqp091-go"
)

// maxShortStrBytes is the AMQP shortstr ceiling. amqp091's writeShortstr refuses
// anything longer, and the Connection — not the publish, not the channel — is
// what dies when a frame write fails: it answers with `go c.shutdown(...)`, and
// every publisher in the process shares that connection.
//
// The same 255 is spelled inside routingKeyPattern on the consume side, where it
// is belt only (a CONSUMED value arrives through readShortstr and cannot exceed
// it); a regexp cannot read a constant, so the two state the ceiling separately
// and this comment is the link between them.
const maxShortStrBytes = 255

// ErrInvalidPublishDestination is returned when a caller-supplied field that the
// AMQP wire format carries as a shortstr cannot fit one. Two doors return it:
// Publish/PublishToExchange, for the basic.publish frame's exchange, routing key
// and header keys; and Declarations.Validate, for a declared name or routing key,
// which fails startup rather than the first publish. Match it with errors.Is; the wrapped message names the FIELD and its
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
		// amqp.Table IS map[string]any, so normalize and recurse once rather
		// than carrying two identical branches.
		if table, ok := value.(amqp.Table); ok {
			value = map[string]any(table)
		}
		if nested, ok := value.(map[string]any); ok {
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
// Covered: every exchange and queue NAME, every binding and publisher ROUTING
// KEY, and the KEYS of every Args/Headers table on those four kinds — each is a
// shortstr in the declare or publish frame it feeds. Consumer declarations are
// not covered here; their tag and queue reach basic.consume, a different frame
// on the consume side, and no publisher's connection depends on them.
//
// Every violation is reported, not just the first — the file's own convention
// for startup checks (see validateStreamDeclarations), so an operator with two
// over-long names fixes both in one deploy instead of discovering the second
// after redeploying.
//
// Declared names are first-party config, not caller input, so the failure names
// which declaration kind is at fault; the offending value is the name itself and
// repeating a 256-byte string into a startup error helps nobody.
func validateDeclaredShortStrs(d *Declarations) error {
	// Two checks per declaration across the four kinds; errors.Join drops the
	// nils, so this is a ceiling rather than a count.
	errs := make([]error, 0, 2*(len(d.Exchanges)+len(d.Queues)+len(d.Bindings)+len(d.Publishers)))

	for _, name := range slices.Sorted(maps.Keys(d.Exchanges)) {
		errs = append(errs,
			checkShortStr("declared exchange name", name),
			checkTableKeys(d.Exchanges[name].Args))
	}
	for _, name := range slices.Sorted(maps.Keys(d.Queues)) {
		errs = append(errs,
			checkShortStr("declared queue name", name),
			checkTableKeys(d.Queues[name].Args))
	}
	for _, binding := range d.Bindings {
		errs = append(errs,
			checkShortStr("binding routing key", binding.RoutingKey),
			checkTableKeys(binding.Args))
	}
	for _, publisher := range d.Publishers {
		errs = append(errs,
			checkShortStr("publisher routing key", publisher.RoutingKey),
			checkTableKeys(publisher.Headers))
	}

	return errors.Join(errs...)
}
