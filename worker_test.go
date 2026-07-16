package camunda_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
)

// jobTemplate renders a single activated job with the given key; %s is the jobKey.
const jobTemplate = `{
  "type": "demo-task",
  "processDefinitionId": "demo-process",
  "processDefinitionVersion": 1,
  "elementId": "task",
  "customHeaders": {},
  "worker": "test-worker",
  "retries": 3,
  "deadline": 1784256664927,
  "variables": {},
  "tenantId": "<default>",
  "jobKey": "%s",
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
}`

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

func TestJobWorkerThrowsBpmnErrorOnBpmnError(t *testing.T) {
	thrown := make(chan string, 1)
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
		case strings.HasSuffix(r.URL.Path, "/error"):
			select {
			case thrown <- r.URL.Path:
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
			return nil, &camunda.BpmnError{Code: "BOOM", Message: "nope"}
		},
		camunda.WithRequestTimeout(50*time.Millisecond),
		camunda.WithPollInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	select {
	case path := <-thrown:
		if !strings.HasSuffix(path, "/jobs/123/error") {
			t.Errorf("error path = %q, want .../jobs/123/error", path)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("BPMN error was not thrown when handler returned a *BpmnError")
	}
}

// TestJobWorkerBoundsConcurrencyToMaxConcurrent verifies that the REST worker
// never runs more handlers concurrently than maxConcurrent, by activating only
// (maxConcurrent - inFlight) jobs per cycle. The activation server echoes exactly
// the requested job count, so a broken cap would surface as peak concurrency > 2.
func TestJobWorkerBoundsConcurrencyToMaxConcurrent(t *testing.T) {
	var keyCounter atomic.Int64
	var mu sync.Mutex
	var live, peak int
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs/activation"):
			var body struct {
				MaxJobsToActivate int `json:"maxJobsToActivate"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			jobs := make([]string, 0, body.MaxJobsToActivate)
			for i := 0; i < body.MaxJobsToActivate; i++ {
				jobs = append(jobs, fmt.Sprintf(jobTemplate, strconv.FormatInt(keyCounter.Add(1), 10)))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jobs":[`+strings.Join(jobs, ",")+`]}`)
		case strings.HasSuffix(r.URL.Path, "/completion"):
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
			mu.Lock()
			live++
			if live > peak {
				peak = live
			}
			mu.Unlock()
			<-release
			mu.Lock()
			live--
			mu.Unlock()
			return nil, nil
		},
		camunda.WithMaxConcurrentJobs(2),
		camunda.WithRequestTimeout(50*time.Millisecond),
		camunda.WithPollInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	// Wait until two handlers are concurrently live.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		l := live
		mu.Unlock()
		if l >= 2 {
			break
		}
		select {
		case <-deadline:
			close(release)
			t.Fatal("worker did not reach two concurrent handlers")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// Give a would-be third handler time to start if the cap were broken.
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	p := peak
	mu.Unlock()
	if p != 2 {
		t.Errorf("peak concurrency = %d, want 2 (bounded by maxConcurrent)", p)
	}
	close(release)
}

// TestJobWorkerAcksAfterContextCancel verifies that a handler that finishes after
// the worker's context is cancelled still has its completion acknowledged (the
// ack uses a context detached from Run's lifecycle).
func TestJobWorkerAcksAfterContextCancel(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})
	completed := make(chan string, 1)
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
			close(started)
			<-proceed
			return map[string]any{"ok": true}, nil
		},
		camunda.WithRequestTimeout(50*time.Millisecond),
		camunda.WithPollInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { _ = worker.Run(ctx); close(runDone) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("handler never started")
	}

	// Cancel the worker while the handler is still in flight, then let it finish.
	cancel()
	close(proceed)

	select {
	case path := <-completed:
		if !strings.HasSuffix(path, "/jobs/123/completion") {
			t.Errorf("completion path = %q, want .../jobs/123/completion", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion ack was dropped after context cancellation")
	}

	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// TestJobWorkerAppliesDefaultTenant verifies that the client's default tenant is
// sent as the activation tenant filter.
func TestJobWorkerAppliesDefaultTenant(t *testing.T) {
	tenants := make(chan []string, 1)
	var activateCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/jobs/activation") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			TenantIds []string `json:"tenantIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if activateCount.Add(1) == 1 {
			select {
			case tenants <- body.TenantIds:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jobs":[]}`)
	}))
	defer srv.Close()

	client, err := camunda.New(camunda.WithRestAddress(srv.URL), camunda.WithLogLevel(camunda.LogOff),
		camunda.WithDefaultTenantID("acme"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	worker := client.NewJobWorker("demo-task",
		func(context.Context, *camunda.Job) (map[string]any, error) { return nil, nil },
		camunda.WithRequestTimeout(50*time.Millisecond),
		camunda.WithPollInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	select {
	case ids := <-tenants:
		if len(ids) != 1 || ids[0] != "acme" {
			t.Errorf("activation tenantIds = %v, want [acme]", ids)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not activate jobs")
	}
}
