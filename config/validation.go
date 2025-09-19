package config

import (
	"fmt"
	"slices"
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
// EnvDevelopment, EnvStaging, or EnvProduction, and RateLimit to be > 0.
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

	if cfg.RateLimit <= 0 {
		return fmt.Errorf("rate limit must be positive")
	}

	return nil
}

func validateServer(cfg *ServerConfig) error {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", cfg.Port)
	}

	if cfg.ReadTimeout <= 0 {
		return fmt.Errorf("read timeout must be positive")
	}

	if cfg.WriteTimeout <= 0 {
		return fmt.Errorf("write timeout must be positive")
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

	return applyDatabasePoolDefaults(cfg)
}

// validateDatabaseWithConnectionString validates database settings when a connection
// string is provided and applies defaults for query-related fields when zero.
// It checks (and returns an error for) an explicit database Type that is not allowed,
// an invalid optional Port, non-positive MaxConns, and negative values for
// MaxQueryLength or SlowQueryThreshold. If MaxQueryLength or SlowQueryThreshold
// are zero they are set to defaultMaxQueryLength and defaultSlowQueryThreshold,
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

	if cfg.MaxConns <= 0 {
		return fmt.Errorf("max connections must be positive")
	}

	if cfg.MaxQueryLength < 0 {
		return fmt.Errorf("max query length must be zero or positive")
	}
	if cfg.MaxQueryLength == 0 {
		cfg.MaxQueryLength = defaultMaxQueryLength
	}

	if cfg.SlowQueryThreshold < 0 {
		return fmt.Errorf("slow query threshold must be zero or positive")
	}
	if cfg.SlowQueryThreshold == 0 {
		cfg.SlowQueryThreshold = defaultSlowQueryThreshold
	}

	return nil
}

// validateDatabaseType validates that dbType is one of the supported database type
// constants (PostgreSQL or Oracle). It returns nil when dbType is valid and an
// error describing the invalid value and the allowed types when it is not.
func validateDatabaseType(dbType string) error {
	validTypes := []string{PostgreSQL, Oracle}
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

	if cfg.Database == "" {
		return fmt.Errorf("database name is required")
	}

	if cfg.Username == "" {
		return fmt.Errorf("database username is required")
	}

	return nil
}

func validateOptionalDatabasePort(port int) error {
	if port > 0 && port > 65535 {
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
// - MaxConns: if 0, sets to 25; if negative, returns an error.
// - MaxQueryLength: if negative, returns an error; if 0, sets to defaultMaxQueryLength.
// - SlowQueryThreshold: if negative, returns an error; if 0, sets to defaultSlowQueryThreshold.
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyDatabasePoolDefaults(cfg *DatabaseConfig) error {
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 25
	} else if cfg.MaxConns < 0 {
		return fmt.Errorf("max connections must be positive")
	}

	if cfg.MaxQueryLength < 0 {
		return fmt.Errorf("max query length must be zero or positive")
	}
	if cfg.MaxQueryLength == 0 {
		cfg.MaxQueryLength = defaultMaxQueryLength
	}

	if cfg.SlowQueryThreshold < 0 {
		return fmt.Errorf("slow query threshold must be zero or positive")
	}
	if cfg.SlowQueryThreshold == 0 {
		cfg.SlowQueryThreshold = defaultSlowQueryThreshold
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
