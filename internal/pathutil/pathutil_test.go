package pathutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnsureLeadingSlash(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty_becomes_root", in: "", want: "/"},
		{name: "missing_slash_added", in: "foo", want: "/foo"},
		{name: "existing_slash_preserved", in: "/foo", want: "/foo"},
		{name: "root_preserved", in: "/", want: "/"},
		{name: "nested_path_preserved", in: "/foo/bar", want: "/foo/bar"},
		{name: "trailing_slash_preserved", in: "/foo/", want: "/foo/"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, EnsureLeadingSlash(tc.in))
		})
	}
}

func TestNormalizePrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty_collapses", in: "", want: ""},
		{name: "root_collapses", in: "/", want: ""},
		{name: "leading_slash_added", in: "foo", want: "/foo"},
		{name: "already_normalized", in: "/foo", want: "/foo"},
		{name: "trailing_slash_stripped", in: "/foo/", want: "/foo"},
		{name: "both_added_and_stripped", in: "foo/", want: "/foo"},
		{name: "deep_path_normalized", in: "foo/bar/", want: "/foo/bar"},
		{name: "multiple_trailing_slashes_stripped", in: "/foo///", want: "/foo"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizePrefix(tc.in))
		})
	}
}

func TestStripPathPrefix(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		prefix  string
		wantStr string
		wantOk  bool
	}{
		{name: "empty_prefix_returns_path_false", path: "/foo/bar", prefix: "", wantStr: "/foo/bar", wantOk: false},
		{name: "exact_match_returns_empty_true", path: "/foo", prefix: "/foo", wantStr: "", wantOk: true},
		{name: "child_path_returns_remainder", path: "/foo/bar", prefix: "/foo", wantStr: "/bar", wantOk: true},
		{name: "deep_child_returns_remainder", path: "/foo/bar/baz", prefix: "/foo", wantStr: "/bar/baz", wantOk: true},
		{name: "non_matching_returns_path_false", path: "/other", prefix: "/foo", wantStr: "/other", wantOk: false},
		{name: "partial_word_match_rejected", path: "/foobar", prefix: "/foo", wantStr: "/foobar", wantOk: false},
		{name: "partial_word_match_with_slash_rejected", path: "/foobar/baz", prefix: "/foo", wantStr: "/foobar/baz", wantOk: false},
		{name: "trailing_slash_path_under_prefix", path: "/foo/", prefix: "/foo", wantStr: "/", wantOk: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotStr, gotOk := StripPathPrefix(tc.path, tc.prefix)
			assert.Equal(t, tc.wantOk, gotOk)
			assert.Equal(t, tc.wantStr, gotStr)
		})
	}
}
