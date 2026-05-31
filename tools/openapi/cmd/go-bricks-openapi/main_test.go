package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gaborage/go-bricks/tools/openapi/internal/testutil"
)

// withVersion temporarily sets the build-injected version global for a test.
// NOTE: mutates the package-level version; do not call from parallel tests.
func withVersion(t *testing.T, v string) {
	t.Helper()
	orig := version
	version = v
	t.Cleanup(func() { version = orig })
}

// TestRunVersionFlag verifies --version surfaces the injected version (cobra's
// builtin writes to the command's configured output, captured via SetOut).
func TestRunVersionFlag(t *testing.T) {
	withVersion(t, "test-9.9.9")
	var out, errBuf bytes.Buffer
	code := run([]string{"--version"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "test-9.9.9") {
		t.Errorf("--version output missing injected version: %q", out.String())
	}
}

// TestRunHelpListsSubcommands verifies all three subcommands are registered. It
// asserts each subcommand's distinctive Short description (which appears under
// "Available Commands:" only when the subcommand is registered) rather than the
// bare name — the name "version" also occurs in the --version flag line, so a
// name check would pass even if the version subcommand were dropped.
func TestRunHelpListsSubcommands(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"--help"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	// Distinctive Short strings, one per subcommand.
	for _, short := range []string{
		"Generate OpenAPI specification from go-bricks service",
		"Check environment and project compatibility",
		"Show version information",
	} {
		if !strings.Contains(out.String(), short) {
			t.Errorf("subcommand description %q not listed in --help:\n%s", short, out.String())
		}
	}
}

// TestRunVersionSubcommand verifies the version subcommand prints the injected
// version (it writes via fmt.Printf to os.Stdout, hence testutil.CaptureStdout).
func TestRunVersionSubcommand(t *testing.T) {
	withVersion(t, "sub-1.2.3")
	var out, errBuf bytes.Buffer
	var code int
	got := testutil.CaptureStdout(t, func() { code = run([]string{"version"}, &out, &errBuf) })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(got, "sub-1.2.3") {
		t.Errorf("version subcommand output missing injected version: %q", got)
	}
}

// TestRunGenerateHappyPath verifies a successful generate exits 0 and writes a
// parseable OpenAPI spec (not merely that the path exists).
func TestRunGenerateHappyPath(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "spec.yaml")
	var outBuf, errBuf bytes.Buffer
	var code int
	_ = testutil.CaptureStdout(t, func() {
		code = run([]string{"generate", "--project", dir, "--output", out}, &outBuf, &errBuf)
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errBuf.String())
	}

	content, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("expected spec at %s: %v", out, err)
	}
	if !strings.Contains(string(content), "openapi: 3.0.1") {
		t.Errorf("generated spec missing the OpenAPI version marker:\n%s", content)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(content, &doc); err != nil {
		t.Fatalf("generated spec is not valid YAML: %v", err)
	}
	if _, ok := doc["openapi"]; !ok {
		t.Errorf("generated spec has no top-level openapi key: %v", doc)
	}
}

// TestRunErrorPathSingleErrorNoUsage verifies the error path prints exactly one
// "Error:" line and no usage block (the SilenceErrors/SilenceUsage contract).
func TestRunErrorPathSingleErrorNoUsage(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	code := run([]string{"generate", "--project", filepath.Join(t.TempDir(), "does-not-exist"),
		"--output", filepath.Join(t.TempDir(), "x.yaml")}, &outBuf, &errBuf)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	stderr := errBuf.String()
	if n := strings.Count(stderr, "Error:"); n != 1 {
		t.Errorf("expected exactly one Error: line, got %d:\n%s", n, stderr)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Errorf("error path must not print a Usage: block:\n%s", stderr)
	}
}

// TestRunSingleDashLongFlagFails guards the single-dash trap deterministically: a
// single-dash long flag is NOT equivalent to its double-dash form. pflag parses
// -<long> as a cluster of shorthand flags, so for a long flag with no shorthand
// (--api-version) the first letter ('a') is an unknown shorthand and the run is
// rejected — proving single-dash long flags are not silently honored.
func TestRunSingleDashLongFlagFails(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	code := run([]string{"generate", "-api-version", "9.9.9"}, &outBuf, &errBuf)
	if code != 1 {
		t.Fatalf("expected exit 1 for a single-dash long flag, got %d (stderr: %s)", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "unknown shorthand flag") {
		t.Errorf("expected an 'unknown shorthand flag' parse error, got: %s", errBuf.String())
	}
}
