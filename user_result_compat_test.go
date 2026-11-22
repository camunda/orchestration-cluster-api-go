package camunda_test

import (
	"encoding/json"
	"testing"

	camunda "github.com/camunda/orchestration-cluster-api-go/v11"
)

// Older servers omit isAnonymous; both user-result models must still decode.
func TestUserResultWithoutIsAnonymousDecodes(t *testing.T) {
	body := `{"username":"demo","displayName":"Demo","email":null,"authorizedComponents":[],"tenants":[],"groups":[],"roles":[],"salesPlanType":null,"c8Links":{},"canLogout":true}`
	var u camunda.CamundaUserResult
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatalf("CamundaUserResult: %v", err)
	}
	if u.IsAnonymous {
		t.Fatal("absent isAnonymous must decode as false")
	}
	var a camunda.AuthenticatedCamundaUserResult
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatalf("AuthenticatedCamundaUserResult: %v", err)
	}
	if a.IsAnonymous {
		t.Fatal("absent isAnonymous must decode as false")
	}
}
