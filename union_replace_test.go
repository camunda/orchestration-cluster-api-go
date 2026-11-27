package camunda_test

import (
	"encoding/json"
	"testing"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
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
		var a camundaapi.ProbeAnswerRequest
		if err := json.Unmarshal([]byte(`{"outcome":"ERROR","message":"x"}`), &a); err != nil {
			t.Fatalf("decode ERROR: %v", err)
		}
		if err := json.Unmarshal([]byte(`{"outcome":"OK","data":{}}`), &a); err != nil {
			t.Fatalf("decode OK: %v", err)
		}
		if a.ProbeErrorAnswerRequest != nil {
			t.Errorf("stale ProbeErrorAnswerRequest branch survived a later decode")
		}
		if a.ProbeOkAnswerRequest == nil {
			t.Fatalf("ProbeOkAnswerRequest branch not set")
		}
		out, err := json.Marshal(a)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got struct {
			Outcome string `json:"outcome"`
		}
		if err := json.Unmarshal(out, &got); err != nil || got.Outcome != "OK" {
			t.Errorf("marshal sent %s, want the OK outcome", out)
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
		var a camundaapi.ProbeAnswerRequest
		if err := json.Unmarshal([]byte(`{"outcome":"OK","data":{}}`), &a); err != nil {
			t.Fatalf("decode OK: %v", err)
		}
		if err := json.Unmarshal([]byte(`{"outcome":"OK","data":5}`), &a); err == nil {
			t.Fatalf("expected an error decoding an invalid OK payload")
		}
		if a.ProbeOkAnswerRequest != nil {
			t.Errorf("stale branch survived a failed decode")
		}
	})
}
