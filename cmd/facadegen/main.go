// Command facadegen generates facade_generated.go by AST-parsing the generated
// REST client package (client/). For every operation it emits an ergonomic method
// on *CamundaClient that delegates to the raw client, exposing required parameters
// and returning (value, error) — hiding the *http.Response and the fluent
// request-builder boilerplate.
//
// The openapi-generator Go shape this parser targets:
//
//	type <Svc>APIService struct { ... }
//	func (a *<Svc>APIService) <Op>(ctx context.Context, <required...>) Api<Op>Request
//	func (a *<Svc>APIService) <Op>Execute(r Api<Op>Request) (<T>, *http.Response, error)
//	type APIClient struct { <Field> *<Svc>APIService ... }
//
// The generated facade is package `camunda` and references the raw client through
// the alias `openapi`, the client field `c.raw`, and the error mapper `c.wrapError`,
// which are provided by the hand-written client wiring.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	clientImportPath = "github.com/camunda/orchestration-cluster-api-go/client"
	clientAlias      = "openapi"
)

type param struct {
	name string
	typ  string
}

type operation struct {
	field       string // APIClient field, e.g. "TopologyAPI"
	name        string // operation, e.g. "GetTopology"
	params      []param
	retType     string // qualified value return type, or "" when the op returns no value
	reqType     string // qualified request-builder type, e.g. "openapi.ApiGetTopologyRequest"
	bodyType    string // qualified request-body type (e.g. "openapi.JobActivationRequest"), or ""
	bodyBuilder string // request-body builder method on the ApiXxxRequest, or ""
	example     string // dedented usage snippet from examples/, injected into the doc comment
}

// bodyInfo describes an operation's JSON request body, derived from spec metadata.
type bodyInfo struct {
	builder string // builder method name on ApiXxxRequest (== the body model name)
	typ     string // qualified Go type, e.g. "openapi.JobActivationRequest"
}

func main() {
	clientDir := "client"
	outPath := "facade_generated.go"
	metadataPath := ""
	examplesDir := ""
	if len(os.Args) > 1 {
		clientDir = os.Args[1]
	}
	if len(os.Args) > 2 {
		outPath = os.Args[2]
	}
	if len(os.Args) > 3 {
		metadataPath = os.Args[3]
	}
	if len(os.Args) > 4 {
		examplesDir = os.Args[4]
	}

	src, count, err := generateFacade(clientDir, metadataPath, examplesDir)
	if err != nil {
		fatalf("%v", err)
	}
	if err := os.MkdirAll(filepath.Dir(absOrDot(outPath)), 0o755); err != nil && filepath.Dir(outPath) != "." {
		fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(outPath, []byte(src), 0o644); err != nil {
		fatalf("writing %s: %v", outPath, err)
	}
	fmt.Printf("facadegen: emitted %d operations into %s\n", count, outPath)
}

// generateFacade AST-parses the client package in clientDir and returns the
// emitted facade source and the number of operations it covers. When
// metadataPath is non-empty, JSON request bodies are surfaced as typed method
// parameters (derived from the spec metadata).
func generateFacade(clientDir, metadataPath, examplesDir string) (string, int, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, clientDir, func(fi os.FileInfo) bool { //nolint:staticcheck // ParseDir is adequate for the single generated client package
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return "", 0, fmt.Errorf("parsing %s: %w", clientDir, err)
	}

	var files []*ast.File
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return "", 0, fmt.Errorf("no Go files found in %s", clientDir)
	}

	r := &renderer{fset: fset, clientTypes: collectTypeNames(files), usedPkgs: map[string]bool{}}
	fieldByService := collectAPIClientFields(files) // serviceType -> field name
	constructors, executes := collectServiceMethods(files)
	bodyByOp := loadBodyInfo(metadataPath, r.clientTypes)
	exampleByOp := loadExamples(examplesDir)

	var ops []operation
	for key, ctor := range constructors {
		exec, ok := executes[key]
		if !ok {
			continue
		}
		field, ok := fieldByService[ctor.serviceType]
		if !ok {
			continue
		}
		op := operation{
			field:   field,
			name:    ctor.op,
			params:  r.params(ctor.decl),
			retType: r.valueReturn(exec.decl),
			reqType: r.resultType(ctor.decl),
		}
		if bi, ok := bodyByOp[op.name]; ok {
			op.bodyType = bi.typ
			op.bodyBuilder = bi.builder
		}
		if ex, ok := exampleByOp[lowerFirst(op.name)]; ok {
			op.example = ex
		}
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].field != ops[j].field {
			return ops[i].field < ops[j].field
		}
		return ops[i].name < ops[j].name
	})

	var extraStd []string
	for pkg := range r.usedPkgs {
		if pkg == "context" {
			continue // always imported
		}
		extraStd = append(extraStd, pkg)
	}
	sort.Strings(extraStd)
	return emit(ops, extraStd), len(ops), nil
}

// lowerFirst returns s with its first rune lowercased (PascalCase method name ->
// camelCase operationId, matching operation-map.json keys).
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

var (
	_exOpenRe  = regexp.MustCompile(`^\s*// region ([\w.-]+)\s*$`)
	_exCloseRe = regexp.MustCompile(`^\s*// endregion ([\w.-]+)\s*$`)
)

// loadExamples reads examples/operation-map.json and returns, per operationId,
// the dedented code of the region its first entry points at. Returns nil when
// examplesDir is empty or the map is absent, so generation still works without
// examples (e.g. the facadegen unit test).
func loadExamples(examplesDir string) map[string]string {
	if examplesDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(examplesDir, "operation-map.json"))
	if err != nil {
		return nil
	}
	type entry struct {
		File   string `json:"file"`
		Region string `json:"region"`
	}
	var opMap map[string][]entry
	if err := json.Unmarshal(data, &opMap); err != nil {
		return nil
	}
	regionCache := map[string]map[string]string{}
	regionsFor := func(file string) map[string]string {
		if r, ok := regionCache[file]; ok {
			return r
		}
		r := parseExampleRegions(filepath.Join(examplesDir, file))
		regionCache[file] = r
		return r
	}
	out := map[string]string{}
	for opID, entries := range opMap {
		if len(entries) == 0 {
			continue
		}
		e := entries[0]
		if code, ok := regionsFor(e.File)[e.Region]; ok && code != "" {
			out[opID] = code
		}
	}
	return out
}

// parseExampleRegions extracts `// region X` ... `// endregion X` blocks from a
// Go example file, returning region name -> dedented code.
func parseExampleRegions(path string) map[string]string {
	regions := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return regions
	}
	var cur string
	var buf []string
	for _, ln := range strings.Split(string(data), "\n") {
		if m := _exOpenRe.FindStringSubmatch(ln); m != nil {
			cur, buf = m[1], nil
			continue
		}
		if m := _exCloseRe.FindStringSubmatch(ln); m != nil && m[1] == cur {
			regions[cur] = dedent(buf)
			cur = ""
			continue
		}
		if cur != "" {
			buf = append(buf, ln)
		}
	}
	return regions
}

// dedent removes the smallest common leading-tab indentation from non-empty
// lines and trims surrounding blank lines.
func dedent(lines []string) string {
	minTabs := -1
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		n := len(ln) - len(strings.TrimLeft(ln, "\t"))
		if minTabs == -1 || n < minTabs {
			minTabs = n
		}
	}
	if minTabs < 0 {
		minTabs = 0
	}
	out := make([]string, len(lines))
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			out[i] = ""
		} else {
			out[i] = ln[minTabs:]
		}
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

type methodKey = string

type ctorInfo struct {
	serviceType string
	op          string
	decl        *ast.FuncDecl
}

type execInfo struct {
	decl *ast.FuncDecl
}

// collectServiceMethods pairs each <Op> constructor with its <Op>Execute method,
// keyed by "<serviceType>.<op>".
func collectServiceMethods(files []*ast.File) (map[methodKey]ctorInfo, map[methodKey]execInfo) {
	ctors := map[methodKey]ctorInfo{}
	execs := map[methodKey]execInfo{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recv := recvTypeName(fn.Recv.List[0].Type)
			if !strings.HasSuffix(recv, "APIService") {
				continue
			}
			name := fn.Name.Name
			if strings.HasSuffix(name, "Execute") {
				op := strings.TrimSuffix(name, "Execute")
				execs[recv+"."+op] = execInfo{decl: fn}
			} else {
				ctors[recv+"."+name] = ctorInfo{serviceType: recv, op: name, decl: fn}
			}
		}
	}
	return ctors, execs
}

// collectAPIClientFields maps each service type to its field name on APIClient.
func collectAPIClientFields(files []*ast.File) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != "APIClient" {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, fld := range st.Fields.List {
					star, ok := fld.Type.(*ast.StarExpr)
					if !ok || len(fld.Names) != 1 {
						continue
					}
					if id, ok := star.X.(*ast.Ident); ok && strings.HasSuffix(id.Name, "APIService") {
						out[id.Name] = fld.Names[0].Name
					}
				}
			}
		}
	}
	return out
}

func collectTypeNames(files []*ast.File) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					out[ts.Name.Name] = true
				}
			}
		}
	}
	return out
}

func recvTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		if id, ok := star.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

type renderer struct {
	fset        *token.FileSet
	clientTypes map[string]bool
	// usedPkgs records non-client selector packages referenced in signatures
	// (e.g. "os" from *os.File), so emit can import them.
	usedPkgs map[string]bool
}

// params returns the constructor parameters excluding the leading context.Context.
func (r *renderer) params(fn *ast.FuncDecl) []param {
	var out []param
	if fn.Type.Params == nil {
		return out
	}
	for _, field := range fn.Type.Params.List {
		typ := r.typeString(field.Type)
		if typ == "context.Context" {
			continue
		}
		for _, n := range field.Names {
			out = append(out, param{name: n.Name, typ: typ})
		}
	}
	return out
}

// valueReturn returns the qualified value return type of an Execute method, or ""
// when it returns only (*http.Response, error).
func (r *renderer) valueReturn(fn *ast.FuncDecl) string {
	if fn.Type.Results == nil {
		return ""
	}
	results := fn.Type.Results.List
	// Signatures are either (T, *http.Response, error) or (*http.Response, error).
	if len(results) < 3 {
		return ""
	}
	return r.typeString(results[0].Type)
}

// resultType returns the qualified type of a constructor's single result — the
// ApiXxxRequest builder type used for the opts transform.
func (r *renderer) resultType(fn *ast.FuncDecl) string {
	if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return ""
	}
	return r.typeString(fn.Type.Results.List[0].Type)
}

// typeString renders a type expression, qualifying client-package types with the
// `openapi.` alias.
func (r *renderer) typeString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		if r.clientTypes[e.Name] && ast.IsExported(e.Name) {
			return clientAlias + "." + e.Name
		}
		return e.Name
	case *ast.StarExpr:
		return "*" + r.typeString(e.X)
	case *ast.ArrayType:
		if e.Len != nil {
			return "[" + r.exprString(e.Len) + "]" + r.typeString(e.Elt)
		}
		return "[]" + r.typeString(e.Elt)
	case *ast.MapType:
		return "map[" + r.typeString(e.Key) + "]" + r.typeString(e.Value)
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			r.usedPkgs[id.Name] = true
		}
		return r.exprString(e) // e.g. context.Context, os.File — pass through
	case *ast.Ellipsis:
		return "..." + r.typeString(e.Elt)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return r.exprString(expr)
	}
}

func (r *renderer) exprString(expr ast.Expr) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, r.fset, expr)
	return buf.String()
}

func emit(ops []operation, extraStd []string) string {
	var b strings.Builder
	b.WriteString("// Code generated by cmd/facadegen. DO NOT EDIT.\n\n")
	b.WriteString("package camunda\n\n")
	b.WriteString("import (\n\t\"context\"\n")
	for _, p := range extraStd {
		b.WriteString("\t\"" + p + "\"\n")
	}
	b.WriteString("\n\t" + clientAlias + " \"" + clientImportPath + "\"\n)\n\n")
	b.WriteString("var _ = context.Background\n\n")

	for _, op := range ops {
		sig := "ctx context.Context"
		call := "ctx"
		for _, p := range op.params {
			sig += ", " + p.name + " " + p.typ
			call += ", " + p.name
		}
		if op.bodyBuilder != "" {
			sig += ", body " + op.bodyType
		}
		// opts gives type-safe access to the operation's optional request-builder
		// setters (query params, headers, optional body fields) without dropping to
		// Raw(): each transform receives and returns the fluent request value.
		hasOpts := op.reqType != ""
		if hasOpts {
			sig += fmt.Sprintf(", opts ...func(%s) %s", op.reqType, op.reqType)
		}

		fmt.Fprintf(&b, "// %s calls the %s operation.\n", op.name, op.name)
		if op.example != "" {
			b.WriteString("//\n// Example:\n//\n")
			for _, line := range strings.Split(op.example, "\n") {
				if line == "" {
					b.WriteString("//\n")
				} else {
					b.WriteString("//\t" + line + "\n")
				}
			}
		}
		retSig := "error"
		if op.retType != "" {
			retSig = fmt.Sprintf("(%s, error)", op.retType)
		}
		fmt.Fprintf(&b, "func (c *CamundaClient) %s(%s) %s {\n", op.name, sig, retSig)
		fmt.Fprintf(&b, "\treq := c.raw.%s.%s(%s)\n", op.field, op.name, call)
		if op.bodyBuilder != "" {
			fmt.Fprintf(&b, "\treq = req.%s(body)\n", op.bodyBuilder)
		}
		if hasOpts {
			b.WriteString("\tfor _, opt := range opts {\n\t\treq = opt(req)\n\t}\n")
		}
		if op.retType != "" {
			b.WriteString("\tvalue, resp, err := req.Execute()\n")
			b.WriteString("\treturn value, c.wrapError(resp, err)\n}\n\n")
		} else {
			b.WriteString("\tresp, err := req.Execute()\n")
			b.WriteString("\treturn c.wrapError(resp, err)\n}\n\n")
		}
	}
	return b.String()
}

// loadBodyInfo reads the spec metadata and returns, keyed by generated method
// name (PascalCase operationId), the JSON request body to surface on the facade.
// Multipart bodies and bodies whose model is not a client type are skipped.
func loadBodyInfo(metadataPath string, clientTypes map[string]bool) map[string]bodyInfo {
	if metadataPath == "" {
		return nil
	}
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil
	}
	var meta struct {
		Operations []struct {
			OperationID             string   `json:"operationId"`
			HasRequestBody          bool     `json:"hasRequestBody"`
			RequestBodySchemaRef    string   `json:"requestBodySchemaRef"`
			RequestBodyContentTypes []string `json:"requestBodyContentTypes"`
		} `json:"operations"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return nil
	}
	out := map[string]bodyInfo{}
	for _, o := range meta.Operations {
		if !o.HasRequestBody || o.RequestBodySchemaRef == "" || !hasJSONContent(o.RequestBodyContentTypes) {
			continue
		}
		model := o.RequestBodySchemaRef
		if !clientTypes[model] {
			continue // e.g. multipart deploy, or a primitive/inline body
		}
		out[upperFirst(o.OperationID)] = bodyInfo{builder: model, typ: clientAlias + "." + model}
	}
	return out
}

func hasJSONContent(cts []string) bool {
	for _, ct := range cts {
		if strings.Contains(ct, "application/json") {
			return true
		}
	}
	return false
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func absOrDot(p string) string {
	if filepath.Dir(p) == "" {
		return "."
	}
	return p
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "facadegen: "+format+"\n", args...)
	os.Exit(1)
}
