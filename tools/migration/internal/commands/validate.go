package commands

import (
	"github.com/spf13/cobra"

	"github.com/gaborage/go-bricks/migration"
)

// NewValidateCommand returns the `validate` subcommand.
func NewValidateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate migrations for every tenant without applying them",
		Args:  cobra.NoArgs,
	}
	flags := addCommonFlags(cmd)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		return runAction(c, flags, migration.ActionValidate)
	}
	return cmd
}
