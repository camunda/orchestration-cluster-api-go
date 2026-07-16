package camunda_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

// TestFacadeAppliesOptsTransform verifies that a facade method applies the
// variadic opts transform to the request builder before executing. The opt runs
// during request construction (before the HTTP round-trip), so the assertion
// holds regardless of the server response.
func TestFacadeAppliesOptsTransform(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	client, err := camunda.New(camunda.WithRestAddress(srv.URL), camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	called := false
	_, _ = client.GetTopology(context.Background(),
		func(r openapi.ApiGetTopologyRequest) openapi.ApiGetTopologyRequest {
			called = true
			return r
		})
	if !called {
		t.Error("facade did not apply the opts transform to the request builder")
	}
}
