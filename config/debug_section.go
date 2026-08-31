package config

// checkDebug validates the debug block's two IP-shaped keys: debug.allowedips must be
// syntactically parseable (bare address or CIDR), and debug.trustedproxies must not
// contain a default route and must not be entirely unparseable.
// Semantics match scheduler.security.trustedproxies: empty is valid (proxy
// headers ignored), an all-invalid list fails fast, and a partial-invalid list
// passes with a middleware-time WARN so a single typo cannot silently weaken
// the allowlist's spoofing protection.
func checkDebug(cfg *DebugConfig) error {
	if err := validateIPOrCIDRList(fieldDebugAllowedIPs, cfg.AllowedIPs); err != nil {
		return err
	}
	return validateTrustedProxyList(fieldDebugTrustedProxies, cfg.TrustedProxies)
}
