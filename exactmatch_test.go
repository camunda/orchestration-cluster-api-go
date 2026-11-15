package camunda_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
)

// Every *ExactMatch filter branch must be a scalar: openapi-generator emits enum-
// and key-backed ones as empty structs, which marshal as `{}` and cannot decode the
// value they exist to carry. Checked over the whole generated client, not a sample.
func TestEveryExactMatchIsAScalar(t *testing.T) {
	files, err := filepath.Glob("client/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("list client files: %v", err)
	}
	structs := map[string]bool{}
	exact := map[string]ast.Expr{}
	fset := token.NewFileSet()
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if _, ok := ts.Type.(*ast.StructType); ok {
					structs[ts.Name.Name] = true
				}
				if strings.HasSuffix(ts.Name.Name, "ExactMatch") {
					exact[ts.Name.Name] = ts.Type
				}
			}
		}
	}
	if len(exact) == 0 {
		t.Fatal("found no *ExactMatch types; the guard would pass vacuously")
	}
	for name, typ := range exact {
		if _, ok := typ.(*ast.StructType); ok {
			t.Errorf("%s is a struct; it must alias its key or enum type", name)
			continue
		}
		if id, ok := typ.(*ast.Ident); ok && (structs[id.Name] || id.Name == "ModelString") {
			t.Errorf("%s aliases %s, not a key or enum scalar", name, id.Name)
		}
	}
}

func TestExactMatchFiltersRoundTripTheirScalar(t *testing.T) {
	cases := []struct {
		name  string
		value string
		into  any
	}{
		{"key-backed", `"2251799813685249"`, &camundaapi.ProcessInstanceKeyFilterProperty{}},
		{"enum-backed", `"ACTIVE"`, &camundaapi.ProcessInstanceStateFilterProperty{}},
		{"protection mode", `"REDACT"`, &camundaapi.ProtectionModeFilterProperty{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tc.value), tc.into); err != nil {
				t.Fatalf("decode %s: %v", tc.value, err)
			}
			out, err := json.Marshal(tc.into)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if string(out) != tc.value {
				t.Errorf("round trip = %s, want %s", out, tc.value)
			}
		})
	}
}
