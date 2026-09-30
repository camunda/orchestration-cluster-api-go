package camunda_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// clientPlumbing is the generated HTTP-client machinery that package camunda
// deliberately does not re-export (see cmd/facadegen/reexport.go). It is listed
// by name, so a generator upgrade that adds a new exported helper fails
// TestReexportCoversEveryClientNameExceptPlumbing until someone decides which
// side it belongs on.
var clientPlumbing = []string{
	"APIClient", "APIKey", "APIResponse", "BasicAuth", "CacheExpires", "Configuration",
	"ContextAccessToken", "ContextBasicAuth", "ContextOperationServerIndices",
	"ContextOperationServerVariables", "ContextServerIndex", "ContextServerVariables",
	"GenericOpenAPIError", "IsNil", "JsonCheck", "MappedNullable", "NewAPIClient",
	"NewAPIResponse", "NewAPIResponseWithError", "NewConfiguration", "PtrBool",
	"PtrFloat32", "PtrFloat64", "PtrInt", "PtrInt32", "PtrInt64", "PtrString", "PtrTime",
	"ServerConfiguration", "ServerConfigurations", "ServerVariable", "XmlCheck",
}

// exportedDecls returns the exported package-level names declared by the
// matching non-test files in dir, mapped to a constant's literal value ("" for
// anything else).
func exportedDecls(t *testing.T, dir string, keep func(name string) bool) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || !keep(name) {
			continue
		}
		f, err := parser.ParseFile(fset, dir+"/"+name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && ast.IsExported(d.Name.Name) {
					out[d.Name.Name] = ""
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(s.Name.Name) {
							out[s.Name.Name] = ""
						}
					case *ast.ValueSpec:
						for i, n := range s.Names {
							if !ast.IsExported(n.Name) {
								continue
							}
							out[n.Name] = ""
							if d.Tok == token.CONST && i < len(s.Values) {
								if lit, ok := s.Values[i].(*ast.BasicLit); ok {
									out[n.Name] = lit.Value
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

func sortedDiff(a, b map[string]string) []string {
	var out []string
	for n := range a {
		if _, ok := b[n]; !ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// TestReexportCoversEveryClientNameExceptPlumbing asserts, in both directions,
// that package camunda re-exports exactly the generated client's exported names
// minus clientPlumbing, so callers never need the client import for anything
// but the raw HTTP client.
func TestReexportCoversEveryClientNameExceptPlumbing(t *testing.T) {
	client := exportedDecls(t, "client", func(string) bool { return true })
	reexported := exportedDecls(t, ".", func(name string) bool { return name == "reexport_generated.go" })
	if len(reexported) == 0 {
		t.Fatal("reexport_generated.go declares nothing; regenerate with make generate")
	}

	expected := map[string]string{}
	for n, v := range client {
		expected[n] = v
	}
	for _, n := range clientPlumbing {
		if _, ok := client[n]; !ok {
			t.Errorf("clientPlumbing lists %s, which the client no longer exports; remove it", n)
		}
		delete(expected, n)
	}

	if missing := sortedDiff(expected, reexported); len(missing) > 0 {
		t.Errorf("client names neither re-exported nor listed as plumbing (%d): %s",
			len(missing), strings.Join(missing, ", "))
	}
	if extra := sortedDiff(reexported, expected); len(extra) > 0 {
		t.Errorf("re-exported names that are plumbing or absent from the client (%d): %s",
			len(extra), strings.Join(extra, ", "))
	}

	// A constant re-exported by literal must carry the client's value exactly.
	for n, v := range reexported {
		if v != "" && v != client[n] {
			t.Errorf("constant %s re-exported as %s, client declares %s", n, v, client[n])
		}
	}
}

// TestExportedSignaturesUseReexportedNames keeps the camunda API reference free of
// camundaapi qualifiers: exported functions, methods and struct fields must name
// a re-exported client type by its camunda name. Plumbing (e.g. Raw's
// *camundaapi.APIClient) is the only client type that may appear qualified.
func TestExportedSignaturesUseReexportedNames(t *testing.T) {
	plumbing := map[string]bool{}
	for _, n := range clientPlumbing {
		plumbing[n] = true
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "reexport_generated.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		var exposed []ast.Node
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if ast.IsExported(d.Name.Name) {
					exposed = append(exposed, d.Type)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ast.IsExported(ts.Name.Name) {
						continue
					}
					if st, ok := ts.Type.(*ast.StructType); ok {
						for _, fld := range st.Fields.List {
							if len(fld.Names) > 0 && ast.IsExported(fld.Names[0].Name) {
								exposed = append(exposed, fld.Type)
							}
						}
					}
				}
			}
		}
		for _, node := range exposed {
			ast.Inspect(node, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "camundaapi" && !plumbing[sel.Sel.Name] {
					offenders = append(offenders, fset.Position(sel.Pos()).String()+": camundaapi."+sel.Sel.Name)
				}
				return true
			})
		}
	}
	if len(offenders) > 0 {
		t.Errorf("exported API names re-exported client types via camundaapi; use the camunda name:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
