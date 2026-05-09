// Package migration provides integration with Flyway for database migrations
package migration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

const (
	// Flyway command flag constants
	flagConfigFiles = "-configFiles="
	flagLocationsFS = "-locations=filesystem:"

	flywayExecutable  = "flyway"
	flywayCmdValidate = "validate"
)

// FlywayMigrator handles database migrations using Flyway
type FlywayMigrator struct {
	config        *config.Config
	logger        logger.Logger
	defaultConfig func(*FlywayMigrator) *Config
}

// Config configuration for migrations
type Config struct {
	FlywayPath    string        // Path to the Flyway executable
	ConfigPath    string        // Path to the configuration file
	MigrationPath string        // Path to migration scripts
	Timeout       time.Duration // Timeout for migration operations
	Environment   string        // Environment (development, testing, production)
	DryRun        bool          // Only validate, do not execute
}

// NewFlywayMigrator creates a new instance of the migrator
func NewFlywayMigrator(cfg *config.Config, log logger.Logger) *FlywayMigrator {
	fm := &FlywayMigrator{
		config: cfg,
		logger: log,
	}

	fm.defaultConfig = (*FlywayMigrator).defaultMigrationConfig

	return fm
}

// DefaultMigrationConfig returns the default configuration for migrations
func (fm *FlywayMigrator) DefaultMigrationConfig() *Config {
	if fm.defaultConfig == nil {
		fm.defaultConfig = (*FlywayMigrator).defaultMigrationConfig
	}

	return fm.defaultConfig(fm)
}

// DefaultMigrationConfigForVendor returns the default migration config for the
// given database vendor (e.g. "postgresql", "oracle"). Used by multi-tenant
// migrations where each tenant may run a different vendor than the migrator's
// own cfg.Database.Type.
func (fm *FlywayMigrator) DefaultMigrationConfigForVendor(vendor string) *Config {
	return &Config{
		FlywayPath:    flywayExecutable,
		ConfigPath:    fmt.Sprintf("flyway/flyway-%s.conf", vendor),
		MigrationPath: fmt.Sprintf("migrations/%s", vendor),
		Timeout:       5 * time.Minute,
		Environment:   fm.config.App.Env,
		DryRun:        false,
	}
}

func (fm *FlywayMigrator) defaultMigrationConfig() *Config {
	return fm.DefaultMigrationConfigForVendor(fm.config.Database.Type)
}

// Migrate executes pending migrations against the migrator's configured database.
func (fm *FlywayMigrator) Migrate(ctx context.Context, cfg *Config) error {
	if cfg == nil {
		cfg = fm.DefaultMigrationConfig()
	}
	return fm.MigrateFor(ctx, &fm.config.Database, cfg)
}

// MigrateFor executes pending migrations against the supplied database.
// Used by multi-tenant migrations to target a tenant-specific DatabaseConfig.
func (fm *FlywayMigrator) MigrateFor(ctx context.Context, db *config.DatabaseConfig, cfg *Config) error {
	if cfg == nil {
		cfg = fm.DefaultMigrationConfigForVendor(dbVendor(db, fm.config.Database.Type))
	}

	fm.logger.Info().Str("vendor", dbVendor(db, fm.config.Database.Type)).Msg("Starting database migrations")

	args := []string{
		flagConfigFiles + cfg.ConfigPath,
		flagLocationsFS + cfg.MigrationPath,
		"migrate",
	}

	return fm.runFlywayCommandFor(ctx, db, cfg, args)
}

// Info shows information about the status of migrations against the migrator's database.
func (fm *FlywayMigrator) Info(ctx context.Context, cfg *Config) error {
	if cfg == nil {
		cfg = fm.DefaultMigrationConfig()
	}
	return fm.InfoFor(ctx, &fm.config.Database, cfg)
}

// InfoFor shows migration status for the supplied database.
func (fm *FlywayMigrator) InfoFor(ctx context.Context, db *config.DatabaseConfig, cfg *Config) error {
	if cfg == nil {
		cfg = fm.DefaultMigrationConfigForVendor(dbVendor(db, fm.config.Database.Type))
	}

	args := []string{
		flagConfigFiles + cfg.ConfigPath,
		flagLocationsFS + cfg.MigrationPath,
		"info",
	}

	return fm.runFlywayCommandFor(ctx, db, cfg, args)
}

// Validate validates migrations without executing them against the migrator's database.
func (fm *FlywayMigrator) Validate(ctx context.Context, cfg *Config) error {
	if cfg == nil {
		cfg = fm.DefaultMigrationConfig()
	}
	return fm.ValidateFor(ctx, &fm.config.Database, cfg)
}

// ValidateFor validates migrations for the supplied database.
func (fm *FlywayMigrator) ValidateFor(ctx context.Context, db *config.DatabaseConfig, cfg *Config) error {
	if cfg == nil {
		cfg = fm.DefaultMigrationConfigForVendor(dbVendor(db, fm.config.Database.Type))
	}

	args := []string{
		flagConfigFiles + cfg.ConfigPath,
		flagLocationsFS + cfg.MigrationPath,
		flywayCmdValidate,
	}

	return fm.runFlywayCommandFor(ctx, db, cfg, args)
}

// runFlywayCommandFor executes a Flyway command using the supplied database config.
func (fm *FlywayMigrator) runFlywayCommandFor(ctx context.Context, db *config.DatabaseConfig, cfg *Config, args []string) error {
	if err := fm.validateFlywayPath(cfg.FlywayPath); err != nil {
		return fmt.Errorf("invalid flyway path: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	// #nosec G204 -- FlywayPath is validated by validateFlywayPath function
	cmd := exec.CommandContext(timeoutCtx, cfg.FlywayPath, args...)

	cmd.Env = append(os.Environ(), buildEnvironmentVariables(db)...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		fm.logger.Error().Err(err).Str("output", string(output)).Msg("Error executing Flyway command")
		return fmt.Errorf("flyway command failed: %w", err)
	}

	fm.logger.Info().Msg("Flyway command completed successfully")
	return nil
}

// dbVendor returns the database vendor string from the supplied config, falling
// back to the migrator's default when the config has no Type set.
func dbVendor(db *config.DatabaseConfig, fallback string) string {
	if db != nil && db.Type != "" {
		return db.Type
	}
	return fallback
}

// buildEnvironmentVariables builds Flyway environment variables for the supplied database.
func buildEnvironmentVariables(db *config.DatabaseConfig) []string {
	if db == nil {
		return nil
	}

	var envVars []string

	switch db.Type {
	case config.PostgreSQL:
		envVars = append(envVars,
			fmt.Sprintf("DB_HOST=%s", db.Host),
			fmt.Sprintf("DB_PORT=%v", db.Port),
			fmt.Sprintf("DB_USER=%s", db.Username),
			fmt.Sprintf("DB_PASSWORD=%s", db.Password),
			fmt.Sprintf("DB_NAME=%s", db.Database),
		)
	case config.Oracle:
		envVars = append(envVars,
			fmt.Sprintf("ORACLE_HOST=%s", db.Host),
			fmt.Sprintf("ORACLE_PORT=%v", db.Port),
			fmt.Sprintf("ORACLE_USER=%s", db.Username),
			fmt.Sprintf("ORACLE_PASSWORD=%s", db.Password),
			fmt.Sprintf("ORACLE_PDB=%s", db.Database),
		)
	}

	return envVars
}

// validateFlywayPath valida que el path de Flyway sea seguro
func (fm *FlywayMigrator) validateFlywayPath(flywayPath string) error {
	if flywayPath == "" {
		return fmt.Errorf("flyway path cannot be empty")
	}

	// Verificar que no contenga caracteres peligrosos
	if strings.Contains(flywayPath, "..") || strings.Contains(flywayPath, ";") || strings.Contains(flywayPath, "&") {
		return fmt.Errorf("flyway path contains dangerous characters")
	}

	// Verificar que sea un path absoluto o relativo válido
	cleanPath := filepath.Clean(flywayPath)
	if cleanPath != flywayPath {
		fm.logger.Warn().
			Str("original", flywayPath).
			Str("cleaned", cleanPath).
			Msg("Flyway path was cleaned, potential security issue")
	}

	// Verificar que el archivo exista (opcional, pero recomendado para seguridad)
	if _, err := os.Stat(cleanPath); os.IsNotExist(err) {
		return fmt.Errorf("flyway executable not found at path: %s", cleanPath)
	}

	return nil
}

// RunMigrationsAtStartup executes migrations automatically at application startup
func (fm *FlywayMigrator) RunMigrationsAtStartup(ctx context.Context) error {
	migrationConfig := fm.DefaultMigrationConfig()

	// In development, run migrations automatically
	if fm.config.App.Env == config.EnvDevelopment {
		fm.logger.Info().Msg("Running automatic migrations in development environment")
		return fm.Migrate(ctx, migrationConfig)
	}

	// In other environments, only validate
	fm.logger.Info().Msg("Validating migrations in non-development environment")
	return fm.Validate(ctx, migrationConfig)
}
