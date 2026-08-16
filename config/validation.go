package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
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

	fieldMessagingStreamsURI = "messaging.streams.uri"

	fieldDatabaseTLS = "database.tls"

	sslModeDisable    = "disable"
	sslModeAllow      = "allow"
	sslModePrefer     = "prefer"
	sslModeRequire    = "require"
	sslModeVerifyCA   = "verify-ca"
	sslModeVerifyFull = "verify-full"
)

// checkDebug rejects an entirely unparseable debug.trustedproxies list.
// Semantics match scheduler.security.trustedproxies: empty is valid (proxy
// headers ignored), an all-invalid list fails fast, and a partial-invalid list
// passes with a middleware-time WARN so a single typo cannot silently weaken
// the allowlist's spoofing protection.
func checkDebug(cfg *DebugConfig) error {
	return validateCIDRList("debug.trustedproxies", cfg.TrustedProxies)
}

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

// checkMessaging rejects an inverted reconnect.maxdelay/delay pair, then the
// streams block.
func checkMessaging(cfg *MessagingConfig, multitenant bool) error {
	// Both sides are defaulted by normalizeMessaging, so this compares effective
	// values. computeBackoff silently clamps an inverted pair (maxdelay <
	// delay), which would leave the configured ceiling ignored.
	if cfg.Reconnect.MaxDelay < cfg.Reconnect.Delay {
		return NewValidationError("messaging.reconnect.maxdelay", "must be >= messaging.reconnect.delay")
	}

	return checkMessagingStreams(&cfg.Streams, multitenant)
}

// checkMessagingStreams rejects a stream URI with an unsupported scheme or no
// host, streams in multi-tenant mode, and a half-set address resolver.
//
// SECURITY: messaging.streams.uri carries broker credentials, so no error raised
// here echoes the URI — only the config key and the offending scheme reach the
// message.
func checkMessagingStreams(cfg *StreamsConfig, multitenant bool) error {
	if cfg.URI != "" {
		// Multi-tenant stream consumption would need one Environment per tenant and a
		// per-tenant stream URI leg; until that exists the combination fails loudly
		// instead of consuming one tenant's streams on behalf of all of them.
		if multitenant {
			return NewValidationError("messaging.streams",
				"single-tenant only; multi-tenant stream consumption is not yet supported")
		}

		u, err := url.Parse(cfg.URI)
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

	return validateStreamsAddressResolver(&cfg.AddressResolver)
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

// checkServer rejects a server section the server could not start from.
func checkServer(cfg *ServerConfig) error {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return NewInvalidFieldError(fieldServerPort, fmt.Sprintf(errInvalidField, cfg.Port), []string{portRange})
	}

	if cfg.Timeout.Read <= 0 {
		return NewValidationError("server.timeout.read", errMustBePositive)
	}

	if cfg.Timeout.Write <= 0 {
		return NewValidationError("server.timeout.write", errMustBePositive)
	}

	if cfg.Timeout.Middleware <= 0 {
		return NewValidationError("server.timeout.middleware", errMustBePositive)
	}

	// Middleware timeout should be less than write timeout to allow graceful error responses
	// Otherwise the write timeout will trigger first, causing connection drops
	if cfg.Timeout.Middleware >= cfg.Timeout.Write {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    "server.timeout.middleware",
			Message:  fmt.Sprintf("must be less than server.timeout.write (%v)", cfg.Timeout.Write),
			Action:   "reduce server.timeout.middleware or increase server.timeout.write",
		}
	}

	if cfg.Timeout.Shutdown <= 0 {
		return NewValidationError("server.timeout.shutdown", errMustBePositive)
	}

	if cfg.Gzip.MinLength < 0 {
		return NewValidationError("server.gzip.minlength", errMustBeNonNegative)
	}

	// A negative body limit is an operator error (a typo silently reverting to the
	// default is worse than a startup failure). Zero is permitted and resolves to
	// the framework default at wire-up (see server.SetupMiddlewares).
	if cfg.BodyLimit < 0 {
		return NewValidationError("server.bodylimit", errMustBeNonNegative)
	}

	if err := validateServerTLS(&cfg.TLS); err != nil {
		return err
	}

	if err := validateServerTrustedProxies(cfg.TrustedProxies); err != nil {
		return err
	}

	return validateServerForwardedClientCert(&cfg.ForwardedClientCert)
}

// Rejection reasons from ParseTrustedProxyCIDR. Unexported: only this package
// needs to tell them apart (to pick an Action string); server just skips and logs.
var (
	errTrustedProxyInvalidCIDR  = errors.New("not a valid CIDR range")
	errTrustedProxyHostBits     = errors.New("host bits set, which silently widens the trusted range")
	errTrustedProxyDefaultRoute = errors.New("trusts every address, which restores X-Forwarded-For spoofing")
)

// ParseTrustedProxyCIDR parses one server.trustedproxies entry and rejects every
// shape that would make the list trust more than the operator wrote:
//
//   - anything net.ParseCIDR cannot parse, including a bare address — an operator
//     writing a single host gets an error instead of a silently dropped entry;
//   - an entry whose host bits are set: net.ParseCIDR accepts 10.1.2.3/8 and
//     silently masks it to 10.0.0.0/8, widening the range past what was written;
//   - a default route, which trusts every hop, so echo's walk finds nothing
//     untrusted and returns the caller-authored left-most X-Forwarded-For entry.
//
// Surrounding whitespace is trimmed, matching validateCIDRList and
// server.ParseCIDRs, so a YAML sequence entry with incidental spacing is accepted.
//
// Both config validation and the server's extractor wiring call this, so the rule
// set cannot drift between what startup accepts and what actually gets trusted.
// On the host-bits rejection the returned net is the masked range the entry would
// have silently become, so a caller can name it; every other failure returns nil.
func ParseTrustedProxyCIDR(entry string) (*net.IPNet, error) {
	ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(entry))
	if err != nil {
		return nil, errTrustedProxyInvalidCIDR
	}

	if !ip.Equal(ipNet.IP) {
		return ipNet, errTrustedProxyHostBits
	}

	if ones, _ := ipNet.Mask.Size(); ones == 0 {
		return nil, errTrustedProxyDefaultRoute
	}

	return ipNet, nil
}

// validateServerTrustedProxies rejects any entry that would change who the
// client-IP extractor trusts in a way the operator did not write. A trusted
// proxy list is a security control, so a malformed entry aborts startup
// instead of being dropped with a warning: the difference between a trusted
// range and a missing one is invisible in behavior until it is abused.
func validateServerTrustedProxies(entries []string) error {
	for _, entry := range entries {
		ipNet, err := ParseTrustedProxyCIDR(entry)

		switch {
		case errors.Is(err, errTrustedProxyInvalidCIDR):
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldServerTrustedProxies,
				Message:  fmt.Sprintf("'%s' is not a valid CIDR range", entry),
				Action:   "use CIDR notation with a prefix length (a single host is /32 for IPv4 or /128 for IPv6)",
			}
		case errors.Is(err, errTrustedProxyHostBits):
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldServerTrustedProxies,
				Message:  fmt.Sprintf("'%s' has host bits set, which silently widens the trusted range", entry),
				Action:   fmt.Sprintf("write the masked form '%s' if that is the range you mean", ipNet.String()),
			}
		case errors.Is(err, errTrustedProxyDefaultRoute):
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldServerTrustedProxies,
				Message:  fmt.Sprintf("'%s' trusts every address, which restores X-Forwarded-For spoofing", entry),
				Action:   "list the specific proxy ranges to trust instead of a default route",
			}
		}
	}

	return nil
}

// validateServerForwardedClientCert rejects a Require-without-Enabled
// configuration: rejecting requests on an identity source that is never
// parsed would be a silent no-op (every request would be treated as missing
// an identity, since ForwardedClientCert.Enabled gates whether the
// middleware ever runs at all).
func validateServerForwardedClientCert(cfg *ForwardedClientCertConfig) error {
	if cfg.Require && !cfg.Enabled {
		return NewValidationError(fieldServerForwardedClientCertRequire, "requires server.forwardedclientcert.enabled")
	}

	return nil
}

// validateServerTLS checks structural TLS material configuration (presence
// and mutual exclusivity of file/value sources, min-version enum). It does
// NOT touch the filesystem — reading and parsing PEM material happens at
// Start() time (see server/tls.go), so a bad path here still fails fast, just
// one hop later.
func validateServerTLS(cfg *ServerTLSConfig) error {
	if !cfg.Enabled {
		return nil
	}

	if err := validateServerTLSMaterial(fieldServerTLSCertFile, fieldServerTLSCertValue, cfg.CertFile, cfg.CertValue); err != nil {
		return err
	}

	if err := validateServerTLSMaterial(fieldServerTLSKeyFile, fieldServerTLSKeyValue, cfg.KeyFile, cfg.KeyValue); err != nil {
		return err
	}

	switch cfg.MinVersion {
	case "", tlsVersion12, tlsVersion13:
		return nil
	default:
		return NewInvalidFieldError(fieldServerTLSMinVersion, fmt.Sprintf(errInvalidField, cfg.MinVersion), []string{tlsVersion12, tlsVersion13})
	}
}

// validateServerTLSMaterial enforces exactly one of a file/value pair is set
// for a single PEM piece (cert or key).
func validateServerTLSMaterial(fileField, valueField, file, value string) error {
	switch {
	case file == "" && value == "":
		return NewValidationError(fileField, "exactly one of "+fileField+" or "+valueField+" is required when server.tls.enabled is true")
	case file != "" && value != "":
		return NewValidationError(fileField, fileField+" and "+valueField+" are mutually exclusive (exactly one)")
	default:
		return nil
	}
}

// identityKeyHost and identityKeyPort name recurring identity-key suffixes as
// constants (goconst: both bare literals recur elsewhere in the package's
// test fixtures).
const (
	identityKeyHost = "host"
	identityKeyPort = "port"
)

// databaseIdentityKeys mirrors IsDatabaseConfigured's field set — the koanf
// key suffixes whose PRESENCE marks a database section as delivered. Keep the
// two in lockstep; TestDatabaseIdentityKeysMatchPredicate pins it.
var databaseIdentityKeys = []string{
	"connectionstring", "type", identityKeyHost, identityKeyPort, fieldDatabase,
	"username", "password", "oracle.service.name", "oracle.service.sid",
}

// pgSSLModes mirrors the sslmode values pgx v5 accepts in configTLS; anything
// else fails only at connect time with a redacted parse error, so gate it here.
var pgSSLModes = []string{sslModeDisable, sslModeAllow, sslModePrefer, sslModeRequire, sslModeVerifyCA, sslModeVerifyFull}

// pgTLSMandatorySSLModes are the modes under which pgx is guaranteed to use
// configured TLS material; the opportunistic modes silently discard or
// downgrade it.
var pgTLSMandatorySSLModes = []string{sslModeRequire, sslModeVerifyCA, sslModeVerifyFull}

// validateNoDeliveredEmptyDatabase fails startup when any database section the
// deployment consumes was delivered with only empty identity fields — the shape
// ADR-047 could not see (ADR-051). Inert for hand-built Config literals (no
// koanf instance) and for dynamic-source tenant configs (never in koanf). Every
// offending key is reported, not just the first: the error promises
// "field(s)", and an operator who clears only the one named would hit the same
// abort again. Sorted, so the startup error is deterministic.
func validateNoDeliveredEmptyDatabase(cfg *Config) error {
	var offending []string
	_ = forEachDatabaseSection(cfg, func(section dbSection, db *DatabaseConfig) error {
		if IsDatabaseConfigured(db) {
			return nil
		}
		for _, k := range databaseIdentityKeys {
			if key := section.path + "." + k; cfg.Exists(key) {
				offending = append(offending, key)
			}
		}
		return nil
	})
	if len(offending) == 0 {
		return nil
	}
	slices.Sort(offending)
	return &ConfigError{
		Category: errCategoryInvalid,
		Field:    offending[0],
		Message:  fmt.Sprintf("database identity field(s) delivered empty: %v", offending),
		Action: "set real values (empty secretKeyRef / unset envsubst variable?) or remove the keys entirely — " +
			"an absent database section is the supported database-free posture (ADR-047, ADR-051)",
	}
}

// IsDatabaseConfigured reports whether a database is intentionally configured
// (ADR-003, ADR-047).
//
// Any connection-identity field counts as intent. The strictness is the point: a
// partially delivered database section must fail validation loudly rather than read
// as an intentionally database-free service, because everything downstream treats
// "no database at all" as a benign posture. Only a section with literally zero
// identity fields is absence.
//
// Fields that applyDatabasePoolDefaults fills in (timezone, pool, query) are
// deliberately excluded, so the verdict is identical before and after defaulting.
//
// A field delivered as an EMPTY string (an empty secretKeyRef, envsubst over an
// unset variable) is indistinguishable from an unset one here and reads as
// absence — but that shape is now caught at Load time by
// validateNoDeliveredEmptyDatabase (ADR-051), which consults koanf key presence
// rather than decoded values. The remaining blind spots are hand-built Config
// values (no koanf instance to consult) and dynamic-source tenant configs
// (resolved from a remote store, never koanf). TLS material is likewise
// excluded from this predicate — it identifies no database on its own.
//
// The answer must not change across normalizeDatabaseSection: normalization
// never adds identity to a section that had none, which is what lets check
// consult this predicate after normalize (validateNoSingleTenantConflict).
func IsDatabaseConfigured(cfg *DatabaseConfig) bool {
	return cfg.ConnectionString != "" ||
		cfg.Type != "" ||
		cfg.Host != "" ||
		cfg.Port != 0 ||
		cfg.Database != "" ||
		cfg.Username != "" ||
		cfg.Password != "" ||
		// Oracle names its target with a service name or SID instead of a database
		// name, so a split config delivering only those is still intent.
		cfg.Oracle.Service.Name != "" ||
		cfg.Oracle.Service.SID != ""
}

// inferDatabaseTypeFromConnectionString maps a recognized DSN scheme to its vendor.
// Surrounding whitespace is tolerated for classification only — a DSN read from a
// file, a mounted secret, or a command substitution routinely carries a trailing
// newline, and losing the scheme match there would silently leave the config
// untyped. The caller's stored DSN is never rewritten; whether the untrimmed value
// then fails at dial is the driver's business. An unrecognized scheme returns "" and
// is deliberately not an error here: whether an untyped DSN is fatal depends on who
// connects (ADR-050).
func inferDatabaseTypeFromConnectionString(cs string) string {
	lower := strings.ToLower(strings.TrimSpace(cs))
	switch {
	case strings.HasPrefix(lower, "postgres://"), strings.HasPrefix(lower, "postgresql://"):
		return PostgreSQL
	case strings.HasPrefix(lower, "oracle://"):
		return Oracle
	}
	return ""
}

// validateDatabaseType validates that dbType is one of the supported database type
// constants (PostgreSQL or Oracle). It returns nil when dbType is valid and an
// error describing the invalid value and the allowed types when it is not.
func validateDatabaseType(dbType string) error {
	validTypes := []string{PostgreSQL, Oracle}
	if !slices.Contains(validTypes, dbType) {
		return NewInvalidFieldError("database.type", fmt.Sprintf(errNotSupportedFmt, dbType), validTypes)
	}
	return nil
}

func validateDatabaseCoreFields(cfg *DatabaseConfig) error {
	if cfg.Host == "" {
		return NewMissingFieldError("database.host", "DATABASE_HOST", "database.host")
	}

	if err := validateRequiredDatabasePort(cfg.Port); err != nil {
		return err
	}

	// For Oracle, database name is optional if Service.Name or SID is provided
	// Oracle-specific validation will provide more detailed error messages
	if cfg.Type != Oracle && cfg.Database == "" {
		return NewMissingFieldError("database.database", "DATABASE_DATABASE", "database.database")
	}

	if cfg.Username == "" {
		return NewMissingFieldError("database.username", "DATABASE_USERNAME", "database.username")
	}

	// A non-empty password below MinDatabasePasswordLength cannot be safely
	// redacted from Flyway output at migration time, so the engine would suppress
	// the whole output and hide the migration outcome. Reject it here (message
	// never echoes the value). Empty passwords (trust/IAM auth) are exempt.
	if cfg.Password != "" && len(cfg.Password) < MinDatabasePasswordLength {
		return NewInvalidFieldError(
			fieldDatabasePassword,
			fmt.Sprintf("must be at least %d characters when set", MinDatabasePasswordLength),
			[]string{fmt.Sprintf("%d+ characters", MinDatabasePasswordLength)},
		)
	}

	return nil
}

func validateOptionalDatabasePort(port int) error {
	if port < 0 || port > 65535 {
		return NewInvalidFieldError(fieldDatabasePort, fmt.Sprintf(errInvalidField, port), []string{portRange})
	}
	return nil
}

func validateRequiredDatabasePort(port int) error {
	if port <= 0 {
		return NewMissingFieldError(fieldDatabasePort, "DATABASE_PORT", fieldDatabasePort)
	}
	if port > 65535 {
		return NewInvalidFieldError(fieldDatabasePort, "invalid port; must be between 1 and 65535", []string{portRange})
	}
	return nil
}

// applyConnectionCountDefaults defaults and validates the max/idle connection
// counts. Max is defaulted first; idle then defaults to — and is capped at — max
// so the pool reuses warm connections instead of churning them (TCP+TLS+auth) on
// every burst. database/sql caps idle at max-open, so an explicit idle above max
// is clamped here to keep the reported value (startup log, Stats(), OTEL gauges)
// truthful. An explicit idle below max is honored. See ADR-025.
func applyConnectionCountDefaults(cfg *DatabaseConfig) error {
	if cfg.Pool.Max.Connections == 0 {
		cfg.Pool.Max.Connections = defaultPoolMaxConnections
	} else if cfg.Pool.Max.Connections < 0 {
		return NewValidationError("database.pool.max.connections", errMustBeNonNegative)
	}

	if cfg.Pool.Idle.Connections < 0 {
		return NewValidationError("database.pool.idle.connections", errMustBeNonNegative)
	}
	if cfg.Pool.Idle.Connections == 0 || cfg.Pool.Idle.Connections > cfg.Pool.Max.Connections {
		cfg.Pool.Idle.Connections = cfg.Pool.Max.Connections
	}
	return nil
}

// ApplyDatabasePoolDefaults normalizes a DatabaseConfig for connection: it infers
// a missing Type from a recognized connectionstring scheme, rejects vendor field
// combinations the driver would silently drop, and fills zero-value Pool,
// Timezone, and Query (log/slow-threshold) settings with the documented defaults
// (25 max connections, idle tracks max, keepalive rules, UTC timezone).
//
// Exported so callers that bypass Validate — notably dynamic multi-tenant
// DBConfigProviders resolved in DbManager — get the same normalization as static
// config; the inference (ADR-050) is what lets a provider's DSN-only config dial
// instead of failing on the factory's empty-type dispatch. Unlike config.Validate,
// this seam never errors on an explicit Type that contradicts the scheme — it is on
// the per-tenant connection path, where the vendor dial error is the right failure.
// It does reject Oracle TLS material and an unpaired PostgreSQL sslcert/sslkey,
// because that failure mode is silent and open rather than loud at dial. The
// asymmetry is deliberate.
//
// It is the connect-strictness door of the database-section normalization
// module (database_section.go); a rejected config returns untouched.
func ApplyDatabasePoolDefaults(cfg *DatabaseConfig) error {
	if cfg == nil {
		return NewValidationError("database", "configuration is nil")
	}
	return normalizeDatabaseValues(cfg, dbStrictnessConnect)
}

// applyDatabasePoolDefaults sets production-safe defaults and validates database pool/query/session settings.
//
// It modifies cfg in-place:
//   - Timezone: if empty, sets to "UTC"; validates with time.LoadLocation unless set to "-".
//   - Pool.Max.Connections / Pool.Idle.Connections: defaulted and validated by
//     applyConnectionCountDefaults (idle defaults to and is capped at max; see ADR-025).
//   - Pool.Idle.Time: if 0, sets to 5m (closes idle connections before NAT/firewall timeout); if negative, returns an error.
//   - Pool.Lifetime.Max: if 0, sets to 30m (forces periodic connection recycling); if negative, returns an error.
//   - Pool.KeepAlive.Enabled: if absent (nil), sets to true (recommended for cloud); an
//     explicit true or false is honored regardless of Interval.
//   - Pool.KeepAlive.Interval: if 0, sets to 60s (below typical NAT timeouts); this never
//     flips Enabled.
//   - Query.Log.MaxLength: if negative, returns an error; if 0, sets to defaultMaxQueryLength.
//   - Query.Slow.Threshold: if negative, returns an error; if 0, sets to defaultSlowQueryThreshold.
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyDatabasePoolDefaults(cfg *DatabaseConfig) error {
	if err := applyDatabaseTimezoneDefault(cfg); err != nil {
		return err
	}

	if err := applyConnectionCountDefaults(cfg); err != nil {
		return err
	}

	// Apply default idle time - closes connections before NAT/firewall timeout
	if cfg.Pool.Idle.Time == 0 {
		cfg.Pool.Idle.Time = defaultPoolIdleTime
	} else if cfg.Pool.Idle.Time < 0 {
		return NewValidationError("database.pool.idle.time", errMustBeNonNegative)
	}

	// Apply default connection lifetime - forces periodic recycling
	if cfg.Pool.Lifetime.Max == 0 {
		cfg.Pool.Lifetime.Max = defaultPoolLifetimeMax
	} else if cfg.Pool.Lifetime.Max < 0 {
		return NewValidationError("database.pool.lifetime.max", errMustBeNonNegative)
	}

	// Apply keep-alive defaults for cloud deployments. Enabled and Interval are
	// defaulted independently so an explicit enabled=false is always honored,
	// even when Interval is left at its zero default (the natural opt-out).
	//   - Enabled: default to true only when the key is absent (nil). An explicit
	//     true or false survives untouched.
	//   - Interval: default to 60s when zero; this never flips Enabled.
	if cfg.Pool.KeepAlive.Enabled == nil {
		enabled := defaultKeepAliveEnabled
		cfg.Pool.KeepAlive.Enabled = &enabled
	}
	if cfg.Pool.KeepAlive.Interval == 0 {
		cfg.Pool.KeepAlive.Interval = defaultKeepAliveInterval
	} else if cfg.Pool.KeepAlive.Interval < 0 {
		return NewValidationError("database.pool.keepalive.interval", errMustBeNonNegative)
	}

	if cfg.Query.Log.MaxLength < 0 {
		return NewValidationError("database.query.log.maxlength", errMustBeNonNegative)
	}
	if cfg.Query.Log.MaxLength == 0 {
		cfg.Query.Log.MaxLength = defaultMaxQueryLength
	}

	if cfg.Query.Slow.Threshold < 0 {
		return NewValidationError("database.query.slow.threshold", errMustBeNonNegative)
	}
	if cfg.Query.Slow.Threshold == 0 {
		cfg.Query.Slow.Threshold = defaultSlowQueryThreshold
	}

	return nil
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

// applyDatabaseTimezoneDefault sets cfg.Timezone to the default ("UTC") when unset
// and validates the configured value as a loadable IANA timezone. The opt-out
// sentinel "-" skips validation and tells the connection layer to leave session
// timezone untouched.
func applyDatabaseTimezoneDefault(cfg *DatabaseConfig) error {
	normalized, err := normalizeIANATimezone("database.timezone", cfg.Timezone)
	cfg.Timezone = normalized
	return err
}

// normalizeScheduler normalizes scheduler.timezone: default "UTC", "-"
// opt-out for host-local, IANA validation — an invalid zone fails fast at
// startup.
func normalizeScheduler(cfg *SchedulerConfig) error {
	normalized, err := normalizeIANATimezone("scheduler.timezone", cfg.Timezone)
	cfg.Timezone = normalized
	return err
}

// checkScheduler rejects an entirely unparseable CIDR allowlist or
// trusted-proxy list.
func checkScheduler(cfg *SchedulerConfig) error {
	if err := validateCIDRList("scheduler.security.cidrallowlist", cfg.Security.CIDRAllowlist); err != nil {
		return err
	}
	return validateCIDRList("scheduler.security.trustedproxies", cfg.Security.TrustedProxies)
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

// normalizeNamedDatabases shapes every databases.* section (opaque; see
// normalizeDatabaseSection) and writes the result back so the defaults reach
// downstream consumers such as TenantStore. The map-key rules (empty name,
// reserved prefix, tenant-ID collision) are check's — see checkNamedDatabases.
func normalizeNamedDatabases(databases map[string]DatabaseConfig) error {
	// Sorted, like forEachDatabaseSection: with several malformed entries the
	// startup error names the same one every run.
	for _, name := range slices.Sorted(maps.Keys(databases)) {
		dbCfg := databases[name]
		if err := normalizeDatabaseSection(&dbCfg, namedDatabaseSection(name)); err != nil {
			return err
		}
		databases[name] = dbCfg
	}
	return nil
}

// checkNamedDatabases rejects a databases.* map key without touching its
// section's content (that is normalizeNamedDatabases' job).
func checkNamedDatabases(databases map[string]DatabaseConfig, mt *MultitenantConfig) error {
	for _, name := range slices.Sorted(maps.Keys(databases)) {
		if err := validateNamedDatabaseName(name, mt); err != nil {
			return err
		}
	}
	return nil
}

// validateNamedDatabaseName checks the map key: non-empty, not the reserved
// prefix, and not colliding with a static tenant ID.
func validateNamedDatabaseName(name string, mt *MultitenantConfig) error {
	if name == "" {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabases,
			Message:  "database name cannot be empty",
			Action:   "provide a non-empty key for each entry in databases section",
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
	// A '.' collides with koanf's path delimiter: constructed section paths
	// (databases.<name>, and this name's own error Field above) become
	// ambiguous, so the bare "databases" Field is used here rather than
	// fmt.Sprintf(databasesFieldPrefix, name) — embedding the dotted name
	// would reproduce the same ambiguity in the error itself.
	if strings.Contains(name, ".") {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabases,
			Message:  fmt.Sprintf("database name %q cannot contain '.' (the config path delimiter)", name),
			Action:   "rename the databases entry without dots",
		}
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

	return applyModeAwarePoolDefault(&cfg.Publisher.MaxCached, defaultMaxPublishers, "messaging.publisher.maxcached", multitenant)
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

// validateVendorSpecificFields validates database vendor-specific configuration fields
func validateVendorSpecificFields(cfg *DatabaseConfig) error {
	// Trim once here so both vendors and the downstream DSN builder see canonical values.
	cfg.TLS.Mode = strings.TrimSpace(cfg.TLS.Mode)
	cfg.TLS.CertFile = strings.TrimSpace(cfg.TLS.CertFile)
	cfg.TLS.KeyFile = strings.TrimSpace(cfg.TLS.KeyFile)
	cfg.TLS.CAFile = strings.TrimSpace(cfg.TLS.CAFile)

	switch cfg.Type {
	case Oracle:
		return validateOracleFields(cfg)
	case PostgreSQL:
		return validatePostgreSQLFields(cfg)
	default:
		// Unknown database type should have been caught by validateDatabaseType
		return nil
	}
}

// validatePostgreSQLFields fails closed on database.tls shapes that pgx would silently
// discard or downgrade (ADR-062). Check order is
// load-bearing: connectionstring short-circuits, then the mode allowlist, then the
// material/mode coherence rule, then the cert/key pairing.
func validatePostgreSQLFields(cfg *DatabaseConfig) error {
	if cfg.ConnectionString != "" {
		if cfg.TLS.Mode != "" || cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.CAFile != "" {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldDatabaseTLS,
				Message:  "database.tls is ignored when connectionstring is set (the DSN is used verbatim)",
				Action:   "move TLS settings into the connection string (sslmode/sslrootcert/sslcert/sslkey) and remove the database.tls block",
			}
		}
		return nil
	}

	if cfg.TLS.Mode != "" && !slices.Contains(pgSSLModes, cfg.TLS.Mode) {
		return NewInvalidFieldError("database.tls.mode", fmt.Sprintf(errInvalidField, cfg.TLS.Mode), pgSSLModes)
	}

	hasMaterial := cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.CAFile != ""
	if hasMaterial && !slices.Contains(pgTLSMandatorySSLModes, cfg.TLS.Mode) {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabaseTLS,
			Message: "TLS cert/key/ca require a mode that guarantees TLS; under disable/allow/prefer " +
				"(or an unset mode, which defaults to prefer) pgx silently discards the material, downgrades " +
				"to plaintext, or (for ca: system) silently upgrades the mode — none of which is what the config says",
			Action: "set database.tls.mode to require, verify-ca, or verify-full",
		}
	}

	// Client-certificate (mTLS) auth requires BOTH sslcert and sslkey; pgx rejects a lone
	// one only at connect time, where go-bricks redacts the parse error.
	if (cfg.TLS.CertFile != "") != (cfg.TLS.KeyFile != "") {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabaseTLS,
			Message:  "sslcert and sslkey must be configured together for client-certificate (mTLS) auth",
			Action:   "set both database.tls.cert and database.tls.key, or neither",
		}
	}
	return nil
}

// validateOracleFields validates Oracle-specific configuration fields.
// It ensures that exactly one of Service.Name, SID, or Database is configured,
// mirroring the DSN selection logic in database/oracle/connection.go — except in
// connection-string mode, where the DSN supplies the identifier and none is required.
func validateOracleFields(cfg *DatabaseConfig) error {
	// Oracle TLS (tcps/wallet) is not implemented in go-bricks, so reject the whole
	// database.tls block rather than silently ignoring it — mode included, since nothing
	// wires the TLS it implies, leaving an operator to believe the connection is encrypted.
	if cfg.TLS.Mode != "" || cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.CAFile != "" {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabaseTLS,
			Message:  "database.tls settings are not supported for Oracle (tcps/wallet is not implemented)",
			Action:   "remove the database.tls block for Oracle connections",
		}
	}

	serviceSet := cfg.Oracle.Service.Name != ""
	sidSet := cfg.Oracle.Service.SID != ""
	databaseSet := cfg.Database != ""

	count := 0
	if serviceSet {
		count++
	}
	if sidSet {
		count++
	}
	if databaseSet {
		count++
	}

	// buildOracleDSN returns ConnectionString verbatim, so none of these fields is
	// consulted in that mode — the DSN carries the identifier. Requiring a separate
	// one that is then ignored would reject a valid connection-string-only config.
	if count == 0 && cfg.ConnectionString == "" {
		return &ConfigError{
			Category: errCategoryMissing,
			Field:    "oracle connection identifier",
			Message:  "exactly one required",
			Action:   "set database.oracle.service.name, database.oracle.service.sid, or database.database",
		}
	}

	if count > 1 {
		configured := make([]string, 0, 3)
		if serviceSet {
			configured = append(configured, "service name")
		}
		if sidSet {
			configured = append(configured, "SID")
		}
		if databaseSet {
			configured = append(configured, "database name")
		}
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    "oracle connection identifier",
			Message:  "multiple identifiers configured",
			Action:   fmt.Sprintf("remove all but one of: %s", strings.Join(configured, ", ")),
		}
	}

	return nil
}

// checkKeyStore returns nil if no keys are configured. SecretMinLength must be
// non-negative. Each entry is either an RSA pair (public required with exactly
// one source, private optional) or a symmetric secret — a mixed entry is
// rejected.
func checkKeyStore(cfg *KeyStoreConfig) error {
	if cfg.SecretMinLength < 0 {
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
			Field:    fmt.Sprintf("keystore.keys.%s", name),
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
// section: an empty or dotted ID, or (once the ID is valid) whatever
// checkTenantCache rejects.
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
	if IsMessagingConfigured(msg) {
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
			Field:    "multitenant.tenants messaging",
			Message:  "inconsistent configuration",
			Action:   "either all tenants must have messaging configured or none should",
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

// checkTenantCache is checkCache with the tenant named in the error.
func checkTenantCache(tenantID string, cache *CacheConfig) error {
	if err := checkCache(cache); err != nil {
		return fmt.Errorf("tenant %s cache: %w", tenantID, err)
	}
	return nil
}

// validateSourceConfig validates the source configuration type
func validateSourceConfig(cfg *SourceConfig) error {
	if cfg.Type != SourceTypeStatic && cfg.Type != SourceTypeDynamic {
		return NewInvalidFieldError("source.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), []string{"static", "dynamic"})
	}
	return nil
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
