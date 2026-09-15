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
		// The key is compared after JSON decoding: a unicode escape and an escaped
		// quote in the raw key must still match the path.
		{name: "escaped_key", doc: `{"c\u0061rd":"v"}`, path: "card", value: `"v"`},
		{name: "escaped_quote_in_key", doc: `{"a":1,"card\"x":"v"}`, path: `card"x`, value: `"v"`},
		{name: "escaped_key_not_confused_with_raw_namesake", doc: `{"c\u0061rd":"first","x":{"card":"inner"}}`, path: "card", value: `"first"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			span, err := locateSubject([]byte(tc.doc), tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.value, string(span.value))
			assert.Equal(t, tc.value, tc.doc[span.start:span.end], "span must address the value bytes")
			assert.Len(t, span.value, span.end-span.start)
		})
	}
}

// TestLocateSubjectIgnoresCaseFoldTwins is the opener's half of the G9 rule: walkToSubject
// refuses a clear twin only for the sealer (pinSubject passes true), so locateSubject — the
// door Open goes through at rule 10 — must keep returning the span. Without this, a change
// that made the opener ask for the rule too would refuse messages sealed before the rule
// existed, and no other test would notice.
func TestLocateSubjectIgnoresCaseFoldTwins(t *testing.T) {
	cases := []struct {
		name  string
		doc   string
		value string
	}{
		{name: "twin_before_the_subject", doc: `{"Card":"clear","card":"sealed"}`, value: `"sealed"`},
		{name: "twin_after_the_subject", doc: `{"card":"sealed","CARD":"clear"}`, value: `"sealed"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			span, err := locateSubject([]byte(tc.doc), "card")
			require.NoError(t, err, "the opener judges no case-fold twin")
			assert.Equal(t, tc.value, string(span.value))

			_, sealErr := pinSubject([]byte(tc.doc), "card")
			assert.ErrorIs(t, sealErr, errSubjectCaseFoldTwin, "the sealer does")
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
	assert.Equal(t, `{"a":1,"card":"eyJ.hdr.body","z":true}`, string(out))                              //nolint:testifylint // byte-exact output is the property; JSONEq ignores key order
	assert.Equal(t, `{"a":1,"card":{"pan":"4111"},"z":true}`, string(doc), "input must not be mutated") //nolint:testifylint // asserts the input buffer is byte-identical
	assert.True(t, json.Valid(out))
}

func TestSpliceRawInsertsReplacementVerbatim(t *testing.T) {
	doc := []byte(`{"a":1,"card":"eyJ.x.y","z":true}`)
	span, err := locateSubject(doc, "card")
	require.NoError(t, err)
	out := spliceRaw(doc, span, []byte(`{"pan":"4111"}`))
	assert.Equal(t, `{"a":1,"card":{"pan":"4111"},"z":true}`, string(out)) //nolint:testifylint // verbatim insertion is a byte-level property
	assert.Equal(t, `{"a":1,"card":"eyJ.x.y","z":true}`, string(doc))      //nolint:testifylint // asserts the input buffer is byte-identical
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

// TestRemoveMemberDeletesTheMemberAndOneSeparator covers the three separator shapes: a
// leading comma to swallow (middle or last member), a trailing comma to swallow (first
// member, more follow), and no comma at all (the only member) — every result must stay
// valid JSON with every other byte untouched. The whitespace cases are the regression net
// for the first-member fixup: the decoder's peek skips whitespace, so any padding between
// the opening brace and the subject key must not defeat it and emit `{ ,…`. A middle member
// keeps the padding before its swallowed leading comma, which stays valid either way.
func TestRemoveMemberDeletesTheMemberAndOneSeparator(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{name: "only_member", doc: `{"card":{"pan":"4111"}}`, want: `{}`},
		{name: "first_of_two", doc: `{"card":{"pan":"4111"},"z":true}`, want: `{"z":true}`},
		{name: "first_of_three_with_whitespace", doc: `{"card":1  ,  "a":2,"z":3}`, want: `{"a":2,"z":3}`},
		{name: "leading_whitespace_first_member", doc: `{ "card":1,"z":2}`, want: `{ "z":2}`},
		{name: "leading_newline_first_member", doc: "{\n  \"card\": 1,\n  \"z\": 2\n}", want: "{\n  \"z\": 2\n}"},
		{name: "leading_crlf_first_member", doc: "{\r\n\"card\":1,\r\n\"z\":2}", want: "{\r\n\"z\":2}"},
		{name: "leading_whitespace_only_member", doc: "{\n  \"card\": 1\n}", want: "{\n  \n}"},
		{name: "middle", doc: `{"a":1,"card":{"pan":"4111"},"z":true}`, want: `{"a":1,"z":true}`},
		{name: "middle_whitespace_before_the_comma", doc: `{"a":1 , "card":2, "z":3}`, want: `{"a":1 , "z":3}`},
		{name: "middle_whitespace_on_both_sides", doc: `{"a":1 , "card":2 , "z":3}`, want: `{"a":1  , "z":3}`},
		{name: "last", doc: `{"a":1,"z":true,"card":{"pan":"4111"}}`, want: `{"a":1,"z":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := []byte(tc.doc)
			span, err := locateSubject(doc, "card")
			require.NoError(t, err)
			out := removeMember(doc, span)
			assert.Equal(t, []byte(tc.want), out)
			assert.True(t, json.Valid(out))
			assert.Equal(t, tc.doc, string(doc), "input must not be mutated")
		})
	}
}

func TestNextMemberReadsOneMember(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		key     string
		value   string
		wantErr error
	}{
		{name: "string_value", doc: `{"card":"x"}`, key: "card", value: `"x"`},
		{name: "object_value", doc: `{"card":{"pan":"4111"},"z":1}`, key: "card", value: `{"pan":"4111"}`},
		{name: "escaped_key_decoded", doc: `{"c\u0061rd":1}`, key: "card", value: `1`},
		{name: "truncated_key", doc: `{"card`, wantErr: errDocNotObject},
		{name: "missing_value", doc: `{"card":}`, wantErr: errDocNotObject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := json.NewDecoder(bytes.NewReader([]byte(tc.doc)))
			require.NoError(t, expectDelim(dec, '{'))
			key, raw, err := nextMember(dec)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, key)
				assert.Nil(t, raw)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.key, key)
			assert.Equal(t, tc.value, string(raw))
		})
	}
}

func TestFinishWalkJudgesTheEndOfTheWalk(t *testing.T) {
	span := subjectSpan{value: json.RawMessage(`1`), start: 5, end: 6}
	cases := []struct {
		name  string
		doc   string
		drain bool
		found *subjectSpan
		want  error
	}{
		{name: "closed_with_subject", doc: `{"a":1}`, drain: true, found: &span},
		{name: "closed_without_subject", doc: `{"a":1}`, drain: true, want: errSubjectAbsent},
		{name: "trailing_content", doc: `{"a":1} {"b":2}`, drain: true, found: &span, want: errDocTrailingContent},
		// The close is judged before the find: a document that never closed with a brace is
		// refused even though the Subject was already located.
		{name: "not_closed_by_brace", doc: `[`, found: &span, want: errDocNotObject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := json.NewDecoder(bytes.NewReader([]byte(tc.doc)))
			if tc.drain {
				require.NoError(t, expectDelim(dec, '{'))
				for dec.More() {
					_, _, err := nextMember(dec)
					require.NoError(t, err)
				}
			}
			got, err := finishWalk(dec, tc.found)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				assert.Equal(t, subjectSpan{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, *tc.found, got)
		})
	}
}
