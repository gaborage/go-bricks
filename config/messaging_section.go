package config

import (
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// sealGenerationPattern is the Activation selector's value grammar: a
// generation marker without its hyphen, a positive integer with no leading
// zero. It mirrors keystore.generationVersionPattern — keep in sync — so a
// selector can only ever spell a generation the way the keystore names it.
// config is the lower layer, so the keystore's vocabulary is not exported
// from here; the keystore re-checks its own copy at resolution.
var sealGenerationPattern = regexp.MustCompile(`^v[1-9]\d*$`)

// normalizeMessaging shapes messaging configuration: reconnect/publisher pool
// defaults (multitenant selects the deployment-mode-dependent Publisher.IdleTTL
// and Publisher.MaxCached defaults — see applyMessagingDefaults) and the
// streams offset-store defaults.
//
// Defaults are applied unconditionally, even when the root broker URL is empty
// (IsMessagingConfigured is false): per-tenant AMQP clients and the outbox
// relay still run with these defaults, and check's cross-field rules read the
// effective values.
func normalizeMessaging(cfg *MessagingConfig, multitenant bool) error {
	if err := applyMessagingDefaults(cfg, multitenant); err != nil {
		return err
	}

	return applyStreamsDefaults(&cfg.Streams)
}

// checkMessaging rejects an inverted reconnect.maxdelay/delay pair and an
// unknown tenancy, then the streams block.
func checkMessaging(cfg *MessagingConfig, multitenant bool) error {
	// Both sides are defaulted by normalizeMessaging, so this compares effective
	// values. computeBackoff silently clamps an inverted pair (maxdelay <
	// delay), which would leave the configured ceiling ignored.
	if cfg.Reconnect.MaxDelay < cfg.Reconnect.Delay {
		return NewValidationError("messaging.reconnect.maxdelay", "must be >= messaging.reconnect.delay")
	}

	if cfg.Tenancy != TenancyPerTenant && cfg.Tenancy != TenancyShared {
		return NewInvalidFieldError("messaging.tenancy",
			fmt.Sprintf(errNotSupportedFmt, cfg.Tenancy),
			[]string{TenancyPerTenant, TenancyShared})
	}

	if err := checkMessagingSeal(&cfg.Seal); err != nil {
		return err
	}
	return checkMessagingStreams(cfg, multitenant)
}

// checkMessagingSeal judges the Activation selector's shape: every key is a
// user-chosen section name (env-reachable, no '.'), every value a canonical
// generation. Whether the key names a Logical kid the keystore holds is the
// resolver's question (keystore.ActiveGeneration), asked once the store exists.
func checkMessagingSeal(cfg *SealConfig) error {
	for _, logical := range slices.Sorted(maps.Keys(cfg.Active)) {
		// A '.' would make the constructed path ambiguous, so the parent field
		// is reported, as the keystore.keys rule does.
		if logical == "" || strings.Contains(logical, ".") {
			err := NewValidationError(fieldMessagingSealActive, fmt.Sprintf("logical kid %q cannot be empty or contain '.' (the config path delimiter)", logical))
			err.Action = "name the messaging.seal.active entry after the keystore family, without dots"
			return err
		}
		field := fieldMessagingSealActive + "." + logical
		if err := checkSectionName(field, logical); err != nil {
			return err
		}
		if gen := cfg.Active[logical]; !sealGenerationPattern.MatchString(gen) {
			err := NewValidationError(field, fmt.Sprintf("generation %q must be v<N> with N a positive integer without leading zeros (v1, not v0 or v01)", gen))
			err.Action = "name the generation exactly as the keystore.keys entry suffix spells it"
			return err
		}
	}
	return nil
}

// checkMessagingStreams rejects a stream URI with an unsupported scheme or no
// host, streams under per-tenant tenancy, and a half-set address resolver. It
// takes the whole messaging block because the gate is a tenancy policy, which
// belongs next to the rationale below.
//
// SECURITY: messaging.streams.uri carries broker credentials, so no error raised
// here echoes the URI — only the config key and the offending scheme reach the
// message.
func checkMessagingStreams(cfg *MessagingConfig, multitenant bool) error {
	streams := &cfg.Streams
	if streams.URI != "" {
		// Per-tenant stream consumption would need one Environment per tenant and a
		// per-tenant stream URI leg; until that exists the combination fails loudly
		// instead of consuming one tenant's streams on behalf of all of them. Shared
		// tenancy does not need it: the lane consumes once on the control-plane key.
		if multitenant && cfg.Tenancy == TenancyPerTenant {
			return NewValidationError("messaging.streams",
				"single-tenant only; multi-tenant stream consumption is not yet supported")
		}

		u, err := url.Parse(streams.URI)
		if err != nil {
			return NewValidationError(fieldMessagingStreamsURI, "must be a valid URI")
		}
		if u.Scheme != streamsURIScheme && u.Scheme != streamsURITLSScheme {
			return NewInvalidFieldError(fieldMessagingStreamsURI,
				fmt.Sprintf(errNotSupportedFmt, u.Scheme),
				[]string{streamsURIScheme + "://", streamsURITLSScheme + "://"})
		}
		// Nothing to dial in either shape: a missing "//" parses opaque with no host,
		// and "://:5552" gives a port-only Host that a Host == "" check lets through.
		// Hostname() strips the port, so it catches both; neither names a host in the
		// manager's connect error, which shows the fixed placeholder or a bare "@:5552".
		if u.Hostname() == "" {
			return NewValidationError(fieldMessagingStreamsURI,
				"must include a host, e.g. "+streamsURIScheme+"://<user>:<password>@<host>:5552/%2f")
		}
	}

	return validateStreamsAddressResolver(&streams.AddressResolver)
}

// validateStreamsAddressResolver enforces the both-or-neither rule: a host with no
// port cannot be dialed, and a port with no host silently does nothing.
func validateStreamsAddressResolver(cfg *StreamsAddressResolverConfig) error {
	if cfg.Host == "" && cfg.Port == 0 {
		return nil
	}
	if cfg.Host == "" {
		return NewValidationError("messaging.streams.addressresolver.host",
			"must be set when messaging.streams.addressresolver.port is set")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return NewInvalidFieldError("messaging.streams.addressresolver.port",
			fmt.Sprintf(errInvalidField, cfg.Port), []string{portRange})
	}
	return nil
}

// applyStreamsDefaults materializes the offset-store defaults with the same
// "zero applies the default, negative is invalid" rule the rest of the messaging
// block uses.
func applyStreamsDefaults(cfg *StreamsConfig) error {
	if err := applyNonNegativeDefault(&cfg.OffsetStore.CountBeforeStorage, defaultStreamsOffsetCount,
		"messaging.streams.offsetstore.countbeforestorage"); err != nil {
		return err
	}
	return applyNonNegativeDefault(&cfg.OffsetStore.FlushInterval, defaultStreamsOffsetInterval,
		"messaging.streams.offsetstore.flushinterval")
}

// applyMessagingDefaults sets production-safe defaults for messaging configuration.
//
// It modifies cfg in-place:
//   - Reconnect.Delay: if 0, sets to 5s; if negative, returns an error.
//   - Reconnect.ReinitDelay: if 0, sets to 2s; if negative, returns an error.
//   - Reconnect.ResendDelay: if 0, sets to 5s; if negative, returns an error.
//   - Reconnect.ConnectionTimeout: if 0, sets to 30s; if negative, returns an error.
//   - Reconnect.ReadyTimeout: if 0, sets to 5s; if negative, returns an error.
//   - Reconnect.MaxDelay: if 0, sets to 60s; if negative, returns an error.
//   - Publisher.MaxCached: if 0, sets to 50 when multitenant is false; in
//     multi-tenant mode zero is preserved so app.ManagerConfigBuilder scales the
//     publisher pool to the tenant limit; if negative, returns an error.
//   - Publisher.IdleTTL: if 0, sets to 1h when multitenant is false, 10m when true
//     (mode-dependent — a shorter multi-tenant default bounds per-tenant publisher
//     churn); if negative, returns an error.
//   - Publisher.CleanupInterval: if 0, sets to 2m; if negative, returns an error.
//   - Reconnect.MaxPublishAttempts: if 0, sets to 5; if negative, returns an error.
//   - Tenancy: if empty, sets to "per-tenant".
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyMessagingDefaults(cfg *MessagingConfig, multitenant bool) error {
	// Each field follows the same "zero applies the default, negative is invalid" rule,
	// factored into applyNonNegativeDefault to keep the policy in one place. Publisher.IdleTTL
	// is handled separately below because its default depends on the deployment mode.
	for _, d := range []struct {
		field *time.Duration
		def   time.Duration
		name  string
	}{
		{&cfg.Reconnect.Delay, defaultReconnectDelay, "messaging.reconnect.delay"},
		{&cfg.Reconnect.ReinitDelay, defaultReinitDelay, "messaging.reconnect.reinitdelay"},
		{&cfg.Reconnect.ResendDelay, defaultResendDelay, "messaging.reconnect.resenddelay"},
		{&cfg.Reconnect.ConnectionTimeout, defaultConnectionTimeout, "messaging.reconnect.connectiontimeout"},
		{&cfg.Reconnect.ReadyTimeout, defaultReadyTimeout, "messaging.reconnect.readytimeout"},
		{&cfg.Reconnect.MaxDelay, defaultMaxReconnectDelay, "messaging.reconnect.maxdelay"},
		{&cfg.Publisher.CleanupInterval, defaultPublisherCleanupInterval, "messaging.publisher.cleanupinterval"},
	} {
		if err := applyNonNegativeDefault(d.field, d.def, d.name); err != nil {
			return err
		}
	}

	idleTTLDefault := defaultPublisherIdleTTL
	if multitenant {
		idleTTLDefault = defaultPublisherIdleTTLMultiTenant
	}
	if err := applyNonNegativeDefault(&cfg.Publisher.IdleTTL, idleTTLDefault, "messaging.publisher.idlettl"); err != nil {
		return err
	}

	if err := applyNonNegativeDefault(&cfg.Reconnect.MaxPublishAttempts, defaultMaxPublishAttempts, "messaging.reconnect.maxpublishattempts"); err != nil {
		return err
	}

	if cfg.Tenancy == "" {
		cfg.Tenancy = TenancyPerTenant
	}

	return applyModeAwarePoolDefault(&cfg.Publisher.MaxCached, defaultMaxPublishers, "messaging.publisher.maxcached", multitenant)
}

// IsMessagingConfigured determines if messaging is intentionally configured.
// This mirrors the logic used to determine if messaging should be initialized.
func IsMessagingConfigured(cfg *MessagingConfig) bool {
	return cfg.Broker.URL != ""
}

// isTenantMessagingConfigured determines if tenant messaging is intentionally configured.
// Returns true if the tenant has a non-empty messaging URL.
func isTenantMessagingConfigured(cfg *TenantMessagingConfig) bool {
	return strings.TrimSpace(cfg.URL) != ""
}
