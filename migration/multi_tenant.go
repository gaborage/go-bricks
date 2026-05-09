package migration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
)

// Action selects which Flyway operation MigrateAll runs against each tenant.
type Action int

const (
	// ActionMigrate applies pending migrations.
	ActionMigrate Action = iota
	// ActionValidate verifies migrations without applying them.
	ActionValidate
	// ActionInfo prints the migration status table.
	ActionInfo
)

// String returns the human-readable form of the action.
func (a Action) String() string {
	switch a {
	case ActionMigrate:
		return "migrate"
	case ActionValidate:
		return "validate"
	case ActionInfo:
		return "info"
	default:
		return fmt.Sprintf("unknown(%d)", a)
	}
}

// TenantLister enumerates the tenant IDs that should receive migrations.
// Implementations include the HTTP source (for control-plane APIs) and a
// static source backed by config.TenantStore.
type TenantLister interface {
	ListTenants(ctx context.Context) ([]string, error)
}

// TenantResult captures the outcome of running an Action against one tenant.
type TenantResult struct {
	TenantID string
	Vendor   string
	Err      error
	Duration time.Duration
}

// MigrateAllResult aggregates per-tenant results from a MigrateAll run.
type MigrateAllResult struct {
	Action  Action
	Results []TenantResult
}

// Failed returns only the tenant results whose Err is non-nil.
func (r *MigrateAllResult) Failed() []TenantResult {
	if r == nil {
		return nil
	}
	var out []TenantResult
	for _, res := range r.Results {
		if res.Err != nil {
			out = append(out, res)
		}
	}
	return out
}

// MigrateAllOptions tunes per-tenant execution.
type MigrateAllOptions struct {
	// BaseConfig supplies Flyway timeout / paths. ConfigPath and
	// MigrationPath are auto-resolved per vendor when zero.
	BaseConfig *Config

	// ContinueOnError keeps iterating after the first per-tenant failure.
	// Default false (fail-fast).
	ContinueOnError bool

	// Parallelism caps concurrent tenant migrations. 0 or 1 = sequential.
	// Implementation caps the value to a reasonable maximum to avoid
	// connection storms.
	Parallelism int

	// Logger receives progress updates. May be nil.
	Logger logger.Logger

	// Hook is invoked after each tenant completes (success or failure).
	// Useful for streaming progress to the CLI / CI logs. May be nil.
	Hook func(TenantResult)
}

// ErrNoLister is returned when MigrateAll is called without a TenantLister.
var ErrNoLister = errors.New("migration: TenantLister is nil")

// ErrNoConfigProvider is returned when MigrateAll is called without a DBConfigProvider.
var ErrNoConfigProvider = errors.New("migration: database.DBConfigProvider is nil")

// maxParallelism caps Parallelism to a sensible upper bound.
const maxParallelism = 32

// MigrateAll lists tenants via lister, resolves each tenant's database config
// via configs (the existing database.DBConfigProvider abstraction), and runs
// the chosen Flyway action against every one. Sequential fail-fast unless
// opts say otherwise.
func MigrateAll(
	ctx context.Context,
	migrator *FlywayMigrator,
	lister TenantLister,
	configs database.DBConfigProvider,
	action Action,
	opts MigrateAllOptions,
) (*MigrateAllResult, error) {
	if migrator == nil {
		return nil, errors.New("migration: FlywayMigrator is nil")
	}
	if lister == nil {
		return nil, ErrNoLister
	}
	if configs == nil {
		return nil, ErrNoConfigProvider
	}

	tenantIDs, err := lister.ListTenants(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}

	logProgress := func(event func() logger.LogEvent, msg string) {
		if opts.Logger == nil {
			return
		}
		event().Msg(msg)
	}

	logProgress(func() logger.LogEvent {
		return opts.Logger.Info().
			Int("tenants", len(tenantIDs)).
			Str("action", action.String())
	}, "Starting multi-tenant migration")

	if opts.Parallelism > 1 && !opts.ContinueOnError {
		// Parallel + fail-fast can't cancel cleanly without losing in-flight
		// results. Force continue-on-error semantics to keep behavior sane.
		opts.ContinueOnError = true
	}

	if opts.Parallelism <= 1 {
		return runSequential(ctx, migrator, configs, action, tenantIDs, opts)
	}
	return runParallel(ctx, migrator, configs, action, tenantIDs, opts)
}

func runSequential(
	ctx context.Context,
	migrator *FlywayMigrator,
	configs database.DBConfigProvider,
	action Action,
	tenantIDs []string,
	opts MigrateAllOptions,
) (*MigrateAllResult, error) {
	out := &MigrateAllResult{Action: action, Results: make([]TenantResult, 0, len(tenantIDs))}
	for _, id := range tenantIDs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res := runOne(ctx, migrator, configs, action, id, opts.BaseConfig)
		out.Results = append(out.Results, res)

		if opts.Hook != nil {
			opts.Hook(res)
		}

		if res.Err != nil && !opts.ContinueOnError {
			return out, res.Err
		}
	}
	return out, nil
}

func runParallel(
	ctx context.Context,
	migrator *FlywayMigrator,
	configs database.DBConfigProvider,
	action Action,
	tenantIDs []string,
	opts MigrateAllOptions,
) (*MigrateAllResult, error) {
	parallelism := opts.Parallelism
	if parallelism > maxParallelism {
		if opts.Logger != nil {
			opts.Logger.Warn().
				Int("requested", parallelism).
				Int("cap", maxParallelism).
				Msg("Parallelism capped to maximum")
		}
		parallelism = maxParallelism
	}

	out := &MigrateAllResult{Action: action, Results: make([]TenantResult, len(tenantIDs))}
	var hookMu sync.Mutex
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup

	for i, id := range tenantIDs {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(idx int, tenantID string) {
			defer wg.Done()
			defer func() { <-sem }()

			res := runOne(ctx, migrator, configs, action, tenantID, opts.BaseConfig)
			out.Results[idx] = res

			if opts.Hook != nil {
				hookMu.Lock()
				opts.Hook(res)
				hookMu.Unlock()
			}
		}(i, id)
	}

	wg.Wait()
	return out, nil
}

func runOne(
	ctx context.Context,
	migrator *FlywayMigrator,
	configs database.DBConfigProvider,
	action Action,
	tenantID string,
	baseCfg *Config,
) TenantResult {
	start := time.Now()
	res := TenantResult{TenantID: tenantID}

	dbCfg, err := configs.DBConfig(ctx, tenantID)
	if err != nil {
		res.Err = fmt.Errorf("resolve db config: %w", err)
		res.Duration = time.Since(start)
		return res
	}
	res.Vendor = dbCfg.Type

	cfg := baseCfg
	if cfg == nil || cfg.ConfigPath == "" || cfg.MigrationPath == "" {
		defaults := migrator.DefaultMigrationConfigForVendor(dbCfg.Type)
		cfg = mergeConfigs(defaults, baseCfg)
	}

	switch action {
	case ActionMigrate:
		res.Err = migrator.MigrateFor(ctx, dbCfg, cfg)
	case ActionValidate:
		res.Err = migrator.ValidateFor(ctx, dbCfg, cfg)
	case ActionInfo:
		res.Err = migrator.InfoFor(ctx, dbCfg, cfg)
	default:
		res.Err = fmt.Errorf("migration: unsupported action %v", action)
	}
	res.Duration = time.Since(start)
	return res
}

// mergeConfigs returns a *Config that prefers user-supplied fields over
// vendor defaults. When override has nothing to contribute, the defaults
// pointer is returned untouched so the per-tenant loop avoids an allocation.
func mergeConfigs(defaults, override *Config) *Config {
	if defaults == nil {
		return override
	}
	if override == nil || isEmptyConfig(override) {
		return defaults
	}
	out := *defaults
	if override.FlywayPath != "" {
		out.FlywayPath = override.FlywayPath
	}
	if override.ConfigPath != "" {
		out.ConfigPath = override.ConfigPath
	}
	if override.MigrationPath != "" {
		out.MigrationPath = override.MigrationPath
	}
	if override.Timeout > 0 {
		out.Timeout = override.Timeout
	}
	if override.Environment != "" {
		out.Environment = override.Environment
	}
	if override.DryRun {
		out.DryRun = override.DryRun
	}
	return &out
}

// isEmptyConfig reports whether c carries no override fields.
func isEmptyConfig(c *Config) bool {
	return c.FlywayPath == "" && c.ConfigPath == "" && c.MigrationPath == "" &&
		c.Timeout == 0 && c.Environment == "" && !c.DryRun
}
