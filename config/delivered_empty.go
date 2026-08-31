package config

// deliveredEmptyRejectingKeys are the list-shaped keys where a delivered-empty value is a
// broken template rather than an instruction. A key earns a place here only when clearing
// it FAILS OPEN — where the empty list disables a control instead of tightening one — so the
// list is deliberately short and each addition is its own decision.
//
// debug.allowedips is the one such key today (ADR-078). Its default is the loopback pair, so
// an empty value REPLACES a control with nothing: with debug.bearertoken set, ADR-049's
// registration gate is satisfied by the token alone and the IP whitelist is never installed.
// The other list keys are safe to clear — scheduler.security.cidrallowlist fails closed to
// localhost, multitenant.resolver.order fails validation, and the trusted-proxy and
// sensitive-field lists treat empty as the stricter posture.
var deliveredEmptyRejectingKeys = []string{"debug.allowedips"}

// validateNoDeliveredEmptyList fails startup when one of those keys was delivered as an
// empty STRING rather than an empty list. The distinction is the whole check and it lives in
// the raw koanf value, not in Exists: these keys carry defaults, so Exists is true even when
// nothing was configured. koanf keeps what the source actually delivered —
//
//	unset                  -> []string{"127.0.0.1", "::1"}   (the default; untouched)
//	DEBUG_ALLOWEDIPS=      -> ""                             (delivered empty; rejected)
//	allowedips: ""         -> ""                             (same shape, same rejection)
//	allowedips: []         -> []interface{}{}                (deliberate clear; allowed)
//
// — so an empty string is a template that rendered nothing, while an empty sequence is an
// operator saying "no entries" in the one spelling that cannot be produced by accident. That
// keeps ADR-049's sanctioned token-only posture expressible, which is why this rejects the
// shape rather than the outcome.
//
// Inert for hand-built Config literals (no koanf instance), exactly as
// validateNoDeliveredEmptyDatabase is: the app-layer ADR-049 gate remains the second seam.
func validateNoDeliveredEmptyList(cfg *Config) error {
	if cfg == nil || cfg.k == nil {
		return nil
	}
	for _, key := range deliveredEmptyRejectingKeys {
		raw, ok := cfg.rawValue(key)
		if !ok {
			continue
		}
		if !deliveredEmptyValue(raw) {
			continue
		}
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    key,
			Message:  "delivered empty — an empty value here removes a control rather than relaxing one",
			Action:   deliveredEmptyListAction(key),
		}
	}
	return nil
}

// deliveredEmptyValue reports whether a raw koanf value is a delivery that produces NO
// entries. Two shapes qualify, and neither can be written by an operator who means "no
// entries" — that spelling is an empty sequence, which reaches here as a slice and is
// deliberately not matched:
//
//   - a STRING the decoder would split into nothing. The test is the decoder's own rule, not
//     TrimSpace: splitAndTrimList drops empty parts, so "," and ",,," and " , " all decode to
//     zero entries while trimming non-empty. A Helm `join ","` over unset values, or an
//     envsubst over "${A},${B}", renders exactly those.
//   - YAML NULL (`allowedips:`, `null`, `~`), which arrives as nil. This is where the key
//     departs from ADR-074/077, and deliberately: for a numeric or bool key a null takes the
//     DEFAULT, so it behaves as absence and is left alone. Here it REPLACES the default —
//     unset decodes to the loopback pair, null decodes to nil — so the same spelling that is
//     harmless there removes a control here. A bare `allowedips:` is what
//     `allowedips: {{ .Values.debug.allowedIPs }}` renders when the value is unset.
func deliveredEmptyValue(raw any) bool {
	if raw == nil {
		return true
	}
	str, isString := raw.(string)
	return isString && len(splitAndTrimList(str, listSeparator)) == 0
}

// deliveredEmptyListAction names both ways out, in the order an operator wants them: put a
// value back, or say "no entries" in the one spelling that cannot be rendered by accident.
// The env half is emitted only when a variable actually reaches the key (envVarForKey), so
// the hint never sends anyone to set a variable that lands somewhere else.
func deliveredEmptyListAction(key string) string {
	set := "give " + key + " a value"
	if envVar := envVarForKey(key); envVar != "" {
		set = "set " + envVar + " to a value"
	}
	return set + ", or write `" + key + ": []` in config.yaml to clear it deliberately"
}
