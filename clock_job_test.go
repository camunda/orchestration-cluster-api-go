// A handler can only pace itself on the SDK's timeline if the job it is given carries
// the *client's* clock. Handing it LiveClock{} would compile, pass every other test,
// and silently leave handlers on real time.
package camunda_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
)

func TestAHandlerReceivesTheClientsClock(t *testing.T) {
	activated := []map[string]any{{
		"type":                     "demo",
		"processDefinitionId":      "proc",
		"processDefinitionVersion": 1,
		"elementId":                "task",
		"customHeaders":            map[string]any{},
		"worker":                   "test-worker",
		"retries":                  3,
		"deadline":                 0,
		"variables":                map[string]any{},
		"tenantId":                 "<default>",
		"physicalTenantId":         "physical",
		"jobKey":                   "1",
		"processInstanceKey":       "2",
		"processDefinitionKey":     "3",
		"elementInstanceKey":       "4",
		"kind":                     "BPMN_ELEMENT",
		"listenerEventType":        "UNSPECIFIED",
		"userTask":                 nil,
		"tags":                     []string{},
		"rootProcessInstanceKey":   "2",
		"priority":                 0,
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v2/jobs/activation" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": activated})
			activated = nil // one delivery only; later polls come back empty
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	clock := newCountingClock()
	client, err := camunda.New(
		camunda.WithRestAddress(srv.URL),
		camunda.WithNoAuth(),
		camunda.WithForceREST(true),
		camunda.WithClock(clock),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	seen := make(chan camunda.Clock, 1)
	worker := client.NewJobWorker("demo", func(_ context.Context, job *camunda.Job) (map[string]any, error) {
		select {
		case seen <- job.Clock():
		default:
		}
		return nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	select {
	case got := <-seen:
		if got != camunda.Clock(clock) {
			t.Fatalf("handler received clock %T, want the injected one", got)
		}
	case <-ctx.Done():
		t.Fatal("handler never ran")
	}
}
