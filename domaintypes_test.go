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

// TestResponseFieldsAreBranded is a class-scoped guard for the model-rewrite
// path of hook 01 (issue: response-side domain typing). openapi-generator emits
// semantic fields with their bare underlying Go type (string/int32/[]string);
// the hook must retype them to the branded named type. This test parses the
// generated struct fields and asserts that representative shapes are branded, so
// the whole defect class is caught if _rewrite_model_file regresses:
//
//   - a scalar $ref field           -> branded scalar   (ProcessInstanceKey)
//   - a property $ref to an array   -> branded slice     ([]Tag, from TagSet)
//   - an inline oneOf union field   -> branded union     (ScopeKey)
//   - a nullable/optional field     -> Nullable<Type>    (NullableProcessInstanceKey)
//
// It reads the concrete field types from client/ so a bare underlying type
// surviving anywhere in these representative fields fails loudly.
func TestResponseFieldsAreBranded(t *testing.T) {
	fieldTypes := parseStructFieldTypes(t)
	if len(fieldTypes) == 0 {
		t.Skip("client/ not generated; skipping response-branding guard")
	}

	// (struct, json field) -> required branded Go type. These exercise the four
	// resolution shapes the hook must handle. A bare string/int32/[]string here
	// means the field was left outside the type system.
	want := []struct{ typ, field, wantGo string }{
		{"ElementInstanceFilterFields", "elementInstanceScopeKey", "ScopeKey"}, // inline oneOf -> union
		{"ProcessInstanceResult", "tags", "[]Tag"},                             // $ref -> TagSet -> []Tag
		{"AuditLogResult", "processInstanceKey", "NullableProcessInstanceKey"}, // nullable wrapper
	}
	for _, w := range want {
		got, ok := fieldTypes[w.typ+"."+w.field]
		if !ok {
			// Field/struct not present in this spec revision: skip rather than
			// pin to an exact schema shape that upstream may rename.
			continue
		}
		// A branded type never equals a bare underlying token. NullableModelString
		// is the un-branded wrapper a nullable semantic key regresses to, so it
		// counts as bare here alongside the primitive tokens.
		bare := strings.TrimPrefix(strings.TrimPrefix(got, "[]"), "*")
		if bare == "string" || bare == "int32" || bare == "int64" || strings.HasPrefix(bare, "NullableInt") || bare == "NullableString" || bare == "NullableModelString" {
			t.Errorf("%s.%s is %q (bare underlying type); expected branded %q — model rewrite (hook 01) did not retype it",
				w.typ, w.field, got, w.wantGo)
		}
	}
}

// parseStructFieldTypes returns "StructName.jsonKey" -> Go field type expression
// for every struct field in the generated client package that carries a json tag.
func parseStructFieldTypes(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	fset := token.NewFileSet()
	entries, err := os.ReadDir("client")
	if err != nil {
		return out // client/ absent; caller skips
	}
	jsonKey := func(tag string) string {
		tag = strings.Trim(tag, "`")
		i := strings.Index(tag, `json:"`)
		if i < 0 {
			return ""
		}
		rest := tag[i+len(`json:"`):]
		if j := strings.IndexByte(rest, '"'); j >= 0 {
			rest = rest[:j]
		}
		if c := strings.IndexByte(rest, ','); c >= 0 {
			rest = rest[:c]
		}
		return rest
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
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				for _, fld := range st.Fields.List {
					if fld.Tag == nil || len(fld.Names) != 1 {
						continue
					}
					key := jsonKey(fld.Tag.Value)
					if key == "" {
						continue
					}
					out[ts.Name.Name+"."+key] = goTypeString(fld.Type)
				}
			}
		}
	}
	return out
}

// goTypeString renders the subset of type expressions the generated models use
// for scalar, pointer, and slice fields (e.g. "ScopeKey", "*ScopeKey", "[]Tag").
func goTypeString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + goTypeString(x.X)
	case *ast.ArrayType:
		return "[]" + goTypeString(x.Elt)
	case *ast.SelectorExpr:
		return goTypeString(x.X) + "." + x.Sel.Name
	default:
		return ""
	}
}
