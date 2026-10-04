package commands

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
)

const flagSharedMigrator = "shared-migrator"

// resolveSharedMigrator applies the GOBRICKS_MIGRATE_SHARED_MIGRATOR fallback. An
// explicit --shared-migrator always wins and an empty value is unset, as in
// applyEnvFallback. It belongs on the runAction path only, so quiesce and list
// never read it.
func resolveSharedMigrator(cmd *cobra.Command, flags *CommonFlags) error {
	if cmd.Flags().Changed(flagSharedMigrator) {
		return nil
	}
	v := os.Getenv(envSharedMigrator)
	if v == "" {
		return nil
	}
	armed, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("%s=%q is not a boolean; use true or false", envSharedMigrator, v)
	}
	flags.SharedMigrator = armed
	return nil
}

// sharedMigratorProvider refuses a tenant whose database type is not one
// go-bricks supports. The library's schema guard keys on the exact type
// "postgresql", so an empty or misspelled type would otherwise skip it and let
// Flyway fall back to whatever flyway.conf targets.
//
// It wraps the TLS-validating provider, so it judges the normalized copy: an
// empty type a recognized connectionstring infers is already filled in. The
// refusal names the tenant and the type only, never a connection field.
type sharedMigratorProvider struct {
	inner database.DBConfigProvider
}

func (p *sharedMigratorProvider) DBConfig(ctx context.Context, key string) (*config.DatabaseConfig, error) {
	cfg, err := p.inner.DBConfig(ctx, key)
	if err != nil || cfg == nil {
		return cfg, err
	}
	if err := database.ValidateDatabaseType(cfg.Type); err != nil {
		return nil, fmt.Errorf("tenant %q: --shared-migrator cannot aim Flyway at it: %w", key, err)
	}
	return cfg, nil
}
