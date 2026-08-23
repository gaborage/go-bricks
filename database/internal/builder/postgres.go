package builder

import (
	"fmt"
	"sort"
	"strings"
)

// buildPostgreSQLUpsert creates a PostgreSQL upsert: ON CONFLICT (columns) DO UPDATE SET ...
// when update columns are provided, or DO NOTHING otherwise.
// Its preconditions are enforced by BuildUpsert, the only caller.
func (qb *QueryBuilder) buildPostgreSQLUpsert(table string, conflictColumns []string, insertColumns, updateKeys map[string]any) (query string, args []any, err error) {
	// Built directly rather than through qb.Insert(): BuildUpsert has already
	// validated this table, and the columns below are ESCAPED renderings rather
	// than raw names — `a""b` renders as `"a""b"`, which the identifier grammar
	// rightly refuses as an argument. Re-entering the public door would judge the
	// builder's own output by the rule meant for a caller's input, and would
	// validate the table a second time.
	orderedCols := sortedKeys(insertColumns)
	vals := valuesByKeyOrder(insertColumns, orderedCols)
	cols := qb.escapeIdentifiers(orderedCols)

	insertBuilder := qb.statementBuilder.Insert(qb.quoteTableForQuery(table)).
		Columns(cols...).
		Values(vals...)

	// Build ON CONFLICT clause with deterministic order
	cc := make([]string, len(conflictColumns))
	copy(cc, conflictColumns)
	sort.Strings(cc)

	escapedCC := qb.escapeIdentifiers(cc)
	updateCols := sortedKeys(updateKeys)

	var conflictClause string
	var updateVals []any
	if len(updateCols) == 0 {
		// If no update columns are provided, do nothing on conflict
		conflictClause = "ON CONFLICT (" + strings.Join(escapedCC, ", ") + ") DO NOTHING"
	} else {
		// Bind the caller's update values as parameters rather than reusing EXCLUDED (the
		// proposed-insert values). EXCLUDED silently ignored the updateColumns values —
		// diverging from Oracle's MERGE — and broke update columns absent from the insert
		// set (EXCLUDED.<not-inserted> references a non-existent column). The update
		// placeholders continue numbering after the insert placeholders ($len(vals)+i).
		updateVals = valuesByKeyOrder(updateKeys, updateCols)
		setParts := make([]string, 0, len(updateCols))
		for i, col := range updateCols {
			escapedCol := qb.EscapeIdentifier(col)
			setParts = append(setParts, fmt.Sprintf("%s = $%d", escapedCol, len(vals)+1+i))
		}
		conflictClause = "ON CONFLICT (" + strings.Join(escapedCC, ", ") + ") DO UPDATE SET " + strings.Join(setParts, ", ")
	}

	// Generate the final SQL with conflict resolution
	sql, args, err := insertBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}

	// Append the update values (bound by the placeholders above) after the insert args.
	args = append(args, updateVals...)

	query = sql + " " + conflictClause
	return query, args, nil
}
