package analyzer

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
)

const (
	serverImportPath = "github.com/gaborage/go-bricks/server"
	appImportPath    = "github.com/gaborage/go-bricks/app"

	// Framework types that should be filtered out from request/response extraction
	frameworkTypeHandlerContext = "HandlerContext"
	frameworkTypeAPIError       = "IAPIError"
	frameworkTypeError          = "error"
	frameworkPkgServer          = "server"
	frameworkPkgApp             = "app"
	stdlibPkgHTTP               = "http"
	frameworkTypeModuleDeps     = "ModuleDeps"

	// Framework response wrappers. Result[R] / ResultWithMeta[R] are unwrapped to
	// the inner response type R; NoContentResult marks a bodyless 204 response.
	resultTypeName          = "Result"
	resultWithMetaTypeName  = "ResultWithMeta"
	noContentResultTypeName = "NoContentResult"

	// Module interface method names
	moduleMethodName           = "Name"
	moduleMethodInit           = "Init"
	moduleMethodRegisterRoutes = "RegisterRoutes"
	moduleMethodShutdown       = "Shutdown"

	// RouteRegistrar.Group(prefix) — sub-router with a path prefix.
	groupMethodName        = "Group"
	routeRegistrarTypeName = "RouteRegistrar"

	// Go primitive type names used in AST inspection
	goTypeString = "string"

	// HTTP method names
	httpMethodPost = "POST"

	// File and directory names
	goFileExt     = ".go"
	testFileExt   = "_test.go"
	goModFileName = "go.mod"

	// Directories to skip during discovery
	vendorDir      = "vendor"
	gitDir         = ".git"
	nodeModulesDir = "node_modules"

	// Struct tag names
	tagJSON     = "json"
	tagParam    = "param"
	tagQuery    = "query"
	tagHeader   = "header"
	tagDoc      = "doc"
	tagExample  = "example"
	tagValidate = "validate"

	// Parameter types for OpenAPI
	paramTypePath   = "path"
	paramTypeQuery  = "query"
	paramTypeHeader = "header"

	// Special tag values
	jsonSkipValue      = "-"
	boolTrueString     = "true"
	constraintRequired = "required"
)

// ProjectAnalyzer analyzes Go-Bricks projects to extract module and route information
type ProjectAnalyzer struct {
	projectRoot  string
	modulePath   string // go.mod module path; prefix for translating in-module imports to dirs
	fileSet      *token.FileSet
	constants    map[string]string               // Map of constant names to their values
	warnings     []string                        // Non-fatal diagnostics collected during analysis
	typeRegistry map[string]*models.TypeInfo     // Named struct types reachable from routes (by final schema name)
	pkgCache     map[string]map[string]*ast.File // dir -> (file path -> parsed AST), populated on demand
	nameAssign   map[string]string               // "pkg\x00Type" -> final schema name (collision qualification)
	usedNames    map[string]struct{}             // final schema names already taken
}

// New creates a new project analyzer
func New(projectRoot string) *ProjectAnalyzer {
	return &ProjectAnalyzer{
		projectRoot:  projectRoot,
		fileSet:      token.NewFileSet(),
		constants:    make(map[string]string),
		typeRegistry: make(map[string]*models.TypeInfo),
		pkgCache:     make(map[string]map[string]*ast.File),
		nameAssign:   make(map[string]string),
		usedNames:    make(map[string]struct{}),
	}
}

// addWarningf records a non-fatal diagnostic (e.g. a route whose path could not
// be resolved to a literal). Callers surface these to the user after analysis.
func (a *ProjectAnalyzer) addWarningf(format string, args ...any) {
	a.warnings = append(a.warnings, fmt.Sprintf(format, args...))
}

// Warnings returns a copy of the non-fatal diagnostics collected during the last
// analysis. ctx is accepted per the repo's context-first convention for exported
// APIs; the returned slice is cloned so callers cannot mutate analyzer state.
func (a *ProjectAnalyzer) Warnings(_ context.Context) []string {
	return slices.Clone(a.warnings)
}

// isFrameworkType checks if a type should be filtered out as a framework type.
// Returns true for framework types that shouldn't be treated as request/response types.
func (a *ProjectAnalyzer) isFrameworkType(typeName, pkgName string) bool {
	// Standard framework types (HandlerContext, IAPIError, error)
	if typeName == frameworkTypeHandlerContext ||
		typeName == frameworkTypeAPIError ||
		typeName == frameworkTypeError {
		return true
	}

	// Qualified server package types (server.HandlerContext, server.IAPIError)
	if pkgName == frameworkPkgServer &&
		(typeName == frameworkTypeHandlerContext || typeName == frameworkTypeAPIError) {
		return true
	}

	return false
}

// AnalyzeProject discovers modules and routes from a go-bricks project
func (a *ProjectAnalyzer) AnalyzeProject() (*models.Project, error) {
	project := &models.Project{
		Name:        "Go-Bricks API",
		Version:     "1.0.0",
		Description: "Generated API specification",
		Modules:     []models.Module{},
	}

	// Reset per-run state so repeated analyses on the same analyzer don't
	// accumulate stale warnings or type-registry entries.
	a.warnings = nil
	a.typeRegistry = make(map[string]*models.TypeInfo)
	a.pkgCache = make(map[string]map[string]*ast.File)
	a.nameAssign = make(map[string]string)
	a.usedNames = make(map[string]struct{})

	// Discover project metadata from go.mod
	a.discoverProjectMetadata(project)

	// Discover modules by walking the project directory
	modules, err := a.discoverModules()
	if err != nil {
		return nil, fmt.Errorf("failed to discover modules: %w", err)
	}

	project.Modules = modules
	project.Types = a.typeRegistry
	return project, nil
}

// discoverProjectMetadata extracts project information from go.mod
func (a *ProjectAnalyzer) discoverProjectMetadata(project *models.Project) {
	goModPath := filepath.Join(a.projectRoot, goModFileName)
	if a.validateProjectPath(goModPath) != nil {
		return // Skip if path validation fails
	}
	// #nosec G304 - goModPath is validated to be within project root
	content, err := os.ReadFile(goModPath)
	if err != nil {
		return
	}
	a.parseGoModForProjectName(project, content)
}

// parseGoModForProjectName extracts the project name from go.mod content
func (a *ProjectAnalyzer) parseGoModForProjectName(project *models.Project, content []byte) {
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		moduleName := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		a.modulePath = moduleName // full module path for in-module import resolution
		// Extract just the last part as the project name
		parts := strings.Split(moduleName, "/")
		if len(parts) > 0 {
			name := parts[len(parts)-1]
			if name != "" {
				project.Name = strings.ToUpper(name[:1]) + name[1:] + " API"
			}
		}
		break
	}
}

// discoverModules finds all go-bricks modules in the project
func (a *ProjectAnalyzer) discoverModules() ([]models.Module, error) {
	d := &moduleDiscoverer{
		analyzer: a,
		modules:  []models.Module{},
		seen:     make(map[string]bool),
	}

	err := filepath.Walk(a.projectRoot, d.walk)
	return d.modules, err
}

// moduleDiscoverer holds the state for module discovery
type moduleDiscoverer struct {
	analyzer *ProjectAnalyzer
	modules  []models.Module
	seen     map[string]bool
}

// walk is the callback function for filepath.Walk to discover modules
func (d *moduleDiscoverer) walk(path string, info os.FileInfo, err error) error {
	if err != nil {
		return nil // Skip errors to continue discovery
	}

	if info.IsDir() && shouldSkipDir(info.Name()) {
		return filepath.SkipDir
	}

	if !strings.HasSuffix(path, goFileExt) || strings.HasSuffix(path, testFileExt) {
		return nil
	}

	module, err := d.analyzer.analyzeGoFile(path)
	if err != nil {
		// Log error but continue processing other files
		return nil
	}

	if module != nil {
		key := module.Package
		if !d.seen[key] {
			d.modules = append(d.modules, *module)
			d.seen[key] = true
		}
	}

	return nil
}

// shouldSkipDir checks if a directory should be skipped during discovery
func shouldSkipDir(name string) bool {
	return name == vendorDir || name == gitDir || strings.HasPrefix(name, ".") || name == nodeModulesDir
}

// analyzeGoFile parses a Go file and extracts module information
func (a *ProjectAnalyzer) analyzeGoFile(filePath string) (*models.Module, error) {
	if err := a.validateGoFilePath(filePath); err != nil {
		return nil, fmt.Errorf("invalid file path %s: %w", filePath, err)
	}

	// #nosec G304 - filePath is validated to be a .go file within project root
	src, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	// Parse the Go file
	astFile, err := parser.ParseFile(a.fileSet, filePath, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("failed to parse file %s: %w", filePath, err)
	}

	// Extract constants first (needed for route path resolution)
	a.extractConstants(astFile)

	// Check if this file contains a go-bricks module
	module, structName := a.extractModuleFromAST(astFile, filePath)
	if module == nil {
		return nil, nil // Not a module file
	}

	// Extract routes from the RegisterRoutes method, including other files in the package
	module.Routes = a.extractRoutesFromPackage(astFile, filePath, structName)

	// Stamp the owning module identity onto each route at discovery time so later
	// passes (operationId namespacing, component-name disambiguation) can use it.
	for i := range module.Routes {
		module.Routes[i].Module = module.Name
		module.Routes[i].Package = module.Package
	}

	return module, nil
}

// extractModuleFromAST checks if the AST contains a go-bricks module
func (a *ProjectAnalyzer) extractModuleFromAST(astFile *ast.File, filePath string) (module *models.Module, structName string) {
	structName = a.findModuleStruct(astFile, filePath)
	if structName == "" {
		return nil, ""
	}

	// Use package name as module name and extract package-level description
	moduleDescription := a.extractPackageDescription(astFile)
	packageName := astFile.Name.Name

	module = &models.Module{
		Name:        packageName,
		Package:     packageName,
		Description: moduleDescription,
		Routes:      []models.Route{},
	}
	return module, structName
}

// findModuleStruct iterates through declarations to find a go-bricks module struct
func (a *ProjectAnalyzer) findModuleStruct(astFile *ast.File, filePath string) string {
	for _, decl := range astFile.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			if _, ok := typeSpec.Type.(*ast.StructType); !ok {
				continue
			}

			// Check if this struct has methods that indicate it's a Module
			if a.hasModuleMethods(astFile, typeSpec.Name.Name, filePath) {
				return typeSpec.Name.Name
			}
		}
	}
	return ""
}

// hasModuleMethods checks if the struct has methods indicating it's a go-bricks module
func (a *ProjectAnalyzer) hasModuleMethods(astFile *ast.File, structName, filePath string) bool {
	requiredMethods := map[string]bool{
		moduleMethodName:           false,
		moduleMethodInit:           false,
		moduleMethodRegisterRoutes: false,
		moduleMethodShutdown:       false,
	}

	files, err := a.parsePackage(filePath, astFile.Name.Name)
	if err != nil || files == nil {
		serverAliases := a.extractImportAliases(astFile, serverImportPath)
		appAliases := a.extractImportAliases(astFile, appImportPath)
		a.collectMethodFlagsFromFile(astFile, structName, requiredMethods, serverAliases, appAliases)
	} else {
		for _, file := range files {
			serverAliases := a.extractImportAliases(file, serverImportPath)
			appAliases := a.extractImportAliases(file, appImportPath)
			a.collectMethodFlagsFromFile(file, structName, requiredMethods, serverAliases, appAliases)
		}
	}

	// Check if we have at least the core methods with valid signatures
	return requiredMethods[moduleMethodName] && requiredMethods[moduleMethodInit] && requiredMethods[moduleMethodRegisterRoutes]
}

// isMethodOnStruct checks if a function is a method on the specified struct
func (a *ProjectAnalyzer) isMethodOnStruct(recv *ast.FieldList, structName string) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}

	field := recv.List[0]
	switch t := field.Type.(type) {
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name == structName
		}
	case *ast.Ident:
		return t.Name == structName
	}

	return false
}

// isModuleDepsField checks if a field is of type *app.ModuleDeps
func (a *ProjectAnalyzer) isModuleDepsField(field *ast.Field) bool {
	starExpr, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return false
	}

	selExpr, ok := starExpr.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	pkgIdent, ok := selExpr.X.(*ast.Ident)
	if !ok {
		return false
	}

	return pkgIdent.Name == frameworkPkgApp && selExpr.Sel.Name == frameworkTypeModuleDeps
}

// extractRoutesFromPackage extracts route registrations for the module across the entire package
func (a *ProjectAnalyzer) extractRoutesFromPackage(astFile *ast.File, filePath, structName string) []models.Route {
	files, err := a.parsePackage(filePath, astFile.Name.Name)
	if err != nil || files == nil {
		return a.collectRoutesFromFile(astFile, filePath, structName, map[string]struct{}{frameworkPkgServer: {}})
	}

	var routes []models.Route

	// Reset constants map to prevent leakage from previous packages
	a.constants = make(map[string]string)

	// First collect constants so paths can be resolved regardless of declaration order
	for _, file := range files {
		a.extractConstants(file)
	}

	for _, file := range files {
		aliases := a.extractImportAliases(file, serverImportPath)
		if len(aliases) == 0 {
			continue
		}
		routes = append(routes, a.collectRoutesFromFile(file, filePath, structName, aliases)...)
	}

	return routes
}

// collectRoutesFromFile gathers routes for a specific module struct from a single file
func (a *ProjectAnalyzer) collectRoutesFromFile(astFile *ast.File, filePath, structName string, serverAliases map[string]struct{}) []models.Route {
	var routes []models.Route

	for _, decl := range astFile.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Name.Name != moduleMethodRegisterRoutes || funcDecl.Body == nil {
			continue
		}

		if !a.isMethodOnStruct(funcDecl.Recv, structName) {
			continue
		}

		if !a.isValidRegisterRoutesSignature(funcDecl, serverAliases) {
			continue
		}

		recvVar := receiverVarName(funcDecl.Recv)
		routes = append(routes, a.extractRoutesFromFuncBodyWithAliases(funcDecl.Body, astFile, filePath, structName, recvVar, serverAliases)...)
	}

	return routes
}

// extractRoutesFromFuncBody extracts route registrations from a function body
// using only the default "server" alias and no handler/helper context.
func (a *ProjectAnalyzer) extractRoutesFromFuncBody(body *ast.BlockStmt) []models.Route {
	return a.extractRoutesFromFuncBodyWithAliases(body, nil, "", "", "", map[string]struct{}{frameworkPkgServer: {}})
}

// extractRoutesFromFuncBodyWithAliases walks a RegisterRoutes body (and the
// same-receiver helper methods it calls) to collect every route registration,
// including those nested inside if/for/range/blocks and those registered on a
// r.Group(prefix) registrar.
func (a *ProjectAnalyzer) extractRoutesFromFuncBodyWithAliases(
	body *ast.BlockStmt, astFile *ast.File, filePath, structName, recvVar string, serverAliases map[string]struct{},
) []models.Route {
	w := &routeWalker{
		a:             a,
		astFile:       astFile,
		filePath:      filePath,
		structName:    structName,
		recvVar:       recvVar,
		serverAliases: serverAliases,
		// Seed the recursion stack with the entry method so a helper that calls
		// back into RegisterRoutes cannot re-walk it (cycle guard for the root).
		stack: map[string]bool{moduleMethodRegisterRoutes: true},
	}
	w.walkBody(body, nil)
	return w.routes
}

// routeWalker collects routes from a RegisterRoutes method body and the
// same-receiver helper methods it transitively calls.
type routeWalker struct {
	a             *ProjectAnalyzer
	astFile       *ast.File
	filePath      string
	structName    string // module struct, for handler resolution
	recvVar       string // module receiver var name (for helper calls); "" if none
	serverAliases map[string]struct{}
	stack         map[string]bool // helper methods currently on the walk stack (cycle guard)
	routes        []models.Route
}

// walkBody collects server.METHOD route registrations from a body (with
// r.Group(prefix) prefixes applied) and recurses into same-receiver helpers.
// seed carries registrar->prefix bindings inherited from a caller (so a helper
// invoked with a grouped registrar inherits that group's prefix).
func (w *routeWalker) walkBody(body *ast.BlockStmt, seed map[string]string) {
	if body == nil {
		return
	}
	prefixes := w.collectGroupPrefixes(body)
	for reg, prefix := range seed {
		if _, ok := prefixes[reg]; !ok {
			prefixes[reg] = prefix
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if route := w.routeFromCall(call, prefixes); route != nil {
			w.routes = append(w.routes, *route)
			return true
		}
		w.maybeRecurseHelper(call, prefixes)
		return true
	})
}

// collectGroupPrefixes maps each registrar variable assigned from <reg>.Group(p)
// to its accumulated path prefix. Nested groups resolve because Go requires the
// parent registrar to be declared (and thus visited) before the child.
//
// Limitation: the map is keyed by identifier name across the whole body, so it
// is not scope-aware. A registrar var name shadowed in different branches with
// DIFFERENT prefixes (e.g. an `if` and `else` both doing `api := r.Group(...)`
// with distinct paths) collapses to the last assignment. The idiomatic pattern
// declares each group once at body scope, which resolves correctly; a fully
// scope-aware walk is deferred until a real case requires it.
func (w *routeWalker) collectGroupPrefixes(body *ast.BlockStmt) map[string]string {
	prefixes := map[string]string{}
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != groupMethodName || len(call.Args) < 1 {
			return true
		}
		parent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if prefix, pok := w.a.extractPathFromArg(call.Args[0]); pok {
			prefixes[lhs.Name] = prefixes[parent.Name] + prefix
		}
		return true
	})
	return prefixes
}

// routeFromCall builds a route from a server.METHOD(...) call, or nil if the call
// is not a route registration. Unresolvable paths drop the route with a warning.
func (w *routeWalker) routeFromCall(call *ast.CallExpr, prefixes map[string]string) *models.Route {
	method, ok := w.a.validateServerCall(call, w.serverAliases)
	if !ok {
		return nil
	}

	rawPath, resolved := w.a.extractPathFromArg(call.Args[2])
	if !resolved {
		w.a.addWarningf("skipping a server.%s route: its path argument could not be resolved to a literal string", method)
		return nil
	}

	// Prepend the group prefix bound to the registrar argument (Args[1]).
	prefix := ""
	if reg, ok := call.Args[1].(*ast.Ident); ok {
		prefix = prefixes[reg.Name]
	}

	route := &models.Route{
		Method: strings.ToUpper(method),
		Tags:   []string{},
		Path:   normalizePath(prefix + rawPath),
	}
	route.HandlerName, route.Request, route.Response, route.SuccessStatus = w.a.extractHandlerInfo(call, w.astFile, w.filePath, w.structName)
	for i := 4; i < len(call.Args); i++ {
		w.a.extractRouteMetadata(call.Args[i], route, w.serverAliases)
	}
	return route
}

// maybeRecurseHelper recurses into a same-receiver route-registration helper
// (w.recvVar.method(...) where method takes a server.RouteRegistrar) so routes
// registered in helpers are discovered. The RouteRegistrar requirement excludes
// non-registration methods (e.g. predicates or handler factories) so their
// internal server.* calls are not mistaken for routes. The stack guards against
// infinite recursion on mutually-recursive helpers while still allowing a helper
// to be invoked more than once. The prefix bound to the registrar argument is
// threaded into the helper's registrar parameter.
func (w *routeWalker) maybeRecurseHelper(call *ast.CallExpr, prefixes map[string]string) {
	if w.recvVar == "" || w.astFile == nil {
		return
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok || recv.Name != w.recvVar {
		return
	}
	method := sel.Sel.Name
	if w.stack[method] {
		return
	}
	decl := w.a.findMethodDecl(w.astFile, w.filePath, w.structName, method)
	idx, paramName := w.a.routeRegistrarParam(decl, w.serverAliases)
	if idx < 0 {
		return // not a route-registration helper
	}

	// Thread the prefix of the registrar argument into the helper's registrar
	// parameter so routes inside the helper inherit the caller's group prefix.
	seed := map[string]string{}
	if paramName != "" && idx < len(call.Args) {
		if argIdent, ok := call.Args[idx].(*ast.Ident); ok {
			if prefix := prefixes[argIdent.Name]; prefix != "" {
				seed[paramName] = prefix
			}
		}
	}

	w.stack[method] = true
	w.walkBody(decl.Body, seed)
	delete(w.stack, method)
}

// routeRegistrarParam returns the positional index and name of a function's
// server.RouteRegistrar parameter, or (-1, "") if it has none. The index is
// counted over flattened parameter names so it lines up with call arguments.
// serverAliases lets it recognize an aliased server import.
func (a *ProjectAnalyzer) routeRegistrarParam(decl *ast.FuncDecl, serverAliases map[string]struct{}) (idx int, name string) {
	if decl == nil || decl.Type.Params == nil {
		return -1, ""
	}
	pos := 0
	for _, field := range decl.Type.Params.List {
		isReg := a.isRouteRegistrarType(field.Type, serverAliases)
		if len(field.Names) == 0 {
			if isReg {
				return pos, ""
			}
			pos++
			continue
		}
		for _, n := range field.Names {
			if isReg {
				return pos, n.Name
			}
			pos++
		}
	}
	return -1, ""
}

// isRouteRegistrarType reports whether expr is server.RouteRegistrar (honoring an
// aliased server import).
func (a *ProjectAnalyzer) isRouteRegistrarType(expr ast.Expr, serverAliases map[string]struct{}) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && a.aliasContains(serverAliases, pkg.Name, frameworkPkgServer) && sel.Sel.Name == routeRegistrarTypeName
}

// validateServerCall reports whether call is a server.METHOD(hr, r, path, ...)
// route registration and returns the HTTP method name.
func (a *ProjectAnalyzer) validateServerCall(callExpr *ast.CallExpr, serverAliases map[string]struct{}) (method string, ok bool) {
	selExpr, ok := callExpr.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}

	pkgIdent, ok := selExpr.X.(*ast.Ident)
	if !ok || !a.aliasContains(serverAliases, pkgIdent.Name, frameworkPkgServer) {
		return "", false
	}

	method = selExpr.Sel.Name
	if !a.isHTTPMethod(method) {
		return "", false
	}

	if len(callExpr.Args) < 3 {
		return "", false
	}

	return method, true
}

// findMethodDecl finds the declaration of method methodName on structName,
// searching the current file then the rest of the package.
func (a *ProjectAnalyzer) findMethodDecl(astFile *ast.File, filePath, structName, methodName string) *ast.FuncDecl {
	if decl := a.findMethodInFile(astFile, structName, methodName); decl != nil {
		return decl
	}
	files, err := a.parsePackage(filePath, astFile.Name.Name)
	if err == nil {
		for _, file := range files {
			if decl := a.findMethodInFile(file, structName, methodName); decl != nil {
				return decl
			}
		}
	}
	return nil
}

// findMethodInFile finds a method named methodName on structName within one file.
func (a *ProjectAnalyzer) findMethodInFile(astFile *ast.File, structName, methodName string) *ast.FuncDecl {
	for _, decl := range astFile.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != methodName || fn.Body == nil {
			continue
		}
		if a.isMethodOnStruct(fn.Recv, structName) {
			return fn
		}
	}
	return nil
}

// receiverVarName returns the receiver variable name of a method, or "" if the
// receiver is unnamed (e.g. func (*Module) ...).
func receiverVarName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 || len(recv.List[0].Names) == 0 {
		return ""
	}
	return recv.List[0].Names[0].Name
}

// extractHandlerInfo extracts handler name and type information from a route call.
// Returns handler name, request type, and response type.
func (a *ProjectAnalyzer) extractHandlerInfo(
	callExpr *ast.CallExpr,
	astFile *ast.File,
	filePath string,
	structName string,
) (handlerName string, reqType, respType *models.TypeInfo, successStatus int) {
	if len(callExpr.Args) <= 3 {
		return "", nil, nil, 0
	}

	handlerName, receiverType, isPackageFunc, ok := a.resolveHandler(callExpr.Args[3], structName, astFile, filePath)
	if !ok {
		return "", nil, nil, 0
	}

	// Extract handler signature if we have required context.
	if astFile != nil && filePath != "" {
		req, resp, status, err := a.extractHandlerSignature(astFile, filePath, receiverType, isPackageFunc, handlerName)
		if err != nil {
			// Don't fail — some routes use inline or external handlers.
			return handlerName, nil, nil, 0
		}
		return handlerName, req, resp, status
	}

	return handlerName, nil, nil, 0
}

// resolveHandler determines the handler name and where to find its signature from
// the handler argument of a route registration. It supports:
//
//	m.createUser    — a method on the module struct (receiver = the module)
//	m.h.createUser   — a method on the type of a module field (the documented
//	                   Enhanced Handler Pattern; receiver = the field's type)
//	Ping             — a package-level function (isPackageFunc = true)
//
// Returns ok=false when the argument is not a recognizable handler reference
// (e.g. an inline function literal).
func (a *ProjectAnalyzer) resolveHandler(
	arg ast.Expr, moduleStruct string, astFile *ast.File, filePath string,
) (handlerName, receiverType string, isPackageFunc, ok bool) {
	switch h := arg.(type) {
	case *ast.Ident:
		// Bare function reference: server.GET(hr, r, path, Ping).
		return h.Name, "", true, true
	case *ast.SelectorExpr:
		handlerName = h.Sel.Name
		switch x := h.X.(type) {
		case *ast.Ident:
			// m.createUser — the qualifier is the module receiver variable.
			return handlerName, moduleStruct, false, true
		case *ast.SelectorExpr:
			// m.h.createUser — the qualifier is a module field; resolve its type.
			// Only single-level field indirection is supported: x.Sel is taken as a
			// field of the module struct. Deeper chains (m.a.b.createUser) resolve
			// x.Sel against the module and fail closed (no types) if it is not a
			// module field, rather than emitting a wrong schema.
			return handlerName, a.resolveFieldType(moduleStruct, x.Sel.Name, astFile, filePath), false, true
		}
	}
	return "", "", false, false
}

// resolveFieldType returns the base type name of field fieldName on structName
// (leading pointer and package qualifier stripped), or "" if it cannot be
// resolved (struct or field not found).
func (a *ProjectAnalyzer) resolveFieldType(structName, fieldName string, astFile *ast.File, filePath string) string {
	if structName == "" {
		return ""
	}
	structType, err := a.findStructDefinition(astFile, filePath, structName)
	if err != nil || structType.Fields == nil {
		return ""
	}
	for _, field := range structType.Fields.List {
		for _, name := range field.Names {
			if name.Name == fieldName {
				return baseTypeName(field.Type)
			}
		}
	}
	return ""
}

// baseTypeName returns the unqualified type name of an expression, stripping a
// leading pointer and any package qualifier (e.g. *pkg.Handler -> Handler).
func baseTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return baseTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// isHTTPMethod checks if the method name is a valid HTTP method
func (a *ProjectAnalyzer) isHTTPMethod(method string) bool {
	httpMethods := []string{"GET", httpMethodPost, "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}
	methodUpper := strings.ToUpper(method)
	return slices.Contains(httpMethods, methodUpper)
}

// extractRouteMetadata extracts metadata from server.WithXXX calls
func (a *ProjectAnalyzer) extractRouteMetadata(arg ast.Expr, route *models.Route, serverAliases map[string]struct{}) {
	callExpr, ok := arg.(*ast.CallExpr)
	if !ok {
		return
	}

	selExpr, ok := callExpr.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	pkg, ok := selExpr.X.(*ast.Ident)
	if !ok || !a.aliasContains(serverAliases, pkg.Name, frameworkPkgServer) {
		return
	}

	switch selExpr.Sel.Name {
	case "WithTags":
		route.Tags = a.extractStringLiterals(callExpr.Args)
	case "WithSummary":
		route.Summary = a.extractStringFromFirstArg(callExpr)
	case "WithDescription":
		route.Description = a.extractStringFromFirstArg(callExpr)
	case "WithHandlerName":
		if name := a.extractStringFromFirstArg(callExpr); name != "" {
			route.HandlerName = name
		}
	case "WithRawResponse":
		route.RawResponse = true
	}
}

// extractStringFromFirstArg extracts a string from the first argument of a call expression
func (a *ProjectAnalyzer) extractStringFromFirstArg(callExpr *ast.CallExpr) string {
	if len(callExpr.Args) > 0 {
		if lit, ok := callExpr.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			return strings.Trim(lit.Value, `"`)
		}
	}
	return ""
}

// extractStringLiterals extracts string literals from function arguments
func (a *ProjectAnalyzer) extractStringLiterals(args []ast.Expr) []string {
	var results []string
	for _, arg := range args {
		if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			results = append(results, strings.Trim(lit.Value, `"`))
		}
	}
	return results
}

// extractCommentDescription extracts description from comment group
func (a *ProjectAnalyzer) extractCommentDescription(commentGroup *ast.CommentGroup) string {
	if commentGroup == nil {
		return ""
	}

	var lines []string
	for _, comment := range commentGroup.List {
		text := strings.TrimPrefix(comment.Text, "//")
		text = strings.TrimPrefix(text, "/*")
		text = strings.TrimSuffix(text, "*/")
		text = strings.TrimSpace(text)
		if text != "" {
			lines = append(lines, text)
		}
	}

	return strings.Join(lines, " ")
}

// extractPathFromArg resolves a route path argument to a literal string. It
// handles string literals, same-package string constants, and "+" concatenation
// of resolvable operands. The bool result is false when the path could not be
// fully resolved (an unknown identifier, a non-string expression, fmt.Sprintf,
// etc.); the caller then drops the route rather than emitting a garbage path key.
func (a *ProjectAnalyzer) extractPathFromArg(arg ast.Expr) (string, bool) {
	switch expr := arg.(type) {
	case *ast.BasicLit:
		// Direct string literal.
		if expr.Kind == token.STRING {
			return strings.Trim(expr.Value, `"`), true
		}
	case *ast.Ident:
		// Same-package string constant.
		if value, exists := a.constants[expr.Name]; exists {
			return value, true
		}
	case *ast.BinaryExpr:
		// String concatenation: fold only when both operands resolve.
		if expr.Op == token.ADD {
			left, lok := a.extractPathFromArg(expr.X)
			right, rok := a.extractPathFromArg(expr.Y)
			if lok && rok {
				return left + right, true
			}
		}
	}
	return "", false
}

// normalizePath converts an Echo-style route path into OpenAPI 3.0 path
// templating. Echo names path parameters with a leading colon (":id"); OpenAPI
// templates them with braces ("{id}"). Each "/"-delimited segment is rewritten
// independently so literal segments and the leading/trailing slashes are
// preserved verbatim:
//
//	/users/:id             -> /users/{id}
//	/orgs/:orgID/users/:id -> /orgs/{orgID}/users/{id}
//
// A bare ":" with no name is left untouched (defensive — Echo would not register
// such a route, and emitting "{}" would produce an invalid template).
//
// Echo catch-all wildcards ("/files/*", "/assets/*filepath") are intentionally
// left as literal segments. Templating them ("{path}") would require a matching
// declared path parameter to satisfy OpenAPI, but the generator derives path
// parameters from request-struct tags, which a catch-all does not carry —
// emitting "{path}" without that parameter yields an invalid document.
// Synthesising the parameter is deferred to the parameter-fidelity work.
func normalizePath(path string) string {
	if !strings.Contains(path, ":") {
		return path
	}

	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if strings.HasPrefix(seg, ":") && len(seg) > 1 {
			segments[i] = "{" + seg[1:] + "}"
		}
	}

	return strings.Join(segments, "/")
}

// extractConstants finds constant declarations in the AST file
func (a *ProjectAnalyzer) extractConstants(astFile *ast.File) {
	for _, decl := range astFile.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}

		for _, spec := range genDecl.Specs {
			a.processConstSpec(spec)
		}
	}
}

// processConstSpec processes a constant spec to extract constant values
func (a *ProjectAnalyzer) processConstSpec(spec ast.Spec) {
	valueSpec, ok := spec.(*ast.ValueSpec)
	if !ok {
		return
	}

	// Extract const name and value
	for i, name := range valueSpec.Names {
		if i < len(valueSpec.Values) {
			if value := a.extractStringFromExpr(valueSpec.Values[i]); value != "" {
				a.constants[name.Name] = value
			}
		}
	}
}

// extractStringFromExpr extracts string value from an expression
func (a *ProjectAnalyzer) extractStringFromExpr(expr ast.Expr) string {
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		return strings.Trim(lit.Value, `"`)
	}
	return ""
}

// extractPackageDescription extracts description from package-level comments
func (a *ProjectAnalyzer) extractPackageDescription(astFile *ast.File) string {
	if astFile.Doc == nil {
		return ""
	}

	var lines []string
	for _, comment := range astFile.Doc.List {
		text := strings.TrimPrefix(comment.Text, "//")
		text = strings.TrimPrefix(text, "/*")
		text = strings.TrimSuffix(text, "*/")
		text = strings.TrimSpace(text)

		// Skip package declaration comments
		if strings.HasPrefix(text, "Package ") {
			// Extract the description part after the package name
			parts := strings.SplitN(text, " ", 3)
			if len(parts) >= 3 {
				text = strings.TrimSpace(parts[2])
			}
		}

		if text != "" {
			lines = append(lines, text)
		}
	}

	return strings.Join(lines, " ")
}

// parsePackage parses all Go files (excluding tests) within the module's directory
func (a *ProjectAnalyzer) parsePackage(filePath, packageName string) (map[string]*ast.File, error) {
	dir := filepath.Dir(filePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	files := make(map[string]*ast.File)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, goFileExt) || strings.HasSuffix(name, testFileExt) {
			continue
		}

		fullPath := filepath.Join(dir, name)
		astFile, parseErr := parser.ParseFile(a.fileSet, fullPath, nil, parser.ParseComments)
		if parseErr != nil {
			continue
		}
		if astFile.Name.Name == packageName {
			files[fullPath] = astFile
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("package %s not found in %s", packageName, dir)
	}

	return files, nil
}

// extractImportAliases returns the aliases used for a specific import path within a file
func (a *ProjectAnalyzer) extractImportAliases(astFile *ast.File, importPath string) map[string]struct{} {
	aliases := make(map[string]struct{})
	found := false
	for _, imp := range astFile.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path != importPath {
			continue
		}
		found = true
		if imp.Name != nil && imp.Name.Name != "" && imp.Name.Name != "_" && imp.Name.Name != "." {
			aliases[imp.Name.Name] = struct{}{}
			continue
		}
		if imp.Name == nil {
			aliases[filepath.Base(importPath)] = struct{}{}
		}
	}

	if !found {
		return map[string]struct{}{}
	}

	if len(aliases) == 0 {
		aliases[filepath.Base(importPath)] = struct{}{}
	}

	return aliases
}

// collectMethodFlagsFromFile inspects a file for module methods with valid signatures
func (a *ProjectAnalyzer) collectMethodFlagsFromFile(astFile *ast.File, structName string, flags map[string]bool, serverAliases, appAliases map[string]struct{}) {
	for _, decl := range astFile.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Recv == nil {
			continue
		}

		if !a.isMethodOnStruct(funcDecl.Recv, structName) {
			continue
		}

		a.checkMethodSignature(funcDecl, flags, serverAliases, appAliases)
	}
}

// checkMethodSignature checks a single method's signature and updates flags
func (a *ProjectAnalyzer) checkMethodSignature(funcDecl *ast.FuncDecl, flags map[string]bool, serverAliases, appAliases map[string]struct{}) {
	switch funcDecl.Name.Name {
	case moduleMethodName:
		if a.isValidNameSignature(funcDecl) {
			flags[moduleMethodName] = true
		}
	case moduleMethodInit:
		if a.isValidInitSignature(funcDecl, appAliases) {
			flags[moduleMethodInit] = true
		}
	case moduleMethodRegisterRoutes:
		if a.isValidRegisterRoutesSignature(funcDecl, serverAliases) {
			flags[moduleMethodRegisterRoutes] = true
		}
	case moduleMethodShutdown:
		if a.isValidShutdownSignature(funcDecl) {
			flags[moduleMethodShutdown] = true
		}
	}
}

func (a *ProjectAnalyzer) isValidNameSignature(funcDecl *ast.FuncDecl) bool {
	if funcDecl.Type.Params != nil && len(funcDecl.Type.Params.List) > 0 {
		return false
	}
	if funcDecl.Type.Results == nil || len(funcDecl.Type.Results.List) != 1 {
		return false
	}
	if ident, ok := funcDecl.Type.Results.List[0].Type.(*ast.Ident); ok {
		return ident.Name == goTypeString
	}
	return false
}

func (a *ProjectAnalyzer) isValidInitSignature(funcDecl *ast.FuncDecl, appAliases map[string]struct{}) bool {
	if funcDecl.Type.Params == nil || len(funcDecl.Type.Params.List) != 1 {
		return false
	}

	param := funcDecl.Type.Params.List[0]
	starExpr, ok := param.Type.(*ast.StarExpr)
	if !ok {
		return false
	}

	selExpr, ok := starExpr.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	pkgIdent, ok := selExpr.X.(*ast.Ident)
	if !ok || !a.aliasContains(appAliases, pkgIdent.Name, frameworkPkgApp) {
		return false
	}

	if selExpr.Sel.Name != frameworkTypeModuleDeps {
		return false
	}

	if funcDecl.Type.Results == nil || len(funcDecl.Type.Results.List) != 1 {
		return false
	}

	if ident, ok := funcDecl.Type.Results.List[0].Type.(*ast.Ident); ok {
		return ident.Name == frameworkTypeError
	}

	return false
}

func (a *ProjectAnalyzer) isValidRegisterRoutesSignature(funcDecl *ast.FuncDecl, serverAliases map[string]struct{}) bool {
	if funcDecl.Type.Params == nil || len(funcDecl.Type.Params.List) < 2 {
		return false
	}

	firstParam := funcDecl.Type.Params.List[0]
	firstStar, ok := firstParam.Type.(*ast.StarExpr)
	if !ok {
		return false
	}

	firstSel, ok := firstStar.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	firstPkg, ok := firstSel.X.(*ast.Ident)
	if !ok || !a.aliasContains(serverAliases, firstPkg.Name, frameworkPkgServer) {
		return false
	}

	if firstSel.Sel.Name != "HandlerRegistry" {
		return false
	}

	secondParam := funcDecl.Type.Params.List[1]
	secondSel, ok := secondParam.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	secondPkg, ok := secondSel.X.(*ast.Ident)
	if !ok || !a.aliasContains(serverAliases, secondPkg.Name, frameworkPkgServer) {
		return false
	}

	if secondSel.Sel.Name != "RouteRegistrar" {
		return false
	}

	// RegisterRoutes does not return values
	return funcDecl.Type.Results == nil || len(funcDecl.Type.Results.List) == 0
}

func (a *ProjectAnalyzer) isValidShutdownSignature(funcDecl *ast.FuncDecl) bool {
	if funcDecl.Type.Params != nil && len(funcDecl.Type.Params.List) > 0 {
		return false
	}

	if funcDecl.Type.Results == nil || len(funcDecl.Type.Results.List) != 1 {
		return false
	}

	if ident, ok := funcDecl.Type.Results.List[0].Type.(*ast.Ident); ok {
		return ident.Name == frameworkTypeError
	}

	return false
}

func (a *ProjectAnalyzer) aliasContains(aliases map[string]struct{}, name, defaultAlias string) bool {
	if len(aliases) == 0 {
		return name == defaultAlias
	}
	_, ok := aliases[name]
	return ok
}

// validateProjectPath validates that a path is within the project root and safe to read
func (a *ProjectAnalyzer) validateProjectPath(path string) error {
	// Get absolute paths for comparison
	absProjectRoot, err := filepath.Abs(a.projectRoot)
	if err != nil {
		return fmt.Errorf("failed to get absolute project root: %w", err)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Clean both paths to resolve any .. or . components
	cleanProjectRoot := filepath.Clean(absProjectRoot)
	cleanPath := filepath.Clean(absPath)

	// Compute relative path from project root to target path
	relPath, err := filepath.Rel(cleanProjectRoot, cleanPath)
	if err != nil {
		return fmt.Errorf("failed to compute relative path: %w", err)
	}

	// Reject any path that begins with ".." or equals ".."
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return errors.New("path is outside project root")
	}

	return nil
}

// validateGoFilePath validates that a Go file path is safe to read
func (a *ProjectAnalyzer) validateGoFilePath(filePath string) error {
	// Get absolute paths for comparison
	absProjectRoot, err := filepath.Abs(a.projectRoot)
	if err != nil {
		return fmt.Errorf("failed to get absolute project root: %w", err)
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Clean both paths to resolve any .. or . components
	cleanProjectRoot := filepath.Clean(absProjectRoot)
	cleanPath := filepath.Clean(absPath)

	// Compute relative path from project root to target path
	relPath, err := filepath.Rel(cleanProjectRoot, cleanPath)
	if err != nil {
		return fmt.Errorf("failed to compute relative path: %w", err)
	}

	// Reject any path that begins with ".." or equals ".."
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return errors.New("path is outside project root")
	}

	// Reject paths where the relative path contains ".." segments
	if strings.Contains(relPath, "..") {
		return errors.New("path contains directory traversal")
	}

	// Ensure the cleaned path has a .go suffix
	if !strings.HasSuffix(cleanPath, goFileExt) {
		return errors.New("not a Go file")
	}

	return nil
}

// typeInfoFromExpr converts an AST type expression to TypeInfo
// Handles identifiers, pointers, and qualified type names
func (a *ProjectAnalyzer) typeInfoFromExpr(expr ast.Expr, packageName string) *models.TypeInfo {
	switch t := expr.(type) {
	case *ast.Ident:
		return a.handleIdentType(t, packageName)
	case *ast.StarExpr:
		return a.handleStarExprType(t, packageName)
	case *ast.SelectorExpr:
		return a.handleSelectorExprType(t)
	case *ast.IndexExpr:
		// Single-type-param generic, e.g. server.Result[User].
		return a.handleResultWrapper(t.X, t.Index, packageName)
	case *ast.IndexListExpr:
		// Multi-type-param generic, e.g. Foo[A, B]; the response type is the first.
		if len(t.Indices) > 0 {
			return a.handleResultWrapper(t.X, t.Indices[0], packageName)
		}
	}

	return nil
}

// handleResultWrapper unwraps a framework result wrapper (server.Result[R] /
// server.ResultWithMeta[R]) to the inner response type R. Any other generic is
// not a known response carrier, so it returns nil rather than guessing.
func (a *ProjectAnalyzer) handleResultWrapper(x, index ast.Expr, packageName string) *models.TypeInfo {
	if !isResultWrapper(x) {
		return nil
	}
	return a.typeInfoFromExpr(index, packageName)
}

// isResultWrapper reports whether x is server.Result or server.ResultWithMeta.
// (Matches the existing "server" qualifier assumption used by isFrameworkType.)
func isResultWrapper(x ast.Expr) bool {
	sel, ok := x.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != frameworkPkgServer {
		return false
	}
	return sel.Sel.Name == resultTypeName || sel.Sel.Name == resultWithMetaTypeName
}

// handleIdentType processes simple identifier types (e.g., TypeName)
func (a *ProjectAnalyzer) handleIdentType(t *ast.Ident, packageName string) *models.TypeInfo {
	if a.isFrameworkType(t.Name, "") {
		return nil
	}
	return &models.TypeInfo{
		Name:      t.Name,
		Package:   packageName,
		IsPointer: false,
	}
}

// handleStarExprType processes pointer types (e.g., *TypeName or *pkg.TypeName)
func (a *ProjectAnalyzer) handleStarExprType(t *ast.StarExpr, packageName string) *models.TypeInfo {
	// Handle simple pointer: *TypeName
	if ident, ok := t.X.(*ast.Ident); ok {
		if a.isFrameworkType(ident.Name, "") {
			return nil
		}
		return &models.TypeInfo{
			Name:      ident.Name,
			Package:   packageName,
			IsPointer: true,
		}
	}

	// Handle qualified pointer: *pkg.TypeName
	if selExpr, ok := t.X.(*ast.SelectorExpr); ok {
		if pkg, ok := selExpr.X.(*ast.Ident); ok {
			if a.isFrameworkType(selExpr.Sel.Name, pkg.Name) {
				return nil
			}
			return &models.TypeInfo{
				Name:      selExpr.Sel.Name,
				Package:   pkg.Name,
				IsPointer: true,
			}
		}
	}

	return nil
}

// handleSelectorExprType processes qualified types (e.g., pkg.TypeName)
func (a *ProjectAnalyzer) handleSelectorExprType(t *ast.SelectorExpr) *models.TypeInfo {
	pkg, ok := t.X.(*ast.Ident)
	if !ok {
		return nil
	}

	// server.NoContentResult is a bodyless 204 response — carry a marker (no
	// Name/Fields, so no component is generated) rather than treating it as a
	// schema-bearing response type.
	if pkg.Name == frameworkPkgServer && t.Sel.Name == noContentResultTypeName {
		return &models.TypeInfo{NoContent: true}
	}

	if a.isFrameworkType(t.Sel.Name, pkg.Name) {
		return nil
	}

	return &models.TypeInfo{
		Name:      t.Sel.Name,
		Package:   pkg.Name,
		IsPointer: false,
	}
}

// extractRequestType extracts request type from handler parameters.
// Returns the first non-framework type parameter, or nil if none found.
func (a *ProjectAnalyzer) extractRequestType(params *ast.FieldList, packageName string) *models.TypeInfo {
	if params == nil || len(params.List) == 0 {
		return nil
	}

	// Return the first parameter that is not a framework type
	// (HandlerContext can appear in first or second position)
	for _, p := range params.List {
		if ti := a.typeInfoFromExpr(p.Type, packageName); ti != nil {
			return ti
		}
	}

	return nil
}

// extractResponseType extracts response type from handler return values.
// Returns the first non-framework type result, or nil if none found.
func (a *ProjectAnalyzer) extractResponseType(results *ast.FieldList, packageName string) *models.TypeInfo {
	if results == nil || len(results.List) == 0 {
		return nil
	}

	// First result is response type (second is IAPIError or error, filtered by typeInfoFromExpr)
	firstResult := results.List[0]
	return a.typeInfoFromExpr(firstResult.Type, packageName)
}

// findHandlerInFile searches a single AST file for a handler method
// Returns request and response TypeInfo if found
func (a *ProjectAnalyzer) findHandlerInFile(
	astFile *ast.File,
	receiverType string,
	isPackageFunc bool,
	handlerName string,
) (requestType, responseType *models.TypeInfo, successStatus int) {
	for _, decl := range astFile.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Name.Name != handlerName {
			continue
		}

		// Check the receiver matches the resolved handler target.
		if !a.handlerReceiverMatches(funcDecl.Recv, receiverType, isPackageFunc) {
			continue
		}

		// Extract types using helpers
		requestType := a.extractRequestType(funcDecl.Type.Params, astFile.Name.Name)
		responseType := a.extractResponseType(funcDecl.Type.Results, astFile.Name.Name)
		status := extractSuccessStatus(funcDecl)

		return requestType, responseType, status
	}

	return nil, nil, 0
}

// extractSuccessStatus inspects a handler body for the result constructor used in
// its return statements and maps it to an HTTP success status: server.Created ->
// 201, Accepted -> 202, NoContent -> 204, NewResult(s, ...)/
// NewResultWithMeta(s, ...) -> s (when s is an integer literal or an http.StatusXxx
// constant). Returns 0 ("use the default" -> 200) when no recognizable server
// constructor is returned.
//
// The LAST recognized return wins: idiomatic handlers put the happy-path return
// last and any earlier server-constructor returns are typically guard branches
// (e.g. a cache-hit early return), so the terminal status is the documented one.
// The "server" qualifier is matched literally, consistent with the rest of the
// type-resolution path (isResultWrapper, NoContentResult).
func extractSuccessStatus(funcDecl *ast.FuncDecl) int {
	if funcDecl.Body == nil {
		return 0
	}
	status := 0
	ast.Inspect(funcDecl.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		call, ok := ret.Results[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != frameworkPkgServer {
			return true
		}
		if s := statusForConstructor(sel.Sel.Name, call.Args); s != 0 {
			status = s // last-wins: keep scanning, terminal return is authoritative
		}
		return true
	})
	return status
}

// statusForConstructor maps a server result-constructor name to its HTTP status.
// For NewResult/NewResultWithMeta the status is the first argument resolved via
// statusFromArg (an integer literal or an http.StatusXxx constant). Returns 0 for
// anything unrecognized.
func statusForConstructor(name string, args []ast.Expr) int {
	switch name {
	case "Created":
		return 201
	case "Accepted":
		return 202
	case "NoContent":
		return 204
	case "NewResult", "NewResultWithMeta":
		if len(args) > 0 {
			return statusFromArg(args[0])
		}
	}
	return 0
}

// statusFromArg resolves the status argument of NewResult/NewResultWithMeta. It
// accepts a bare integer literal (NewResult(201, ...)) and the idiomatic
// net/http status constant (NewResult(http.StatusCreated, ...)), which is the
// dominant real-world form. Returns 0 when the argument is anything else (a
// variable, a non-http constant), letting the caller fall back to the default.
func statusFromArg(arg ast.Expr) int {
	switch a := arg.(type) {
	case *ast.BasicLit:
		if a.Kind == token.INT {
			if v, err := strconv.Atoi(a.Value); err == nil {
				return v
			}
		}
	case *ast.SelectorExpr:
		if pkg, ok := a.X.(*ast.Ident); ok && pkg.Name == stdlibPkgHTTP {
			return httpStatusConstants[a.Sel.Name]
		}
	}
	return 0
}

// httpStatusConstants maps the net/http 2xx status-constant names to their codes.
// Only success codes are needed: a handler returning a 4xx/5xx as a non-error
// Result is a misuse the generator does not try to model as a success response.
var httpStatusConstants = map[string]int{
	"StatusOK":                   200,
	"StatusCreated":              201,
	"StatusAccepted":             202,
	"StatusNonAuthoritativeInfo": 203,
	"StatusNoContent":            204,
	"StatusResetContent":         205,
	"StatusPartialContent":       206,
	"StatusMultiStatus":          207,
	"StatusAlreadyReported":      208,
	"StatusIMUsed":               226,
}

// handlerReceiverMatches reports whether a function declaration's receiver matches
// the resolved handler target: a package-level function must have no receiver,
// otherwise the receiver type must equal receiverType.
func (a *ProjectAnalyzer) handlerReceiverMatches(recv *ast.FieldList, receiverType string, isPackageFunc bool) bool {
	if isPackageFunc {
		return recv == nil
	}
	if receiverType == "" {
		return false
	}
	return a.isMethodOnStruct(recv, receiverType)
}

// extractHandlerSignature extracts request and response type information from a handler method
// Searches current file first, then falls back to other files in the package
// Also populates struct fields for discovered types
func (a *ProjectAnalyzer) extractHandlerSignature(
	astFile *ast.File,
	filePath string,
	receiverType string,
	isPackageFunc bool,
	handlerName string,
) (reqType, respType *models.TypeInfo, successStatus int, err error) {
	// Try current file first
	if reqType, respType, status := a.findHandlerInFile(astFile, receiverType, isPackageFunc, handlerName); reqType != nil || respType != nil {
		a.populateTypeFields(reqType, astFile, filePath)
		a.populateTypeFields(respType, astFile, filePath)
		return reqType, respType, status, nil
	}

	// Try other files in the package
	files, err := a.parsePackage(filePath, astFile.Name.Name)
	if err == nil && files != nil {
		for _, file := range files {
			if reqType, respType, status := a.findHandlerInFile(file, receiverType, isPackageFunc, handlerName); reqType != nil || respType != nil {
				a.populateTypeFields(reqType, file, filePath)
				a.populateTypeFields(respType, file, filePath)
				return reqType, respType, status, nil
			}
		}
	}

	// Handler not found - this is not necessarily an error
	// Some routes might use inline handlers or external handlers
	return nil, nil, 0, fmt.Errorf("handler %s not found for receiver %q", handlerName, receiverType)
}

// populateTypeFields populates a request/response TypeInfo's fields and registers
// every named struct type reachable from it (nested, sliced, pointed-to, or
// recursive) into the analyzer's type registry, so the generator can emit a
// component per type and $ref between them.
func (a *ProjectAnalyzer) populateTypeFields(typeInfo *models.TypeInfo, astFile *ast.File, filePath string) {
	if typeInfo == nil {
		return
	}
	registered := a.registerType(typeInfo.Name, typeInfo.Package, astFile, filePath)
	if registered == nil && typeInfo.Package != "" {
		// A qualified request/response type (e.g. server.Result[types.Money]) arrives
		// as Name="Money", Package="types" (the import alias). Resolve it
		// cross-package using that alias against the handler file's imports.
		registered = a.registerQualifiedType(typeInfo.Package+"."+typeInfo.Name, astFile)
	}
	if registered == nil {
		return
	}
	// Mirror the registered fields onto the route's TypeInfo (request bodies and
	// responses read Fields directly). Adopt the final (collision-qualified) name
	// too, so the response envelope's $ref points at the component actually emitted.
	typeInfo.Name = registered.Name
	typeInfo.Fields = registered.Fields
	typeInfo.JOSE = registered.JOSE
}

// registerType resolves the named type and registers it (and its struct-typed
// fields) into the type registry, returning its TypeInfo (whose Name is the final,
// possibly collision-qualified, schema name). A qualified name (pkg.Type) is
// resolved cross-package when it points at an in-module package; stdlib and
// third-party types return nil (left to the generator's well-known map / object
// fallback). Returns nil when the name is not a resolvable struct.
func (a *ProjectAnalyzer) registerType(name, pkg string, astFile *ast.File, filePath string) *models.TypeInfo {
	if strings.Contains(name, ".") {
		return a.registerQualifiedType(name, astFile)
	}
	structType, err := a.findStructDefinition(astFile, filePath, name)
	if err != nil {
		return nil
	}
	return a.registerStruct(name, pkg, structType, astFile, filePath)
}

// registerStruct registers a resolved struct under its collision-qualified schema
// name and recurses into its fields. It registers BEFORE recursing so self- or
// mutually-referential types (e.g. Category{Parent *Category}) terminate.
func (a *ProjectAnalyzer) registerStruct(typeName, pkg string, structType *ast.StructType, astFile *ast.File, filePath string) *models.TypeInfo {
	key := a.schemaKey(typeName, pkg)
	if existing, ok := a.typeRegistry[key]; ok {
		return existing
	}

	ti := &models.TypeInfo{Name: key, Package: pkg}
	a.typeRegistry[key] = ti // register before recursing (cycle guard)
	// Seed the embedded-promotion ancestor set with this type so a self-embed
	// (e.g. type Node struct{ *Node }) is not promoted into itself.
	ti.Fields = a.extractStructFields(structType, pkg, astFile, filePath, map[string]struct{}{typeName: {}})
	ti.JOSE = hasJOSESentinelTag(structType)

	for i := range ti.Fields {
		a.registerFieldRef(&ti.Fields[i], pkg, astFile, filePath)
	}
	return ti
}

// schemaKey returns a stable, collision-free component name for (pkg, typeName).
// It prefers the bare type name and falls back to <Pkg><TypeName> (then a numeric
// suffix) when that bare name is already taken by a different package, so two
// packages each defining e.g. Request get distinct components. Idempotent per
// (pkg, typeName).
//
// On a collision the bare name goes to whichever (pkg, typeName) is registered
// first; which one that is follows discovery order. Discovery walks the project
// deterministically, so a given source tree yields a stable assignment, but the
// emitted spec is always valid either way — every $ref resolves to the component
// the field/response actually carries (RefName / TypeInfo.Name are the final
// key). A future enhancement could make the choice order-independent (e.g. always
// qualify by package), but that is a naming-policy change, not a correctness fix.
func (a *ProjectAnalyzer) schemaKey(typeName, pkg string) string {
	k := pkg + "\x00" + typeName
	if name, ok := a.nameAssign[k]; ok {
		return name
	}
	final := typeName
	if _, taken := a.usedNames[final]; taken {
		final = exportedPkgName(pkg) + typeName
		for i := 2; ; i++ {
			if _, dup := a.usedNames[final]; !dup {
				break
			}
			final = exportedPkgName(pkg) + typeName + strconv.Itoa(i)
		}
	}
	a.usedNames[final] = struct{}{}
	a.nameAssign[k] = final
	return final
}

// qualifiedStruct is a struct resolved from another in-module package, with the
// context needed to recurse into it in its own package.
type qualifiedStruct struct {
	typeName string
	pkg      string
	st       *ast.StructType
	file     *ast.File
	filePath string
}

// resolveQualifiedStruct resolves a pkg.Type reference against astFile's imports.
// It parses the in-module target package (with a per-dir cache) and finds the
// named struct. Returns ok=false for stdlib/third-party imports, unknown aliases,
// or names that are not a struct in the target package.
func (a *ProjectAnalyzer) resolveQualifiedStruct(qualified string, astFile *ast.File) (qualifiedStruct, bool) {
	dot := strings.LastIndex(qualified, ".")
	if dot < 0 {
		return qualifiedStruct{}, false
	}
	pkgAlias, typeName := qualified[:dot], qualified[dot+1:]
	importPath, ok := a.fileImports(astFile)[pkgAlias]
	if !ok {
		return qualifiedStruct{}, false // unknown alias (e.g. dot/blank import)
	}
	dir, ok := a.inModuleDir(importPath)
	if !ok {
		return qualifiedStruct{}, false // stdlib/third-party — well-known map or object
	}
	files, err := a.parsePackageDir(dir)
	if err != nil {
		return qualifiedStruct{}, false
	}
	// Iterate files in a stable order so the resolved definition is deterministic
	// even when several files in the dir could match (e.g. build-tagged variants).
	for _, path := range slices.Sorted(maps.Keys(files)) {
		file := files[path]
		if st := a.findStructInFile(file, typeName); st != nil {
			return qualifiedStruct{typeName: typeName, pkg: file.Name.Name, st: st, file: file, filePath: path}, true
		}
	}
	return qualifiedStruct{}, false // not a struct in the target package (alias/interface)
}

// registerQualifiedType resolves and registers a cross-package struct as a
// component, returning nil when it cannot be resolved in-module.
func (a *ProjectAnalyzer) registerQualifiedType(qualified string, astFile *ast.File) *models.TypeInfo {
	q, ok := a.resolveQualifiedStruct(qualified, astFile)
	if !ok {
		return nil
	}
	return a.registerStruct(q.typeName, q.pkg, q.st, q.file, q.filePath)
}

// inModuleDir translates an import path to a filesystem dir under projectRoot when
// it is within this module, else reports false.
func (a *ProjectAnalyzer) inModuleDir(importPath string) (string, bool) {
	if a.modulePath == "" {
		return "", false
	}
	if importPath == a.modulePath {
		return a.projectRoot, true
	}
	prefix := a.modulePath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(importPath, prefix)
	return filepath.Join(a.projectRoot, filepath.FromSlash(rel)), true
}

// fileImports maps each import's local alias to its import path. The default alias
// is the path's last segment (an approximation of the package name, correct for
// the common case where they match); explicit aliases override it. Blank (_) and
// dot (.) imports are skipped.
func (a *ProjectAnalyzer) fileImports(astFile *ast.File) map[string]string {
	out := make(map[string]string, len(astFile.Imports))
	for _, imp := range astFile.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		alias := filepath.Base(path)
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				continue
			}
			alias = imp.Name.Name
		}
		out[alias] = path
	}
	return out
}

// parsePackageDir parses all non-test Go files in a directory, caching the result
// per directory so repeated cross-package lookups reuse one parse.
func (a *ProjectAnalyzer) parsePackageDir(dir string) (map[string]*ast.File, error) {
	if cached, ok := a.pkgCache[dir]; ok {
		return cached, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make(map[string]*ast.File)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, goFileExt) || strings.HasSuffix(name, testFileExt) {
			continue
		}
		full := filepath.Join(dir, name)
		f, perr := parser.ParseFile(a.fileSet, full, nil, parser.ParseComments)
		if perr != nil {
			continue
		}
		files[full] = f
	}
	a.pkgCache[dir] = files
	return files, nil
}

// exportedPkgName upper-cases the first letter of a package name for use as a
// component-name qualifier (orders -> Orders). Returns "" unchanged.
func exportedPkgName(pkg string) string {
	if pkg == "" {
		return ""
	}
	return strings.ToUpper(pkg[:1]) + pkg[1:]
}

// registerFieldRef registers the named struct type(s) a field references and
// stamps the matching (final, collision-qualified) ref name onto the field. A map
// field refs its value struct via MapValueRefName (a map is never itself a $ref);
// any other field refs its underlying struct (after pointer/slice unwrap) via
// RefName. Fields excluded from JSON (json:"-") are skipped so a type reachable
// only through them is not registered as an orphan component.
func (a *ProjectAnalyzer) registerFieldRef(f *models.FieldInfo, pkg string, astFile *ast.File, filePath string) {
	if f.JSONName == jsonSkipValue && f.ParamType == "" {
		return
	}
	if vName, isMap := mapValueStructName(f.Type); isMap {
		if reg := a.registerType(vName, pkg, astFile, filePath); reg != nil {
			f.MapValueRefName = reg.Name
		}
		return
	}
	if reg := a.registerType(baseStructTypeName(f.Type), pkg, astFile, filePath); reg != nil {
		f.RefName = reg.Name
		return // a struct ref carries no scalar underlying kind
	}
	// Not a struct: classify a named, non-struct scalar (Cents -> integer, etc.).
	f.UnderlyingKind = a.resolveUnderlyingKind(f.Type, astFile, filePath)
}

// knownUnderlyingKinds maps qualified stdlib/library types with a non-struct
// scalar underlying type to their OpenAPI kind. (time.Time is a struct handled by
// the generator's well-known map, so it is intentionally absent.)
var knownUnderlyingKinds = map[string]string{
	"time.Duration": "integer",
}

// resolveUnderlyingKind returns the OpenAPI 3-way kind ("integer"/"number"/
// "string") of a named, non-struct scalar type, or "" when the type is a builtin
// primitive (handled directly), a struct, or unresolved. It strips pointer/slice
// markers, recognizes a small set of qualified stdlib types, and resolves
// local `type X <primitive>` declarations to their underlying kind.
func (a *ProjectAnalyzer) resolveUnderlyingKind(typeStr string, astFile *ast.File, filePath string) string {
	base := baseStructTypeName(typeStr)
	if k, ok := knownUnderlyingKinds[base]; ok {
		return k
	}
	if strings.Contains(base, ".") {
		return "" // other qualified types: not classified here
	}
	if primitiveKind(base) != "" {
		return "" // a builtin used directly is not a named wrapper
	}
	return a.namedScalarKind(base, astFile, filePath, 0)
}

// namedScalarKind resolves a LOCAL named type to its underlying primitive kind,
// following alias chains (type Cents int64 -> integer; type A B; type B int -> A
// resolves to integer). depth bounds pathological chains.
func (a *ProjectAnalyzer) namedScalarKind(name string, astFile *ast.File, filePath string, depth int) string {
	if depth > 8 {
		return ""
	}
	underlying, ok := a.localTypeUnderlying(name, astFile, filePath)
	if !ok {
		return ""
	}
	if k := primitiveKind(underlying); k != "" {
		return k // underlying is a builtin scalar
	}
	return a.namedScalarKind(underlying, astFile, filePath, depth+1) // chained named type
}

// primitiveKind maps a Go builtin scalar type name to its OpenAPI 3-way kind, or
// "" if it is not one of them. It reuses the constraint mapper's type classifiers
// so the integer/float/string sets live in one place.
func primitiveKind(goType string) string {
	switch {
	case isIntegerType(goType):
		return "integer"
	case isFloatType(goType):
		return "number"
	case isStringType(goType):
		return goTypeString
	}
	return ""
}

// localTypeUnderlying finds a local `type Name <ident>` declaration and returns
// the underlying identifier (e.g. Cents -> "int64"). Returns ok=false when Name
// is not a local non-struct named type.
func (a *ProjectAnalyzer) localTypeUnderlying(name string, astFile *ast.File, filePath string) (string, bool) {
	search := func(file *ast.File) (string, bool) {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != name {
					continue
				}
				if ident, ok := ts.Type.(*ast.Ident); ok {
					return ident.Name, true
				}
				return "", false // underlying is not a bare ident (struct/slice/etc.)
			}
		}
		return "", false
	}
	if u, ok := search(astFile); ok {
		return u, true
	}
	// Sibling files in the same package, via the cached per-dir parse.
	files, err := a.parsePackageDir(filepath.Dir(filePath))
	if err != nil {
		return "", false
	}
	for _, file := range files {
		if u, ok := search(file); ok {
			return u, true
		}
	}
	return "", false
}

// baseStructTypeName strips slice and pointer markers from a Go type string to
// expose the underlying named type (e.g. "[]*Address" -> "Address"). Qualified
// types (pkg.T) and maps are returned as-is and will not resolve to a local
// struct (cross-package and map value resolution are handled separately).
func baseStructTypeName(t string) string {
	for {
		switch {
		case strings.HasPrefix(t, "*"):
			t = t[1:]
		case strings.HasPrefix(t, "[]"):
			t = t[2:]
		default:
			return t
		}
	}
}

// mapValueType reports whether goType is a map (after an optional leading
// pointer) and returns its value type string verbatim. For map[string]Address it
// returns ("Address", true); for map[string][]Address it returns ("[]Address",
// true). The key is assumed simple (no nested brackets), which holds for
// JSON-serializable string-keyed maps.
//
// NOTE: the generator keeps a twin of this pure parser (generator.mapValueType);
// it is intentionally NOT exported and shared, to avoid an exported cross-package
// helper (which the repo convention would require to carry a context.Context the
// pure parser has no use for). Keep the two in sync.
func mapValueType(goType string) (string, bool) {
	goType = strings.TrimPrefix(goType, "*")
	if !strings.HasPrefix(goType, "map[") {
		return "", false
	}
	rest := goType[len("map["):]
	i := strings.IndexByte(rest, ']')
	if i < 0 {
		return "", false
	}
	return rest[i+1:], true
}

// mapValueStructName returns the base struct name of a map's value type (after
// unwrapping a pointer/slice on the value). For map[string]Address ->
// ("Address", true); for map[string]string -> ("string", true), where the
// caller's registerType lookup then fails for the primitive, leaving
// MapValueRefName empty.
func mapValueStructName(t string) (string, bool) {
	v, ok := mapValueType(t)
	if !ok {
		return "", false
	}
	return baseStructTypeName(v), true
}

// hasJOSESentinelTag reports whether the struct uses the JOSE sentinel-field
// convention — a blank-identifier field (`_`) carrying a `jose:"..."` struct tag.
// Restricting to the sentinel pattern matches the documented convention and avoids
// false-positives where a regular field happens to use the same tag namespace for
// something else (the runtime jose.ScanType *would* try to parse such a tag and fail,
// so the OpenAPI spec for that struct should not pre-emptively claim JOSE wrapping).
//
// reflect.StructTag.Lookup is used rather than a substring match on Tag.Value because
// substring matching false-positives on tag values that contain the literal `jose:"`
// (e.g., a description tag escaping a quoted reference). Importing the runtime jose
// package is not an option because the openapi tool is in its own go.mod.
func hasJOSESentinelTag(s *ast.StructType) bool {
	if s == nil || s.Fields == nil {
		return false
	}
	for _, field := range s.Fields.List {
		if field.Tag == nil {
			continue
		}
		// Sentinel field: exactly one blank-identifier name. Embedded fields have
		// len(Names) == 0 — those don't match the convention.
		if len(field.Names) != 1 || field.Names[0].Name != "_" {
			continue
		}
		raw := strings.Trim(field.Tag.Value, "`")
		if _, ok := reflect.StructTag(raw).Lookup("jose"); ok {
			return true
		}
	}
	return false
}

// findStructDefinition searches for a struct type definition by name
// Searches current file first, then other files in the package
func (a *ProjectAnalyzer) findStructDefinition(
	astFile *ast.File,
	filePath string,
	typeName string,
) (*ast.StructType, error) {
	// Search current file first
	if structType := a.findStructInFile(astFile, typeName); structType != nil {
		return structType, nil
	}

	// Try other files in the package
	files, err := a.parsePackage(filePath, astFile.Name.Name)
	if err == nil && files != nil {
		for _, file := range files {
			if structType := a.findStructInFile(file, typeName); structType != nil {
				return structType, nil
			}
		}
	}

	return nil, fmt.Errorf("struct %s not found", typeName)
}

// findStructInFile searches a single AST file for a struct type definition,
// returning the struct type or nil if not found.
func (a *ProjectAnalyzer) findStructInFile(astFile *ast.File, typeName string) *ast.StructType {
	for _, decl := range astFile.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != typeName {
				continue
			}

			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			return structType
		}
	}

	return nil
}

// extractStructFields extracts field information from a struct type including
// struct tags. astFile/filePath locate sibling files for resolving embedded
// struct types; visited is the embedded-promotion ancestor set (cycle guard).
//
// Direct named fields and json-tagged embeds (which nest at the parent's level)
// are "shallow"; fields flattened from an untagged embed are "promoted" (deeper).
// On a JSON-name collision the shallow field wins, mirroring encoding/json's
// shallower-depth-wins rule (so a parent field is never silently overridden by a
// promoted one, regardless of declaration order).
func (a *ProjectAnalyzer) extractStructFields(structType *ast.StructType, pkg string, astFile *ast.File, filePath string, visited map[string]struct{}) []models.FieldInfo {
	if structType.Fields == nil {
		return nil
	}

	var shallow, promoted []models.FieldInfo
	for _, field := range structType.Fields.List {
		if len(field.Names) == 0 {
			fields, isPromotion := a.embeddedFields(field, pkg, astFile, filePath, visited)
			if isPromotion {
				promoted = append(promoted, fields...)
			} else {
				shallow = append(shallow, fields...)
			}
			continue
		}
		shallow = append(shallow, a.namedFields(field)...)
	}

	return mergeFieldsByPrecedence(shallow, promoted)
}

// namedFields builds FieldInfo entries for a single non-anonymous AST field
// (one per exported name; unexported names are skipped).
func (a *ProjectAnalyzer) namedFields(field *ast.Field) []models.FieldInfo {
	out := make([]models.FieldInfo, 0, len(field.Names))
	for _, fieldName := range field.Names {
		if !fieldName.IsExported() {
			continue
		}
		out = append(out, a.buildFieldInfo(fieldName.Name, field))
	}
	return out
}

// mergeFieldsByPrecedence concatenates shallow then promoted fields, dropping any
// promoted field whose JSON name already appears (so shallower wins). Among
// promoted fields a colliding name keeps the first (equal-depth ambiguity is
// rare; encoding/json would drop both, but documenting one is more useful).
func mergeFieldsByPrecedence(shallow, promoted []models.FieldInfo) []models.FieldInfo {
	seen := make(map[string]struct{}, len(shallow))
	for i := range shallow {
		if n := shallow[i].JSONName; n != "" {
			seen[n] = struct{}{}
		}
	}
	out := shallow
	for i := range promoted {
		if n := promoted[i].JSONName; n != "" {
			if _, dup := seen[n]; dup {
				continue
			}
			seen[n] = struct{}{}
		}
		out = append(out, promoted[i])
	}
	return out
}

// embeddedFields handles an anonymous (embedded) struct field, mirroring
// encoding/json promotion rules. It returns the resulting fields and whether they
// were PROMOTED (flattened from the embed, i.e. deeper) vs nested at the parent
// level:
//   - With an explicit json name (Base `json:"base"`) it NESTS as a single field
//     of the embedded type (rendered as a $ref) — returned as shallow.
//   - With json:"-" it is excluded.
//   - Otherwise its exported fields are PROMOTED (flattened) into the parent.
//
// Embedded pointers (*Base) are unwrapped. A cross-package (in-module) embedded
// struct is resolved and promoted from its own package. An embedded type that
// still cannot be resolved to a struct — a stdlib/third-party type or a named
// non-struct like `type Code string` — is skipped without crashing. The visited
// ancestor set (add-before-recurse / delete-after backtracking) terminates self-
// and mutually-embedded cycles.
func (a *ProjectAnalyzer) embeddedFields(field *ast.Field, pkg string, astFile *ast.File, filePath string, visited map[string]struct{}) (fields []models.FieldInfo, promoted bool) {
	typeName := baseStructTypeName(a.typeToString(field.Type))

	// An explicit json name turns embedding into nesting (a parent-level field).
	if field.Tag != nil {
		jsonName := a.parseStructTags(strings.Trim(field.Tag.Value, "`")).jsonName
		switch jsonName {
		case jsonSkipValue:
			return nil, false // json:"-" — excluded
		case "":
			// no explicit name — fall through to promotion
		default:
			return []models.FieldInfo{a.buildFieldInfo(typeName, field)}, false
		}
	}

	if _, seen := visited[typeName]; seen {
		return nil, false // cycle / self-embed guard
	}
	// Local embed: promote the struct's fields from this package.
	if embedded, err := a.findStructDefinition(astFile, filePath, typeName); err == nil {
		visited[typeName] = struct{}{}
		fields = a.extractStructFields(embedded, pkg, astFile, filePath, visited)
		delete(visited, typeName) // backtrack: keep visited scoped to the current chain
		return fields, true
	}
	// Cross-package (in-module) embed: resolve and promote from the target package
	// so its fields are extracted in their own package context.
	if q, ok := a.resolveQualifiedStruct(typeName, astFile); ok {
		visited[typeName] = struct{}{}
		fields = a.extractStructFields(q.st, q.pkg, q.file, q.filePath, visited)
		delete(visited, typeName)
		return fields, true
	}
	return nil, false // unresolvable (stdlib/third-party or non-struct) — skip, never crash
}

// buildFieldInfo creates a FieldInfo from a field name and AST field
func (a *ProjectAnalyzer) buildFieldInfo(name string, field *ast.Field) models.FieldInfo {
	fieldInfo := models.FieldInfo{
		Name:        name,
		Type:        a.typeToString(field.Type),
		Constraints: make(map[string]string),
	}

	// Parse struct tags if present
	if field.Tag != nil {
		a.parseFieldTags(&fieldInfo, field.Tag)
	}

	return fieldInfo
}

// parseFieldTags parses struct tags and populates the FieldInfo
func (a *ProjectAnalyzer) parseFieldTags(fieldInfo *models.FieldInfo, tag *ast.BasicLit) {
	tagValue := strings.Trim(tag.Value, "`")
	tags := a.parseStructTags(tagValue)

	fieldInfo.JSONName = tags.jsonName
	fieldInfo.ParamType = tags.paramType
	fieldInfo.ParamName = tags.paramName
	fieldInfo.Description = tags.description
	fieldInfo.Example = tags.example
	fieldInfo.RawValidation = tags.rawValidation

	// Parse validation constraints
	if tags.rawValidation != "" {
		fieldInfo.Constraints = a.parseValidationTag(tags.rawValidation)
		// Set Required flag based on constraints
		if fieldInfo.Constraints[constraintRequired] == boolTrueString {
			fieldInfo.Required = true
		}
	}
}

// typeToString converts an AST type expression to a string representation
func (a *ProjectAnalyzer) typeToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name

	case *ast.StarExpr:
		return "*" + a.typeToString(t.X)

	case *ast.ArrayType:
		return "[]" + a.typeToString(t.Elt)

	case *ast.MapType:
		return "map[" + a.typeToString(t.Key) + "]" + a.typeToString(t.Value)

	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok {
			return pkg.Name + "." + t.Sel.Name
		}

	case *ast.InterfaceType:
		return "interface{}"
	}

	return "unknown"
}

// parsedTags holds the extracted information from struct field tags
type parsedTags struct {
	jsonName      string
	paramType     string
	paramName     string
	description   string
	example       string
	rawValidation string
}

// parseJSONTagName extracts the field name from a json tag, handling the special "-" sentinel
// Returns the field name or "-" if the field should be skipped
func (a *ProjectAnalyzer) parseJSONTagName(jsonTag string) string {
	if jsonTag == "" {
		return ""
	}

	// Split by comma and take first part as field name
	// Example: "fieldName,omitempty" -> "fieldName"
	parts := strings.Split(jsonTag, ",")
	if len(parts) == 0 {
		return ""
	}

	switch parts[0] {
	case jsonSkipValue:
		// Preserve "-" sentinel so downstream code can skip field
		return jsonSkipValue
	default:
		return parts[0]
	}
}

// parseParameterTags extracts parameter type and name from param/query/header tags
// Precedence: header > query > param (last one wins if multiple are present)
// Returns paramType ("path", "query", "header") and paramName
func (a *ProjectAnalyzer) parseParameterTags(tag string) (paramType, paramName string) {
	// Check param tag: `param:"id"`
	if paramTag := a.extractTag(tag, tagParam); paramTag != "" {
		paramType = paramTypePath
		paramName = paramTag
	}

	// Check query tag: `query:"page"` (overrides param)
	if queryTag := a.extractTag(tag, tagQuery); queryTag != "" {
		paramType = paramTypeQuery
		paramName = queryTag
	}

	// Check header tag: `header:"Authorization"` (overrides query and param)
	if headerTag := a.extractTag(tag, tagHeader); headerTag != "" {
		paramType = paramTypeHeader
		paramName = headerTag
	}

	return paramType, paramName
}

// parseStructTags extracts relevant information from struct field tags
// Returns a parsedTags struct containing JSONName, ParamType, ParamName, Description, Example, and RawValidation
func (a *ProjectAnalyzer) parseStructTags(tag string) parsedTags {
	if tag == "" {
		return parsedTags{}
	}

	var result parsedTags

	// Parse json tag: `json:"fieldName,omitempty"`
	if jsonTag := a.extractTag(tag, tagJSON); jsonTag != "" {
		result.jsonName = a.parseJSONTagName(jsonTag)
	}

	// Parse parameter tags (param/query/header)
	result.paramType, result.paramName = a.parseParameterTags(tag)

	// Parse doc tag: `doc:"User email address"`
	result.description = a.extractTag(tag, tagDoc)

	// Parse example tag: `example:"user@example.com"`
	result.example = a.extractTag(tag, tagExample)

	// Parse validate tag: `validate:"required,email,min=5"`
	result.rawValidation = a.extractTag(tag, tagValidate)

	return result
}

// extractTag extracts a specific tag value from a struct tag string
// Handles both quoted and unquoted tag values
func (a *ProjectAnalyzer) extractTag(tagStr, tagName string) string {
	// Look for tagName:"value" or tagName:`value`
	prefix := tagName + `:"`
	startIdx := strings.Index(tagStr, prefix)
	if startIdx == -1 {
		// Try backtick version
		prefix = tagName + ":`"
		startIdx = strings.Index(tagStr, prefix)
		if startIdx == -1 {
			return ""
		}
	}

	startIdx += len(prefix)
	endIdx := strings.IndexByte(tagStr[startIdx:], '"')
	if endIdx == -1 {
		endIdx = strings.IndexByte(tagStr[startIdx:], '`')
		if endIdx == -1 {
			return ""
		}
	}

	return tagStr[startIdx : startIdx+endIdx]
}

// parseValidationTag parses a validation tag string into a constraints map
// Example: "required,email,min=5,max=100" -> {"required": "true", "email": "true", "min": "5", "max": "100"}
func (a *ProjectAnalyzer) parseValidationTag(validateTag string) map[string]string {
	constraints := make(map[string]string)
	if validateTag == "" {
		return constraints
	}

	// Split by comma
	rules := strings.Split(validateTag, ",")
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}

		// Check if rule has a value (e.g., "min=5")
		if equalIdx := strings.IndexByte(rule, '='); equalIdx != -1 {
			key := rule[:equalIdx]
			value := rule[equalIdx+1:]
			constraints[key] = value
		} else {
			// Boolean constraint (e.g., "required", "email")
			constraints[rule] = boolTrueString
		}
	}

	return constraints
}
