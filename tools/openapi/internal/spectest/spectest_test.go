package spectest

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// update regenerates the golden files instead of comparing against them.
// Run: go test ./internal/spectest -update
var update = flag.Bool("update", false, "update golden expected.yaml files")

const (
	testdataDir = "testdata"
	goldenFile  = "expected.yaml"
)

// TestGoldenFixtures runs every testdata/<case>/ project through the full
// analyze->generate pipeline, validates the emitted document as OpenAPI 3.0,
// and compares it (line-ending-normalized) against the checked-in golden file.
// This is the regression net every later generator change relies on: a fidelity
// fix shows up as a reviewable golden diff, and a structural regression fails
// Validate.
func TestGoldenFixtures(t *testing.T) {
	entries, err := os.ReadDir(testdataDir)
	require.NoError(t, err)

	ran := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ran++

		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(testdataDir, name)

			spec, genErr := Generate(t.Context(), dir)
			require.NoError(t, genErr)

			// Primary gate: the emitted document must be valid OpenAPI 3.0.
			require.NoError(t, Validate(t.Context(), []byte(spec)), "emitted spec is not valid OpenAPI 3.0")

			goldenPath := filepath.Join(dir, goldenFile)
			if *update {
				require.NoError(t, os.WriteFile(goldenPath, []byte(spec), 0o600))
				return
			}

			want, readErr := os.ReadFile(goldenPath)
			require.NoError(t, readErr, "missing golden file; run `go test ./internal/spectest -update`")
			// Compare with line endings normalized: the generator always emits LF,
			// but git may check the golden out with CRLF on Windows (autocrlf).
			assert.Equal(t, normalizeEOL(string(want)), normalizeEOL(spec),
				"generated spec drifted from golden; run `go test ./internal/spectest -update` to refresh")
		})
	}

	require.Positive(t, ran, "no fixtures found under testdata/")
}

// normalizeEOL collapses CRLF to LF so golden comparisons are stable across
// platforms. The generator emits LF; only the checked-out golden can carry CRLF.
func normalizeEOL(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// TestValidateRejectsMalformedSpec locks the negative arm of Validate so the
// harness cannot silently degrade into a no-op gate that passes everything.
func TestValidateRejectsMalformedSpec(t *testing.T) {
	t.Run("not_yaml", func(t *testing.T) {
		require.Error(t, Validate(t.Context(), []byte(":\n  not: [valid")))
	})
	t.Run("missing_required_fields", func(t *testing.T) {
		// Parses as YAML but is not a valid OpenAPI document (no openapi/info/paths).
		require.Error(t, Validate(t.Context(), []byte("foo: bar\n")))
	})
}

// TestGenerateEmptyProject exercises Generate on a directory with no modules so
// the helper's success path is covered independently of the golden fixtures. A
// project with zero discovered modules is not an error at the analyzer layer
// (the CLI surfaces that separately); the helper must still return a document.
func TestGenerateEmptyProject(t *testing.T) {
	spec, err := Generate(t.Context(), t.TempDir())
	require.NoError(t, err)
	require.NotEmpty(t, spec)
}
