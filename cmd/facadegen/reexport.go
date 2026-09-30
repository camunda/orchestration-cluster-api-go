package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// plumbingFiles hold the generated HTTP-client machinery: the raw APIClient, its
// Configuration and server selection, the auth context keys, and the raw error
// and response types. It stays in camundaapi only, so package camunda has one
// client (CamundaClient), one configuration (Config) and one error (APIError).
var plumbingFiles = map[string]bool{
	"client.go":        true,
	"configuration.go": true,
	"response.go":      true,
}

// plumbingNames are the same kind of machinery declared in utils.go.
var plumbingNames = map[string]bool{
	"IsNil":          true,
	"MappedNullable": true,
	"PtrBool":        true,
	"PtrFloat32":     true,
	"PtrFloat64":     true,
	"PtrInt":         true,
	"PtrInt32":       true,
	"PtrInt64":       true,
	"PtrString":      true,
	"PtrTime":        true,
}

func isPlumbing(file, name string) bool {
	return plumbingFiles[file] || plumbingNames[name]
}

type clientFile struct {
	name string // base file name
	ast  *ast.File
}

type clientPkg struct {
	fset  *token.FileSet
	files []clientFile // sorted by name, so output is deterministic
}

func (p *clientPkg) astFiles() []*ast.File {
	out := make([]*ast.File, len(p.files))
	for i, f := range p.files {
		out[i] = f.ast
	}
	return out
}

func parseClient(dir string) (*clientPkg, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { //nolint:staticcheck // ParseDir is adequate for the single generated client package
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", dir, err)
	}
	p := &clientPkg{fset: fset}
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			p.files = append(p.files, clientFile{name: filepath.Base(path), ast: f})
		}
	}
	if len(p.files) == 0 {
		return nil, fmt.Errorf("no Go files found in %s", dir)
	}
	sort.Slice(p.files, func(i, j int) bool { return p.files[i].name < p.files[j].name })
	return p, nil
}

// reexportedTypes returns the client types that package camunda re-exports.
func reexportedTypes(files []clientFile) map[string]bool {
	out := map[string]bool{}
	for _, cf := range files {
		for _, decl := range cf.ast.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				name := spec.(*ast.TypeSpec).Name.Name
				if ast.IsExported(name) && !isPlumbing(cf.name, name) {
					out[name] = true
				}
			}
		}
	}
	return out
}

// generateReexports parses the client package in clientDir and returns the source
// of a package camunda file that re-exports every exported, non-plumbing client
// declaration, plus the number of names re-exported. Types become aliases,
// functions become forwarding wrappers (so they document as functions and group
// under their result type), enum constants keep their literal value (so the value
// shows in the docs), and variables are re-bound.
//
// It fails when a re-exported name is already declared by package camunda in
// rootDir; skip lists the generated files in rootDir that are being regenerated.
func generateReexports(clientDir, rootDir string, skip ...string) (string, int, error) {
	pkg, err := parseClient(clientDir)
	if err != nil {
		return "", 0, err
	}
	imports, err := collectImports(pkg.files)
	if err != nil {
		return "", 0, err
	}
	r := &renderer{
		fset:        pkg.fset,
		clientTypes: collectTypeNames(pkg.astFiles()),
		short:       reexportedTypes(pkg.files),
		usedPkgs:    map[string]bool{},
	}

	var body strings.Builder
	origin := map[string]string{} // re-exported name -> client file
	add := func(name, file string) { origin[name] = file }

	for _, cf := range pkg.files {
		for _, decl := range cf.ast.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				name := d.Name.Name
				if d.Recv != nil || !ast.IsExported(name) || isPlumbing(cf.name, name) {
					continue
				}
				writeDoc(&body, d.Doc)
				body.WriteString(r.wrapper(d))
				add(name, cf.name)
			case *ast.GenDecl:
				switch d.Tok {
				case token.TYPE:
					for _, spec := range d.Specs {
						ts := spec.(*ast.TypeSpec)
						name := ts.Name.Name
						if !ast.IsExported(name) || isPlumbing(cf.name, name) {
							continue
						}
						writeDoc(&body, specDoc(d, ts.Doc),
							fmt.Sprintf("Fields and methods are documented on [%s.%s].", clientAlias, name))
						fmt.Fprintf(&body, "type %s = %s.%s\n\n", name, clientAlias, name)
						add(name, cf.name)
					}
				case token.CONST, token.VAR:
					var lines []string
					for _, spec := range d.Specs {
						vs := spec.(*ast.ValueSpec)
						for i, n := range vs.Names {
							if !ast.IsExported(n.Name) || isPlumbing(cf.name, n.Name) {
								continue
							}
							lines = append(lines, r.valueLine(d.Tok, vs, i))
							add(n.Name, cf.name)
						}
					}
					if len(lines) == 0 {
						continue
					}
					writeDoc(&body, d.Doc)
					fmt.Fprintf(&body, "%s (\n\t%s\n)\n\n", d.Tok, strings.Join(lines, "\n\t"))
				}
			}
		}
	}

	if err := checkClashes(rootDir, origin, skip); err != nil {
		return "", 0, err
	}

	var b strings.Builder
	b.WriteString("// Code generated by cmd/facadegen. DO NOT EDIT.\n\n")
	b.WriteString("package camunda\n\n")
	b.WriteString("import (\n")
	var used []string
	for name := range r.usedPkgs {
		used = append(used, name)
	}
	sort.Strings(used)
	for _, name := range used {
		path, ok := imports[name]
		if !ok {
			return "", 0, fmt.Errorf("client signature uses package %q, which no client file imports", name)
		}
		if filepath.Base(path) == name {
			fmt.Fprintf(&b, "\t%q\n", path)
		} else {
			fmt.Fprintf(&b, "\t%s %q\n", name, path)
		}
	}
	fmt.Fprintf(&b, "\n\t%s %q\n)\n\n", clientAlias, clientImportPath)
	b.WriteString(body.String())
	return b.String(), len(origin), nil
}

// wrapper renders a forwarding function for a client package-level function.
func (r *renderer) wrapper(fn *ast.FuncDecl) string {
	var params, args []string
	i := 0
	for _, field := range fn.Type.Params.List {
		typ := r.typeString(field.Type)
		_, variadic := field.Type.(*ast.Ellipsis)
		names := field.Names
		if len(names) == 0 {
			names = []*ast.Ident{nil}
		}
		for _, n := range names {
			name := "p" + strconv.Itoa(i)
			if n != nil && n.Name != "_" {
				name = n.Name
			}
			i++
			params = append(params, name+" "+typ)
			if variadic {
				name += "..."
			}
			args = append(args, name)
		}
	}
	var results []string
	if fn.Type.Results != nil {
		for _, field := range fn.Type.Results.List {
			typ := r.typeString(field.Type)
			for range max(len(field.Names), 1) {
				results = append(results, typ)
			}
		}
	}
	ret := ""
	switch len(results) {
	case 0:
	case 1:
		ret = " " + results[0]
	default:
		ret = " (" + strings.Join(results, ", ") + ")"
	}
	call := clientAlias + "." + fn.Name.Name + "(" + strings.Join(args, ", ") + ")"
	if len(results) > 0 {
		call = "return " + call
	}
	return fmt.Sprintf("func %s(%s)%s {\n\t%s\n}\n\n", fn.Name.Name, strings.Join(params, ", "), ret, call)
}

// valueLine renders the i-th name of a const or var spec. A typed constant with a
// literal value keeps that literal, so its value is visible in the camunda docs
// and it groups under its (aliased) type; anything else refers to the client.
func (r *renderer) valueLine(tok token.Token, vs *ast.ValueSpec, i int) string {
	name := vs.Names[i].Name
	if tok == token.CONST && vs.Type != nil && i < len(vs.Values) {
		if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
			return name + " " + r.typeString(vs.Type) + " = " + lit.Value
		}
	}
	return name + " = " + clientAlias + "." + name
}

// specDoc returns a type's doc comment, which go/ast attaches to the GenDecl
// rather than the spec when the declaration is not parenthesized.
func specDoc(d *ast.GenDecl, doc *ast.CommentGroup) *ast.CommentGroup {
	if doc == nil && !d.Lparen.IsValid() {
		return d.Doc
	}
	return doc
}

func writeDoc(b *strings.Builder, cg *ast.CommentGroup, extra ...string) {
	var lines []string
	if text := strings.TrimRight(cg.Text(), "\n"); text != "" {
		lines = strings.Split(text, "\n")
	}
	for _, e := range extra {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, e)
	}
	for _, l := range lines {
		if l == "" {
			b.WriteString("//\n")
		} else {
			b.WriteString("// " + l + "\n")
		}
	}
}

// collectImports maps each import name used across the client files to its path.
func collectImports(files []clientFile) (map[string]string, error) {
	out := map[string]string{}
	for _, cf := range files {
		for _, imp := range cf.ast.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return nil, err
			}
			name := filepath.Base(path)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if prev, ok := out[name]; ok && prev != path {
				return nil, fmt.Errorf("client files import both %q and %q as %s", prev, path, name)
			}
			out[name] = path
		}
	}
	return out, nil
}

// checkClashes fails when a name about to be re-exported is already declared at
// package level by package camunda in rootDir (tests included, as they compile
// into the same package).
func checkClashes(rootDir string, origin map[string]string, skip []string) error {
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", rootDir, err)
	}
	fset := token.NewFileSet()
	var clashes []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || skipped[name] {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(rootDir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", name, err)
		}
		if f.Name.Name != "camunda" {
			continue
		}
		for _, declared := range topLevelNames(f) {
			if src, ok := origin[declared]; ok {
				clashes = append(clashes, fmt.Sprintf("%s (declared in %s, re-exported from client/%s)", declared, name, src))
			}
		}
	}
	if len(clashes) > 0 {
		sort.Strings(clashes)
		return fmt.Errorf("re-exported client names clash with package camunda:\n  %s\n"+
			"Rename the hand-written declaration, or add the client name to plumbingNames "+
			"in cmd/facadegen/reexport.go if it should not be re-exported",
			strings.Join(clashes, "\n  "))
	}
	return nil
}

// topLevelNames returns the exported package-level names a file declares.
func topLevelNames(f *ast.File) []string {
	var out []string
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && ast.IsExported(d.Name.Name) {
				out = append(out, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if ast.IsExported(s.Name.Name) {
						out = append(out, s.Name.Name)
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if ast.IsExported(n.Name) {
							out = append(out, n.Name)
						}
					}
				}
			}
		}
	}
	return out
}
