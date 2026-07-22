package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateFacade locks the AST-based facade generator against a fixture
// client package: it must emit one ergonomic *CamundaClient method per operation,
// qualify client types with the openapi alias, and handle both value-returning
// and no-value operations.
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
		`openapi "github.com/camunda/orchestration-cluster-api-go/client"`,
		// Value-returning op: exposes required params + an opts transform, returns (value, error).
		"func (c *CamundaClient) GetWidget(ctx context.Context, id openapi.WidgetKey, opts ...func(openapi.ApiGetWidgetRequest) openapi.ApiGetWidgetRequest) (*openapi.Widget, error) {",
		"req := c.raw.WidgetAPI.GetWidget(ctx, id)",
		"req = opt(req)",
		"value, resp, err := req.Execute()",
		"return value, c.wrapError(resp, err)",
		// No-value op: returns error only.
		"func (c *CamundaClient) DeleteWidget(ctx context.Context, id openapi.WidgetKey, opts ...func(openapi.ApiDeleteWidgetRequest) openapi.ApiDeleteWidgetRequest) error {",
		"return c.wrapError(resp, err)",
	}
	for _, w := range want {
		if !strings.Contains(src, w) {
			t.Errorf("facade output missing %q\n--- generated ---\n%s", w, src)
		}
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
	if !ok || info.builder != "WidgetRequest" || info.typ != "openapi.WidgetRequest" {
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
