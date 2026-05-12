package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

	jsonKeyTenants = "tenants"
)

// resolveFlags applies env-var fallbacks and returns a validated copy of flags.
// cmd is consulted via Flags().Changed so an explicit --secrets-prefix on the
// command line wins over the env override even when both equal the default.
func resolveFlags(cmd *cobra.Command, flags *CommonFlags) error {
	if flags.SourceToken == "" {
		flags.SourceToken = os.Getenv(envSourceToken)
	}
	if v := os.Getenv(envSecretsPrefix); v != "" && !cmd.Flags().Changed("secrets-prefix") {
		flags.SecretsPrefix = v
	}

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
			BearerToken: flags.SourceToken,
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

	var cfg config.Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config %q: %w", path, err)
	}
	return config.NewTenantStore(&cfg), nil
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
// user-supplied fields filled. Vendor-specific defaults (including Timeout)
// are applied per tenant inside MigrateAll, so leaving Timeout zero here lets
// each vendor's recommended timeout win unless the user overrides it.
func buildBaseConfig(flags *CommonFlags) *migration.Config {
	return &migration.Config{
		FlywayPath:    flags.FlywayPath,
		ConfigPath:    flags.FlywayConfig,
		MigrationPath: flags.MigrationsDir,
	}
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

	log := logger.New("info", false)
	if flags.Verbose {
		log = logger.New("debug", false)
	}

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
		}
		fmt.Fprintf(out, "  %s (%s) ... %s (%s)%s\n", r.TenantID, vendorOrUnknown(r.Vendor), status, r.Duration.Round(10*time.Millisecond), extra)
	}
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
	for _, f := range failed {
		fmt.Fprintf(out, "  - %s: %v\n", f.TenantID, f.Err)
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
