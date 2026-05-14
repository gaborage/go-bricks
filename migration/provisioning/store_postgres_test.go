package provisioning

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver registration for stubDB
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPostgresStoreRejectsNilDB(t *testing.T) {
	_, err := NewPostgresStore(nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil *sql.DB")
}

func TestNewPostgresStoreDefaultsTableName(t *testing.T) {
	db := stubDB(t)
	s, err := NewPostgresStore(db, "")
	require.NoError(t, err)
	assert.Equal(t, DefaultPostgresTable, s.tableName)
	assert.Equal(t, `"provisioning_jobs"`, s.quotedTable)
}

func TestNewPostgresStoreAcceptsValidNames(t *testing.T) {
	db := stubDB(t)
	cases := []struct {
		in         string
		wantQuoted string
		wantBase   string
	}{
		{"provisioning_jobs", `"provisioning_jobs"`, "provisioning_jobs"},
		{"tenant_provisioning", `"tenant_provisioning"`, "tenant_provisioning"},
		{"_internal_jobs", `"_internal_jobs"`, "_internal_jobs"},
		// Schema-qualified form is permitted.
		{"app.provisioning_jobs", `"app"."provisioning_jobs"`, "provisioning_jobs"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			s, err := NewPostgresStore(db, c.in)
			require.NoError(t, err)
			assert.Equal(t, c.wantQuoted, s.quotedTable)
			assert.Equal(t, c.wantBase, s.indexBase)
		})
	}
}

func TestNewPostgresStoreRejectsInvalidNames(t *testing.T) {
	db := stubDB(t)
	invalid := []string{
		"1starts_with_digit",
		"has-hyphen",
		"has space",
		"semicolon;DROP",
		`embedded"quote`,
		"too.many.dots.here",
		// Length cap (NAMEDATALEN-1 per segment).
		strings.Repeat("a", 64),
	}
	for _, name := range invalid {
		name := name
		t.Run(name, func(t *testing.T) {
			_, err := NewPostgresStore(db, name)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidTableName))
		})
	}
}

func TestEncodeMetadata(t *testing.T) {
	// nil in, nil out — preserves the COALESCE behavior in Transition.
	b, err := encodeMetadata(nil)
	require.NoError(t, err)
	assert.Nil(t, b)

	b, err = encodeMetadata(map[string]string{"k": "v", "n": "1"})
	require.NoError(t, err)
	// JSON encoding is deterministic for string-keyed maps under Go's
	// sort-by-key contract, so we can substring-match safely.
	got := string(b)
	assert.Contains(t, got, `"k":"v"`)
	assert.Contains(t, got, `"n":"1"`)
}

func TestQuoteQualifiedAndIndexBase(t *testing.T) {
	assert.Equal(t, `"foo"`, quoteQualified("foo"))
	assert.Equal(t, `"a"."b"`, quoteQualified("a.b"))
	assert.Equal(t, "foo", indexBaseName("foo"))
	assert.Equal(t, "b", indexBaseName("a.b"))
}

func TestPostgresStateTableDDLHasExpectedColumns(t *testing.T) {
	// The DDL constant is operator-facing — exported for inclusion in
	// external migration tooling. Lock the contract so future renames
	// surface here rather than in production.
	required := []string{
		"id",
		"tenant_id",
		"state",
		"attempts",
		"last_error",
		"metadata    JSONB",
		"created_at",
		"updated_at",
	}
	for _, col := range required {
		assert.Contains(t, PostgresStateTableDDL, col, "DDL missing %q", col)
	}
	assert.Contains(t, PostgresStateTableDDL, "CREATE TABLE IF NOT EXISTS",
		"DDL must be idempotent")
}

// stubDB returns a non-nil *sql.DB that hasn't dialed any driver. NewPostgresStore
// doesn't connect, so this is enough to exercise validation paths without
// requiring a live database.
func stubDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://stub:stub@127.0.0.1:1/stubdb?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
