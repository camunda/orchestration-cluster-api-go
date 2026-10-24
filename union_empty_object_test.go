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
// filter-property unions to accept exactly `{}` as an unset wrapper (and to
// marshal the unset state as `null` rather than failing), while every other
// no-match input keeps the strict oneOf error. This test scopes itself to the
// whole defect class — every filter-property union listed in the spec
// metadata — not just the two examples from the issue.
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

	t.Run("whitespace-only empty objects decode as unset", func(t *testing.T) {
		// Pretty-printed or hand-formatted stored queries carry empty objects
		// with internal or surrounding whitespace; they are the same JSON
		// value as `{}` and must decode the same way.
		variants := []string{
			`{ }`,
			"{\n\t}",
			"{\n  \n}",
			" { } ",
			"{\r\n}",
		}
		for _, data := range variants {
			var v camundaapi.BasicStringFilterProperty
			if err := json.Unmarshal([]byte(data), &v); err != nil {
				t.Errorf("unmarshal %q into BasicStringFilterProperty: %v", data, err)
			}
		}
	})

	t.Run("null decodes as unset and round-trips", func(t *testing.T) {
		// The unset wrapper marshals to `null` (see below), so the decoder
		// must accept its own output: `null` unmarshals into every pointer
		// branch and the generated code would otherwise reject it with
		// "data matches more than one schema".
		for _, data := range []string{`null`, ` null `} {
			var v camundaapi.BasicStringFilterProperty
			if err := json.Unmarshal([]byte(data), &v); err != nil {
				t.Errorf("unmarshal %q into BasicStringFilterProperty: %v", data, err)
			}
			if got := v.GetActualInstance(); got != nil {
				t.Errorf("unmarshal %q: GetActualInstance() = %v, want nil (wrapper treated as unset)", data, got)
			}
		}
	})

	t.Run("replace semantics: unset or scalar/advanced decode clears a populated wrapper", func(t *testing.T) {
		// UnmarshalJSON has replace semantics: decoding over a previously
		// populated wrapper must never retain a stale branch that would then
		// be serialized again. Covers both pointer branch types (scalar and
		// advanced) being cleared/replaced (orchestration-cluster-api-go#78).
		populate := map[string]string{
			"scalar":   `"hello"`,
			"advanced": `{"$eq":"foo"}`,
		}
		for branch, seed := range populate {
			branch, seed := branch, seed
			t.Run("decode null over "+branch, func(t *testing.T) {
				var v camundaapi.BasicStringFilterProperty
				if err := json.Unmarshal([]byte(seed), &v); err != nil {
					t.Fatalf("seed unmarshal %s: %v", seed, err)
				}
				if v.GetActualInstance() == nil {
					t.Fatalf("precondition: %s seed did not populate the wrapper", branch)
				}
				if err := json.Unmarshal([]byte(`null`), &v); err != nil {
					t.Fatalf("unmarshal `null` over %s: %v", branch, err)
				}
				if got := v.GetActualInstance(); got != nil {
					t.Errorf("after null over %s: GetActualInstance() = %v, want nil", branch, got)
				}
				if out, _ := json.Marshal(v); string(out) != "null" {
					t.Errorf("after null over %s: marshal = %s, want null (stale branch retained)", branch, out)
				}
			})
			t.Run("decode {} over "+branch, func(t *testing.T) {
				var v camundaapi.BasicStringFilterProperty
				if err := json.Unmarshal([]byte(seed), &v); err != nil {
					t.Fatalf("seed unmarshal %s: %v", seed, err)
				}
				if err := json.Unmarshal([]byte(`{}`), &v); err != nil {
					t.Fatalf("unmarshal `{}` over %s: %v", branch, err)
				}
				if got := v.GetActualInstance(); got != nil {
					t.Errorf("after {} over %s: GetActualInstance() = %v, want nil", branch, got)
				}
				if out, _ := json.Marshal(v); string(out) != "null" {
					t.Errorf("after {} over %s: marshal = %s, want null (stale branch retained)", branch, out)
				}
			})
		}
		t.Run("decode advanced over scalar and back", func(t *testing.T) {
			var v camundaapi.BasicStringFilterProperty
			if err := json.Unmarshal([]byte(`"hello"`), &v); err != nil {
				t.Fatalf("seed scalar: %v", err)
			}
			if err := json.Unmarshal([]byte(`{"$eq":"foo"}`), &v); err != nil {
				t.Fatalf("decode advanced over scalar: %v", err)
			}
			if v.String != nil {
				t.Errorf("stale scalar branch retained after decoding advanced: %v", *v.String)
			}
			out, _ := json.Marshal(v)
			if string(out) != `{"$eq":"foo"}` {
				t.Errorf("advanced over scalar marshal = %s, want {\"$eq\":\"foo\"}", out)
			}
		})
	})

	t.Run("decode-marshal-decode round trip", func(t *testing.T) {
		// The full cycle from the issue plus the serialized unset state:
		// decode `{}`, marshal (emits `null`), decode again — the second
		// decode must succeed and stay unset.
		var v camundaapi.BasicStringFilterProperty
		if err := json.Unmarshal([]byte(`{}`), &v); err != nil {
			t.Fatalf("unmarshal `{}`: %v", err)
		}
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var w camundaapi.BasicStringFilterProperty
		if err := json.Unmarshal(out, &w); err != nil {
			t.Fatalf("re-unmarshal %s: %v", out, err)
		}
		if got := w.GetActualInstance(); got != nil {
			t.Errorf("after round trip: GetActualInstance() = %v, want nil", got)
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
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal unset wrapper: %v", err)
		}
		if string(out) != "null" {
			t.Errorf("marshal unset wrapper = %s, want null", out)
		}
	})

	t.Run("empty-object filter marshals inside a request", func(t *testing.T) {
		// The full round trip from the issue: decode a stored query carrying an
		// empty filter, then marshal the request again. The unset union must not
		// fail encoding/json with "unexpected end of JSON input".
		var req struct {
			Filter struct {
				Name *camundaapi.BasicStringFilterProperty `json:"name,omitempty"`
			} `json:"filter"`
		}
		if err := json.Unmarshal([]byte(`{"filter":{"name":{}}}`), &req); err != nil {
			t.Fatalf(`unmarshal {"filter":{"name":{}}}: %v`, err)
		}
		if _, err := json.Marshal(req); err != nil {
			t.Fatalf("marshal request with empty-object filter: %v", err)
		}
	})

	t.Run("invalid values are still rejected", func(t *testing.T) {
		// Accepting `{}` must not turn the no-match branch into a fail-open:
		// inputs that match no branch are invalid filter values and must keep
		// the strict oneOf error instead of being silently discarded.
		invalid := map[string]string{
			"boolean":              `true`,
			"number":               `42`,
			"array":                `[]`,
			"bad operator type":    `{"$eq":42}`,
			"unknown operator":     `{"$nope":"x"}`,
			"empty array with ws":  ` [ ] `,
			"null-adjacent scalar": `0`,
		}
		for name, data := range invalid {
			var v camundaapi.BasicStringFilterProperty
			if err := json.Unmarshal([]byte(data), &v); err == nil {
				t.Errorf("unmarshal %s (%s) into BasicStringFilterProperty: expected error, got nil", name, data)
			}
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

	t.Run("non-filter oneOf unions still reject null", func(t *testing.T) {
		// The null short-circuit is scoped to the filter-property unions
		// (whose unset state marshals to `null` and must round-trip). Other
		// unions keep the generated behaviour: `null` matches every pointer
		// branch and is rejected as ambiguous.
		strict := map[string]json.Unmarshaler{
			"ProcessInstanceCreationInstruction": &camundaapi.ProcessInstanceCreationInstruction{},
			"DecisionEvaluationInstruction":      &camundaapi.DecisionEvaluationInstruction{},
			"AuthorizationRequest":               &camundaapi.AuthorizationRequest{},
		}
		for name, v := range strict {
			if err := json.Unmarshal([]byte(`null`), v); err == nil {
				t.Errorf("unmarshal `null` into %s: expected error, got nil", name)
			}
		}
	})
}
