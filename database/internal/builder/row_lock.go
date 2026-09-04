package builder

import (
	"github.com/Masterminds/squirrel"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// The row-lock clause. Both vendors spell FOR UPDATE [NOWAIT] identically, so
// it is a builder constant rather than a vendorRenderer method; what IS
// vendor-specific — the clause must follow Oracle's OFFSET/FETCH and
// PostgreSQL's LIMIT/OFFSET — withRowLock settles by rendering it as the last
// suffix, after applyPagination.
const (
	forUpdate       = "FOR UPDATE"
	forUpdateNoWait = "FOR UPDATE NOWAIT"
)

// ForUpdate appends FOR UPDATE; see dbtypes.SelectQueryBuilder.
func (sqb *SelectQueryBuilder) ForUpdate() dbtypes.SelectQueryBuilder {
	sqb.lock = forUpdate
	return sqb
}

// ForUpdateNoWait appends FOR UPDATE NOWAIT; see dbtypes.SelectQueryBuilder.
func (sqb *SelectQueryBuilder) ForUpdateNoWait() dbtypes.SelectQueryBuilder {
	sqb.lock = forUpdateNoWait
	return sqb
}

// withRowLock appends the lock clause, if any, as the statement's final suffix.
func (sqb *SelectQueryBuilder) withRowLock(builder squirrel.SelectBuilder) squirrel.SelectBuilder {
	if sqb.lock == "" {
		return builder
	}
	return builder.Suffix(sqb.lock)
}

// validateRowLock refuses the one combination a vendor cannot run: Oracle's
// row_limiting_clause (OFFSET/FETCH, what Limit/Offset render there) "cannot
// [be specified] with the for_update_clause" — Oracle Database SQL Language
// Reference, SELECT, "Restrictions on the row_limiting_clause". PostgreSQL
// accepts LIMIT/OFFSET with FOR UPDATE, so only Oracle is judged.
func (sqb *SelectQueryBuilder) validateRowLock() error {
	if sqb.lock == "" || sqb.qb.vendor != dbtypes.Oracle {
		return nil
	}
	if sqb.limit > 0 || sqb.offset > 0 {
		return dbtypes.ErrRowLockWithPagination
	}
	return nil
}
