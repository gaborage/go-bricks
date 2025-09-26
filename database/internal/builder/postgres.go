package builder

import (
	"fmt"
	"sort"
	"strings"
)

// buildPostgreSQLUpsert creates a PostgreSQL ON CONFLICT DO UPDATE statement.
// PostgreSQL uses INSERT ... ON CONFLICT (columns) DO UPDATE SET ... syntax.
func (qb *QueryBuilder) buildPostgreSQLUpsert(table string, conflictColumns []string, insertColumns, updateKeys map[string]any) (query string, args []any, err error) {
	// Build the base INSERT statement
	insertQuery := qb.Insert(table)

	// Create deterministic column order for consistent SQL generation
	orderedCols := make([]string, 0, len(insertColumns))
	for c := range insertColumns {
		orderedCols = append(orderedCols, c)
	}
	sort.Strings(orderedCols)

	// Extract values in column order while keeping identifiers escaped
	vals := make([]any, 0, len(orderedCols))
	cols := make([]string, 0, len(orderedCols))
	for _, c := range orderedCols {
		vals = append(vals, insertColumns[c])
		cols = append(cols, qb.EscapeIdentifier(c))
	}

	insertQuery = insertQuery.Columns(cols...).Values(vals...)

	// Build ON CONFLICT clause with deterministic order
	cc := make([]string, len(conflictColumns))
	copy(cc, conflictColumns)
	sort.Strings(cc)

	if len(cc) == 0 || len(updateKeys) == 0 {
		return "", nil, fmt.Errorf("conflict columns and update keys required for PostgreSQL upsert")
	}

	escapedCC := make([]string, len(cc))
	for i, c := range cc {
		escapedCC[i] = qb.EscapeIdentifier(c)
	}
	conflictClause := "ON CONFLICT (" + strings.Join(escapedCC, ", ") + ") DO UPDATE SET "

	// Build UPDATE SET clause with deterministic order
	updateCols := make([]string, 0, len(updateKeys))
	for c := range updateKeys {
		updateCols = append(updateCols, c)
	}
	sort.Strings(updateCols)

	var setParts = make([]string, 0, len(updateCols))
	for _, col := range updateCols {
		escapedCol := qb.EscapeIdentifier(col)
		setParts = append(setParts, escapedCol+" = EXCLUDED."+escapedCol)
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
