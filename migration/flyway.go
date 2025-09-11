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

// FlywayMigrator handles database migrations using Flyway
type FlywayMigrator struct {
	config *config.Config
	logger logger.Logger
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
	return &FlywayMigrator{
		config: cfg,
		logger: log,
	}
}

// GetDefaultMigrationConfig gets the default configuration for migrations
func (fm *FlywayMigrator) GetDefaultMigrationConfig() *Config {
	vendor := fm.config.Database.Type

	return &Config{
		FlywayPath:    "flyway",
		ConfigPath:    fmt.Sprintf("flyway/flyway-%s.conf", vendor),
		MigrationPath: fmt.Sprintf("migrations/%s", vendor),
		Timeout:       5 * time.Minute,
		Environment:   fm.config.App.Env,
		DryRun:        false,
	}
}

// Migrate executes pending migrations
func (fm *FlywayMigrator) Migrate(ctx context.Context, cfg *Config) error {
	if cfg == nil {
		cfg = fm.GetDefaultMigrationConfig()
	}

	fm.logger.Info().Str("vendor", fm.config.Database.Type).Msg("Starting database migrations")

	// Execute Flyway command
	args := []string{
		"-configFiles=" + cfg.ConfigPath,
		"-locations=filesystem:" + cfg.MigrationPath,
		"migrate",
	}

	return fm.runFlywayCommand(ctx, cfg, args)
}

// Info shows information about the status of migrations
func (fm *FlywayMigrator) Info(ctx context.Context, cfg *Config) error {
	if cfg == nil {
		cfg = fm.GetDefaultMigrationConfig()
	}

	args := []string{
		"-configFiles=" + cfg.ConfigPath,
		"-locations=filesystem:" + cfg.MigrationPath,
		"info",
	}

	return fm.runFlywayCommand(ctx, cfg, args)
}

// Validate validates migrations without executing them
func (fm *FlywayMigrator) Validate(ctx context.Context, cfg *Config) error {
	if cfg == nil {
		cfg = fm.GetDefaultMigrationConfig()
	}

	args := []string{
		"-configFiles=" + cfg.ConfigPath,
		"-locations=filesystem:" + cfg.MigrationPath,
		"validate",
	}

	return fm.runFlywayCommand(ctx, cfg, args)
}

// runFlywayCommand executes a Flyway command
func (fm *FlywayMigrator) runFlywayCommand(ctx context.Context, cfg *Config, args []string) error {
	// Validate FlywayPath para prevenir inyección de comandos
	if err := fm.validateFlywayPath(cfg.FlywayPath); err != nil {
		return fmt.Errorf("invalid flyway path: %w", err)
	}

	// Create context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	// Prepare command
	// #nosec G204 -- FlywayPath is validated by validateFlywayPath function
	cmd := exec.CommandContext(timeoutCtx, cfg.FlywayPath, args...)

	// Set environment variables
	cmd.Env = append(os.Environ(), fm.buildEnvironmentVariables()...)

	// Execute command
	output, err := cmd.CombinedOutput()

	if err != nil {
		fm.logger.Error().Err(err).Str("output", string(output)).Msg("Error executing Flyway command")
		return fmt.Errorf("flyway command failed: %w", err)
	}

	fm.logger.Info().Msg("Flyway command completed successfully")
	return nil
}

// buildEnvironmentVariables builds environment variables for Flyway
func (fm *FlywayMigrator) buildEnvironmentVariables() []string {
	var envVars []string

	db := fm.config.Database

	switch db.Type {
	case "postgresql":
		envVars = append(envVars,
			fmt.Sprintf("DB_HOST=%s", db.Host),
			fmt.Sprintf("DB_PORT=%v", db.Port),
			fmt.Sprintf("DB_USER=%s", db.Username),
			fmt.Sprintf("DB_PASSWORD=%s", db.Password),
			fmt.Sprintf("DB_NAME=%s", db.Database),
		)
	case "oracle":
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
	migrationConfig := fm.GetDefaultMigrationConfig()

	// In development, run migrations automatically
	if fm.config.App.Env == "development" {
		fm.logger.Info().Msg("Running automatic migrations in development environment")
		return fm.Migrate(ctx, migrationConfig)
	}

	// In other environments, only validate
	fm.logger.Info().Msg("Validating migrations in non-development environment")
	return fm.Validate(ctx, migrationConfig)
}
