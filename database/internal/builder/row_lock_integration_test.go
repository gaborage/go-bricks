//go:build integration

package builder

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/testing/containers"
)

// TestForUpdateSkipLockedClaimsNextRowPostgresIntegration runs the queue-claim
// shape against real PostgreSQL: while one transaction holds row 1, a second
// transaction issuing the same builder query gets row 2 at once instead of
// blocking on row 1 or returning it.
func TestForUpdateSkipLockedClaimsNextRowPostgresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	pg := containers.MustStartPostgreSQLContainer(ctx, t, nil).WithCleanup(t)

	connect := func() *pgx.Conn {
		conn, err := pgx.Connect(ctx, pg.ConnectionString())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}
	first, second := connect(), connect()

	_, err := first.Exec(ctx, "CREATE TABLE skip_locked_jobs (id INT PRIMARY KEY)")
	require.NoError(t, err)
	_, err = first.Exec(ctx, "INSERT INTO skip_locked_jobs (id) VALUES (1), (2), (3)")
	require.NoError(t, err)

	claimSQL, claimArgs, err := NewQueryBuilder(dbtypes.PostgreSQL).
		Select("id").From("skip_locked_jobs").OrderBy("id").Limit(1).ForUpdateSkipLocked().ToSQL()
	require.NoError(t, err)

	tx1, err := first.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx1.Rollback(context.Background()) })
	var claimed1 int
	require.NoError(t, tx1.QueryRow(ctx, claimSQL, claimArgs...).Scan(&claimed1))
	require.Equal(t, 1, claimed1)

	tx2, err := second.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx2.Rollback(context.Background()) })
	_, err = tx2.Exec(ctx, "SET LOCAL lock_timeout = '2s'")
	require.NoError(t, err)
	var claimed2 int
	require.NoError(t, tx2.QueryRow(ctx, claimSQL, claimArgs...).Scan(&claimed2),
		"the second claim must skip the locked row rather than wait on it")
	require.Equal(t, 2, claimed2)
}
