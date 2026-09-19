package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
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

	// migratorOverlayLogMsg records that Flyway will connect as the migrator. The
	// username is logged beside it; the password never is.
	migratorOverlayLogMsg = "Migrator identity overlay active"

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
func loadTenantStoreFromFile(path string) (*config.TenantStore, error) {
	if err := validateConfigPath(path); err != nil {
		return nil, err
	}

	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("load config %q: %w", path, err)
	}

	// Empty Tag keeps koanf's "koanf" TagName: binds by koanf tag / case-insensitive
	// field name, ignoring mapstructure tags, so it only reaches config.Config keys that
	// are flat-smushed (underscore-free). That invariant is enforced by
	// config.TestConfigKoanfTagsHaveNoUnderscore; if it ever regressed, an underscored key
	// would silently fail to bind here (see issue #554). The guard decoder rejects bare
	// numeric time.Duration values here too.
	var cfg config.Config
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{DecoderConfig: tenantDecoderConfig()}); err != nil {
		return nil, fmt.Errorf("unmarshal config %q: %w", path, err)
	}
	return config.NewTenantStore(&cfg), nil
}

// durationType is time.Duration's reflect.Type, computed once for the guard hook's fast path.
var durationType = reflect.TypeOf(time.Duration(0))

// tenantDecoderConfig mirrors config.buildDecoderConfig (github.com/gaborage/go-bricks
// config/config.go) so a tenants.yaml decodes byte-identically to the framework's Load:
// numeric-duration guard + comma-split []string hook + StringToTimeDuration + text-unmarshaler,
// WeaklyTypedInput. Keep the hook set in sync with buildDecoderConfig — without the slice hook
// a comma-scalar []string field (e.g. an allowlist) would decode to one element here but two
// under the framework.
func tenantDecoderConfig() *mapstructure.DecoderConfig {
	return &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			emptyStringToScalarGuardHookFunc(),
			numericToDurationGuardHookFunc(),
			stringToTrimmedSliceHookFunc(","),
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.TextUnmarshallerHookFunc(),
		),
		WeaklyTypedInput: true,
	}
}

// numericToDurationGuardHookFunc mirrors
// github.com/gaborage/go-bricks/internal/configdecode.NumericToDurationGuardHookFunc as a
// byte-identical local copy. The import would in fact compile — Go's internal rule is
// import-path-prefix based and this module sits under github.com/gaborage/go-bricks/ — but the
// copy is kept deliberately so the CLI does not bind to a framework package that carries no
// compatibility guarantee across releases. Keep the two in sync (bool rejection, typed-Duration pass-through, grouped zero test,
// message). Reject a bare non-zero numeric bound to time.Duration (WeaklyTypedInput would coerce
// 300 -> 300ns); an explicit zero (incl. -0.0) is the "unset -> use default" idiom and stays
// exempt; a bool is never a duration; a source already time.Duration passes untouched.
// Guards exact time.Duration only, matching StringToTimeDurationHookFunc's scope.
func numericToDurationGuardHookFunc() mapstructure.DecodeHookFunc {
	return func(f, t reflect.Type, data any) (any, error) {
		if t != durationType {
			return data, nil
		}
		// A source already time.Duration (typed default) is not unit-less; its Kind is Int64
		// and would otherwise be rejected, so pass it through before the numeric checks.
		if f == durationType {
			return data, nil
		}
		v := reflect.ValueOf(data)
		var isZero bool
		switch f.Kind() {
		case reflect.Bool:
			// A boolean is never a duration: reject with no zero exemption.
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			isZero = v.Int() == 0
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			isZero = v.Uint() == 0
		case reflect.Float32, reflect.Float64:
			isZero = v.Float() == 0 // == 0 (not IsZero): keeps -0.0 exempt
		default:
			return data, nil
		}
		if isZero {
			return data, nil
		}
		return nil, fmt.Errorf(
			"unit-less numeric duration %v — use a duration string with an explicit unit (e.g. \"300s\", \"5m\", \"1h30m\")",
			data,
		)
	}
}

// emptyStringToScalarGuardHookFunc mirrors
// github.com/gaborage/go-bricks/internal/configdecode.EmptyStringToScalarGuardHookFunc as a
// byte-identical local copy, for the reason given on numericToDurationGuardHookFunc above.
// Keep the two in sync (time.Duration exemption, whitespace trim, both messages).
// Reject an empty or whitespace-only string bound to a numeric or bool field: WeaklyTypedInput
// would coerce it to that target's zero value, so a set-but-empty variable decodes as a legal
// 0 / false and boots a config nobody wrote. time.Duration is exempt
// (StringToTimeDurationHookFunc already fails loudly on it) and every other target is untouched.
func emptyStringToScalarGuardHookFunc() mapstructure.DecodeHookFunc {
	return func(f, t reflect.Type, data any) (any, error) {
		if f.Kind() != reflect.String {
			return data, nil
		}
		// No pointer walk: mapstructure recurses into a pointer target and re-runs the
		// hook chain against the element type, so *int arrives here as int.
		if t == durationType || !isWeakScalarKind(t.Kind()) {
			return data, nil
		}
		// reflect rather than a concrete type assertion: a named string type (type Env
		// string) has Kind String but fails data.(string), and passing it through hands it
		// straight to the weak "" -> zero conversion this guard exists to stop.
		if strings.TrimSpace(reflect.ValueOf(data).String()) != "" {
			return data, nil
		}
		// The message stays inline rather than in a package var: the mirror-drift test
		// compares function bodies, so a message hoisted out would drift unchecked. Only
		// the remedy differs by kind — the part an operator acts on.
		kind, remedy := "numeric", "an explicit value"
		if t.Kind() == reflect.Bool {
			kind, remedy = "boolean", "an explicit true/false"
		}
		return nil, fmt.Errorf(
			"%s value delivered empty — set %s (empty secretKeyRef / unset envsubst variable?) "+
				"or remove the key entirely to take its default", kind, remedy,
		)
	}
}

// isWeakScalarKind reports whether k is one mapstructure's WeaklyTypedInput would fill from
// a string, i.e. the SCALAR kinds where "" silently becomes that kind's zero value — 0 for
// the numerics, false for a bool. Slice and map elements re-enter the hook individually, so
// they are judged by this same predicate on their element kind.
func isWeakScalarKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

// stringToTrimmedSliceHookFunc splits a scalar string into []string on sep, trimming each
// element and dropping empties. Scoped to string -> []string only, so []byte, other slices,
// and YAML sequences are untouched. Local copy of config.stringToTrimmedSliceHookFunc
// (github.com/gaborage/go-bricks config/) — keep in sync.
//
// The split itself lives in splitAndTrimList below, mirroring the framework's own division,
// so TestMirroredHooksHaveNotDrifted can compare each half against its counterpart. Inlining
// it here is what let this pair drift unchecked (ADR-078's rider).
func stringToTrimmedSliceHookFunc(sep string) mapstructure.DecodeHookFunc {
	return func(f reflect.Type, t reflect.Type, data any) (any, error) {
		if f.Kind() != reflect.String || t != reflect.TypeOf([]string(nil)) {
			return data, nil
		}
		// reflect.Value.String() (not data.(string)) so named string types don't panic.
		return splitAndTrimList(reflect.ValueOf(data).String(), sep), nil
	}
}

// splitAndTrimList mirrors github.com/gaborage/go-bricks/config.splitAndTrimList — keep in
// sync. An all-whitespace input yields an empty slice, not a one-element slice of "".
func splitAndTrimList(raw, sep string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	parts := strings.Split(raw, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
// Every invocation emits exactly one summary record, including the runs that
// end before the first dispatch, so a pipeline parsing the stream always has a
// terminal record to read.
func runAction(cmd *cobra.Command, flags *CommonFlags, action migration.Action) error {
	out := cmd.OutOrStdout()
	if err := resolveFlags(cmd, flags); err != nil {
		return failedBeforeDispatch(out, action, flags.JSON, err)
	}
	if err := resolveMigratorIdentity(flags); err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	lister, provider, err := buildRunDeps(ctx, flags)
	if err != nil {
		return failedBeforeDispatch(out, action, flags.JSON, err)
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

	if len(result.Failed()) > 0 {
		return errAtLeastOneFailed
	}
	return err
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

// failedBeforeDispatch reports a failure that happened before the first tenant
// was dispatched. The run still emits its summary record, because a pipeline
// reads one per invocation and "nothing ran" is exactly what it needs to see.
func failedBeforeDispatch(out io.Writer, action migration.Action, asJSON bool, err error) error {
	writeSummary(out, &migration.MigrateAllResult{Action: action}, asJSON)
	return err
}

// errAtLeastOneFailed signals a non-zero exit without printing a duplicate message.
var errAtLeastOneFailed = errors.New("one or more tenants failed")

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
