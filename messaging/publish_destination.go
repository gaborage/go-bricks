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
// Publish/PublishToExchange, for the exchange and routing key of the
// basic.publish METHOD frame and the header keys of the CONTENT-HEADER frame
// beside it; and Declarations.Validate, for a declared name or routing key,
// which fails startup rather than the first publish. Match it with errors.Is; the wrapped message names the FIELD and its
// byte length, never the value — an over-long destination is usually built from
// request data, and this error reaches logs and spans.
//
// The publish is refused outright rather than retried: the frame is unwritable
// whatever the broker's state, so a retry only re-tears the connection it just
// brought back. ErrPublishRetriesExhausted is deliberately NOT involved.
var ErrInvalidPublishDestination = errors.New("amqp: publish destination exceeds the AMQP shortstr limit")

// ValidatePublishDestination checks every caller-supplied shortstr a publish
// puts on the wire: the exchange and routing key, which travel in the
// basic.publish METHOD frame, and every header KEY, which travels in the
// CONTENT-HEADER frame that follows it (the same frame carrying CorrelationId,
// which ADR-070 already guards). One operation, two frames, one ceiling
// (nested tables included — a table's keys are shortstrs at every depth).
//
// Length only. The charset is deliberately NOT checked: unlike the consume side
// (ADR-070, `[C60.17]`), where the value is a foreign publisher's, these are the
// service's OWN destinations, and a broker that dislikes one answers with a
// CHANNEL error — recoverable, and not the connection-wide failure this guard
// exists to prevent. Empty is legal: the default exchange and a fanout binding
// both use it.
//
// It is exported for callers that record a destination now and publish it later —
// the outbox writes exchange, routing key and header keys to a ledger row, and a
// row the frame can never carry is better refused at the INSERT than parked by the
// relay after MaxRetries. They run the rule rather than restating the ceiling.
func ValidatePublishDestination(options PublishOptions) error {
	if err := checkShortStr("exchange", options.Exchange); err != nil {
		return err
	}
	if err := checkShortStr("routing key", options.RoutingKey); err != nil {
		return err
	}
	return checkTableKeys("header key", options.Headers)
}

// checkTableKeys walks a table, judging its KEYS, and descends through every
// structure amqp091 encodes recursively: a nested table, and a FIELD-ARRAY,
// whose elements go back through writeField and can therefore be tables of
// their own (write.go, `case []any` → writeField → writeTable → writeShortstr).
// A key one array deep is written by the same function as a key at the top, so
// it fails the frame the same way.
//
// Values are longstrs and carry their own far larger bound, so they are not this
// guard's business — only the keys are.
//
// field names the location for the error, since a header table and a
// declaration's Args reach different frames and an operator needs to know which
// one they are looking at.
func checkTableKeys(field string, headers map[string]any) error {
	for key, value := range headers {
		if err := checkShortStr(field, key); err != nil {
			return err
		}
		if err := checkTableValue(field, value); err != nil {
			return err
		}
	}
	return nil
}

// checkTableValue descends into whatever a table value can carry. amqp.Table IS
// map[string]any, so it is normalized rather than given a branch of its own.
func checkTableValue(field string, value any) error {
	if table, ok := value.(amqp.Table); ok {
		value = map[string]any(table)
	}

	switch nested := value.(type) {
	case map[string]any:
		return checkTableKeys(field, nested)
	case []any:
		// A field-array can hold tables, and arrays of arrays of tables.
		for _, element := range nested {
			if err := checkTableValue(field, element); err != nil {
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
// Covered: every exchange NAME and TYPE (the type reaches exchange.declare as a
// shortstr of its own), every queue NAME, every binding and publisher ROUTING
// KEY, and the KEYS of every Args/Headers table on those four kinds — each is a
// shortstr in the declare or publish frame it feeds. Each names its own
// location, so an error says which table an operator should be reading. Consumer declarations are
// not covered here; their tag and queue reach basic.consume, a different frame
// on the consume side, and no publisher's connection depends on them.
//
// Violations are aggregated with errors.Join — the file's own convention for
// startup checks (see validateStreamDeclarations) — one error per FIELD, so an
// operator with an over-long exchange name and an over-long publisher routing
// key fixes both in one deploy. The aggregation stops at the field: checkTableKeys
// returns on the first oversized key inside a table, and map order is undefined,
// so a single table carrying two of them names one and the next boot names the
// other. Reporting every key in a table would mean threading a collector through
// the recursion for a case that is a config typo either way.
//
// A nil entry in either map is reported rather than dereferenced: the maps are
// exported, so a module can put one there, and a nil-map-value panic at startup
// tells an operator far less than the key that carries it. The KEY is named here
// — unlike an over-long value, a map key that was registered without a
// declaration is the only thing identifying it. It reuses errNilDeclaration, the
// sentinel the declare path already returns for the same mistake.
//
// Declared names are first-party config, not caller input, so the failure names
// which declaration kind is at fault; the offending value is the name itself and
// repeating a 256-byte string into a startup error helps nobody.
func validateDeclaredShortStrs(d *Declarations) error {
	// Deliberately not preallocated: a capacity is arithmetic on a value nothing
	// observes, so its mutant cannot be killed by any test, and the diff-scoped
	// mutation gate blocks on it. The allocation runs once, at startup.
	var errs []error

	for _, name := range slices.Sorted(maps.Keys(d.Exchanges)) {
		exchange := d.Exchanges[name]
		if exchange == nil {
			errs = append(errs, fmt.Errorf("%w: exchange %q", errNilDeclaration, name))
			continue
		}
		errs = append(errs,
			checkShortStr("declared exchange name", name),
			checkShortStr("declared exchange type", exchange.Type),
			checkTableKeys("declared exchange argument key", exchange.Args))
	}
	for _, name := range slices.Sorted(maps.Keys(d.Queues)) {
		queue := d.Queues[name]
		if queue == nil {
			errs = append(errs, fmt.Errorf("%w: queue %q", errNilDeclaration, name))
			continue
		}
		errs = append(errs,
			checkShortStr("declared queue name", name),
			checkTableKeys("declared queue argument key", queue.Args))
	}
	for _, binding := range d.Bindings {
		errs = append(errs,
			checkShortStr("binding routing key", binding.RoutingKey),
			checkTableKeys("binding argument key", binding.Args))
	}
	for _, publisher := range d.Publishers {
		errs = append(errs,
			checkShortStr("publisher routing key", publisher.RoutingKey),
			checkTableKeys("publisher header key", publisher.Headers))
	}

	return errors.Join(errs...)
}
