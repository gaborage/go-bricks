package database

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

var identGen = rapid.StringMatching(`[a-z][a-z0-9_]{0,29}`)

var (
	pgPlaceholderRe     = regexp.MustCompile(`\$(\d+)`)
	oraclePlaceholderRe = regexp.MustCompile(`:(\d+)`)
)

func buildEqChain(t *rapid.T, qb *QueryBuilder, n int) (sql string, args []any, err error) {
	f := qb.Filter()
	filter := f.Eq(identGen.Draw(t, "col0"), rapid.Int().Draw(t, "val0"))
	for i := 1; i < n; i++ {
		filter = f.And(filter, f.Eq(identGen.Draw(t, fmt.Sprintf("col%d", i)), rapid.Int().Draw(t, fmt.Sprintf("val%d", i))))
	}
	return qb.Select("id").From("users").Where(filter).ToSQL()
}

func TestQueryBuilderPlaceholderArityProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		vendor := rapid.SampledFrom([]string{PostgreSQL, Oracle}).Draw(rt, "vendor")
		n := rapid.IntRange(1, 8).Draw(rt, "conds")
		sql, args, err := buildEqChain(rt, NewQueryBuilder(vendor), n)
		if err != nil {
			rt.Fatalf("ToSQL: %v", err)
		}
		re := pgPlaceholderRe
		if vendor == Oracle {
			re = oraclePlaceholderRe
		}
		if got := len(re.FindAllString(sql, -1)); got != len(args) {
			rt.Fatalf("placeholders %d != args %d in %q", got, len(args), sql)
		}
	})
}

func TestQueryBuilderPostgresPlaceholdersSequentialProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(rt, "conds")
		sql, _, err := buildEqChain(rt, NewQueryBuilder(PostgreSQL), n)
		if err != nil {
			rt.Fatalf("ToSQL: %v", err)
		}
		for i, m := range pgPlaceholderRe.FindAllStringSubmatch(sql, -1) {
			if m[1] != strconv.Itoa(i+1) {
				rt.Fatalf("placeholder %d is $%s in %q", i+1, m[1], sql)
			}
		}
	})
}

// buildFromDraws chains f.Eq/f.And over pre-drawn column/value slices — the
// same shape as buildEqChain, but parameterized so a caller can build from the
// same draws more than once instead of drawing fresh values per build.
func buildFromDraws(qb *QueryBuilder, cols []string, vals []int) (sql string, args []any, err error) {
	f := qb.Filter()
	filter := f.Eq(cols[0], vals[0])
	for i := 1; i < len(cols); i++ {
		filter = f.And(filter, f.Eq(cols[i], vals[i]))
	}
	return qb.Select("id").From("users").Where(filter).ToSQL()
}

func TestQueryBuilderDeterministicOutputProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		vendor := rapid.SampledFrom([]string{PostgreSQL, Oracle}).Draw(rt, "vendor")
		n := rapid.IntRange(2, 6).Draw(rt, "conds")
		cols := make([]string, n)
		vals := make([]int, n)
		for i := 0; i < n; i++ {
			cols[i] = identGen.Draw(rt, fmt.Sprintf("col%d", i))
			vals[i] = rapid.Int().Draw(rt, fmt.Sprintf("val%d", i))
		}
		build := func() (string, []any) {
			qb := NewQueryBuilder(vendor)
			sql, args, err := buildFromDraws(qb, cols, vals)
			if err != nil {
				rt.Fatalf("ToSQL: %v", err)
			}
			return sql, args
		}
		sql1, args1 := build()
		sql2, args2 := build()
		if sql1 != sql2 || fmt.Sprint(args1) != fmt.Sprint(args2) {
			rt.Fatalf("non-deterministic: %q/%v vs %q/%v", sql1, args1, sql2, args2)
		}
	})
}

func TestQueryBuilderOracleReservedWordQuotingProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		reserved := rapid.SampledFrom([]string{"number", "level", "size"}).Draw(rt, "reserved")
		sql, _, err := NewQueryBuilder(Oracle).Select("id", reserved).From("users").ToSQL()
		if err != nil {
			rt.Fatalf("ToSQL: %v", err)
		}
		if want := `"` + reserved + `"`; !strings.Contains(sql, want) {
			rt.Fatalf("reserved word %q not quoted in %q", reserved, sql)
		}
	})
}
