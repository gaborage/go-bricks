package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/sqlid"
)

// sqlStore is the DML shared by both vendors, built with the query builder so
// the vendor decides placeholders, quoting and LIMIT spelling. What still
// differs between the vendors is small and named: the error column (Oracle
// reserves ERROR), the row scanner (Oracle stores an empty string as NULL) and the DDL,
// which the vendor types keep beside their CreateTable.
type sqlStore struct {
	vendor      string // the label every error carries: "postgres" or "oracle"
	tableName   string
	leaderTable string
	errorColumn string
	qb          *database.QueryBuilder
	scanRecord  func(rows *sql.Rows) (Record, error)
}

// pendingColumns is the projection FetchPending reads, in scan order.
var pendingColumns = []string{
	"id", "event_type", "aggregate_id", "payload", "headers",
	"exchange", "routing_key", "lane", "stream", "partition_key",
	"status", "retry_count", "created_at", "seq",
}

func newSQLStore(vendor dbtypes.Vendor, label, tableName, errorColumn string, scan func(*sql.Rows) (Record, error)) sqlStore {
	return sqlStore{
		vendor:      label,
		tableName:   tableName,
		leaderTable: sqlid.LeaderTableName(tableName),
		errorColumn: errorColumn,
		qb:          database.NewQueryBuilder(vendor),
		scanRecord:  scan,
	}
}

// op labels an operation with the prefix the hand-written stores used —
// "outbox postgres: insert failed" — so the ExecError the helpers return reads
// "outbox postgres: insert failed: exec: …": the old text survives as a prefix
// and the helper's stage is the one addition.
func (s *sqlStore) op(name string) string {
	return "outbox " + s.vendor + ": " + name + " failed"
}

func (s *sqlStore) Insert(ctx context.Context, tx dbtypes.Tx, record *Record) error {
	return database.ExecuteInsert(ctx, tx, s.qb.Insert(s.tableName).
		Columns("id", "event_type", "aggregate_id", "payload", "headers", "exchange", "routing_key",
			"lane", "stream", "partition_key", "status", "created_at").
		Values(record.ID, record.EventType, record.AggregateID, record.Payload, record.Headers,
			record.Exchange, record.RoutingKey, laneOrDefault(record.Lane), record.Stream,
			record.PartitionKey, record.Status, record.CreatedAt),
		s.op("insert"))
}

// FetchPending returns up to batchSize pending events, oldest first. Selection is
// status-gated only (no retry_count filter) so an outage-inflated count cannot
// freeze a healthy event. The batch size renders as a literal LIMIT / FETCH
// NEXT, where the hand-written statements bound it — same plan, one argument
// fewer.
func (s *sqlStore) FetchPending(ctx context.Context, db dbtypes.Interface, batchSize int) ([]Record, error) {
	f := s.qb.Filter()
	query := s.qb.Select(pendingColumns).
		From(s.tableName).
		Where(f.Eq("status", StatusPending)).
		OrderBy("seq ASC").
		Limit(uint64(max(batchSize, 0))) // #nosec G115 -- clamped at zero; a batch size is a small config value

	var records []Record
	err := database.ExecuteQueryMany(ctx, db, query, s.op("fetch pending"), func(rows *sql.Rows) error {
		r, err := s.scanRecord(rows)
		if err != nil {
			return err
		}
		records = append(records, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

func (s *sqlStore) MarkPublished(ctx context.Context, db dbtypes.Interface, eventID string) error {
	f := s.qb.Filter()
	_, err := database.ExecuteUpdate(ctx, db, s.qb.Update(s.tableName).
		Set("status", StatusPublished).
		Set("published_at", time.Now()).
		Where(f.Eq("id", eventID)),
		s.op("mark published"))
	return err
}

func (s *sqlStore) MarkFailed(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error {
	f := s.qb.Filter()
	_, err := database.ExecuteUpdate(ctx, db, s.qb.Update(s.tableName).
		Set("retry_count", s.qb.MustExpr("retry_count + 1")).
		Set(s.errorColumn, errMsg).
		Where(f.Eq("id", eventID)),
		s.op("mark failed"))
	return err
}

func (s *sqlStore) MarkDeadLettered(ctx context.Context, db dbtypes.Interface, eventID, errMsg string) error {
	f := s.qb.Filter()
	_, err := database.ExecuteUpdate(ctx, db, s.qb.Update(s.tableName).
		Set("retry_count", s.qb.MustExpr("retry_count + 1")).
		Set("status", StatusFailed).
		Set(s.errorColumn, errMsg).
		Where(f.Eq("id", eventID)),
		s.op("mark dead-lettered"))
	return err
}

func (s *sqlStore) DeletePublished(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error) {
	f := s.qb.Filter()
	return database.ExecuteUpdate(ctx, db, s.qb.Delete(s.tableName).
		Where(f.And(f.Eq("status", StatusPublished), f.Lt("published_at", before))),
		s.op("delete published"))
}

// Lead takes the ledger's leader row FOR UPDATE NOWAIT in a transaction held until
// Release. The row lock IS the claim, so the transaction stays open for the cycle.
// The probe is the vendor's table-less SELECT 1 — the builder adds FROM dual on
// Oracle.
func (s *sqlStore) Lead(ctx context.Context, db dbtypes.Interface) (Leadership, error) {
	f := s.qb.Filter()
	lockSQL, lockArgs, err := s.qb.Select("id").From(s.leaderTable).Where(f.Eq("id", 1)).ForUpdateNoWait().ToSQL()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.op("build leader lock"), err)
	}
	probeSQL, _, err := s.qb.Select(s.qb.MustExpr("1")).ToSQL()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.op("build leader probe"), err)
	}
	return leadRow(ctx, db, s.vendor, s.leaderTable, lockSQL, lockArgs, probeSQL)
}
