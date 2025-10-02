package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSlowQueryThreshold = 200 * time.Millisecond
	defaultMaxQueryLength     = 1000
)

// Database type constants
const (
	PostgreSQL = "postgresql"
	Oracle     = "oracle"
	MongoDB    = "mongodb"
)

// Environment constants
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

func Validate(cfg *Config) error {
	if err := validateApp(&cfg.App); err != nil {
		return fmt.Errorf("app config: %w", err)
	}

	if err := validateServer(&cfg.Server); err != nil {
		return fmt.Errorf("server config: %w", err)
	}

	if err := validateMultitenant(&cfg.Multitenant, &cfg.Database, &cfg.Messaging, &cfg.Source); err != nil {
		return fmt.Errorf("multitenant config: %w", err)
	}

	if err := validateDatabase(&cfg.Database); err != nil {
		return fmt.Errorf("database config: %w", err)
	}

	if err := validateLog(&cfg.Log); err != nil {
		return fmt.Errorf("log config: %w", err)
	}

	return nil
}

// validateApp validates the application configuration in cfg.
// It requires Name and Version to be non-empty, Env to be one of
// EnvDevelopment, EnvStaging, or EnvProduction, and Rate.Limit to be non-negative.
// Returns an error describing the first failed validation, or nil if valid.
func validateApp(cfg *AppConfig) error {
	if cfg.Name == "" {
		return NewMissingFieldError("app.name", "APP_NAME", "app.name")
	}

	if cfg.Version == "" {
		return NewMissingFieldError("app.version", "APP_VERSION", "app.version")
	}

	validEnvs := []string{EnvDevelopment, EnvStaging, EnvProduction}
	if !slices.Contains(validEnvs, cfg.Env) {
		return NewInvalidFieldError("app.env", fmt.Sprintf("'%s' is not valid", cfg.Env), validEnvs)
	}

	if cfg.Rate.Limit < 0 {
		return NewValidationError("app.rate.limit", "must be non-negative")
	}

	if cfg.Rate.Burst < 0 {
		return NewValidationError("app.rate.burst", "must be non-negative")
	}

	return nil
}

func validateServer(cfg *ServerConfig) error {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return NewInvalidFieldError("server.port", fmt.Sprintf("%d is out of valid range", cfg.Port), []string{"1-65535"})
	}

	if cfg.Timeout.Read <= 0 {
		return NewValidationError("server.timeout.read", "must be positive")
	}

	if cfg.Timeout.Write <= 0 {
		return NewValidationError("server.timeout.write", "must be positive")
	}

	if cfg.Timeout.Middleware <= 0 {
		return NewValidationError("server.timeout.middleware", "must be positive")
	}

	if cfg.Timeout.Shutdown <= 0 {
		return NewValidationError("server.timeout.shutdown", "must be positive")
	}

	return nil
}

// IsDatabaseConfigured determines if database is intentionally configured.
// This mirrors the logic used in app.isDatabaseEnabled() for consistency.
func IsDatabaseConfigured(cfg *DatabaseConfig) bool {
	// Connection string indicates explicit database configuration
	if cfg.ConnectionString != "" {
		return true
	}

	// Mirror the logic from app.isDatabaseEnabled()
	return cfg.Host != "" || cfg.Type != ""
}

func validateDatabase(cfg *DatabaseConfig) error {
	if !IsDatabaseConfigured(cfg) {
		return nil
	}

	if cfg.ConnectionString != "" {
		return validateDatabaseWithConnectionString(cfg)
	}

	if err := validateDatabaseType(cfg.Type); err != nil {
		return err
	}

	if err := validateDatabaseCoreFields(cfg); err != nil {
		return err
	}

	if err := validateVendorSpecificFields(cfg); err != nil {
		return err
	}

	return applyDatabasePoolDefaults(cfg)
}

// validateDatabaseWithConnectionString validates database settings when a connection
// string is provided and applies defaults for query-related fields when zero.
// It checks (and returns an error for) an explicit database Type that is not allowed,
// an invalid optional Port, and negative values for Pool/Query fields.
// Pool.Max.Connections defaults to 25 when 0; Query.Log.MaxLength and Query.Slow.Threshold
// default to the respective constants when 0. Negative values are rejected.
// respectively. The cfg argument is mutated for those default assignments.
func validateDatabaseWithConnectionString(cfg *DatabaseConfig) error {
	if cfg.Type != "" {
		if err := validateDatabaseType(cfg.Type); err != nil {
			return err
		}
	}

	if err := validateOptionalDatabasePort(cfg.Port); err != nil {
		return err
	}

	if err := applyDatabasePoolDefaults(cfg); err != nil {
		return err
	}

	// Validate vendor-specific fields even with connection string
	if err := validateVendorSpecificFields(cfg); err != nil {
		return err
	}

	return nil
}

// validateDatabaseType validates that dbType is one of the supported database type
// constants (PostgreSQL, Oracle, or MongoDB). It returns nil when dbType is valid and an
// error describing the invalid value and the allowed types when it is not.
func validateDatabaseType(dbType string) error {
	validTypes := []string{PostgreSQL, Oracle, MongoDB}
	if !slices.Contains(validTypes, dbType) {
		return NewInvalidFieldError("database.type", fmt.Sprintf("'%s' is not supported", dbType), validTypes)
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

	return nil
}

func validateOptionalDatabasePort(port int) error {
	if port < 0 || port > 65535 {
		return NewInvalidFieldError("database.port", fmt.Sprintf("%d is out of valid range", port), []string{"1-65535"})
	}
	return nil
}

func validateRequiredDatabasePort(port int) error {
	if port <= 0 {
		return NewMissingFieldError("database.port", "DATABASE_PORT", "database.port")
	}
	if port > 65535 {
		return NewInvalidFieldError("database.port", "invalid port; must be between 1 and 65535", []string{"1-65535"})
	}
	return nil
}

// applyDatabasePoolDefaults sets sensible defaults and validates database pool/query settings on cfg.
//
// It modifies cfg in-place:
// - Pool.Max.Connections: if 0, sets to 25; if negative, returns an error.
// - Query.Log.MaxLength: if negative, returns an error; if 0, sets to defaultMaxQueryLength.
// - Query.Slow.Threshold: if negative, returns an error; if 0, sets to defaultSlowQueryThreshold.
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyDatabasePoolDefaults(cfg *DatabaseConfig) error {
	if cfg.Pool.Max.Connections == 0 {
		cfg.Pool.Max.Connections = 25
	} else if cfg.Pool.Max.Connections < 0 {
		return NewValidationError("database.pool.max.connections", "must be non-negative")
	}

	if cfg.Pool.Idle.Connections < 0 {
		return NewValidationError("database.pool.idle.connections", "must be non-negative")
	}

	if cfg.Query.Log.MaxLength < 0 {
		return NewValidationError("database.query.log.maxlength", "must be non-negative")
	}
	if cfg.Query.Log.MaxLength == 0 {
		cfg.Query.Log.MaxLength = defaultMaxQueryLength
	}

	if cfg.Query.Slow.Threshold < 0 {
		return NewValidationError("database.query.slow.threshold", "must be non-negative")
	}
	if cfg.Query.Slow.Threshold == 0 {
		cfg.Query.Slow.Threshold = defaultSlowQueryThreshold
	}

	return nil
}

// validateVendorSpecificFields validates database vendor-specific configuration fields
func validateVendorSpecificFields(cfg *DatabaseConfig) error {
	switch cfg.Type {
	case MongoDB:
		return validateMongoDBFields(cfg)
	case Oracle:
		return validateOracleFields(cfg)
	case PostgreSQL:
		// No vendor-specific validation needed for PostgreSQL currently
		return nil
	default:
		// Unknown database type should have been caught by validateDatabaseType
		return nil
	}
}

// validateMongoDBFields validates MongoDB-specific configuration fields
func validateMongoDBFields(cfg *DatabaseConfig) error {
	if cfg.Mongo.Replica.Preference != "" {
		if err := validateMongoDBReadPreference(cfg.Mongo.Replica.Preference); err != nil {
			return err
		}
	}

	if cfg.Mongo.Concern.Write != "" {
		if err := validateMongoDBWriteConcern(cfg.Mongo.Concern.Write); err != nil {
			return err
		}
	}

	return nil
}

// validateMongoDBReadPreference validates MongoDB read preference values
func validateMongoDBReadPreference(pref string) error {
	validPreferences := map[string]struct{}{
		"primary":            {},
		"primarypreferred":   {},
		"secondary":          {},
		"secondarypreferred": {},
		"nearest":            {},
	}

	if _, ok := validPreferences[strings.ToLower(pref)]; ok {
		return nil
	}

	validOptions := []string{"primary", "primaryPreferred", "secondary", "secondaryPreferred", "nearest"}
	return NewInvalidFieldError("database.mongo.replica.preference", fmt.Sprintf("'%s' is not supported", pref), validOptions)
}

// validateMongoDBWriteConcern validates MongoDB write concern values
func validateMongoDBWriteConcern(concern string) error {
	// First, try to parse as a non-negative integer
	if num, err := strconv.Atoi(concern); err == nil && num >= 0 {
		return nil
	}

	// Check for valid textual concerns (case-insensitive)
	validConcerns := []string{
		"majority",
		"acknowledged",
		"unacknowledged",
	}

	concernLower := strings.ToLower(concern)
	for _, valid := range validConcerns {
		if strings.EqualFold(valid, concernLower) {
			return nil
		}
	}

	validOptions := []string{"majority", "acknowledged", "unacknowledged", "or a non-negative integer"}
	return NewInvalidFieldError("database.mongo.concern.write", fmt.Sprintf("'%s' is not supported", concern), validOptions)
}

// validateOracleFields validates Oracle-specific configuration fields.
// It ensures that exactly one of Service.Name, SID, or Database is configured,
// mirroring the DSN selection logic in database/oracle/connection.go.
func validateOracleFields(cfg *DatabaseConfig) error {
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

	if count == 0 {
		return &ConfigError{
			Category: "missing",
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
			Category: "invalid",
			Field:    "oracle connection identifier",
			Message:  "multiple identifiers configured",
			Action:   fmt.Sprintf("remove all but one of: %s", strings.Join(configured, ", ")),
		}
	}

	return nil
}

// validateLog validates that cfg.Level is one of the supported log levels.
// It returns an error listing the allowed values if the level is invalid.
func validateLog(cfg *LogConfig) error {
	validLevels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}
	if !slices.Contains(validLevels, cfg.Level) {
		return NewInvalidFieldError("log.level", fmt.Sprintf("'%s' is not supported", cfg.Level), validLevels)
	}

	return nil
}

// validateMultitenant validates multi-tenant configuration and ensures no conflicts
// with single-tenant settings. When multitenant is enabled, database and messaging
// configurations must be provided by the tenant config provider.
func validateMultitenant(mt *MultitenantConfig, db *DatabaseConfig, msg *MessagingConfig, source *SourceConfig) error {
	if !mt.Enabled {
		return nil
	}

	// Validate resolver configuration
	if err := validateMultitenantResolver(&mt.Resolver); err != nil {
		return fmt.Errorf("resolver: %w", err)
	}

	// Validate limits configuration
	if err := validateMultitenantLimits(&mt.Limits); err != nil {
		return fmt.Errorf("limits: %w", err)
	}

	// Validate source type
	if err := validateSourceConfig(source); err != nil {
		return fmt.Errorf("source: %w", err)
	}

	// For static sources, validate tenants if provided (optional but must be valid if present)
	// For dynamic sources, tenants are optional and loaded from external store
	if source.Type == SourceTypeStatic && mt.Tenants != nil {
		if len(mt.Tenants) == 0 {
			return fmt.Errorf("tenants: empty map provided - either omit tenants section or provide at least one tenant for static source")
		}
		if err := validateMultitenantTenants(mt.Tenants); err != nil {
			return fmt.Errorf("tenants: %w", err)
		}
	}

	// For static sources with tenants, ensure no conflict with single-tenant config
	if source.Type == SourceTypeStatic && mt.Tenants != nil && len(mt.Tenants) > 0 {
		if IsDatabaseConfigured(db) {
			return &ConfigError{
				Category: "invalid",
				Field:    "database",
				Message:  "not allowed when static tenants are configured",
				Action:   "remove database section from root config or move to multitenant.tenants.<tenant_id>.database",
			}
		}
		if IsMessagingConfigured(msg) {
			return &ConfigError{
				Category: "invalid",
				Field:    "messaging",
				Message:  "not allowed when static tenants are configured",
				Action:   "remove messaging section from root config or move to multitenant.tenants.<tenant_id>.messaging",
			}
		}
	}

	return nil
}

// validateMultitenantResolver validates tenant resolver configuration
func validateMultitenantResolver(cfg *ResolverConfig) error {
	validTypes := []string{"header", "subdomain", "composite"}
	if !slices.Contains(validTypes, cfg.Type) {
		return NewInvalidFieldError("multitenant.resolver.type", fmt.Sprintf("'%s' is not supported", cfg.Type), validTypes)
	}

	// Set defaults
	if cfg.Header == "" {
		cfg.Header = "X-Tenant-ID"
	}

	// Validate subdomain-specific configuration
	if cfg.Type == "subdomain" || cfg.Type == "composite" {
		if strings.TrimSpace(cfg.Domain) == "" {
			return NewMissingFieldError("multitenant.resolver.domain", "MULTITENANT_RESOLVER_DOMAIN", "multitenant.resolver.domain")
		}
		// Normalize: leading dot is optional in config
		if !strings.HasPrefix(cfg.Domain, ".") {
			cfg.Domain = "." + cfg.Domain
		}
	}

	return nil
}

// validateMultitenantLimits validates limits configuration with defaults
func validateMultitenantLimits(cfg *LimitsConfig) error {
	if cfg.Tenants <= 0 {
		cfg.Tenants = 100 // default
	}
	if cfg.Tenants > 1000 {
		return NewValidationError("multitenant.limits.tenants", "cannot exceed 1000")
	}
	return nil
}

// validateMultitenantTenants validates tenant configurations when they are provided
func validateMultitenantTenants(tenants map[string]TenantEntry) error {
	if len(tenants) == 0 {
		return NewValidationError("multitenant.tenants", "at least one tenant must be configured")
	}

	// Check consistency: if any tenant has messaging configured, all must have it configured
	// This prevents confusing scenarios where some tenants can use messaging and others cannot
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

	// Enforce all-or-nothing messaging configuration for consistency
	if hasAnyMessaging && hasNoMessaging {
		return &ConfigError{
			Category: "invalid",
			Field:    "multitenant.tenants messaging",
			Message:  "inconsistent configuration",
			Action:   "either all tenants must have messaging configured or none should",
		}
	}

	for tenantID := range tenants {
		tenant := tenants[tenantID]
		if tenantID == "" {
			return NewValidationError("multitenant.tenants", "tenant ID cannot be empty")
		}

		// Validate tenant database configuration
		if !IsDatabaseConfigured(&tenant.Database) {
			return NewMultiTenantError(tenantID, "database", "configuration required", fmt.Sprintf("add multitenant.tenants.%s.database section", tenantID))
		}
		if err := validateDatabase(&tenant.Database); err != nil {
			return fmt.Errorf("tenant %s database: %w", tenantID, err)
		}
	}

	return nil
}

// validateSourceConfig validates the source configuration type
func validateSourceConfig(cfg *SourceConfig) error {
	if cfg.Type != SourceTypeStatic && cfg.Type != SourceTypeDynamic {
		return NewInvalidFieldError("source.type", fmt.Sprintf("'%s' is not supported", cfg.Type), []string{"static", "dynamic"})
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
