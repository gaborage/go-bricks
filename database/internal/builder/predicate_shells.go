package builder

import "github.com/Masterminds/squirrel"

// This file holds the generic bodies for the method pairs that were structural
// copies between Filter and JoinFilter — And/Or, Eq..Gte, Between, In, NotIn,
// Null/NotNull (#1259). The one real interface difference between the
// families — a jf value door splices a RawExpression, an f value door refuses
// it with ErrExpressionInFilter (a WHERE-side expression takes an annotated
// f.Raw) — is a doorFamily argument the shells pass to the operand judge.

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

// comparePredicate is the shared body of Filter and JoinFilter Eq, NotEq, Lt,
// Lte, Gt and Gte: quote the column, judge the operand, render the answer. A
// column failure or a refused operand is lifted through errorSqlizer, whose
// ToSql defers the error to the parent query's build.
func comparePredicate[T squirrel.Sqlizer](
	qb *QueryBuilder,
	column, op string,
	value any,
	family doorFamily,
	wrap func(squirrel.Sqlizer) T,
) T {
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return wrap(errorSqlizer{err: err})
	}
	judged, err := judgeOperand(op, value, family)
	if err != nil {
		return wrap(errorSqlizer{err: err})
	}
	return wrap(renderPredicate(quotedColumn, op, judged, family))
}

// betweenPredicate is the shared body of Filter.Between and
// JoinFilter.Between. Each bound is an ordering operand judged on its own —
// lower under `>=`, upper under `<=` — so there is no rendering of
// `col >= NULL`, and each renders itself, a jf expression bound spliced beside
// a bound value.
func betweenPredicate[T squirrel.Sqlizer](
	qb *QueryBuilder,
	column string,
	lowerBound, upperBound any,
	family doorFamily,
	wrap func(squirrel.Sqlizer) T,
) T {
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return wrap(errorSqlizer{err: err})
	}
	lower, err := judgeOperand(">=", lowerBound, family)
	if err != nil {
		return wrap(errorSqlizer{err: err})
	}
	upper, err := judgeOperand("<=", upperBound, family)
	if err != nil {
		return wrap(errorSqlizer{err: err})
	}
	return wrap(squirrel.And{
		renderPredicate(quotedColumn, ">=", lower, family),
		renderPredicate(quotedColumn, "<=", upper, family),
	})
}

// inListPredicate is the shared body of Filter.In/NotIn and
// JoinFilter.In/NotIn: quote the column, resolve the list operand through the
// same normalization the compare doors use (nil/pointer/Valuer elements,
// scalar-to-one-element wrapping, a RawExpression judged by family — spliced as
// `col IN (<sql>)` or refused), and render either squirrel's own empty-list
// constant or the column-to-list comparison.
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
	family doorFamily,
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
	normalized, exprSQL, empty, err := resolveListOperands(op, values, family)
	if err != nil {
		return wrap(errorSqlizer{err: err})
	}
	if exprSQL != "" {
		return wrap(squirrel.Expr(quotedColumn + " " + op + " (" + exprSQL + ")"))
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
