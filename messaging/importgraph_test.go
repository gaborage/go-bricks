package messaging

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const sealedPackage = "github.com/gaborage/go-bricks/messaging/sealed"

var forbiddenJoseModules = []string{
	"github.com/go-jose",
	"github.com/gaborage/go-bricks/jose",
}

func isForbiddenJose(importPath string) bool {
	return slices.ContainsFunc(forbiddenJoseModules, func(forbidden string) bool {
		return importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/")
	})
}

// TestMessagingStaysJoseFree checks that no non-test code in messaging outside messaging/sealed imports
// go-jose or go-bricks/jose, transitively per go list and directly in every source file whatever its build constraints.
func TestMessagingStaysJoseFree(t *testing.T) {
	hits := append(transitiveJoseHits(t), directJoseImportHits(t)...)
	require.Empty(t, hits, "messaging packages or source files other than messaging/sealed link or import jose:\n%s", strings.Join(hits, "\n"))
}

func transitiveJoseHits(t *testing.T) []string {
	t.Helper()
	pkgs, err := exec.CommandContext(t.Context(), "go", "list", "github.com/gaborage/go-bricks/messaging/...").CombinedOutput()
	require.NoError(t, err, "go list failed: %s", pkgs)
	roots := slices.DeleteFunc(strings.Fields(string(pkgs)), func(pkg string) bool { return pkg == sealedPackage })

	args := append([]string{"list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}"}, roots...)
	out, err := exec.CommandContext(t.Context(), "go", args...).CombinedOutput()
	require.NoError(t, err, "go list -deps failed: %s", out)

	var hits []string
	for _, line := range strings.Split(string(out), "\n") {
		if isForbiddenJose(line) {
			hits = append(hits, "deps: "+line)
		}
	}
	return hits
}

func directJoseImportHits(t *testing.T) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == "sealed" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found, err := joseImportsIn(path)
		hits = append(hits, found...)
		return err
	})
	require.NoError(t, err)
	return hits
}

func joseImportsIn(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var hits []string
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		if isForbiddenJose(importPath) {
			hits = append(hits, path+": "+importPath)
		}
	}
	return hits, nil
}
