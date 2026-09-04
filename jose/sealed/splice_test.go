package sealed

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocateSubjectFindsSpanExactly(t *testing.T) {
	cases := []struct {
		name  string
		doc   string
		path  string
		value string
	}{
		{name: "middle_object", doc: `{"a":1,"card":{"pan":"4111"},"z":true}`, path: "card", value: `{"pan":"4111"}`},
		{name: "first_string", doc: `{"card":"x","a":1}`, path: "card", value: `"x"`},
		{name: "last_null", doc: `{"a":1,"card":null}`, path: "card", value: `null`},
		{name: "array_value", doc: `{"card":[1,2,{"n":[]}]}`, path: "card", value: `[1,2,{"n":[]}]`},
		{name: "nested_same_name_ignored", doc: `{"a":{"card":"inner"},"card":"outer"}`, path: "card", value: `"outer"`},
		{name: "escaped_key", doc: `{"card":"v"}`, path: "card", value: `"v"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			span, err := locateSubject([]byte(tc.doc), tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.value, string(span.value))
			assert.Equal(t, tc.value, tc.doc[span.start:span.end], "span must address the value bytes")
			assert.Equal(t, span.end-span.start, len(span.value))
		})
	}
}

func TestLocateSubjectRejectsBadDocuments(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want error
	}{
		{name: "not_object_null", doc: `null`, want: errDocNotObject},
		{name: "not_object_array", doc: `[{"card":1}]`, want: errDocNotObject},
		{name: "empty", doc: ``, want: errDocNotObject},
		{name: "truncated", doc: `{"card":`, want: errDocNotObject},
		{name: "truncated_after_comma", doc: `{"a":1,`, want: errDocNotObject},
		{name: "garbage_key", doc: `{"a":1,x}`, want: errDocNotObject},
		{name: "missing_comma", doc: `{"a":1 "card":2}`, want: errDocNotObject},
		{name: "unterminated_object", doc: `{"card":"x"`, want: errDocNotObject},
		{name: "closed_with_bracket", doc: `{"card":"x"]`, want: errDocNotObject},
		{name: "subject_absent", doc: `{"a":1}`, want: errSubjectAbsent},
		{name: "only_nested_namesake", doc: `{"a":{"card":"inner"}}`, want: errSubjectAbsent},
		{name: "namesake_inside_array", doc: `{"a":[{"card":"inner"}]}`, want: errSubjectAbsent},
		{name: "namesake_inside_string", doc: `{"a":"\"card\":1"}`, want: errSubjectAbsent},
		{name: "subject_duplicate", doc: `{"card":1,"card":2}`, want: errSubjectDuplicate},
		{name: "trailing_content", doc: `{"card":1} {"x":2}`, want: errDocTrailingContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := locateSubject([]byte(tc.doc), "card")
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestSpliceReplacesOnlyTheSpan(t *testing.T) {
	doc := []byte(`{"a":1,"card":{"pan":"4111"},"z":true}`)
	span, err := locateSubject(doc, "card")
	require.NoError(t, err)
	out, err := splice(doc, span, "eyJ.hdr.body")
	require.NoError(t, err)
	assert.Equal(t, `{"a":1,"card":"eyJ.hdr.body","z":true}`, string(out))
	assert.Equal(t, `{"a":1,"card":{"pan":"4111"},"z":true}`, string(doc), "input must not be mutated")
	assert.True(t, json.Valid(out))
}

func TestSpliceRawInsertsReplacementVerbatim(t *testing.T) {
	doc := []byte(`{"a":1,"card":"eyJ.x.y","z":true}`)
	span, err := locateSubject(doc, "card")
	require.NoError(t, err)
	out := spliceRaw(doc, span, []byte(`{"pan":"4111"}`))
	assert.Equal(t, `{"a":1,"card":{"pan":"4111"},"z":true}`, string(out))
	assert.Equal(t, `{"a":1,"card":"eyJ.x.y","z":true}`, string(doc))
}

func TestSpliceRawHandlesLargeInputs(t *testing.T) {
	// A document and a replacement far beyond any realistic event (multi-MiB each): the
	// splice must be exact with no size arithmetic in play.
	big := bytes.Repeat([]byte("x"), 4<<20)
	doc := append(append([]byte(`{"pad":"`), big...), []byte(`","card":"old","z":1}`)...)
	span, err := locateSubject(doc, "card")
	require.NoError(t, err)
	replacement := append(append([]byte(`"`), bytes.Repeat([]byte("A"), 3<<20)...), '"')
	out := spliceRaw(doc, span, replacement)
	assert.Len(t, out, len(doc)-len(span.value)+len(replacement))
	assert.True(t, bytes.HasPrefix(out, doc[:span.start]))
	assert.True(t, bytes.HasSuffix(out, doc[span.end:]))
	assert.Equal(t, replacement, out[span.start:span.start+len(replacement)])
	assert.True(t, json.Valid(out))
}

func TestSpliceRefusesNonCompactReplacements(t *testing.T) {
	doc := []byte(`{"card":1}`)
	span, err := locateSubject(doc, "card")
	require.NoError(t, err)
	for name, bad := range map[string]string{"empty": "", "quote": `a"b`, "space": "a b", "plus": "a+b", "slash": "a/b", "equals": "a=", "unicode": "é"} {
		t.Run(name, func(t *testing.T) {
			out, err := splice(doc, span, bad)
			assert.Nil(t, out)
			assert.ErrorIs(t, err, errNotCompactJOSE)
		})
	}
}

func TestIsCompactJOSEAcceptsEveryBase64URLByteAndDots(t *testing.T) {
	assert.True(t, isCompactJOSE("ABCXYZabcxyz0189-_.-_."))
	assert.False(t, isCompactJOSE("ABC~"))
	assert.False(t, isCompactJOSE("ABC@"))
	assert.False(t, isCompactJOSE("AB[C"))
	assert.False(t, isCompactJOSE("AB`C"))
	assert.False(t, isCompactJOSE("AB{C"))
	assert.False(t, isCompactJOSE("AB/C"))
	assert.False(t, isCompactJOSE("AB:C"))
}
