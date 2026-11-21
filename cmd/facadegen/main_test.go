package main

import (
	"go/ast"
	"go/parser"
	"go/token"
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

// TestEmitInjectsDefaultTenantHooks locks the emission of the default-tenant
// injection (issue #79): body ops default the tenant on the FINAL body — after
// the opts, read back via the request's Get<Body>() accessor — so a
// body-replacing opt cannot discard the injected tenant; request ops adjust the
// request after the opts so an explicit tenant always wins.
func TestEmitInjectsDefaultTenantHooks(t *testing.T) {
	ops := []operation{
		{field: "MessageAPI", name: "PublishMessage", reqType: "ApiPublishMessageRequest",
			bodyType: "MessagePublicationRequest", bodyBuilder: "MessagePublicationRequest",
			tenant: &tenantShape{bodyType: "MessagePublicationRequest", direct: true}},
		{field: "ResourceAPI", name: "CreateDeployment", reqType: "ApiCreateDeploymentRequest"},
		{field: "MessageAPI", name: "GetMessage", reqType: "ApiGetMessageRequest"},
	}
	src := emit(ops, nil, false)

	// The body is attached first, opts run, then the final body is defaulted and
	// re-attached — so the default survives a body-replacing opt.
	builderIdx := strings.Index(src, "req = req.MessagePublicationRequest(body)")
	optIdx := strings.Index(src, "req = opt(req)")
	defaultIdx := strings.Index(src, "req = req.MessagePublicationRequest(c.withDefaultTenantMessagePublicationRequest(*b))")
	getIdx := strings.Index(src, "req.GetMessagePublicationRequest()")
	if builderIdx == -1 || optIdx == -1 || defaultIdx == -1 || getIdx == -1 ||
		builderIdx >= optIdx || optIdx >= defaultIdx {
		t.Errorf("PublishMessage must attach the body, run opts, then default the final body\n%s", src)
	}
	reqHookIdx := strings.Index(src, "req = c.withDefaultTenantCreateDeployment(req)")
	if reqHookIdx == -1 || optIdx == -1 || reqHookIdx < optIdx {
		t.Errorf("CreateDeployment must default the tenant after the opts\n%s", src)
	}
	if !strings.Contains(src, "func (c *CamundaClient) withDefaultTenantMessagePublicationRequest(body MessagePublicationRequest) MessagePublicationRequest {") {
		t.Errorf("derived helper not emitted\n%s", src)
	}
	if strings.Contains(src, "withDefaultTenantGetMessage") || strings.Contains(src, "withDefaultTenantMessageCorrelation") {
		t.Errorf("operations outside the default-tenant set must not be hooked\n%s", src)
	}
}

// Without the optional metadata argument loadBodyInfo yields no body builder, so
// a tenant-body op has bodyBuilder == "". The tenant hook must then be skipped
// entirely: emitting req.Get()/req.() would overwrite the facade with invalid Go.
func TestEmitSkipsTenantBodyHookWhenBodyBuilderMissing(t *testing.T) {
	ops := []operation{
		{field: "MessageAPI", name: "PublishMessage", reqType: "ApiPublishMessageRequest",
			tenant: &tenantShape{bodyType: "MessagePublicationRequest", direct: true}},
	}
	src := emit(ops, nil, false)

	for _, bad := range []string{"req.Get()", "req.(c.withDefaultTenant", "c.withDefaultTenant"} {
		if strings.Contains(src, bad) {
			t.Errorf("tenant-body hook must be skipped when bodyBuilder is empty, found %q\n%s", bad, src)
		}
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
			{"operationId":"createWidget","hasRequestBody":true,"requestBodySchemaRef":"WidgetRequest","requestBodyContentTypes":["application/json"],"optionalTenantIdsInBody":false},
			{"operationId":"uploadWidget","hasRequestBody":true,"requestBodySchemaRef":"WidgetRequest","requestBodyContentTypes":["multipart/form-data"]},
			{"operationId":"inlineBody","hasRequestBody":true,"requestBodySchemaRef":"Inline","requestBodyContentTypes":["application/json"]}
		]}`
	if err := os.WriteFile(path, []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadBodyInfo(path, map[string]bool{"WidgetRequest": true})
	if err != nil {
		t.Fatal(err)
	}
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

// TestTenantShapeFromMetadata pins #217/#223: default-tenant injection comes
// from the spec metadata (optionalTenantIdInBodyVariants), per union variant —
// not from an operation list or the SDK's own spec walk.
func TestTenantShapeFromMetadata(t *testing.T) {
	src := `package x
type ById struct { TenantId *string }
type ByKey struct { Key string ` + "`json:\"key\"`" + ` }
type Direct struct { TenantId *string }
type Union struct { ById *ById; ByKey *ByKey }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	structs := collectStructs([]*ast.File{f})
	r := &renderer{clientTypes: map[string]bool{}, short: map[string]bool{}, usedPkgs: map[string]bool{}}
	if s, err := tenantShapeOf("Direct", []string{"Direct"}, structs, r); err != nil || s == nil || !s.direct {
		t.Errorf("Direct: %+v %v", s, err)
	}
	if s, err := tenantShapeOf("Union", []string{"ById"}, structs, r); err != nil || s == nil || s.direct || len(s.variants) != 1 || s.variants[0] != "ById" {
		t.Errorf("Union: %+v %v", s, err)
	}
	if s, err := tenantShapeOf("Union", nil, structs, r); err != nil || s != nil {
		t.Errorf("unlisted body must not be injected: %+v %v", s, err)
	}
	if _, err := tenantShapeOf("Union", []string{"Missing"}, structs, r); err == nil {
		t.Error("a metadata variant absent from the union must fail, not be guessed")
	}
}

// TestLoadBodyInfoReadsTenantMetadata checks the spec-metadata fields of
// camunda-schema-bundler >= 2.5.0 reach the facade.
func TestLoadBodyInfoReadsTenantMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.json")
	meta := `{"operations":[
 {"operationId":"suspend","hasRequestBody":true,"requestBodySchemaRef":"Union","requestBodyContentTypes":["application/json"],"optionalTenantIdInBodyVariants":["ById"]},
 {"operationId":"activateJobs","hasRequestBody":true,"requestBodySchemaRef":"Jobs","requestBodyContentTypes":["application/json"],"optionalTenantIdsInBody":true}]}`
	if err := os.WriteFile(path, []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadBodyInfo(path, map[string]bool{"Union": true, "Jobs": true})
	if err != nil {
		t.Fatal(err)
	}
	if bi := got["Suspend"]; len(bi.tenantVariants) != 1 || bi.tenantVariants[0] != "ById" || bi.tenantIDsInBody {
		t.Errorf("Suspend: %+v", bi)
	}
	if bi := got["ActivateJobs"]; !bi.tenantIDsInBody || len(bi.tenantVariants) != 0 {
		t.Errorf("ActivateJobs: %+v", bi)
	}
}

// loadBodyInfo must fail closed on metadata that cannot drive default-tenant
// injection, instead of silently emitting a facade without it.
func TestLoadBodyInfoFailsClosedOnStaleOrBrokenMetadata(t *testing.T) {
	cases := map[string]string{
		"legacy boolean without variants": `{"operations":[
			{"operationId":"publish","hasRequestBody":true,"requestBodySchemaRef":"Msg","requestBodyContentTypes":["application/json"],"optionalTenantIdInBody":true,"optionalTenantIdsInBody":false}]}`,
		"no 2.5.0 fields at all": `{"operations":[
			{"operationId":"publish","hasRequestBody":true,"requestBodySchemaRef":"Msg","requestBodyContentTypes":["application/json"]}]}`,
		"malformed json": `{"operations":`,
	}
	for name, metadata := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "metadata.json")
			if err := os.WriteFile(path, []byte(metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadBodyInfo(path, map[string]bool{"Msg": true}); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
	if _, err := loadBodyInfo(filepath.Join(t.TempDir(), "missing.json"), nil); err == nil {
		t.Fatal("missing metadata file: expected an error, got nil")
	}
}
