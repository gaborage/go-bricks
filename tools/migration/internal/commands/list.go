package commands

import (
	"context"
	"encoding/json"
	"errors"
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
		// list never needs credentials — bypass full resolveFlags credential
		// validation. Require exactly one source selector.
		selectors := 0
		if flags.SourceURL != "" {
			selectors++
		}
		if flags.SourceConfig != "" {
			selectors++
		}
		if flags.Tenant != "" {
			selectors++
		}
		if selectors != 1 {
			return errors.New("exactly one of --source-url, --source-config, or --tenant is required")
		}

		lister, err := buildLister(flags, nil)
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
			if err := json.NewEncoder(out).Encode(map[string]any{jsonKeyTenants: ids}); err != nil {
				return fmt.Errorf("encode tenants JSON: %w", err)
			}
			return nil
		}
		for _, id := range ids {
			if _, err := fmt.Fprintln(out, id); err != nil {
				return fmt.Errorf("write tenant id: %w", err)
			}
		}
		return nil
	}
	return cmd
}
