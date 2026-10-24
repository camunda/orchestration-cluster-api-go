package camunda_test

import (
	"encoding/json"
	"testing"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
)

// TestOneOfFilterPropertyAcceptsEmptyObject is the regression guard for
// https://github.com/camunda/orchestration-cluster-api-go/issues/78.
//
// `{}` is a valid instance of the advanced-filter variant of every
// *FilterProperty oneOf union (all of its properties are optional), and the
// JS/Python/C#/PHP SDKs accept and send it. openapi-generator's oneOf
// UnmarshalJSON nils out an empty-struct match and then errors with
// "data failed to match schemas in oneOf(...)", so users decoding stored or
// forwarded query JSON into the Go request types could not round-trip it.
//
// The post-processing pipeline rewrites the generated no-match branch of the
// filter-property unions to leave the wrapper unset instead of erroring. This
// test scopes itself to the whole defect class — every filter-property union
// listed in the spec metadata — not just the two examples from the issue.
func TestOneOfFilterPropertyAcceptsEmptyObject(t *testing.T) {
	t.Run("issue example: BasicStringFilterProperty", func(t *testing.T) {
		var v camundaapi.BasicStringFilterProperty
		if err := json.Unmarshal([]byte(`{}`), &v); err != nil {
			t.Fatalf("unmarshal `{}` into BasicStringFilterProperty: %v", err)
		}
	})

	t.Run("issue example: IntegerFilterProperty", func(t *testing.T) {
		var v camundaapi.IntegerFilterProperty
		if err := json.Unmarshal([]byte(`{}`), &v); err != nil {
			t.Fatalf("unmarshal `{}` into IntegerFilterProperty: %v", err)
		}
	})

	t.Run("issue example: nested filter property", func(t *testing.T) {
		var req struct {
			Filter struct {
				Name *camundaapi.BasicStringFilterProperty `json:"name,omitempty"`
			} `json:"filter"`
		}
		if err := json.Unmarshal([]byte(`{"filter":{"name":{}}}`), &req); err != nil {
			t.Fatalf(`unmarshal {"filter":{"name":{}}}: %v`, err)
		}
	})

	t.Run("defect class: every filter-property union", func(t *testing.T) {
		cases := map[string]json.Unmarshaler{
			"BasicStringFilterProperty":          &camundaapi.BasicStringFilterProperty{},
			"StringFilterProperty":               &camundaapi.StringFilterProperty{},
			"IntegerFilterProperty":              &camundaapi.IntegerFilterProperty{},
			"DateTimeFilterProperty":             &camundaapi.DateTimeFilterProperty{},
			"JobKeyFilterProperty":               &camundaapi.JobKeyFilterProperty{},
			"ProcessInstanceKeyFilterProperty":   &camundaapi.ProcessInstanceKeyFilterProperty{},
			"ProcessInstanceStateFilterProperty": &camundaapi.ProcessInstanceStateFilterProperty{},
			"UserTaskStateFilterProperty":        &camundaapi.UserTaskStateFilterProperty{},
			"IncidentStateFilterProperty":        &camundaapi.IncidentStateFilterProperty{},
			"ElementIdFilterProperty":            &camundaapi.ElementIdFilterProperty{},
		}
		for name, v := range cases {
			if err := json.Unmarshal([]byte(`{}`), v); err != nil {
				t.Errorf("unmarshal `{}` into %s: %v", name, err)
			}
		}
	})

	t.Run("unset wrapper marshals back to null", func(t *testing.T) {
		var v camundaapi.BasicStringFilterProperty
		if err := json.Unmarshal([]byte(`{}`), &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := v.GetActualInstance(); got != nil {
			t.Errorf("GetActualInstance() = %v, want nil (wrapper treated as unset)", got)
		}
	})

	t.Run("non-empty values still match their variant", func(t *testing.T) {
		var s camundaapi.BasicStringFilterProperty
		if err := json.Unmarshal([]byte(`"hello"`), &s); err != nil {
			t.Fatalf(`unmarshal "hello": %v`, err)
		}
		if s.String == nil || *s.String != "hello" {
			t.Errorf("String = %v, want \"hello\"", s.String)
		}

		var adv camundaapi.StringFilterProperty
		if err := json.Unmarshal([]byte(`{"$eq":"foo"}`), &adv); err != nil {
			t.Fatalf(`unmarshal {"$eq":"foo"}: %v`, err)
		}
		if adv.AdvancedStringFilter == nil || adv.AdvancedStringFilter.Eq == nil || *adv.AdvancedStringFilter.Eq != "foo" {
			t.Errorf("AdvancedStringFilter.Eq = %v, want \"foo\"", adv.AdvancedStringFilter)
		}
	})

	t.Run("non-filter oneOf unions stay strict", func(t *testing.T) {
		// These unions have no all-optional branch, so `{}` matches nothing
		// and must still be rejected.
		strict := map[string]json.Unmarshaler{
			"ProcessInstanceCreationInstruction": &camundaapi.ProcessInstanceCreationInstruction{},
			"DecisionEvaluationInstruction":      &camundaapi.DecisionEvaluationInstruction{},
			"AuthorizationRequest":               &camundaapi.AuthorizationRequest{},
		}
		for name, v := range strict {
			if err := json.Unmarshal([]byte(`{}`), v); err == nil {
				t.Errorf("unmarshal `{}` into %s: expected error, got nil", name)
			}
		}
	})
}
