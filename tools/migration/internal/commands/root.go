// Package commands implements the cobra subcommands for go-bricks-migrate.
package commands

import (
	"github.com/spf13/cobra"

	"github.com/gaborage/go-bricks/migration"
)

// CommonFlags holds flags shared by every action subcommand.
type CommonFlags struct {
	// Listing source.
	SourceURL    string
	SourceToken  string
	SourceConfig string

	// Credential resolution.
	SecretsPrefix   string
	AWSRegion       string
	AWSProfile      string
	AWSEndpoint     string
	CredentialsFrom string

	// Flyway.
	FlywayConfig  string
	MigrationsDir string
	FlywayPath    string

	// Behavior.
	ContinueOnError bool
	Parallel        int
	Tenant          string
	JSON            bool
	Verbose         bool
}

// NewRootCommand constructs the cobra root with shared flags wired up.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "go-bricks-migrate",
		Short: "Run go-bricks Flyway migrations across multiple tenants",
		Long: `Run Flyway migrations against every tenant returned by a control-plane API.

The CLI lists tenants from a go-bricks control-plane endpoint (or a local YAML
file in dev mode), resolves each tenant's database credentials from AWS Secrets
Manager, and applies the Flyway action (migrate/validate/info) sequentially by
default.

Example:
  go-bricks-migrate migrate \
    --source-url https://control-plane.example.com/api \
    --secrets-prefix gobricks/migrate/ \
    --aws-region us-east-1`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	return root
}

// addCommonFlags attaches the shared flag set to a subcommand and returns the
// CommonFlags struct that will be populated when the command runs.
func addCommonFlags(cmd *cobra.Command) *CommonFlags {
	flags := &CommonFlags{}

	// Listing source.
	cmd.Flags().StringVar(&flags.SourceURL, "source-url", "", "Base URL of the control-plane tenant-listing API")
	cmd.Flags().StringVar(&flags.SourceToken, "source-token", "", "Bearer token for the control-plane API (env: GOBRICKS_MIGRATE_SOURCE_TOKEN)")
	cmd.Flags().StringVar(&flags.SourceConfig, "source-config", "", "Path to a YAML config file with multitenant.tenants block (alternative to --source-url)")

	// Credential resolution.
	cmd.Flags().StringVar(&flags.SecretsPrefix, "secrets-prefix", migration.DefaultSecretsPrefix, "Prefix for AWS Secrets Manager secret names (final name = prefix + tenant_id)")
	cmd.Flags().StringVar(&flags.AWSRegion, "aws-region", "", "AWS region (falls back to AWS_REGION / SDK default chain)")
	cmd.Flags().StringVar(&flags.AWSProfile, "aws-profile", "", "AWS profile (falls back to AWS_PROFILE)")
	cmd.Flags().StringVar(&flags.AWSEndpoint, "aws-endpoint", "", "AWS Secrets Manager endpoint override (LocalStack, private VPC endpoint)")
	cmd.Flags().StringVar(&flags.CredentialsFrom, "credentials-from", "aws-secrets-manager", "Credential source: aws-secrets-manager | config-file")

	// Flyway.
	cmd.Flags().StringVar(&flags.FlywayConfig, "flyway-config", "", "Path to flyway.conf (per-vendor default when empty)")
	cmd.Flags().StringVar(&flags.MigrationsDir, "migrations-dir", "", "Path to migrations directory (per-vendor default when empty)")
	cmd.Flags().StringVar(&flags.FlywayPath, "flyway-path", "flyway", "Path to flyway executable")

	// Behavior.
	cmd.Flags().BoolVar(&flags.ContinueOnError, "continue-on-error", false, "Continue iterating tenants after a per-tenant failure")
	cmd.Flags().IntVar(&flags.Parallel, "parallel", 1, "Concurrent tenants to process (1 = sequential)")
	cmd.Flags().StringVar(&flags.Tenant, "tenant", "", "Run for a single tenant ID instead of listing all")
	cmd.Flags().BoolVar(&flags.JSON, "json", false, "Emit NDJSON progress records (for CI/CD parsing)")
	cmd.Flags().BoolVarP(&flags.Verbose, "verbose", "v", false, "Verbose logging")

	return flags
}
