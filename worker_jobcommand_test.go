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

	camunda "github.com/camunda/orchestration-cluster-api-go/v10"
)

// A job command whose outcome is unknown (504) is sent once and is not followed by
// failJob; one that was not applied (429, 503) is retried.
func TestJobWorkerCompletionRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		status      int
		completions int32
	}{
		{http.StatusGatewayTimeout, 1},
		{http.StatusBadGateway, 1},
		{http.StatusServiceUnavailable, 2},
		{http.StatusTooManyRequests, 2},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var activations, completions, failures atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/jobs/activation"):
					w.Header().Set("Content-Type", "application/json")
					if activations.Add(1) == 1 {
						_, _ = io.WriteString(w, oneJobResponse)
					} else {
						_, _ = io.WriteString(w, `{"jobs":[]}`)
					}
				case strings.HasSuffix(r.URL.Path, "/completion"):
					// Fail the first attempt; succeed afterwards.
					if completions.Add(1) == 1 {
						w.WriteHeader(tc.status)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/failure"):
					failures.Add(1)
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
				func(ctx context.Context, job *camunda.Job) (map[string]any, error) { return nil, nil },
				camunda.WithRequestTimeout(50*time.Millisecond),
				camunda.WithPollInterval(10*time.Millisecond),
			)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- worker.Run(ctx) }()

			deadline := time.Now().Add(3 * time.Second)
			for completions.Load() < tc.completions && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(300 * time.Millisecond) // let any (wrong) retry or failJob land
			cancel()
			<-done

			if got := completions.Load(); got != tc.completions {
				t.Errorf("completion requests = %d, want %d", got, tc.completions)
			}
			if got := failures.Load(); got != 0 {
				t.Errorf("failJob requests = %d, want 0", got)
			}
		})
	}
}
