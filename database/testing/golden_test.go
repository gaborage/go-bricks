package testing

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestSQLGoldenRendersFixedClockAndMasksOthers pins the rule the golden files
// rely on: the fixture clock prints verbatim, any other time prints as a
// marker, bytes print as text, and pool/tx logs are grouped in order.
func TestSQLGoldenRendersFixedClockAndMasksOthers(t *testing.T) {
	fixed := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	g := SQLGolden{FixedClock: fixed}

	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec("").WillReturnRowsAffected(1)
	tx := db.ExpectTransaction()
	tx.ExpectQuery("").WillReturnRows(NewRowSet("c"))

	ctx := context.Background()
	_, err := db.Exec(ctx, "UPDATE t SET a = $1, b = $2, c = $3", fixed, time.Now(), []byte("x"))
	require.NoError(t, err)
	rows, err := tx.Query(ctx, "SELECT 1 FROM t WHERE p = $1", &fixed)
	require.NoError(t, err)
	defer rows.Close()

	got := g.Render(db, tx)
	assert.Equal(t, `# pool queries
# pool execs
EXEC: UPDATE t SET a = $1, b = $2, c = $3
  arg[0] time.Time = 2026-09-04T12:00:00Z
  arg[1] time.Time = <time>
  arg[2] []uint8 = x
# tx[0] queries
QUERY: SELECT 1 FROM t WHERE p = $1
  arg[0] *time.Time = 2026-09-04T12:00:00Z
# tx[0] execs
`, got)
}

// TestCompareWritesOnUpdateAndFailsOnDrift pins the two modes: -update writes
// the file, an identical rendering passes, and drift is reported naming the
// file — plus a missing golden, which must not read as a pass.
func TestCompareWritesOnUpdateAndFailsOnDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sql", "x.golden")

	require.Error(t, compareGolden(path, "one\n", false), "a missing golden is a failure, not a pass")

	require.NoError(t, compareGolden(path, "one\n", true))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "one\n", string(data))

	require.NoError(t, compareGolden(path, "one\n", false))
	err = compareGolden(path, "two\n", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "x.golden")

	Compare(t, path, "one\n", false) // the testing.T door on the passing path
}
