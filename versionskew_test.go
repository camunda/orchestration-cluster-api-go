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

// TestProcessDefinitionResultDecodesWithState guards the opposite skew
// direction: upstream `main` replaced ProcessDefinitionResult.isDeleted (bool)
// with the required state enum (ACTIVE|DELETED), and the strict generated
// decoder rejects any field the bundled spec does not declare — so a server
// running ahead of the bundled spec broke GetProcessDefinition outright
// (`json: unknown field "state"`). The payload mirrors a real
// /v2/process-definitions/{key} response from an 8.10-SNAPSHOT server.
func TestProcessDefinitionResultDecodesWithState(t *testing.T) {
	const realResponse = `{
  "name": "Demo Process",
  "resourceName": "greet.bpmn",
  "version": 1,
  "versionTag": "",
  "processDefinitionId": "demo-process",
  "tenantId": "<default>",
  "processDefinitionKey": "2251799813685330",
  "hasStartForm": false,
  "state": "ACTIVE"
}`

	var result openapi.ProcessDefinitionResult
	if err := json.Unmarshal([]byte(realResponse), &result); err != nil {
		t.Fatalf("a process-definition response must decode: %v", err)
	}
	if got := result.GetProcessDefinitionId(); got != "demo-process" {
		t.Errorf("processDefinitionId = %q, want demo-process", got)
	}
	if got := result.GetState(); got != "ACTIVE" {
		t.Errorf("state = %q, want ACTIVE", got)
	}
}

// TestProcessInstanceResultDecodesWithoutSuspendedDate guards suspendedDate, a
// field added in 8.10 and required on ProcessInstanceResult in the bundled spec
// (which tracks camunda `main`) but not emitted by the pinned integration server
// (8.10.0-alpha3). Relaxed by the version-skew hook. The payload mirrors a real
// /v2/process-instances/{key} response with suspendedDate absent.
func TestProcessInstanceResultDecodesWithoutSuspendedDate(t *testing.T) {
	const realResponse = `{
  "processDefinitionId": "demo-process",
  "processDefinitionName": "Demo Process",
  "processDefinitionVersion": 1,
  "processDefinitionVersionTag": null,
  "startDate": "2026-07-24T10:00:00.000Z",
  "endDate": null,
  "state": "ACTIVE",
  "hasIncident": false,
  "tenantId": "<default>",
  "processInstanceKey": "2251799813685340",
  "processDefinitionKey": "2251799813685330",
  "parentProcessInstanceKey": null,
  "parentElementInstanceKey": null,
  "rootProcessInstanceKey": null,
  "tags": []
}`

	var result openapi.ProcessInstanceResult
	if err := json.Unmarshal([]byte(realResponse), &result); err != nil {
		t.Fatalf("a process-instance response must decode despite a missing spec-required suspendedDate: %v", err)
	}
	if got := result.GetProcessDefinitionId(); got != "demo-process" {
		t.Errorf("processDefinitionId = %q, want demo-process", got)
	}
	if result.SuspendedDate.IsSet() {
		t.Error("omitted suspendedDate should be unset")
	}
}
