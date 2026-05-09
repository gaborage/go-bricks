package commands

import (
	"github.com/spf13/cobra"

	"github.com/gaborage/go-bricks/migration"
)

// NewMigrateCommand returns the `migrate` subcommand.
func NewMigrateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending migrations to every tenant",
		Long:  "Lists tenants from the configured source, resolves each tenant's database credentials, and runs Flyway migrate against each.",
		Args:  cobra.NoArgs,
	}
	flags := addCommonFlags(cmd)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		return runAction(c, flags, migration.ActionMigrate)
	}
	return cmd
}
