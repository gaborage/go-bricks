package outbox

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/scheduler"
)

// Module implements the GoBricks Module interface for transactional outbox.
// It provides reliable event publishing by writing events to a database table
// within the caller's transaction, then publishing them to the message broker
// via a background relay job.
//
// The module is registered like any other GoBricks module:
//
//	fw.RegisterModules(
//	    scheduler.NewModule(),  // Required: relay runs as a scheduled job
//	    outbox.NewModule(),     // Outbox module
//	    &myapp.OrderModule{},
//	)
type Module struct {
	logger logger.Logger
	config *config.Config
	getDB  func(context.Context) (dbtypes.Interface, error)
	getMsg func(context.Context) (messaging.AMQPClient, error)

	store     Store
	publisher app.OutboxPublisher
	cfg       config.OutboxConfig

	initMu       sync.Mutex // Protects store initialization (lazy init from concurrent goroutines)
	tableCreated bool
}

// NewModule creates a new Module instance.
func NewModule() *Module {
	return &Module{}
}

// Name implements app.Module.
func (m *Module) Name() string {
	return "outbox"
}

// Init implements app.Module.
// Stores dependencies, creates the vendor-specific store, and initializes the publisher.
func (m *Module) Init(deps *app.ModuleDeps) error {
	m.logger = deps.Logger
	m.config = deps.Config
	m.getDB = deps.DB
	m.getMsg = deps.Messaging

	// Load outbox config
	if m.config != nil {
		m.cfg = m.config.Outbox
	}
	applyDefaults(&m.cfg)

	if err := validateConfig(&m.cfg); err != nil {
		return err
	}

	if !m.cfg.Enabled {
		m.logger.Info().Msg("Outbox module disabled (outbox.enabled=false)")
		return nil
	}

	// Store creation is deferred until first use (lazy init like scheduler)
	// because we need to know the database vendor type, which requires a DB connection.
	// The publisher wraps the store and handles lazy initialization.
	m.publisher = &lazyPublisher{module: m}

	m.logger.Info().
		Str("table", m.cfg.TableName).
		Dur("pollInterval", m.cfg.PollInterval).
		Int("batchSize", m.cfg.BatchSize).
		Msg("Outbox module initialized")

	return nil
}

// ensureStoreInitialized creates the vendor-specific store on first use.
// This is lazy because the database vendor type is only known at runtime.
// Uses double-check locking (mutex, not sync.Once) because initialization
// can fail and should be retried — matching the scheduler module pattern.
func (m *Module) ensureStoreInitialized(ctx context.Context) error {
	m.initMu.Lock()
	defer m.initMu.Unlock()

	// Already initialized
	if m.store != nil {
		return nil
	}

	db, err := m.getDB(ctx)
	if err != nil {
		return fmt.Errorf("outbox: database unavailable: %w", err)
	}

	// Create vendor-specific store into local variable — only assign to m.store on success
	var store Store
	switch db.DatabaseType() {
	case dbtypes.PostgreSQL:
		store, err = NewPostgresStore(m.cfg.TableName)
	case dbtypes.Oracle:
		store, err = NewOracleStore(m.cfg.TableName)
	default:
		return fmt.Errorf("outbox: unsupported database vendor: %s", db.DatabaseType())
	}
	if err != nil {
		return fmt.Errorf("outbox: failed to create store: %w", err)
	}

	// Auto-create table if configured.
	// Warning-only on failure: PostgreSQL uses IF NOT EXISTS (idempotent),
	// Oracle may return ORA-00955 if the table/index already exists.
	// Both cases are benign — the table is usable either way.
	//
	// m.tableCreated is set unconditionally to prevent repeated warnings on
	// every subsequent call. If the failure was transient (e.g., network),
	// the store operations themselves will fail with a clear error.
	if m.cfg.AutoCreateTable && !m.tableCreated {
		if createErr := store.CreateTable(ctx, db); createErr != nil {
			m.logger.Warn().Err(createErr).
				Str("table", m.cfg.TableName).
				Msg("Outbox table creation failed (may already exist)")
		}
		m.tableCreated = true
	}

	m.store = store
	return nil
}

// OutboxPublisher implements app.OutboxProvider — returns the Publisher for ModuleDeps wiring.
func (m *Module) OutboxPublisher() app.OutboxPublisher {
	return m.publisher
}

// RegisterJobs implements app.JobProvider.
// Registers the relay and cleanup jobs with the scheduler.
func (m *Module) RegisterJobs(registrar app.JobRegistrar) error {
	if !m.cfg.Enabled {
		return nil
	}

	// Register relay job
	relay := &Relay{
		store:  &lazyStore{module: m},
		config: m.cfg,
		getMessaging: func(ctx scheduler.JobContext) messaging.AMQPClient {
			client := ctx.Messaging()
			if client == nil {
				return nil
			}
			if amqpClient, ok := client.(messaging.AMQPClient); ok {
				return amqpClient
			}
			return nil
		},
	}

	if err := registrar.FixedRate("outbox-relay", relay, m.cfg.PollInterval); err != nil {
		return fmt.Errorf("outbox: failed to register relay job: %w", err)
	}

	// Register cleanup job if retention is configured
	if m.cfg.RetentionPeriod > 0 {
		cleanup := &Cleanup{
			store:           &lazyStore{module: m},
			retentionPeriod: m.cfg.RetentionPeriod,
		}

		cleanupTime := time.Date(0, 1, 1, 4, 0, 0, 0, time.Local)
		if err := registrar.DailyAt("outbox-cleanup", cleanup, cleanupTime); err != nil {
			return fmt.Errorf("outbox: failed to register cleanup job: %w", err)
		}
	}

	m.logger.Info().
		Dur("pollInterval", m.cfg.PollInterval).
		Dur("retentionPeriod", m.cfg.RetentionPeriod).
		Msg("Outbox relay and cleanup jobs registered")

	return nil
}

// Shutdown implements app.Module.
func (m *Module) Shutdown() error {
	m.logger.Info().Msg("Outbox module shut down")
	return nil
}

// lazyPublisher wraps app.OutboxPublisher to lazily initialize the store on first use.
// Caches the publisher instance after first successful initialization via sync.Once.
type lazyPublisher struct {
	module    *Module
	once      sync.Once
	cachedPub app.OutboxPublisher
}

func (p *lazyPublisher) Publish(ctx context.Context, tx dbtypes.Tx, event *app.OutboxEvent) (string, error) {
	if err := p.module.ensureStoreInitialized(ctx); err != nil {
		return "", err
	}

	p.once.Do(func() {
		p.cachedPub = newPublisher(p.module.store, p.module.cfg.DefaultExchange)
	})
	return p.cachedPub.Publish(ctx, tx, event)
}

// lazyStore wraps Store to lazily initialize via the module.
// Used by relay and cleanup jobs that start after Init().
type lazyStore struct {
	module *Module
}

func (s *lazyStore) Insert(ctx context.Context, tx dbtypes.Tx, record *Record) error {
	if err := s.module.ensureStoreInitialized(ctx); err != nil {
		return err
	}
	return s.module.store.Insert(ctx, tx, record)
}

func (s *lazyStore) FetchPending(ctx context.Context, db dbtypes.Interface, batchSize, maxRetries int) ([]Record, error) {
	if err := s.module.ensureStoreInitialized(ctx); err != nil {
		return nil, err
	}
	return s.module.store.FetchPending(ctx, db, batchSize, maxRetries)
}

func (s *lazyStore) MarkPublished(ctx context.Context, db dbtypes.Interface, eventID string) error {
	if err := s.module.ensureStoreInitialized(ctx); err != nil {
		return err
	}
	return s.module.store.MarkPublished(ctx, db, eventID)
}

func (s *lazyStore) MarkFailed(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error {
	if err := s.module.ensureStoreInitialized(ctx); err != nil {
		return err
	}
	return s.module.store.MarkFailed(ctx, db, eventID, errMsg)
}

func (s *lazyStore) DeletePublished(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error) {
	if err := s.module.ensureStoreInitialized(ctx); err != nil {
		return 0, err
	}
	return s.module.store.DeletePublished(ctx, db, before)
}

func (s *lazyStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	if err := s.module.ensureStoreInitialized(ctx); err != nil {
		return err
	}
	return s.module.store.CreateTable(ctx, db)
}
