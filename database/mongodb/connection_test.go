package mongodb

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLogger implements logger.Logger for testing
type testLogger struct{}

func (l *testLogger) Info() logger.LogEvent                             { return &testLogEvent{} }
func (l *testLogger) Error() logger.LogEvent                            { return &testLogEvent{} }
func (l *testLogger) Debug() logger.LogEvent                            { return &testLogEvent{} }
func (l *testLogger) Warn() logger.LogEvent                             { return &testLogEvent{} }
func (l *testLogger) Fatal() logger.LogEvent                            { return &testLogEvent{} }
func (l *testLogger) WithContext(_ interface{}) logger.Logger           { return l }
func (l *testLogger) WithFields(_ map[string]interface{}) logger.Logger { return l }

// testLogEvent implements logger.LogEvent for testing
type testLogEvent struct{}

func (e *testLogEvent) Str(_, _ string) logger.LogEvent                   { return e }
func (e *testLogEvent) Int(_ string, _ int) logger.LogEvent               { return e }
func (e *testLogEvent) Int64(_ string, _ int64) logger.LogEvent           { return e }
func (e *testLogEvent) Uint64(_ string, _ uint64) logger.LogEvent         { return e }
func (e *testLogEvent) Dur(_ string, _ time.Duration) logger.LogEvent     { return e }
func (e *testLogEvent) Interface(_ string, _ interface{}) logger.LogEvent { return e }
func (e *testLogEvent) Bytes(_ string, _ []byte) logger.LogEvent          { return e }
func (e *testLogEvent) Err(_ error) logger.LogEvent                       { return e }
func (e *testLogEvent) Msg(_ string) {
	// No-op implementation for testing
}
func (e *testLogEvent) Msgf(_ string, _ ...interface{}) {
	// No-op implementation for testing
}

func TestBuildMongoURI(t *testing.T) {
	tests := []struct {
		name     string
		config   *config.DatabaseConfig
		expected string
	}{
		{
			name: "basic connection",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Port:     27017,
				Database: "testdb",
			},
			expected: "mongodb://localhost:27017/testdb",
		},
		{
			name: "with credentials",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Port:     27017,
				Database: "testdb",
				Username: "user",
				Password: "pass",
			},
			expected: "mongodb://user:pass@localhost:27017/testdb",
		},
		{
			name: "with replica set",
			config: &config.DatabaseConfig{
				Host:       "localhost",
				Port:       27017,
				Database:   "testdb",
				Username:   "user",
				Password:   "pass",
				ReplicaSet: "rs0",
			},
			expected: "mongodb://user:pass@localhost:27017/testdb?replicaSet=rs0",
		},
		{
			name: "with auth source and replica set",
			config: &config.DatabaseConfig{
				Host:       "localhost",
				Port:       27017,
				Database:   "testdb",
				Username:   "user",
				Password:   "pass",
				ReplicaSet: "rs0",
				AuthSource: "admin",
			},
			expected: "mongodb://user:pass@localhost:27017/testdb?replicaSet=rs0&authSource=admin",
		},
		{
			name: "no port specified",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Database: "testdb",
			},
			expected: "mongodb://localhost/testdb",
		},
		{
			name: "username only",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Port:     27017,
				Database: "testdb",
				Username: "user",
			},
			expected: "mongodb://user@localhost:27017/testdb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildMongoURI(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseReadPreference(t *testing.T) {
	tests := []struct {
		name        string
		preference  string
		expectError bool
	}{
		{"primary", "primary", false},
		{"primaryPreferred", "primarypreferred", false},
		{"secondary", "secondary", false},
		{"secondaryPreferred", "secondarypreferred", false},
		{"nearest", "nearest", false},
		{"invalid", "invalid", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseReadPreference(tt.preference)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestParseWriteConcern(t *testing.T) {
	tests := []struct {
		name        string
		concern     string
		expectError bool
	}{
		{"majority", "majority", false},
		{"acknowledged", "acknowledged", false},
		{"unacknowledged", "unacknowledged", false},
		{"invalid", "invalid", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseWriteConcern(tt.concern)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestConnection_DatabaseType(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("database type", func(mt *mtest.T) {
		cfg := &config.DatabaseConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "test",
		}
		log := &testLogger{}

		// Mock successful connection
		originalConnect := connectMongoDB
		originalPing := pingMongoDB
		defer func() {
			connectMongoDB = originalConnect
			pingMongoDB = originalPing
		}()

		connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
			return mt.Client, nil
		}
		pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
			return nil
		}

		conn, err := NewConnection(cfg, log)
		require.NoError(t, err)

		assert.Equal(t, "mongodb", conn.DatabaseType())
	})
}

func TestConnection_GetMigrationTable(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("migration table name", func(mt *mtest.T) {
		cfg := &config.DatabaseConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "test",
		}
		log := &testLogger{}

		// Mock successful connection
		originalConnect := connectMongoDB
		originalPing := pingMongoDB
		defer func() {
			connectMongoDB = originalConnect
			pingMongoDB = originalPing
		}()

		connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
			return mt.Client, nil
		}
		pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
			return nil
		}

		conn, err := NewConnection(cfg, log)
		require.NoError(t, err)

		assert.Equal(t, "schema_migrations", conn.GetMigrationTable())
	})
}

func TestConnection_SQLCompatibilityMethods(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("SQL compatibility", func(mt *mtest.T) {
		cfg := &config.DatabaseConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "test",
		}
		log := &testLogger{}

		// Mock successful connection
		originalConnect := connectMongoDB
		originalPing := pingMongoDB
		defer func() {
			connectMongoDB = originalConnect
			pingMongoDB = originalPing
		}()

		connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
			return mt.Client, nil
		}
		pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
			return nil
		}

		conn, err := NewConnection(cfg, log)
		require.NoError(t, err)

		ctx := context.Background()

		// Test Query method returns error
		rows, err := conn.Query(ctx, "SELECT * FROM test")
		assert.Error(t, err)
		assert.Nil(t, rows)
		assert.Contains(t, err.Error(), "SQL query operations not supported")

		// Test QueryRow method returns nil
		row := conn.QueryRow(ctx, "SELECT * FROM test")
		assert.Nil(t, row)

		// Test Exec method returns error
		result, err := conn.Exec(ctx, "INSERT INTO test VALUES (1)")
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "SQL exec operations not supported")

		// Test Prepare method returns error
		stmt, err := conn.Prepare(ctx, "SELECT * FROM test WHERE id = ?")
		assert.Error(t, err)
		assert.Nil(t, stmt)
		assert.Contains(t, err.Error(), "prepared statements not supported")
	})
}

func TestConnection_Health(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("health check", func(mt *mtest.T) {
		cfg := &config.DatabaseConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "test",
		}
		log := &testLogger{}

		// Mock successful connection
		originalConnect := connectMongoDB
		originalPing := pingMongoDB
		defer func() {
			connectMongoDB = originalConnect
			pingMongoDB = originalPing
		}()

		connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
			return mt.Client, nil
		}
		pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
			return nil
		}

		conn, err := NewConnection(cfg, log)
		require.NoError(t, err)

		// Test health check
		err = conn.Health(context.Background())
		assert.NoError(t, err)
	})
}

func TestConnection_Stats(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("stats", func(mt *mtest.T) {
		cfg := &config.DatabaseConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "test",
		}
		log := &testLogger{}

		// Mock successful connection
		originalConnect := connectMongoDB
		originalPing := pingMongoDB
		defer func() {
			connectMongoDB = originalConnect
			pingMongoDB = originalPing
		}()

		connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
			return mt.Client, nil
		}
		pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
			return nil
		}

		conn, err := NewConnection(cfg, log)
		require.NoError(t, err)

		// Mock server status and database stats using helpers
		mt.AddMockResponses(MockServerStatus(1, 999))
		mt.AddMockResponses(MockDatabaseStats(1, 1024))

		stats, err := conn.Stats()
		assert.NoError(t, err)
		assert.NotNil(t, stats)
		assert.Equal(t, "mongodb", stats["database_type"])
	})
}

func TestNewConnection_ConfigValidation(t *testing.T) {
	log := &testLogger{}

	t.Run("connection string takes precedence", func(t *testing.T) {
		cfg := &config.DatabaseConfig{
			ConnectionString: "mongodb://custom:27017/customdb",
			Host:             "ignored",
			Port:             9999,
			Database:         "ignored",
		}

		// Mock connection failure to test URI was used
		originalConnect := connectMongoDB
		defer func() { connectMongoDB = originalConnect }()

		connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
			// We can't easily test the exact URI, but we can test that it gets passed through
			return nil, assert.AnError
		}

		_, err := NewConnection(cfg, log)
		assert.Error(t, err)
	})

	t.Run("invalid read preference", func(t *testing.T) {
		cfg := &config.DatabaseConfig{
			Host:           "localhost",
			Port:           27017,
			Database:       "test",
			ReadPreference: "invalid",
		}

		_, err := NewConnection(cfg, log)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid read preference")
	})

	t.Run("invalid write concern", func(t *testing.T) {
		cfg := &config.DatabaseConfig{
			Host:         "localhost",
			Port:         27017,
			Database:     "test",
			WriteConcern: "invalid",
		}

		_, err := NewConnection(cfg, log)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid write concern")
	})
}

func TestConnection_CreateMigrationTable(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("create migration table", func(mt *mtest.T) {
		cfg := &config.DatabaseConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "test",
		}
		log := &testLogger{}

		// Mock successful connection
		originalConnect := connectMongoDB
		originalPing := pingMongoDB
		defer func() {
			connectMongoDB = originalConnect
			pingMongoDB = originalPing
		}()

		connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
			return mt.Client, nil
		}
		pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
			return nil
		}

		conn, err := NewConnection(cfg, log)
		require.NoError(t, err)

		// Mock that collection doesn't exist using helper
		mt.AddMockResponses(MockEmptyCollectionList(TestDatabase()))

		// Mock successful collection and index creation using helpers
		mt.AddMockResponses(MockSuccessResponse())
		mt.AddMockResponses(MockSuccessResponse())

		err = conn.CreateMigrationTable(context.Background())
		assert.NoError(t, err)
	})

	mt.Run("migration table already exists", func(mt *mtest.T) {
		cfg := &config.DatabaseConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "test",
		}
		log := &testLogger{}

		// Mock successful connection
		originalConnect := connectMongoDB
		originalPing := pingMongoDB
		defer func() {
			connectMongoDB = originalConnect
			pingMongoDB = originalPing
		}()

		connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
			return mt.Client, nil
		}
		pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
			return nil
		}

		conn, err := NewConnection(cfg, log)
		require.NoError(t, err)

		// Mock that collection exists using helper
		mt.AddMockResponses(MockCollectionExists(TestDatabase(), "schema_migrations"))

		err = conn.CreateMigrationTable(context.Background())
		assert.NoError(t, err) // Should not error if already exists
	})
}
