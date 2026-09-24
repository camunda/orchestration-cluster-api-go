package camunda

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"testing"
)

const bundledSpecPath = "external-spec/bundled/rest-api.bundle.json"

type specCoupling struct {
	responseSchema string
	responseField  string
	requestFlag    string
}

// couplingsDeclaredBySpec re-derives the x-present-when couplings straight from the
// bundled spec, so the test does not depend on the same code path that generated the
// table it is checking.
func couplingsDeclaredBySpec(t *testing.T) []specCoupling {
	t.Helper()
	raw, err := os.ReadFile(bundledSpecPath)
	if err != nil {
		t.Fatalf("read bundled spec: %v", err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse bundled spec: %v", err)
	}

	var out []specCoupling
	for schemaName, schema := range spec.Components.Schemas {
		for fieldName, field := range schema.Properties {
			marker, ok := field["x-present-when"].(map[string]any)
			if !ok {
				continue
			}
			flag, _ := marker["request"].(string)
			out = append(out, specCoupling{
				responseSchema: schemaName,
				responseField:  fieldName,
				requestFlag:    flag,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].responseSchema != out[j].responseSchema {
			return out[i].responseSchema < out[j].responseSchema
		}
		return out[i].responseField < out[j].responseField
	})
	return out
}

// TestPresentWhenTableMatchesSpec is the derivation guard. It is scoped to the class
// of defect -- a coupling the spec declares that the generated table does not carry --
// rather than to the lease instance, so a second x-present-when field added upstream
// cannot slip past the generator unnoticed.
func TestPresentWhenTableMatchesSpec(t *testing.T) {
	declared := couplingsDeclaredBySpec(t)
	if len(declared) == 0 {
		t.Fatal("bundled spec declares no x-present-when markers: either the bundler is " +
			"stripping the vendor key or upstream dropped it; this guard cannot pass vacuously")
	}
	if len(presentWhenCouplings) != len(declared) {
		t.Fatalf("generated table has %d coupling(s), spec declares %d: %v vs %v",
			len(presentWhenCouplings), len(declared), presentWhenCouplings, declared)
	}
	for i, want := range declared {
		got := presentWhenCouplings[i]
		if got.ResponseSchema != want.responseSchema || got.ResponseField != want.responseField {
			t.Errorf("coupling %d = %s.%s, spec declares %s.%s",
				i, got.ResponseSchema, got.ResponseField, want.responseSchema, want.responseField)
			continue
		}
		if got.RequestFlag != want.requestFlag {
			t.Errorf("%s.%s request flag = %q, spec declares %q",
				got.ResponseSchema, got.ResponseField, got.RequestFlag, want.requestFlag)
		}
	}
}

// TestEveryPresentWhenCouplingIsEnforced is the second half of the class-scoped guard.
// Deriving a coupling is worthless if no runtime path acts on it, so a coupling the
// spec declares and the runtime does not enforce fails here rather than silently
// handing callers an unfenced job.
func TestEveryPresentWhenCouplingIsEnforced(t *testing.T) {
	if len(presentWhenCouplings) == 0 {
		t.Fatal("no couplings in the generated table")
	}
	for _, c := range presentWhenCouplings {
		if !enforcedCouplings[c.key()] {
			t.Errorf("spec declares dependent presence for %s (when %q is set) but no runtime "+
				"path enforces it: a caller opting in would receive an unfenced job and never know",
				c.key(), c.RequestFlag)
		}
	}
}

// TestEnforcedCouplingsAreDeclared catches the mirror-image drift: an enforcement entry
// naming a coupling the spec no longer declares, which would leave dead runtime code
// asserting a contract that has moved.
func TestEnforcedCouplingsAreDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, c := range presentWhenCouplings {
		declared[c.key()] = true
	}
	for key := range enforcedCouplings {
		if !declared[key] {
			t.Errorf("runtime enforces %s but the spec no longer declares it", key)
		}
	}
}

func TestRequireLeasePresence(t *testing.T) {
	tests := []struct {
		name      string
		requested bool
		token     string
		wantErr   bool
	}{
		{name: "lease requested and honored", requested: true, token: "lease-abc"},
		{name: "lease requested but not honored", requested: true, token: "", wantErr: true},
		{name: "no lease requested and none returned", requested: false, token: ""},
		{name: "no lease requested but one returned", requested: false, token: "lease-abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := requireLeasePresence(tc.requested, tc.token)
			if tc.wantErr {
				if !errors.Is(err, ErrLeaseNotHonored) {
					t.Fatalf("err = %v, want ErrLeaseNotHonored", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}
