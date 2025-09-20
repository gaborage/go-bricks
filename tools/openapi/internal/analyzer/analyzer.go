package analyzer

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gaborage/go-bricks/tools/openapi/internal/models"
)

const (
	serverImportPath = "github.com/gaborage/go-bricks/server"
	appImportPath    = "github.com/gaborage/go-bricks/app"
)

// ProjectAnalyzer analyzes Go-Bricks projects to extract module and route information
type ProjectAnalyzer struct {
	projectRoot string
	fileSet     *token.FileSet
	constants   map[string]string // Map of constant names to their values
}

// New creates a new project analyzer
func New(projectRoot string) *ProjectAnalyzer {
	return &ProjectAnalyzer{
		projectRoot: projectRoot,
		fileSet:     token.NewFileSet(),
		constants:   make(map[string]string),
	}
}

// AnalyzeProject discovers modules and routes from a go-bricks project
func (a *ProjectAnalyzer) AnalyzeProject() (*models.Project, error) {
	project := &models.Project{
		Name:        "Go-Bricks API",
		Version:     "1.0.0",
		Description: "Generated API specification",
		Modules:     []models.Module{},
	}

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

// discoverProjectMetadata extracts project information from go.mod and main files
func (a *ProjectAnalyzer) discoverProjectMetadata(project *models.Project) {
	// Try to read go.mod for module name
	goModPath := filepath.Join(a.projectRoot, "go.mod")
	if content, err := os.ReadFile(goModPath); err == nil {
		for line := range strings.SplitSeq(string(content), "\n") {
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
}

// discoverModules finds all go-bricks modules in the project
func (a *ProjectAnalyzer) discoverModules() ([]models.Module, error) {
	var modules []models.Module

	err := filepath.Walk(a.projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors to continue discovery
		}

		// Skip vendor, .git, and other common directories
		if info.IsDir() && (info.Name() == "vendor" || info.Name() == ".git" ||
			strings.HasPrefix(info.Name(), ".") || info.Name() == "node_modules") {
			return filepath.SkipDir
		}

		// Only process Go files
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		// Parse the Go file
		module, err := a.analyzeGoFile(path)
		if err != nil {
			// Log error but continue processing other files
			return nil
		}

		if module != nil {
			modules = append(modules, *module)
		}

		return nil
	})

	return modules, err
}

// analyzeGoFile parses a Go file and extracts module information
func (a *ProjectAnalyzer) analyzeGoFile(filePath string) (*models.Module, error) {
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

	return module, nil
}

// extractModuleFromAST checks if the AST contains a go-bricks module
func (a *ProjectAnalyzer) extractModuleFromAST(astFile *ast.File, filePath string) (module *models.Module, structName string) {
	var (
		hasModuleStruct bool
	)
	packageName := astFile.Name.Name

	// Look for struct types that might implement the Module interface
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

			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			// Check if this struct has methods that indicate it's a Module
			if a.hasModuleMethods(astFile, typeSpec.Name.Name, filePath) {
				hasModuleStruct = true
				structName = typeSpec.Name.Name
				break
			}

			// Additional check - look for deps field of type *app.ModuleDeps
			if slices.ContainsFunc(structType.Fields.List, a.isModuleDepsField) {
				hasModuleStruct = true
			}

			if hasModuleStruct {
				break
			}
		}

		if hasModuleStruct {
			break
		}
	}

	if !hasModuleStruct {
		return nil, ""
	}

	// Use package name as module name and extract package-level description
	moduleDescription := a.extractPackageDescription(astFile)

	module = &models.Module{
		Name:        packageName,
		Package:     packageName,
		Description: moduleDescription,
		Routes:      []models.Route{},
	}
	return module, structName
}

// hasModuleMethods checks if the struct has methods indicating it's a go-bricks module
func (a *ProjectAnalyzer) hasModuleMethods(astFile *ast.File, structName, filePath string) bool {
	requiredMethods := map[string]bool{
		"Name":           false,
		"Init":           false,
		"RegisterRoutes": false,
		"Shutdown":       false,
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
	return requiredMethods["Name"] && requiredMethods["Init"] && requiredMethods["RegisterRoutes"]
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

	return pkgIdent.Name == "app" && selExpr.Sel.Name == "ModuleDeps"
}

// extractRoutesFromPackage extracts route registrations for the module across the entire package
func (a *ProjectAnalyzer) extractRoutesFromPackage(astFile *ast.File, filePath, structName string) []models.Route {
	files, err := a.parsePackage(filePath, astFile.Name.Name)
	if err != nil || files == nil {
		return a.collectRoutesFromFile(astFile, structName, map[string]struct{}{"server": {}})
	}

	var routes []models.Route

	// First collect constants so paths can be resolved regardless of declaration order
	for _, file := range files {
		a.extractConstants(file)
	}

	for _, file := range files {
		aliases := a.extractImportAliases(file, serverImportPath)
		if len(aliases) == 0 {
			continue
		}
		routes = append(routes, a.collectRoutesFromFile(file, structName, aliases)...)
	}

	return routes
}

// collectRoutesFromFile gathers routes for a specific module struct from a single file
func (a *ProjectAnalyzer) collectRoutesFromFile(astFile *ast.File, structName string, serverAliases map[string]struct{}) []models.Route {
	var routes []models.Route

	for _, decl := range astFile.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Name.Name != "RegisterRoutes" || funcDecl.Body == nil {
			continue
		}

		if !a.isMethodOnStruct(funcDecl.Recv, structName) {
			continue
		}

		if !a.isValidRegisterRoutesSignature(funcDecl, serverAliases) {
			continue
		}

		routes = append(routes, a.extractRoutesFromFuncBodyWithAliases(funcDecl.Body, serverAliases)...)
	}

	return routes
}

// extractRoutesFromFuncBody extracts route registrations from function statements
func (a *ProjectAnalyzer) extractRoutesFromFuncBody(body *ast.BlockStmt) []models.Route {
	return a.extractRoutesFromFuncBodyWithAliases(body, map[string]struct{}{"server": {}})
}

// extractRoutesFromFuncBodyWithAliases extracts route registrations with explicit server aliases
func (a *ProjectAnalyzer) extractRoutesFromFuncBodyWithAliases(body *ast.BlockStmt, serverAliases map[string]struct{}) []models.Route {
	var routes []models.Route

	for _, stmt := range body.List {
		if route := a.extractRouteFromStatement(stmt, serverAliases); route != nil {
			routes = append(routes, *route)
		}
	}

	return routes
}

// extractRouteFromStatement extracts a route from a statement like server.GET(...)
func (a *ProjectAnalyzer) extractRouteFromStatement(stmt ast.Stmt, serverAliases map[string]struct{}) *models.Route {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return nil
	}

	callExpr, ok := exprStmt.X.(*ast.CallExpr)
	if !ok {
		return nil
	}

	// Check if this is a server method call (GET, POST, etc.)
	selExpr, ok := callExpr.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	pkgIdent, ok := selExpr.X.(*ast.Ident)
	if !ok || !a.aliasContains(serverAliases, pkgIdent.Name, "server") {
		return nil
	}

	method := selExpr.Sel.Name
	if !a.isHTTPMethod(method) {
		return nil
	}

	// Extract route path and handler from arguments
	if len(callExpr.Args) < 3 {
		return nil // Need at least hr, r, path, handler
	}

	route := &models.Route{
		Method: strings.ToUpper(method),
		Tags:   []string{},
	}

	// Extract path from the third argument (handle both literals and constants)
	route.Path = a.extractPathFromArg(callExpr.Args[2])

	// Extract handler name from the fourth argument
	if len(callExpr.Args) > 3 {
		if selExpr, ok := callExpr.Args[3].(*ast.SelectorExpr); ok {
			route.HandlerName = selExpr.Sel.Name
		}
	}

	// Extract metadata from remaining arguments (server.WithTags, server.WithSummary, etc.)
	for i := 4; i < len(callExpr.Args); i++ {
		a.extractRouteMetadata(callExpr.Args[i], route, serverAliases)
	}

	return route
}

// isHTTPMethod checks if the method name is a valid HTTP method
func (a *ProjectAnalyzer) isHTTPMethod(method string) bool {
	httpMethods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}
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
	if !ok || !a.aliasContains(serverAliases, pkg.Name, "server") {
		return
	}

	methodName := selExpr.Sel.Name
	switch methodName {
	case "WithTags":
		route.Tags = a.extractStringLiterals(callExpr.Args)
	case "WithSummary":
		if len(callExpr.Args) > 0 {
			if lit, ok := callExpr.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				route.Summary = strings.Trim(lit.Value, `"`)
			}
		}
	case "WithDescription":
		if len(callExpr.Args) > 0 {
			if lit, ok := callExpr.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				route.Description = strings.Trim(lit.Value, `"`)
			}
		}
	}
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

// extractPathFromArg extracts path string from AST argument (handles literals and constants)
func (a *ProjectAnalyzer) extractPathFromArg(arg ast.Expr) string {
	switch expr := arg.(type) {
	case *ast.BasicLit:
		// Direct string literal
		if expr.Kind == token.STRING {
			return strings.Trim(expr.Value, `"`)
		}
	case *ast.Ident:
		// Constant reference - look up in constants map
		if value, exists := a.constants[expr.Name]; exists {
			return value
		}
		// If not found, return the identifier name as a fallback
		return expr.Name
	}
	return ""
}

// extractConstants finds constant declarations in the AST file
func (a *ProjectAnalyzer) extractConstants(astFile *ast.File) {
	for _, decl := range astFile.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}

		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
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
func (a *ProjectAnalyzer) parsePackage(filePath, packageName string) (files map[string]*ast.File, err error) {
	dir := filepath.Dir(filePath)
	pkgs, err := parser.ParseDir(a.fileSet, dir, func(info fs.FileInfo) bool {
		if info.IsDir() {
			return false
		}
		name := info.Name()
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, parser.ParseComments)
	if len(pkgs) == 0 {
		return nil, err
	}

	if pkg, ok := pkgs[packageName]; ok {
		return pkg.Files, nil
	}

	for _, pkg := range pkgs {
		return pkg.Files, nil
	}

	return nil, fmt.Errorf("package %s not found in %s", packageName, dir)
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

		switch funcDecl.Name.Name {
		case "Name":
			if a.isValidNameSignature(funcDecl) {
				flags["Name"] = true
			}
		case "Init":
			if a.isValidInitSignature(funcDecl, appAliases) {
				flags["Init"] = true
			}
		case "RegisterRoutes":
			if a.isValidRegisterRoutesSignature(funcDecl, serverAliases) {
				flags["RegisterRoutes"] = true
			}
		case "Shutdown":
			if a.isValidShutdownSignature(funcDecl) {
				flags["Shutdown"] = true
			}
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
		return ident.Name == "string"
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
	if !ok || !a.aliasContains(appAliases, pkgIdent.Name, "app") {
		return false
	}

	if selExpr.Sel.Name != "ModuleDeps" {
		return false
	}

	if funcDecl.Type.Results == nil || len(funcDecl.Type.Results.List) != 1 {
		return false
	}

	if ident, ok := funcDecl.Type.Results.List[0].Type.(*ast.Ident); ok {
		return ident.Name == "error"
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
	if !ok || !a.aliasContains(serverAliases, firstPkg.Name, "server") {
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
	if !ok || !a.aliasContains(serverAliases, secondPkg.Name, "server") {
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
		return ident.Name == "error"
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
