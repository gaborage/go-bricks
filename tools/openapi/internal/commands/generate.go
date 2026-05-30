package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/gaborage/go-bricks/tools/openapi/internal/analyzer"
	"github.com/gaborage/go-bricks/tools/openapi/internal/generator"
	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
)

const (
	cmdNameGenerate = "generate"
	extYAML         = ".yaml"
	extYML          = ".yml"
	extJSON         = ".json"
	formatYAML      = "yaml"
	formatJSON      = "json"
)

// GenerateOptions holds options for the generate command
type GenerateOptions struct {
	ProjectRoot string
	OutputFile  string
	Verbose     bool

	// Document metadata (override the analyzer defaults).
	Title       string
	APIVersion  string
	Description string
	Servers     []string
	License     string
	LicenseURL  string

	Format            string // "yaml" (default) or "json"
	DisableTenantAuth bool   // omit the X-Tenant-ID security scheme (single-tenant)
	Strict            bool   // treat warnings (empty/untyped) as a non-zero exit
}

// NewGenerateCommand creates the generate command
func NewGenerateCommand() *cobra.Command {
	opts := &GenerateOptions{}

	cmd := &cobra.Command{
		Use:   cmdNameGenerate,
		Short: "Generate OpenAPI specification from go-bricks service",
		Long: `Analyzes a go-bricks service and generates an OpenAPI 3.0.1 specification.

The tool discovers modules, analyzes route registrations and type definitions,
and produces a comprehensive API specification with validation constraints.`,
		Example: `  # Generate spec for current directory
  go-bricks-openapi generate

  # Generate spec for a specific project, with metadata
  go-bricks-openapi generate --project ./my-service --output docs/openapi.yaml \
    --title "My Service" --api-version 2.1.0 --server https://api.example.com

  # Emit JSON
  go-bricks-openapi generate --project ./my-service --output docs/openapi.json --format json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGenerate(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.ProjectRoot, "project", "p", ".", "Project root directory")
	cmd.Flags().StringVarP(&opts.OutputFile, "output", "o", "openapi.yaml", "Output file path")
	cmd.Flags().BoolVarP(&opts.Verbose, "verbose", "v", false, "Verbose output")
	cmd.Flags().StringVar(&opts.Title, "title", "", "info.title (overrides the project name)")
	cmd.Flags().StringVar(&opts.APIVersion, "api-version", "", "info.version (overrides 1.0.0)")
	cmd.Flags().StringVar(&opts.Description, "description", "", "info.description")
	cmd.Flags().StringArrayVar(&opts.Servers, "server", nil, "Server URL (repeatable)")
	cmd.Flags().StringVar(&opts.License, "license", "", "info.license.name")
	cmd.Flags().StringVar(&opts.LicenseURL, "license-url", "", "info.license.url")
	cmd.Flags().StringVar(&opts.Format, "format", "", "Output format: yaml (default) or json")
	cmd.Flags().BoolVar(&opts.DisableTenantAuth, "no-tenant-security", false, "Omit the X-Tenant-ID security scheme (single-tenant)")
	cmd.Flags().BoolVar(&opts.Strict, "strict", false, "Exit non-zero if the spec has warnings (no modules/routes, untyped routes)")

	return cmd
}

func runGenerate(ctx context.Context, opts *GenerateOptions) error {
	if err := validateGenerateOptions(opts); err != nil {
		return err
	}
	format, err := resolveFormat(opts)
	if err != nil {
		return err
	}

	fmt.Printf("Generating OpenAPI spec for project: %s\n", opts.ProjectRoot)
	fmt.Printf("Output file: %s\n", opts.OutputFile)

	projectAnalyzer := analyzer.New(opts.ProjectRoot)
	project, err := projectAnalyzer.AnalyzeProject()
	if err != nil {
		return fmt.Errorf("failed to analyze project: %w", err)
	}

	if opts.Verbose {
		fmt.Printf("Discovered %d modules\n", len(project.Modules))
		for _, module := range project.Modules {
			fmt.Printf("  Module: %s (%s) with %d routes\n", module.Name, module.Package, len(module.Routes))
		}
	}

	// Surface non-fatal analysis diagnostics on stderr so they don't corrupt the spec.
	for _, w := range projectAnalyzer.Warnings(ctx) {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	gen := generator.NewWithConfig(buildGeneratorConfig(opts))
	specContent, err := gen.Generate(project)
	if err != nil {
		return fmt.Errorf("failed to generate spec: %w", err)
	}
	if format == formatJSON {
		specContent, err = toJSON(specContent)
		if err != nil {
			return fmt.Errorf("failed to render JSON: %w", err)
		}
	}

	// Content warnings (empty/untyped) on stderr. With --strict they are fatal
	// BEFORE we persist, so a failed strict run never prints a success line or
	// leaves a stale artifact on disk for a downstream step to consume.
	if emitContentWarnings(project) && opts.Strict {
		return fmt.Errorf("spec generated with warnings and --strict is set")
	}

	if err := os.MkdirAll(filepath.Dir(opts.OutputFile), 0750); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	if err := os.WriteFile(opts.OutputFile, []byte(specContent), 0600); err != nil {
		return fmt.Errorf("failed to write output file: %w", err)
	}

	fmt.Printf("✓ OpenAPI specification generated: %s\n", opts.OutputFile)
	return nil
}

// buildGeneratorConfig maps CLI options to a generator.Config.
func buildGeneratorConfig(opts *GenerateOptions) *generator.Config {
	cfg := &generator.Config{
		Title:                 opts.Title,
		Version:               opts.APIVersion,
		Description:           opts.Description,
		Servers:               opts.Servers,
		DisableTenantSecurity: opts.DisableTenantAuth,
	}
	if opts.License != "" {
		cfg.License = &generator.License{Name: opts.License, URL: opts.LicenseURL}
	}
	return cfg
}

// formatForExt maps a recognized output-file extension to its format. It returns
// ok=false for any unrecognized or absent extension.
func formatForExt(path string) (format string, ok bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case extJSON:
		return formatJSON, true
	case extYAML, extYML:
		return formatYAML, true
	default:
		return "", false
	}
}

// resolveFormat resolves the effective output format. An explicit --format wins,
// but it must not contradict a recognized output extension — writing JSON into a
// .yaml file (or vice versa) produces a file whose extension lies about its
// contents, so a mismatch is a hard error rather than a silent override. With no
// --format the format is inferred from the extension (.json -> json, .yaml/.yml
// -> yaml), defaulting to yaml. An unrecognized --format is an error.
func resolveFormat(opts *GenerateOptions) (string, error) {
	explicit := strings.ToLower(opts.Format)
	extFormat, extKnown := formatForExt(opts.OutputFile)
	if explicit != "" {
		if explicit != formatYAML && explicit != formatJSON {
			return "", fmt.Errorf("invalid --format %q (want yaml or json)", opts.Format)
		}
		if extKnown && extFormat != explicit {
			return "", fmt.Errorf("--format %s conflicts with output extension %q "+
				"(the file would lie about its contents)", explicit, filepath.Ext(opts.OutputFile))
		}
		return explicit, nil
	}
	if extKnown {
		return extFormat, nil
	}
	return formatYAML, nil
}

// validateGenerateOptions validates the project root, rejects a --license-url
// without a --license name (OpenAPI's License Object requires name before url),
// and appends the resolved format's extension when the output filename has none.
func validateGenerateOptions(opts *GenerateOptions) error {
	if _, err := os.Stat(opts.ProjectRoot); os.IsNotExist(err) {
		return fmt.Errorf("project root does not exist: %s", opts.ProjectRoot)
	}
	if opts.LicenseURL != "" && opts.License == "" {
		return fmt.Errorf("--license-url requires --license (OpenAPI license.url requires license.name)")
	}
	format, err := resolveFormat(opts)
	if err != nil {
		return err
	}
	if filepath.Ext(opts.OutputFile) == "" {
		if format == formatJSON {
			opts.OutputFile += extJSON
		} else {
			opts.OutputFile += extYAML
		}
	}
	return nil
}

// toJSON re-parses the generated YAML document and re-emits it as indented JSON,
// preserving the canonical OpenAPI section order (openapi, info, servers, paths,
// components, security). A plain map[string]any round-trip would let
// encoding/json sort keys alphabetically and scramble that order, so we walk the
// ordered yaml.Node tree and render mappings through orderedMap.
func toJSON(yamlSpec string) (string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(yamlSpec), &root); err != nil {
		return "", err
	}
	doc, err := yamlNodeToJSONValue(&root)
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// yamlNodeToJSONValue converts an ordered yaml.Node tree into a json-marshalable
// value: mappings become *orderedMap (key order preserved), sequences []any, and
// scalars their yaml-decoded Go type (string/int/float/bool/nil).
func yamlNodeToJSONValue(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) == 0 {
			return nil, nil
		}
		return yamlNodeToJSONValue(node.Content[0])
	case yaml.MappingNode:
		m := &orderedMap{
			keys: make([]string, 0, len(node.Content)/2),
			vals: make([]any, 0, len(node.Content)/2),
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			val, err := yamlNodeToJSONValue(node.Content[i+1])
			if err != nil {
				return nil, err
			}
			m.keys = append(m.keys, node.Content[i].Value)
			m.vals = append(m.vals, val)
		}
		return m, nil
	case yaml.SequenceNode:
		arr := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			v, err := yamlNodeToJSONValue(item)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		return arr, nil
	case yaml.ScalarNode:
		var v any
		if err := node.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported yaml node kind %d", node.Kind)
	}
}

// orderedMap marshals to a JSON object preserving insertion order, unlike
// map[string]any (which encoding/json sorts alphabetically). MarshalIndent
// re-indents the compact output this produces, so nested maps stay ordered too.
type orderedMap struct {
	keys []string
	vals []any
}

func (m *orderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(m.vals[i])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// emitContentWarnings prints stderr warnings for a content-free or partially-typed
// spec and reports whether any were emitted (so --strict can fail the run).
func emitContentWarnings(project *models.Project) bool {
	stats := calculateProjectStats(project)
	warned := false
	switch {
	case stats.ModuleCount == 0:
		fmt.Fprintln(os.Stderr, "warning: no go-bricks modules discovered — the spec has no operations")
		warned = true
	case stats.RouteCount == 0:
		fmt.Fprintln(os.Stderr, "warning: modules discovered but no routes — the spec has no operations")
		warned = true
	}
	if n := len(stats.UntypedRoutes); n > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d route(s) have no resolved request/response type: %s\n",
			n, strings.Join(stats.UntypedRoutes, ", "))
		warned = true
	}
	return warned
}
