package camunda_test

import (
	"encoding/json"
	"testing"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

// TestActivatedJobDecodesWithoutPhysicalTenantId is a regression guard for
// issue #3. The bundled spec (which tracks camunda `main`) requires
// physicalTenantId, but the pinned integration server (8.10.0-alpha3) does not
// emit it, so the generated strict decoder must tolerate its absence (relaxed by
// the version-skew post-processing hook). Guarding the defect class: a
// spec-required-but-server-omitted field must not break decoding.
//
// The payload is copied verbatim from a live /v2/jobs/activation response, with
// physicalTenantId absent exactly as the server sends it.
func TestActivatedJobDecodesWithMissingVersionSkewFields(t *testing.T) {
	const realResponse = `{
  "jobs": [
    {
      "type": "check-inventory",
      "processDefinitionId": "order-process",
      "processDefinitionVersion": 1,
      "elementId": "CheckInventory",
      "customHeaders": {},
      "worker": "check-inventory-worker",
      "retries": 3,
      "deadline": 1784256664927,
      "variables": {"item": "Widget", "quantity": 3},
      "tenantId": "<default>",
      "jobKey": "2251799813685424",
      "processInstanceKey": "2251799813685417",
      "processDefinitionKey": "2251799813685416",
      "elementInstanceKey": "2251799813685423",
      "kind": "BPMN_ELEMENT",
      "listenerEventType": "UNSPECIFIED",
      "userTask": null,
      "tags": [],
      "rootProcessInstanceKey": "2251799813685417",
      "businessId": null,
      "priority": 0
    }
  ]
}`

	var result openapi.JobActivationResult
	if err := json.Unmarshal([]byte(realResponse), &result); err != nil {
		t.Fatalf("a real activate-jobs response must decode despite a missing spec-required field: %v", err)
	}
	jobs := result.GetJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].GetType() != "check-inventory" {
		t.Errorf("job type = %q, want check-inventory", jobs[0].GetType())
	}
	if jobs[0].GetPhysicalTenantId() != "" {
		t.Errorf("omitted physicalTenantId should default to empty, got %q", jobs[0].GetPhysicalTenantId())
	}
}

// TestDeploymentMetadataDecodesWithOnlyProcessDefinition guards the deployment
// response defect class: DeploymentMetadataResult declares processDefinition,
// decisionDefinition, decisionRequirements, form, and resource all required, but
// a deployment populates only the members matching the deployed resource kinds.
// A BPMN-only deployment therefore omits every key except processDefinition, and
// the strict all-required decoder rejected it until hook_05 relaxed the check
// (scoped to this model). The payload mirrors a real /v2/deployments response
// item for a single deployed process.
func TestDeploymentMetadataDecodesWithOnlyProcessDefinition(t *testing.T) {
	const realItem = `{
  "processDefinition": {
    "processDefinitionId": "demo-process",
    "processDefinitionVersion": 1,
    "resourceName": "greet.bpmn",
    "tenantId": "<default>",
    "processDefinitionKey": "2251799813685249"
  }
}`

	var meta openapi.DeploymentMetadataResult
	if err := json.Unmarshal([]byte(realItem), &meta); err != nil {
		t.Fatalf("a BPMN-only deployment item must decode despite the other union members being absent: %v", err)
	}
	proc := meta.GetProcessDefinition()
	if got := proc.GetProcessDefinitionId(); got != "demo-process" {
		t.Errorf("processDefinitionId = %q, want demo-process", got)
	}
	if meta.DecisionDefinition.IsSet() {
		t.Error("decisionDefinition should be unset for a BPMN-only deployment")
	}
	if meta.Form.IsSet() {
		t.Error("form should be unset for a BPMN-only deployment")
	}
}

// TestCreateProcessInstanceResultDecodesWithoutBusinessId guards businessId, a
// field required in the bundled spec (which tracks camunda `main`) but observed
// absent from release-server responses during integration testing. Relaxed
// globally by the version-skew hook across the result schemas that declare it.
// The payload mirrors a real /v2/process-instances create response with
// businessId absent.
func TestCreateProcessInstanceResultDecodesWithoutBusinessId(t *testing.T) {
	const realResponse = `{
  "processDefinitionId": "demo-process",
  "processDefinitionVersion": 1,
  "tenantId": "<default>",
  "variables": {"name": "REST"},
  "processDefinitionKey": "2251799813685249",
  "processInstanceKey": "2251799813685260",
  "tags": []
}`

	var result openapi.CreateProcessInstanceResult
	if err := json.Unmarshal([]byte(realResponse), &result); err != nil {
		t.Fatalf("a create-process-instance response must decode despite a missing spec-required businessId: %v", err)
	}
	if got := result.GetProcessDefinitionId(); got != "demo-process" {
		t.Errorf("processDefinitionId = %q, want demo-process", got)
	}
	if result.BusinessId.IsSet() {
		t.Error("omitted businessId should be unset")
	}
}

// TestProcessDefinitionResultDecodesWithoutIsDeleted guards isDeleted, a field
// required in the bundled spec (which tracks camunda `main`) but not emitted by
// the pinned integration server (8.10.0-alpha3) on ProcessDefinitionResult.
// Relaxed by the version-skew hook. The payload mirrors a real
// /v2/process-definitions/{key} response with isDeleted absent.
func TestProcessDefinitionResultDecodesWithoutIsDeleted(t *testing.T) {
	const realResponse = `{
  "name": "Demo Process",
  "resourceName": "greet.bpmn",
  "version": 1,
  "versionTag": "",
  "processDefinitionId": "demo-process",
  "tenantId": "<default>",
  "processDefinitionKey": "2251799813685330",
  "hasStartForm": false
}`

	var result openapi.ProcessDefinitionResult
	if err := json.Unmarshal([]byte(realResponse), &result); err != nil {
		t.Fatalf("a process-definition response must decode despite a missing spec-required isDeleted: %v", err)
	}
	if got := result.GetProcessDefinitionId(); got != "demo-process" {
		t.Errorf("processDefinitionId = %q, want demo-process", got)
	}
}
