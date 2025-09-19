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

func validateLog(cfg *LogConfig) error {
	validLevels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}
	if !slices.Contains(validLevels, cfg.Level) {
		return fmt.Errorf("invalid log level: %s (must be one of: %s)",
			cfg.Level, strings.Join(validLevels, ", "))
	}

	return nil
}
