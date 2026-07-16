package camunda_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
)

// oneJobResponse is a JobActivationResult carrying a single job (key 123, type
// demo-task). Fields mirror a real server response.
const oneJobResponse = `{
  "jobs": [
    {
      "type": "demo-task",
      "processDefinitionId": "demo-process",
      "processDefinitionVersion": 1,
      "elementId": "task",
      "customHeaders": {"h": "v"},
      "worker": "test-worker",
      "retries": 3,
      "deadline": 1784256664927,
      "variables": {"amount": 42},
      "tenantId": "<default>",
      "jobKey": "123",
      "processInstanceKey": "2251799813685417",
      "processDefinitionKey": "2251799813685416",
      "elementInstanceKey": "2251799813685423",
      "kind": "BPMN_ELEMENT",
      "listenerEventType": "UNSPECIFIED",
      "userTask": null,
      "tags": [],
      "rootProcessInstanceKey": "2251799813685417",
      "businessId": null,
      "priority": 0,
      "leaseToken": null
    }
  ]
}`

func TestJobWorkerActivatesHandlesAndCompletes(t *testing.T) {
	var activateCount atomic.Int32
	completed := make(chan string, 1)
	handled := make(chan *camunda.Job, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs/activation"):
			w.Header().Set("Content-Type", "application/json")
			if activateCount.Add(1) == 1 {
				_, _ = io.WriteString(w, oneJobResponse)
			} else {
				_, _ = io.WriteString(w, `{"jobs":[]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/completion"):
			select {
			case completed <- r.URL.Path:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := camunda.New(camunda.WithRestAddress(srv.URL), camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	worker := client.NewJobWorker("demo-task",
		func(ctx context.Context, job *camunda.Job) (map[string]any, error) {
			select {
			case handled <- job:
			default:
			}
			return map[string]any{"done": true}, nil
		},
		camunda.WithRequestTimeout(50*time.Millisecond),
		camunda.WithPollInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	select {
	case job := <-handled:
		if job.Type() != "demo-task" {
			t.Errorf("job type = %q, want demo-task", job.Type())
		}
		if job.Key() != "123" {
			t.Errorf("job key = %q, want 123", job.Key())
		}
		var vars struct {
			Amount int `json:"amount"`
		}
		if err := job.Variables(&vars); err != nil || vars.Amount != 42 {
			t.Errorf("Variables() = %+v, err %v; want amount 42", vars, err)
		}
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("handler was not invoked")
	}

	select {
	case path := <-completed:
		if !strings.HasSuffix(path, "/jobs/123/completion") {
			t.Errorf("completion path = %q, want .../jobs/123/completion", path)
		}
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("job was not completed")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
}

func TestJobWorkerFailsJobOnHandlerError(t *testing.T) {
	failed := make(chan string, 1)
	var activateCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs/activation"):
			w.Header().Set("Content-Type", "application/json")
			if activateCount.Add(1) == 1 {
				_, _ = io.WriteString(w, oneJobResponse)
			} else {
				_, _ = io.WriteString(w, `{"jobs":[]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/failure"):
			select {
			case failed <- r.URL.Path:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := camunda.New(camunda.WithRestAddress(srv.URL), camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	worker := client.NewJobWorker("demo-task",
		func(ctx context.Context, job *camunda.Job) (map[string]any, error) {
			return nil, io.ErrUnexpectedEOF // any non-BpmnError -> fail
		},
		camunda.WithRequestTimeout(50*time.Millisecond),
		camunda.WithPollInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	select {
	case path := <-failed:
		if !strings.HasSuffix(path, "/jobs/123/failure") {
			t.Errorf("failure path = %q, want .../jobs/123/failure", path)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("job was not failed on handler error")
	}
}
