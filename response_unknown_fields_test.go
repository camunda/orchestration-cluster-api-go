package camunda

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Issue #124: a server that adds a response field must not break decoding.
// Guards the defect class over every generated response model: no model reachable
// from an operation response may reject unknown fields.
func TestResponseModelsDoNotRejectUnknownFields(t *testing.T) {
	raw, err := os.ReadFile(bundledSpecPath)
	if err != nil {
		t.Fatalf("read bundled spec: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse bundled spec: %v", err)
	}
	comps, _ := spec["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)
	responses, _ := comps["responses"].(map[string]any)

	var collect func(node any, acc map[string]bool)
	collect = func(node any, acc map[string]bool) {
		switch n := node.(type) {
		case map[string]any:
			for k, v := range n {
				if s, ok := v.(string); ok && k == "$ref" {
					acc[s[strings.LastIndex(s, "/")+1:]] = true
				} else {
					collect(v, acc)
				}
			}
		case []any:
			for _, v := range n {
				collect(v, acc)
			}
		}
	}
	roots := map[string]bool{}
	paths, _ := spec["paths"].(map[string]any)
	for _, item := range paths {
		ops, _ := item.(map[string]any)
		for _, op := range ops {
			if o, ok := op.(map[string]any); ok {
				collect(o["responses"], roots)
			}
		}
	}
	reach := map[string]bool{}
	var todo []string
	for n := range roots {
		todo = append(todo, n)
	}
	for len(todo) > 0 {
		n := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if reach[n] {
			continue
		}
		reach[n] = true
		node, ok := schemas[n]
		if !ok {
			node = responses[n]
		}
		next := map[string]bool{}
		collect(node, next)
		for m := range next {
			todo = append(todo, m)
		}
	}

	files, err := filepath.Glob(filepath.Join("client", "model_*.go"))
	if err != nil {
		t.Fatal(err)
	}
	varRe := regexp.MustCompile(`var\w+ := _(\w+)\{\}`)
	checked := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m := varRe.FindSubmatch(src)
		if m == nil || !reach[string(m[1])] {
			continue
		}
		checked++
		if strings.Contains(string(src), "DisallowUnknownFields") {
			t.Errorf("%s: response model %s rejects unknown fields", f, m[1])
		}
	}
	if checked < 100 {
		t.Fatalf("only %d response models checked; spec walk or file mapping is broken", checked)
	}
}

// Runtime check over representative response models: decode JSON carrying an
// extra unknown field and assert success with the known fields preserved.
func TestResponseModelsDecodeWithUnknownField(t *testing.T) {
	cases := map[string]any{
		"CreateProcessInstanceResult":      &CreateProcessInstanceResult{},
		"JobActivationResult":              &JobActivationResult{},
		"ProcessInstanceSearchQueryResult": &ProcessInstanceSearchQueryResult{},
		"DeploymentResult":                 &DeploymentResult{},
		"TopologyResponse":                 &TopologyResponse{},
	}
	for name, zero := range cases {
		t.Run(name, func(t *testing.T) {
			// Marshal the zero value: emits every required property. (Models with
			// required enums are covered by the static test above: their zero value
			// is not a valid enum.)
			base, err := json.Marshal(zero)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var obj map[string]any
			if err := json.Unmarshal(base, &obj); err != nil {
				t.Fatalf("to map: %v", err)
			}
			obj["someFutureServerField"] = map[string]any{"x": true}
			withExtra, _ := json.Marshal(obj)

			dst := reflect.New(reflect.TypeOf(zero).Elem()).Interface()
			if err := json.Unmarshal(withExtra, dst); err != nil {
				t.Fatalf("decode with unknown field: %v", err)
			}
			again, err := json.Marshal(dst)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
			if string(again) != string(base) {
				t.Fatalf("known fields not preserved:\n got  %s\n want %s", again, base)
			}
		})
	}
}
