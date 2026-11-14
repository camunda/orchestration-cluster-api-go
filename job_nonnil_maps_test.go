package camunda

import (
	"testing"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
	"github.com/camunda/orchestration-cluster-api-go/v11/pb"
)

// TestJobMapsAreNeverNil pins the contract that RawVariables() and CustomHeaders()
// return a non-nil map on every transport and for every "no content" payload shape,
// so a handler that writes to the map never panics with "assignment to entry in nil
// map". It is scoped to the defect class (every constructor x every empty shape),
// not to the single nil case first observed.
func TestJobMapsAreNeverNil(t *testing.T) {
	// gRPC carries variables and custom headers as JSON strings.
	grpcShapes := []struct{ name, payload string }{
		{"absent", ""},
		{"null", "null"},
		{"empty object", "{}"},
		{"malformed", "{"},
		{"not json", "not json"},
		{"array", "[1,2,3]"},
		{"scalar", "42"},
	}
	for _, sh := range grpcShapes {
		t.Run("grpc/"+sh.name, func(t *testing.T) {
			j := newGRPCJob(&pb.ActivatedJob{Key: 1, Type: "t", Variables: sh.payload, CustomHeaders: sh.payload}, LiveClock{})
			assertWritable(t, "RawVariables", j.RawVariables())
			assertWritable(t, "CustomHeaders", j.CustomHeaders())
		})
	}

	// REST decodes into the generated model, whose maps are nil when absent or null.
	restShapes := []struct {
		name string
		mut  func(*camundaapi.ActivatedJobResult)
	}{
		{"zero value (absent)", func(*camundaapi.ActivatedJobResult) {}},
		{"explicit nil", func(aj *camundaapi.ActivatedJobResult) { aj.Variables, aj.CustomHeaders = nil, nil }},
		{"empty object", func(aj *camundaapi.ActivatedJobResult) {
			aj.Variables, aj.CustomHeaders = map[string]any{}, map[string]any{}
		}},
	}
	for _, sh := range restShapes {
		t.Run("rest/"+sh.name, func(t *testing.T) {
			var aj camundaapi.ActivatedJobResult
			sh.mut(&aj)
			j := newRESTJob(aj, LiveClock{})
			assertWritable(t, "RawVariables", j.RawVariables())
			assertWritable(t, "CustomHeaders", j.CustomHeaders())
		})
	}
}

// assertWritable fails if m is nil or writing to it panics.
func assertWritable(t *testing.T, what string, m map[string]any) {
	t.Helper()
	if m == nil {
		t.Errorf("%s() = nil map, want a non-nil empty map", what)
		return
	}
	if len(m) != 0 {
		t.Errorf("%s() = %v, want empty", what, m)
	}
	m["handler-wrote"] = true // must not panic
}

// TestJobMapsKeepDecodedContent guards the normalisation against over-reaching:
// real content must still come through untouched.
func TestJobMapsKeepDecodedContent(t *testing.T) {
	g := newGRPCJob(&pb.ActivatedJob{Key: 1, Variables: `{"a":1}`, CustomHeaders: `{"h":"v"}`}, LiveClock{})
	if g.RawVariables()["a"] != float64(1) || g.CustomHeaders()["h"] != "v" {
		t.Errorf("grpc content lost: %v %v", g.RawVariables(), g.CustomHeaders())
	}
	r := newRESTJob(camundaapi.ActivatedJobResult{Variables: map[string]any{"a": 1}, CustomHeaders: map[string]any{"h": "v"}}, LiveClock{})
	if r.RawVariables()["a"] != 1 || r.CustomHeaders()["h"] != "v" {
		t.Errorf("rest content lost: %v %v", r.RawVariables(), r.CustomHeaders())
	}
}
