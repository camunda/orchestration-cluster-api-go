package camunda_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEverySemanticKeyHasAGeneratedType is a class-scoped guard for the Domain
// Type System (post-processing hook 01). openapi-generator does not define
// Camunda's semantic key types; the hook must generate a distinct named type for
// every semantic key in the spec metadata, or the client will not compile. This
// test fails if any key is left undefined (the whole defect class), not just a
// specific one.
func TestEverySemanticKeyHasAGeneratedType(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("external-spec", "bundled", "spec-metadata.json"))
	if err != nil {
		t.Skipf("spec metadata not available: %v", err)
	}
	var meta struct {
		SemanticKeys []struct {
			Name string `json:"name"`
		} `json:"semanticKeys"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse spec metadata: %v", err)
	}
	if len(meta.SemanticKeys) == 0 {
		t.Fatal("spec metadata contains no semanticKeys")
	}

	types, funcs := parseClientDecls(t)
	if len(types) == 0 {
		t.Skip("client/ not generated; skipping domain-type guard")
	}
	if !types["ModelString"] {
		t.Error("client is missing the base ModelString type (domain-type hook 01)")
	}

	for _, k := range meta.SemanticKeys {
		if k.Name == "" {
			continue
		}
		if !types[k.Name] {
			t.Errorf("semantic key %q has no generated type in client/ (the client would not compile)", k.Name)
		}
		// Constructor-triad completeness for keys the domain-type hook owns.
		if funcs["New"+k.Name] && !funcs["Must"+k.Name] {
			t.Errorf("key %q has a New%s constructor but no Must%s", k.Name, k.Name, k.Name)
		}
	}
}

// parseClientDecls returns the set of top-level type names and (non-method)
// function names declared in the generated client package.
func parseClientDecls(t *testing.T) (types map[string]bool, funcs map[string]bool) {
	t.Helper()
	types = map[string]bool{}
	funcs = map[string]bool{}
	fset := token.NewFileSet()
	entries, err := os.ReadDir("client")
	if err != nil {
		return types, funcs // client/ absent; caller skips
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join("client", e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse client/%s: %v", e.Name(), err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						types[ts.Name.Name] = true
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil {
					funcs[d.Name.Name] = true
				}
			}
		}
	}
	return types, funcs
}
