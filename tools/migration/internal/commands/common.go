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

	jsonKeyTenants = "tenants"
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
func buildConfigProvider(ctx context.Context, flags *CommonFlags, fileStore *config.TenantStore) (database.DBConfigProvider, error) {
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
			numericToDurationGuardHookFunc(),
			stringToTrimmedSliceHookFunc(","),
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.TextUnmarshallerHookFunc(),
		),
		WeaklyTypedInput: true,
	}
}

// numericToDurationGuardHookFunc mirrors
// github.com/gaborage/go-bricks/internal/configdecode.NumericToDurationGuardHookFunc — this is
// a separate module that cannot import go-bricks/internal, so this is a byte-identical local
// copy. Keep the two in sync (bool rejection, typed-Duration pass-through, grouped zero test,
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

// stringToTrimmedSliceHookFunc splits a scalar string into []string on sep, trimming each
// element and dropping empties. Scoped to string -> []string only, so []byte, other slices,
// and YAML sequences are untouched. Local copy of config.stringToTrimmedSliceHookFunc /
// splitAndTrimList (github.com/gaborage/go-bricks config/) — keep in sync.
func stringToTrimmedSliceHookFunc(sep string) mapstructure.DecodeHookFunc {
	return func(f, t reflect.Type, data any) (any, error) {
		if f.Kind() != reflect.String || t != reflect.TypeOf([]string(nil)) {
			return data, nil
		}
		raw := reflect.ValueOf(data).String()
		if strings.TrimSpace(raw) == "" {
			return []string{}, nil
		}
		parts := strings.Split(raw, sep)
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out, nil
	}
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
// recommended timeout win; a non-zero --timeout overrides it via mergeConfigs.
func buildBaseConfig(flags *CommonFlags) *migration.Config {
	return &migration.Config{
		FlywayPath:    flags.FlywayPath,
		ConfigPath:    flags.FlywayConfig,
		MigrationPath: flags.MigrationsDir,
		Timeout:       flags.Timeout, // 0 -> vendor default wins in mergeConfigs
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
func runAction(cmd *cobra.Command, flags *CommonFlags, action migration.Action) error {
	if err := resolveFlags(cmd, flags); err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	fileStore, err := maybeLoadFileStore(flags)
	if err != nil {
		return err
	}

	lister, err := buildLister(flags, fileStore)
	if err != nil {
		return fmt.Errorf("build tenant lister: %w", err)
	}

	provider, err := buildConfigProvider(ctx, flags, fileStore)
	if err != nil {
		return fmt.Errorf("build config provider: %w", err)
	}

	log := newCLILogger(flags)

	// Construct a minimal *config.Config to satisfy FlywayMigrator's needs.
	// Per-tenant DatabaseConfig is supplied via MigrateAll.
	migCfg := &config.Config{App: config.AppConfig{Env: "production"}}
	migrator := migration.NewFlywayMigrator(migCfg, log)

	out := cmd.OutOrStdout()
	hook := makeHook(out, flags.JSON)

	result, err := migration.MigrateAll(ctx, migrator, lister, provider, action, migration.MigrateAllOptions{
		BaseConfig:      buildBaseConfig(flags),
		ContinueOnError: flags.ContinueOnError,
		Parallelism:     flags.Parallel,
		Logger:          log,
		Hook:            hook,
	})
	if err != nil && result == nil {
		return err
	}

	writeSummary(out, result, flags.JSON)

	if result != nil && len(result.Failed()) > 0 {
		return errAtLeastOneFailed
	}
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

func writeSummary(out io.Writer, result *migration.MigrateAllResult, asJSON bool) {
	if result == nil {
		return
	}
	failed := result.Failed()
	total := len(result.Results)

	if asJSON {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"event":  "summary",
			"action": result.Action.String(),
			"total":  total,
			"failed": len(failed),
		})
		return
	}

	fmt.Fprintf(out, "\n%s summary: %d tenants total, %d failed\n", titleASCII(result.Action.String()), total, len(failed))
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
