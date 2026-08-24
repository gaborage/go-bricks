package saferender

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const panMarker = "4111111111111111"

func TestRedactNamespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "no_brackets", input: "Req.Amount", want: "Req.Amount"},
		{name: "slice_index", input: "Req.Items[0]", want: "Req.Items[*]"},
		{name: "map_key", input: "Req.Limits[PAN-" + panMarker + "]", want: "Req.Limits[*]"},
		{name: "all_digit_map_key", input: "Req.Limits[" + panMarker + "]", want: "Req.Limits[*]"},
		{name: "nested_brackets", input: "Req.Grid[a[b]].Name", want: "Req.Grid[*].Name"},
		{name: "empty_brackets", input: "Req.Items[]", want: "Req.Items[*]"},
		{name: "multiple_segments", input: "Req.Items[0].Tags[secret]", want: "Req.Items[*]"},
		{name: "unterminated_bracket", input: "Req.Items[secret", want: "Req.Items[*]"},
		{name: "already_sanitized", input: "Req.Items[*]", want: "Req.Items[*]"},
		// Hostile keys: each defeats a depth counter, which returns to zero at
		// the key's own ']' and copies the remainder through.
		{name: "key_leads_with_closing_bracket", input: "Req.Limits[]" + panMarker + "]", want: "Req.Limits[*]"},
		{name: "key_embeds_closing_bracket", input: "Req.Limits[pan]=" + panMarker + "]", want: "Req.Limits[*]"},
		{name: "key_contains_dot", input: "Req.Limits[a.b]", want: "Req.Limits[*]"},
		{name: "leading_bracket", input: "[" + panMarker + "]", want: "[*]"},
		{name: "empty_key_keeps_trailing_path", input: "Req.Limits[].Amount", want: "Req.Limits[*].Amount"},
		{name: "empty_input", input: "", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RedactNamespace(tc.input))
		})
	}
}

func TestRedactLeafField(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain_field", input: "CreateReq.Amount", want: "Amount"},
		{name: "unqualified_field", input: "Amount", want: "Amount"},
		{name: "map_key", input: "CreateReq.Limits[" + panMarker + "]", want: "Limits[*]"},
		{name: "slice_index", input: "CreateReq.Items[0]", want: "Items[*]"},
		{name: "nested_path_keeps_tail", input: "Req.Items[0].Tags[x].Name", want: "Items[*].Name"},
		{name: "hostile_key_with_closing_bracket", input: "Req.Limits[]" + panMarker + "]", want: "Limits[*]"},
		{name: "key_contains_dot", input: "Req.Limits[a.b]", want: "Limits[*]"},
		{name: "unterminated_bracket", input: "Req.Limits[" + panMarker, want: "Limits[*]"},
		{name: "leading_bracket", input: "[" + panMarker + "]", want: "[*]"},
		// An empty key puts the closing bracket first in the remainder, so the
		// trailing field path survives only if the search is relative to it.
		{name: "empty_key_keeps_trailing_path", input: "Req.Limits[].Amount", want: "Limits[*].Amount"},
		// A leading '.' puts the separator at index 0, the boundary an
		// off-by-one in the leaf search would wave through.
		{name: "leading_dot", input: ".Amount", want: "Amount"},
		{name: "empty_input", input: "", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactLeafField(tc.input)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, panMarker)
		})
	}
}

type mapPayload struct {
	Limits map[string]int `json:"limits"`
}

type schemaPayload struct {
	Amount int64  `json:"amount"`
	Name   string `json:"name"`
}

type slicePayload struct {
	Rows []mapPayload `json:"rows"`
}

type embeddedMapPayload struct {
	mapPayload
	Name string `json:"name"`
}

type interfacePayload struct {
	Extra any `json:"extra"`
}

type customUnmarshaler struct{ N int }

func (c *customUnmarshaler) UnmarshalJSON(b []byte) error {
	var m map[string]int

	return json.Unmarshal(b, &m)
}

type wrapsCustomUnmarshaler struct {
	Inner customUnmarshaler `json:"inner"`
}

type timePayload struct {
	At     time.Time `json:"at"`
	Amount int64     `json:"amount"`
}

type selfRefNoMap struct {
	Next *selfRefNoMap `json:"next"`
}

type selfRefWithMap struct {
	Next   *selfRefWithMap `json:"next"`
	Limits map[string]int  `json:"limits"`
}

func TestFieldPathIsSchema(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want bool
	}{
		{name: "map_free_struct", typ: reflect.TypeFor[schemaPayload](), want: true},
		{name: "map_at_top_level", typ: reflect.TypeFor[map[string]int]()},
		{name: "map_field", typ: reflect.TypeFor[mapPayload]()},
		{name: "map_behind_pointer", typ: reflect.TypeFor[*mapPayload]()},
		{name: "map_inside_slice_element_struct", typ: reflect.TypeFor[slicePayload]()},
		{name: "map_as_map_value_type", typ: reflect.TypeFor[map[string]schemaPayload]()},
		{name: "embedded_struct_carrying_a_map", typ: reflect.TypeFor[embeddedMapPayload]()},
		{name: "interface_field", typ: reflect.TypeFor[interfacePayload]()},
		{name: "nested_custom_unmarshaler", typ: reflect.TypeFor[wrapsCustomUnmarshaler]()},
		{name: "custom_unmarshaler_itself", typ: reflect.TypeFor[customUnmarshaler]()},
		{name: "stdlib_unmarshaler_field", typ: reflect.TypeFor[timePayload]()},
		{name: "self_referential_without_map", typ: reflect.TypeFor[selfRefNoMap](), want: true},
		{name: "self_referential_with_map", typ: reflect.TypeFor[selfRefWithMap]()},
		{name: "nil_type", typ: nil, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, FieldPathIsSchema(tc.typ))
		})
	}
}

// TestJSONDecodeSummary drives every case through a REAL decoder, so a rendering
// that leaks cannot pass by a hand-built literal that happens to be clean.
func TestJSONDecodeSummary(t *testing.T) {
	t.Run("type_mismatch_gated_off_withholds_the_field", func(t *testing.T) {
		var dest mapPayload
		err := json.Unmarshal([]byte(`{"limits":{"`+panMarker+`":"x"}}`), &dest)
		summary := JSONDecodeSummary(err, FieldPathIsSchema(reflect.TypeFor[mapPayload]()))

		assert.True(t, strings.HasPrefix(summary, "json: type mismatch (want int, offset "), "got %q", summary)
		assert.NotContains(t, summary, panMarker)
		assert.NotContains(t, summary, "field")
	})

	t.Run("type_mismatch_gated_on_keeps_the_field", func(t *testing.T) {
		var dest schemaPayload
		err := json.Unmarshal([]byte(`{"amount":"`+panMarker+`"}`), &dest)
		summary := JSONDecodeSummary(err, FieldPathIsSchema(reflect.TypeFor[schemaPayload]()))

		assert.Contains(t, summary, `field "amount"`)
		assert.NotContains(t, summary, panMarker)
	})

	t.Run("syntax_error_renders_offset_only", func(t *testing.T) {
		var dest schemaPayload
		err := json.Unmarshal([]byte(`{"amount":`+panMarker+`x}`), &dest)
		summary := JSONDecodeSummary(err, true)

		assert.True(t, strings.HasPrefix(summary, "json: syntax error at offset "), "got %q", summary)
		assert.NotContains(t, summary, panMarker)
	})

	t.Run("wrapped_cause_is_still_matched", func(t *testing.T) {
		var dest schemaPayload
		err := json.Unmarshal([]byte(`{"amount":"1"}`), &dest)
		summary := JSONDecodeSummary(fmt.Errorf("bind: %w", err), true)

		assert.Contains(t, summary, "json: type mismatch")
	})

	t.Run("unaudited_shape_renders_nothing", func(t *testing.T) {
		assert.Empty(t, JSONDecodeSummary(errors.New("json: unknown field \"secret\""), true))
		assert.Empty(t, JSONDecodeSummary(nil, true))
	})

	t.Run("nil_type_renders_unknown", func(t *testing.T) {
		assert.Equal(t, "json: type mismatch (want unknown, offset 0)",
			JSONDecodeSummary(&json.UnmarshalTypeError{}, true))
	})
}
