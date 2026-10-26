package config

import (
	"errors"
	"strings"
)

// retiredKey is a key no framework code ever read, and where its setting belongs instead.
// subtree marks a map key: any key delivered under it is the same delivery.
type retiredKey struct {
	key     string
	subtree bool
	instead string
}

// retiredKeys are refused at startup instead of ignored (ADR-153). They decoded into
// MessagingConfig for their whole history while the AMQP client dialed messaging.broker.url
// verbatim, so a delivered value configured nothing and said so nowhere.
var retiredKeys = []retiredKey{
	{
		key:     "messaging.broker.virtualhost",
		instead: "put the vhost in the broker URL path, amqp://host:port/<vhost>, with each / in the name encoded as %2F",
	},
	{key: "messaging.headers", subtree: true, instead: "set messaging.PublisherDeclaration.Headers"},
	{key: "messaging.routing.exchange", instead: "set messaging.PublisherDeclaration.Exchange"},
	{key: "messaging.routing.key", instead: "set messaging.PublisherDeclaration.RoutingKey"},
}

// validateNoRetiredKeys fails startup when an operator layer delivered a retired key, with
// any value: an empty string or a YAML null is as much a template copy as a real value. It
// reads the ADR-104 presence record, so the koanf tree is never consulted for values and no
// configured value — the broker URL above all (#2192) — can reach the error.
//
// Paths are matched case-insensitively, as mapstructure matched the fields they decoded
// into. Every refused key is reported, in key order, so a verbatim template copy is fixed in
// one pass.
func validateNoRetiredKeys(cfg *Config) error {
	// A hand-built literal has no source, so nothing was delivered.
	if cfg.src == nil {
		return nil
	}
	var errs []error
	for _, r := range retiredKeys {
		if !retiredKeyDelivered(cfg, r) {
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

// retiredKeyDelivered reports whether any delivered path is r.key, or lies under it for a
// subtree key. Section paths are legitimate here: a YAML null written at messaging.headers
// records that path itself.
func retiredKeyDelivered(cfg *Config, r retiredKey) bool {
	for path := range cfg.src.delivered {
		folded := strings.ToLower(path)
		if folded != r.key && (!r.subtree || !strings.HasPrefix(folded, r.key+koanfDelim)) {
			continue
		}
		if cfg.delivered(path) {
			return true
		}
	}
	return false
}
