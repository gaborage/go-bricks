package inbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbident "github.com/gaborage/go-bricks/database/identifier"
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
	// Used for auto-migration when inbox.autocreatetable is true.
	CreateTable(ctx context.Context, db dbtypes.Interface) error
}

// inboxLongestDerivedAffix is the longest thing appended to the configured name:
// the index "idx_<name>_processed". Budgeting for it covers every derived name,
// since the table itself and the Oracle primary-key constraint are shorter.
const inboxLongestDerivedAffix = len("idx__processed")

// maxTableNameLen is the vendor-blind bound: Oracle's, the loosest of the two.
// It is what the config-time check can enforce, because the vendor is not in
// scope when the configuration is validated.
const maxTableNameLen = dbident.MaxOracleBytes - inboxLongestDerivedAffix

// maxPostgresTableNameLen is the same budget against PostgreSQL's tighter cap.
// It matters because the two vendors fail differently: Oracle raises ORA-00972
// on an over-long identifier, while PostgreSQL TRUNCATES past NAMEDATALEN-1
// rather than refusing — so two over-long names sharing a prefix collapse onto
// one object, and a second CREATE INDEX would quietly target the first one.
const maxPostgresTableNameLen = dbident.MaxPostgreSQLBytes - inboxLongestDerivedAffix

// validateTableName checks that name is a safe, unqualified SQL identifier.
// The inbox requires an unqualified name (no schema prefix) because the Oracle
// store derives a primary-key constraint name from it, and bounds its length so
// the derived constraint/index identifiers fit Oracle's limit.
func validateTableName(name string) error {
	if err := sqlid.ValidateTableName(name); err != nil {
		return fmt.Errorf("inbox: %w", err)
	}
	if strings.Contains(name, ".") {
		return fmt.Errorf("inbox: table name %q must be unqualified (no schema prefix)", name)
	}
	if len(name) > maxTableNameLen {
		return fmt.Errorf("inbox: table name %q is too long (max %d; derived Oracle identifiers must fit %d chars)", name, maxTableNameLen, dbident.MaxOracleBytes)
	}
	return nil
}

// maxTableNameLenFor is the configured-name budget for a store's own vendor.
func maxTableNameLenFor(vendor dbtypes.Vendor) int {
	if vendor == dbtypes.PostgreSQL {
		return maxPostgresTableNameLen
	}
	return maxTableNameLen
}

// validateTableNameForVendor is validateTableName plus the bound of the vendor
// the store actually talks to. Only the store constructors call it: they each
// know their vendor, while the config-time check does not.
func validateTableNameForVendor(vendor dbtypes.Vendor, name string) error {
	if err := validateTableName(name); err != nil {
		return err
	}
	if maxLen := maxTableNameLenFor(vendor); len(name) > maxLen {
		return fmt.Errorf("inbox: table name %q is too long for %s (max %d; derived identifiers must fit the vendor's %d-byte cap)",
			name, vendor, maxLen, maxLen+inboxLongestDerivedAffix)
	}
	return nil
}

// deleteProcessedBefore is the retention sweep both vendors share: the builder
// renders the vendor's placeholder, and the rows-affected count is the result.
func deleteProcessedBefore(ctx context.Context, db dbtypes.Interface, qb *database.QueryBuilder, table, label string, before time.Time) (int64, error) {
	f := qb.Filter()
	return database.ExecuteUpdate(ctx, db, qb.Delete(table).Where(f.Lt("processed_at", before)), label)
}
