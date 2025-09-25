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

	if err := validateMultitenant(&cfg.Multitenant, &cfg.Database, &cfg.Messaging); err != nil {
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
		return fmt.Errorf("app name is required")
	}

	if cfg.Version == "" {
		return fmt.Errorf("app version is required")
	}

	validEnvs := []string{EnvDevelopment, EnvStaging, EnvProduction}
	if !slices.Contains(validEnvs, cfg.Env) {
		return fmt.Errorf("invalid environment: %s (must be one of: %s)",
			cfg.Env, strings.Join(validEnvs, ", "))
	}

	if cfg.Rate.Limit < 0 {
		return fmt.Errorf("rate limit must be non-negative")
	}

	if cfg.Rate.Burst < 0 {
		return fmt.Errorf("rate burst must be non-negative")
	}

	return nil
}

func validateServer(cfg *ServerConfig) error {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", cfg.Port)
	}

	if cfg.Timeout.Read <= 0 {
		return fmt.Errorf("read timeout must be positive")
	}

	if cfg.Timeout.Write <= 0 {
		return fmt.Errorf("write timeout must be positive")
	}

	if cfg.Timeout.Middleware <= 0 {
		return fmt.Errorf("middleware timeout must be positive")
	}

	if cfg.Timeout.Shutdown <= 0 {
		return fmt.Errorf("shutdown timeout must be positive")
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
		return fmt.Errorf("invalid database type: %s (must be one of: %s)",
			dbType, strings.Join(validTypes, ", "))
	}
	return nil
}

func validateDatabaseCoreFields(cfg *DatabaseConfig) error {
	if cfg.Host == "" {
		return fmt.Errorf("database host is required")
	}

	if err := validateRequiredDatabasePort(cfg.Port); err != nil {
		return err
	}

	// For Oracle, database name is optional if Service.Name or SID is provided
	// Oracle-specific validation will provide more detailed error messages
	if cfg.Type != Oracle && cfg.Database == "" {
		return fmt.Errorf("database name is required")
	}

	if cfg.Username == "" {
		return fmt.Errorf("database username is required")
	}

	return nil
}

func validateOptionalDatabasePort(port int) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("invalid database port: %d", port)
	}
	return nil
}

func validateRequiredDatabasePort(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid database port: %d", port)
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
		return fmt.Errorf("max connections must be non-negative")
	}

	if cfg.Pool.Idle.Connections < 0 {
		return fmt.Errorf("max idle connections must be non-negative")
	}

	if cfg.Query.Log.MaxLength < 0 {
		return fmt.Errorf("max query length must be non-negative")
	}
	if cfg.Query.Log.MaxLength == 0 {
		cfg.Query.Log.MaxLength = defaultMaxQueryLength
	}

	if cfg.Query.Slow.Threshold < 0 {
		return fmt.Errorf("slow query threshold must be non-negative")
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

	return fmt.Errorf("invalid MongoDB read preference: %s (must be one of: primary, primaryPreferred, secondary, secondaryPreferred, nearest)", pref)
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

	return fmt.Errorf("invalid MongoDB write concern: %s (must be one of: majority, acknowledged, unacknowledged, or a non-negative integer)", concern)
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
		return fmt.Errorf("oracle configuration requires exactly one of: service name, SID, or database name")
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
		return fmt.Errorf("oracle configuration has multiple connection identifiers configured (%s), exactly one is required", strings.Join(configured, ", "))
	}

	return nil
}

// validateLog validates that cfg.Level is one of the supported log levels.
// It returns an error listing the allowed values if the level is invalid.
func validateLog(cfg *LogConfig) error {
	validLevels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}
	if !slices.Contains(validLevels, cfg.Level) {
		return fmt.Errorf("invalid log level: %s (must be one of: %s)",
			cfg.Level, strings.Join(validLevels, ", "))
	}

	return nil
}

// validateMultitenant validates multi-tenant configuration and ensures no conflicts
// with single-tenant settings. When multitenant is enabled, database and messaging
// configurations must be provided by the tenant config provider.
func validateMultitenant(mt *MultitenantConfig, db *DatabaseConfig, msg *MessagingConfig) error {
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

	// Validate tenants configuration
	if err := validateMultitenantTenants(mt.Tenants); err != nil {
		return fmt.Errorf("tenants: %w", err)
	}

	// Ensure no conflict with single-tenant database configuration
	if IsDatabaseConfigured(db) {
		return fmt.Errorf("database configuration not allowed when multitenant.enabled is true (tenant configs are provided dynamically)")
	}

	// Ensure no conflict with single-tenant messaging configuration
	if isMessagingConfigured(msg) {
		return fmt.Errorf("messaging configuration not allowed when multitenant.enabled is true (tenant configs are provided dynamically)")
	}

	return nil
}

// validateMultitenantResolver validates tenant resolver configuration
func validateMultitenantResolver(cfg *ResolverConfig) error {
	validTypes := []string{"header", "subdomain", "composite"}
	if !slices.Contains(validTypes, cfg.Type) {
		return fmt.Errorf("invalid type: %s (must be one of: %s)",
			cfg.Type, strings.Join(validTypes, ", "))
	}

	// Set defaults
	if cfg.Header == "" {
		cfg.Header = "X-Tenant-ID"
	}

	// Validate subdomain-specific configuration
	if cfg.Type == "subdomain" || cfg.Type == "composite" {
		if strings.TrimSpace(cfg.Domain) == "" {
			return fmt.Errorf("domain is required for subdomain resolution")
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
		return fmt.Errorf("tenants cannot exceed 1000")
	}
	return nil
}

// validateMultitenantTenants validates tenant configurations when they are provided
func validateMultitenantTenants(tenants map[string]TenantEntry) error {
	// Allow empty tenants map for dynamic tenant configuration
	if len(tenants) == 0 {
		return nil
	}

	// If tenants are provided statically, validate them
	for tenantID := range tenants {
		tenant := tenants[tenantID]
		if tenantID == "" {
			return fmt.Errorf("tenant ID cannot be empty")
		}

		// Validate tenant database configuration
		if err := validateDatabase(&tenant.Database); err != nil {
			return fmt.Errorf("tenant %s database: %w", tenantID, err)
		}

		// Validate tenant messaging configuration
		if tenant.Messaging.URL == "" {
			return fmt.Errorf("tenant %s messaging URL cannot be empty", tenantID)
		}
	}

	return nil
}

// isMessagingConfigured determines if messaging is intentionally configured
func isMessagingConfigured(cfg *MessagingConfig) bool {
	return cfg.Broker.URL != ""
}
