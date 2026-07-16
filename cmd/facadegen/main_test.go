package main

import (
	"strings"
	"testing"
)

// TestGenerateFacade locks the AST-based facade generator against a fixture
// client package: it must emit one ergonomic *CamundaClient method per operation,
// qualify client types with the openapi alias, and handle both value-returning
// and no-value operations.
func TestGenerateFacade(t *testing.T) {
	src, count, err := generateFacade("testdata/client", "")
	if err != nil {
		t.Fatalf("generateFacade: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 operations, got %d", count)
	}

	want := []string{
		"package camunda",
		`openapi "github.com/camunda/orchestration-cluster-api-go/client"`,
		// Value-returning op: exposes required params, returns (value, error).
		"func (c *CamundaClient) GetWidget(ctx context.Context, id openapi.WidgetKey) (*openapi.Widget, error) {",
		"value, resp, err := c.raw.WidgetAPI.GetWidget(ctx, id).Execute()",
		"return value, c.wrapError(resp, err)",
		// No-value op: returns error only.
		"func (c *CamundaClient) DeleteWidget(ctx context.Context, id openapi.WidgetKey) error {",
		"return c.wrapError(resp, err)",
	}
	for _, w := range want {
		if !strings.Contains(src, w) {
			t.Errorf("facade output missing %q\n--- generated ---\n%s", w, src)
		}
	}
}
