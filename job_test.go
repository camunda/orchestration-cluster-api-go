package camunda

import (
	"testing"

	"github.com/camunda/orchestration-cluster-api-go/pb"
)

// TestNewGRPCJobDecodesFields verifies that a gRPC ActivatedJob is normalized
// into a Job: int64 keys become decimal strings, the JSON-string variables and
// custom headers are decoded into maps, and the lease token is carried through.
func TestNewGRPCJobDecodesFields(t *testing.T) {
	lease := "lease-abc"
	aj := &pb.ActivatedJob{
		Key:                123,
		Type:               "demo",
		Retries:            5,
		ProcessInstanceKey: 456,
		ElementId:          "task-1",
		CustomHeaders:      `{"h":"v"}`,
		Variables:          `{"amount":42,"name":"x"}`,
		LeaseToken:         &lease,
	}
	j := newGRPCJob(aj)

	if j.Key() != "123" {
		t.Errorf("Key() = %q, want 123", j.Key())
	}
	if j.Type() != "demo" {
		t.Errorf("Type() = %q, want demo", j.Type())
	}
	if j.Retries() != 5 {
		t.Errorf("Retries() = %d, want 5", j.Retries())
	}
	if j.ProcessInstanceKey() != "456" {
		t.Errorf("ProcessInstanceKey() = %q, want 456", j.ProcessInstanceKey())
	}
	if j.ElementID() != "task-1" {
		t.Errorf("ElementID() = %q, want task-1", j.ElementID())
	}
	if j.LeaseToken() != "lease-abc" {
		t.Errorf("LeaseToken() = %q, want lease-abc", j.LeaseToken())
	}
	if got := j.CustomHeaders()["h"]; got != "v" {
		t.Errorf("CustomHeaders()[h] = %v, want v", got)
	}

	var v struct {
		Amount int    `json:"amount"`
		Name   string `json:"name"`
	}
	if err := j.Variables(&v); err != nil {
		t.Fatalf("Variables() error: %v", err)
	}
	if v.Amount != 42 || v.Name != "x" {
		t.Errorf("Variables() = %+v, want {Amount:42 Name:x}", v)
	}
}

// TestNewGRPCJobHandlesEmptyJSONStrings verifies that a job with empty
// variables/custom-header JSON strings yields usable empty maps rather than nil
// (which would make Variables/CustomHeaders access fragile).
func TestNewGRPCJobHandlesEmptyJSONStrings(t *testing.T) {
	j := newGRPCJob(&pb.ActivatedJob{Key: 1, Type: "demo"})

	if j.RawVariables() == nil || len(j.RawVariables()) != 0 {
		t.Errorf("RawVariables() = %v, want empty non-nil map", j.RawVariables())
	}
	if j.CustomHeaders() == nil || len(j.CustomHeaders()) != 0 {
		t.Errorf("CustomHeaders() = %v, want empty non-nil map", j.CustomHeaders())
	}
	if j.LeaseToken() != "" {
		t.Errorf("LeaseToken() = %q, want empty", j.LeaseToken())
	}

	var v map[string]any
	if err := j.Variables(&v); err != nil {
		t.Errorf("Variables() error: %v", err)
	}
}

// TestParseJSONObject verifies that malformed or empty JSON yields an empty map,
// never nil.
func TestParseJSONObject(t *testing.T) {
	for _, in := range []string{"", "not json", "[1,2,3]"} {
		if m := parseJSONObject(in); m == nil {
			t.Errorf("parseJSONObject(%q) = nil, want empty map", in)
		}
	}
	if m := parseJSONObject(`{"a":1}`); len(m) != 1 {
		t.Errorf("parseJSONObject valid = %v, want one entry", m)
	}
}
