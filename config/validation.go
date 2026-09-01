package config

import (
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// Database pool defaults
const (
	defaultSlowQueryThreshold = 200 * time.Millisecond
	defaultMaxQueryLength     = 1000
	defaultKeepAliveEnabled   = true
	defaultKeepAliveInterval  = 60 * time.Second
	defaultPoolIdleTime       = 5 * time.Minute  // Close idle connections before NAT/firewall timeout
	defaultPoolLifetimeMax    = 30 * time.Minute // Force periodic connection recycling
	defaultPoolMaxConnections = int32(25)        // Maximum open connections (tune to workload/server capacity)
)

// Database session defaults
const (
	// DefaultTimezone is the IANA timezone applied when a timezone config field
	// (database.timezone, scheduler.timezone) is unset. UTC is opinionated to keep
	// behavior identical across environments regardless of host or server defaults.
	DefaultTimezone = "UTC"
	// DefaultDatabaseTimezone is the default IANA timezone for database sessions.
	// Retained as a domain-named alias of DefaultTimezone for readability at
	// database call sites.
	DefaultDatabaseTimezone = DefaultTimezone
	// TimezoneDisabledSentinel opts out of session-level timezone enforcement,
	// preserving the database server's default timezone (legacy behavior).
	// Connection layers compare against this constant to decide whether to
	// apply per-connection timezone setup.
	TimezoneDisabledSentinel = "-"

	// DefaultBodyLimitBytes is the maximum request body size (10 MB) applied when
	// server.bodylimit is unset or resolves to a non-positive value. Single source
	// of truth for both the koanf default (loadDefaults) and the server-side
	// wire-up fallback (server.SetupMiddlewares).
	DefaultBodyLimitBytes int64 = 10 * 1024 * 1024

	// DefaultKeyStoreSecretMinLength is the byte floor for symmetric keystore
	// secrets when keystore.secretminlength is absent. Single source of truth: the
	// normalize fill applies it (normalizeKeyStore) and the koanf default derives
	// from that fill rather than rendering it a second time (derivedDefaultKeys).
	DefaultKeyStoreSecretMinLength = 32
)

// Messaging reconnection defaults
const (
	defaultReconnectDelay    = 5 * time.Second  // Initial delay between reconnection attempts
	defaultReinitDelay       = 2 * time.Second  // Delay before channel reinitialization
	defaultResendDelay       = 5 * time.Second  // Delay before retrying failed publishes
	defaultConnectionTimeout = 30 * time.Second // Per-publish broker confirmation (ACK/NACK) wait
	defaultReadyTimeout      = 5 * time.Second  // Pre-flight wait for a not-yet-ready client before a publish begins
	defaultMaxReconnectDelay = 60 * time.Second // Maximum delay for exponential backoff cap
	defaultMaxPublishers     = 50               // Maximum publisher clients in cache
	defaultPublisherIdleTTL  = 1 * time.Hour    // Time before idle publishers are evicted (single-tenant)
	// defaultPublisherIdleTTLMultiTenant is the multi-tenant idle-eviction default. It is
	// deliberately shorter than the single-tenant default to bound per-tenant publisher
	// churn, and matches the value multi-tenant deployments actually received before
	// applyMessagingDefaults became mode-aware (app/managers.go's BuildMessagingOptions
	// multi-tenant fallback — previously unreachable on the production path once this
	// function unconditionally applied the single-tenant default here first).
	defaultPublisherIdleTTLMultiTenant = 10 * time.Minute
	defaultPublisherCleanupInterval    = 2 * time.Minute // Publisher-pool cleanup goroutine frequency
	defaultMaxPublishAttempts          = 5               // Bounded publish retry attempts before giving up
)

// Native stream-protocol (messaging.streams.*) defaults
const (
	defaultStreamsOffsetCount    = 500             // Handled messages before a server-side offset commit
	defaultStreamsOffsetInterval = 5 * time.Second // Elapsed time before a pending offset is committed

	streamsURIScheme    = "rabbitmq-stream"
	streamsURITLSScheme = "rabbitmq-stream+tls"
)

// Cache manager defaults
const (
	defaultCacheMaxSize         = 100              // Maximum tenant cache instances
	defaultCacheIdleTTL         = 15 * time.Minute // Idle timeout per cache
	defaultCacheCleanupInterval = 5 * time.Minute  // Cleanup goroutine frequency
)

// Database manager defaults
const (
	defaultDatabaseManagerMaxSize            = 10
	defaultDatabaseManagerIdleTTL            = 1 * time.Hour
	defaultDatabaseManagerIdleTTLMultiTenant = 30 * time.Minute
	defaultDatabaseManagerCleanupInterval    = 5 * time.Minute
)

// Redis cache defaults. The top-level cache.* keys receive these via koanf
// (see config.go), but per-tenant multitenant.tenants.<id>.cache.* keys have no
// koanf defaults, so validation must apply them itself (see applyRedisDefaults).
const (
	defaultRedisPort            = 6379
	defaultRedisPoolSize        = 10
	defaultRedisDialTimeout     = 5 * time.Second
	defaultRedisReadTimeout     = 3 * time.Second
	defaultRedisWriteTimeout    = 3 * time.Second
	defaultRedisMaxRetries      = 3
	defaultRedisMinRetryBackoff = 8 * time.Millisecond
	defaultRedisMaxRetryBackoff = 512 * time.Millisecond
)

// Startup timeout defaults
const (
	defaultStartupTimeout              = 10 * time.Second // Overall startup timeout
	defaultStartupDatabaseTimeout      = 10 * time.Second // Database health check timeout
	defaultStartupMessagingTimeout     = 10 * time.Second // Broker connection timeout
	defaultStartupCacheTimeout         = 5 * time.Second  // Cache initialization timeout
	defaultStartupObservabilityTimeout = 15 * time.Second // OTLP provider initialization timeout
)

// Scheduler timeout defaults. Single source in the strict sense now: normalize applies
// these, the koanf loader DERIVES its values from that (derivedDefaultKeys in config.go)
// rather than rendering them a second time, and the scheduler module reads the normalized
// config rather than mirroring them (#1029, #1023).
const (
	defaultSchedulerShutdownTimeout  = 30 * time.Second // Budget for in-flight jobs during graceful shutdown
	defaultSchedulerSlowJobThreshold = 25 * time.Second // Successful jobs slower than this log at WARN
)

// Database type constants
const (
	PostgreSQL = "postgresql"
	Oracle     = "oracle"
)

// MinDatabasePasswordLength is the minimum length for a non-empty database
// password. Below this, the migration engine cannot safely substring-redact the
// password from Flyway output and suppresses the whole output instead, which
// hides a migration's outcome (see migration.redactPassword). Empty passwords
// (trust/IAM auth) are exempt.
const MinDatabasePasswordLength = 8

// Environment constants
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Cache type constants
const (
	CacheTypeRedis = "redis"
)

// Validation error message constants
const (
	errMustBeNonNegative    = "must be non-negative"
	errMustBePositive       = "must be positive"
	errNotSupportedFmt      = "'%s' is not supported"
	errDatabaseIncomplete   = "database configuration incomplete"
	portRange               = "1-65535"
	fieldDatabase           = "database"
	fieldDatabases          = "databases"
	fieldMultitenantTenants = "multitenant.tenants"
	fieldKeystoreKeys       = "keystore.keys"
	fieldDatabasePort       = "database.port"
	fieldDatabasePassword   = "database.password"
	fieldMessaging          = "messaging"
	fieldCache              = "cache"
	fieldDebug              = "debug"
	fieldServerPort         = "server.port"
	fieldLogLevel           = "log.level"
	fieldAppEnv             = "app.env"
	fieldAppRateLimit       = "app.rate.limit"
	fieldCacheRedisDB       = "cache.redis.database"
	fieldCacheRedisPool     = "cache.redis.poolsize"
	fieldResolverOrder      = "multitenant.resolver.order"
	errInvalidField         = "invalid value: %v"
	databasesFieldPrefix    = "databases.%s"
	keystoreKeysFieldPrefix = "keystore.keys.%s"
	tenantsFieldPrefix      = "multitenant.tenants.%s"
	defaultHost             = "localhost"

	fieldServerTLSCertFile   = "server.tls.certfile"
	fieldServerTLSCertValue  = "server.tls.certvalue"
	fieldServerTLSKeyFile    = "server.tls.keyfile"
	fieldServerTLSKeyValue   = "server.tls.keyvalue"
	fieldServerTLSMinVersion = "server.tls.minversion"
	tlsVersion12             = "1.2"
	tlsVersion13             = "1.3"

	fieldServerForwardedClientCertRequire = "server.forwardedclientcert.require"

	fieldServerTrustedProxies = "server.trustedproxies"

	// actionListSpecificProxyRanges is the remedy for a default route on ANY of the three
	// trusted-proxy keys. Shared so the three refusals cannot drift apart in wording.
	actionListSpecificProxyRanges = "list the specific proxy ranges to trust instead of a default route"
	// The other two TRUST keys. All three answer to the same default-route rule and
	// differ only in which parser handles their remaining syntax.
	fieldDebugTrustedProxies     = "debug.trustedproxies"
	fieldSchedulerTrustedProxies = "scheduler.security.trustedproxies"

	// An ALLOWLIST key, deliberately not subject to that rule: admitting every address
	// is a legitimate posture here (ADR-049 recommends it), where trusting every
	// address is not.
	fieldDebugAllowedIPs = "debug.allowedips"
)

const (
	fieldMessagingStreamsURI = "messaging.streams.uri"

	fieldDatabaseTLS = "database.tls"

	sslModeDisable    = "disable"
	sslModeAllow      = "allow"
	sslModePrefer     = "prefer"
	sslModeRequire    = "require"
	sslModeVerifyCA   = "verify-ca"
	sslModeVerifyFull = "verify-full"
)

// validateTrustedProxyList is the whole rule for a LENIENT trusted-proxy key: refuse a
// default route outright, then require that not every remaining entry is unparseable.
// Both keys that use it must apply both halves, so they share one call rather than two
// copies of the pair (server.trustedproxies answers to the stricter
// validateServerTrustedProxies instead).
func validateTrustedProxyList(field string, list []string) error {
	if err := rejectTotalCoverage(field, list); err != nil {
		return err
	}
	return validateCIDRList(field, list)
}

// newDefaultRouteError builds the refusal all three trusted-proxy keys share. The message
// is composed from errTrustedProxyDefaultRoute rather than restating it, so the wording
// cannot drift between the strict server path and the two lenient ones.
func newDefaultRouteError(field, entry string) *ConfigError {
	return &ConfigError{
		Category: errCategoryInvalid,
		Field:    field,
		Message:  fmt.Sprintf("'%s' %s", entry, errTrustedProxyDefaultRoute),
		Action:   actionListSpecificProxyRanges,
	}
}

// NormalizeIPNet returns a net's address and mask size as the family net.IPNet.Contains
// will actually use them. It exists because Mask.Size() and Contains disagree on a
// v4-mapped IPv6 net: "::ffff:0.0.0.0/96" measures 96 of 128 bits, but Contains re-derives
// a 4-byte mask and matches every IPv4 address — so a mask-size test reads it as a /96
// while it behaves as 0.0.0.0/0. Measuring the wrong one is how a default route walks past
// a default-route check.
func NormalizeIPNet(n *net.IPNet) (ip net.IP, ones, bits int) {
	if v4 := n.IP.To4(); v4 != nil {
		mask := n.Mask
		if len(mask) == net.IPv6len {
			mask = mask[12:]
		}
		o, b := mask.Size()
		return v4, o, b
	}
	o, b := n.Mask.Size()
	return n.IP, o, b
}

// addressSpan is one contiguous run of addresses, inclusive of both ends.
type addressSpan struct{ lo, hi *big.Int }

// CoversAddressFamily reports whether nets, merged, span an ENTIRE address family.
//
// This is the rule for a trusted-proxy list, and it is deliberately about coverage rather
// than about spelling. "0.0.0.0/0" is only the most obvious way to trust everyone;
// ["0.0.0.0/1","128.0.0.0/1"] and ["0.0.0.0/1","128.0.0.0/2","192.0.0.0/2"] do the same
// thing with properly-masked, non-default-route entries, and no per-entry test reaches
// them. Trusting every address makes every peer a trusted proxy, which is what lets a
// caller connecting directly have their forwarding headers believed (ADR-080).
//
// Merging is exact and there is no threshold: a list covering all-but-one-address is NOT
// rejected. See the residual note in ADR-080 — any cut-off would be arbitrary and would
// refuse legitimate large lists, and a list built that way is not an accident.
func CoversAddressFamily(nets []*net.IPNet, bits int) bool {
	var spans []addressSpan
	for _, n := range nets {
		if n == nil || n.IP == nil {
			continue
		}
		ip, ones, netBits := NormalizeIPNet(n)
		if netBits != bits {
			continue
		}
		lo := new(big.Int).SetBytes(ip.Mask(net.CIDRMask(ones, netBits)))
		size := new(big.Int).Lsh(big.NewInt(1), uint(netBits-ones))
		hi := new(big.Int).Sub(new(big.Int).Add(lo, size), big.NewInt(1))
		spans = append(spans, addressSpan{lo, hi})
	}
	if len(spans) == 0 {
		return false
	}
	slices.SortFunc(spans, func(a, b addressSpan) int { return a.lo.Cmp(b.lo) })

	// The family is covered only if the merged run starts at zero and reaches the top
	// with no gap. Any gap is an address the list does not trust, which is the whole
	// point of having a list.
	if spans[0].lo.Sign() != 0 {
		return false
	}
	one := big.NewInt(1)
	reach := spans[0].hi
	for _, s := range spans[1:] {
		if s.lo.Cmp(new(big.Int).Add(reach, one)) > 0 {
			return false
		}
		// reach is the running MAXIMUM, not the last endpoint seen: CIDR sets nest freely
		// (10.0.0.0/8 sits inside 0.0.0.0/1), and a nested span would otherwise pull reach
		// backwards and invent a gap the list does not have.
		reach = slices.MaxFunc([]*big.Int{reach, s.hi}, (*big.Int).Cmp)
	}
	top := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(bits)), one)
	return reach.Cmp(top) == 0
}

// rejectTotalCoverage fails when a trusted-proxy list trusts an entire address family.
//
// Entries net.ParseCIDR rejects are skipped rather than reported: the two lenient keys
// tolerate a partial-invalid list on purpose, and validateCIDRList still owns that
// judgement. This rule adds one refusal; it does not tighten the syntax.
func rejectTotalCoverage(field string, list []string) error {
	var nets []*net.IPNet
	var parsed []string
	for _, entry := range list {
		trimmed := strings.TrimSpace(entry)
		if _, n, err := net.ParseCIDR(trimmed); err == nil {
			nets = append(nets, n)
			parsed = append(parsed, trimmed)
		}
	}
	for _, bits := range []int{net.IPv4len * 8, net.IPv6len * 8} {
		if !CoversAddressFamily(nets, bits) {
			continue
		}
		// One entry doing it alone keeps the message this repo has always used; a set
		// doing it together names the set, because no single entry is at fault.
		if len(parsed) == 1 {
			return newDefaultRouteError(field, parsed[0])
		}
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    field,
			Message: fmt.Sprintf("entries %v together trust every address, which restores X-Forwarded-For spoofing",
				parsed),
			Action: actionListSpecificProxyRanges,
		}
	}
	return nil
}

// validateIPOrCIDRList rejects an entry that is neither a bare IP address nor a CIDR
// range. It is the allowlist counterpart to validateCIDRList: bare addresses are accepted
// because the shipped debug.allowedips default is ["127.0.0.1","::1"], which the strict
// proxy parser refuses.
//
// A default route is NOT an error here. An allowlist that admits everything is a
// legitimate posture — ADR-049 recommends ["0.0.0.0/0"] for exactly that — whereas a trust
// list that trusts everything re-opens header spoofing. The two keys look alike and mean
// opposite things.
//
// Host bits ARE an error, for the same reason ParseTrustedProxyCIDR refuses them on the
// proxy keys: "192.168.1.55/16" silently admits 65,536 hosts where the operator wrote one
// address, and nobody writes that intending a /16.
//
// Quotes are stripped exactly as app.IPWhitelist.cleanIPString strips them at runtime: a
// startup check stricter than the runtime parser would abort a deployment the runtime
// would have served (DEBUG_ALLOWEDIPS='"127.0.0.1"' works today).
//
// An entry that trims to empty is SKIPPED, not rejected — ADR-078's check inspects the raw
// koanf value and never sees one empty item inside a populated list, and NewIPWhitelist
// discards it silently. Deliberate, not a handoff.
func validateIPOrCIDRList(field string, list []string) error {
	for _, entry := range list {
		trimmed := strings.Trim(strings.TrimSpace(entry), "\"'")
		if trimmed == "" {
			continue
		}
		if net.ParseIP(trimmed) != nil {
			continue
		}
		ip, ipNet, err := net.ParseCIDR(trimmed)
		if err != nil {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    field,
				Message:  fmt.Sprintf("'%s' is neither an IP address nor a CIDR range", trimmed),
				Action:   "use an IP address (127.0.0.1) or a CIDR range (10.0.0.0/8); comma-separate multiple values in one env var",
			}
		}
		if !ip.Equal(ipNet.IP) {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    field,
				Message:  fmt.Sprintf("'%s' has host bits set, which silently widens the allowed range to %s", trimmed, ipNet),
				Action:   "write the network address (" + ipNet.String() + ") or the single host without a prefix",
			}
		}
	}
	return nil
}

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

// normalizeApp fills the startup timeout defaults.
func normalizeApp(cfg *AppConfig) error {
	return applyStartupDefaults(&cfg.Startup)
}

// checkApp rejects a missing Name or Version, an Env outside envFormat (see
// its docs for the policy), and negative rate limits.
func checkApp(cfg *AppConfig) error {
	if cfg.Name == "" {
		return NewMissingFieldError("app.name", "APP_NAME", "app.name")
	}

	if cfg.Version == "" {
		return NewMissingFieldError("app.version", "APP_VERSION", "app.version")
	}

	if !envFormat.MatchString(cfg.Env) {
		return NewInvalidFieldError(
			fieldAppEnv,
			fmt.Sprintf("'%s' must be 1-32 lowercase alphanumeric or hyphen, starting with a letter", cfg.Env),
			nil,
		)
	}

	if cfg.Rate.Limit < 0 {
		return NewValidationError(fieldAppRateLimit, errMustBeNonNegative)
	}

	if cfg.Rate.Burst < 0 {
		return NewValidationError("app.rate.burst", errMustBeNonNegative)
	}

	return nil
}

const (
	identityKeyHost = "host"
	identityKeyPort = "port"
)

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

// normalizeIANATimezone defaults an empty timezone to UTC and validates it as a
// loadable IANA name. The "-" sentinel passes through unchanged so callers can
// treat it as "disabled". field labels the validation error. Shared by config
// sections that carry IANA timezone fields (currently database and scheduler)
// so the contracts cannot drift.
func normalizeIANATimezone(field, value string) (string, error) {
	if value == "" {
		value = DefaultTimezone
	}
	if value == TimezoneDisabledSentinel {
		return value, nil
	}
	if _, err := time.LoadLocation(value); err != nil {
		return value, NewInvalidFieldError(
			field,
			fmt.Sprintf("invalid IANA timezone %q: %v", value, err),
			[]string{`a valid IANA timezone (e.g. "UTC", "America/New_York") or "-" to disable`},
		)
	}
	return value, nil
}

// validateCIDRList fails when a non-empty list contains zero parseable CIDRs.
// Empty lists are valid (localhost-only / no-trusted-proxy defaults). Partial-invalid
// lists pass here and keep the existing middleware-time WARN so a single typo does not
// crash startup, while an all-invalid security control fails fast instead of silently
// degrading to a more restrictive (or, for redaction, weaker) posture.
//
// The parse loop intentionally mirrors scheduler/cidr_middleware.go's parser; config
// cannot import scheduler (import cycle), so the few lines are duplicated rather than shared.
func validateCIDRList(field string, list []string) error {
	if len(list) == 0 {
		return nil
	}
	var invalid []string
	valid := 0
	for _, entry := range list {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(entry)); err != nil {
			invalid = append(invalid, entry)
			continue
		}
		valid++
	}
	if valid == 0 {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    field,
			Message:  fmt.Sprintf("no valid CIDR entries (all %d rejected: %v)", len(list), invalid),
			Action:   "use CIDR notation, e.g. 10.0.0.0/8; comma-separate multiple values in one env var",
		}
	}
	return nil
}

// sectionNamePattern is the grammar every USER-CHOSEN section key obeys:
// entries under databases, multitenant.tenants and keystore.keys. It is the
// resolver's tenant-ID grammar without the length bound, which stays the
// resolver's.
//
// The reason is reachability, not taste. Load maps an environment variable to
// a config key by lowercasing it and turning '_' into '.', which is not
// injective: DATABASES_REPORT_DB_PORT reaches databases.report.db.port, so a
// section named report_db cannot be addressed by any variable — its value
// either lands on a phantom key or, when a sibling named report exists,
// silently on the sibling. Uppercase is unreachable the same way. Rejecting
// the name at check makes the transform injective over every key that
// survives startup, without touching the transform itself (ADR-024).
//
// Hyphen is legal here; whether a hyphenated name is settable depends on the
// runtime (Docker and Kubernetes permit '-' in variable names, POSIX `export`
// does not), which the docs state and this rule does not police.
var sectionNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// checkSectionName rejects a user-chosen section key no environment variable
// can address. field is the key PATH, so an operator can find the entry.
func checkSectionName(field, name string) error {
	if sectionNamePattern.MatchString(name) {
		return nil
	}
	return &ConfigError{
		Category: errCategoryInvalid,
		Message:  fmt.Sprintf("name %q is not reachable by an environment variable", name),
		Field:    field,
		Action:   "rename it using lowercase letters, digits and '-' only: an environment variable lowercases and maps '_' to the config path delimiter, so any other name is unaddressable",
	}
}

// validateNamedDatabaseName checks the map key: non-empty, not the reserved
// prefix, reachable by an environment variable (checkSectionName), and not
// colliding with a static tenant ID.
func validateNamedDatabaseName(name string, mt *MultitenantConfig) error {
	if name == "" {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabases,
			Message:  "database name cannot be empty",
			Action:   "provide a non-empty key for each entry in databases section",
		}
	}
	// A '.' collides with koanf's path delimiter: constructed section paths
	// (databases.<name>) become ambiguous, so the bare "databases" Field is used
	// here rather than fmt.Sprintf(databasesFieldPrefix, name) — embedding the
	// dotted name would reproduce the same ambiguity in the error itself. This
	// runs BEFORE the reserved-prefix rule below, which does embed the name: a
	// name breaking both (`gb_.foo`) must still be reported against the parent.
	if strings.Contains(name, ".") {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabases,
			Message:  fmt.Sprintf("database name %q cannot contain '.' (the config path delimiter)", name),
			Action:   "rename the databases entry without dots",
		}
	}
	if strings.HasPrefix(name, NamedDatabasePrefix) {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fmt.Sprintf(databasesFieldPrefix, name),
			Message:  fmt.Sprintf("name cannot start with reserved prefix '%s'", NamedDatabasePrefix),
			Action:   fmt.Sprintf("rename databases.%s to remove the '%s' prefix", name, NamedDatabasePrefix),
		}
	}
	// Everything the dot rule above does not already reject is judged by the
	// shared reachability grammar. It runs after that rule because a dotted
	// name cannot carry an unambiguous Field, which this one needs.
	if err := checkSectionName(fmt.Sprintf(databasesFieldPrefix, name), name); err != nil {
		return err
	}
	if mt.Enabled && mt.Tenants != nil {
		if _, exists := mt.Tenants[name]; exists {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fmt.Sprintf(databasesFieldPrefix, name),
				Message:  fmt.Sprintf("name conflicts with tenant ID '%s'", name),
				Action:   fmt.Sprintf("rename databases.%s or multitenant.tenants.%s to avoid conflict", name, name),
			}
		}
	}
	return nil
}

// applyModeAwarePoolDefault handles pool-size keys whose multi-tenant default is
// dynamic (the builder scales the pool to the tenant limit when the key is unset):
// zero is preserved in multi-tenant mode so that scaling can happen, negative is
// always rejected, and single-tenant zero gets the flat default. Stamping the flat
// default in multi-tenant mode would silently cap the pool below the tenant limit
// (#661). Shared by the messaging, cache, and database manager appliers.
func applyModeAwarePoolDefault(field *int, def int, name string, multitenant bool) error {
	if multitenant {
		if *field < 0 {
			return NewValidationError(name, errMustBeNonNegative)
		}
		return nil
	}
	return applyNonNegativeDefault(field, def, name)
}

// applyNonNegativeDefault sets *field to def when it is zero, or returns a validation
// error for the named config key when it is negative. Shared across default-appliers
// to keep the "zero applies the default, negative is invalid" rule in one place.
func applyNonNegativeDefault[T ~int64 | ~int](field *T, def T, name string) error {
	switch {
	case *field == 0:
		*field = def
	case *field < 0:
		return NewValidationError(name, errMustBeNonNegative)
	}
	return nil
}

// applyCacheManagerDefaults sets production-safe defaults for cache manager configuration.
//
// It modifies cfg in-place:
//   - Manager.IdleTTL: if 0, sets to 15m; if negative, returns an error.
//   - Manager.CleanupInterval: if 0, sets to 5m; if negative, returns an error.
//   - Manager.MaxSize: if 0, sets to 100 when multitenant is false; in multi-tenant
//     mode zero is preserved so app.ManagerConfigBuilder.BuildCacheOptions scales the
//     cache pool to the tenant limit; if negative, returns an error.
//
// IdleTTL/CleanupInterval have no mode-specific defaults (cache carries no such constants).
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyCacheManagerDefaults(cfg *CacheConfig, multitenant bool) error {
	if err := applyNonNegativeDefault(&cfg.Manager.IdleTTL, defaultCacheIdleTTL, "cache.manager.idlettl"); err != nil {
		return err
	}
	if err := applyNonNegativeDefault(&cfg.Manager.CleanupInterval, defaultCacheCleanupInterval, "cache.manager.cleanupinterval"); err != nil {
		return err
	}

	return applyModeAwarePoolDefault(&cfg.Manager.MaxSize, defaultCacheMaxSize, "cache.manager.maxsize", multitenant)
}

// applyDatabaseManagerDefaults sets production-safe defaults for the database
// connection-manager pool.
//
// It modifies cfg in-place:
//   - IdleTTL: if 0, sets to 1h single-tenant / 30m multi-tenant; if negative, errors.
//   - CleanupInterval: if 0, sets to 5m; if negative, errors.
//   - MaxSize: if 0, sets to 10 when multitenant is false; in multi-tenant mode
//     zero is preserved so app.ManagerConfigBuilder.BuildDatabaseOptions scales
//     the handle pool to the tenant limit; if negative, errors.
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyDatabaseManagerDefaults(cfg *DatabaseManagerConfig, multitenant bool) error {
	idleTTLDefault := defaultDatabaseManagerIdleTTL
	if multitenant {
		idleTTLDefault = defaultDatabaseManagerIdleTTLMultiTenant
	}
	if err := applyNonNegativeDefault(&cfg.IdleTTL, idleTTLDefault, "database.manager.idlettl"); err != nil {
		return err
	}
	if err := applyNonNegativeDefault(&cfg.CleanupInterval, defaultDatabaseManagerCleanupInterval, "database.manager.cleanupinterval"); err != nil {
		return err
	}

	return applyModeAwarePoolDefault(&cfg.MaxSize, defaultDatabaseManagerMaxSize, "database.manager.maxsize", multitenant)
}

// applyTimeoutDefault validates and applies default to a component timeout.
// Fallback hierarchy: explicit value > global fallback > per-component default.
// Returns an error if the value is negative.
func applyTimeoutDefault(
	value *time.Duration,
	fieldName string,
	globalWasSet bool,
	globalTimeout time.Duration,
	componentDefault time.Duration,
) error {
	if *value < 0 {
		return NewValidationError(fieldName, errMustBeNonNegative)
	}
	if *value == 0 {
		if globalWasSet {
			*value = globalTimeout
		} else {
			*value = componentDefault
		}
	}
	return nil
}

// applyStartupDefaults sets production-safe defaults for startup configuration.
//
// Fallback hierarchy for component timeouts:
//  1. Explicit component value (preserved if set)
//  2. Global Timeout (used when component is 0 and Timeout was explicitly set)
//  3. Per-component default (used when both component and original Timeout are 0)
//
// Default values:
// - Timeout: 10s, Database: 10s, Messaging: 10s, Cache: 5s, Observability: 15s
//
// Returns an error when any value is negative; otherwise returns nil.
func applyStartupDefaults(cfg *StartupConfig) error {
	// Capture whether global timeout was originally set (non-zero)
	globalWasSet := cfg.Timeout != 0

	// Validate and default the global timeout first
	if cfg.Timeout < 0 {
		return NewValidationError("app.startup.timeout", errMustBeNonNegative)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultStartupTimeout
	}

	// Apply defaults to each component using helper
	if err := applyTimeoutDefault(&cfg.Database, "app.startup.database",
		globalWasSet, cfg.Timeout, defaultStartupDatabaseTimeout); err != nil {
		return err
	}
	if err := applyTimeoutDefault(&cfg.Messaging, "app.startup.messaging",
		globalWasSet, cfg.Timeout, defaultStartupMessagingTimeout); err != nil {
		return err
	}
	if err := applyTimeoutDefault(&cfg.Cache, "app.startup.cache",
		globalWasSet, cfg.Timeout, defaultStartupCacheTimeout); err != nil {
		return err
	}
	if err := applyTimeoutDefault(&cfg.Observability, "app.startup.observability",
		globalWasSet, cfg.Timeout, defaultStartupObservabilityTimeout); err != nil {
		return err
	}

	return nil
}

// normalizeKeyStore fills the nil default: an unset SecretMinLength becomes
// DefaultKeyStoreSecretMinLength (32). An explicit 0 or N is left untouched —
// 0 keeps the floor off (deprecated) and check rejects a negative. Nothing
// here can fail.
func normalizeKeyStore(cfg *KeyStoreConfig) {
	if cfg.SecretMinLength == nil {
		cfg.SecretMinLength = new(cfg.SecretFloor())
	}
}

// checkKeyStore returns nil if no keys are configured. A set SecretMinLength
// must be non-negative — nil is left alone since white-box tests call
// checkKeyStore directly, before normalize has filled it. Each entry is
// either an RSA pair (public required with exactly one source, private
// optional) or a symmetric secret — a mixed entry is rejected. Each entry's
// NAME is judged first, against the env-reachability grammar.
func checkKeyStore(cfg *KeyStoreConfig) error {
	if cfg.SecretMinLength != nil && *cfg.SecretMinLength < 0 {
		return NewValidationError("keystore.secretminlength", errMustBeNonNegative)
	}

	if len(cfg.Keys) == 0 {
		return nil
	}

	// Sort keys for deterministic error ordering
	names := make([]string, 0, len(cfg.Keys))
	for name := range cfg.Keys {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		// A '.' collides with koanf's path delimiter: the constructed section
		// path keystore.keys.<name>.public becomes ambiguous, and so would this
		// name's own error Field — keystore.keys.my.key reads as a "key" under
		// "my". The parent field is reported instead, as the databases and
		// static-tenant rules do, and this runs first so the ambiguous path is
		// never built.
		if strings.Contains(name, ".") {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldKeystoreKeys,
				Message:  fmt.Sprintf("key name %q cannot contain '.' (the config path delimiter)", name),
				Action:   "rename the keystore.keys entry without dots",
			}
		}
		// The name is judged before its sources: an unreachable entry cannot be
		// configured by environment variable whatever its file or value says.
		if err := checkSectionName(fmt.Sprintf(keystoreKeysFieldPrefix, name), name); err != nil {
			return err
		}
		kp := cfg.Keys[name]
		if err := validateKeyEntry(&kp, name); err != nil {
			return err
		}
	}
	return nil
}

// validateKeyEntry validates a single keystore entry. An entry is either an
// RSA pair (public required, private optional) or a symmetric secret — a mixed
// entry is a structural error detected here without an explicit discriminator.
func validateKeyEntry(kp *KeyPairConfig, name string) error {
	hasSecret := kp.Secret.IsSet()
	hasAsymmetric := kp.Public.IsSet() || kp.Private.IsSet()

	if hasSecret && hasAsymmetric {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fmt.Sprintf(keystoreKeysFieldPrefix, name),
			Message:  "entry has both a symmetric 'secret' and asymmetric 'public'/'private' material",
			Action:   "configure an entry as either a 'secret' or an RSA pair, not both",
		}
	}

	if hasSecret {
		return validateKeySource(kp.Secret, name, "secret", true)
	}

	if err := validateKeySource(kp.Public, name, "public", true); err != nil {
		return err
	}
	return validateKeySource(kp.Private, name, "private", false)
}

// validateKeySource checks that a key source has exactly one of file or value set.
// If required is true, at least one source must be configured.
func validateKeySource(src KeySourceConfig, keyName, keyType string, required bool) error {
	hasFile := src.File != ""
	hasValue := src.Value != ""

	if hasFile && hasValue {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fmt.Sprintf("keystore.keys.%s.%s", keyName, keyType),
			Message:  "both 'file' and 'value' set",
			Action:   "use exactly one of 'file' or 'value'",
		}
	}
	if required && !src.IsSet() {
		return &ConfigError{
			Category: errCategoryMissing,
			Field:    fmt.Sprintf("keystore.keys.%s.%s", keyName, keyType),
			Message:  "key source required",
			Action:   "set either 'file' (path) or 'value' (base64)",
		}
	}
	return nil
}

// checkLog rejects an unsupported log level, listing the allowed values.
func checkLog(cfg *LogConfig) error {
	validLevels := []string{logger.LevelTrace, logger.LevelDebug, logger.LevelInfo, logger.LevelWarn, logger.LevelError, logger.LevelFatal, logger.LevelPanic}
	if !slices.Contains(validLevels, cfg.Level) {
		return NewInvalidFieldError(fieldLogLevel, fmt.Sprintf(errNotSupportedFmt, cfg.Level), validLevels)
	}

	return nil
}

// normalizeCache fills Redis defaults unconditionally, even when the cache is
// disabled — koanf already fills cache.redis.* in that state, and an enabled
// hand-built cache with a zero port/poolsize must not fail where koanf gives
// 6379/10. Manager defaults (mode-dependent MaxSize, see
// applyCacheManagerDefaults) fill only when enabled: koanf carries no
// cache.manager.* defaults, and a disabled cache's negative manager value is
// CreateCacheManager's to reject (ADR-054), not Validate's.
func normalizeCache(cfg *CacheConfig, multitenant bool) error {
	applyRedisDefaults(&cfg.Redis)

	if cfg.Enabled {
		return applyCacheManagerDefaults(cfg, multitenant)
	}
	return nil
}

// checkCache rejects an enabled cache's type and Redis fields; a disabled
// cache is not checked.
func checkCache(cfg *CacheConfig) error {
	if !cfg.Enabled {
		return nil
	}

	validTypes := []string{CacheTypeRedis}
	if !slices.Contains(validTypes, cfg.Type) {
		return NewInvalidFieldError("cache.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), validTypes)
	}
	return validateRedisCache(&cfg.Redis)
}

// applyRedisDefaults fills in production-safe Redis defaults for any unset
// fields. The top-level cache.* config receives these via koanf, but per-tenant
// cache config (multitenant.tenants.<id>.cache.*) has no koanf defaults, so this
// is the only place those values are populated for tenant caches. Host is left
// untouched: a missing host is a real misconfiguration that must fail fast.
func applyRedisDefaults(cfg *RedisConfig) {
	if cfg.Port == 0 {
		cfg.Port = defaultRedisPort
	}
	if cfg.PoolSize == 0 {
		cfg.PoolSize = defaultRedisPoolSize
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = defaultRedisDialTimeout
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = defaultRedisReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = defaultRedisWriteTimeout
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaultRedisMaxRetries
	}
	if cfg.MinRetryBackoff == 0 {
		cfg.MinRetryBackoff = defaultRedisMinRetryBackoff
	}
	if cfg.MaxRetryBackoff == 0 {
		cfg.MaxRetryBackoff = defaultRedisMaxRetryBackoff
	}
}

// validateRedisCache validates Redis-specific cache configuration.
func validateRedisCache(cfg *RedisConfig) error {
	if cfg.Host == "" {
		return NewMissingFieldError("cache.redis.host", "CACHE_REDIS_HOST", "cache.redis.host")
	}

	if cfg.Port <= 0 || cfg.Port > 65535 {
		return NewInvalidFieldError("cache.redis.port", fmt.Sprintf(errInvalidField, cfg.Port), []string{portRange})
	}

	if cfg.Database < 0 || cfg.Database > 15 {
		return NewValidationError(fieldCacheRedisDB, "must be between 0 and 15")
	}

	if cfg.PoolSize <= 0 {
		return NewValidationError(fieldCacheRedisPool, errMustBePositive)
	}

	if cfg.DialTimeout < 0 {
		return NewValidationError("cache.redis.dialtimeout", errMustBeNonNegative)
	}

	if cfg.ReadTimeout < -1 {
		return NewValidationError("cache.redis.readtimeout", "must be >= -1")
	}

	if cfg.WriteTimeout < -1 {
		return NewValidationError("cache.redis.writetimeout", "must be >= -1")
	}

	return nil
}

// normalizeMultitenant shapes the multitenant section: resolver and limits
// fills, then (static source only) each tenant's database (opaque) and cache.
// Map-key rules (empty/dotted ID, messaging consistency, single-tenant
// conflicts) are check's — see checkMultitenant.
func normalizeMultitenant(mt *MultitenantConfig, source *SourceConfig) error {
	if !mt.Enabled {
		return nil
	}

	normalizeMultitenantResolver(&mt.Resolver)
	normalizeMultitenantLimits(&mt.Limits)

	if hasStaticTenants(source, mt) {
		if err := normalizeMultitenantTenants(mt.Tenants); err != nil {
			return fmt.Errorf("tenants: %w", err)
		}
	}

	return nil
}

// hasStaticTenants is the phase-shared gate for the static tenant map: dynamic
// sources load tenants from an external store and never reach it. A delivered
// but empty static map is not "has tenants" — check rejects that case.
func hasStaticTenants(source *SourceConfig, mt *MultitenantConfig) bool {
	return source.Type == SourceTypeStatic && len(mt.Tenants) > 0
}

// checkMultitenant rejects a normalized multitenant section without changing
// it: resolver and limits enumerations, source type, and (static source only)
// the tenant map's key rules and its conflicts with single-tenant config.
func checkMultitenant(mt *MultitenantConfig, db *DatabaseConfig, msg *MessagingConfig, source *SourceConfig) error {
	if !mt.Enabled {
		return nil
	}

	if err := checkMultitenantResolver(&mt.Resolver); err != nil {
		return fmt.Errorf("resolver: %w", err)
	}

	if err := checkMultitenantLimits(&mt.Limits); err != nil {
		return fmt.Errorf("limits: %w", err)
	}

	if err := validateSourceConfig(source); err != nil {
		return fmt.Errorf("source: %w", err)
	}

	// For static sources, validate tenants if provided (optional but must be valid if present)
	// For dynamic sources, tenants are optional and loaded from external store
	if source.Type == SourceTypeStatic && mt.Tenants != nil {
		if len(mt.Tenants) == 0 {
			return errors.New("tenants: empty map provided - either omit tenants section or provide at least one tenant for static source")
		}

		if err := checkTenantMessagingConsistency(mt.Tenants); err != nil {
			return fmt.Errorf("tenants: %w", err)
		}

		if err := checkTenantMessagingReachable(mt.Tenants, msg); err != nil {
			return fmt.Errorf("tenants: %w", err)
		}

		// Sorted, like forEachDatabaseSection: with several malformed tenants the
		// startup error names the same one every run.
		for _, tenantID := range slices.Sorted(maps.Keys(mt.Tenants)) {
			tenant := mt.Tenants[tenantID]
			if err := checkMultitenantTenantEntry(tenantID, &tenant); err != nil {
				return fmt.Errorf("tenants: %w", err)
			}
		}
	}

	if hasStaticTenants(source, mt) {
		return validateNoSingleTenantConflict(db, msg)
	}

	return nil
}

// checkMultitenantTenantEntry rejects one static tenant's ID and cache
// section: an empty or dotted ID, an ID no environment variable can address
// (checkSectionName), or (once the ID is valid) whatever checkTenantCache
// rejects.
func checkMultitenantTenantEntry(tenantID string, entry *TenantEntry) error {
	if tenantID == "" {
		return NewValidationError(fieldMultitenantTenants, "tenant ID cannot be empty")
	}
	// A '.' collides with koanf's path delimiter: the constructed section path
	// multitenant.tenants.<id>.database becomes ambiguous. Koanf has no
	// delimiter escaping, so fail fast rather than let a later lookup consult
	// the wrong flattened key.
	if strings.Contains(tenantID, ".") {
		return NewValidationError(fieldMultitenantTenants,
			fmt.Sprintf("tenant ID %q cannot contain '.' (the config path delimiter)", tenantID))
	}
	// Static tenant map keys only. A dynamic source never reaches here (see
	// checkMultitenant's SourceTypeStatic gate); the resolver's own grammar is
	// its request-time gate.
	if err := checkSectionName(fmt.Sprintf(tenantsFieldPrefix, tenantID), tenantID); err != nil {
		return err
	}
	return checkTenantCache(tenantID, &entry.Cache)
}

// validateNoSingleTenantConflict checks for conflicts with single-tenant configuration
func validateNoSingleTenantConflict(db *DatabaseConfig, msg *MessagingConfig) error {
	if IsDatabaseConfigured(db) {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabase,
			Message:  "not allowed when static tenants are configured",
			Action:   "remove database section from root config or move to multitenant.tenants.<tenant_id>.database",
		}
	}
	// Under shared tenancy the root block IS the control-plane broker every tenant
	// is served from, so it is the configuration this mode requires, not a conflict.
	if IsMessagingConfigured(msg) && msg.Tenancy != TenancyShared {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldMessaging,
			Message:  "not allowed when static tenants are configured",
			Action:   "remove messaging section from root config or move to multitenant.tenants.<tenant_id>.messaging",
		}
	}
	return nil
}

// normalizeMultitenantResolver fills the header default and, when the config
// builds a subdomain resolver, trims a delivered domain and prefixes it with
// '.'. An empty domain stays empty for checkMultitenantResolver to reject.
func normalizeMultitenantResolver(cfg *ResolverConfig) {
	if cfg.Header == "" {
		cfg.Header = "X-Tenant-ID"
	}
	if buildsSubdomainResolver(cfg) && domainDelivered(cfg.Domain) {
		cfg.Domain = strings.TrimSpace(cfg.Domain)
		if !strings.HasPrefix(cfg.Domain, ".") {
			cfg.Domain = "." + cfg.Domain
		}
	}
}

// checkMultitenantResolver rejects an unknown resolver type, a composite order
// that is missing or malformed, and the field requirements each type implies
// (subdomain root domain, path prefix/segment).
func checkMultitenantResolver(cfg *ResolverConfig) error {
	validTypes := []string{ResolverTypeHeader, ResolverTypeSubdomain, ResolverTypePath, ResolverTypeComposite}
	if !slices.Contains(validTypes, cfg.Type) {
		return NewInvalidFieldError("multitenant.resolver.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), validTypes)
	}

	if err := validateResolverOrder(cfg); err != nil {
		return err
	}

	if err := validateSubdomainResolverFields(cfg); err != nil {
		return err
	}
	return validatePathResolverFields(cfg)
}

// validateResolverOrder validates the composite sub-resolver order. Order is
// only meaningful for type: composite — setting it on any other type is
// rejected rather than silently ignored. For type: composite, Order is
// REQUIRED — there is no implicit default. Any default (header-last or
// header-first) is an unverifiable bet on the deployment's edge topology, and
// a caller-controlled X-Tenant-ID header must never silently outrank a
// resolver the operator explicitly wired up. See DefaultResolverOrder.
func validateResolverOrder(cfg *ResolverConfig) error {
	if cfg.Type != ResolverTypeComposite {
		if len(cfg.Order) > 0 {
			return NewValidationError(fieldResolverOrder, "only valid when multitenant.resolver.type is 'composite'")
		}
		return nil
	}

	if len(cfg.Order) == 0 {
		err := NewMissingFieldError(fieldResolverOrder, "MULTITENANT_RESOLVER_ORDER", fieldResolverOrder)
		err.Message = "required when multitenant.resolver.type is 'composite' — no implicit default"
		err.Details = []string{
			"recommended: [subdomain, path, header]",
			"if a trusted gateway strips and sets X-Tenant-ID, use a header-first order instead, e.g. [header, subdomain, path]",
		}
		return err
	}

	seen := make(map[string]bool, len(cfg.Order))
	for _, entry := range cfg.Order {
		if !slices.Contains(resolverOrderEntries, entry) {
			return NewInvalidFieldError(fieldResolverOrder, fmt.Sprintf(errNotSupportedFmt, entry), resolverOrderEntries)
		}
		if seen[entry] {
			return NewValidationError(fieldResolverOrder, fmt.Sprintf("duplicate entry %q", entry))
		}
		seen[entry] = true
	}
	return nil
}

func validateSubdomainResolverFields(cfg *ResolverConfig) error {
	if !buildsSubdomainResolver(cfg) {
		return nil
	}
	if !domainDelivered(cfg.Domain) {
		return NewMissingFieldError("multitenant.resolver.domain", "MULTITENANT_RESOLVER_DOMAIN", "multitenant.resolver.domain")
	}
	return nil
}

// buildsSubdomainResolver reports whether the config will construct a
// subdomain resolver: type subdomain, or a composite whose order includes
// one. Order is required and checked separately, so a composite reaching
// the check always has an explicit, non-empty Order.
func buildsSubdomainResolver(cfg *ResolverConfig) bool {
	switch cfg.Type {
	case ResolverTypeSubdomain:
		return true
	case ResolverTypeComposite:
		return slices.Contains(cfg.Order, ResolverTypeSubdomain)
	default:
		return false
	}
}

// domainDelivered treats "." and whitespace as no domain: once the leading dot
// is trimmed nothing is left, and newSubdomainResolver builds nil from that.
func domainDelivered(domain string) bool {
	return strings.TrimPrefix(strings.TrimSpace(domain), ".") != ""
}

// validatePathResolverFields enforces path-segment + prefix rules for the path
// resolver and for composite configurations that opt into a path sub-resolver
// (cfg.Order containing "path" indicates intent to include path — Order is
// required and validated before this runs, so it is always explicit here).
func validatePathResolverFields(cfg *ResolverConfig) error {
	required := cfg.Type == ResolverTypePath ||
		(cfg.Type == ResolverTypeComposite && slices.Contains(cfg.Order, ResolverTypePath))
	if !required {
		return nil
	}
	if cfg.Path.Segment <= 0 {
		return NewValidationError("multitenant.resolver.path.segment", errMustBePositive)
	}
	if cfg.Path.Prefix != "" && !strings.HasPrefix(cfg.Path.Prefix, "/") {
		return NewValidationError("multitenant.resolver.path.prefix", "must start with '/' when set")
	}
	return nil
}

// normalizeMultitenantLimits fills the tenant-count default. A negative value
// is treated the same as zero — kept intentionally, not tightened to reject.
func normalizeMultitenantLimits(cfg *LimitsConfig) {
	if cfg.Tenants <= 0 {
		cfg.Tenants = 100 // default
	}
}

// checkMultitenantLimits rejects a tenant cap above 1000; zero and negatives
// were already defaulted by normalizeMultitenantLimits.
func checkMultitenantLimits(cfg *LimitsConfig) error {
	if cfg.Tenants > 1000 {
		return NewValidationError("multitenant.limits.tenants", "cannot exceed 1000")
	}
	return nil
}

// normalizeMultitenantTenants shapes each static tenant's database (opaque)
// and cache, and writes the result back to the map. The tenant-ID rules
// (empty, dotted) and cross-tenant messaging consistency are check's — see
// checkMultitenant.
func normalizeMultitenantTenants(tenants map[string]TenantEntry) error {
	// Sorted, like forEachDatabaseSection: with several malformed tenants the
	// startup error names the same one every run.
	for _, tenantID := range slices.Sorted(maps.Keys(tenants)) {
		tenant := tenants[tenantID]

		if err := normalizeDatabaseSection(&tenant.Database, tenantDatabaseSection(tenantID)); err != nil {
			return err
		}

		if err := normalizeTenantCache(&tenant.Cache); err != nil {
			return err
		}

		// Persist defaults back to the map (see normalizeNamedDatabases for rationale).
		tenants[tenantID] = tenant
	}

	return nil
}

// checkTenantMessagingConsistency enforces all-or-nothing messaging configuration
// across tenants: if any tenant has messaging configured, all must have it
// configured. This prevents confusing scenarios where some tenants can use
// messaging and others cannot.
func checkTenantMessagingConsistency(tenants map[string]TenantEntry) error {
	hasAnyMessaging := false
	hasNoMessaging := false

	for tenantID := range tenants {
		tenant := tenants[tenantID]
		if isTenantMessagingConfigured(&tenant.Messaging) {
			hasAnyMessaging = true
		} else {
			hasNoMessaging = true
		}
	}

	if hasAnyMessaging && hasNoMessaging {
		return &ConfigError{
			Category: errCategoryInvalid,
			// A wildcard segment, not a literal: this is a whole-map invariant, and
			// "multitenant.tenants.messaging" would be indistinguishable from a tenant
			// actually named "messaging" (tenantField's sentinel emits exactly that).
			Field:   "multitenant.tenants.*.messaging",
			Message: "inconsistent configuration",
			Action:  "either all tenants must have messaging configured or none should",
		}
	}
	return nil
}

// checkTenantMessagingReachable rejects per-tenant messaging blocks that shared
// tenancy would never read: under messaging.tenancy: shared every consumer and
// publisher resolves the control-plane key, so a tenant broker URL is a silently
// dead setting rather than a working per-tenant broker.
func checkTenantMessagingReachable(tenants map[string]TenantEntry, msg *MessagingConfig) error {
	if msg.Tenancy != TenancyShared {
		return nil
	}
	for tenantID := range tenants {
		tenant := tenants[tenantID]
		if isTenantMessagingConfigured(&tenant.Messaging) {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    "multitenant.tenants.*.messaging",
				Message:  "unreachable under messaging.tenancy: " + TenancyShared,
				Action:   "remove the per-tenant messaging blocks or set messaging.tenancy: " + TenancyPerTenant,
			}
		}
	}
	return nil
}

// normalizeTenantCache shapes a tenant's cache configuration with the same
// fail-fast posture as the tenant database: an enabled-but-misconfigured
// cache must crash at startup, not at the first per-request cache access (see
// tenant_store.go CacheConfig). Per-tenant cache keys have no koanf defaults,
// so the type defaults to redis here before normalizeCache fills the rest.
func normalizeTenantCache(cache *CacheConfig) error {
	if cache.Enabled && cache.Type == "" {
		cache.Type = CacheTypeRedis
	}
	// per-tenant caches only exist in multi-tenant mode
	return normalizeCache(cache, true)
}

// checkTenantCache is checkCache addressed to the tenant. The tenant travels in Field, not
// in a wrapping message: a consumer matching on ConfigError.Field could not otherwise tell
// which tenant's cache failed, and the database sections next door already spell it this
// way (C60.19). The addressing itself is the exported door, so the startup and runtime cache
// doors cannot drift apart.
func checkTenantCache(tenantID string, cache *CacheConfig) error {
	return QualifyCacheConfigErrorForKey(checkCache(cache), tenantID)
}

// validateSourceConfig validates the source configuration type
func validateSourceConfig(cfg *SourceConfig) error {
	if cfg.Type != SourceTypeStatic && cfg.Type != SourceTypeDynamic {
		return NewInvalidFieldError("source.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), []string{"static", "dynamic"})
	}
	return nil
}
