package inbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/sqlid"
)

// Record is a single row in the inbox ledger: a processed event id scoped to a
// tenant, with the time it was processed.
type Record struct {
	TenantID    string
	EventID     string
	ProcessedAt time.Time
}

// Store abstracts inbox ledger operations for vendor-agnostic SQL.
// Implementations exist for PostgreSQL and Oracle with vendor-specific
// placeholder styles, DDL, and duplicate-detection (PostgreSQL ON CONFLICT vs
// Oracle unique-violation catch).
type Store interface {
	// MarkProcessed records (tenant_id, event_id) within the given transaction.
	// It returns inserted=true the first time an id is seen and inserted=false on
	// a duplicate (the id was already processed).
	MarkProcessed(ctx context.Context, tx dbtypes.Tx, rec Record) (inserted bool, err error)

	// DeleteProcessed removes ledger rows processed before the given time.
	// Returns the number of rows deleted.
	DeleteProcessed(ctx context.Context, db dbtypes.Interface, before time.Time) (int64, error)

	// CreateTable creates the inbox table and its index if they do not exist.
	// Used for auto-migration when inbox.auto_create_table is true.
	CreateTable(ctx context.Context, db dbtypes.Interface) error
}

// validateTableName checks that name is a safe, unqualified SQL identifier.
// The inbox requires an unqualified name (no schema prefix) because the Oracle
// store derives a primary-key constraint name from it.
func validateTableName(name string) error {
	if err := sqlid.ValidateTableName(name); err != nil {
		return fmt.Errorf("inbox: %w", err)
	}
	if strings.Contains(name, ".") {
		return fmt.Errorf("inbox: table name %q must be unqualified (no schema prefix)", name)
	}
	return nil
}
