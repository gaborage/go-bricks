// Package spectest provides an end-to-end test harness for the OpenAPI
// generator. It runs the real pipeline — analyzer.AnalyzeProject ->
// generator.Generate — over fixture projects under testdata/ and validates the
// emitted document against the OpenAPI 3.0 specification in-process.
//
// It exists because the per-package unit suites build models.Project by hand,
// so they never exercise the analyze->generate path that the binary actually
// runs (which is precisely why functionally-broken specs ship while every unit
// test stays green). This harness closes that blind spot with two checks per
// fixture: structural validation via kin-openapi (the primary, deterministic,
// network-free gate) and a golden-file comparison of the exact emitted YAML.
//
// The exported helpers (Generate, Validate) are reusable by later generator
// work so every fidelity change can assert real wire output.
package spectest

import (
	"context"
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/gaborage/go-bricks/tools/openapi/internal/analyzer"
	"github.com/gaborage/go-bricks/tools/openapi/internal/generator"
)

// Document metadata passed to the generator. NOTE: the generator currently
// derives the emitted spec title from project metadata (the go.mod module name,
// or a built-in default for a module-less project), so harnessTitle is
// overridden in practice — it is supplied only to satisfy the generator.New
// signature and becomes meaningful once a title override lands. harnessVersion
// and harnessDescription fill the corresponding info fields.
const (
	harnessTitle       = "Fixture API"
	harnessVersion     = "1.0.0"
	harnessDescription = "Generated API specification"
)

// Generate runs the analyzer and generator over the project rooted at dir and
// returns the emitted OpenAPI document as a string. ctx is the first parameter
// per the repo's context-first convention; it is honoured for early
// cancellation and will thread into the analyzer/generator once those become
// context-aware.
func Generate(ctx context.Context, dir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	project, err := analyzer.New(dir).AnalyzeProject()
	if err != nil {
		return "", fmt.Errorf("analyze %s: %w", dir, err)
	}

	spec, err := generator.New(harnessTitle, harnessVersion, harnessDescription).Generate(project)
	if err != nil {
		return "", fmt.Errorf("generate %s: %w", dir, err)
	}

	return spec, nil
}

// Validate parses spec data (YAML or JSON) and validates it against OpenAPI 3.0
// in-process, honouring ctx for cancellation. This is the primary structural
// gate: it is deterministic and needs no network or external toolchain, unlike
// the redocly step run in CI.
func Validate(ctx context.Context, data []byte) error {
	loader := openapi3.NewLoader()

	doc, err := loader.LoadFromData(data)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}

	if err := doc.Validate(ctx); err != nil {
		return fmt.Errorf("validate spec: %w", err)
	}

	return nil
}
