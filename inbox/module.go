package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/tenantstore"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// Module implements the GoBricks Module interface for the consumer-side inbox.
// It provides durable, exactly-once event processing via deps.Inbox.ProcessOnce
// and a daily cleanup job that prunes old processed-event records.
//
// Register it like any other module (the scheduler is optional but required for
// the retention cleanup job):
//
//	for _, m := range []app.Module{
//	    scheduler.NewModule(), // optional: enables inbox-cleanup
//	    inbox.NewModule(),
//	    &myapp.ConsumerModule{},
//	} {
//	    if err := fw.RegisterModule(m); err != nil {
//	        log.Fatal(err)
//	    }
//	}
type Module struct {
	logger logger.Logger
	config *config.Config
	getDB  func(context.Context) (dbtypes.Interface, error)

	// sharedDB is the control-plane ("" key) database resolver injected by
	// app.RegisterModule. Used only when inbox.tenancy=shared.
	sharedDB func(context.Context) (dbtypes.Interface, error)

	processor app.InboxProcessor
	cfg       config.InboxConfig

	stores tenantstore.Cache[Store] // one store per tenant ("" = single-tenant)
}

// NewModule creates a new inbox Module instance.
func NewModule() *Module {
	return &Module{}
}

// Name implements app.Module.
func (m *Module) Name() string {
	return "inbox"
}

// SetSharedResolvers injects the control-plane ("" key) resolvers. Called by
// app.RegisterModule; used only when inbox.tenancy=shared. The messaging
// resolver is ignored — the inbox has no broker of its own (ProcessOnce only
// touches the database).
func (m *Module) SetSharedResolvers(
	db func(context.Context) (dbtypes.Interface, error),
	_ func(context.Context) (messaging.AMQPClient, error),
) {
	m.sharedDB = db
}

// sharedLedger reports whether the inbox is configured for shared
// (control-plane) tenancy rather than the default per-tenant fan-out.
func (m *Module) sharedLedger() bool { return m.cfg.Tenancy == config.TenancyShared }

// Init implements app.Module.
func (m *Module) Init(deps *app.ModuleDeps) error {
	m.logger = deps.Logger
	m.config = deps.Config
	m.getDB = deps.DB

	if m.config != nil {
		m.cfg = m.config.Inbox
	}
	applyDefaults(&m.cfg)
	if err := validateConfig(&m.cfg); err != nil {
		return err
	}

	if !m.cfg.Enabled {
		m.logger.Info().Msg("Inbox module disabled (inbox.enabled=false)")
		return nil
	}

	// Fail fast: when the inbox is enabled, the DB resolver is required.
	if m.getDB == nil {
		return fmt.Errorf("inbox: database resolver (deps.DB) is required when inbox is enabled")
	}

	// Shared (control-plane) ledger tenancy: require the framework-injected resolver
	// (set by app.RegisterModule via the sharedResolverSetter duck-type) and swap it
	// in. Runs unconditionally for tenancy=shared (even with multitenant disabled) —
	// shared+MT-disabled resolves the same "" key single-tenant mode already uses, so
	// this is a no-op in effect, but the resolver must still have been injected.
	if m.sharedLedger() {
		if m.sharedDB == nil {
			return tenantstore.SharedResolversRequired("inbox")
		}
		m.getDB = m.sharedDB
		if err := tenantstore.RejectSharedWithStaticTenants("inbox", m.config); err != nil {
			return err
		}
	}

	m.processor = &Inbox{module: m}

	m.logger.Info().
		Str("table", m.cfg.TableName).
		Dur("retentionPeriod", m.cfg.RetentionPeriod).
		Str("tenancy", m.cfg.Tenancy).
		Msg("Inbox module initialized")
	return nil
}

// ensureStoreInitialized returns the store for the tenant in ctx, creating it
// (and, if configured, its table) on first use for that tenant. Lazy because
// the vendor is only known once a connection exists; per-tenant because each
// tenant has its own DB (and possibly its own vendor).
func (m *Module) ensureStoreInitialized(ctx context.Context) (Store, error) {
	return m.stores.Get(ctx, &tenantstore.Deps[Store]{
		Name:            "inbox",
		TableName:       m.cfg.TableName,
		AutoCreateTable: m.cfg.AutoCreateTable,
		Logger:          m.logger,
		GetDB:           m.getDB,
		NewPostgres:     NewPostgresStore,
		NewOracle:       NewOracleStore,
		WarnMsg:         "Inbox table creation failed (may already exist)",
		SingleKey:       m.sharedLedger(),
	})
}

// InboxProcessor implements app.InboxProvider — returns the processor for
// ModuleDeps wiring. Returns nil when the inbox is disabled.
func (m *Module) InboxProcessor() app.InboxProcessor {
	return m.processor
}

// RegisterJobs implements app.JobProvider. The inbox has no relay; it registers
// only the retention cleanup job, and only when retention is positive.
func (m *Module) RegisterJobs(registrar app.JobRegistrar) error {
	if !m.cfg.Enabled || m.cfg.RetentionPeriod <= 0 {
		return nil
	}

	// Fail fast on multi-tenant configurations the cleanup job cannot fan out across, so
	// it does not silently never prune any tenant's ledger. ProcessOnce itself is
	// unaffected (it resolves the tenant from the request context), which is why this
	// guard lives here — failing only when the cleanup job would actually be registered —
	// rather than in Init, so ProcessOnce-only deployments (no scheduler) still work.
	// Shared tenancy takes the single control-plane pass instead, so it is exempt:
	//   - dynamic sources: the tenant set is not enumerable at registration time;
	//   - static sources with no tenants: there is nothing to fan out to.
	if m.config != nil && m.config.Multitenant.Enabled && !m.sharedLedger() {
		switch {
		case m.config.Source.Type == config.SourceTypeDynamic:
			return fmt.Errorf("inbox: retention cleanup is not supported with dynamic multi-tenant sources " +
				"(source.type=dynamic); use static multitenant.tenants config, set inbox.tenancy=shared to " +
				"prune a single control-plane ledger, or run without the scheduler to use ProcessOnce only")
		case len(m.config.Multitenant.Tenants) == 0:
			return fmt.Errorf("inbox: multi-tenant is enabled but no static multitenant.tenants are configured; " +
				"the cleanup job would prune nothing. Configure multitenant.tenants, set inbox.tenancy=shared to " +
				"prune a single control-plane ledger, or run without the scheduler to use ProcessOnce only")
		}
	}

	tenants := m.config.PerTenantJobKeys()
	if m.sharedLedger() {
		tenants = []string{""}
	}

	cleanup := &Cleanup{
		store:           &lazyStore{module: m},
		retentionPeriod: m.cfg.RetentionPeriod,
		getDB:           m.getDB,
		tenants:         tenants,
	}

	// DailyAt uses only the time-of-day (04:00) and applies the scheduler's
	// configured timezone; the date and Location here are placeholders.
	cleanupTime := time.Date(0, 1, 1, 4, 0, 0, 0, time.UTC)
	if err := registrar.DailyAt("inbox-cleanup", cleanup, cleanupTime); err != nil {
		return fmt.Errorf("inbox: failed to register cleanup job: %w", err)
	}

	m.logger.Info().
		Dur("retentionPeriod", m.cfg.RetentionPeriod).
		Msg("Inbox cleanup job registered")
	return nil
}

// Shutdown implements app.Module.
func (m *Module) Shutdown() error {
	m.logger.Info().Msg("Inbox module shut down")
	return nil
}

// lazyStore wraps Store to lazily initialize via the module. Used by the cleanup
// job, which starts after Init() (before the vendor is known).
type lazyStore struct {
	module *Module
}

func (s *lazyStore) MarkProcessed(ctx context.Context, tx dbtypes.Tx, rec Record) (bool, error) {
	store, err := s.module.ensureStoreInitialized(ctx)
	if err != nil {
		return false, err
	}
	return store.MarkProcessed(ctx, tx, rec)
}

func (s *lazyStore) DeleteProcessed(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error) {
	store, err := s.module.ensureStoreInitialized(ctx)
	if err != nil {
		return 0, err
	}
	return store.DeleteProcessed(ctx, db, before)
}

func (s *lazyStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	store, err := s.module.ensureStoreInitialized(ctx)
	if err != nil {
		return err
	}
	return store.CreateTable(ctx, db)
}

// Compile-time guards.
//
// The SetSharedResolvers guard pins its exact signature: the app-side duck-type
// (app.sharedResolverSetter) matches structurally, so an accidental local signature
// edit would otherwise silently stop injection instead of failing to compile.
var (
	_ app.Module         = (*Module)(nil)
	_ app.InboxProvider  = (*Module)(nil)
	_ app.JobProvider    = (*Module)(nil)
	_ app.InboxProcessor = (*Inbox)(nil)
	_ Store              = (*lazyStore)(nil)
	_ interface {
		SetSharedResolvers(
			func(context.Context) (dbtypes.Interface, error),
			func(context.Context) (messaging.AMQPClient, error),
		)
	} = (*Module)(nil)
)
