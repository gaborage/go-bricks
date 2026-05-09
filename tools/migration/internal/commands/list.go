package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// NewListCommand returns the `list` subcommand which prints tenant IDs and exits.
// Useful for debugging the listing source without touching credentials or DBs.
func NewListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the tenant IDs returned by the configured source",
	}
	flags := addCommonFlags(cmd)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		// list never needs credentials — relax the cred-source flag check
		// by short-circuiting validation here.
		if flags.SourceURL == "" && flags.SourceConfig == "" && flags.Tenant == "" {
			return fmt.Errorf("one of --source-url, --source-config, or --tenant is required")
		}
		if flags.SourceURL != "" && flags.SourceConfig != "" {
			return fmt.Errorf("--source-url and --source-config are mutually exclusive")
		}

		lister, err := buildLister(flags)
		if err != nil {
			return err
		}

		ctx := c.Context()
		if ctx == nil {
			ctx = context.Background()
		}

		ids, err := lister.ListTenants(ctx)
		if err != nil {
			return err
		}

		out := c.OutOrStdout()
		if flags.JSON {
			_ = json.NewEncoder(out).Encode(map[string]any{"tenants": ids})
			return nil
		}
		for _, id := range ids {
			fmt.Fprintln(out, id)
		}
		return nil
	}
	return cmd
}
