package camunda_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	camunda "github.com/camunda/orchestration-cluster-api-go/v10"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/v10/client"
)

// bodyCapture spins up a server that records the raw request body (and, for
// multipart requests, the tenantId form field) of a single call.
type bodyCapture struct {
	srv         *httptest.Server
	body        map[string]any
	formTenant  string
	contentType string
}

func newBodyCapture(t *testing.T) *bodyCapture {
	t.Helper()
	bc := &bodyCapture{}
	bc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bc.contentType = r.Header.Get("Content-Type")
		if strings.HasPrefix(bc.contentType, "multipart/form-data") {
			if err := r.ParseMultipartForm(1 << 20); err == nil {
				bc.formTenant = r.FormValue("tenantId")
			}
		} else {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &bc.body)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(bc.srv.Close)
	return bc
}

func (bc *bodyCapture) client(t *testing.T, opts ...camunda.Option) *camunda.CamundaClient {
	t.Helper()
	client, err := camunda.New(append([]camunda.Option{
		camunda.WithRestAddress(bc.srv.URL),
		camunda.WithLogLevel(camunda.LogOff),
	}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// tenantOf extracts the tenantId the request body carried ("" when absent).
func (bc *bodyCapture) tenantOf() string {
	if bc.body == nil {
		return ""
	}
	if v, ok := bc.body["tenantId"].(string); ok {
		return v
	}
	return ""
}

// TestDefaultTenantInjectedIntoRequestBodies pins the conformance contract of
// issue #79: WithDefaultTenantID / CAMUNDA_DEFAULT_TENANT_ID must reach the
// optional tenantId field of every operation that accepts one, matching the JS,
// Python and C# SDKs.
func TestDefaultTenantInjectedIntoRequestBodies(t *testing.T) {
	t.Run("PublishMessage", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		body := camundaapi.NewMessagePublicationRequest("msg")
		_, _ = client.PublishMessage(context.Background(), *body)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q", got, "acme")
		}
	})

	t.Run("CorrelateMessage", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		body := camundaapi.NewMessageCorrelationRequest("msg")
		_, _ = client.CorrelateMessage(context.Background(), *body)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q", got, "acme")
		}
	})

	t.Run("BroadcastSignal", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		body := camundaapi.NewSignalBroadcastRequest("sig")
		_, _ = client.BroadcastSignal(context.Background(), *body)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q", got, "acme")
		}
	})

	t.Run("EvaluateConditionals", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		body := camundaapi.NewConditionalEvaluationInstruction(map[string]interface{}{"x": 1})
		_, _ = client.EvaluateConditionals(context.Background(), *body)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q", got, "acme")
		}
	})

	t.Run("EvaluateExpression", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		body := camundaapi.NewExpressionEvaluationRequest("=1+1")
		_, _ = client.EvaluateExpression(context.Background(), *body)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q", got, "acme")
		}
	})

	t.Run("EvaluateDecisionById", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		byID := camundaapi.NewDecisionEvaluationById(camundaapi.MustDecisionDefinitionId("dec"))
		instruction := camundaapi.DecisionEvaluationByIdAsDecisionEvaluationInstruction(byID)
		_, _ = client.EvaluateDecision(context.Background(), instruction)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q", got, "acme")
		}
	})

	t.Run("EvaluateDecisionByKey", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		byKey := camundaapi.NewDecisionEvaluationByKey(camundaapi.MustDecisionDefinitionKey("2251799813685360"))
		instruction := camundaapi.DecisionEvaluationByKeyAsDecisionEvaluationInstruction(byKey)
		_, _ = client.EvaluateDecision(context.Background(), instruction)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q", got, "acme")
		}
	})

	t.Run("CreateProcessInstanceById", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"), camunda.WithFalcon(false))
		byID := camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.MustProcessDefinitionId("proc"))
		instruction := camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID)
		_, _ = client.CreateProcessInstance(context.Background(), instruction)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q", got, "acme")
		}
	})

	t.Run("CreateProcessInstanceByKey", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"), camunda.WithFalcon(false))
		byKey := camundaapi.NewProcessInstanceCreationInstructionByKey(camundaapi.MustProcessDefinitionKey("2251799813685360"))
		instruction := camundaapi.ProcessInstanceCreationInstructionByKeyAsProcessInstanceCreationInstruction(byKey)
		_, _ = client.CreateProcessInstance(context.Background(), instruction)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q", got, "acme")
		}
	})

	t.Run("CreateDeployment", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		_, _ = client.CreateDeployment(context.Background(),
			func(r camundaapi.ApiCreateDeploymentRequest) camundaapi.ApiCreateDeploymentRequest {
				return r.Resources([]*os.File{bpmnFile(t)})
			})
		if got := bc.formTenant; got != "acme" {
			t.Errorf("tenantId form field = %q, want %q", got, "acme")
		}
	})
}

// bpmnFile writes a minimal BPMN resource to a temp file and returns it opened
// for reading, as CreateDeployment expects.
func bpmnFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "proc-*.bpmn")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("<definitions/>"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestDefaultTenantDoesNotOverrideExplicitTenant: a tenant the caller set on the
// request always wins over the configured default.
func TestDefaultTenantDoesNotOverrideExplicitTenant(t *testing.T) {
	t.Run("BodyField", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		body := camundaapi.NewMessagePublicationRequest("msg")
		body.SetTenantId(camundaapi.MustTenantId("explicit"))
		_, _ = client.PublishMessage(context.Background(), *body)
		if got := bc.tenantOf(); got != "explicit" {
			t.Errorf("tenantId = %q, want %q", got, "explicit")
		}
	})

	t.Run("DeploymentOpt", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		_, _ = client.CreateDeployment(context.Background(),
			func(r camundaapi.ApiCreateDeploymentRequest) camundaapi.ApiCreateDeploymentRequest {
				return r.Resources([]*os.File{bpmnFile(t)}).TenantId("explicit")
			})
		if got := bc.formTenant; got != "explicit" {
			t.Errorf("tenantId form field = %q, want %q", got, "explicit")
		}
	})
}

// TestNoDefaultTenantLeavesTenantUnset: with no configured default the request
// goes out without a tenantId, preserving the server-side default.
func TestNoDefaultTenantLeavesTenantUnset(t *testing.T) {
	bc := newBodyCapture(t)
	client := bc.client(t)
	body := camundaapi.NewMessagePublicationRequest("msg")
	_, _ = client.PublishMessage(context.Background(), *body)
	if _, present := bc.body["tenantId"]; present {
		t.Errorf("tenantId present (%v), want absent", bc.body["tenantId"])
	}
}

// TestDefaultTenantSurvivesBodyReplacingOpt: a body-builder opt that REPLACES
// the request body (r.<Body>(replacement)) must not discard the configured
// default tenant — the facade defaults the tenant on the final body, after the
// opts run, so the replacement still gets the default. This is the regression
// test for the "defaulting before opts" ordering bug.
func TestDefaultTenantSurvivesBodyReplacingOpt(t *testing.T) {
	// A body-replacing opt swaps in a fresh body whose tenant is unset; the
	// configured default must still be applied to that final body.
	t.Run("BodyReplacingOptGetsDefault", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		replacement := camundaapi.NewMessagePublicationRequest("replaced")
		_, _ = client.PublishMessage(context.Background(), *camundaapi.NewMessagePublicationRequest("original"),
			func(r camundaapi.ApiPublishMessageRequest) camundaapi.ApiPublishMessageRequest {
				return r.MessagePublicationRequest(*replacement)
			})
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q (default must apply to the opt-replaced body)", got, "acme")
		}
		// The replacement body is what was actually sent.
		if got, _ := bc.body["name"].(string); got != "replaced" {
			t.Errorf("body name = %q, want %q (the opt's replacement body)", got, "replaced")
		}
	})

	// An explicit tenant set on the replacement body always wins over the default.
	t.Run("BodyReplacingOptExplicitTenantWins", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		replacement := camundaapi.NewMessagePublicationRequest("replaced")
		replacement.SetTenantId(camundaapi.MustTenantId("explicit"))
		_, _ = client.PublishMessage(context.Background(), *camundaapi.NewMessagePublicationRequest("original"),
			func(r camundaapi.ApiPublishMessageRequest) camundaapi.ApiPublishMessageRequest {
				return r.MessagePublicationRequest(*replacement)
			})
		if got := bc.tenantOf(); got != "explicit" {
			t.Errorf("tenantId = %q, want %q (explicit tenant on the replaced body must win)", got, "explicit")
		}
	})

	// The same ordering holds on the hand-written REST path for
	// CreateProcessInstance (falcon.go): a body-replacing opt gets the default.
	t.Run("CreateProcessInstanceBodyReplacingOptGetsDefault", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"), camunda.WithFalcon(false))
		replacement := camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(
			camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.MustProcessDefinitionId("replaced-proc")))
		original := camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(
			camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.MustProcessDefinitionId("original-proc")))
		_, _ = client.CreateProcessInstance(context.Background(), original,
			func(r camundaapi.ApiCreateProcessInstanceRequest) camundaapi.ApiCreateProcessInstanceRequest {
				return r.ProcessInstanceCreationInstruction(replacement)
			})
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("tenantId = %q, want %q (default must apply to the opt-replaced body)", got, "acme")
		}
		if got, _ := bc.body["processDefinitionId"].(string); got != "replaced-proc" {
			t.Errorf("processDefinitionId = %q, want %q (the opt's replacement body)", got, "replaced-proc")
		}
	})
}

// TestDefaultTenantFromEnv: CAMUNDA_TENANT_ID (the cross-SDK variable) feeds the
// same injection path as WithDefaultTenantID.
func TestDefaultTenantFromEnv(t *testing.T) {
	t.Setenv("CAMUNDA_TENANT_ID", "env-tenant")
	bc := newBodyCapture(t)
	client := bc.client(t)
	body := camundaapi.NewSignalBroadcastRequest("sig")
	_, _ = client.BroadcastSignal(context.Background(), *body)
	if got := bc.tenantOf(); got != "env-tenant" {
		t.Errorf("tenantId = %q, want %q", got, "env-tenant")
	}
}

// TestDefaultTenantDoesNotMutateCallerInstruction: the decision and
// process-creation unions carry pointers, so injecting the default tenant must
// copy the variant — mutating it in place would leak the first client's tenant
// into every reuse of the caller's instruction (and race on concurrent reuse).
func TestDefaultTenantDoesNotMutateCallerInstruction(t *testing.T) {
	t.Run("DecisionEvaluationById", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		byID := camundaapi.NewDecisionEvaluationById(camundaapi.MustDecisionDefinitionId("dec"))
		instruction := camundaapi.DecisionEvaluationByIdAsDecisionEvaluationInstruction(byID)
		_, _ = client.EvaluateDecision(context.Background(), instruction)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("request tenantId = %q, want %q", got, "acme")
		}
		if byID.TenantId != nil {
			t.Errorf("caller's variant mutated: TenantId = %q, want nil", *byID.TenantId)
		}
	})

	t.Run("DecisionEvaluationByKey", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"))
		byKey := camundaapi.NewDecisionEvaluationByKey(camundaapi.MustDecisionDefinitionKey("2251799813685360"))
		instruction := camundaapi.DecisionEvaluationByKeyAsDecisionEvaluationInstruction(byKey)
		_, _ = client.EvaluateDecision(context.Background(), instruction)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("request tenantId = %q, want %q", got, "acme")
		}
		if byKey.TenantId != nil {
			t.Errorf("caller's variant mutated: TenantId = %q, want nil", *byKey.TenantId)
		}
	})

	t.Run("ProcessInstanceCreationById", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"), camunda.WithFalcon(false))
		byID := camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.MustProcessDefinitionId("proc"))
		instruction := camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID)
		_, _ = client.CreateProcessInstance(context.Background(), instruction)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("request tenantId = %q, want %q", got, "acme")
		}
		if byID.TenantId != nil {
			t.Errorf("caller's variant mutated: TenantId = %q, want nil", *byID.TenantId)
		}
	})

	t.Run("ProcessInstanceCreationByKey", func(t *testing.T) {
		bc := newBodyCapture(t)
		client := bc.client(t, camunda.WithDefaultTenantID("acme"), camunda.WithFalcon(false))
		byKey := camundaapi.NewProcessInstanceCreationInstructionByKey(camundaapi.MustProcessDefinitionKey("2251799813685360"))
		instruction := camundaapi.ProcessInstanceCreationInstructionByKeyAsProcessInstanceCreationInstruction(byKey)
		_, _ = client.CreateProcessInstance(context.Background(), instruction)
		if got := bc.tenantOf(); got != "acme" {
			t.Errorf("request tenantId = %q, want %q", got, "acme")
		}
		if byKey.TenantId != nil {
			t.Errorf("caller's variant mutated: TenantId = %q, want nil", *byKey.TenantId)
		}
	})

	// Reusing one instruction across two clients with different defaults must
	// send each client's tenant — only possible when the first call left the
	// caller's instruction untouched.
	t.Run("ReuseAcrossClients", func(t *testing.T) {
		bcA, bcB := newBodyCapture(t), newBodyCapture(t)
		clientA := bcA.client(t, camunda.WithDefaultTenantID("tenant-a"))
		clientB := bcB.client(t, camunda.WithDefaultTenantID("tenant-b"))
		byID := camundaapi.NewDecisionEvaluationById(camundaapi.MustDecisionDefinitionId("dec"))
		instruction := camundaapi.DecisionEvaluationByIdAsDecisionEvaluationInstruction(byID)
		_, _ = clientA.EvaluateDecision(context.Background(), instruction)
		_, _ = clientB.EvaluateDecision(context.Background(), instruction)
		if got := bcA.tenantOf(); got != "tenant-a" {
			t.Errorf("client A request tenantId = %q, want %q", got, "tenant-a")
		}
		if got := bcB.tenantOf(); got != "tenant-b" {
			t.Errorf("client B request tenantId = %q, want %q (first client's tenant leaked)", got, "tenant-b")
		}
	})
}

// TestInvalidDefaultTenantIDRejectedAtConfig: an invalid default tenant must be
// rejected with ErrConfig at configuration time — otherwise the first
// tenant-accepting operation panics in MustTenantId instead of returning an
// error.
func TestInvalidDefaultTenantIDRejectedAtConfig(t *testing.T) {
	t.Run("Option", func(t *testing.T) {
		bc := newBodyCapture(t)
		_, err := camunda.New(
			camunda.WithRestAddress(bc.srv.URL),
			camunda.WithLogLevel(camunda.LogOff),
			camunda.WithDefaultTenantID("acme/eu"),
		)
		if err == nil {
			t.Fatal("New with an invalid default tenant id should fail")
		}
		if !errors.Is(err, camunda.ErrConfig) {
			t.Errorf("error = %v, want it to wrap ErrConfig", err)
		}
	})

	t.Run("Environment", func(t *testing.T) {
		t.Setenv("CAMUNDA_DEFAULT_TENANT_ID", "acme/eu")
		bc := newBodyCapture(t)
		_, err := camunda.New(
			camunda.WithRestAddress(bc.srv.URL),
			camunda.WithLogLevel(camunda.LogOff),
		)
		if err == nil {
			t.Fatal("New with an invalid CAMUNDA_DEFAULT_TENANT_ID should fail")
		}
		if !errors.Is(err, camunda.ErrConfig) {
			t.Errorf("error = %v, want it to wrap ErrConfig", err)
		}
	})

	t.Run("ValidAndEmptyStillAccepted", func(t *testing.T) {
		bc := newBodyCapture(t)
		for _, id := range []string{"", "<default>", "acme", "acme-eu.1"} {
			if _, err := camunda.New(
				camunda.WithRestAddress(bc.srv.URL),
				camunda.WithLogLevel(camunda.LogOff),
				camunda.WithDefaultTenantID(id),
			); err != nil {
				t.Errorf("New(WithDefaultTenantID(%q)) = %v, want nil", id, err)
			}
		}
	})
}
