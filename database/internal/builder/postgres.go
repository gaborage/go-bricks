package builder

import (
	"sort"
	"strings"
)

// buildPostgreSQLUpsert creates a PostgreSQL ON CONFLICT DO UPDATE statement.
// PostgreSQL uses INSERT ... ON CONFLICT (columns) DO UPDATE SET ... syntax.
func (qb *QueryBuilder) buildPostgreSQLUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error) {
	// Build the base INSERT statement
	insertQuery := qb.Insert(table)

	// Create deterministic column order for consistent SQL generation
	cols := make([]string, 0, len(insertColumns))
	for c := range insertColumns {
		cols = append(cols, c)
	}
	sort.Strings(cols)

	// Extract values in column order
	vals := make([]any, 0, len(cols))
	for _, c := range cols {
		vals = append(vals, insertColumns[c])
	}
	insertQuery = insertQuery.Columns(cols...).Values(vals...)

	// Build ON CONFLICT clause with deterministic order
	cc := make([]string, len(conflictColumns))
	copy(cc, conflictColumns)
	sort.Strings(cc)
	conflictClause := "ON CONFLICT (" + strings.Join(cc, ", ") + ") DO UPDATE SET "

	// Build UPDATE SET clause with deterministic order
	updateCols := make([]string, 0, len(updateColumns))
	for c := range updateColumns {
		updateCols = append(updateCols, c)
	}
	sort.Strings(updateCols)

	var setParts = make([]string, 0, len(updateCols))
	for _, col := range updateCols {
		setParts = append(setParts, col+" = EXCLUDED."+col)
	}
	conflictClause += strings.Join(setParts, ", ")

	// Generate the final SQL with conflict resolution
	sql, args, err := insertQuery.ToSql()
	if err != nil {
		return "", nil, err
	}

	// Append the conflict clause to the INSERT statement
	query = sql + " " + conflictClause
	return query, args, nil
}
