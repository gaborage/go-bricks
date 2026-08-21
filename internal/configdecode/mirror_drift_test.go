package configdecode

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The tools/migration CLI is a separate module that keeps byte-identical copies of these
// hooks rather than importing this package (see #1109 for whether that stays). Nothing
// gated the promise: the two test suites exercise each copy separately, so a change here
// leaves the CLI's copy green while the two behave differently — and a tenants.yaml would
// then decode under different rules than a framework config.
//
// This compares the bodies as source text, which needs no import and therefore does not
// prejudge #1109. It reads the mirror by path; the two live in one repository.
const mirrorPath = "../../tools/migration/internal/commands/common.go"

func TestMirroredHooksHaveNotDrifted(t *testing.T) {
	canonical := readSource(t, "configdecode.go")
	mirror := readSource(t, mirrorPath)

	tests := []struct {
		name          string
		canonicalFunc string
		mirrorFunc    string
	}{
		{
			name:          "empty_string_to_numeric_guard",
			canonicalFunc: "EmptyStringToNumericGuardHookFunc",
			mirrorFunc:    "emptyStringToNumericGuardHookFunc",
		},
		{
			name:          "numeric_to_duration_guard",
			canonicalFunc: "NumericToDurationGuardHookFunc",
			mirrorFunc:    "numericToDurationGuardHookFunc",
		},
		{
			name:          "numeric_kind_predicate",
			canonicalFunc: "isNumericKind",
			mirrorFunc:    "isNumericKind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := funcBody(t, canonical, tt.canonicalFunc)
			got := funcBody(t, mirror, tt.mirrorFunc)

			require.Equal(t, want, got,
				"%s has drifted from its mirror in %s — change both, or resolve #1109",
				tt.canonicalFunc, mirrorPath)
		})
	}
}

func readSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err, "the mirror moved; update mirrorPath or resolve #1109")
	return string(raw)
}

// funcBody returns everything between a function's signature and its closing brace, with
// comments and blank lines dropped: the mirror renames the exported hooks and carries its
// own doc comments, so only the statements are comparable.
func funcBody(t *testing.T, source, name string) string {
	t.Helper()

	start := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(`)
	loc := start.FindStringIndex(source)
	require.NotNil(t, loc, "func %s not found", name)

	rest := source[loc[0]:]
	end := strings.Index(rest, "\n}\n")
	require.NotEqual(t, -1, end, "func %s is not terminated", name)

	var kept []string
	for _, line := range strings.Split(rest[:end], "\n")[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		kept = append(kept, trimmed)
	}
	return strings.Join(kept, "\n")
}
