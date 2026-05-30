package analyzer

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	projectRoot string
	fileSet     *token.FileSet
	constants   map[string]string // Map of constant names to their values
	warnings    []string          // Non-fatal diagnostics collected during analysis
}

// New creates a new project analyzer
func New(projectRoot string) *ProjectAnalyzer {
	return &ProjectAnalyzer{
		projectRoot: projectRoot,
		fileSet:     token.NewFileSet(),
		constants:   make(map[string]string),
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

	// Reset per-run diagnostics so repeated analyses on the same analyzer don't
	// accumulate stale warnings.
	a.warnings = nil

	// Discover project metadata from go.mod
	a.discoverProjectMetadata(project)

	// Discover modules by walking the project directory
	modules, err := a.discoverModules()
	if err != nil {
		return nil, fmt.Errorf("failed to discover modules: %w", err)
	}

	project.Modules = modules
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
		if strings.HasPrefix(line, "module ") {
			moduleName := strings.TrimSpace(strings.TrimPrefix(line, "module"))
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
	route.HandlerName, route.Request, route.Response = w.a.extractHandlerInfo(call, w.astFile, w.filePath, w.structName)
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
) (handlerName string, reqType, respType *models.TypeInfo) {
	if len(callExpr.Args) <= 3 {
		return "", nil, nil
	}

	handlerName, receiverType, isPackageFunc, ok := a.resolveHandler(callExpr.Args[3], structName, astFile, filePath)
	if !ok {
		return "", nil, nil
	}

	// Extract handler signature if we have required context.
	if astFile != nil && filePath != "" {
		req, resp, err := a.extractHandlerSignature(astFile, filePath, receiverType, isPackageFunc, handlerName)
		if err != nil {
			// Don't fail — some routes use inline or external handlers.
			return handlerName, nil, nil
		}
		return handlerName, req, resp
	}

	return handlerName, nil, nil
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
) (requestType, responseType *models.TypeInfo) {
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

		return requestType, responseType
	}

	return nil, nil
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
) (reqType, respType *models.TypeInfo, err error) {
	// Try current file first
	if reqType, respType := a.findHandlerInFile(astFile, receiverType, isPackageFunc, handlerName); reqType != nil || respType != nil {
		a.populateTypeFields(reqType, astFile, filePath)
		a.populateTypeFields(respType, astFile, filePath)
		return reqType, respType, nil
	}

	// Try other files in the package
	files, err := a.parsePackage(filePath, astFile.Name.Name)
	if err == nil && files != nil {
		for _, file := range files {
			if reqType, respType := a.findHandlerInFile(file, receiverType, isPackageFunc, handlerName); reqType != nil || respType != nil {
				a.populateTypeFields(reqType, file, filePath)
				a.populateTypeFields(respType, file, filePath)
				return reqType, respType, nil
			}
		}
	}

	// Handler not found - this is not necessarily an error
	// Some routes might use inline handlers or external handlers
	return nil, nil, fmt.Errorf("handler %s not found for receiver %q", handlerName, receiverType)
}

// populateTypeFields populates the Fields slice for a TypeInfo by finding its struct definition
func (a *ProjectAnalyzer) populateTypeFields(typeInfo *models.TypeInfo, astFile *ast.File, filePath string) {
	if typeInfo == nil {
		return
	}

	// Find struct definition
	structType, err := a.findStructDefinition(astFile, filePath, typeInfo.Name)
	if err != nil {
		// Struct not found - might be a primitive type or external type
		return
	}

	// Extract fields from struct
	typeInfo.Fields = a.extractStructFields(structType, typeInfo.Package)
	// Scan for the JOSE sentinel field — the standard `_ struct{} `jose:"..."` ` pattern
	// is filtered out of Fields by the unexported-name check, so JOSE detection runs as
	// a separate pre-pass over raw struct fields.
	typeInfo.JOSE = hasJOSESentinelTag(structType)
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

// extractStructFields extracts field information from a struct type including struct tags
func (a *ProjectAnalyzer) extractStructFields(structType *ast.StructType, _ string) []models.FieldInfo {
	var fields []models.FieldInfo

	if structType.Fields == nil {
		return fields
	}

	for _, field := range structType.Fields.List {
		fields = append(fields, a.processStructField(field)...)
	}

	return fields
}

// processStructField processes a single AST field and returns all associated FieldInfo entries
func (a *ProjectAnalyzer) processStructField(field *ast.Field) []models.FieldInfo {
	// Skip fields without names (embedded structs, for now)
	if len(field.Names) == 0 {
		return nil
	}

	fieldInfos := make([]models.FieldInfo, 0, len(field.Names))
	for _, fieldName := range field.Names {
		// Skip unexported fields
		if !fieldName.IsExported() {
			continue
		}

		fieldInfo := a.buildFieldInfo(fieldName.Name, field)
		fieldInfos = append(fieldInfos, fieldInfo)
	}

	return fieldInfos
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
