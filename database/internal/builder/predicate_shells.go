package builder

import "github.com/Masterminds/squirrel"

// This file holds the generic bodies for the six method pairs that were
// structural copies between Filter and JoinFilter — And/Or, In, NotIn,
// Null/NotNull (#1259). Eq/NotEq/Between/compare and the subquery doors stay
// family-owned: JoinFilter's RawExpression acceptance there is a real
// interface difference, not accidental duplication, so this file does not
// touch them.

// logicalContainer is the squirrel wrapper And/Or fold into. squirrel.And and
// squirrel.Or are both named []squirrel.Sqlizer types that also render
// themselves, so combineLogical can pre-size the EXACT container type each
// unrolled method built by hand, rather than assembling a plain slice and
// converting it after the fact.
type logicalContainer interface {
	~[]squirrel.Sqlizer
	squirrel.Sqlizer
}

// combineLogical is the shared body of Filter.And/Or and JoinFilter.And/Or.
// Both walked an identical loop — skip a nil filter as a deliberate no-op,
// unwrap the family's own concrete type down to the squirrel.Sqlizer it
// wraps, or fall back to the filter's own Sqlizer implementation for
// anything else — differing only in the concrete wrapper (Filter vs
// JoinFilter) and the squirrel container (And vs Or, selected by C).
//
// classify carries the one part that differs per FAMILY rather than per
// operator: the nil check and the concrete-type unwrap. It reports
// (sqlizer, false) to skip a filter outright (the nil case) and
// (sqlizer, true) to append, having already resolved which sqlizer that is.
func combineLogical[T squirrel.Sqlizer, C logicalContainer](
	filters []T,
	classify func(T) (sqlizer squirrel.Sqlizer, include bool),
	wrap func(squirrel.Sqlizer) T,
) T {
	sqlizers := make(C, 0, len(filters))
	for _, filter := range filters {
		if sqlizer, include := classify(filter); include {
			sqlizers = append(sqlizers, sqlizer)
		}
	}
	return wrap(sqlizers)
}

// inListPredicate is the shared body of Filter.In/NotIn and
// JoinFilter.In/NotIn: quote the column, resolve the list operand through the
// same normalization the compare doors use (nil/pointer/Valuer elements,
// scalar-to-one-element wrapping), and render either squirrel's own
// empty-list constant or the column-to-list comparison.
//
// negate selects NOT IN over IN. The three facts that vary between the two —
// the operand-resolution error's operator name, the vendor-neutral constant
// squirrel has always rendered for the empty case ("(1=0)" for IN — always
// false, "(1=1)" for NOT IN — always true), and squirrel.Eq vs squirrel.NotEq
// for the non-empty case — moved in lockstep at every call site, so negate
// derives all three internally instead of taking them as three separate,
// coupled parameters.
//
// On a resolution failure, errorSqlizer (filter.go) is lifted through the
// same wrap used for real predicates — its ToSql defers the error to the
// parent query's build.
func inListPredicate[T squirrel.Sqlizer](
	qb *QueryBuilder,
	column string,
	values any,
	negate bool,
	wrap func(squirrel.Sqlizer) T,
) T {
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return wrap(errorSqlizer{err: err})
	}
	op, emptySQL := "IN", "(1=0)"
	if negate {
		op, emptySQL = "NOT IN", "(1=1)"
	}
	normalized, empty, err := resolveListOperands(op, values)
	if err != nil {
		return wrap(errorSqlizer{err: err})
	}
	if empty {
		return wrap(squirrel.Expr(emptySQL))
	}
	if negate {
		return wrap(squirrel.NotEq{quotedColumn: normalized})
	}
	return wrap(squirrel.Eq{quotedColumn: normalized})
}

// nullPredicate is the shared body of Filter.Null/NotNull and
// JoinFilter.Null/NotNull: quote the column and render whichever squirrel
// wrapper against a nil value — squirrel.Eq with a nil value renders IS
// NULL, squirrel.NotEq renders IS NOT NULL, exactly as the compare doors
// already render a nil operand, so a NULL check and a nil-operand comparison
// mean one thing rendered one way.
//
// negate selects IS NOT NULL over IS NULL.
//
// On a resolution failure, errorSqlizer (filter.go) is lifted through the
// same wrap used for real predicates — its ToSql defers the error to the
// parent query's build.
func nullPredicate[T squirrel.Sqlizer](
	qb *QueryBuilder,
	column string,
	negate bool,
	wrap func(squirrel.Sqlizer) T,
) T {
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return wrap(errorSqlizer{err: err})
	}
	if negate {
		return wrap(squirrel.NotEq{quotedColumn: nil})
	}
	return wrap(squirrel.Eq{quotedColumn: nil})
}
