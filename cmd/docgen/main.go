// Command docgen emits a JSON description of a Go package's exported API.
//
// It is the Go analogue of `cargo rustdoc --output-format json` in the Rust SDK:
// a machine-readable snapshot of the public surface that
// scripts/generate-docusaurus-md.py renders into the Docusaurus pages published
// at https://docs.camunda.io.
//
// Unlike rustdoc JSON, the schema here is ours, so it is stable by construction
// and carries only what the renderer needs: doc comments, rendered signatures,
// struct fields, and the constant groups that back the SDK's enum-like types.
//
// Usage:
//
//	docgen -dir . -import-path github.com/camunda/orchestration-cluster-api-go \
//	       -out docs-json/camunda.json
//	docgen -dir ./client -include zz_generated_domain_keys.go \
//	       -import-path github.com/camunda/orchestration-cluster-api-go/client \
//	       -out docs-json/domain-keys.json
//
// The -include flag restricts parsing to a single file, which keeps the
// generated client's 700-odd model files out of the domain-key snapshot.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxDeclChars caps the rendered form of a const or var declaration. A handful
// of package-level values (ConfigSchema, for one) are hundreds of lines of
// literal; the reference wants their documentation, not their body.
const maxDeclChars = 2000

// Package is the root of the emitted document.
type Package struct {
	Name       string  `json:"name"`
	ImportPath string  `json:"importPath"`
	Doc        string  `json:"doc"`
	Consts     []Value `json:"consts"`
	Vars       []Value `json:"vars"`
	Funcs      []Func  `json:"funcs"`
	Types      []Type  `json:"types"`
}

// Value is a const or var declaration group. A single group may bind several
// names — the sentinel error block, or an iota-based enum.
type Value struct {
	Names []string `json:"names"`
	Doc   string   `json:"doc"`
	Decl  string   `json:"decl"`
}

// Func is a package-level function, a constructor, or a method.
type Func struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
	Doc       string `json:"doc"`
	Recv      string `json:"recv,omitempty"`
}

// Field is an exported struct field.
type Field struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Doc  string `json:"doc"`
}

// Type is an exported named type together with everything go/doc associates
// with it: its constructors, its methods, and any const or var group whose
// type it is.
type Type struct {
	Name string `json:"name"`
	// Kind is one of struct, interface, func, alias, or basic. The renderer
	// uses it to decide between a field table and a rendered declaration.
	Kind    string  `json:"kind"`
	Doc     string  `json:"doc"`
	Decl    string  `json:"decl,omitempty"`
	Fields  []Field `json:"fields"`
	Consts  []Value `json:"consts"`
	Vars    []Value `json:"vars"`
	Funcs   []Func  `json:"funcs"`
	Methods []Func  `json:"methods"`
}

func main() {
	var (
		dir        = flag.String("dir", ".", "package directory to document")
		importPath = flag.String("import-path", "", "import path of the package (required)")
		include    = flag.String("include", "", "if set, parse only this file within -dir")
		out        = flag.String("out", "", "output JSON path (required)")
	)
	flag.Parse()

	if *importPath == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "docgen: -import-path and -out are required")
		flag.Usage()
		os.Exit(2)
	}

	pkg, err := document(*dir, *importPath, *include)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docgen: %v\n", err)
		os.Exit(1)
	}

	body, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "docgen: marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "docgen: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(body, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "docgen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Wrote %s (%d types, %d funcs)\n", *out, len(pkg.Types), len(pkg.Funcs))
}

func document(dir, importPath, include string) (*Package, error) {
	fset := token.NewFileSet()
	files, err := parseDir(fset, dir, include)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Go source files found in %s", dir)
	}

	// doc.NewFromFiles with the default mode keeps only exported declarations,
	// which is exactly the surface the published reference documents.
	dpkg, err := doc.NewFromFiles(fset, files, importPath)
	if err != nil {
		return nil, fmt.Errorf("build package documentation: %w", err)
	}

	r := &renderer{fset: fset}
	pkg := &Package{
		Name:       dpkg.Name,
		ImportPath: importPath,
		Doc:        strings.TrimSpace(dpkg.Doc),
		Consts:     r.values(dpkg.Consts),
		Vars:       r.values(dpkg.Vars),
		Funcs:      r.funcs(dpkg.Funcs),
		Types:      make([]Type, 0, len(dpkg.Types)),
	}
	for _, dt := range dpkg.Types {
		pkg.Types = append(pkg.Types, r.typeItem(dt))
	}
	sort.Slice(pkg.Types, func(i, j int) bool { return pkg.Types[i].Name < pkg.Types[j].Name })
	return pkg, nil
}

// parseDir reads dir and parses its non-test Go files. It deliberately avoids
// go/parser.ParseDir, which is deprecated and build-constraint unaware.
func parseDir(fset *token.FileSet, dir, include string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if include != "" && name != include {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		files = append(files, f)
	}
	if include != "" && len(files) == 0 {
		return nil, fmt.Errorf("-include %q matched no file in %s", include, dir)
	}
	return files, nil
}

type renderer struct {
	fset *token.FileSet
	buf  bytes.Buffer
}

// print renders an AST node back to Go source. The printer reproduces the
// original line breaks, so gofmt'd single-line signatures stay single-line.
func (r *renderer) print(node ast.Node) string {
	r.buf.Reset()
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 4}
	if err := cfg.Fprint(&r.buf, r.fset, node); err != nil {
		return ""
	}
	return strings.TrimSpace(r.buf.String())
}

func (r *renderer) values(vals []*doc.Value) []Value {
	out := make([]Value, 0, len(vals))
	for _, v := range vals {
		decl := r.print(stripDoc(v.Decl))
		if len(decl) > maxDeclChars {
			decl = ""
		}
		out = append(out, Value{
			Names: v.Names,
			Doc:   strings.TrimSpace(v.Doc),
			Decl:  decl,
		})
	}
	return out
}

func (r *renderer) funcs(fns []*doc.Func) []Func {
	out := make([]Func, 0, len(fns))
	for _, f := range fns {
		out = append(out, Func{
			Name:      f.Name,
			Signature: r.signature(f.Decl),
			Doc:       strings.TrimSpace(f.Doc),
			Recv:      f.Recv,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// signature renders a function declaration without its body or doc comment.
func (r *renderer) signature(fn *ast.FuncDecl) string {
	if fn == nil {
		return ""
	}
	clone := *fn
	clone.Doc = nil
	clone.Body = nil
	return r.print(&clone)
}

func (r *renderer) typeItem(dt *doc.Type) Type {
	t := Type{
		Name:    dt.Name,
		Kind:    "basic",
		Doc:     strings.TrimSpace(dt.Doc),
		Fields:  []Field{},
		Consts:  r.values(dt.Consts),
		Vars:    r.values(dt.Vars),
		Funcs:   r.funcs(dt.Funcs),
		Methods: r.funcs(dt.Methods),
	}

	spec := typeSpec(dt)
	if spec == nil {
		return t
	}
	if spec.Assign.IsValid() {
		t.Kind = "alias"
	}
	switch node := spec.Type.(type) {
	case *ast.StructType:
		if !spec.Assign.IsValid() {
			t.Kind = "struct"
		}
		t.Fields = r.fields(node.Fields)
	case *ast.InterfaceType:
		if !spec.Assign.IsValid() {
			t.Kind = "interface"
		}
	case *ast.FuncType:
		if !spec.Assign.IsValid() {
			t.Kind = "func"
		}
	}

	// A struct's shape is carried by the field table; an interface's by its
	// method set. For everything else the declaration itself is the clearest
	// statement of what the type is.
	if t.Kind != "struct" && t.Kind != "interface" {
		clone := *spec
		clone.Doc = nil
		clone.Comment = nil
		t.Decl = "type " + r.print(&clone)
	}
	return t
}

// typeSpec locates the TypeSpec inside a doc.Type's declaration. go/doc groups
// a `type (...)` block under one GenDecl, so match on the name.
func typeSpec(dt *doc.Type) *ast.TypeSpec {
	if dt.Decl == nil {
		return nil
	}
	for _, s := range dt.Decl.Specs {
		ts, ok := s.(*ast.TypeSpec)
		if ok && ts.Name != nil && ts.Name.Name == dt.Name {
			return ts
		}
	}
	return nil
}

func (r *renderer) fields(list *ast.FieldList) []Field {
	out := []Field{}
	if list == nil {
		return out
	}
	for _, f := range list.List {
		typeStr := r.print(f.Type)
		docStr := commentText(f.Doc, f.Comment)
		if len(f.Names) == 0 {
			// Embedded field: its name is the type name.
			out = append(out, Field{Name: typeStr, Type: typeStr, Doc: docStr})
			continue
		}
		for _, n := range f.Names {
			if !n.IsExported() {
				continue
			}
			out = append(out, Field{Name: n.Name, Type: typeStr, Doc: docStr})
		}
	}
	return out
}

// commentText prefers the doc comment above a field over the trailing one
// beside it.
func commentText(doc, trailing *ast.CommentGroup) string {
	if doc != nil {
		return strings.TrimSpace(doc.Text())
	}
	if trailing != nil {
		return strings.TrimSpace(trailing.Text())
	}
	return ""
}

// stripDoc detaches a declaration's doc comment so the rendered form carries
// only the declaration; the comment is emitted separately as Doc.
func stripDoc(decl *ast.GenDecl) ast.Node {
	if decl == nil {
		return nil
	}
	clone := *decl
	clone.Doc = nil
	return &clone
}
