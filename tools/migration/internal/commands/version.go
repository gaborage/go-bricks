package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewVersionCommand returns the `version` subcommand.
func NewVersionCommand(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "go-bricks-migrate version %s\n", version)
		},
	}
}
