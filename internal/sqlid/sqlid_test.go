package sqlid

import (
	"fmt"
	"strings"
	"testing"
)

// TestValidateTableName is the parity fixture for the move onto
// database/identifier: the accept/reject verdicts were recorded by running the
// hand-written regexp this validator used beforehand, and only over_cap_part
// differs. Every row pins the exact error text, not just accept/reject,
// because outbox and inbox wrap this error with their own prefix and their
// tests match on it — so a reworded message is a break.
func TestValidateTableName(t *testing.T) {
	part128 := "a" + strings.Repeat("b", 127)
	part129 := "a" + strings.Repeat("b", 128)

	cases := []struct {
		name    string
		input   string
		wantErr string // empty means the input must be accepted
	}{
		{"simple", "gobricks_inbox", ""},
		{"schema_qualified", "myschema.gobricks_inbox", ""},
		{"dollar_hash", "outbox$events#1", ""},
		{"hash_mid", "a#b", ""},
		{"dollar_mid", "A$B", ""},
		{"leading_underscore", "_x", ""},
		{"two_parts", "x.y", ""},
		{"at_cap_part", part128, ""},
		{"empty", "", "table name must not be empty"},
		{"semicolon", "t; DROP TABLE x", `table name "t; DROP TABLE x" contains dangerous SQL characters`},
		{"trailing_semicolon", "x;", `table name "x;" contains dangerous SQL characters`},
		{"comment_dashes", "t--x", `table name "t--x" contains dangerous SQL characters`},
		{"block_comment_open", "t/*x", `table name "t/*x" contains dangerous SQL characters`},
		{"block_comment_close", "t*/x", `table name "t*/x" contains dangerous SQL characters`},
		{"three_parts", "a.b.c", `table name "a.b.c" has too many dot-separated parts (expected schema.table or table)`},
		{"three_parts_short", "x.y.z", `table name "x.y.z" has too many dot-separated parts (expected schema.table or table)`},
		{"double_dot", "..", `table name ".." has too many dot-separated parts (expected schema.table or table)`},
		{"leading_digit", "1table", `table name part "1table" contains invalid identifier characters`},
		{"digit_first", "9x", `table name part "9x" contains invalid identifier characters`},
		{"space", "my table", `table name part "my table" contains invalid identifier characters`},
		{"space_short", "x y", `table name part "x y" contains invalid identifier characters`},
		{"hyphen", "x-y", `table name part "x-y" contains invalid identifier characters`},
		{"quoted", `"x"`, `table name part "\"x\"" contains invalid identifier characters`},
		{"leading_dot", ".x", `table name part "" contains invalid identifier characters`},
		{"trailing_dot", "x.", `table name part "" contains invalid identifier characters`},
		{"unicode", "ünïcode", `table name part "ünïcode" contains invalid identifier characters`},
		// Anchor pins. Go's $ is \z, not \Z, so a trailing newline is refused;
		// a grammar swapped for a multiline-anchored one would smuggle a whole
		// second line past this validator and into the DDL it guards.
		{"trailing_newline", "ab\n", "table name part \"ab\\n\" contains invalid identifier characters"},
		{"embedded_newline", "a\nb", "table name part \"a\\nb\" contains invalid identifier characters"},
		{"embedded_tab", "a\tb", "table name part \"a\\tb\" contains invalid identifier characters"},
		{"embedded_nul", "a\x00b", "table name part \"a\\x00b\" contains invalid identifier characters"},
		// The one verdict composing on database/identifier changed: the Oracle
		// byte cap now refuses a part no supported server would accept anyway.
		{"over_cap_part", part129, fmt.Sprintf("table name part %q exceeds 128 bytes", part129)},
		// The cap is judged per part, not on the whole name: two at-cap parts
		// are a 257-byte name that is still accepted, while an over-cap schema
		// part is refused and names itself in the error.
		{"two_at_cap_parts", part128 + "." + part128, ""},
		{"over_cap_schema_part", part129 + "." + part128, fmt.Sprintf("table name part %q exceeds 128 bytes", part129)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTableName(tc.input)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateTableName(%q) = %v, want nil", tc.input, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateTableName(%q) = nil, want error %q", tc.input, tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("ValidateTableName(%q) error = %q, want %q", tc.input, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestIndexBaseName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"events", "events"},
		{"myschema.events", "events"},
		{"MYSCHEMA.OUTBOX_EVENTS", "OUTBOX_EVENTS"},
		{"", ""},
		{"a.b.c", "c"}, // last segment, even for multi-dot input
	}
	for _, c := range cases {
		if got := IndexBaseName(c.in); got != c.want {
			t.Fatalf("IndexBaseName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLeaderTableName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gobricks_outbox", "gobricks_outbox_leader"},
		{"myschema.outbox", "myschema.outbox_leader"},
	}
	for _, c := range cases {
		if got := LeaderTableName(c.in); got != c.want {
			t.Fatalf("LeaderTableName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
