package config

import "errors"

// retiredKey is a key no framework code ever read, and where its setting belongs instead.
type retiredKey struct {
	key     string
	instead string
}

// retiredKeys are refused at startup instead of ignored (ADR-153). They decoded into
// MessagingConfig for their whole history while the AMQP client dialed messaging.broker.url
// verbatim, so a delivered value configured nothing and said so nowhere. Kept in key order,
// which is the order the refusals are reported in.
var retiredKeys = []retiredKey{
	{
		key:     "messaging.broker.virtualhost",
		instead: "put the vhost in the broker URL path, amqp://host:port/<vhost>, with each / in the name encoded as %2F",
	},
	{key: "messaging.headers", instead: "set messaging.PublisherOptions.Headers where the module declares the publisher"},
	{key: "messaging.routing.exchange", instead: "set messaging.PublisherOptions.Exchange where the module declares the publisher"},
	{key: "messaging.routing.key", instead: "set messaging.PublisherOptions.RoutingKey where the module declares the publisher"},
}

// validateNoRetiredKeys fails startup when an operator layer delivered a retired key, with
// any value: an empty string or a YAML null is as much a template copy as a real value. It
// reads presence only, so no configured value — the broker URL above all (#2192) — can reach
// the error.
func validateNoRetiredKeys(cfg *Config) error {
	var errs []error
	for _, r := range retiredKeys {
		if !cfg.deliveredAtOrUnder(r.key) {
			continue
		}
		set := r.key
		if envVar := envVarForKey(r.key); envVar != "" {
			set += " (" + envVar + ")"
		}
		errs = append(errs, &ConfigError{
			Category: errCategoryInvalid,
			Field:    r.key,
			Message:  "is retired and was never read;",
			Action:   "remove " + set + " and " + r.instead,
		})
	}
	return errors.Join(errs...)
}
