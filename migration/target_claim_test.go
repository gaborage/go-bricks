package migration

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

func pgConfig(host string, port int, database, schema, username string) *config.DatabaseConfig {
	db := &config.DatabaseConfig{Type: config.PostgreSQL, Host: host, Port: port, Database: database, Username: username}
	db.PostgreSQL.Schema = schema
	return db
}

func oracleConfig(host string, port int, pdb, username string) *config.DatabaseConfig {
	return &config.DatabaseConfig{Type: config.Oracle, Host: host, Port: port, Database: pdb, Username: username}
}

func TestMigrationTargetForKeysTwoTenantsTogether(t *testing.T) {
	cases := []struct {
		name     string
		a, b     *config.DatabaseConfig
		fallback string
		same     bool
	}{
		{name: "pg_empty_schema_shared_user", a: pgConfig("db", 5432, "app", "", "migrator"), b: pgConfig("db", 5432, "app", "", "migrator"), same: true},
		{name: "pg_empty_schema_distinct_users", a: pgConfig("db", 5432, "app", "", "t1_migrator"), b: pgConfig("db", 5432, "app", "", "t2_migrator")},
		{name: "pg_explicit_schema_distinct_users", a: pgConfig("db", 5432, "app", "tenant", "u1"), b: pgConfig("db", 5432, "app", "tenant", "u2"), same: true},
		{name: "pg_distinct_schemas", a: pgConfig("db", 5432, "app", "t1", "u"), b: pgConfig("db", 5432, "app", "t2", "u")},
		{name: "pg_distinct_databases", a: pgConfig("db", 5432, "t1", "", "u"), b: pgConfig("db", 5432, "t2", "", "u")},
		{name: "pg_distinct_ports", a: pgConfig("db", 5432, "app", "", "u"), b: pgConfig("db", 5433, "app", "", "u")},
		{name: "pg_distinct_hosts", a: pgConfig("db1", 5432, "app", "", "u"), b: pgConfig("db2", 5432, "app", "", "u")},
		{name: "pg_host_case", a: pgConfig("DB.example", 5432, "app", "", "u"), b: pgConfig("db.example", 5432, "app", "", "u"), same: true},
		{name: "pg_ipv6_brackets", a: pgConfig("[::1]", 5432, "app", "", "u"), b: pgConfig("::1", 5432, "app", "", "u"), same: true},
		{name: "pg_port_zero_is_5432", a: pgConfig("db", 0, "app", "", "u"), b: pgConfig("db", 5432, "app", "", "u"), same: true},
		{name: "pg_username_case_distinct", a: pgConfig("db", 5432, "app", "", "Migrator"), b: pgConfig("db", 5432, "app", "", "migrator")},
		{name: "pg_database_case_distinct", a: pgConfig("db", 5432, "App", "", "u"), b: pgConfig("db", 5432, "app", "", "u")},
		{name: "pg_schema_case_distinct", a: pgConfig("db", 5432, "app", "Tenant", "u"), b: pgConfig("db", 5432, "app", "tenant", "u")},
		{name: "pg_empty_schema_never_matches_explicit", a: pgConfig("db", 5432, "app", "", "public"), b: pgConfig("db", 5432, "app", "public", "public")},
		{name: "oracle_same_user", a: oracleConfig("ora", 1521, "PDB1", "app"), b: oracleConfig("ORA", 1521, "PDB1", "app"), same: true},
		{name: "oracle_distinct_users", a: oracleConfig("ora", 1521, "PDB1", "t1"), b: oracleConfig("ora", 1521, "PDB1", "t2")},
		{name: "oracle_port_zero_not_defaulted", a: oracleConfig("ora", 0, "PDB1", "app"), b: oracleConfig("ora", 1521, "PDB1", "app")},
		{name: "vendor_from_fallback", a: &config.DatabaseConfig{Host: "db", Port: 5432, Database: "app", Username: "u"}, b: pgConfig("db", 5432, "app", "", "u"), fallback: config.PostgreSQL, same: true},
		{name: "vendors_differ", a: pgConfig("db", 1521, "app", "", "u"), b: oracleConfig("db", 1521, "app", "u")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ka, okA := migrationTargetFor(tc.a, tc.fallback)
			kb, okB := migrationTargetFor(tc.b, tc.fallback)
			require.True(t, okA, "a is keyed")
			require.True(t, okB, "b is keyed")
			assert.Equal(t, tc.same, ka == kb)
		})
	}
}

func TestMigrationTargetForLeavesConfOwnedTargetsUnkeyed(t *testing.T) {
	dsn := pgConfig("db", 5432, "app", "", "u")
	dsn.ConnectionString = "postgres://u@db:5432/app"
	cases := []struct {
		name     string
		db       *config.DatabaseConfig
		fallback string
	}{
		{name: "nil_config", db: nil, fallback: config.PostgreSQL},
		{name: "nil_config_oracle_fallback", db: nil, fallback: config.Oracle},
		{name: "pg_connection_string", db: dsn, fallback: config.PostgreSQL},
		{name: "pg_no_host", db: pgConfig("", 5432, "app", "", "u")},
		{name: "pg_no_database", db: pgConfig("db", 5432, "", "", "u")},
		{name: "oracle_no_host", db: oracleConfig("", 1521, "PDB1", "u")},
		{name: "oracle_no_pdb", db: oracleConfig("ora", 1521, "", "u")},
		{name: "type_less_without_fallback", db: &config.DatabaseConfig{Host: "db", Port: 5432, Database: "app", Username: "u"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := migrationTargetFor(tc.db, tc.fallback)
			assert.False(t, ok)
		})
	}
}

func TestTargetClaimsFirstClaimantWins(t *testing.T) {
	claims := newTargetClaims()
	key, ok := migrationTargetFor(pgConfig("db", 5432, "app", "", "u"), "")
	require.True(t, ok)
	other, ok := migrationTargetFor(pgConfig("db", 5432, "other", "", "u"), "")
	require.True(t, ok)

	owner, claimed := claims.claim(&key, "t1")
	assert.True(t, claimed)
	assert.Empty(t, owner)

	owner, claimed = claims.claim(&key, "t2")
	assert.False(t, claimed)
	assert.Equal(t, "t1", owner)

	owner, claimed = claims.claim(&key, "t1")
	assert.False(t, claimed, "a repeated tenant ID collides through the same key")
	assert.Equal(t, "t1", owner)

	_, claimed = claims.claim(&other, "t3")
	assert.True(t, claimed)
}

func TestTargetClaimsAdmitOneConcurrentClaimant(t *testing.T) {
	claims := newTargetClaims()
	key, ok := migrationTargetFor(pgConfig("db", 5432, "app", "", "u"), "")
	require.True(t, ok)

	const tenants = 32
	won := make(chan string, tenants)
	var wg sync.WaitGroup
	for i := range tenants {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, claimed := claims.claim(&key, id); claimed {
				won <- id
			}
		}(strconv.Itoa(i))
	}
	wg.Wait()
	close(won)
	assert.Len(t, won, 1)
}
