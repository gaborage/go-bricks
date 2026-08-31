package builder

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gaborage/go-bricks/database/internal/sqllex"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func (qb *QueryBuilder) quoteOracleColumn(column string) string {
	if qb.vendor != dbtypes.Oracle {
		return column
	}
	return sqllex.QuoteOracleIdentifier(column)
}

// quoteOracleIdentifierForClause handles Oracle-specific identifier quoting for ORDER BY and GROUP BY clauses
// It parses expressions to distinguish column references from SQL functions and direction keywords
func (qb *QueryBuilder) quoteOracleIdentifierForClause(identifier string) string {
	if qb.vendor != dbtypes.Oracle {
		return identifier
	}

	trimmed := strings.TrimSpace(identifier)
	if trimmed == "" {
		return trimmed
	}

	// Read the clause with the grammar the validator enforces, rather than
	// splitting on whitespace and understanding only `col DIR`: the four-token
	// form `col DESC NULLS LAST` is blessed by that grammar, and the old split
	// swallowed the whole string into one quoted identifier (#1156).
	m := validClauseIdentifierPattern.FindStringSubmatch(trimmed)
	if m == nil {
		// Not clause-shaped. Every door validates against this same pattern first,
		// so this is unreachable from them; quoting the whole string keeps the
		// function total for any future caller.
		return sqllex.QuoteOracleIdentifier(trimmed)
	}

	// An absent optional group is "", so the direction and the nulls ordering
	// append unconditionally.
	quoted := sqllex.QuoteOracleIdentifier(m[validClauseIdentifierPattern.SubexpIndex("ident")])
	quoted += strings.ToUpper(m[validClauseIdentifierPattern.SubexpIndex("dir")])
	return quoted + strings.ToUpper(m[validClauseIdentifierPattern.SubexpIndex("nulls")])
}

// quoteOracleColumnsForDML applies Oracle-specific quoting for column lists used in DML statements
// like INSERT or UPDATE where reserved words must be safely referenced. It delegates to the same
// reserved-word-only quoting used for query conditions (sqllex.QuoteOracleIdentifier) and preserves the
// caller's original case verbatim — it does not upper-case reserved words.
func (qb *QueryBuilder) quoteOracleColumnsForDML(columns ...string) []string {
	if qb.vendor != dbtypes.Oracle {
		return columns
	}

	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = sqllex.QuoteOracleIdentifier(col)
	}
	return quoted
}

// buildOraclePaginationClause constructs an Oracle-compatible pagination clause using OFFSET and FETCH NEXT syntax.
// The returned string contains "OFFSET {offset} ROWS" and/or "FETCH NEXT {limit} ROWS ONLY" as applicable; it is empty if both limit and offset are zero.
// Takes uint64 to match SelectQueryBuilder's limit/offset: narrowing to int would
// wrap values above math.MaxInt64 to negative and silently drop the clause.
func buildOraclePaginationClause(limit, offset uint64) string {
	// No zero/zero guard: neither branch below appends, and joining no parts
	// already yields "". A guard would be an equivalent-mutant magnet.
	parts := make([]string, 0, 2)
	if offset > 0 {
		parts = append(parts, fmt.Sprintf("OFFSET %d ROWS", offset))
	}
	if limit > 0 {
		parts = append(parts, fmt.Sprintf("FETCH NEXT %d ROWS ONLY", limit))
	}

	return strings.Join(parts, " ")
}

// BuildUpsert creates a vendor-specific UPSERT query: PostgreSQL emits
// INSERT ... ON CONFLICT (columns) DO UPDATE/DO NOTHING; Oracle emits
// MERGE INTO ... USING ... ON ... WHEN MATCHED/NOT MATCHED.
// A column present in both conflictColumns and updateColumns is rejected on
// every vendor: Oracle's MERGE cannot update an ON-clause column (ORA-38104).
// The preconditions are checked here rather than in either vendor builder: they
// are what makes one call mean one thing on both vendors, so enforcing them at
// the single dispatch point is what stops the two builders drifting apart again.
func (qb *QueryBuilder) BuildUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error) {
	// Vendor support is settled first so an unsupported vendor is reported as
	// such, rather than as a precondition failure for an upsert it cannot build.
	if qb.vendor != dbtypes.Oracle && qb.vendor != dbtypes.PostgreSQL {
		return "", nil, fmt.Errorf("upsert not supported for database vendor: %s", qb.vendor)
	}

	// The table is settled before any column rule, because it sits first in both
	// vendors' templates: an unvalidated name ends the statement with a trailing
	// comment and takes the rest of it, which no column precondition can catch
	// (#1104). Same grammar From/Update/Delete apply.
	table, tableErr := validateTableName(table)
	if tableErr != nil {
		return "", nil, fmt.Errorf("BuildUpsert: %w", tableErr)
	}

	if len(conflictColumns) == 0 {
		return "", nil, errConflictColumnsRequired
	}

	// Acceptance and normalization are settled first, for every column the
	// statement will name. The five identity checks below all consult
	// upsertColumnName, whose quote test is only correct for a rendering that is
	// one whole token, so no key that would defeat it may reach them. They then
	// compare the NORMALIZED spelling, which is the one the builders render, so
	// no door judges a key the statement does not carry (#1196).
	conflicts, conflictNameErr := normalizeUpsertColumns("conflict", conflictColumns)
	if conflictNameErr != nil {
		return "", nil, conflictNameErr
	}

	insertKeys, insertNameErr := normalizeUpsertColumns("insert", sortedKeys(insertColumns))
	if insertNameErr != nil {
		return "", nil, insertNameErr
	}

	updateKeys, updateNameErr := normalizeUpsertColumns("update", sortedKeys(updateColumns))
	if updateNameErr != nil {
		return "", nil, updateNameErr
	}

	if uniqueErr := qb.requireDistinctColumnIdentities("conflict", conflicts); uniqueErr != nil {
		return "", nil, uniqueErr
	}

	if insertDistinctErr := qb.requireDistinctColumnIdentities("insert", insertKeys); insertDistinctErr != nil {
		return "", nil, insertDistinctErr
	}

	if updateDistinctErr := qb.requireDistinctColumnIdentities("update", updateKeys); updateDistinctErr != nil {
		return "", nil, updateDistinctErr
	}

	if insertErr := qb.requireConflictColumnsInInsertSet(conflicts, insertKeys); insertErr != nil {
		return "", nil, insertErr
	}

	if conflictErr := qb.rejectConflictColumnUpdates(conflicts, updateKeys); conflictErr != nil {
		return "", nil, conflictErr
	}

	// From here the builders see only normalized spellings, so neither can render
	// a key that differs from the one every check above judged.
	normalizedConflicts := normalizedSpellings(conflicts)
	normalizedInserts := normalizedColumnMap(insertColumns, insertKeys)
	normalizedUpdates := normalizedColumnMap(updateColumns, updateKeys)

	if qb.vendor == dbtypes.Oracle {
		return qb.buildOracleMerge(table, normalizedConflicts, normalizedInserts, normalizedUpdates)
	}
	return qb.buildPostgreSQLUpsert(table, normalizedConflicts, normalizedInserts, normalizedUpdates)
}

// buildOracleMerge constructs an Oracle MERGE statement for upsert operations.
// Its preconditions are enforced by BuildUpsert, the only caller.
func (qb *QueryBuilder) buildOracleMerge(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error) {
	// Build the USING clause with values. Use reserved-word-only quoting (the same
	// quoting the DML paths use) so non-reserved identifiers stay unquoted and Oracle
	// folds them to the uppercase form created by standard DDL.
	insertKeys := sortedKeys(insertColumns)
	quotedInsertCols := qb.quoteOracleColumnsForDML(insertKeys...)
	usingValues := make([]string, len(insertKeys))
	for i, col := range quotedInsertCols {
		usingValues[i] = fmt.Sprintf(":%d AS %s", i+1, col)
	}
	usingArgs := valuesByKeyOrder(insertColumns, insertKeys)

	// Build ON clause for conflict detection
	orderedConflicts := append([]string(nil), conflictColumns...)
	sort.Strings(orderedConflicts)
	quotedConflicts := qb.quoteOracleColumnsForDML(orderedConflicts...)
	onConditions := make([]string, len(quotedConflicts))
	for i, col := range quotedConflicts {
		onConditions[i] = fmt.Sprintf("target.%s = source.%s", col, col)
	}

	// Build UPDATE SET clause
	updateKeys := sortedKeys(updateColumns)
	quotedUpdateCols := qb.quoteOracleColumnsForDML(updateKeys...)
	updateSets := make([]string, len(updateKeys))
	baseIndex := len(insertKeys) + 1
	for i, col := range quotedUpdateCols {
		updateSets[i] = fmt.Sprintf("%s = :%d", col, baseIndex+i)
	}
	updateArgs := valuesByKeyOrder(updateColumns, updateKeys)

	// Build INSERT clause
	insertCols := make([]string, len(quotedInsertCols))
	insertVals := make([]string, len(quotedInsertCols))
	for i, col := range quotedInsertCols {
		insertCols[i] = col
		insertVals[i] = "source." + col
	}

	query = fmt.Sprintf(`MERGE INTO %s target USING (SELECT %s FROM dual) source ON (%s)`,
		qb.quoteTableForQuery(table),
		strings.Join(usingValues, ", "),
		strings.Join(onConditions, " AND "))

	if len(updateSets) > 0 {
		query += fmt.Sprintf(" WHEN MATCHED THEN UPDATE SET %s", strings.Join(updateSets, ", "))
	}

	query += fmt.Sprintf(" WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)",
		strings.Join(insertCols, ", "),
		strings.Join(insertVals, ", "))

	// Combine arguments: using args first, then update args
	args = make([]any, 0, len(usingArgs)+len(updateArgs))
	args = append(args, usingArgs...)
	args = append(args, updateArgs...)

	return query, args, nil
}
