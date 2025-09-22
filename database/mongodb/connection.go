package mongodb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
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

// NewConnection creates a new MongoDB connection
func NewConnection(cfg *config.DatabaseConfig, log logger.Logger) (database.Interface, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	if cfg.MaxConns > 0 {
		maxPoolSize := uint64(cfg.MaxConns)
		opts.SetMaxPoolSize(maxPoolSize)
	}
	if cfg.MaxIdleConns > 0 {
		minPoolSize := uint64(cfg.MaxIdleConns)
		opts.SetMinPoolSize(minPoolSize)
	}
	if cfg.ConnMaxLifetime > 0 {
		opts.SetMaxConnIdleTime(cfg.ConnMaxIdleTime)
	}

	// Set read preference
	if cfg.ReadPreference != "" {
		rp, err := parseReadPreference(cfg.ReadPreference)
		if err != nil {
			return nil, fmt.Errorf("invalid read preference: %w", err)
		}
		opts.SetReadPreference(rp)
	}

	// Set write concern
	if cfg.WriteConcern != "" {
		wc, err := parseWriteConcern(cfg.WriteConcern)
		if err != nil {
			return nil, fmt.Errorf("invalid write concern: %w", err)
		}
		opts.SetWriteConcern(wc)
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

// buildMongoURI constructs a MongoDB connection URI from configuration
func buildMongoURI(cfg *config.DatabaseConfig) string {
	var uri strings.Builder

	uri.WriteString("mongodb://")

	// Add credentials if provided
	if cfg.Username != "" {
		uri.WriteString(url.QueryEscape(cfg.Username))
		if cfg.Password != "" {
			uri.WriteString(":")
			uri.WriteString(url.QueryEscape(cfg.Password))
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
		params = append(params, "replicaSet="+cfg.ReplicaSet)
	}
	if cfg.AuthSource != "" {
		params = append(params, "authSource="+cfg.AuthSource)
	}

	switch strings.ToLower(cfg.SSLMode) {
	case "require", "verify-ca", "verify-full":
		params = append(params, "tls=true")
	case "disable":
		params = append(params, "tls=false")
	}

	if len(params) > 0 {
		uri.WriteString("?")
		uri.WriteString(strings.Join(params, "&"))
	}

	return uri.String()
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
		return nil, fmt.Errorf("unknown read preference: %s", pref)
	}
}

// parseWriteConcern converts string to MongoDB write concern
func parseWriteConcern(concern string) (*writeconcern.WriteConcern, error) {
	switch strings.ToLower(concern) {
	case "majority":
		return writeconcern.Majority(), nil
	case "acknowledged":
		return &writeconcern.WriteConcern{W: 1}, nil
	case "unacknowledged":
		return &writeconcern.WriteConcern{W: 0}, nil
	default:
		return nil, fmt.Errorf("unknown write concern: %s", concern)
	}
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

	// Convert SQL transaction options to MongoDB session options
	var mongoOpts *options.TransactionOptions
	if opts != nil {
		mongoOpts = options.Transaction()
		// Map SQL isolation levels to MongoDB read concerns if needed
		// MongoDB transactions use read concern "snapshot" by default
	}

	err = session.StartTransaction(mongoOpts)
	if err != nil {
		session.EndSession(ctx)
		return nil, fmt.Errorf("failed to start MongoDB transaction: %w", err)
	}

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
