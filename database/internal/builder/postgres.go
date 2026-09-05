package builder

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Masterminds/squirrel"

	dbident "github.com/gaborage/go-bricks/database/identifier"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// postgresRenderer is the PostgreSQL half of the vendorRenderer seam. A column
// or table argument has already passed the identifier grammar by the time it
// gets here, so rendering is the identity: PostgreSQL folds an unquoted name to
// lower case, and quoting one here would rename it. Making that an adapter
// rather than a `default:` arm is the point of the seam — the behavior now has
// a name and a place to grow (#1258).
type postgresRenderer struct{}

var _ vendorRenderer = postgresRenderer{}

// QuoteColumn renders a column reference verbatim.
func (postgresRenderer) QuoteColumn(column string) string { return column }

// QuoteTable renders a table argument, inline alias included, verbatim.
func (postgresRenderer) QuoteTable(table string) string { return table }

// QuoteIdentifierForClause renders an ORDER BY / GROUP BY item verbatim.
func (postgresRenderer) QuoteIdentifierForClause(identifier string) string { return identifier }

// ValidateCharset judges one bare segment against PostgreSQL's unquoted
// alphabet. `#` is the notable exclusion: it is an operator character on
// PostgreSQL, so a name carrying one has to be quoted, while Oracle takes it
// bare — which is why the grammar cannot live in the shared lexer (#1202).
func (postgresRenderer) ValidateCharset(segment string) error {
	return dbident.ValidateCharset(dbtypes.PostgreSQL, segment)
}

// CaseInsensitiveLike renders ILIKE, PostgreSQL's case-insensitive operator, so
// neither side of the comparison has to be folded.
func (postgresRenderer) CaseInsensitiveLike(quotedColumn, likePattern string) squirrel.Sqlizer {
	return squirrel.ILike{quotedColumn: likePattern}
}

// Regex renders the POSIX-regex operators: ~ (CS), ~* (CI), !~ (NOT CS),
// !~* (NOT CI). Negation and case are operator spellings here, not wrappers.
func (postgresRenderer) Regex(quotedColumn, pattern string, caseInsensitive, negated bool) squirrel.Sqlizer {
	op := "~"
	if negated {
		op = "!~"
	}
	if caseInsensitive {
		op += "*"
	}
	return squirrel.Expr(quotedColumn+" "+op+" ?", pattern)
}

// JSONContains renders the jsonb containment operator. The payload is settled
// first and returns early, so a malformed one costs no column quoting.
func (postgresRenderer) JSONContains(value any, quoteColumn func() (string, error)) squirrel.Sqlizer {
	jsonStr, err := jsonContainsPayload(value)
	if err != nil {
		return errorSqlizer{err: fmt.Errorf("JSONContains: %w", err)}
	}
	quotedColumn, quoteErr := quoteColumn()
	if quoteErr != nil {
		return errorSqlizer{err: quoteErr}
	}
	return squirrel.Expr(quotedColumn+" @> ?::jsonb", jsonStr)
}

// CurrentTimestamp renders NOW().
func (postgresRenderer) CurrentTimestamp() string { return sqlFuncNow }

// UUIDGeneration renders gen_random_uuid(), in pgcrypto and in core since 13.
func (postgresRenderer) UUIDGeneration() string { return "gen_random_uuid()" }

// BooleanValue passes the bool through: PostgreSQL has a native boolean type.
func (postgresRenderer) BooleanValue(value bool) any { return value }

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
