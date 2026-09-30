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
	pkgs, err := exec.CommandContext(t.Context(), "go", "list", "github.com/gaborage/go-bricks/messaging/...").CombinedOutput()
	require.NoError(t, err, "go list failed: %s", pkgs)
	roots := slices.DeleteFunc(strings.Fields(string(pkgs)), func(pkg string) bool { return pkg == sealedPackage })

	args := append([]string{"list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}"}, roots...)
	out, err := exec.CommandContext(t.Context(), "go", args...).CombinedOutput()
	require.NoError(t, err, "go list -deps failed: %s", out)

	var hits []string
	for _, line := range strings.Split(string(out), "\n") {
		if isForbiddenJose(line) {
			hits = append(hits, line)
		}
	}
	require.Empty(t, hits, "messaging packages other than messaging/sealed link jose:\n%s", strings.Join(hits, "\n"))

	var direct []string
	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
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
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if isForbiddenJose(importPath) {
				direct = append(direct, path+": "+importPath)
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, direct, "messaging source files other than messaging/sealed import jose:\n%s", strings.Join(direct, "\n"))
}
