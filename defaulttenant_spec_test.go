package camunda_test

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	camunda "github.com/camunda/orchestration-cluster-api-go/v11"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
)

// specTenantOps walks the bundled OpenAPI spec and returns the operationIds
// whose JSON request body — or any oneOf/anyOf variant of it — has an optional
// top-level tenantId property. The list comes from the spec, never from code.
func specTenantOps(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("external-spec/bundled/rest-api.bundle.json")
	if err != nil {
		t.Fatalf("reading bundled spec: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	resolve := func(s map[string]any) map[string]any {
		for i := 0; i < 16; i++ {
			ref, ok := s["$ref"].(string)
			if !ok {
				return s
			}
			s, _ = schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
			if s == nil {
				return nil
			}
		}
		return s
	}
	var hasOptionalTenant func(s map[string]any, depth int) bool
	hasOptionalTenant = func(s map[string]any, depth int) bool {
		s = resolve(s)
		if s == nil || depth > 8 {
			return false
		}
		if props, ok := s["properties"].(map[string]any); ok {
			if _, ok := props["tenantId"]; ok {
				required := false
				if req, ok := s["required"].([]any); ok {
					for _, r := range req {
						required = required || r == "tenantId"
					}
				}
				if !required {
					return true
				}
			}
		}
		for _, k := range []string{"oneOf", "anyOf", "allOf"} {
			if vs, ok := s[k].([]any); ok {
				for _, v := range vs {
					if m, ok := v.(map[string]any); ok && hasOptionalTenant(m, depth+1) {
						return true
					}
				}
			}
		}
		return false
	}
	out := map[string]bool{}
	for _, item := range spec["paths"].(map[string]any) {
		for _, opAny := range item.(map[string]any) {
			op, ok := opAny.(map[string]any)
			if !ok {
				continue
			}
			rb, ok := op["requestBody"].(map[string]any)
			if !ok {
				continue
			}
			content, _ := rb["content"].(map[string]any)
			js, ok := content["application/json"].(map[string]any)
			if !ok {
				continue
			}
			if s, ok := js["schema"].(map[string]any); ok && hasOptionalTenant(s, 0) {
				out[op["operationId"].(string)] = true
			}
		}
	}
	return out
}

// TestEveryOptionalBodyTenantOperationIsInjected fails when an operation whose
// spec request body (or a union variant of it) has an optional tenantId is not
// wired to a default-tenant helper — derived from the spec, not a list (#217).
func TestEveryOptionalBodyTenantOperationIsInjected(t *testing.T) {
	ops := specTenantOps(t)
	if len(ops) == 0 {
		t.Fatal("found no tenant-scoped operations in the spec; the walker is broken")
	}
	bodies := map[string]string{} // Go method name -> source of its body
	for _, file := range []string{"facade_generated.go", "falcon.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		src, _ := os.ReadFile(file)
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv != nil && fn.Body != nil {
				bodies[fn.Name.Name] += string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			}
		}
	}
	// CreateProcessInstance is hand-written (FALCON) in falcon.go and parsed above.
	for opID := range ops {
		name := strings.ToUpper(opID[:1]) + opID[1:]
		body, ok := bodies[name]
		if !ok {
			t.Errorf("%s: no facade method found", name)
			continue
		}
		if !strings.Contains(body, "withDefaultTenant") {
			t.Errorf("%s: request body has an optional tenantId but the default tenant is not injected", name)
		}
	}
	for _, want := range []string{"suspendProcessDefinitions", "resumeProcessDefinitions", "publishMessage", "evaluateDecision"} {
		if !ops[want] {
			t.Errorf("spec walker missed %s", want)
		}
	}
}

// TestDefaultTenantProcessDefinitionSuspension pins #217: the by-id variant
// of suspend/resume gets the effective default tenant, an explicit tenant
// wins, and the by-key variant (no tenantId) gets nothing.
func TestDefaultTenantProcessDefinitionSuspension(t *testing.T) {
	ctx := context.Background()
	type call func(c *camunda.CamundaClient, byID bool, explicit string)
	calls := map[string]call{
		"Suspend": func(c *camunda.CamundaClient, byID bool, explicit string) {
			var body camundaapi.ProcessDefinitionSuspensionInstruction
			if byID {
				v := camundaapi.NewProcessDefinitionSuspensionInstructionById("proc")
				if explicit != "" {
					v.SetTenantId(camundaapi.MustTenantId(explicit))
				}
				body.ProcessDefinitionSuspensionInstructionById = v
			} else {
				body.ProcessDefinitionSuspensionInstructionByKey = camundaapi.NewProcessDefinitionSuspensionInstructionByKey("2251799813685249")
			}
			_, _ = c.SuspendProcessDefinitions(ctx, body)
			if byID && body.ProcessDefinitionSuspensionInstructionById.TenantId != nil && explicit == "" {
				t.Error("caller's instruction was mutated")
			}
		},
		"Resume": func(c *camunda.CamundaClient, byID bool, explicit string) {
			var body camundaapi.ProcessDefinitionResumptionInstruction
			if byID {
				v := camundaapi.NewProcessDefinitionResumptionInstructionById("proc")
				if explicit != "" {
					v.SetTenantId(camundaapi.MustTenantId(explicit))
				}
				body.ProcessDefinitionResumptionInstructionById = v
			} else {
				body.ProcessDefinitionResumptionInstructionByKey = camundaapi.NewProcessDefinitionResumptionInstructionByKey("2251799813685249")
			}
			_, _ = c.ResumeProcessDefinitions(ctx, body)
			if byID && body.ProcessDefinitionResumptionInstructionById.TenantId != nil && explicit == "" {
				t.Error("caller's instruction was mutated")
			}
		},
	}
	for name, do := range calls {
		t.Run(name+"/ByIdConfigured", func(t *testing.T) {
			bc := newBodyCapture(t)
			do(bc.client(t, camunda.WithDefaultTenantID("acme")), true, "")
			if got := bc.tenantOf(); got != "acme" {
				t.Errorf("tenantId = %q, want acme", got)
			}
		})
		t.Run(name+"/ByIdUnconfigured", func(t *testing.T) {
			bc := newBodyCapture(t)
			do(bc.client(t), true, "")
			if got := bc.tenantOf(); got != camunda.DefaultTenantIdentifier {
				t.Errorf("tenantId = %q, want %q", got, camunda.DefaultTenantIdentifier)
			}
		})
		t.Run(name+"/ByIdExplicit", func(t *testing.T) {
			bc := newBodyCapture(t)
			do(bc.client(t, camunda.WithDefaultTenantID("acme")), true, "explicit")
			if got := bc.tenantOf(); got != "explicit" {
				t.Errorf("tenantId = %q, want explicit", got)
			}
		})
		t.Run(name+"/ByKey", func(t *testing.T) {
			bc := newBodyCapture(t)
			do(bc.client(t, camunda.WithDefaultTenantID("acme")), false, "")
			if _, ok := bc.body["tenantId"]; ok || bc.body == nil {
				t.Errorf("by-key body must not carry tenantId: %v", bc.body)
			}
		})
	}
}
