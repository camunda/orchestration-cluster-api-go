package camunda_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	camunda "github.com/camunda/orchestration-cluster-api-go/v11"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
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
