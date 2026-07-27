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

func TestQueryBuilderDeterministicOutputProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		vendor := rapid.SampledFrom([]string{PostgreSQL, Oracle}).Draw(rt, "vendor")
		col := identGen.Draw(rt, "col")
		val := rapid.Int().Draw(rt, "val")
		build := func() (string, []any) {
			qb := NewQueryBuilder(vendor)
			sql, args, err := qb.Select("id").From("users").Where(qb.Filter().Eq(col, val)).ToSQL()
			if err != nil {
				rt.Fatalf("ToSQL: %v", err)
			}
			return sql, args
		}
		sql1, args1 := build()
		sql2, args2 := build()
		if sql1 != sql2 || fmt.Sprint(args1) != fmt.Sprint(args2) {
			rt.Fatalf("non-deterministic: %q vs %q", sql1, sql2)
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
