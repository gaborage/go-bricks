package migration

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/config"
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
		return flywayCmdMigrate
	case ActionValidate:
		return flywayCmdValidate
	case ActionInfo:
		return flywayCmdInfo
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

	// Result is the parsed Flyway outcome for ActionMigrate. Zero-valued
	// for Validate / Info, or when Flyway crashed before emitting JSON.
	Result Result
}

// MigrateAllResult aggregates per-tenant results from a MigrateAll run.
type MigrateAllResult struct {
	Action Action

	// Results holds one row per dispatched tenant. A listed tenant that was
	// never dispatched has no row; it appears in NeverDispatched instead.
	Results []TenantResult

	// NeverDispatched holds the listed tenant IDs the run stopped before
	// dispatching (context done, quiesce, fail-fast), in listing order.
	NeverDispatched []string
}

// Listed returns how many tenant IDs the TenantLister returned: the dispatched
// rows plus the never-dispatched IDs.
func (r *MigrateAllResult) Listed() int {
	if r == nil {
		return 0
	}
	return len(r.Results) + len(r.NeverDispatched)
}

// ErrFleetSplit is the Verdict of a run that dispatched at least one tenant
// but left at least one listed tenant failed or never dispatched: the fleet
// may be at mixed versions and needs a re-run.
var ErrFleetSplit = errors.New("migration: fleet split")

// ErrNothingAttempted is the Verdict of a run that dispatched no tenant (empty
// listing, listing failure, context done or quiesce set before the first
// dispatch): no schema was touched.
var ErrNothingAttempted = errors.New("migration: no tenant attempted")

// Verdict classifies the run as a whole: nil when at least one tenant was
// listed and every listed tenant was dispatched and succeeded, ErrFleetSplit,
// or ErrNothingAttempted. A nil result is ErrNothingAttempted. It is
// independent of MigrateAll's returned error.
func (r *MigrateAllResult) Verdict() error {
	if r == nil || len(r.Results) == 0 {
		return ErrNothingAttempted
	}
	if len(r.NeverDispatched) > 0 {
		return ErrFleetSplit
	}
	for i := range r.Results {
		if r.Results[i].Err != nil {
			return ErrFleetSplit
		}
	}
	return nil
}

// Failed returns the dispatched tenant results whose Err is non-nil. Tenants
// that were never dispatched are not included; see NeverDispatched and Verdict.
func (r *MigrateAllResult) Failed() []TenantResult {
	if r == nil {
		return nil
	}
	var out []TenantResult
	// Index iteration: TenantResult is too large for gocritic's
	// rangeValCopy threshold (each step would copy the embedded Result).
	for i := range r.Results {
		if r.Results[i].Err != nil {
			out = append(out, r.Results[i])
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

	// Quiesce, when set, gates tenant dispatch on the deployment quiesce flag:
	// once the flag is observed set, no further tenants are dispatched (in-flight
	// tenants drain) and MigrateAll returns ErrQuiesceBlocked with the partial
	// result. Nil disables the check (fully opt-in). Check errors fail open.
	Quiesce QuiesceGate

	// MigratorIdentity, when set, replaces the username and password on a copy of
	// every tenant's resolved database config before Flyway runs; host, port,
	// database, schema targeting and TLS stay the tenant's. Nil keeps the
	// provider's credentials.
	MigratorIdentity *MigratorIdentity
}

// MigratorIdentity is the shared role Flyway connects as across the fleet.
//
// Its pair is FlywayMigrator.WithSharedMigrator, which refuses a tenant that
// does not aim Flyway at an explicit schema. Setting MigratorIdentity does NOT
// arm that guard: database-per-tenant PostgreSQL with one migrator role across
// every database and the target schema (typically public) in each is a
// legitimate deployment where the role-level search_path is correct everywhere,
// so inferring the requirement from this field would break real setups. The
// signal is explicit for that reason.
type MigratorIdentity struct {
	Username string
	Password string
}

// ErrNoLister is returned when MigrateAll is called without a TenantLister.
var ErrNoLister = errors.New("migration: TenantLister is nil")

// ErrInvalidMigratorIdentity is returned when MigrateAllOptions.MigratorIdentity is
// set with an empty username or password, a password too short to redact from
// Flyway output, or a CR/LF/NUL in either.
var ErrInvalidMigratorIdentity = errors.New("migration: invalid migrator identity")

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
	if err := validateMigratorIdentity(opts.MigratorIdentity); err != nil {
		return nil, err
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

	var out *MigrateAllResult
	if opts.Parallelism <= 1 {
		out, err = runSequential(ctx, migrator, configs, action, tenantIDs, opts)
	} else {
		out, err = runParallel(ctx, migrator, configs, action, tenantIDs, opts)
	}
	// Both paths dispatch in listing order, so the undispatched tenants are the tail.
	out.NeverDispatched = slices.Clone(tenantIDs[len(out.Results):])
	return out, err
}

func validateMigratorIdentity(identity *MigratorIdentity) error {
	switch {
	case identity == nil:
		return nil
	case identity.Username == "":
		return fmt.Errorf("%w: username is empty", ErrInvalidMigratorIdentity)
	case identity.Password == "":
		return fmt.Errorf("%w: password is empty", ErrInvalidMigratorIdentity)
	}
	creds := &config.DatabaseConfig{Username: identity.Username, Password: identity.Password}
	if err := cmp.Or(validateEnvFields(creds), ensurePasswordRedactable(creds)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMigratorIdentity, err)
	}
	return nil
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
		if err := dispatchBlocked(ctx, opts); err != nil {
			return out, err
		}
		res := runOne(ctx, migrator, configs, action, id, &opts)
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

	// Derived cancellation lets fail-fast siblings unblock when a tenant fails.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := &MigrateAllResult{Action: action, Results: make([]TenantResult, len(tenantIDs))}
	state := &parallelState{
		out:      out,
		cancel:   cancel,
		migrator: migrator,
		configs:  configs,
		action:   action,
		opts:     opts,
	}
	dispatched, stopErr := state.dispatch(runCtx, tenantIDs, parallelism)
	state.wg.Wait()

	// Trim trailing zero-value slots when the dispatch loop exited early so
	// callers iterating Results don't see synthetic empty tenants.
	out.Results = out.Results[:dispatched]

	if state.firstErr != nil {
		return out, state.firstErr
	}
	if stopErr != nil {
		return out, stopErr
	}
	return out, ctx.Err()
}

// dispatchBlocked returns why a runner must not dispatch its next tenant: the
// context's error, ErrQuiesceBlocked, or nil. The context is checked again after
// the quiesce check, which a cancel can outlast and which then fails open.
func dispatchBlocked(ctx context.Context, opts MigrateAllOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if quiesceBlocks(ctx, opts.Quiesce, opts.Logger) {
		return ErrQuiesceBlocked
	}
	return ctx.Err()
}

// parallelState bundles the shared state of a runParallel invocation. Inputs
// that don't change for the duration of the call (migrator, configs, action,
// opts) live here so runWorker keeps a small parameter list. The run-context
// is intentionally NOT a field — Go's idiom keeps Context per call, and
// runWorker takes it as a parameter.
type parallelState struct {
	out      *MigrateAllResult
	cancel   context.CancelFunc
	migrator *FlywayMigrator
	configs  database.DBConfigProvider
	action   Action
	opts     MigrateAllOptions
	wg       sync.WaitGroup
	hookMu   sync.Mutex
	errMu    sync.Mutex
	firstErr error
}

// dispatch starts one worker per tenant, in listing order, until the list is
// exhausted or the run must stop. It returns how many tenants it dispatched and
// why it stopped early, if it did.
func (s *parallelState) dispatch(ctx context.Context, tenantIDs []string, parallelism int) (int, error) {
	sem := make(chan struct{}, parallelism)
	for i, id := range tenantIDs {
		// Checked before the select: with a free slot and a done context both
		// cases are ready, and select would dispatch a random prefix.
		if err := ctx.Err(); err != nil {
			return i, err
		}
		select {
		case <-ctx.Done():
			return i, ctx.Err()
		case sem <- struct{}{}:
		}
		// Judged again once the slot is held: waiting for it can outlast a
		// fail-fast cancel or a quiesce flip.
		if err := dispatchBlocked(ctx, s.opts); err != nil {
			<-sem
			return i, err
		}

		s.wg.Add(1)
		go func(idx int, tenantID string) {
			defer s.wg.Done()
			defer func() { <-sem }()
			s.runWorker(ctx, idx, tenantID)
		}(i, id)
	}
	return len(tenantIDs), nil
}

func (s *parallelState) runWorker(ctx context.Context, idx int, tenantID string) {
	res := runOne(ctx, s.migrator, s.configs, s.action, tenantID, &s.opts)
	s.out.Results[idx] = res

	if s.opts.Hook != nil {
		s.hookMu.Lock()
		s.opts.Hook(res)
		s.hookMu.Unlock()
	}

	if res.Err != nil && !s.opts.ContinueOnError {
		s.recordFirstErr(res.Err)
		s.cancel()
	}
}

func (s *parallelState) recordFirstErr(err error) {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.firstErr == nil {
		s.firstErr = err
	}
}

func runOne(
	ctx context.Context,
	migrator *FlywayMigrator,
	configs database.DBConfigProvider,
	action Action,
	tenantID string,
	opts *MigrateAllOptions,
) TenantResult {
	start := time.Now()
	res := TenantResult{TenantID: tenantID}

	dbCfg, err := configs.DBConfig(ctx, tenantID)
	if err != nil {
		res.Err = fmt.Errorf("resolve db config: %w", err)
		res.Duration = time.Since(start)
		return res
	}
	if dbCfg == nil {
		res.Err = fmt.Errorf("resolve db config: %w", database.ErrNoDatabaseConfig)
		res.Duration = time.Since(start)
		return res
	}
	res.Vendor = dbCfg.Type
	if identity := opts.MigratorIdentity; identity != nil {
		overlaid := *dbCfg
		overlaid.Username = identity.Username
		overlaid.Password = identity.Password
		dbCfg = &overlaid
	}

	defaults := migrator.DefaultMigrationConfigForVendor(dbCfg.Type)
	cfg := mergeConfigs(defaults, opts.BaseConfig)

	switch action {
	case ActionMigrate:
		res.Result, res.Err = migrator.MigrateFor(ctx, dbCfg, cfg)
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
	// Audit fields propagate per-field so per-tenant overrides can refine
	// individual fields without clobbering inherited ones. Without this,
	// every per-tenant migration.applied event would emit with the
	// vendor-default Audit (Principal=<unspecified>) — see ADR-019.
	if override.Audit.Principal != "" {
		out.Audit.Principal = override.Audit.Principal
	}
	if override.Audit.GitCommitSHA != "" {
		out.Audit.GitCommitSHA = override.Audit.GitCommitSHA
	}
	if override.Audit.PipelineRunID != "" {
		out.Audit.PipelineRunID = override.Audit.PipelineRunID
	}
	if override.Audit.Target != "" {
		out.Audit.Target = override.Audit.Target
	}
	return &out
}

// isEmptyConfig reports whether c carries no override fields.
func isEmptyConfig(c *Config) bool {
	return c.FlywayPath == "" && c.ConfigPath == "" && c.MigrationPath == "" &&
		c.Timeout == 0 && c.Environment == "" && !c.DryRun &&
		c.Audit.Principal == "" && c.Audit.GitCommitSHA == "" &&
		c.Audit.PipelineRunID == "" && c.Audit.Target == ""
}
