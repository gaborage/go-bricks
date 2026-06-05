package inbox

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// Module implements the GoBricks Module interface for the consumer-side inbox.
// It provides durable, exactly-once event processing via deps.Inbox.ProcessOnce
// and a daily cleanup job that prunes old processed-event records.
//
// Register it like any other module (the scheduler is optional but required for
// the retention cleanup job):
//
//	fw.RegisterModules(
//	    scheduler.NewModule(), // optional: enables inbox-cleanup
//	    inbox.NewModule(),
//	    &myapp.ConsumerModule{},
//	)
type Module struct {
	logger logger.Logger
	config *config.Config
	getDB  func(context.Context) (dbtypes.Interface, error)

	store     Store
	processor app.InboxProcessor
	cfg       config.InboxConfig

	initMu       sync.Mutex // Protects lazy store initialization
	tableCreated bool
}

// NewModule creates a new inbox Module instance.
func NewModule() *Module {
	return &Module{}
}

// Name implements app.Module.
func (m *Module) Name() string {
	return "inbox"
}

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

	m.processor = &Inbox{module: m}

	m.logger.Info().
		Str("table", m.cfg.TableName).
		Dur("retentionPeriod", m.cfg.RetentionPeriod).
		Msg("Inbox module initialized")
	return nil
}

// ensureStoreInitialized creates the vendor-specific store on first use.
// Lazy because the database vendor type is only known once a connection exists.
// Uses double-check locking (mutex, not sync.Once) so a failed init can retry.
func (m *Module) ensureStoreInitialized(ctx context.Context) error {
	m.initMu.Lock()
	defer m.initMu.Unlock()

	if m.store != nil {
		return nil
	}

	db, err := m.getDB(ctx)
	if err != nil {
		return fmt.Errorf("inbox: database unavailable: %w", err)
	}

	var store Store
	switch db.DatabaseType() {
	case dbtypes.PostgreSQL:
		store, err = NewPostgresStore(m.cfg.TableName)
	case dbtypes.Oracle:
		store, err = NewOracleStore(m.cfg.TableName)
	default:
		return fmt.Errorf("inbox: unsupported database vendor: %s", db.DatabaseType())
	}
	if err != nil {
		return fmt.Errorf("inbox: failed to create store: %w", err)
	}

	// Auto-create the table if configured. Warning-only on failure: PostgreSQL
	// uses IF NOT EXISTS (idempotent), Oracle may return ORA-00955 if it exists.
	if m.cfg.AutoCreateTable && !m.tableCreated {
		if createErr := store.CreateTable(ctx, db); createErr != nil {
			m.logger.Warn().Err(createErr).
				Str("table", m.cfg.TableName).
				Msg("Inbox table creation failed (may already exist)")
		}
		m.tableCreated = true
	}

	m.store = store
	return nil
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

	cleanup := &Cleanup{
		store:           &lazyStore{module: m},
		retentionPeriod: m.cfg.RetentionPeriod,
	}

	// DailyAt uses only the time-of-day (04:00 local); the date components are
	// placeholders.
	cleanupTime := time.Date(0, 1, 1, 4, 0, 0, 0, time.Local)
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
	if err := s.module.ensureStoreInitialized(ctx); err != nil {
		return false, err
	}
	return s.module.store.MarkProcessed(ctx, tx, rec)
}

func (s *lazyStore) DeleteProcessed(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error) {
	if err := s.module.ensureStoreInitialized(ctx); err != nil {
		return 0, err
	}
	return s.module.store.DeleteProcessed(ctx, db, before)
}

func (s *lazyStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	if err := s.module.ensureStoreInitialized(ctx); err != nil {
		return err
	}
	return s.module.store.CreateTable(ctx, db)
}

// Compile-time guards.
var (
	_ app.Module         = (*Module)(nil)
	_ app.InboxProvider  = (*Module)(nil)
	_ app.JobProvider    = (*Module)(nil)
	_ app.InboxProcessor = (*Inbox)(nil)
	_ Store              = (*lazyStore)(nil)
)
