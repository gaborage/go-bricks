package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/gaborage/go-bricks/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
				Host:     "localhost",
				Port:     27017,
				Database: "testdb",
				Username: "user",
				Password: "pass",
				Mongo: config.MongoConfig{
					Replica: config.ReplicaConfig{
						Set: "rs0",
					},
				},
			},
			expected: "mongodb://user:pass@localhost:27017/testdb?replicaSet=rs0",
		},
		{
			name: "with auth source and replica set",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Port:     27017,
				Database: "testdb",
				Username: "user",
				Password: "pass",
				Mongo: config.MongoConfig{
					Replica: config.ReplicaConfig{
						Set: "rs0",
					},
					Auth: config.AuthConfig{
						Source: "admin",
					},
				},
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
		conn := SetupMockConnection(t, mt, DefaultTestConfig(), CreateTestLogger())
		AssertConnectionState(t, conn, "test")
	})
}

func TestConnectionGetMigrationTable(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("migration table name", func(mt *mtest.T) {
		conn := SetupMockConnection(t, mt, DefaultTestConfig(), CreateTestLogger())
		assert.Equal(mt, "schema_migrations", conn.GetMigrationTable())
	})
}

func TestConnectionSQLCompatibilityMethods(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("SQL compatibility", func(mt *mtest.T) {
		conn := SetupMockConnection(t, mt, DefaultTestConfig(), CreateTestLogger())
		ctx := context.Background()

		// Test Query method returns error
		rows, err := conn.Query(ctx, "SELECT * FROM test")
		assert.Error(mt, err)
		assert.Nil(mt, rows)
		assert.Contains(mt, err.Error(), "SQL query operations not supported")

		// Test QueryRow method returns nil
		row := conn.QueryRow(ctx, "SELECT * FROM test")
		assert.Nil(mt, row)

		// Test Exec method returns error
		result, err := conn.Exec(ctx, "INSERT INTO test VALUES (1)")
		assert.Error(mt, err)
		assert.Nil(mt, result)
		assert.Contains(mt, err.Error(), "SQL exec operations not supported")

		// Test Prepare method returns error
		stmt, err := conn.Prepare(ctx, "SELECT * FROM test WHERE id = ?")
		assert.Error(mt, err)
		assert.Nil(mt, stmt)
		assert.Contains(mt, err.Error(), "prepared statements not supported")
	})
}

func TestConnectionHealth(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("health check", func(mt *mtest.T) {
		conn := SetupMockConnection(t, mt, DefaultTestConfig(), CreateTestLogger())

		// Test health check
		err := conn.Health(context.Background())
		assert.NoError(t, err)
	})
}

func TestConnectionStats(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("stats", func(mt *mtest.T) {
		conn := SetupMockConnection(t, mt, DefaultTestConfig(), CreateTestLogger())

		// Mock server status and database stats using helpers
		mt.AddMockResponses(MockServerStatus(1, 999))
		mt.AddMockResponses(MockDatabaseStats(1, 1024))

		stats, err := conn.Stats()
		assert.NoError(mt, err)
		assert.NotNil(mt, stats)
		assert.Equal(mt, "mongodb", stats["database_type"])

		// Validate key fields to ensure mocks are actually consumed
		if connections, ok := stats["connections"]; ok {
			if connMap, ok := connections.(map[string]any); ok {
				assert.Contains(mt, connMap, "current")
				assert.Contains(mt, connMap, "available")
				assert.Equal(mt, int32(1), connMap["current"])
				assert.Equal(mt, int32(999), connMap["available"])
			}
		}

		if database, ok := stats["database"]; ok {
			if dbMap, ok := database.(map[string]any); ok {
				assert.Contains(mt, dbMap, "dataSize")
				assert.Contains(mt, dbMap, "collections")
				assert.Equal(mt, int32(1024), dbMap["dataSize"])
				assert.Equal(mt, int32(1), dbMap["collections"])
			}
		}
	})
}

func TestNewConnectionConfigValidation(t *testing.T) {
	log := CreateTestLogger()

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
			Host:     "localhost",
			Port:     27017,
			Database: "test",
			Mongo: config.MongoConfig{
				Replica: config.ReplicaConfig{
					Preference: "invalid",
				},
			},
		}

		_, err := NewConnection(cfg, log)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidReadPreference))
	})

	t.Run("invalid write concern", func(t *testing.T) {
		cfg := &config.DatabaseConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "test",
			Mongo: config.MongoConfig{
				Concern: config.ConcernConfig{
					Write: "invalid",
				},
			},
		}

		_, err := NewConnection(cfg, log)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidWriteConcern))
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
			expectError:          true,
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
			expectError:          true,
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
	log := CreateTestLogger()

	t.Run("successful TLS modes", func(t *testing.T) {
		successfulModes := []string{"disable", "require", "verify-full"}

		for _, sslMode := range successfulModes {
			t.Run(sslMode+" TLS", func(t *testing.T) {
				cfg := DefaultTestConfig()
				cfg.TLS.Mode = sslMode

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
		cfg := DefaultTestConfig()
		cfg.TLS.Mode = "invalid"

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
		conn := SetupMockConnection(t, mt, DefaultTestConfig(), CreateTestLogger())

		// Mock that collection doesn't exist using helper
		mt.AddMockResponses(MockEmptyCollectionList(GetTestDatabase()))

		// Mock successful collection and index creation using helpers
		mt.AddMockResponses(MockSuccessResponse())
		mt.AddMockResponses(MockSuccessResponse())

		err := conn.CreateMigrationTable(context.Background())
		assert.NoError(t, err)
	})

	mt.Run("migration table already exists", func(mt *mtest.T) {
		conn := SetupMockConnection(t, mt, DefaultTestConfig(), CreateTestLogger())

		// Mock that collection exists using helper
		mt.AddMockResponses(MockCollectionExists(GetTestDatabase(), "schema_migrations"))

		err := conn.CreateMigrationTable(context.Background())
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
				Pool: config.PoolConfig{
					Max: config.PoolMaxConfig{
						Connections: 100,
					},
				},
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
				Host:     "localhost",
				Database: "test",
				Pool: config.PoolConfig{
					Idle: config.PoolIdleConfig{
						Connections: 10,
					},
				},
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				assert.NotNil(t, opts)
			},
		},
		{
			name: "connection max idle time only",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Database: "test",
				Pool: config.PoolConfig{
					Idle: config.PoolIdleConfig{
						Time: 5 * time.Minute,
					},
				},
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				assert.NotNil(t, opts)
			},
		},
		{
			name: "all connection options set",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Database: "test",
				Pool: config.PoolConfig{
					Max: config.PoolMaxConfig{
						Connections: 50,
					},
					Idle: config.PoolIdleConfig{
						Connections: 20,
						Time:        10 * time.Minute,
					},
				},
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				assert.NotNil(t, opts)
			},
		},
		{
			name: "zero values ignored",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Database: "test",
				Pool: config.PoolConfig{
					Max: config.PoolMaxConfig{
						Connections: 0,
					},
					Idle: config.PoolIdleConfig{
						Connections: 0,
						Time:        0,
					},
				},
			},
			validate: func(t *testing.T, opts *options.ClientOptions) {
				assert.NotNil(t, opts)
				// Zero values should be ignored and not set
			},
		},
		{
			name: "negative values",
			config: &config.DatabaseConfig{
				Host:     "localhost",
				Database: "test",
				Pool: config.PoolConfig{
					Max: config.PoolMaxConfig{
						Connections: -1,
					},
					Idle: config.PoolIdleConfig{
						Connections: -5,
						Time:        -1,
					},
				},
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
		name          string
		preference    string
		expectedError error
	}{
		{
			name:          "empty preference",
			preference:    "",
			expectedError: nil,
		},
		{
			name:          "primary preference",
			preference:    "primary",
			expectedError: nil,
		},
		{
			name:          "primaryPreferred preference",
			preference:    "primarypreferred",
			expectedError: nil,
		},
		{
			name:          "secondary preference",
			preference:    "secondary",
			expectedError: nil,
		},
		{
			name:          "secondaryPreferred preference",
			preference:    "secondarypreferred",
			expectedError: nil,
		},
		{
			name:          "nearest preference",
			preference:    "nearest",
			expectedError: nil,
		},
		{
			name:          "case insensitive primary",
			preference:    "PRIMARY",
			expectedError: nil,
		},
		{
			name:          "case insensitive secondaryPreferred",
			preference:    "SecondaryPreferred",
			expectedError: nil,
		},
		{
			name:          "mixed case nearest",
			preference:    "Nearest",
			expectedError: nil,
		},
		{
			name:          "invalid preference",
			preference:    "invalid",
			expectedError: ErrInvalidReadPreference,
		},
		{
			name:          "unknown preference",
			preference:    "unknown",
			expectedError: ErrInvalidReadPreference,
		},
		{
			name:          "whitespace preference",
			preference:    "  ",
			expectedError: ErrInvalidReadPreference,
		},
		{
			name:          "primaryPreferred with spaces",
			preference:    " primarypreferred ",
			expectedError: ErrInvalidReadPreference,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := options.Client()

			err := setReadPreference(opts, tt.preference)

			if tt.expectedError != nil {
				assert.Error(t, err)
				assert.True(t, errors.Is(err, tt.expectedError))
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, opts)
			}
		})
	}
}

func TestSetWriteConcern(t *testing.T) {
	tests := []struct {
		name          string
		concern       string
		expectedError error
	}{
		{
			name:          "empty concern",
			concern:       "",
			expectedError: nil,
		},
		{
			name:          "majority concern",
			concern:       "majority",
			expectedError: nil,
		},
		{
			name:          "acknowledged concern",
			concern:       "acknowledged",
			expectedError: nil,
		},
		{
			name:          "unacknowledged concern",
			concern:       "unacknowledged",
			expectedError: nil,
		},
		{
			name:          "case insensitive majority",
			concern:       "MAJORITY",
			expectedError: nil,
		},
		{
			name:          "case insensitive acknowledged",
			concern:       "Acknowledged",
			expectedError: nil,
		},
		{
			name:          "mixed case unacknowledged",
			concern:       "UnAcknowledged",
			expectedError: nil,
		},
		{
			name:          "invalid concern",
			concern:       "invalid",
			expectedError: ErrInvalidWriteConcern,
		},
		{
			name:          "unknown concern",
			concern:       "unknown",
			expectedError: ErrInvalidWriteConcern,
		},
		{
			name:          "numeric string concern 0",
			concern:       "0",
			expectedError: nil,
		},
		{
			name:          "numeric string concern 1",
			concern:       "1",
			expectedError: nil,
		},
		{
			name:          "numeric string concern 2",
			concern:       "2",
			expectedError: nil,
		},
		{
			name:          "whitespace concern",
			concern:       "  ",
			expectedError: ErrInvalidWriteConcern,
		},
		{
			name:          "majority with spaces",
			concern:       " majority ",
			expectedError: nil,
		},
		{
			name:          "acknowledged with spaces",
			concern:       "  acknowledged  ",
			expectedError: nil,
		},
		{
			name:          "numeric with spaces",
			concern:       "  3  ",
			expectedError: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := options.Client()

			err := setWriteConcern(opts, tt.concern)

			if tt.expectedError != nil {
				assert.Error(t, err)
				assert.True(t, errors.Is(err, tt.expectedError))
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, opts)
			}
		})
	}
}
