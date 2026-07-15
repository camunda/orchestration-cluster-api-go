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
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
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
	field   string // APIClient field, e.g. "TopologyAPI"
	name    string // operation, e.g. "GetTopology"
	params  []param
	retType string // qualified value return type, or "" when the op returns no value
}

func main() {
	clientDir := "client"
	outPath := "facade_generated.go"
	if len(os.Args) > 1 {
		clientDir = os.Args[1]
	}
	if len(os.Args) > 2 {
		outPath = os.Args[2]
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, clientDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		fatalf("parsing %s: %v", clientDir, err)
	}

	var files []*ast.File
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		fatalf("no Go files found in %s", clientDir)
	}

	r := &renderer{fset: fset, clientTypes: collectTypeNames(files)}
	fieldByService := collectAPIClientFields(files) // serviceType -> field name
	constructors, executes := collectServiceMethods(files)

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
		ops = append(ops, operation{
			field:   field,
			name:    ctor.op,
			params:  r.params(ctor.decl),
			retType: r.valueReturn(exec.decl),
		})
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].field != ops[j].field {
			return ops[i].field < ops[j].field
		}
		return ops[i].name < ops[j].name
	})

	src := emit(ops)
	if err := os.MkdirAll(filepath.Dir(absOrDot(outPath)), 0o755); err != nil && filepath.Dir(outPath) != "." {
		fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(outPath, []byte(src), 0o644); err != nil {
		fatalf("writing %s: %v", outPath, err)
	}
	fmt.Printf("facadegen: emitted %d operations into %s\n", len(ops), outPath)
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

func emit(ops []operation) string {
	var b strings.Builder
	b.WriteString("// Code generated by cmd/facadegen. DO NOT EDIT.\n\n")
	b.WriteString("package camunda\n\n")
	b.WriteString("import (\n\t\"context\"\n\n\t" + clientAlias + " \"" + clientImportPath + "\"\n)\n\n")
	b.WriteString("var _ = context.Background\n\n")

	for _, op := range ops {
		var sig strings.Builder
		sig.WriteString("ctx context.Context")
		var call strings.Builder
		call.WriteString("ctx")
		for _, p := range op.params {
			sig.WriteString(", " + p.name + " " + p.typ)
			call.WriteString(", " + p.name)
		}

		fmt.Fprintf(&b, "// %s calls the %s operation.\n", op.name, op.name)
		if op.retType != "" {
			fmt.Fprintf(&b, "func (c *CamundaClient) %s(%s) (%s, error) {\n", op.name, sig.String(), op.retType)
			fmt.Fprintf(&b, "\tvalue, _, err := c.raw.%s.%s(%s).Execute()\n", op.field, op.name, call.String())
			b.WriteString("\treturn value, c.wrapError(err)\n}\n\n")
		} else {
			fmt.Fprintf(&b, "func (c *CamundaClient) %s(%s) error {\n", op.name, sig.String())
			fmt.Fprintf(&b, "\t_, err := c.raw.%s.%s(%s).Execute()\n", op.field, op.name, call.String())
			b.WriteString("\treturn c.wrapError(err)\n}\n\n")
		}
	}
	return b.String()
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
