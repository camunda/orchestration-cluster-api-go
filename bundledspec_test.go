package camunda

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// openapi-generator cannot follow a $ref into another operation (`#/paths/...`):
// the referencing operation silently loses that response's typed decoding.
func TestBundledSpecHasNoPathLocalRefs(t *testing.T) {
	raw, err := os.ReadFile(bundledSpecPath)
	if err != nil {
		t.Fatalf("read bundled spec: %v", err)
	}
	var spec any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse bundled spec: %v", err)
	}

	var refs int
	var pathLocal []string
	var walk func(node any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			if ref, ok := n["$ref"].(string); ok {
				refs++
				if strings.HasPrefix(ref, "#/paths/") {
					pathLocal = append(pathLocal, ref)
				}
			}
			for _, v := range n {
				walk(v)
			}
		case []any:
			for _, v := range n {
				walk(v)
			}
		}
	}
	walk(spec)

	if refs == 0 {
		t.Fatal("found no $refs in the bundled spec; the guard would pass vacuously")
	}
	if len(pathLocal) > 0 {
		t.Errorf("%d path-local $refs in %s (first: %s); bundle with --deref-path-local",
			len(pathLocal), bundledSpecPath, pathLocal[0])
	}
}
