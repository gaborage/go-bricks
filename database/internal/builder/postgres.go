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
	orderedCols := sortedKeys(insertColumns)
	vals := valuesByKeyOrder(insertColumns, orderedCols)
	cols := qb.escapeIdentifiers(orderedCols)

	insertQuery = insertQuery.Columns(cols...).Values(vals...)

	// Build ON CONFLICT clause with deterministic order
	cc := make([]string, len(conflictColumns))
	copy(cc, conflictColumns)
	sort.Strings(cc)

	if len(cc) == 0 {
		return "", nil, fmt.Errorf("conflict columns required for PostgreSQL upsert")
	}

	escapedCC := qb.escapeIdentifiers(cc)
	updateCols := sortedKeys(updateKeys)

	var conflictClause string
	if len(updateCols) == 0 {
		// If no update columns are provided, do nothing on conflict
		conflictClause = "ON CONFLICT (" + strings.Join(escapedCC, ", ") + ") DO NOTHING"
	} else {
		var setParts = make([]string, 0, len(updateCols))
		for _, col := range updateCols {
			escapedCol := qb.EscapeIdentifier(col)
			setParts = append(setParts, escapedCol+" = EXCLUDED."+escapedCol)
		}
		conflictClause = "ON CONFLICT (" + strings.Join(escapedCC, ", ") + ") DO UPDATE SET " + strings.Join(setParts, ", ")
	}

	// Generate the final SQL with conflict resolution
	sql, args, err := insertQuery.ToSql()
	if err != nil {
		return "", nil, err
	}

	// Append the conflict clause to the INSERT statement
	query = sql + " " + conflictClause
	return query, args, nil
}
