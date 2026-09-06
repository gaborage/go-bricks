package observability

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minScannedGoFiles guards against a walker that silently scans nothing: the
// framework module carries several hundred .go files, so a run that parses
// fewer than this found the wrong root or skipped everything.
const minScannedGoFiles = 500

// attributePackagePath is the package whose constructors build span attributes.
const attributePackagePath = "go.opentelemetry.io/otel/attribute"

// scanSkipDirs are directory names never descended into at any depth: testdata/
// holds deliberately malformed or illustrative sources, and the rest are not
// source at all. tools/ is handled by skipDir, anchored to the module root.
var scanSkipDirs = map[string]bool{
	"testdata": true,
	".git":     true,
	".claude":  true,
	"vendor":   true,
}

// TestNoErrorMessageReachesSpanAttributes machine-checks the attribute half of
// ADR-083: a span attribute renders an error's CLASSIFICATION, never its
// message. The Consequences section of that ADR prescribed a grep for this;
// this scan is the gate and the grep is now the fallback.
//
// The check is SYNTACTIC — it parses sources, it does not type-check them — and
// it is keyed on the `.Error()` CALL, never on the identifier `err`, so the
// legitimate sites that pass an error into a classifier (`classifyError(err)`,
// `fmt.Sprintf("%T", err)`, `apiErr.ErrorCode()`) stay silent. It anchors on the
// attribute constructor itself, so nesting context is irrelevant: the direct
// form and the calls wrapped in `SetAttributes`, `AddEvent` or `WithAttributes`
// are the same finding and no enumeration of wrappers can go stale.
//
// Two evasions are out of reach and are documented gaps rather than work:
//
//   - aliased evasion — `msg := err.Error()` and then `attribute.String(k, msg)`,
//     because the alias carries no syntax this scan can key on;
//   - an error-typed operand passed without `.Error()` — `attribute.Stringer(k, err)`
//     — because judging it needs a type-checked load, which is its own change.
func TestNoErrorMessageReachesSpanAttributes(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	scanned, constructors := 0, 0

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		if entry.IsDir() {
			if skipDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		scanned++

		findings, seen := scanAttributeCalls(file)
		constructors += seen
		for _, pos := range findings {
			t.Errorf("%s:%d: an error's message reaches a span attribute; render a classification instead (ADR-083)",
				rel, fset.Position(pos).Line)
		}
		return nil
	})
	require.NoError(t, err)

	t.Logf("parsed %d Go files under %s, %d attribute constructor calls seen", scanned, root, constructors)
	require.Greater(t, scanned, minScannedGoFiles, "walker scanned too few files to have covered the module")
	// Positive control: a predicate that stopped recognizing attribute
	// constructors would report nothing over a tree that still parses fine, so
	// silence alone cannot tell "the invariant holds" from "the scan is looking
	// for a spelling nobody uses" (ADR-083 asks its checks to carry one).
	require.NotZero(t, constructors, "predicate recognized no attribute constructor anywhere; the scan is looking for the wrong spelling")
}

// skipDir reports whether a directory, named by its path relative to the module
// root, is never descended into. tools/ is a separate Go module and is anchored
// to the root so a future internal/tools/ stays covered; testdata/, vendor/ and
// the non-source directories are matched by name at any depth.
func skipDir(rel string) bool {
	if rel == "." {
		return false
	}
	if rel == "tools" {
		return true
	}
	return scanSkipDirs[filepath.Base(rel)]
}

// scanAttributeCalls returns the position of every attribute.* constructor call
// in file that carries a zero-argument .Error() call anywhere inside its
// arguments, and the number of attribute constructor calls seen at all — the
// latter is the predicate's positive control.
func scanAttributeCalls(file *ast.File) (findings []token.Pos, constructors int) {
	names, dotImported := attributeBindings(file)
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isAttributeConstructor(call.Fun, names, dotImported) {
			return true
		}
		constructors++
		if slices.ContainsFunc(call.Args, containsErrorMessageCall) {
			findings = append(findings, call.Pos())
		}
		return true
	})
	return findings, constructors
}

// attributeBindings returns the local names go.opentelemetry.io/otel/attribute
// is bound to in file, and whether it is dot-imported. The conventional name is
// always included so a parsed snippet carrying no import declaration still
// matches; an alias (`attr "…/attribute"`) adds its own name, which is what
// keeps the gate from being defeated by a rename that golangci does not forbid.
func attributeBindings(file *ast.File) (names map[string]bool, dotImported bool) {
	names = map[string]bool{"attribute": true}
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != attributePackagePath || imp.Name == nil {
			continue
		}
		if imp.Name.Name == "." {
			dotImported = true
			continue
		}
		names[imp.Name.Name] = true
	}
	return names, dotImported
}

// isAttributeConstructor reports whether fun names a constructor on the
// attribute package under any of its local bindings (attribute.String,
// attr.Stringer, or a bare String under a dot-import).
func isAttributeConstructor(fun ast.Expr, names map[string]bool, dotImported bool) bool {
	switch callee := fun.(type) {
	case *ast.SelectorExpr:
		pkg, ok := callee.X.(*ast.Ident)
		return ok && names[pkg.Name]
	case *ast.Ident:
		return dotImported && callee.IsExported()
	default:
		return false
	}
}

// containsErrorMessageCall reports whether expr contains a call to a method
// named Error taking no arguments — the shape err.Error() has, and the one a
// classification render never does.
func containsErrorMessageCall(expr ast.Expr) bool {
	leaks := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if leaks {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 0 {
			return true
		}
		if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel && sel.Sel.Name == "Error" {
			leaks = true
			return false
		}
		return true
	})
	return leaks
}

// repoRoot ascends from the working directory to the nearest directory holding
// a go.mod, so the scan covers the whole framework module rather than the
// package it happens to live in.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod found above the working directory")
		dir = parent
	}
}

// TestScanAttributeCallsJudgesArgumentShape pins the predicate on parsed
// snippets, so the shapes it must catch and must not catch are asserted in the
// committed tree rather than only by a scratch plant file. Each case pins both
// halves of the predicate: the findings it reports and the constructor count
// that is the scan's positive control.
func TestScanAttributeCallsJudgesArgumentShape(t *testing.T) {
	const attributeImport = `import attr "go.opentelemetry.io/otel/attribute"`
	const dotImport = `import . "go.opentelemetry.io/otel/attribute"`

	tests := []struct {
		name         string
		imports      string
		body         string
		findings     int
		constructors int
	}{
		{
			name:         "direct_attribute_call_with_error_message",
			body:         `attribute.String("error", err.Error())`,
			findings:     1,
			constructors: 1,
		},
		{
			name:         "attribute_call_nested_in_set_attributes",
			body:         `span.SetAttributes(attribute.String("error", err.Error()))`,
			findings:     1,
			constructors: 1,
		},
		{
			name:         "error_message_wrapped_in_another_call",
			body:         `attribute.String("error", fmt.Sprint(err.Error()))`,
			findings:     1,
			constructors: 1,
		},
		{
			name:         "aliased_attribute_import_is_still_matched",
			imports:      attributeImport,
			body:         `attr.String("error", err.Error())`,
			findings:     1,
			constructors: 1,
		},
		{
			name:         "dot_imported_constructor_is_still_matched",
			imports:      dotImport,
			body:         `String("error", err.Error())`,
			findings:     1,
			constructors: 1,
		},
		{
			name:         "classification_render_is_not_a_message",
			body:         `attribute.String("error.type", classifyError(err))`,
			findings:     0,
			constructors: 1,
		},
		{
			name:         "error_message_outside_any_attribute_call",
			body:         `log.Info().Str("error", err.Error()).Msg("failed")`,
			findings:     0,
			constructors: 0,
		},
		{
			name:         "bare_call_without_a_dot_import_is_not_a_constructor",
			body:         `String("error", err.Error())`,
			findings:     0,
			constructors: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\n\n" + tt.imports + "\n\nfunc f() {\n\t" + tt.body + "\n}\n"
			file, err := parser.ParseFile(token.NewFileSet(), "snippet.go", src, parser.SkipObjectResolution)
			require.NoError(t, err)

			findings, constructors := scanAttributeCalls(file)

			assert.Len(t, findings, tt.findings)
			assert.Equal(t, tt.constructors, constructors)
		})
	}
}

// TestSkipDirSkipsToolsOnlyAtTheModuleRoot pins the anchoring: the separate
// tools/ module is skipped where it lives, while a package that merely ends in
// the same name stays covered.
func TestSkipDirSkipsToolsOnlyAtTheModuleRoot(t *testing.T) {
	assert.True(t, skipDir("tools"))
	assert.True(t, skipDir(filepath.Join("outbox", "testdata")))
	assert.False(t, skipDir("."))
	assert.False(t, skipDir(filepath.Join("internal", "tools")))
	assert.False(t, skipDir("observability"))
}
