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
	"github.com/gaborage/go-bricks/server"
)

// OutboxModule implements the GoBricks Module interface for transactional outbox.
// It provides reliable event publishing by writing events to a database table
// within the caller's transaction, then publishing them to the message broker
// via a background relay job.
//
// The module is registered like any other GoBricks module:
//
//	fw.RegisterModules(
//	    scheduler.NewSchedulerModule(),  // Required: relay runs as a scheduled job
//	    outbox.NewOutboxModule(),        // Outbox module
//	    &myapp.OrderModule{},
//	)
//
//nolint:revive // Intentional stutter for clarity - follows GoBricks naming pattern (e.g., scheduler.SchedulerModule)
type OutboxModule struct {
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

// NewOutboxModule creates a new OutboxModule instance.
func NewOutboxModule() *OutboxModule {
	return &OutboxModule{}
}

// Name implements app.Module.
func (m *OutboxModule) Name() string {
	return "outbox"
}

// Init implements app.Module.
// Stores dependencies, creates the vendor-specific store, and initializes the publisher.
func (m *OutboxModule) Init(deps *app.ModuleDeps) error {
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
func (m *OutboxModule) ensureStoreInitialized(ctx context.Context) error {
	// Fast path: already initialized (no lock needed)
	if m.store != nil {
		return nil
	}

	m.initMu.Lock()
	defer m.initMu.Unlock()

	// Double-check after acquiring lock
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
	// Warning-only on failure: CREATE TABLE IF NOT EXISTS may fail if the table
	// already exists and the user lacks DDL permissions. This is benign.
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
func (m *OutboxModule) OutboxPublisher() app.OutboxPublisher {
	return m.publisher
}

// RegisterRoutes implements app.Module. Outbox has no HTTP routes.
func (m *OutboxModule) RegisterRoutes(_ *server.HandlerRegistry, _ server.RouteRegistrar) {
	// No-op: outbox doesn't expose HTTP endpoints
}

// DeclareMessaging implements app.Module. Outbox doesn't declare its own messaging infrastructure.
func (m *OutboxModule) DeclareMessaging(_ *messaging.Declarations) {
	// No-op: outbox publishes to exchanges declared by other modules
}

// RegisterJobs implements app.JobProvider.
// Registers the relay and cleanup jobs with the scheduler.
func (m *OutboxModule) RegisterJobs(registrar app.JobRegistrar) error {
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
func (m *OutboxModule) Shutdown() error {
	m.logger.Info().Msg("Outbox module shut down")
	return nil
}

// lazyPublisher wraps app.OutboxPublisher to lazily initialize the store on first use.
type lazyPublisher struct {
	module *OutboxModule
}

func (p *lazyPublisher) Publish(ctx context.Context, tx dbtypes.Tx, event *app.OutboxEvent) (string, error) {
	if err := p.module.ensureStoreInitialized(ctx); err != nil {
		return "", err
	}

	pub := newPublisher(p.module.store, p.module.cfg.DefaultExchange)
	return pub.Publish(ctx, tx, event)
}

// lazyStore wraps Store to lazily initialize via the module.
// Used by relay and cleanup jobs that start after Init().
type lazyStore struct {
	module *OutboxModule
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
