package messaging

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// validateDeadLetterTopology reports, in sorted queue order, every queue whose x-dead-letter-exchange
// the declaration set shows cannot park a dead-lettered message (ADR-142). The framework nacks a
// failed delivery without requeue, so an unroutable dead-letter route drops it with no log. Args are
// read from the stored declarations, so a mutation through d.Queues or d.Exchanges is judged too.
func (d *Declarations) validateDeadLetterTopology() error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(d.Queues)) {
		if err := d.deadLetterRouteError(name, d.Queues[name].Args); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// deadLetterRouteError judges one queue's dead-letter route. Errors name the queue, the DLX, its
// type and the rule, plus the dead-letter routing key for rule 5; no other Args value.
func (d *Declarations) deadLetterRouteError(queue string, args map[string]any) error {
	dlx, isString := args[argDeadLetterExchange].(string)
	if !isString || dlx == "" {
		return nil // rule 7, rule 1: not judged
	}
	rawKey, keySet := args[argDeadLetterRoutingKey]
	key, isString := rawKey.(string)
	if keySet && !isString {
		return nil // rule 7
	}
	exchange, declared := d.Exchanges[dlx]
	if !declared {
		return absentExchange(fmt.Sprintf("queue %q's x-dead-letter-exchange", queue), dlx, " (dead-letter rule 2)")
	}
	if !routingIsVisible(exchange) {
		return nil // rule 3
	}

	keys := d.bindingKeysTo(dlx)
	if len(keys) == 0 {
		return fmt.Errorf("queue %q dead-letters to %s exchange %q, which has no binding in this declaration set "+
			"(dead-letter rule 4), so every dead-lettered message is dropped: declare the parking queue and its "+
			"binding, or let DeclareQueueWithDLQ own the route; when another service owns the parking side, it "+
			"declares the exchange and this service marks it external with DeclareExternalExchange",
			queue, exchange.Type, dlx)
	}
	if exchange.Type == ExchangeTypeFanout {
		return nil
	}
	if keySet {
		if slices.ContainsFunc(keys, func(bound string) bool { return bindingMatches(exchange.Type, bound, key) }) {
			return nil
		}
		return fmt.Errorf("queue %q dead-letters to %s exchange %q with routing key %q, which no binding to it "+
			"matches (dead-letter rule 5): bind a key that matches it, or declare the exchange fanout",
			queue, exchange.Type, dlx, key)
	}
	if slices.ContainsFunc(keys, func(bound string) bool { return bound != "" }) {
		return nil
	}
	return fmt.Errorf("queue %q dead-letters to %s exchange %q with no x-dead-letter-routing-key, and every binding "+
		"to it has the key \"\" (dead-letter rule 6): a dead-lettered message keeps its original routing key, so it "+
		"is dropped unless that key is \"\"; declare the exchange fanout, bind a key the messages carry, or set "+
		"x-dead-letter-routing-key to a bound key",
		queue, exchange.Type, dlx)
}

// routingIsVisible reports whether the set shows how exchange routes: a fanout, direct or topic
// exchange with no alternate exchange. An external exchange carries no type (ADR-119), so it is not
// judged, nor is a headers or x- plugin exchange.
func routingIsVisible(exchange *ExchangeDeclaration) bool {
	if _, hasAlternate := exchange.Args[argAlternateExchange]; hasAlternate {
		return false
	}
	return exchange.Type == ExchangeTypeFanout || exchange.Type == ExchangeTypeDirect || exchange.Type == ExchangeTypeTopic
}

func (d *Declarations) bindingKeysTo(exchange string) []string {
	var keys []string
	for _, binding := range d.Bindings {
		if binding.Exchange == exchange {
			keys = append(keys, binding.RoutingKey)
		}
	}
	return keys
}

// bindingMatches reports whether a direct or topic binding with key bound delivers a message routed with key.
func bindingMatches(exchangeType, bound, key string) bool {
	if exchangeType == ExchangeTypeTopic {
		return topicMatches(bound, key)
	}
	return bound == key
}
