// Package migration provides integration with Flyway for database migrations
package migration

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
)

const (
	// Flyway command flag constants
	flagConfigFiles    = "-configFiles="
	flagLocationsFS    = "-locations=filesystem:"
	flagOutputTypeJSON = "-outputType=json"

	flywayExecutable  = "flyway"
	flywayCmdMigrate  = "migrate"
	flywayCmdValidate = "validate"
	flywayCmdInfo     = "info"

	// minRedactablePasswordLength gates substring-style password redaction.
	// Short needles (1-7 bytes) are statistically likely to appear inside
	// unrelated Flyway output (timestamps, error codes, table names),
	// causing false-positive over-redaction that obscures the real diagnostic
	// AND failing to mask encoded variants whose alphabet differs from the
	// raw password. 8 bytes matches the same minimum we expect at config
	// validation; below that we drop the output entirely. ASCII-only sentinel
	// so log pipelines without UTF-8 don't mangle it.
	minRedactablePasswordLength = 8
	outputSuppressedSentinel    = "[REDACTED -- output suppressed: password too short for safe substring redaction]"
)

// FlywayMigrator handles database migrations using Flyway
type FlywayMigrator struct {
	config         *config.Config
	logger         logger.Logger
	defaultConfig  func(*FlywayMigrator) *Config
	validatedPaths sync.Map
	audit          *auditEmitter
}

// Config configuration for migrations
type Config struct {
	FlywayPath    string        // Path to the Flyway executable
	ConfigPath    string        // Path to the configuration file
	MigrationPath string        // Path to migration scripts
	Timeout       time.Duration // Timeout for migration operations
	Environment   string        // Environment (development, testing, production)
	DryRun        bool          // Only validate, do not execute

	// Audit carries the per-call audit-event context required by ADR-019.
	// Populated by operators (CLI flags) or pipelines (env vars or library
	// call argument). The framework will NOT infer Principal from IAM/OS
	// context — empty values flow through with a warning log.
	Audit AuditContext
}

// AuditContext groups the per-call audit fields that flow into every
// migration.applied event. Operators MUST supply Principal explicitly per
// ADR-019; GitCommitSHA, PipelineRunID, and Target are optional but
// strongly recommended for deployment-time runs.
type AuditContext struct {
	// Principal identifies who triggered the migration (operator username,
	// service account name, pipeline identifier). Empty values emit with
	// PrincipalUnspecified + a warning so the gap is itself auditable.
	Principal string

	// GitCommitSHA records the source-tree commit the migration was built
	// from. Useful for correlating an audit event to a specific deployment.
	GitCommitSHA string

	// PipelineRunID is an opaque CI/CD run identifier (e.g. a GitHub
	// Actions run ID, a Jenkins build number). Lets compliance reporting
	// trace an audit event back to a pipeline run.
	PipelineRunID string

	// Target overrides the audit event's Target field. Defaults to the
	// database name (db.Database) when empty. Useful for multi-tenant runs
	// where the tenant ID is more informative for compliance correlation
	// than the per-tenant schema name.
	Target string
}

// NewFlywayMigrator creates a new instance of the migrator with the
// always-on OpenTelemetry audit-emission path wired up. Call WithAuditRecorder
// to add an optional compliance-grade durable delivery path per ADR-019.
func NewFlywayMigrator(cfg *config.Config, log logger.Logger) *FlywayMigrator {
	fm := &FlywayMigrator{
		config: cfg,
		logger: log,
		audit:  newAuditEmitter(log, nil),
	}

	fm.defaultConfig = (*FlywayMigrator).defaultMigrationConfig

	return fm
}

// WithAuditRecorder registers an optional AuditRecorder for compliance-grade durable
// delivery alongside the always-on OpenTelemetry emission. Replaces any
// previously-configured sink. Returns the receiver for chaining.
//
// Intended to be called once at startup. The sink runs on its own goroutine
// with a bounded send queue per ADR-019 — slow sinks cannot stall
// migrations, and sink errors are logged but do not abort the migration.
// Call Close to drain the queue on shutdown.
func (fm *FlywayMigrator) WithAuditRecorder(sink AuditRecorder) *FlywayMigrator {
	// Best-effort drain of any previously-installed sink so swapping does
	// not leak the consumer goroutine. Uses a short background deadline
	// since this is a setup-time operation, not a request path.
	if fm.audit != nil && fm.audit.sink != nil {
		drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := fm.audit.Close(drainCtx); err != nil {
			// Surface drain failures so silent audit loss during a recorder
			// swap is operator-visible. We still proceed with the swap —
			// blocking a startup-time reconfiguration on an in-flight sink
			// would be worse than logging and moving on.
			fm.logger.Warn().Err(err).Msg("failed to drain previous audit recorder during swap")
		}
		cancel()
	}
	fm.audit = newAuditEmitter(fm.logger, sink)
	return fm
}

// Close drains the optional AuditRecorder queue and tears down the audit
// consumer goroutine. Safe to call when no sink is configured. Honors ctx
// for shutdown deadline; events still in flight when ctx expires are
// silently dropped (their OTel emission already succeeded).
func (fm *FlywayMigrator) Close(ctx context.Context) error {
	if fm == nil || fm.audit == nil {
		return nil
	}
	return fm.audit.Close(ctx)
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
// own cfg.Database.Type. Unknown vendors fall back to the migrator's
// configured Database.Type so the vendor string never reaches filesystem path
// interpolation unvalidated; if even that is unknown, an "unknown" segment is
// used so callers see an obvious error rather than a path-traversal artifact.
func (fm *FlywayMigrator) DefaultMigrationConfigForVendor(vendor string) *Config {
	safeVendor := safeVendorSegment(vendor, fm.config.Database.Type)
	return &Config{
		FlywayPath:    flywayExecutable,
		ConfigPath:    fmt.Sprintf("flyway/flyway-%s.conf", safeVendor),
		MigrationPath: fmt.Sprintf("migrations/%s", safeVendor),
		Timeout:       5 * time.Minute,
		Environment:   fm.config.App.Env,
		DryRun:        false,
	}
}

// safeVendorSegment returns vendor when it matches a supported go-bricks
// database type, otherwise falls back to the migrator's configured vendor
// (also validated), and finally to a neutral "unknown" sentinel. Prevents
// tenant DatabaseConfig values like "../../tmp" from escaping the flyway/ and
// migrations/ directories via fmt.Sprintf path interpolation. The supported
// set is sourced from database.ValidateDatabaseType so it stays single-sourced
// alongside the connection-factory's own check.
func safeVendorSegment(vendor, fallback string) string {
	if database.ValidateDatabaseType(vendor) == nil {
		return vendor
	}
	if database.ValidateDatabaseType(fallback) == nil {
		return fallback
	}
	return "unknown"
}

func (fm *FlywayMigrator) defaultMigrationConfig() *Config {
	return fm.DefaultMigrationConfigForVendor(fm.config.Database.Type)
}

// Migrate executes pending migrations against the migrator's configured
// database. The Result carries the parsed per-target outcome; on parse
// failure it is zero-valued and the error return is authoritative.
func (fm *FlywayMigrator) Migrate(ctx context.Context, cfg *Config) (Result, error) {
	if cfg == nil {
		cfg = fm.DefaultMigrationConfig()
	}
	return fm.MigrateFor(ctx, &fm.config.Database, cfg)
}

// MigrateFor executes pending migrations against the supplied database.
// Used by multi-tenant migrations to target a tenant-specific DatabaseConfig.
// See Migrate for the Result contract.
func (fm *FlywayMigrator) MigrateFor(ctx context.Context, db *config.DatabaseConfig, cfg *Config) (Result, error) {
	return fm.runFor(ctx, db, cfg, flywayCmdMigrate)
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
	_, err := fm.runFor(ctx, db, cfg, flywayCmdInfo)
	return err
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
	_, err := fm.runFor(ctx, db, cfg, flywayCmdValidate)
	return err
}

func (fm *FlywayMigrator) runFor(ctx context.Context, db *config.DatabaseConfig, cfg *Config, verb string) (Result, error) {
	startedAt := time.Now()

	if cfg == nil {
		cfg = fm.DefaultMigrationConfigForVendor(dbVendor(db, fm.config.Database.Type))
	}

	vendor := dbVendor(db, fm.config.Database.Type)
	fm.logger.Info().
		Str("vendor", vendor).
		Str("action", verb).
		Msg("Starting Flyway command")

	// Info / validate keep their default pretty-printed output for operators
	// running them at the CLI; migrate switches to JSON so we can parse a
	// structured Result for the audit pipeline and CLI consumers.
	isMigrate := verb == flywayCmdMigrate
	args := []string{
		flagConfigFiles + cfg.ConfigPath,
		flagLocationsFS + cfg.MigrationPath,
	}
	if isMigrate {
		args = append(args, flagOutputTypeJSON)
	}
	args = append(args, verb)

	output, runErr := fm.runFlywayCommandFor(ctx, db, cfg, args)

	if !isMigrate {
		return Result{}, runErr
	}

	result, parseErr := parseFlywayJSON(output)
	if parseErr != nil {
		fm.logger.Debug().Err(parseErr).Msg("Failed to parse Flyway JSON output")
	}

	// ADR-019: migration.applied fires only for actual applications.
	fm.emitMigrationApplied(ctx, db, cfg, &flywayRunOutcome{
		Vendor:      vendor,
		StartedAt:   startedAt,
		CompletedAt: time.Now(),
		Output:      output,
		Result:      result,
		Err:         runErr,
	})

	return result, runErr
}

// flywayRunOutcome bundles the result of a single Flyway subprocess
// invocation so emitMigrationApplied keeps a small parameter list. Used
// only by the engine layer; never exported.
type flywayRunOutcome struct {
	Vendor      string
	StartedAt   time.Time
	CompletedAt time.Time
	Output      string
	Result      Result
	Err         error
}

// runFlywayCommandFor executes a Flyway command using the supplied database
// config. Returns the (password-redacted) combined output alongside any
// error so the caller can classify failures into an ErrorClass per ADR-019.
func (fm *FlywayMigrator) runFlywayCommandFor(ctx context.Context, db *config.DatabaseConfig, cfg *Config, args []string) (string, error) {
	if err := fm.validateFlywayPath(cfg.FlywayPath); err != nil {
		return "", fmt.Errorf("invalid flyway path: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	// #nosec G204 -- FlywayPath is validated by validateFlywayPath function
	cmd := exec.CommandContext(timeoutCtx, cfg.FlywayPath, args...)

	envVars, err := buildEnvironmentVariables(db)
	if err != nil {
		return "", fmt.Errorf("build flyway environment: %w", err)
	}
	cmd.Env = append(os.Environ(), envVars...)

	rawOutput, err := cmd.CombinedOutput()
	redacted := redactPassword(string(rawOutput), db)
	if err != nil {
		fm.logger.Error().Err(err).Str("output", redacted).Msg("Error executing Flyway command")
		return redacted, fmt.Errorf("flyway command failed: %w", err)
	}

	fm.logger.Info().Msg("Flyway command completed successfully")
	return redacted, nil
}

// emitMigrationApplied composes an AuditEvent of type migration.applied and
// dispatches it through the configured emitter (always OTel; optionally
// AuditRecorder). Version maps to the parsed Result.EndingVersion — the
// schema version after the run, equal on both fresh-apply and no-op reruns.
func (fm *FlywayMigrator) emitMigrationApplied(ctx context.Context, db *config.DatabaseConfig, cfg *Config, run *flywayRunOutcome) {
	if fm.audit == nil {
		return
	}

	target := cfg.Audit.Target
	if target == "" && db != nil {
		target = db.Database
	}
	if target == "" {
		// Final fallback: the migrator's own configured database name.
		// Prevents an empty Target from reaching the audit pipeline when
		// the per-call db and the operator-supplied override are both
		// missing — a degenerate config that shouldn't happen in
		// practice, but the audit record stays useful regardless.
		target = fm.config.Database.Database
	}

	attrs := map[string]string{
		"migration.vendor":  run.Vendor,
		"migration.dry_run": strconv.FormatBool(cfg.DryRun),
	}
	if run.Result.FlywayVersion != "" {
		attrs["migration.flyway_version"] = run.Result.FlywayVersion
	}
	if applied := run.Result.AppliedVersions; len(applied) > 0 {
		attrs["migration.applied_versions"] = strings.Join(applied, ",")
	}

	ev := &AuditEvent{
		Type:               AuditEventTypeMigrationApplied,
		Target:             target,
		AppliedByPrincipal: cfg.Audit.Principal,
		StartedAt:          run.StartedAt,
		CompletedAt:        run.CompletedAt,
		GitCommitSHA:       cfg.Audit.GitCommitSHA,
		PipelineRunID:      cfg.Audit.PipelineRunID,
		Version:            run.Result.EndingVersion,
		Attributes:         attrs,
	}

	if run.Err != nil {
		ev.Outcome = AuditOutcomeFailed
		ev.ErrorClass = classifyFlywayError(run.Output, run.Err)
	} else {
		ev.Outcome = AuditOutcomeSuccess
	}

	fm.audit.emit(ctx, ev)
}

// dbVendor returns the database vendor string from the supplied config, falling
// back to the migrator's default when the config has no Type set.
func dbVendor(db *config.DatabaseConfig, fallback string) string {
	if db != nil && db.Type != "" {
		return db.Type
	}
	return fallback
}

// ErrEnvFieldHasControlChar is returned when a DatabaseConfig field destined
// for the Flyway subprocess environment contains a forbidden control character
// (CR, LF, or NUL). Go's exec.Cmd.Env passes strings to execve(2) verbatim and
// does not split on newlines, so this isn't a known injection path — but
// rejecting at the boundary prevents a compromised secret writer from
// propagating multi-line surprises into downstream logs or env-parsing tools.
var ErrEnvFieldHasControlChar = errors.New("migration: env field contains forbidden control character (CR/LF/NUL)")

// buildEnvironmentVariables builds Flyway environment variables for the supplied database.
// Returns ErrEnvFieldHasControlChar wrapped with the offending field name when
// any of Host/Username/Password/Database contains CR, LF, or NUL.
func buildEnvironmentVariables(db *config.DatabaseConfig) ([]string, error) {
	if db == nil {
		return nil, nil
	}

	if err := validateEnvFields(db); err != nil {
		return nil, err
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

	return envVars, nil
}

const (
	envFieldHost     = "Host"
	envFieldUsername = "Username"
	envFieldPassword = "Password"
	envFieldDatabase = "Database"
)

// validateEnvFields rejects any DatabaseConfig string field that would be
// formatted into the Flyway subprocess environment if it contains CR, LF, or
// NUL. The error names the offending field but never echoes the value (since
// it may be the password).
func validateEnvFields(db *config.DatabaseConfig) error {
	fields := []struct {
		name  string
		value string
	}{
		{envFieldHost, db.Host},
		{envFieldUsername, db.Username},
		{envFieldPassword, db.Password},
		{envFieldDatabase, db.Database},
	}
	for _, f := range fields {
		if strings.ContainsAny(f.value, "\r\n\x00") {
			return fmt.Errorf("%w: %s", ErrEnvFieldHasControlChar, f.name)
		}
	}
	return nil
}

// validateFlywayPath ensures the Flyway path is non-empty, free of shell
// metacharacters, and points at an existing file. Successful validations are
// memoized per FlywayMigrator so multi-tenant runs don't re-stat the binary.
func (fm *FlywayMigrator) validateFlywayPath(flywayPath string) error {
	if _, ok := fm.validatedPaths.Load(flywayPath); ok {
		return nil
	}
	if flywayPath == "" {
		return fmt.Errorf("flyway path cannot be empty")
	}
	if strings.Contains(flywayPath, "..") || strings.Contains(flywayPath, ";") || strings.Contains(flywayPath, "&") {
		return fmt.Errorf("flyway path contains dangerous characters")
	}

	cleanPath := filepath.Clean(flywayPath)
	if cleanPath != flywayPath {
		fm.logger.Warn().
			Str("original", flywayPath).
			Str("cleaned", cleanPath).
			Msg("Flyway path was cleaned, potential security issue")
	}

	if _, err := os.Stat(cleanPath); os.IsNotExist(err) {
		return fmt.Errorf("flyway executable not found at path: %s", cleanPath)
	}

	fm.validatedPaths.Store(flywayPath, struct{}{})
	return nil
}

// redactPassword scrubs the database password from Flyway's stdout/stderr so
// connection-string echoes in error logs don't leak credentials. Flyway echoes
// resolved JDBC URLs (jdbc:postgresql://user:password@host/db) on connection
// failures, and JDBC drivers percent-encode reserved characters in the
// userinfo segment per RFC 3986 — so we redact both the raw password and its
// PathEscape / QueryEscape forms. Passwords shorter than
// minRedactablePasswordLength are not substring-redacted (they collide with
// unrelated bytes); we drop the output entirely instead.
func redactPassword(output string, db *config.DatabaseConfig) string {
	if db == nil || db.Password == "" {
		return output
	}
	if len(db.Password) < minRedactablePasswordLength {
		return outputSuppressedSentinel
	}

	const placeholder = "[REDACTED]"
	redacted := strings.ReplaceAll(output, db.Password, placeholder)

	// JDBC URL userinfo uses RFC 3986 percent-encoding (PathEscape semantics).
	if encoded := url.PathEscape(db.Password); encoded != db.Password {
		redacted = strings.ReplaceAll(redacted, encoded, placeholder)
	}
	// Belt and suspenders: form-encoded variant (spaces as '+') in case any
	// helper logs the password in application/x-www-form-urlencoded form.
	if encoded := url.QueryEscape(db.Password); encoded != db.Password {
		redacted = strings.ReplaceAll(redacted, encoded, placeholder)
	}
	return redacted
}

// RunMigrationsAtStartup executes migrations automatically at application
// startup. The structured Result is discarded here; downstream consumers
// pick it up via the migration.applied audit event.
func (fm *FlywayMigrator) RunMigrationsAtStartup(ctx context.Context) error {
	migrationConfig := fm.DefaultMigrationConfig()

	// In development, run migrations automatically
	if fm.config.App.IsDevelopment() {
		fm.logger.Info().Msg("Running automatic migrations in development environment")
		_, err := fm.Migrate(ctx, migrationConfig)
		return err
	}

	// In other environments, only validate
	fm.logger.Info().Msg("Validating migrations in non-development environment")
	return fm.Validate(ctx, migrationConfig)
}
