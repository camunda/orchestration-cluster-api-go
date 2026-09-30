package main

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateFacade locks the AST-based facade generator against a fixture
// client package: it must emit one ergonomic *CamundaClient method per operation,
// name client types by their re-exported camunda names, and handle both
// value-returning and no-value operations.
func TestGenerateFacade(t *testing.T) {
	src, count, err := generateFacade("testdata/client", "", "")
	if err != nil {
		t.Fatalf("generateFacade: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 operations, got %d", count)
	}

	want := []string{
		"package camunda",
		// Value-returning op: exposes required params + an opts transform, returns (value, error).
		"func (c *CamundaClient) GetWidget(ctx context.Context, id WidgetKey, opts ...func(ApiGetWidgetRequest) ApiGetWidgetRequest) (*Widget, error) {",
		"req := c.raw.WidgetAPI.GetWidget(ctx, id)",
		"req = opt(req)",
		"value, resp, err := req.Execute()",
		"return value, c.wrapError(resp, err)",
		// No-value op: returns error only.
		"func (c *CamundaClient) DeleteWidget(ctx context.Context, id WidgetKey, opts ...func(ApiDeleteWidgetRequest) ApiDeleteWidgetRequest) error {",
		"return c.wrapError(resp, err)",
	}
	for _, w := range want {
		if !strings.Contains(src, w) {
			t.Errorf("facade output missing %q\n--- generated ---\n%s", w, src)
		}
	}
	// Every fixture type is re-exported, so an unused client import would not compile.
	if strings.Contains(src, clientImportPath) {
		t.Errorf("facade imports the client package although no signature needs it\n%s", src)
	}
}

func TestTypeStringQualifiesOnlyTypesThatAreNotReexported(t *testing.T) {
	r := &renderer{
		clientTypes: map[string]bool{"Widget": true, "Configuration": true},
		short:       map[string]bool{"Widget": true},
		usedPkgs:    map[string]bool{},
	}
	if got := r.typeString(&ast.StarExpr{X: ast.NewIdent("Widget")}); got != "*Widget" || r.qualified {
		t.Fatalf("re-exported type rendered as %q (qualified=%v), want *Widget", got, r.qualified)
	}
	if got := r.typeString(&ast.StarExpr{X: ast.NewIdent("Configuration")}); got != "*camundaapi.Configuration" || !r.qualified {
		t.Fatalf("plumbing type rendered as %q (qualified=%v), want *camundaapi.Configuration", got, r.qualified)
	}
	if src := emit(nil, nil, true); !strings.Contains(src, `camundaapi "`+clientImportPath+`"`) {
		t.Errorf("facade must import the client package when a signature is qualified\n%s", src)
	}
}

func TestLoadExamplesExtractsAndDedentsFirstRegion(t *testing.T) {
	dir := t.TempDir()
	source := "package examples\n\nfunc f() {\n\t// region CreateWidget\n\twidget := create()\n\tif widget != nil {\n\t\tuse(widget)\n\t}\n\t// endregion CreateWidget\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "widgets.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	opMap := `{"createWidget":[{"file":"widgets.go","region":"CreateWidget"}],"missing":[]}`
	if err := os.WriteFile(filepath.Join(dir, "operation-map.json"), []byte(opMap), 0o600); err != nil {
		t.Fatal(err)
	}

	got := loadExamples(dir)
	want := "widget := create()\nif widget != nil {\n\tuse(widget)\n}"
	if got["createWidget"] != want {
		t.Errorf("example = %q, want %q", got["createWidget"], want)
	}
	if _, ok := got["missing"]; ok {
		t.Error("operation with no example entries should be omitted")
	}
}

func TestLoadExamplesToleratesMissingAndInvalidMaps(t *testing.T) {
	if got := loadExamples(""); got != nil {
		t.Errorf("empty directory returned %#v, want nil", got)
	}
	if got := loadExamples(t.TempDir()); got != nil {
		t.Errorf("missing map returned %#v, want nil", got)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "operation-map.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadExamples(dir); got != nil {
		t.Errorf("invalid map returned %#v, want nil", got)
	}
}

func TestLoadBodyInfoIncludesOnlyJSONClientModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata.json")
	metadata := `{"operations":[
			{"operationId":"createWidget","hasRequestBody":true,"requestBodySchemaRef":"WidgetRequest","requestBodyContentTypes":["application/json"]},
			{"operationId":"uploadWidget","hasRequestBody":true,"requestBodySchemaRef":"WidgetRequest","requestBodyContentTypes":["multipart/form-data"]},
			{"operationId":"inlineBody","hasRequestBody":true,"requestBodySchemaRef":"Inline","requestBodyContentTypes":["application/json"]}
		]}`
	if err := os.WriteFile(path, []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}

	got := loadBodyInfo(path, map[string]bool{"WidgetRequest": true})
	info, ok := got["CreateWidget"]
	if !ok || info.builder != "WidgetRequest" {
		t.Fatalf("CreateWidget body info = %+v, present=%v", info, ok)
	}
	if _, ok := got["UploadWidget"]; ok {
		t.Error("multipart body must not be surfaced as a JSON facade body")
	}
	if _, ok := got["InlineBody"]; ok {
		t.Error("non-client body model must not be surfaced")
	}
}

func TestFacadegenHelpers(t *testing.T) {
	if hasJSONContent([]string{"text/plain", "application/problem+json"}) {
		t.Error("application/problem+json should not be treated as application/json")
	}
	if !hasJSONContent([]string{"application/json; charset=utf-8"}) {
		t.Error("JSON content type was not detected")
	}
	if upperFirst("") != "" || upperFirst("widget") != "Widget" {
		t.Error("upperFirst returned an unexpected value")
	}
	if absOrDot("file.go") != "file.go" || absOrDot("") != "" || absOrDot("dir/file.go") != "dir/file.go" {
		t.Error("absOrDot returned an unexpected value")
	}
}
