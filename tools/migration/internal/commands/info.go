package commands

import (
	"github.com/spf13/cobra"

	"github.com/gaborage/go-bricks/migration"
)

// NewInfoCommand returns the `info` subcommand.
func NewInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Print migration status for every tenant",
		Args:  cobra.NoArgs,
	}
	flags := addCommonFlags(cmd)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		return runAction(c, flags, migration.ActionInfo)
	}
	return cmd
}
