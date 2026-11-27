package camunda_test

import (
	"encoding/json"
	"testing"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v10/client"
)

// TestOneOfWrapperReplacesPreviousDecode is the regression guard for
// https://github.com/camunda/orchestration-cluster-api-go/issues/238.
//
// The stock openapi-generator oneOf UnmarshalJSON writes into the receiver's
// existing branch pointers and never clears the others, so decoding two
// different variants into the same value left both populated and MarshalJSON
// sent the stale one. Hook 02c zeroes the receiver first.
func TestOneOfWrapperReplacesPreviousDecode(t *testing.T) {
	t.Run("discriminated union", func(t *testing.T) {
		var a camundaapi.JobResult
		if err := json.Unmarshal([]byte(`{"type":"adHocSubProcess"}`), &a); err != nil {
			t.Fatalf("decode adHocSubProcess: %v", err)
		}
		if err := json.Unmarshal([]byte(`{"type":"userTask"}`), &a); err != nil {
			t.Fatalf("decode userTask: %v", err)
		}
		if a.JobResultAdHocSubProcess != nil {
			t.Errorf("stale JobResultAdHocSubProcess branch survived a later decode")
		}
		if a.JobResultUserTask == nil {
			t.Fatalf("JobResultUserTask branch not set")
		}
		out, err := json.Marshal(a)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(out, &got); err != nil || got.Type != "userTask" {
			t.Errorf("marshal sent %s, want the userTask result", out)
		}
	})

	t.Run("undiscriminated union", func(t *testing.T) {
		var p camundaapi.SearchQueryPageRequest
		if err := json.Unmarshal([]byte(`{"from":5}`), &p); err != nil {
			t.Fatalf("decode offset page: %v", err)
		}
		if p.OffsetPagination == nil {
			t.Fatalf("OffsetPagination branch not set")
		}
		if err := json.Unmarshal([]byte(`{"after":"abc"}`), &p); err != nil {
			t.Fatalf("decode cursor page: %v", err)
		}
		if p.OffsetPagination != nil {
			t.Errorf("stale OffsetPagination branch survived a later decode")
		}
		if p.CursorForwardPagination == nil {
			t.Errorf("CursorForwardPagination branch not set")
		}
	})

	t.Run("a failed decode does not keep the earlier value", func(t *testing.T) {
		// The earlier value is a different branch from the one the failing
		// payload selects, so only the receiver reset can clear it.
		var a camundaapi.JobResult
		if err := json.Unmarshal([]byte(`{"type":"adHocSubProcess"}`), &a); err != nil {
			t.Fatalf("decode adHocSubProcess: %v", err)
		}
		if err := json.Unmarshal([]byte(`{"type":"userTask","denied":"x"}`), &a); err == nil {
			t.Fatalf("expected an error decoding an invalid userTask payload")
		}
		if a.JobResultAdHocSubProcess != nil || a.JobResultUserTask != nil {
			t.Errorf("wrapper kept a branch after a failed decode: %+v", a)
		}
	})
}
