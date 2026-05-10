package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// NewListCommand returns the `list` subcommand which prints tenant IDs and exits.
// Useful for debugging the listing source without touching credentials or DBs.
func NewListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the tenant IDs returned by the configured source",
		Args:  cobra.NoArgs,
	}
	flags := addCommonFlags(cmd)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		if err := requireExactlyOneSource(flags); err != nil {
			return err
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

		return writeTenantIDs(c.OutOrStdout(), ids, flags.JSON)
	}
	return cmd
}

// requireExactlyOneSource enforces the listing-only invariant for the list
// command, which deliberately bypasses resolveFlags' credential checks.
// Whitespace-only flag values are treated as unset so accidental whitespace
// from copy-paste or env-var rendering doesn't satisfy the check.
func requireExactlyOneSource(flags *CommonFlags) error {
	selectors := 0
	if strings.TrimSpace(flags.SourceURL) != "" {
		selectors++
	}
	if strings.TrimSpace(flags.SourceConfig) != "" {
		selectors++
	}
	if strings.TrimSpace(flags.Tenant) != "" {
		selectors++
	}
	if selectors != 1 {
		return errors.New("exactly one of --source-url, --source-config, or --tenant is required")
	}
	return nil
}

// writeTenantIDs emits ids as NDJSON when asJSON is set or one-per-line text
// otherwise, propagating writer errors so a broken pipe surfaces as a non-zero
// exit instead of being silently dropped.
func writeTenantIDs(out io.Writer, ids []string, asJSON bool) error {
	if asJSON {
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
