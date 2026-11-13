package camunda

import (
	"strings"
	"testing"

	"github.com/camunda/orchestration-cluster-api-go/v11/pb"
)

// The trust model documented on Job makes two factual claims that could drift
// without anyone noticing: that a deeply nested payload is rejected rather than
// exhausting the stack, and that a malformed payload degrades to an empty map
// rather than panicking a handler goroutine. These tests pin both, so the
// documentation stays true rather than merely well-intentioned.

// TestDeeplyNestedVariablesAreRejectedNotFatal pins the documented nesting
// bound. A payload nested past encoding/json's limit must come back as an empty
// map, not take the worker goroutine down with it.
func TestDeeplyNestedVariablesAreRejectedNotFatal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		depth int
		want  int
	}{
		{"within the bound", 100, 1},
		{"past the bound", 20000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"v":` + strings.Repeat("[", tc.depth) + strings.Repeat("]", tc.depth) + `}`
			job := newGRPCJob(&pb.ActivatedJob{Variables: payload}, LiveClock{})
			if got := len(job.RawVariables()); got != tc.want {
				t.Errorf("RawVariables() has %d key(s), want %d", got, tc.want)
			}
		})
	}
}

// TestMalformedVariablesDecodeToNoVariables pins the documented behaviour for a
// payload that is not a JSON object: decoding fails softly and the handler sees
// no variables, rather than the worker goroutine dying on the frame.
//
// It asserts emptiness, not non-nilness. RawVariables may legitimately be nil
// today — a REST job activated with no variables, and a gRPC payload of "null",
// both produce a nil map, while an empty gRPC payload produces an empty one.
// That inconsistency is a separate defect (a handler that writes to the returned
// map panics on nil), filed as #147; this test documents the current contract
// rather than asserting one that does not hold.
func TestMalformedVariablesDecodeToNoVariables(t *testing.T) {
	for _, payload := range []string{"", "not json", "[1,2,3]", `"a string"`, "null", "{"} {
		job := newGRPCJob(&pb.ActivatedJob{Variables: payload}, LiveClock{})
		if got := len(job.RawVariables()); got != 0 {
			t.Errorf("payload %q produced %d key(s), want none", payload, got)
		}
		// Reading from a nil map is safe, which is what the documentation tells
		// handler authors to rely on.
		if v, ok := job.RawVariables()["anything"]; ok {
			t.Errorf("payload %q yielded unexpected key with value %v", payload, v)
		}
	}
}

// TestVariablesFillsOnlyTheCallerNamedType pins the central deserialization
// claim: the payload chooses values, never the type that gets instantiated.
// Fields the caller did not ask for are simply dropped.
func TestVariablesFillsOnlyTheCallerNamedType(t *testing.T) {
	job := newGRPCJob(&pb.ActivatedJob{
		Variables: `{"name":"ada","unexpected":{"deeply":{"nested":true}},"count":7}`,
	}, LiveClock{})

	var got struct {
		Name string `json:"name"`
	}
	if err := job.Variables(&got); err != nil {
		t.Fatalf("Variables: %v", err)
	}
	if got.Name != "ada" {
		t.Errorf("Name = %q, want %q", got.Name, "ada")
	}
}

// TestEachJobOwnsItsVariables pins the concurrency claim: two jobs decoded from
// the same payload must not share the map a handler may mutate.
func TestEachJobOwnsItsVariables(t *testing.T) {
	payload := `{"shared":"original"}`
	first := newGRPCJob(&pb.ActivatedJob{Variables: payload}, LiveClock{})
	second := newGRPCJob(&pb.ActivatedJob{Variables: payload}, LiveClock{})

	first.RawVariables()["shared"] = "mutated by the first handler"

	if got := second.RawVariables()["shared"]; got != "original" {
		t.Errorf("second job observed %v; jobs must not share variable maps", got)
	}
}
