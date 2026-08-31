package builder

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Masterminds/squirrel"

	"github.com/gaborage/go-bricks/database/internal/sqllex"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// oracleRenderer is the Oracle half of the vendorRenderer seam: reserved words
// and names outside Oracle's bare-identifier grammar are quoted, everything else
// is left for Oracle to fold. The rendering itself lives in sqllex; this adapter
// is dispatch. Its methods carry no vendor test — rendererFor hands this adapter
// out only for Oracle.
type oracleRenderer struct{}

var _ vendorRenderer = oracleRenderer{}

// QuoteColumn renders a column reference with reserved-word-only quoting,
// preserving the caller's original case — it does not upper-case reserved words.
func (oracleRenderer) QuoteColumn(column string) string {
	return sqllex.QuoteOracleIdentifier(column)
}

// QuoteTable renders a FROM/JOIN table argument.
func (oracleRenderer) QuoteTable(table string) string {
	// An inline alias is part of the table argument's grammar ("users u"), so
	// quote only the identifier and keep the alias: quoting the whole string
	// produced `FROM "users u"`, one table nobody named (#1156).
	trimmed := strings.TrimSpace(table)
	if m := validTableNamePattern.FindStringSubmatch(trimmed); m != nil {
		return sqllex.QuoteOracleIdentifier(m[validTableNamePattern.SubexpIndex("ident")]) +
			m[validTableNamePattern.SubexpIndex("alias")]
	}
	// Not table-shaped. Unreachable from every door — each validates against
	// this same pattern first — and total for any future caller, the same
	// contract QuoteIdentifierForClause's fallback keeps.
	return sqllex.QuoteOracleIdentifier(trimmed)
}

// QuoteIdentifierForClause renders an ORDER BY / GROUP BY item, distinguishing
// the column reference from the direction and NULLS-ordering keywords.
func (oracleRenderer) QuoteIdentifierForClause(identifier string) string {
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

// CaseInsensitiveLike folds both sides with UPPER(): Oracle has no ILIKE, so
// case insensitivity is spelled in the comparison rather than in the operator.
func (oracleRenderer) CaseInsensitiveLike(quotedColumn, likePattern string) squirrel.Sqlizer {
	return squirrel.Like{"UPPER(" + quotedColumn + ")": strings.ToUpper(likePattern)}
}

// Regex renders REGEXP_LIKE with an optional 'i' match flag, wrapped in NOT(...)
// when negated: Oracle carries case and negation as arguments and a wrapper, not
// as four operator spellings.
func (oracleRenderer) Regex(quotedColumn, pattern string, caseInsensitive, negated bool) squirrel.Sqlizer {
	expr := "REGEXP_LIKE(" + quotedColumn + ", ?"
	args := []any{pattern}
	if caseInsensitive {
		expr += ", ?"
		args = append(args, "i")
	}
	expr += ")"
	if negated {
		expr = "NOT (" + expr + ")"
	}
	return squirrel.Expr(expr, args...)
}

// JSONContains has no clean Oracle equivalent — JSON_EQUAL is exact equality and
// JSON_EXISTS wants a path predicate — so it reports that rather than rendering a
// predicate that means something else. The column is never quoted: nothing about
// it can change this outcome. See
// https://github.com/gaborage/go-bricks/issues/341.
func (oracleRenderer) JSONContains(_ any, _ func() (string, error)) squirrel.Sqlizer {
	return errorSqlizer{err: errors.New("JSONContains: Oracle support not implemented; see https://github.com/gaborage/go-bricks/issues/341")}
}

// CurrentTimestamp renders SYSDATE.
func (oracleRenderer) CurrentTimestamp() string { return "SYSDATE" }

// UUIDGeneration renders SYS_GUID(), Oracle's UUID generation.
func (oracleRenderer) UUIDGeneration() string { return "SYS_GUID()" }

// BooleanValue renders 1/0: Oracle stores booleans as NUMBER(1).
func (oracleRenderer) BooleanValue(value bool) any {
	if value {
		return 1
	}
	return 0
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
	quotedInsertCols := qb.quoteColumnsForDML(insertKeys...)
	usingValues := make([]string, len(insertKeys))
	for i, col := range quotedInsertCols {
		usingValues[i] = fmt.Sprintf(":%d AS %s", i+1, col)
	}
	usingArgs := valuesByKeyOrder(insertColumns, insertKeys)

	// Build ON clause for conflict detection
	orderedConflicts := append([]string(nil), conflictColumns...)
	sort.Strings(orderedConflicts)
	quotedConflicts := qb.quoteColumnsForDML(orderedConflicts...)
	onConditions := make([]string, len(quotedConflicts))
	for i, col := range quotedConflicts {
		onConditions[i] = fmt.Sprintf("target.%s = source.%s", col, col)
	}

	// Build UPDATE SET clause
	updateKeys := sortedKeys(updateColumns)
	quotedUpdateCols := qb.quoteColumnsForDML(updateKeys...)
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
