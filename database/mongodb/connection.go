package mongodb

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
)

// Sentinel errors for MongoDB configuration validation
var (
	ErrInvalidReadPreference = errors.New("invalid read preference")
	ErrInvalidWriteConcern   = errors.New("invalid write concern")
)

// Connection implements the database.Interface for MongoDB
type Connection struct {
	client   *mongo.Client
	database *mongo.Database
	config   *config.DatabaseConfig
	logger   logger.Logger
}

var (
	connectMongoDB = func(ctx context.Context, opts *options.ClientOptions) (*mongo.Client, error) {
		return mongo.Connect(ctx, opts)
	}
	pingMongoDB = func(ctx context.Context, client *mongo.Client) error {
		return client.Ping(ctx, readpref.Primary())
	}
)

const (
	defaultConnectionTimeout = 10 * time.Second
)

// NewConnection creates a new MongoDB connection
func NewConnection(cfg *config.DatabaseConfig, log logger.Logger) (database.Interface, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultConnectionTimeout)
	defer cancel()

	// Build connection options
	opts := options.Client()

	// Set URI
	if cfg.ConnectionString != "" {
		opts.ApplyURI(cfg.ConnectionString)
	} else {
		uri := buildMongoURI(cfg)
		opts.ApplyURI(uri)
	}

	// Set connection pool options
	setConnectionOptions(opts, cfg)

	// Set read preference
	if err := setReadPreference(opts, cfg.ReadPreference); err != nil {
		return nil, err
	}

	// Set write concern
	if err := setWriteConcern(opts, cfg.WriteConcern); err != nil {
		return nil, err
	}

	// Configure TLS based on SSL mode
	if cfg.SSLMode != "" {
		tlsConfig, err := buildTLSConfig(cfg.SSLMode)
		if err != nil {
			return nil, fmt.Errorf("failed to configure TLS: %w", err)
		}
		if tlsConfig != nil {
			opts.SetTLSConfig(tlsConfig)
		}
	}

	// Connect to MongoDB
	client, err := connectMongoDB(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Test connection
	if err := pingMongoDB(ctx, client); err != nil {
		if closeErr := client.Disconnect(ctx); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to disconnect MongoDB client after ping failure")
		}
		return nil, fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	// Get database
	mongoDB := client.Database(cfg.Database)

	log.Info().
		Str("host", cfg.Host).
		Int("port", cfg.Port).
		Str("database", cfg.Database).
		Str("replica_set", cfg.ReplicaSet).
		Msg("Connected to MongoDB")

	return &Connection{
		client:   client,
		database: mongoDB,
		config:   cfg,
		logger:   log,
	}, nil
}

// setConnectionOptions sets connection pool options based on configuration
func setConnectionOptions(opts *options.ClientOptions, cfg *config.DatabaseConfig) {
	// Set connection pool options
	if cfg.MaxConns > 0 {
		maxPoolSize := uint64(cfg.MaxConns)
		opts.SetMaxPoolSize(maxPoolSize)
	}

	if cfg.MaxIdleConns > 0 {
		minPoolSize := uint64(cfg.MaxIdleConns)
		opts.SetMinPoolSize(minPoolSize)
	}
	if cfg.ConnMaxIdleTime > 0 {
		opts.SetMaxConnIdleTime(cfg.ConnMaxIdleTime)
	}
}

// setReadPreference sets the read preference in the client options
func setReadPreference(opts *options.ClientOptions, pref string) error {
	if pref != "" {
		rp, err := parseReadPreference(pref)
		if err != nil {
			return fmt.Errorf("invalid read preference: %w", err)
		}
		opts.SetReadPreference(rp)
	}
	return nil
}

// setWriteConcern sets the write concern in the client options
func setWriteConcern(opts *options.ClientOptions, concern string) error {
	if concern != "" {
		wc, err := parseWriteConcern(concern)
		if err != nil {
			return fmt.Errorf("invalid write concern: %w", err)
		}
		opts.SetWriteConcern(wc)
	}
	return nil
}

// buildMongoURI constructs a MongoDB connection URI from configuration
func buildMongoURI(cfg *config.DatabaseConfig) string {
	var uri strings.Builder

	uri.WriteString("mongodb://")

	// Add credentials if provided
	if cfg.Username != "" {
		uri.WriteString(url.PathEscape(cfg.Username))
		if cfg.Password != "" {
			uri.WriteString(":")
			uri.WriteString(url.PathEscape(cfg.Password))
		}
		uri.WriteString("@")
	}

	// Add host:port
	uri.WriteString(cfg.Host)
	if cfg.Port > 0 {
		uri.WriteString(fmt.Sprintf(":%d", cfg.Port))
	}

	// Add database
	if cfg.Database != "" {
		uri.WriteString("/")
		uri.WriteString(cfg.Database)
	}

	// Add query parameters
	var params []string
	if cfg.ReplicaSet != "" {
		params = append(params, "replicaSet="+url.QueryEscape(cfg.ReplicaSet))
	}
	if cfg.AuthSource != "" {
		params = append(params, "authSource="+url.QueryEscape(cfg.AuthSource))
	}

	if len(params) > 0 {
		uri.WriteString("?")
		uri.WriteString(strings.Join(params, "&"))
	}

	return uri.String()
}

// buildTLSConfig creates a TLS configuration based on the SSL mode
func buildTLSConfig(sslMode string) (*tls.Config, error) {
	switch strings.ToLower(sslMode) {
	case "disable":
		// No TLS configuration needed
		return nil, nil
	case "verify-ca":
		// Unsupported in this implementation
		return nil, fmt.Errorf("SSL mode 'verify-ca' is not supported in this implementation")
	case "verify-full", "require":
		// Verify both certificate authority and hostname (most secure)
		// This is the default behavior when InsecureSkipVerify is false
		return &tls.Config{
			InsecureSkipVerify: false,
			MinVersion:         tls.VersionTLS12,
		}, nil
	default:
		return nil, fmt.Errorf("unknown SSL mode: %s", sslMode)
	}
}

// parseReadPreference converts string to MongoDB read preference
func parseReadPreference(pref string) (*readpref.ReadPref, error) {
	switch strings.ToLower(pref) {
	case "primary":
		return readpref.Primary(), nil
	case "primarypreferred":
		return readpref.PrimaryPreferred(), nil
	case "secondary":
		return readpref.Secondary(), nil
	case "secondarypreferred":
		return readpref.SecondaryPreferred(), nil
	case "nearest":
		return readpref.Nearest(), nil
	default:
		return nil, ErrInvalidReadPreference
	}
}

// parseWriteConcern converts string to MongoDB write concern
func parseWriteConcern(concern string) (*writeconcern.WriteConcern, error) {
	// Trim whitespace and convert to lowercase
	trimmed := strings.TrimSpace(concern)
	lower := strings.ToLower(trimmed)

	// Handle predefined write concern names
	switch lower {
	case "majority":
		return writeconcern.Majority(), nil
	case "acknowledged":
		return &writeconcern.WriteConcern{W: 1}, nil
	case "unacknowledged":
		return &writeconcern.WriteConcern{W: 0}, nil
	}

	// Try to parse as a non-negative integer
	if n, err := strconv.Atoi(trimmed); err == nil && n >= 0 {
		return &writeconcern.WriteConcern{W: n}, nil
	}

	// Return error for invalid write concern
	return nil, ErrInvalidWriteConcern
}

// Query executes a MongoDB query (not applicable for document databases)
func (c *Connection) Query(_ context.Context, _ string, _ ...interface{}) (*sql.Rows, error) {
	return nil, fmt.Errorf("SQL query operations not supported for MongoDB")
}

// QueryRow executes a MongoDB query returning a single row (not applicable)
func (c *Connection) QueryRow(_ context.Context, _ string, _ ...interface{}) *sql.Row {
	// MongoDB doesn't support SQL queries, this is for interface compatibility
	return nil
}

// Exec executes a MongoDB command (not applicable for document databases)
func (c *Connection) Exec(_ context.Context, _ string, _ ...interface{}) (sql.Result, error) {
	return nil, fmt.Errorf("SQL exec operations not supported for MongoDB")
}

// Prepare creates a prepared statement (not applicable for MongoDB)
func (c *Connection) Prepare(_ context.Context, _ string) (database.Statement, error) {
	return nil, fmt.Errorf("prepared statements not supported for MongoDB")
}

// Begin starts a MongoDB transaction
func (c *Connection) Begin(ctx context.Context) (database.Tx, error) {
	return c.BeginTx(ctx, nil)
}

// BeginTx starts a MongoDB transaction with options
func (c *Connection) BeginTx(ctx context.Context, opts *sql.TxOptions) (database.Tx, error) {
	session, err := c.client.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start MongoDB session: %w", err)
	}

	// Use defer to ensure session cleanup on any error path
	var sessionStarted bool
	defer func() {
		if !sessionStarted {
			session.EndSession(ctx)
		}
	}()

	// Convert SQL transaction options to MongoDB session options
	var mongoOpts *options.TransactionOptions
	if opts != nil {
		mongoOpts = options.Transaction()
		// Map SQL isolation levels to MongoDB read concerns if needed
		// MongoDB transactions use read concern "snapshot" by default
	}

	err = session.StartTransaction(mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to start MongoDB transaction: %w", err)
	}

	// Mark session as successfully started to prevent cleanup
	sessionStarted = true

	return &Transaction{
		session:   session,
		database:  c.database,
		logger:    c.logger,
		parentCtx: ctx,
	}, nil
}

// Health checks MongoDB connection health
func (c *Connection) Health(ctx context.Context) error {
	return pingMongoDB(ctx, c.client)
}

// Stats returns MongoDB connection statistics
func (c *Connection) Stats() (map[string]interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stats := make(map[string]interface{})

	// Get server status
	var serverStatus map[string]interface{}
	err := c.database.RunCommand(ctx, map[string]interface{}{"serverStatus": 1}).Decode(&serverStatus)
	if err != nil {
		c.logger.Warn().Err(err).Msg("Failed to get MongoDB server status")
	} else {
		if connections, ok := serverStatus["connections"].(map[string]interface{}); ok {
			stats["connections"] = connections
		}
	}

	// Get database stats
	var dbStats map[string]interface{}
	err = c.database.RunCommand(ctx, map[string]interface{}{"dbStats": 1}).Decode(&dbStats)
	if err != nil {
		c.logger.Warn().Err(err).Msg("Failed to get MongoDB database stats")
	} else {
		stats["database"] = dbStats
	}

	stats["database_type"] = "mongodb"
	return stats, nil
}

// Close closes the MongoDB connection
func (c *Connection) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := c.client.Disconnect(ctx); err != nil {
		return fmt.Errorf("failed to disconnect from MongoDB: %w", err)
	}

	c.logger.Info().Msg("Disconnected from MongoDB")
	return nil
}

// DatabaseType returns the database type
func (c *Connection) DatabaseType() string {
	return "mongodb"
}

// GetMigrationTable returns the migration collection name for MongoDB
func (c *Connection) GetMigrationTable() string {
	return "schema_migrations"
}

// CreateMigrationTable creates the migration collection if it doesn't exist
func (c *Connection) CreateMigrationTable(ctx context.Context) error {
	collectionName := c.GetMigrationTable()

	// Check if collection exists
	collections, err := c.database.ListCollectionNames(ctx, map[string]interface{}{"name": collectionName})
	if err != nil {
		return fmt.Errorf("failed to list collections: %w", err)
	}

	// Collection already exists
	if len(collections) > 0 {
		return nil
	}

	// Create collection with schema validation
	opts := options.CreateCollection()
	opts.SetValidator(map[string]interface{}{
		"$jsonSchema": map[string]interface{}{
			"bsonType": "object",
			"required": []string{"version", "description", "applied_at"},
			"properties": map[string]interface{}{
				"version": map[string]interface{}{
					"bsonType":    "string",
					"description": "Migration version identifier",
				},
				"description": map[string]interface{}{
					"bsonType":    "string",
					"description": "Migration description",
				},
				"applied_at": map[string]interface{}{
					"bsonType":    "date",
					"description": "Timestamp when migration was applied",
				},
			},
		},
	})

	err = c.database.CreateCollection(ctx, collectionName, opts)
	if err != nil {
		return fmt.Errorf("failed to create migration collection: %w", err)
	}

	// Create unique index on version
	indexModel := mongo.IndexModel{
		Keys:    map[string]interface{}{"version": 1},
		Options: options.Index().SetUnique(true),
	}

	_, err = c.database.Collection(collectionName).Indexes().CreateOne(ctx, indexModel)
	if err != nil {
		return fmt.Errorf("failed to create migration version index: %w", err)
	}

	c.logger.Info().Str("collection", collectionName).Msg("Created MongoDB migration collection")
	return nil
}

// GetDatabase returns the underlying MongoDB database instance
func (c *Connection) GetDatabase() *mongo.Database {
	return c.database
}

// GetClient returns the underlying MongoDB client instance
func (c *Connection) GetClient() *mongo.Client {
	return c.client
}
