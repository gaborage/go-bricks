package builder

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// identifierQB builds the QueryBuilder these grammar tests validate through.
// Oracle is deliberate: its segment alphabet IS the shape grammar these cases
// were written against (`col#` included), so the rows below keep asserting the
// same thing once the vendor supplies the segment rule.
func identifierQB() *QueryBuilder { return NewQueryBuilder(dbtypes.Oracle) }

// TestValidateIdentifierAcceptsSafeForms verifies the column/SET-target validator
// accepts simple, qualified, and framework-quoted identifiers (M9 / ADR-031).
func TestValidateIdentifierAcceptsSafeForms(t *testing.T) {
	cases := []string{
		"id",
		"user_id",
		"_internal",
		"col$",
		"col#",
		"table.col",
		"schema.table.col",
		`"level"`,       // framework-quoted Oracle reserved word
		`table."level"`, // qualified + quoted segment
		`schema."number"`,
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			normalized, err := identifierQB().validateIdentifier("column", c)
			require.NoError(t, err, "expected %q to be accepted", c)
			require.Equal(t, strings.TrimSpace(c), normalized, "an accepted identifier must come back normalized")
		})
	}
}

// TestValidateIdentifierRejectsInjection verifies the column/SET-target validator
// rejects injection vectors and complex expressions.
func TestValidateIdentifierRejectsInjection(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"semicolon_statement", "name; DROP TABLE users--"},
		{"line_comment", "name--"},
		{"block_comment", "name/* x */"},
		{"embedded_space", "name DESC"},
		{"unbalanced_quote", `name" = "x`},
		{"function_call", "COUNT(*)"},
		{"leading_digit", "1col"},
		{"empty", ""},
		{"or_injection", "1 OR 1=1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := identifierQB().validateIdentifier("column", c.input)
			require.Error(t, err, "expected %q to be rejected", c.input)
		})
	}
}

// TestValidateClauseIdentifierAcceptsDirections verifies ORDER BY / GROUP BY
// arguments may carry the bounded ASC/DESC [NULLS FIRST|LAST] direction grammar.
func TestValidateClauseIdentifierAcceptsDirections(t *testing.T) {
	cases := []string{
		"name",
		"name ASC",
		"name DESC",
		"name asc",
		"created_at DESC",
		"u.name ASC",
		"name ASC NULLS FIRST",
		"name DESC NULLS LAST",
		"name NULLS FIRST",
		`"level" DESC`,
		`u."level" ASC`,
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			normalized, err := identifierQB().validateClauseIdentifier("orderBy", c)
			require.NoError(t, err, "expected %q to be accepted", c)
			require.Equal(t, strings.TrimSpace(c), normalized, "an accepted identifier must come back normalized")
		})
	}
}

// TestValidateClauseIdentifierRejectsInjection verifies ORDER BY / GROUP BY
// arguments reject injection vectors while still allowing legitimate directions.
func TestValidateClauseIdentifierRejectsInjection(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"semicolon_drop", "name; DROP TABLE users--"},
		{"line_comment", "name--"},
		{"block_comment", "name /* x */ DESC"},
		{"function_call", "COUNT(*)"},
		{"extra_token", "name DESC, id"},
		{"bogus_direction", "name SIDEWAYS"},
		{"subselect", "name) UNION SELECT password FROM users--"},
		{"nulls_without_position", "name NULLS"},
		{"direction_without_nulls_position", "name DESC NULLS"},
		{"two_directions", "name ASC DESC"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := identifierQB().validateClauseIdentifier("orderBy", c.input)
			require.Error(t, err, "expected %q to be rejected", c.input)
		})
	}
}

// TestValidateTableNameAcceptsAliasForms verifies the table validator accepts the
// inline-alias and qualified forms the From/JOIN string APIs support.
func TestValidateTableNameAcceptsAliasForms(t *testing.T) {
	cases := []string{
		"users",
		"users u",
		"schema.users",
		"schema.users u",
		`"user"`,
		`"user" u`,
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			normalized, err := identifierQB().validateTableName(c)
			require.NoError(t, err, "expected %q to be accepted", c)
			require.Equal(t, strings.TrimSpace(c), normalized, "an accepted identifier must come back normalized")
		})
	}
}

// TestValidateTableNameRejectsInjection verifies the table validator rejects
// injection vectors and multi-token payloads.
func TestValidateTableNameRejectsInjection(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"semicolon_drop", "users; DROP TABLE accounts--"},
		{"subselect", "users WHERE 1=1--"},
		{"too_many_tokens", "users u extra"},
		{"comment", "users--"},
		{"empty", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := identifierQB().validateTableName(c.input)
			require.Error(t, err, "expected %q to be rejected", c.input)
		})
	}
}

// TestBuilderRejectsIdentifierInjectionBothVendors exercises the end-to-end public
// builder API for PostgreSQL and Oracle: injection vectors surface as a ToSQL()
// error and never reach the generated SQL, while legitimate identifiers, qualified
// names, aliases, and ORDER BY directions build successfully.
func TestBuilderRejectsIdentifierInjectionBothVendors(t *testing.T) {
	vendors := []string{dbtypes.PostgreSQL, dbtypes.Oracle}

	for _, vendor := range vendors {
		t.Run(vendor, func(t *testing.T) {
			t.Run("order_by_injection_rejected", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				sql, _, err := qb.Select(selectAll).From(tableUsers).
					OrderBy("name; DROP TABLE users--").ToSQL()
				require.Error(t, err)
				assert.NotContains(t, sql, "DROP TABLE")
				assert.NotContains(t, sql, "--")
			})

			t.Run("group_by_function_string_rejected", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				_, _, err := qb.Select(selectAll).From(tableUsers).
					GroupBy("COUNT(*)").ToSQL()
				require.Error(t, err, "function expressions must go through qb.Expr()")
			})

			t.Run("from_injection_rejected", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				sql, _, err := qb.Select(selectAll).
					From("users; DROP TABLE accounts--").ToSQL()
				require.Error(t, err)
				assert.NotContains(t, sql, "DROP TABLE")
			})

			t.Run("set_injection_rejected", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				f := qb.Filter()
				_, _, err := qb.Update(tableUsers).
					Set("name = 'x'; DROP TABLE users--", "y").
					Where(f.Eq(colID, 1)).ToSQL()
				require.Error(t, err)
			})

			t.Run("set_map_injection_rejected", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				f := qb.Filter()
				_, _, err := qb.Update(tableUsers).
					SetMap(map[string]any{"name); DROP TABLE users--": "y"}).
					Where(f.Eq(colID, 1)).ToSQL()
				require.Error(t, err)
			})

			t.Run("join_injection_rejected", func(t *testing.T) {
				// The JOIN table argument shares From()'s verbatim-interpolation
				// path, so it must be validated identically (M9 / ADR-031).
				joinCases := []struct {
					name string
					join func(b dbtypes.SelectQueryBuilder, table any, jf dbtypes.JoinFilter) dbtypes.SelectQueryBuilder
				}{
					{"JoinOn", func(b dbtypes.SelectQueryBuilder, table any, jf dbtypes.JoinFilter) dbtypes.SelectQueryBuilder {
						return b.JoinOn(table, jf)
					}},
					{"LeftJoinOn", func(b dbtypes.SelectQueryBuilder, table any, jf dbtypes.JoinFilter) dbtypes.SelectQueryBuilder {
						return b.LeftJoinOn(table, jf)
					}},
					{"RightJoinOn", func(b dbtypes.SelectQueryBuilder, table any, jf dbtypes.JoinFilter) dbtypes.SelectQueryBuilder {
						return b.RightJoinOn(table, jf)
					}},
					{"InnerJoinOn", func(b dbtypes.SelectQueryBuilder, table any, jf dbtypes.JoinFilter) dbtypes.SelectQueryBuilder {
						return b.InnerJoinOn(table, jf)
					}},
				}
				const injection = "profiles p ON 1=1; DROP TABLE users--"
				for _, jc := range joinCases {
					t.Run(jc.name, func(t *testing.T) {
						qb := NewQueryBuilder(vendor)
						jf := qb.JoinFilter().EqColumn("users.id", "p.user_id")
						b := qb.Select(selectAll).From(tableUsers)
						sql, _, err := jc.join(b, injection, jf).ToSQL()
						require.Error(t, err)
						assert.NotContains(t, sql, "DROP TABLE")
						assert.NotContains(t, sql, "--")
					})
				}

				t.Run("CrossJoinOn", func(t *testing.T) {
					qb := NewQueryBuilder(vendor)
					sql, _, err := qb.Select(selectAll).From(tableUsers).
						CrossJoinOn(injection).ToSQL()
					require.Error(t, err)
					assert.NotContains(t, sql, "DROP TABLE")
					assert.NotContains(t, sql, "--")
				})
			})

			t.Run("join_tableref_injection_rejected", func(t *testing.T) {
				// A *TableRef carrying a crafted Name() must also be rejected: the
				// name is interpolated verbatim, so validateTableReference's name
				// branch guards it independently of the string path.
				qb := NewQueryBuilder(vendor)
				jf := qb.JoinFilter().EqColumn("users.id", "p.user_id")
				badRef := dbtypes.MustTable("profiles; DROP TABLE users--").MustAs("p")
				sql, _, err := qb.Select(selectAll).From(tableUsers).
					JoinOn(badRef, jf).ToSQL()
				require.Error(t, err)
				assert.NotContains(t, sql, "DROP TABLE")
			})

			t.Run("from_tableref_injection_rejected", func(t *testing.T) {
				// Covers validateTableReference's *TableRef name-error branch via From.
				qb := NewQueryBuilder(vendor)
				sql, _, err := qb.Select(selectAll).
					From(dbtypes.MustTable("users; DROP TABLE accounts--")).ToSQL()
				require.Error(t, err)
				assert.NotContains(t, sql, "DROP TABLE")
			})

			t.Run("update_table_injection_rejected", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				f := qb.Filter()
				sql, _, err := qb.Update("users; DROP TABLE users--").
					Set(colID, 1).
					Where(f.Eq(colID, 1)).ToSQL()
				require.Error(t, err)
				assert.NotContains(t, sql, "DROP TABLE")
			})

			t.Run("delete_table_injection_rejected", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				f := qb.Filter()
				sql, _, err := qb.Delete("users; DROP TABLE users--").
					Where(f.Eq(colID, 1)).ToSQL()
				require.Error(t, err)
				assert.NotContains(t, sql, "DROP TABLE")
			})

			t.Run("delete_order_by_injection_rejected", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				f := qb.Filter()
				sql, _, err := qb.Delete(tableUsers).
					OrderBy("name; DROP TABLE users--").
					Where(f.Eq(colID, 1)).ToSQL()
				require.Error(t, err)
				assert.NotContains(t, sql, "DROP TABLE")
				assert.NotContains(t, sql, "--")
			})

			t.Run("legitimate_delete_order_by_builds", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				f := qb.Filter()
				sql, _, err := qb.Delete(tableUsers).
					OrderBy("created_at DESC").
					Where(f.Eq(colID, 1)).ToSQL()
				require.NoError(t, err)
				assert.Contains(t, sql, "ORDER BY")
			})

			t.Run("legitimate_join_builds", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				jf := qb.JoinFilter().EqColumn("users.id", "p.user_id")
				sql, _, err := qb.Select(selectAll).From("users u").
					JoinOn("profiles p", jf).ToSQL()
				require.NoError(t, err)
				assert.Contains(t, sql, "JOIN")
				assert.NotContains(t, sql, "DROP TABLE")
			})

			t.Run("legitimate_query_builds", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				sql, _, err := qb.Select(colID, colName).
					From("users u").
					GroupBy(colName).
					OrderBy("u.name ASC", "id DESC").ToSQL()
				require.NoError(t, err)
				assert.Contains(t, sql, "ORDER BY")
				assert.Contains(t, sql, "GROUP BY")
			})

			t.Run("legitimate_update_builds", func(t *testing.T) {
				qb := NewQueryBuilder(vendor)
				f := qb.Filter()
				sql, args, err := qb.Update(tableUsers).
					Set(colName, "Jane").
					SetMap(map[string]any{"email": "jane@example.com"}).
					Where(f.Eq(colID, 1)).ToSQL()
				require.NoError(t, err)
				assert.Contains(t, sql, "UPDATE users SET")
				assert.NotEmpty(t, args)
			})
		})
	}
}

// TestVendorSegmentCheckSkipsQuotedAndWildcard pins the two exemptions the
// vendor check deliberately keeps, each against its own BARE twin so the row
// proves the skip rather than the vendor. A quoted identifier is legal on both
// vendors whatever it contains — it is the framework's own reserved-word form —
// and the wildcard is not an identifier at all.
func TestVendorSegmentCheckSkipsQuotedAndWildcard(t *testing.T) {
	tests := []struct {
		name      string
		column    string
		wantError bool
	}{
		{name: "quoted_hash_is_accepted", column: `"a#b"`},
		{name: "bare_hash_is_refused", column: "a#b", wantError: true},
		{name: "qualified_quoted_hash_is_accepted", column: `u."a#b"`},
		{name: "qualified_bare_hash_is_refused", column: "u.a#b", wantError: true},
		{name: "wildcard_is_accepted", column: "*"},
		{name: "qualified_wildcard_is_accepted", column: "t.*"},
		{name: "qualified_wildcard_on_hashed_table_is_refused", column: "t#1.*", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qb := NewQueryBuilder(dbtypes.PostgreSQL)

			_, _, err := qb.Select(tt.column).From(tableUsers).ToSQL()

			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestIdentifierPatternGroupNamesAreKnown closes the gap between what
// validateVendorSegments' comment claims and what the code enforces. That helper
// finds identifier positions by reading the `ident` and `alias` groups, so a
// pattern that later grows a THIRD identifier-bearing group under some other
// name would be skipped silently — no compiler and no other test would notice.
// This asserts the closed vocabulary instead: every named group in every
// identifier pattern is either an identifier position (ident, alias) or one of
// the keyword tails the vendor grammar must NOT judge (dir, nulls).
func TestIdentifierPatternGroupNamesAreKnown(t *testing.T) {
	identifierPositions := map[string]bool{"ident": true, "alias": true}
	keywordTails := map[string]bool{"dir": true, "nulls": true}

	patterns := map[string]*regexp.Regexp{
		"validIdentifierPattern":       validIdentifierPattern,
		"validClauseIdentifierPattern": validClauseIdentifierPattern,
		"validSelectIdentifierPattern": validSelectIdentifierPattern,
		"validTableNamePattern":        validTableNamePattern,
	}

	for name, pattern := range patterns {
		t.Run(name, func(t *testing.T) {
			for _, group := range pattern.SubexpNames() {
				if group == "" {
					continue
				}
				assert.True(t, identifierPositions[group] || keywordTails[group],
					"group %q is neither a known identifier position nor a keyword tail: "+
						"if it holds an identifier, validateVendorSegments must be taught to read it", group)
			}
		})
	}
}
