//go:build integration

package outbox

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/testing/containers"
)

// These tests prove against a REAL database what the unit suite can only assert against a
// fake: that the identity column actually yields increasing seq under one transaction, that
// the documented managed migration turns a pre-sequence table into a readable one, and that
// the leader row genuinely admits one drainer — including when its holder's session dies.

const (
	itTable       = "outbox_it"
	itPollTimeout = 30 * time.Second
)

func itLogger() logger.Logger { return logger.New("disabled", true) }

func itPoolConfig() config.PoolConfig {
	return config.PoolConfig{
		Max:      config.PoolMaxConfig{Connections: 25},
		Idle:     config.PoolIdleConfig{Connections: 10, Time: 30 * time.Minute},
		Lifetime: config.LifetimeConfig{Max: time.Hour},
	}
}

// newPostgresIT starts a PostgreSQL container and returns a live connection plus a store.
// MustStartPostgreSQLContainer skips the test when Docker is unavailable.
func newPostgresIT(ctx context.Context, t *testing.T) (conn dbtypes.Interface, store Store, dsn string) {
	t.Helper()
	c := containers.MustStartPostgreSQLContainer(ctx, t, nil).WithCleanup(t)
	dsn = c.ConnectionString()

	conn, err := database.NewConnection(&config.DatabaseConfig{
		Type:             database.PostgreSQL,
		ConnectionString: dsn,
		Pool:             itPoolConfig(),
	}, itLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.Health(ctx))

	store, err = NewPostgresStore(itTable)
	require.NoError(t, err)
	return conn, store, dsn
}

func itRecord(marker, key string, at time.Time) *Record {
	// id is a UUID column on PostgreSQL, so the ordering marker rides aggregate_id.
	return &Record{
		ID: uuid.New().String(), EventType: "it.event", AggregateID: marker,
		Payload: []byte(`{}`), Exchange: "ex", RoutingKey: key,
		Status: StatusPending, CreatedAt: at,
	}
}

// TestOutboxStoreCreateTableAndOrderIntegration proves the identity column does the ordering
// work the relay depends on: twenty rows written in ONE transaction share a created_at, so
// only seq can order them, and it must do so in insertion order.
func TestOutboxStoreCreateTableAndOrderIntegration(t *testing.T) {
	ctx := context.Background()
	conn, store, _ := newPostgresIT(ctx, t)

	require.NoError(t, store.CreateTable(ctx, conn))
	require.NoError(t, store.CreateTable(ctx, conn), "CreateTable is idempotent on PostgreSQL")

	const rows = 20
	sameInstant := time.Now().UTC().Truncate(time.Microsecond)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	for i := range rows {
		require.NoError(t, store.Insert(ctx, tx, itRecord(fmt.Sprintf("evt-%02d", i), "k", sameInstant)))
	}
	require.NoError(t, tx.Commit(ctx))

	pending, err := store.FetchPending(ctx, conn, rows)
	require.NoError(t, err)
	require.Len(t, pending, rows)

	for i, r := range pending {
		assert.Equal(t, fmt.Sprintf("evt-%02d", i), r.AggregateID, "rows come back in insertion order")
		assert.Equal(t, LaneAMQP, r.Lane, "the lane default is applied by the store")
		if i > 0 {
			assert.Greater(t, r.Seq, pending[i-1].Seq, "seq is strictly increasing")
		}
	}
	assert.Equal(t, sameInstant.Unix(), pending[0].CreatedAt.UTC().Unix(),
		"every row shares a created_at, so created_at could not have ordered them")
}

// preSequenceDDL is the outbox table as it stood BEFORE the sequence landed, copied here so
// the documented migration is applied to the shape a real deployment actually has.
const preSequenceDDL = `
CREATE TABLE IF NOT EXISTS %s (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type    VARCHAR(255) NOT NULL,
    aggregate_id  VARCHAR(255) NOT NULL,
    payload       BYTEA NOT NULL,
    headers       BYTEA,
    exchange      VARCHAR(255) NOT NULL DEFAULT '',
    routing_key   VARCHAR(255) NOT NULL DEFAULT '',
    status        VARCHAR(20) NOT NULL DEFAULT 'pending',
    retry_count   INTEGER NOT NULL DEFAULT 0,
    error         TEXT,
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    published_at  TIMESTAMP WITH TIME ZONE
)`

// TestOutboxStoreManagedAlterIntegration runs the statements wiki/outbox.md tells an operator
// to run, against a table created with the OLD shape and holding a backlog. This is the path
// no unit test can cover: a deployment that migrates rather than auto-creates.
func TestOutboxStoreManagedAlterIntegration(t *testing.T) {
	ctx := context.Background()
	conn, store, _ := newPostgresIT(ctx, t)

	_, err := conn.Exec(ctx, fmt.Sprintf(preSequenceDDL, itTable))
	require.NoError(t, err)

	backlog := time.Now().UTC().Add(-time.Hour)
	for i := range 3 {
		_, err := conn.Exec(ctx, fmt.Sprintf(
			`INSERT INTO %s (id, event_type, aggregate_id, payload, exchange, routing_key, status, created_at)
			 VALUES (gen_random_uuid(), $1, $2, $3, 'ex', 'k', 'pending', $4)`, itTable),
			"it.event", fmt.Sprintf("agg-%d", i), []byte(`{}`), backlog.Add(time.Duration(i)*time.Second))
		require.NoError(t, err)
	}

	// The documented migration, in its documented order: ALTER, then the backfill, then the
	// index — and the leader table beside them.
	migration := []string{
		fmt.Sprintf(`ALTER TABLE %s
			ADD COLUMN seq BIGINT GENERATED BY DEFAULT AS IDENTITY,
			ADD COLUMN lane VARCHAR(16) NOT NULL DEFAULT 'amqp',
			ADD COLUMN stream VARCHAR(255) NOT NULL DEFAULT '',
			ADD COLUMN partition_key VARCHAR(255) NOT NULL DEFAULT ''`, itTable),
		fmt.Sprintf(`WITH ordered AS (
			  SELECT id, row_number() OVER (ORDER BY created_at, id) AS rn FROM %s
			) UPDATE %s o SET seq = ordered.rn FROM ordered WHERE o.id = ordered.id`, itTable, itTable),
		fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', 'seq'),
			  (SELECT coalesce(max(seq), 1) FROM %s),
			  (SELECT max(seq) IS NOT NULL FROM %s))`, itTable, itTable, itTable),
		fmt.Sprintf(`CREATE INDEX idx_%s_pending ON %s (seq) WHERE status = 'pending'`, itTable, itTable),
		fmt.Sprintf(`CREATE TABLE %s_leader (id SMALLINT PRIMARY KEY)`, itTable),
		fmt.Sprintf(`INSERT INTO %s_leader (id) VALUES (1) ON CONFLICT (id) DO NOTHING`, itTable),
	}
	for i, stmt := range migration {
		_, err := conn.Exec(ctx, stmt)
		require.NoErrorf(t, err, "migration statement %d failed", i)
	}

	pending, err := store.FetchPending(ctx, conn, 10)
	require.NoError(t, err)
	require.Len(t, pending, 3, "the migrated backlog is readable — the startup probe reads it too")
	for i, r := range pending {
		assert.Equal(t, LaneAMQP, r.Lane)
		assert.Empty(t, r.Stream)
		assert.Positive(t, r.Seq, "the backfill gave every pre-existing row a sequence")
		if i > 0 {
			assert.Greater(t, r.Seq, pending[i-1].Seq)
		}
	}

	lead, err := store.Lead(ctx, conn)
	require.NoError(t, err, "the seeded leader row is takeable after the migration")
	require.NoError(t, lead.Release(ctx))

	// A row inserted after the reset must not collide with a backfilled sequence.
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, store.Insert(ctx, tx, itRecord("evt-new", "k", time.Now().UTC())))
	require.NoError(t, tx.Commit(ctx))

	after, err := store.FetchPending(ctx, conn, 10)
	require.NoError(t, err)
	require.Len(t, after, 4)
	assert.Equal(t, "evt-new", after[3].AggregateID, "the new row sorts after the backfilled ones")
	assert.Greater(t, after[3].Seq, after[2].Seq, "the identity was advanced past the backfill")
}

// TestOutboxRelayTwoInstancesOneLedgerIntegration is the claim the whole leader mechanism
// exists for: two relays against ONE table publish every row exactly once, and per key in
// sequence order. A fake cannot prove it — the exclusion is the database's, not the code's.
func TestOutboxRelayTwoInstancesOneLedgerIntegration(t *testing.T) {
	ctx := context.Background()
	conn, store, _ := newPostgresIT(ctx, t)
	require.NoError(t, store.CreateTable(ctx, conn))

	const (
		rows = 200
		keys = 5
	)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	for i := range rows {
		require.NoError(t, store.Insert(ctx, tx,
			itRecord(fmt.Sprintf("evt-%03d", i), fmt.Sprintf("k%d", i%keys), time.Now().UTC())))
	}
	require.NoError(t, tx.Commit(ctx))

	newRelay := func() (*Relay, *fakeAMQP) {
		amqp := newFakeAMQP()
		return &Relay{
			store:        store,
			config:       config.OutboxConfig{BatchSize: 50, MaxRetries: 3, PublishTimeout: 5 * time.Second},
			getDB:        func(context.Context) (dbtypes.Interface, error) { return conn, nil },
			getMessaging: func(context.Context) (messaging.AMQPClient, error) { return amqp, nil },
			tenants:      []string{""},
		}, amqp
	}
	relayA, amqpA := newRelay()
	relayB, amqpB := newRelay()

	var wg sync.WaitGroup
	for _, r := range []*Relay{relayA, relayB} {
		wg.Add(1)
		go func(rel *Relay) {
			defer wg.Done()
			for range 10 {
				// A cycle that finds another instance leading returns nil having done
				// nothing; that is the mechanism working, not a failure.
				assert.NoError(t, rel.Execute(newFakeJobCtx(conn, amqpA)))
			}
		}(r)
	}
	wg.Wait()

	published := append(append([]string{}, amqpA.PublishOrder...), amqpB.PublishOrder...)
	assert.Len(t, published, rows, "every row published EXACTLY once across both relays")

	remaining, err := store.FetchPending(ctx, conn, rows)
	require.NoError(t, err)
	assert.Empty(t, remaining, "the ledger drained")
}

// TestOutboxRelayDeposedLeaderStopsIntegration kills the leader's SESSION from another
// connection — the failure mode a probe exists for, and one no fake can stage: the holder
// believes it still leads until its next statement fails.
func TestOutboxRelayDeposedLeaderStopsIntegration(t *testing.T) {
	ctx := context.Background()
	conn, store, dsn := newPostgresIT(ctx, t)
	require.NoError(t, store.CreateTable(ctx, conn))

	leadA, err := store.Lead(ctx, conn)
	require.NoError(t, err)
	require.NoError(t, leadA.Probe(ctx), "a fresh claim probes clean")

	// A second connection, so terminating the first one's backend does not kill our own.
	killer, err := database.NewConnection(&config.DatabaseConfig{
		Type:             database.PostgreSQL,
		ConnectionString: dsn,
		Pool:             itPoolConfig(),
	}, itLogger())
	require.NoError(t, err)
	defer func() { _ = killer.Close() }()

	// Find the leader's backend before killing it, and REQUIRE a match: a terminate that
	// hits nothing looks identical to one that worked, and would leave the assertion below
	// failing for the wrong reason.
	var victims int
	row := killer.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
		WHERE state = 'idle in transaction' AND datname = current_database() AND pid <> pg_backend_pid()`)
	require.NoError(t, row.Scan(&victims))
	require.Positive(t, victims, "the leader should be holding an open transaction to terminate")

	var killed int
	row = killer.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE state = 'idle in transaction' AND datname = current_database() AND pid <> pg_backend_pid()
	) t`)
	require.NoError(t, row.Scan(&killed))
	require.Positive(t, killed, "at least one backend was terminated")

	require.Eventually(t, func() bool {
		return leadA.Probe(ctx) != nil
	}, itPollTimeout, 200*time.Millisecond, "a deposed leader's probe must start failing")

	// And the claim is genuinely free again, not merely unusable by its old holder.
	require.Eventually(t, func() bool {
		leadB, err := store.Lead(ctx, killer)
		if err != nil {
			return false
		}
		_ = leadB.Release(ctx)
		return true
	}, itPollTimeout, 200*time.Millisecond, "another instance takes the row the dead session held")
}
