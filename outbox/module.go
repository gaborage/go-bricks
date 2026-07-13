package outbox

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

// Module implements the GoBricks Module interface for transactional outbox.
// It provides reliable event publishing by writing events to a database table
// within the caller's transaction, then publishing them to the message broker
// via a background relay job.
//
// The module is registered like any other GoBricks module:
//
//	for _, m := range []app.Module{
//	    scheduler.NewModule(), // Required: relay runs as a scheduled job
//	    outbox.NewModule(),    // Outbox module
//	    &myapp.OrderModule{},
//	} {
//	    if err := fw.RegisterModule(m); err != nil {
//	        log.Fatal(err)
//	    }
//	}
type Module struct {
	logger logger.Logger
	config *config.Config
	getDB  func(context.Context) (dbtypes.Interface, error)
	getMsg func(context.Context) (messaging.AMQPClient, error)

	publisher app.OutboxPublisher
	cfg       config.OutboxConfig

	stores tenantstore.Cache[Store] // one store per tenant ("" = single-tenant)
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
// Stores dependencies and initializes the publisher; the vendor-specific store is
// created lazily on first use (see ensureStoreInitialized).
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

	// Fail fast: when outbox is enabled, DB and Messaging resolvers are required.
	// Without them, the relay job would panic on first poll instead of failing at startup.
	if m.getDB == nil {
		return fmt.Errorf("outbox: database resolver (deps.DB) is required when outbox is enabled")
	}
	if m.getMsg == nil {
		return fmt.Errorf("outbox: messaging resolver (deps.Messaging) is required when outbox is enabled")
	}

	// Fail fast: in single-tenant mode the broker URL must be set at startup,
	// otherwise the relay treats the broker as permanently unreachable and advances
	// every pending event's retry_count on every poll without ever delivering (issue #366).
	// Multi-tenant resolves messaging per-tenant via the resource source, so a static
	// check would yield false positives — skip it there.
	if m.config != nil && !m.config.Multitenant.Enabled && !config.IsMessagingConfigured(&m.config.Messaging) {
		return fmt.Errorf("outbox: messaging is not configured but outbox.enabled=true; " +
			"set messaging.broker.url (or env MESSAGING_BROKER_URL) or set outbox.enabled=false")
	}

	// Fail fast on multi-tenant configurations the relay cannot fan out across, rather
	// than silently never relaying events (the prior behavior: the relay's tenant-less
	// context could not resolve any tenant's database):
	//   - dynamic sources: the tenant set is not enumerable at job-registration time;
	//   - static sources with no tenants: there is nothing to fan out to.
	if m.config != nil && m.config.Multitenant.Enabled {
		switch {
		case m.config.Source.Type == config.SourceTypeDynamic:
			return fmt.Errorf("outbox: relay is not supported with dynamic multi-tenant sources " +
				"(source.type=dynamic); use static multitenant.tenants config, or set outbox.enabled=false")
		case len(m.config.Multitenant.Tenants) == 0:
			return fmt.Errorf("outbox: multi-tenant is enabled but no static multitenant.tenants are configured; " +
				"the relay would never deliver any events. Configure multitenant.tenants, or set outbox.enabled=false")
		}
	}

	// Timeout-tuning validation runs AFTER the root-cause checks above: when messaging
	// isn't configured at all (or the relay can't fan out), that actionable error must
	// surface first — not a derived publishtimeout complaint against defaulted values
	// (config.Validate now defaults connectiontimeout/readytimeout in every mode).
	if err := m.validatePublishTimeout(); err != nil {
		return err
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

// validatePublishTimeout fails fast when outbox.publishtimeout is shorter than the broker
// confirmation wait (messaging.reconnect.connectiontimeout). A shorter timeout would truncate
// EVERY legitimate confirmation into a connectivity failure: the broker actually receives and
// routes the message, but the relay never marks it published and re-publishes it on every
// cycle — an unbounded duplicate-delivery loop. That is severe enough to reject at startup
// (Fail Fast) rather than emit a warning an operator might miss.
//
// It also requires publishtimeout >= messaging.reconnect.readytimeout: a shorter value makes
// the per-record deadline expire INSIDE the client's readiness pre-flight, so a not-ready
// broker surfaces as context.DeadlineExceeded instead of ErrNotConnected — which silently
// defeats the relay's mid-batch broker-drop detection (outcomeBrokerDown never fires) and
// reintroduces the serial per-record stall it exists to cap.
//
// It also requires publishtimeout >= messaging.reconnect.resenddelay unless
// maxpublishattempts is 1 (no retry wait to expire inside).
func (m *Module) validatePublishTimeout() error {
	if m.config == nil {
		return nil
	}
	ct := m.config.Messaging.Reconnect.ConnectionTimeout
	if ct > 0 && m.cfg.PublishTimeout < ct {
		return fmt.Errorf("outbox: publishtimeout (%s) must be >= messaging.reconnect.connectiontimeout (%s); "+
			"a shorter value truncates every publish confirmation into a false failure (duplicate-delivery loop)",
			m.cfg.PublishTimeout, ct)
	}
	rt := m.config.Messaging.Reconnect.ReadyTimeout
	if rt > 0 && m.cfg.PublishTimeout < rt {
		return fmt.Errorf("outbox: publishtimeout (%s) must be >= messaging.reconnect.readytimeout (%s); "+
			"a shorter value expires inside the readiness pre-flight and defeats the relay's mid-batch broker-drop detection",
			m.cfg.PublishTimeout, rt)
	}
	// resenddelay only matters when the publish loop can retry: with
	// maxpublishattempts == 1 the attempt ceiling fires before any wait
	// (messaging/amqp_client.go: retryBackoff), so a no-retry setup is exempt.
	rd := m.config.Messaging.Reconnect.ResendDelay
	if rd > 0 && m.config.Messaging.Reconnect.MaxPublishAttempts != 1 && m.cfg.PublishTimeout < rd {
		return fmt.Errorf("outbox: publishtimeout (%s) must be >= messaging.reconnect.resenddelay (%s); "+
			"a shorter value expires inside a single publish-retry wait, so each retryable event burns its whole timeout delivering nothing",
			m.cfg.PublishTimeout, rd)
	}
	return nil
}

// ensureStoreInitialized returns the store for the tenant in ctx, creating it
// (and, if configured, its table) on first use for that tenant. Lazy because
// the vendor is only known once a connection exists; per-tenant because each
// tenant has its own DB (and possibly its own vendor).
func (m *Module) ensureStoreInitialized(ctx context.Context) (Store, error) {
	return m.stores.Get(ctx, &tenantstore.Deps[Store]{
		Name:            "outbox",
		TableName:       m.cfg.TableName,
		AutoCreateTable: m.cfg.AutoCreateTable,
		Logger:          m.logger,
		GetDB:           m.getDB,
		NewPostgres:     NewPostgresStore,
		NewOracle:       NewOracleStore,
		WarnMsg:         "Outbox table creation failed (may already exist)",
	})
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

	tenants := m.config.PerTenantJobKeys()

	// Register relay job
	relay := &Relay{
		store:        &lazyStore{module: m},
		config:       m.cfg,
		getDB:        m.getDB,
		getMessaging: m.getMsg,
		tenants:      tenants,
	}

	if err := registrar.FixedRate("outbox-relay", relay, m.cfg.PollInterval); err != nil {
		return fmt.Errorf("outbox: failed to register relay job: %w", err)
	}

	// Register cleanup job (RetentionPeriod is always positive after applyDefaults,
	// so this always registers when the module is enabled).
	if m.cfg.RetentionPeriod > 0 {
		cleanup := &Cleanup{
			store:           &lazyStore{module: m},
			retentionPeriod: m.cfg.RetentionPeriod,
			getDB:           m.getDB,
			tenants:         tenants,
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

// lazyPublisher wraps app.OutboxPublisher to resolve the tenant's store on
// every call. No caching: a cached publisher would pin the first caller's
// tenant store (and dialect) for the life of the process.
type lazyPublisher struct {
	module *Module
}

func (p *lazyPublisher) Publish(ctx context.Context, tx dbtypes.Tx, event *app.OutboxEvent) (string, error) {
	store, err := p.module.ensureStoreInitialized(ctx)
	if err != nil {
		return "", err
	}
	return newPublisher(store, p.module.cfg.DefaultExchange).Publish(ctx, tx, event)
}

// lazyStore wraps Store to lazily initialize via the module.
// Used by relay and cleanup jobs that start after Init().
type lazyStore struct {
	module *Module
}

func (s *lazyStore) Insert(ctx context.Context, tx dbtypes.Tx, record *Record) error {
	store, err := s.module.ensureStoreInitialized(ctx)
	if err != nil {
		return err
	}
	return store.Insert(ctx, tx, record)
}

func (s *lazyStore) FetchPending(ctx context.Context, db dbtypes.Interface, batchSize int) ([]Record, error) {
	store, err := s.module.ensureStoreInitialized(ctx)
	if err != nil {
		return nil, err
	}
	return store.FetchPending(ctx, db, batchSize)
}

func (s *lazyStore) MarkPublished(ctx context.Context, db dbtypes.Interface, eventID string) error {
	store, err := s.module.ensureStoreInitialized(ctx)
	if err != nil {
		return err
	}
	return store.MarkPublished(ctx, db, eventID)
}

func (s *lazyStore) MarkFailed(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error {
	store, err := s.module.ensureStoreInitialized(ctx)
	if err != nil {
		return err
	}
	return store.MarkFailed(ctx, db, eventID, errMsg)
}

func (s *lazyStore) MarkDeadLettered(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error {
	store, err := s.module.ensureStoreInitialized(ctx)
	if err != nil {
		return err
	}
	return store.MarkDeadLettered(ctx, db, eventID, errMsg)
}

func (s *lazyStore) DeletePublished(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error) {
	store, err := s.module.ensureStoreInitialized(ctx)
	if err != nil {
		return 0, err
	}
	return store.DeletePublished(ctx, db, before)
}

func (s *lazyStore) CreateTable(ctx context.Context, db dbtypes.Interface) error {
	store, err := s.module.ensureStoreInitialized(ctx)
	if err != nil {
		return err
	}
	return store.CreateTable(ctx, db)
}
