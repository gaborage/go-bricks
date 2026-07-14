// Package migration provides integration with Flyway for database migrations
package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	// unrelated Flyway output (timestamps, error codes, table names), causing
	// false-positive over-redaction that obscures the real diagnostic AND failing
	// to mask encoded variants whose alphabet differs from the raw password. It is
	// single-sourced from config.MinDatabasePasswordLength, which config.Validate
	// now enforces for static configs; the migrate path additionally rejects a
	// short password up front (see ErrDatabasePasswordTooShort), covering
	// per-tenant configs. The whole-output suppression below therefore remains
	// only as a fallback for the non-migrate (info/validate) verbs, whose output
	// is not parsed. ASCII-only sentinel so log pipelines without UTF-8 don't
	// mangle it.
	minRedactablePasswordLength = config.MinDatabasePasswordLength
	outputSuppressedSentinel    = "[REDACTED -- output suppressed: password too short for safe substring redaction]"
	redactionPlaceholder        = "[REDACTED]"

	// flywayKillGraceDelay bounds how long Wait blocks on I/O AFTER the process
	// group has been signaled — a liveness backstop against an orphan still holding
	// the output pipe, not a migration policy. Deliberately independent of
	// Config.Timeout: deriving it (e.g. Timeout/10) would shrink the backstop to 3s
	// under a `--timeout 30s` run and stretch it to 6 minutes under a 1h Oracle
	// timeout — reintroducing the very hang it exists to bound. Do not couple them.
	flywayKillGraceDelay = 10 * time.Second
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
	Environment   string        // Environment (e.g. "development", "staging", "production", or any org-specific alias such as "local"/"stg"/"prd" — not enum-validated per ADR-022)
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
// database. It returns a non-nil error when the Flyway process fails, when its
// JSON output is empty/malformed/redaction-suppressed (errors.Is
// ErrFlywayOutputUnparsed), or when Flyway reports a failed migration via a
// success=false envelope even on a zero exit (errors.Is ErrFlywayReportedFailure).
// The Result is returned best-effort alongside any error.
func (fm *FlywayMigrator) Migrate(ctx context.Context, cfg *Config) (Result, error) {
	if cfg == nil {
		cfg = fm.DefaultMigrationConfig()
	}
	return fm.MigrateFor(ctx, &fm.config.Database, cfg)
}

// MigrateFor executes pending migrations against the supplied database.
// Used by multi-tenant migrations to target a tenant-specific DatabaseConfig.
// See Migrate for the Result and error contract.
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

// effectiveVerb downgrades a dry-run migrate to the validate verb so Config.DryRun
// mutates nothing. Non-migrate verbs and non-dry-run migrates are returned unchanged.
func effectiveVerb(cfg *Config, verb string) string {
	if cfg.DryRun && verb == flywayCmdMigrate {
		return flywayCmdValidate
	}
	return verb
}

func (fm *FlywayMigrator) runFor(ctx context.Context, db *config.DatabaseConfig, cfg *Config, verb string) (Result, error) {
	startedAt := time.Now()

	if cfg == nil {
		cfg = fm.DefaultMigrationConfigForVendor(dbVendor(db, fm.config.Database.Type))
	}

	// Honor DryRun: downgrade a dry-run migrate to validate so no schema is mutated.
	// validate takes the non-migrate path below, which emits no migration.applied audit
	// (ADR-019 fires that only for actual applications) — so a dry run is never recorded
	// as an application, and migration.dry_run stays accurate on real applications.
	verb = effectiveVerb(cfg, verb)

	// A migrate whose password is too short to redact safely is rejected up front
	// rather than run-then-suppressed (which would hide the outcome and audit a
	// real success as failed). This covers per-tenant configs that never pass
	// through config.Validate (#675).
	if verb == flywayCmdMigrate {
		if err := ensurePasswordRedactable(db); err != nil {
			return Result{}, err
		}
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
	outErr := migrateOutcome(runErr, parseErr, &result)

	// ADR-019: migration.applied fires only for actual applications. Feeding the
	// classified error (not just runErr) records an unparsable output or a
	// success=false envelope as AuditOutcomeFailed instead of a silent
	// AuditOutcomeSuccess (#673).
	fm.emitMigrationApplied(ctx, db, cfg, &flywayRunOutcome{
		Vendor:      vendor,
		StartedAt:   startedAt,
		CompletedAt: time.Now(),
		Output:      output,
		Result:      result,
		Err:         outErr,
	})

	return result, outErr
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
	// Without WaitDelay, an orphaned JVM holding the output pipe makes
	// CombinedOutput block indefinitely, turning the timeout into a hang.
	cmd.WaitDelay = flywayKillGraceDelay
	// POSIX only: on Windows the JVM is NOT killed (see proc_windows.go) and
	// WaitDelay alone bounds the hang.
	configureProcessGroup(cmd)

	envVars, err := buildEnvironmentVariables(db)
	if err != nil {
		return "", fmt.Errorf("build flyway environment: %w", err)
	}
	cmd.Env = append(os.Environ(), envVars...)

	startedAt := time.Now()
	rawOutput, err := cmd.CombinedOutput()
	if err != nil && errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		// Elapsed, not cfg.Timeout: timeoutCtx inherits an earlier parent deadline
		// (e.g. a MigrateAll budget), so cfg.Timeout would overstate the wait.
		// killScopeDesc is build-tagged: the message must not promise a process-group
		// teardown on Windows, where the JVM child tree survives.
		err = fmt.Errorf("%w after %s and %s; schema state is unknown "+
			"(a partially applied migration is possible): %w",
			ErrFlywayTimeout, time.Since(startedAt).Round(time.Millisecond), killScopeDesc, err)
	}
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
		"migration.vendor": run.Vendor,
		// Always "false" in practice: a dry-run migrate is downgraded to validate (see
		// effectiveVerb), which never reaches this emit path (ADR-019 — migration.applied
		// fires only for actual applications). The attribute is retained so every emitted
		// event positively asserts "this was a real application, not a rehearsal".
		"migration.dry_run": strconv.FormatBool(cfg.DryRun),
	}
	if errors.Is(run.Err, ErrFlywayTimeout) {
		// classifyFlywayError only sees Flyway's stdout, which a SIGKILLed JVM never
		// writes — so a timeout would otherwise audit as the internal_error catch-all,
		// indistinguishable from a JVM crash. Attributes is ADR-019's free-form seam,
		// so alerting can pin "schema state unknown, do not auto-retry" without a
		// taxonomy change and without string-matching the error text.
		attrs["migration.timed_out"] = "true"
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

	fm.audit.Emit(ctx, ev)
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

// ErrDatabasePasswordTooShort is returned by the migrate path when a non-empty
// database password is shorter than minRedactablePasswordLength. Such passwords
// cannot be safely redacted from Flyway output, so the migration is rejected
// before running rather than suppressing the output and hiding the outcome.
// Match with errors.Is. Empty passwords (trust/IAM auth) are exempt.
var ErrDatabasePasswordTooShort = errors.New("migration: database password too short to safely redact Flyway output")

// ErrFlywayTimeout marks a run the framework killed on its own deadline. The
// schema state is UNKNOWN: Flyway may have applied part of a migration before
// the process group died, so this is not a clean failure and an automatic retry
// can race a partially applied change. Match with errors.Is — the audit
// ErrorClass cannot carry this (classifyFlywayError matches on Flyway's stdout,
// which a SIGKILLed JVM never gets to write), so the machine-readable signals
// are this sentinel and the migration.timed_out audit attribute.
var ErrFlywayTimeout = errors.New("migration: flyway timed out")

// ensurePasswordRedactable rejects a non-empty database password that is too
// short to substring-redact from Flyway output. The error never echoes the
// password.
func ensurePasswordRedactable(db *config.DatabaseConfig) error {
	if db == nil || db.Password == "" {
		return nil
	}
	if len(db.Password) < minRedactablePasswordLength {
		return ErrDatabasePasswordTooShort
	}
	return nil
}

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

// validateFlywayPath ensures the Flyway path is non-empty, rejects path
// traversal ("..") and the ";"/"&" substrings, and points at an existing
// file. Successful validations are memoized per FlywayMigrator so
// multi-tenant runs don't re-stat the binary.
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
// failures, and JDBC drivers percent-encode reserved characters in the userinfo
// segment per RFC 3986 — so we redact the raw password plus its PathEscape,
// QueryEscape, and JSON-string-escaped forms. Passwords shorter than
// minRedactablePasswordLength are not substring-redacted (they collide with
// unrelated bytes); we drop the output entirely instead.
func redactPassword(output string, db *config.DatabaseConfig) string {
	if db == nil || db.Password == "" {
		return output
	}
	if len(db.Password) < minRedactablePasswordLength {
		return outputSuppressedSentinel
	}

	forms := passwordRedactionForms(db.Password)
	// For valid-JSON output, redact only inside string literals. Flyway echoes the
	// password only within strings (JDBC URLs in error messages), never as a bare
	// token, so string-scoped redaction masks every real credential while leaving
	// numbers and structure intact: a numeric password can't corrupt a number
	// token (which would turn a real success into an unparsable-output failure,
	// #673), and a genuine credential is never restored by an all-or-nothing
	// revert. Non-JSON error text is redacted byte-for-byte.
	if json.Valid([]byte(output)) {
		return redactWithinJSONStrings(output, forms)
	}
	return replaceAllForms(output, forms)
}

// passwordRedactionForms returns the distinct byte-forms the password can take
// in Flyway output: the raw bytes plus its percent-encoded userinfo (JDBC URLs),
// form-encoded, and JSON-string-escaped variants.
func passwordRedactionForms(pw string) []string {
	forms := []string{pw}
	for _, enc := range []string{url.PathEscape(pw), url.QueryEscape(pw), jsonStringEscape(pw)} {
		if !slices.Contains(forms, enc) {
			forms = append(forms, enc)
		}
	}
	return forms
}

// replaceAllForms replaces every form of the password with the placeholder.
func replaceAllForms(s string, forms []string) string {
	for _, f := range forms {
		s = strings.ReplaceAll(s, f, redactionPlaceholder)
	}
	return s
}

// redactWithinJSONStrings replaces password forms with the placeholder, but only
// inside JSON string literals, so number/bareword tokens stay untouched and the
// envelope remains valid JSON. Assumes s is valid JSON (the caller checks), so
// string literals are well-formed and the escape scan cannot overrun.
func redactWithinJSONStrings(s string, forms []string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '"' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// Scan the string literal to its closing quote, skipping escaped bytes.
		j := i + 1
		for j < len(s) {
			if s[j] == '\\' {
				j += 2
				continue
			}
			if s[j] == '"' {
				j++
				break
			}
			j++
		}
		if j > len(s) {
			j = len(s)
		}
		b.WriteString(replaceAllForms(s[i:j], forms))
		i = j
	}
	return b.String()
}

// jsonStringEscape returns s as it appears inside a JSON string value
// (backslash-escaped), without the surrounding quotes. Returns s unchanged if
// marshaling fails (it cannot for a Go string).
func jsonStringEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	return string(b[1 : len(b)-1])
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
