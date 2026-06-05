package sqlid

import "testing"

func TestValidateTableName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "gobricks_inbox", false},
		{"schema_qualified", "myschema.gobricks_inbox", false},
		{"dollar_hash", "outbox$events#1", false},
		{"empty", "", true},
		{"semicolon", "t; DROP TABLE x", true},
		{"comment_dashes", "t--x", true},
		{"block_comment_open", "t/*x", true},
		{"block_comment_close", "t*/x", true},
		{"three_parts", "a.b.c", true},
		{"leading_digit", "1table", true},
		{"space", "my table", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTableName(tc.input)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateTableName(%q) = nil, want error", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateTableName(%q) = %v, want nil", tc.input, err)
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
