package configdecode

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

	// canonicalPath is per-row because not every mirrored function lives in this package:
	// the slice hook's canonical copy is in config/, and leaving it out of this table is what
	// let it drift unchecked (ADR-078's rider).
	tests := []struct {
		name          string
		canonicalPath string // "" = this package's configdecode.go
		canonicalFunc string
		mirrorFunc    string
	}{
		{
			name:          "empty_string_to_scalar_guard",
			canonicalFunc: "EmptyStringToScalarGuardHookFunc",
			mirrorFunc:    "emptyStringToScalarGuardHookFunc",
		},
		{
			name:          "numeric_to_duration_guard",
			canonicalFunc: "NumericToDurationGuardHookFunc",
			mirrorFunc:    "numericToDurationGuardHookFunc",
		},
		{
			name:          "weak_scalar_kind_predicate",
			canonicalFunc: "isWeakScalarKind",
			mirrorFunc:    "isWeakScalarKind",
		},
		{
			// The comma-split hook. Its canonical half lives in config/, and the CLI carried
			// an INLINED copy of the split until ADR-078 split it back out — which is why
			// this row could not exist before and why the pair could drift silently.
			name:          "string_to_trimmed_slice_hook",
			canonicalPath: "../../config/config.go",
			canonicalFunc: "stringToTrimmedSliceHookFunc",
			mirrorFunc:    "stringToTrimmedSliceHookFunc",
		},
		{
			name:          "split_and_trim_list",
			canonicalPath: "../../config/converters.go",
			canonicalFunc: "splitAndTrimList",
			mirrorFunc:    "splitAndTrimList",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := canonical
			if tt.canonicalPath != "" {
				src = readSource(t, tt.canonicalPath)
			}
			want := funcBody(t, src, tt.canonicalFunc)
			got := funcBody(t, mirror, tt.mirrorFunc)

			require.Equal(t, want, got,
				"%s has drifted from its mirror in %s — change both, or resolve #1109",
				tt.canonicalFunc, mirrorPath)
		})
	}
}

// TestReadSourceNormalizesCRLF pins the normalization at its own seam — reading a genuinely
// CRLF-terminated file — because without it the drift test passes on Linux and fails on
// Windows, which is a worse failure than the drift it looks for.
func TestReadSourceNormalizesCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	require.NoError(t, os.WriteFile(path,
		[]byte("package x\r\n\r\nfunc sample() bool {\r\n\treturn true\r\n}\r\n"), 0o600))

	source := readSource(t, path)

	assert.NotContains(t, source, "\r")
	assert.Equal(t, "return true", funcBody(t, source, "sample"))
}

func readSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err, "the mirror moved; update mirrorPath or resolve #1109")
	// Normalize line endings: a Windows checkout hands these files back with CRLF, and a
	// parser that looks for "\n}\n" finds nothing there — the comparison would be
	// platform-dependent rather than drift-dependent.
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
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
