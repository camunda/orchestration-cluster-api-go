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

// specTenantOps reads the checked-in camunda-schema-bundler metadata
// (spec-metadata.json, bundler >= 2.5) and returns the operationIds whose
// request body — or any union variant of it — has an optional top-level
// tenantId (optionalTenantIdInBody / optionalTenantIdInBodyVariants) or an
// optional tenantIds list (optionalTenantIdsInBody). The list comes from the
// spec metadata, never from code (#223).
func specTenantOps(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("external-spec/bundled/spec-metadata.json")
	if err != nil {
		t.Fatalf("reading spec metadata: %v", err)
	}
	var meta struct {
		Operations []struct {
			OperationID                    string    `json:"operationId"`
			OptionalTenantIDInBody         *bool     `json:"optionalTenantIdInBody"`
			OptionalTenantIDInBodyVariants *[]string `json:"optionalTenantIdInBodyVariants"`
			OptionalTenantIDsInBody        *bool     `json:"optionalTenantIdsInBody"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, op := range meta.Operations {
		if op.OptionalTenantIDInBody == nil || op.OptionalTenantIDInBodyVariants == nil || op.OptionalTenantIDsInBody == nil {
			t.Fatalf("%s: spec metadata lacks tenant fields; re-bundle with camunda-schema-bundler >= 2.5.1", op.OperationID)
		}
		if *op.OptionalTenantIDInBody || len(*op.OptionalTenantIDInBodyVariants) > 0 || *op.OptionalTenantIDsInBody {
			out[op.OperationID] = true
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
		t.Fatal("found no tenant-scoped operations in the spec metadata")
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
	for _, want := range []string{"suspendProcessDefinitions", "resumeProcessDefinitions", "publishMessage", "evaluateDecision", "activateJobs", "createDeployment"} {
		if !ops[want] {
			t.Errorf("spec metadata missed %s", want)
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
