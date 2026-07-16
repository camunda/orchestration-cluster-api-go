package camunda_test

import (
	"encoding/json"
	"testing"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

// TestActivatedJobDecodesWithoutPhysicalTenantId is a regression guard for
// issue #3. Real 8.9/8.10 servers never emit the spec-required physicalTenantId
// field, so the generated strict decoder must tolerate its absence (relaxed by
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
