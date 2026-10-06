package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/migration"
	httpsource "github.com/gaborage/go-bricks/migration/source/http"
	staticsource "github.com/gaborage/go-bricks/migration/source/static"
	"github.com/gaborage/go-bricks/tools/migration/internal/awssm"
)

const (
	credsSourceAWS  = "aws-secrets-manager"
	credsSourceFile = "config-file"

	envSourceToken   = "GOBRICKS_MIGRATE_SOURCE_TOKEN"
	envSecretsPrefix = "GOBRICKS_MIGRATE_SECRETS_PREFIX"
	envAppliedBy     = "GOBRICKS_MIGRATE_APPLIED_BY"
	envGitSHA        = "GOBRICKS_MIGRATE_GIT_SHA"
	envPipelineRunID = "GOBRICKS_MIGRATE_PIPELINE_RUN_ID"

	envMigratorUser     = "GOBRICKS_MIGRATE_MIGRATOR_USER"
	envMigratorPassword = "GOBRICKS_MIGRATE_MIGRATOR_PASSWORD"

	envSharedMigrator = "GOBRICKS_MIGRATE_SHARED_MIGRATOR"

	// migratorOverlayLogMsg records that Flyway will connect as the migrator. The
	// username is logged beside it; the password never is.
	migratorOverlayLogMsg = "Migrator identity overlay active"

	// sharedMigratorArmedLogMsg records that every tenant must carry a supported
	// type and, on PostgreSQL, an explicit postgresql.schema.
	sharedMigratorArmedLogMsg = "Shared migrator guard armed"

	jsonKeyTenants = "tenants"

	// migrateCLIAppName is reported as the built JDBC URL's application_name, so a
	// migration run by this CLI is identifiable in pg_stat_activity.
	migrateCLIAppName = "go-bricks-migrate"
)

// resolveFlags applies env-var fallbacks and validates flags in place.
// cmd is consulted via Flags().Changed so an explicit --secrets-prefix on the
// command line wins over the env override even when both equal the default.
func resolveFlags(cmd *cobra.Command, flags *CommonFlags) error {
	if flags.SourceToken == "" {
		flags.SourceToken = os.Getenv(envSourceToken)
	}
	if v := os.Getenv(envSecretsPrefix); v != "" && !cmd.Flags().Changed("secrets-prefix") {
		flags.SecretsPrefix = v
	}

	// Audit env fallbacks — an explicit flag always wins (Changed check).
	applyEnvFallback(cmd, "applied-by", envAppliedBy, &flags.AppliedBy)
	applyEnvFallback(cmd, "git-sha", envGitSHA, &flags.GitSHA)
	applyEnvFallback(cmd, "pipeline-run-id", envPipelineRunID, &flags.PipelineRunID)

	if flags.Tenant == "" && flags.SourceURL == "" && flags.SourceConfig == "" {
		return errors.New("one of --source-url, --source-config, or --tenant is required")
	}
	if flags.SourceURL != "" && flags.SourceConfig != "" {
		return errors.New("--source-url and --source-config are mutually exclusive")
	}

	switch flags.CredentialsFrom {
	case credsSourceAWS, credsSourceFile:
	default:
		return fmt.Errorf("--credentials-from %q invalid; expected %q or %q", flags.CredentialsFrom, credsSourceAWS, credsSourceFile)
	}
	if flags.CredentialsFrom == credsSourceFile && flags.SourceConfig == "" {
		return errors.New("--credentials-from=config-file requires --source-config")
	}

	if flags.Parallel < 1 {
		flags.Parallel = 1
	}
	return nil
}

// applyEnvFallback sets *dst from envVar when the operator did not pass flagName
// explicitly (an explicit flag always wins, even when it equals the default).
func applyEnvFallback(cmd *cobra.Command, flagName, envVar string, dst *string) {
	if cmd.Flags().Changed(flagName) {
		return
	}
	if v := os.Getenv(envVar); v != "" {
		*dst = v
	}
}

// resolveMigratorIdentity reads the migrator overlay from the environment. Both
// variables or neither, paired by presence rather than emptiness so a set-but-empty
// value reaches MigrateAll's own validation. Called from runAction, its only
// consumer — quiesce shares resolveFlags but never reads the pair.
func resolveMigratorIdentity(flags *CommonFlags) error {
	user, userSet := os.LookupEnv(envMigratorUser)
	password, passwordSet := os.LookupEnv(envMigratorPassword)
	if userSet != passwordSet {
		missing, present := envMigratorPassword, envMigratorUser
		if passwordSet {
			missing, present = envMigratorUser, envMigratorPassword
		}
		return fmt.Errorf("%s is required when %s is set; set both or neither", missing, present)
	}
	if userSet {
		flags.migratorIdentity = &migration.MigratorIdentity{Username: user, Password: password}
	}
	return nil
}

// maybeLoadFileStore parses the YAML config file once when either the listing
// path or the credentials path will need it, so the parse isn't repeated.
// Returns nil when no path needs the file store.
func maybeLoadFileStore(flags *CommonFlags) (*config.TenantStore, error) {
	if flags.SourceConfig == "" {
		return nil, nil
	}
	listingNeeds := flags.Tenant == "" && flags.SourceURL == ""
	credsNeeds := flags.CredentialsFrom == credsSourceFile
	if !listingNeeds && !credsNeeds {
		return nil, nil
	}
	return loadTenantStoreFromFile(flags.SourceConfig)
}

// buildLister constructs the TenantLister from the supplied flags.
// When --tenant is set, returns a single-tenant lister. fileStore, when
// non-nil, is reused for the static-source path so callers can share one
// parse with buildConfigProvider.
func buildLister(flags *CommonFlags, fileStore *config.TenantStore) (migration.TenantLister, error) {
	if flags.Tenant != "" {
		return &fixedLister{ids: []string{flags.Tenant}}, nil
	}

	if flags.SourceURL != "" {
		return httpsource.New(flags.SourceURL, httpsource.Options{
			BearerToken:         flags.SourceToken,
			AllowInsecureScheme: flags.AllowInsecureScheme,
		})
	}

	if fileStore == nil {
		var err error
		fileStore, err = loadTenantStoreFromFile(flags.SourceConfig)
		if err != nil {
			return nil, err
		}
	}
	return staticsource.FromConfigStore(fileStore), nil
}

// buildConfigProvider constructs a database.DBConfigProvider from the supplied
// flags. fileStore, when non-nil, is reused for the config-file credentials
// source so the YAML isn't parsed twice per invocation.
//
// The provider is wrapped so every resolved config passes the database.tls
// validation go-bricks applies at startup (ADR-062, mirrored in dbtls.go): the CLI
// is the first thing a fleet operator runs, and accepting a config the service will
// refuse to boot on is a worse trap than failing here.
func buildConfigProvider(ctx context.Context, flags *CommonFlags, fileStore *config.TenantStore) (database.DBConfigProvider, error) {
	inner, err := resolveConfigProvider(ctx, flags, fileStore)
	if err != nil {
		return nil, err
	}
	return &tlsValidatingProvider{inner: inner}, nil
}

// resolveConfigProvider selects the credentials source and returns its raw,
// unvalidated provider.
func resolveConfigProvider(ctx context.Context, flags *CommonFlags, fileStore *config.TenantStore) (database.DBConfigProvider, error) {
	switch flags.CredentialsFrom {
	case credsSourceAWS:
		fetcher, err := awssm.NewFetcher(ctx, awssm.Options{
			Region:   flags.AWSRegion,
			Profile:  flags.AWSProfile,
			Endpoint: flags.AWSEndpoint,
		})
		if err != nil {
			return nil, err
		}
		provider := &migration.SecretsProvider{
			Prefix: flags.SecretsPrefix,
			Fetch:  fetcher,
		}
		if err := provider.Validate(); err != nil {
			return nil, err
		}
		return provider, nil

	case credsSourceFile:
		if fileStore != nil {
			return fileStore, nil
		}
		return loadTenantStoreFromFile(flags.SourceConfig)

	default:
		return nil, fmt.Errorf("unknown credentials source: %q", flags.CredentialsFrom)
	}
}

// loadTenantStoreFromFile loads a YAML file at the supplied path and returns
// a *config.TenantStore populated from the multitenant.tenants block.
// Lookup keys mirror the standard go-bricks config layout.
//
// The tree is decoded by config.LoadFromMap, the framework's own decoder (framework
// defaults underneath, no environment, no Validate), so the file decodes as a service's Load
// decodes it at the go-bricks version this module pins: the delivered-empty and
// numeric-duration guards, the comma-split []string hook and the ADR-144 keystore tree
// reader all come from that one place. A local copy of the decoder drifted once already: it
// lacked the keystore tree reader, so a nested messaging.seal.active selector aborted the
// load and dotted keystore names decoded as phantom entries. The defaults change nothing the
// store reads: they configure no database (DBConfig("") still answers not configured) and
// reach no tenant entry, whose keys are user-chosen.
//
// One input decodes apart, so it is refused first: a top-level key containing '.'. See
// refuseTopLevelDottedKeys.
func loadTenantStoreFromFile(path string) (*config.TenantStore, error) {
	if err := validateConfigPath(path); err != nil {
		return nil, err
	}

	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("load config %q: %w", path, err)
	}

	raw := k.Raw()
	if err := refuseTopLevelDottedKeys(raw); err != nil {
		return nil, fmt.Errorf("decode config %q: %w", path, err)
	}
	cfg, err := config.LoadFromMap(raw)
	if err != nil {
		return nil, fmt.Errorf("decode config %q: %w", path, err)
	}
	return config.NewTenantStore(cfg), nil
}

// refuseTopLevelDottedKeys refuses a top-level key that contains '.' (a quoted
// "multitenant.tenants", or a flat multitenant.enabled). config.LoadFromMap reads its map as
// dotted keys and splits every top-level key on '.', in sorted order, so such a key would
// become a path and replace the nested section it names. A service's Load reads its YAML
// files with no such split: it keeps the key literal and ignores it. Decoded as is, the file
// could hand this tool tenants the service never serves; refusing keeps the two readings
// from parting. Only top-level keys are split, so a deeper dotted key is left to the
// framework's own rules.
func refuseTopLevelDottedKeys(raw map[string]any) error {
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		if strings.Contains(key, ".") {
			return fmt.Errorf("top-level key %q contains '.': a service's Load keeps it as one literal key and ignores it, "+
				"while this tool would read it as a path; write it nested, or remove it", key)
		}
	}
	return nil
}

// validateConfigPath rejects paths with shell metacharacters or traversal
// segments before passing them to the YAML loader. Matches the defensive
// posture of migration.FlywayMigrator.validateFlywayPath.
func validateConfigPath(path string) error {
	if path == "" {
		return errors.New("config file path is empty")
	}
	if strings.ContainsAny(path, ";&|`$\n") || strings.Contains(path, "..") {
		return fmt.Errorf("config file path contains unsafe characters: %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("config file %q: %w", path, err)
	}
	return nil
}

// buildBaseConfig translates flag values into a *migration.Config with only
// user-supplied fields filled. Vendor-specific defaults are applied per
// tenant inside MigrateAll, so leaving Timeout zero here lets each vendor's
// recommended timeout win; a positive --timeout overrides it via mergeConfigs.
func buildBaseConfig(flags *CommonFlags) *migration.Config {
	return &migration.Config{
		FlywayPath:    flags.FlywayPath,
		ConfigPath:    flags.FlywayConfig,
		MigrationPath: flags.MigrationsDir,
		Timeout:       flags.Timeout, // 0 or negative -> vendor default wins in mergeConfigs
		Audit: migration.AuditContext{
			Principal:     flags.AppliedBy,
			GitCommitSHA:  flags.GitSHA,
			PipelineRunID: flags.PipelineRunID,
			// Target left empty → the audit emitter falls back to each tenant's
			// db.Database when it emits migration.applied.
		},
	}
}

// Embedded-logger verbosity levels.
const (
	logLevelInfo  = "info"
	logLevelDebug = "debug"
)

// newCLILogger builds the embedded logger at the verbosity the operator
// selected. Shared by runAction and the quiesce command so the level strings
// live in one place.
func newCLILogger(flags *CommonFlags) logger.Logger {
	level := logLevelInfo
	if flags.Verbose {
		level = logLevelDebug
	}
	return logger.New(level, false)
}

// runAction is the shared entry point for migrate/validate/info subcommands.
// Every invocation emits exactly one summary record and returns an error
// carrying the run verdict the exit code is read from (ADR-115). A flag that
// did not resolve dispatched no tenant, so it is ErrNothingAttempted like any
// other pre-dispatch failure.
func runAction(cmd *cobra.Command, flags *CommonFlags, action migration.Action) error {
	out := cmd.OutOrStdout()
	if err := resolveFlags(cmd, flags); err != nil {
		return nothingAttempted(out, action, flags.JSON, err)
	}
	if err := resolveMigratorIdentity(flags); err != nil {
		// A half-set identity pair is caught before any tenant is dispatched, so
		// it reports like every other pre-dispatch failure.
		return nothingAttempted(out, action, flags.JSON, err)
	}
	if err := resolveSharedMigrator(cmd, flags); err != nil {
		return nothingAttempted(out, action, flags.JSON, err)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	lister, provider, err := buildRunDeps(ctx, flags)
	if err != nil {
		return nothingAttempted(out, action, flags.JSON, err)
	}

	log := newCLILogger(flags)

	// Construct a minimal *config.Config to satisfy FlywayMigrator's needs.
	// Per-tenant DatabaseConfig is supplied via MigrateAll. App.Name becomes the
	// built JDBC URL's application_name (ADR-085), so a DBA watching
	// pg_stat_activity sees which tool is migrating; left empty the parameter is
	// omitted entirely, which is worse than the hand-carried value in the
	// flyway.conf URLs this replaces.
	migCfg := &config.Config{App: config.AppConfig{Name: migrateCLIAppName, Env: "production"}}
	migrator := migration.NewFlywayMigrator(migCfg, log)

	if identity := flags.migratorIdentity; identity != nil {
		// The username is safe to log and tells an operator which role Flyway ran
		// as; the password is never logged, in any form.
		log.Info().Str("migrator_user", identity.Username).Msg(migratorOverlayLogMsg)
	}
	if flags.SharedMigrator {
		// Wrapped here, not in buildConfigProvider, which quiesce shares.
		migrator = migrator.WithSharedMigrator()
		provider = &sharedMigratorProvider{inner: provider}
		log.Info().Msg(sharedMigratorArmedLogMsg)
	}

	hook := makeHook(out, flags.JSON)

	result, err := migration.MigrateAll(ctx, migrator, lister, provider, action, migration.MigrateAllOptions{
		BaseConfig:       buildBaseConfig(flags),
		ContinueOnError:  flags.ContinueOnError,
		Parallelism:      flags.Parallel,
		Logger:           log,
		Hook:             hook,
		MigratorIdentity: flags.migratorIdentity,
	})
	if result == nil {
		// MigrateAll returns no result when it failed before the first dispatch;
		// an empty one carries the same verdict and lets the summary name the run.
		result = &migration.MigrateAllResult{Action: action}
	}
	writeSummary(out, result, flags.JSON)
	return verdictError(result, err)
}

// buildRunDeps resolves everything a run needs before its first dispatch. The
// three steps share the tenant store and fail the same way, so they are one
// phase: every failure here is a run that touched no schema, and collecting
// them keeps that classification at a single call site.
func buildRunDeps(ctx context.Context, flags *CommonFlags) (migration.TenantLister, database.DBConfigProvider, error) {
	fileStore, err := maybeLoadFileStore(flags)
	if err != nil {
		return nil, nil, err
	}

	lister, err := buildLister(flags, fileStore)
	if err != nil {
		return nil, nil, fmt.Errorf("build tenant lister: %w", err)
	}

	provider, err := buildConfigProvider(ctx, flags, fileStore)
	if err != nil {
		return nil, nil, fmt.Errorf("build config provider: %w", err)
	}
	return lister, provider, nil
}

// nothingAttempted reports a failure that happened before the first tenant was
// dispatched. No schema was touched, so the run is ErrNothingAttempted; the
// summary is still emitted, because a pipeline reads one record per run.
func nothingAttempted(out io.Writer, action migration.Action, asJSON bool, err error) error {
	writeSummary(out, &migration.MigrateAllResult{Action: action}, asJSON)
	return markNothingAttempted(err)
}

// verdictError turns a finished run into the error the CLI exits on. The
// verdict decides the exit code, so a run that dispatched nothing exits 2 even
// when MigrateAll returned no error at all.
func verdictError(result *migration.MigrateAllResult, err error) error {
	verdict := result.Verdict()
	switch {
	case verdict == nil:
		return err
	case err == nil:
		return verdict
	default:
		return fmt.Errorf("%w: %w", verdict, err)
	}
}

type fixedLister struct{ ids []string }

func (f *fixedLister) ListTenants(context.Context) ([]string, error) { return f.ids, nil }

// makeHook returns a TenantResult callback that streams progress to out.
// JSON mode emits the structured per-target fields populated by the engine's
// Flyway-JSON parser (applied_versions, starting_version, ending_version,
// duration_millis) so CI consumers can pin assertions on schema terminus
// without re-parsing Flyway output themselves.
func makeHook(out io.Writer, asJSON bool) func(migration.TenantResult) {
	if asJSON {
		enc := json.NewEncoder(out)
		return func(r migration.TenantResult) {
			rec := map[string]any{
				"event":     "tenant_complete",
				"tenant_id": r.TenantID,
				"vendor":    r.Vendor,
				"duration":  r.Duration.String(),
			}
			addResultFields(rec, &r.Result)
			if r.Err != nil {
				rec["error"] = r.Err.Error()
				rec["status"] = "fail"
			} else {
				rec["status"] = "ok"
			}
			_ = enc.Encode(rec)
		}
	}
	return func(r migration.TenantResult) {
		status := "ok"
		extra := ""
		if r.Err != nil {
			status = "FAIL"
			extra = ": " + r.Err.Error()
		} else if summary := formatSchemaSummary(&r.Result); summary != "" {
			extra = " " + summary
		}
		fmt.Fprintf(out, "  %s (%s) ... %s (%s)%s\n", r.TenantID, vendorOrUnknown(r.Vendor), status, r.Duration.Round(10*time.Millisecond), extra)
	}
}

// addResultFields conditionally merges Result fields into the JSON event
// record. Empty / zero-valued fields are omitted so consumers parsing the
// stream don't see noise from validate / info actions (which never populate
// a Result) or from migrate runs where Flyway crashed before emitting JSON.
func addResultFields(rec map[string]any, r *migration.Result) {
	if len(r.AppliedVersions) > 0 {
		rec["applied_versions"] = r.AppliedVersions
	}
	if r.StartingVersion != "" {
		rec["starting_version"] = r.StartingVersion
	}
	if r.EndingVersion != "" {
		rec["ending_version"] = r.EndingVersion
	}
	if r.DurationMillis > 0 {
		rec["duration_millis"] = r.DurationMillis
	}
	if r.FlywayVersion != "" {
		rec["flyway_version"] = r.FlywayVersion
	}
	if r.ErrorCode != "" {
		rec["error_code"] = r.ErrorCode
	}
}

// formatSchemaSummary renders the human-readable schema-terminus summary
// appended to each tenant line ("v0 → v2 (2 applied)" or "v2 (no-op)").
// Empty when the Result is zero-valued (validate / info actions).
func formatSchemaSummary(r *migration.Result) string {
	if r.EndingVersion == "" && len(r.AppliedVersions) == 0 {
		return ""
	}
	// Fall back to the last applied version when the parser couldn't read
	// Flyway's targetSchemaVersion. Keeps the line useful instead of showing
	// "v→v (N applied)" on engine output we didn't fully recognize.
	end := r.EndingVersion
	if end == "" && len(r.AppliedVersions) > 0 {
		end = r.AppliedVersions[len(r.AppliedVersions)-1]
	}
	from := r.StartingVersion
	if from == "" {
		from = "∅"
	}
	if len(r.AppliedVersions) == 0 {
		return fmt.Sprintf("schema=v%s (no-op)", end)
	}
	return fmt.Sprintf("schema=v%s→v%s (%d applied)", from, end, len(r.AppliedVersions))
}

func vendorOrUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// writeSummary emits the one summary record every run produces, including the
// runs that ended before the first dispatch — a pipeline reads exactly one per
// invocation. The verdict describes the fleet the run leaves behind, so it is
// derived from the result rather than from the error the process exits on: a
// run that errored with every tenant dispatched and green leaves a clean fleet
// and says so, while the exit code still reports the failure.
func writeSummary(out io.Writer, result *migration.MigrateAllResult, asJSON bool) {
	failed := result.Failed()
	verdict := verdictName(result.Verdict())

	if asJSON {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"event":  "summary",
			"action": result.Action.String(),
			// total keeps the meaning it has always had — the dispatched count,
			// which attempted now names too. Kept so a pipeline reading it
			// still works; prefer attempted or listed in new code.
			"total":         len(result.Results),
			"verdict":       verdict,
			"listed":        result.Listed(),
			"attempted":     len(result.Results),
			"failed":        len(failed),
			"not_attempted": len(result.NeverDispatched),
		})
		return
	}

	fmt.Fprintf(out, "\n%s summary: verdict=%s, %d listed, %d attempted, %d failed, %d not attempted\n",
		titleASCII(result.Action.String()), verdict, result.Listed(), len(result.Results),
		len(failed), len(result.NeverDispatched))
	for i := range failed {
		fmt.Fprintf(out, "  - %s: %v\n", failed[i].TenantID, failed[i].Err)
	}
}

// titleASCII upper-cases the first byte of an ASCII action verb (migrate ->
// Migrate). Action verbs are guaranteed ASCII so we avoid pulling in
// golang.org/x/text just to replace the deprecated strings.Title.
func titleASCII(s string) string {
	if s == "" {
		return s
	}
	first := s[0]
	if first >= 'a' && first <= 'z' {
		return string(first-'a'+'A') + s[1:]
	}
	return s
}
