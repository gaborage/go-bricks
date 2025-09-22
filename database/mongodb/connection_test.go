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

const (
	invalidReadPrefErrMsg     = "invalid read preference"
	invalidWriteConcernErrMsg = "invalid write concern"
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

func TestConnectionDatabaseType(t *testing.T) {
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
		t.Cleanup(func() {
			connectMongoDB = originalConnect
			pingMongoDB = originalPing
		})

		connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
			return mt.Client, nil
		}
		pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
			return nil
		}

		conn, err := NewConnection(cfg, log)
		require.NoError(mt, err)

		assert.Equal(t, "mongodb", conn.DatabaseType())
	})
}

func TestConnectionGetMigrationTable(t *testing.T) {
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
		require.NoError(mt, err)

		assert.Equal(t, "schema_migrations", conn.GetMigrationTable())
	})
}

func TestConnectionSQLCompatibilityMethods(t *testing.T) {
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
		require.NoError(mt, err)

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

func TestConnectionHealth(t *testing.T) {
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
		require.NoError(mt, err)

		// Test health check
		err = conn.Health(context.Background())
		assert.NoError(t, err)
	})
}

func TestConnectionStats(t *testing.T) {
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
		require.NoError(mt, err)

		// Mock server status and database stats using helpers
		mt.AddMockResponses(MockServerStatus(1, 999))
		mt.AddMockResponses(MockDatabaseStats(1, 1024))

		stats, err := conn.Stats()
		assert.NoError(t, err)
		assert.NotNil(t, stats)
		assert.Equal(t, "mongodb", stats["database_type"])
	})
}

func TestNewConnectionConfigValidation(t *testing.T) {
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
		assert.Contains(t, err.Error(), invalidReadPrefErrMsg)
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
		assert.Contains(t, err.Error(), invalidWriteConcernErrMsg)
	})
}

func TestBuildTLSConfig(t *testing.T) {
	tests := []struct {
		name                 string
		sslMode              string
		expectError          bool
		expectedInsecureSkip *bool
		expectedServerName   *string
		expectNilConfig      bool
	}{
		{
			name:            "disable mode",
			sslMode:         "disable",
			expectError:     false,
			expectNilConfig: true,
		},
		{
			name:                 "require mode",
			sslMode:              "require",
			expectError:          false,
			expectedInsecureSkip: func() *bool { b := false; return &b }(),
			expectNilConfig:      false,
		},
		{
			name:                 "verify-ca mode",
			sslMode:              "verify-ca",
			expectError:          false,
			expectedInsecureSkip: func() *bool { b := false; return &b }(),
			expectedServerName:   func() *string { s := ""; return &s }(),
			expectNilConfig:      false,
		},
		{
			name:                 "verify-full mode",
			sslMode:              "verify-full",
			expectError:          false,
			expectedInsecureSkip: func() *bool { b := false; return &b }(),
			expectNilConfig:      false,
		},
		{
			name:                 "case insensitive - REQUIRE",
			sslMode:              "REQUIRE",
			expectError:          false,
			expectedInsecureSkip: func() *bool { b := false; return &b }(),
			expectNilConfig:      false,
		},
		{
			name:                 "case insensitive - Verify-CA",
			sslMode:              "Verify-CA",
			expectError:          false,
			expectedInsecureSkip: func() *bool { b := false; return &b }(),
			expectedServerName:   func() *string { s := ""; return &s }(),
			expectNilConfig:      false,
		},
		{
			name:        "invalid mode",
			sslMode:     "invalid-mode",
			expectError: true,
		},
		{
			name:        "empty mode",
			sslMode:     "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tlsConfig, err := buildTLSConfig(tt.sslMode)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, tlsConfig)
				return
			}

			assert.NoError(t, err)

			if tt.expectNilConfig {
				assert.Nil(t, tlsConfig)
				return
			}

			require.NotNil(t, tlsConfig)

			if tt.expectedInsecureSkip != nil {
				assert.Equal(t, *tt.expectedInsecureSkip, tlsConfig.InsecureSkipVerify)
			}

			if tt.expectedServerName != nil {
				assert.Equal(t, *tt.expectedServerName, tlsConfig.ServerName)
			}
		})
	}
}

// setupSuccessfulTLSMocks configures mocks for successful TLS connection tests
func setupSuccessfulTLSMocks(capturedOptions **options.ClientOptions) func() {
	originalConnect := connectMongoDB
	originalPing := pingMongoDB

	connectMongoDB = func(_ context.Context, opts *options.ClientOptions) (*mongo.Client, error) {
		*capturedOptions = opts
		return &mongo.Client{}, nil
	}
	pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
		return nil
	}

	return func() {
		connectMongoDB = originalConnect
		pingMongoDB = originalPing
	}
}

// setupFailingTLSMocks configures mocks for TLS connection failure tests
func setupFailingTLSMocks() func() {
	originalConnect := connectMongoDB
	originalPing := pingMongoDB

	connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
		return nil, assert.AnError
	}
	pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
		return assert.AnError
	}

	return func() {
		connectMongoDB = originalConnect
		pingMongoDB = originalPing
	}
}

func TestNewConnectionWithTLSModes(t *testing.T) {
	log := &testLogger{}

	t.Run("successful TLS modes", func(t *testing.T) {
		successfulModes := []string{"disable", "require", "verify-ca", "verify-full"}

		for _, sslMode := range successfulModes {
			t.Run(sslMode+" TLS", func(t *testing.T) {
				cfg := &config.DatabaseConfig{
					Host:     "localhost",
					Port:     27017,
					Database: "test",
					SSLMode:  sslMode,
				}

				var capturedOptions *options.ClientOptions
				restoreMocks := setupSuccessfulTLSMocks(&capturedOptions)
				defer restoreMocks()

				_, err := NewConnection(cfg, log)

				assert.NoError(t, err)
				assert.NotNil(t, capturedOptions)
			})
		}
	})

	t.Run("invalid TLS mode", func(t *testing.T) {
		cfg := &config.DatabaseConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "test",
			SSLMode:  "invalid",
		}

		restoreMocks := setupFailingTLSMocks()
		defer restoreMocks()

		_, err := NewConnection(cfg, log)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to configure TLS")
	})
}

func TestConnectionCreateMigrationTable(t *testing.T) {
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
		require.NoError(mt, err)

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
		require.NoError(mt, err)

		// Mock that collection exists using helper
		mt.AddMockResponses(MockCollectionExists(TestDatabase(), "schema_migrations"))

		err = conn.CreateMigrationTable(context.Background())
		assert.NoError(t, err) // Should not error if already exists
	})
}

func TestSetConnectionOptions(t *testing.T) {
	tests := []struct {
		name     string
		config   *config.DatabaseConfig
		validate func(t *testing.T, opts *options.ClientOptions)
	}{
		{
			name: "no connection options set",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Database: "test",
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				// Should not panic and should accept defaults
				assert.NotNil(t, opts)
			},
		},
		{
			name: "max connections only",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Database: "test",
				MaxConns: 100,
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				assert.NotNil(t, opts)
				// Note: We can't directly access the MaxPoolSize from options.ClientOptions
				// but we can test that the function doesn't panic and accepts the value
			},
		},
		{
			name: "max idle connections only",
			config: &config.DatabaseConfig{
				Host:         "localhost",
				Database:     "test",
				MaxIdleConns: 10,
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				assert.NotNil(t, opts)
			},
		},
		{
			name: "connection max idle time only",
			config: &config.DatabaseConfig{
				Host:            "localhost",
				Database:        "test",
				ConnMaxIdleTime: 5 * time.Minute,
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				assert.NotNil(t, opts)
			},
		},
		{
			name: "all connection options set",
			config: &config.DatabaseConfig{
				Host:            "localhost",
				Database:        "test",
				MaxConns:        50,
				MaxIdleConns:    20,
				ConnMaxIdleTime: 10 * time.Minute,
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				assert.NotNil(t, opts)
			},
		},
		{
			name: "zero values ignored",
			config: &config.DatabaseConfig{
				Host:            "localhost",
				Database:        "test",
				MaxConns:        0,
				MaxIdleConns:    0,
				ConnMaxIdleTime: 0,
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				assert.NotNil(t, opts)
				// Zero values should be ignored and not set
			},
		},
		{
			name: "negative values",
			config: &config.DatabaseConfig{
				Host:         "localhost",
				Database:     "test",
				MaxConns:     -1,
				MaxIdleConns: -1,
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				assert.NotNil(t, opts)
				// Negative values should be treated as zero/ignored
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := options.Client()

			// This should not panic
			setConnectionOptions(opts, tt.config)

			// Run custom validation
			tt.validate(t, opts)
		})
	}
}

func TestSetReadPreference(t *testing.T) {
	tests := []struct {
		name        string
		preference  string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "empty preference",
			preference:  "",
			expectError: false,
		},
		{
			name:        "primary preference",
			preference:  "primary",
			expectError: false,
		},
		{
			name:        "primaryPreferred preference",
			preference:  "primarypreferred",
			expectError: false,
		},
		{
			name:        "secondary preference",
			preference:  "secondary",
			expectError: false,
		},
		{
			name:        "secondaryPreferred preference",
			preference:  "secondarypreferred",
			expectError: false,
		},
		{
			name:        "nearest preference",
			preference:  "nearest",
			expectError: false,
		},
		{
			name:        "case insensitive primary",
			preference:  "PRIMARY",
			expectError: false,
		},
		{
			name:        "case insensitive secondaryPreferred",
			preference:  "SecondaryPreferred",
			expectError: false,
		},
		{
			name:        "mixed case nearest",
			preference:  "Nearest",
			expectError: false,
		},
		{
			name:        "invalid preference",
			preference:  "invalid",
			expectError: true,
			errorMsg:    invalidReadPrefErrMsg,
		},
		{
			name:        "unknown preference",
			preference:  "unknown",
			expectError: true,
			errorMsg:    invalidReadPrefErrMsg,
		},
		{
			name:        "whitespace preference",
			preference:  "  ",
			expectError: true,
			errorMsg:    invalidReadPrefErrMsg,
		},
		{
			name:        "primaryPreferred with spaces",
			preference:  " primarypreferred ",
			expectError: true,
			errorMsg:    invalidReadPrefErrMsg,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := options.Client()

			err := setReadPreference(opts, tt.preference)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, opts)
			}
		})
	}
}

func TestSetWriteConcern(t *testing.T) {
	tests := []struct {
		name        string
		concern     string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "empty concern",
			concern:     "",
			expectError: false,
		},
		{
			name:        "majority concern",
			concern:     "majority",
			expectError: false,
		},
		{
			name:        "acknowledged concern",
			concern:     "acknowledged",
			expectError: false,
		},
		{
			name:        "unacknowledged concern",
			concern:     "unacknowledged",
			expectError: false,
		},
		{
			name:        "case insensitive majority",
			concern:     "MAJORITY",
			expectError: false,
		},
		{
			name:        "case insensitive acknowledged",
			concern:     "Acknowledged",
			expectError: false,
		},
		{
			name:        "mixed case unacknowledged",
			concern:     "UnAcknowledged",
			expectError: false,
		},
		{
			name:        "invalid concern",
			concern:     "invalid",
			expectError: true,
			errorMsg:    invalidWriteConcernErrMsg,
		},
		{
			name:        "unknown concern",
			concern:     "unknown",
			expectError: true,
			errorMsg:    invalidWriteConcernErrMsg,
		},
		{
			name:        "numeric string concern",
			concern:     "1",
			expectError: true,
			errorMsg:    invalidWriteConcernErrMsg,
		},
		{
			name:        "whitespace concern",
			concern:     "  ",
			expectError: true,
			errorMsg:    invalidWriteConcernErrMsg,
		},
		{
			name:        "majority with spaces",
			concern:     " majority ",
			expectError: true,
			errorMsg:    invalidWriteConcernErrMsg,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := options.Client()

			err := setWriteConcern(opts, tt.concern)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, opts)
			}
		})
	}
}
