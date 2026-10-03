package observability

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// semconvVersionPattern captures the version segment of a semantic-convention
// import. Subpackages (…/httpconv) carry the same segment.
var semconvVersionPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(semconvPathFragment) + `/(v1\.\d+\.\d+)(?:/|$)`)

// TestSemconvImportsShareOneVersion keeps the framework on a single semantic
// conventions version. No linter enforces it: the import-alias linter checks
// aliases only, so a second version would merge silently.
func TestSemconvImportsShareOneVersion(t *testing.T) {
	versions, scanned := semconvVersionsUnder(t, repoRoot(t))

	require.Greater(t, scanned, minScannedGoFiles, "walker scanned too few files to have covered the module")
	assert.Len(t, versions, 1, "semconv imports must share one version")
}

// semconvVersionsUnder maps every semconv version segment imported under root to
// the first file, relative to root, that imports it, and returns the number of
// files parsed.
func semconvVersionsUnder(t *testing.T, root string) (versions map[string]string, scanned int) {
	t.Helper()
	versions = map[string]string{}
	scanned = walkModuleGoFiles(t, root, token.NewFileSet(), parser.ImportsOnly, func(rel string, file *ast.File) {
		for _, imp := range file.Imports {
			match := semconvVersionPattern.FindStringSubmatch(strings.Trim(imp.Path.Value, `"`))
			if match == nil {
				continue
			}
			if _, seen := versions[match[1]]; !seen {
				versions[match[1]] = rel
			}
		}
	})
	return versions, scanned
}

// TestSemconvVersionsUnderJudgesPlantedTrees runs the guard's walk over planted
// trees: a stale version behind a build tag must still split the set, and a
// subpackage of the current version must not.
func TestSemconvVersionsUnderJudgesPlantedTrees(t *testing.T) {
	const current = "package p\n\nimport semconv \"go.opentelemetry.io/otel/semconv/v1.43.0\"\n"

	tests := []struct {
		name     string
		files    map[string]string
		versions []string
	}{
		{
			name: "stale_import_behind_integration_tag_splits_the_version",
			files: map[string]string{
				"current.go":                  current,
				"planted_integration_test.go": "//go:build integration\n\npackage p\n\nimport semconv \"go.opentelemetry.io/otel/semconv/v1.32.0\"\n",
			},
			versions: []string{"v1.32.0", "v1.43.0"},
		},
		{
			name: "subpackage_of_the_current_version_is_the_same_version",
			files: map[string]string{
				"current.go": current,
				"client.go":  "package p\n\nimport \"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv\"\n",
			},
			versions: []string{"v1.43.0"},
		},
		{
			name: "non_semconv_import_is_ignored",
			files: map[string]string{
				"attr.go": "package p\n\nimport \"go.opentelemetry.io/otel/attribute\"\n",
			},
			versions: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, src := range tt.files {
				require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(src), 0o600))
			}

			versions, scanned := semconvVersionsUnder(t, root)

			assert.Equal(t, len(tt.files), scanned)
			assert.ElementsMatch(t, tt.versions, slices.Collect(maps.Keys(versions)))
		})
	}
}
