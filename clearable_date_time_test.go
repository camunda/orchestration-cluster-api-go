package camunda_test

import (
	"encoding/json"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go/v11"
)

func TestClearableDateTimeDecodeNullResetsPopulatedValue(t *testing.T) {
	v := camunda.DateTime(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	if err := json.Unmarshal([]byte("null"), &v); err != nil {
		t.Fatal(err)
	}
	if v.String != nil || v.TimeTime != nil {
		t.Fatalf("null left stale value: %+v", v)
	}
}

func TestClearableDateTimeDecodeStringOverDateResets(t *testing.T) {
	v := camunda.DateTime(time.Now())
	if err := json.Unmarshal([]byte(`""`), &v); err != nil {
		t.Fatal(err)
	}
	if v.TimeTime != nil || v.String == nil || *v.String != "" {
		t.Fatalf("clear decode wrong: %+v", v)
	}
	if err := json.Unmarshal([]byte(`"2030-01-02T03:04:05Z"`), &v); err != nil {
		t.Fatal(err)
	}
	if v.String != nil || v.TimeTime == nil {
		t.Fatalf("date decode wrong: %+v", v)
	}
}

func TestClearableDateTimeRejectsInvalid(t *testing.T) {
	for _, in := range []string{`"not-a-date"`, `"2030-13-01"`, `42`, `{}`, `true`} {
		var v camunda.ClearableDateTime
		if err := json.Unmarshal([]byte(in), &v); err == nil {
			t.Errorf("%s: expected error, got %+v", in, v)
		}
	}
}

func TestClearableDateTimeNullRoundTrip(t *testing.T) {
	var v camunda.ClearableDateTime
	if err := json.Unmarshal([]byte("null"), &v); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) != "null" {
		t.Fatalf("got %s, %v", b, err)
	}
	b, err = json.Marshal(camunda.ClearableDateTime{})
	if err != nil || string(b) != "null" {
		t.Fatalf("zero value: got %s, %v", b, err)
	}
}

func TestClearableDateTimeMarshalRejectsNonEmptyString(t *testing.T) {
	s := "nope"
	if _, err := json.Marshal(camunda.ClearableDateTime{String: &s}); err == nil {
		t.Fatal("expected error")
	}
}

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
}
