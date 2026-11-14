//go:build saas

package saas

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/client"
)

// reconnectLogLine is what StreamJobWorker.Run logs, at Warn, each time the stream
// ends and is reopened (streamworker.go). It is the only observable signal of a
// dropped stream, since the worker recovers on its own.
const reconnectLogLine = "job stream ended; reconnecting"

// streamHarness runs one StreamJobWorker with its REST sidecar poll disabled, so
// every job it handles was delivered over the gRPC stream.
type streamHarness struct {
	t       *testing.T
	jobType string
	logs    *logCapture
	// handled receives the "name" variable of each job the handler completes.
	handled chan string
	stop    context.CancelFunc
	done    chan error
	once    sync.Once
}

// startStreamWorker starts the worker and returns once Run has been launched. The
// stream registers asynchronously; callers that create a job straight away should
// use waitStreamOpen first.
func startStreamWorker(ctx context.Context, t *testing.T, c *camunda.CamundaClient, logs *logCapture, p process) *streamHarness {
	t.Helper()
	h := &streamHarness{
		t:       t,
		jobType: p.jobType,
		logs:    logs,
		handled: make(chan string, 64),
		done:    make(chan error, 1),
	}
	worker := c.NewStreamJobWorker(p.jobType,
		func(_ context.Context, job *camunda.Job) (map[string]any, error) {
			var in struct {
				Name string `json:"name"`
			}
			if err := job.Variables(&in); err != nil {
				return nil, err
			}
			select {
			case h.handled <- in.Name:
			default:
			}
			return map[string]any{"greeting": "Hello, " + in.Name + "!"}, nil
		},
		// Disable the sidecar poll: a job reaching the handler must have come down the
		// stream, otherwise the 30s poll could mask a dead stream.
		camunda.WithStreamPollInterval(-1),
	)
	wctx, stop := context.WithCancel(ctx)
	h.stop = stop
	go func() { h.done <- worker.Run(wctx) }()
	t.Cleanup(func() { _ = h.shutdown() })
	return h
}

// shutdown stops the worker and waits for Run to return. It is idempotent.
func (h *streamHarness) shutdown() error {
	var err error
	h.once.Do(func() {
		h.stop()
		select {
		case runErr := <-h.done:
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				err = runErr
			}
		case <-time.After(35 * time.Second):
			err = errors.New("worker did not stop within 35 seconds")
		}
	})
	return err
}

// reconnects counts how often this worker's stream ended since it started.
func (h *streamHarness) reconnects() int {
	n := 0
	for _, line := range strings.Split(h.logs.String(), "\n") {
		if strings.Contains(line, reconnectLogLine) && strings.Contains(line, "type="+h.jobType) {
			n++
		}
	}
	return n
}

// waitStreamOpen gives the stream time to register with the gateway. There is no
// readiness signal, so this is a settle delay; the worker exiting early is a failure.
func (h *streamHarness) waitStreamOpen(settle time.Duration) {
	h.t.Helper()
	select {
	case err := <-h.done:
		h.t.Fatalf("stream worker exited during startup: %v", err)
	case <-time.After(settle):
	}
	if n := h.reconnects(); n > 0 {
		h.t.Fatalf("stream ended %d time(s) while opening:\n%s", n, h.logs.String())
	}
}

// idleFor lets the stream sit for d, failing the moment the worker exits or the
// stream is reconnected. The elapsed time in the failure message is the useful
// datum: a drop near 60s points at an intermediary's idle limit.
func (h *streamHarness) idleFor(d time.Duration, since time.Time) {
	h.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		select {
		case err := <-h.done:
			h.t.Fatalf("stream worker exited %s into the idle window: %v", time.Since(since).Round(time.Second), err)
		case <-time.After(time.Second):
		}
		if n := h.reconnects(); n > 0 {
			h.t.Fatalf("stream dropped and reconnected %d time(s) %s into the idle window:\n%s",
				n, time.Since(since).Round(time.Second), h.logs.String())
		}
	}
}

// awaitDelivery waits for the job of the instance named name to reach the handler
// and returns how long it took.
func (h *streamHarness) awaitDelivery(name string, within time.Duration) time.Duration {
	h.t.Helper()
	start := time.Now()
	timeout := time.After(within)
	for {
		select {
		case got := <-h.handled:
			if got == name {
				return time.Since(start)
			}
		case err := <-h.done:
			h.t.Fatalf("stream worker exited before delivering %q: %v", name, err)
		case <-timeout:
			h.t.Fatalf("job %q was not delivered over the stream within %s (reconnects=%d)", name, within, h.reconnects())
		}
	}
}

// awaitCompleted polls until the instance reaches COMPLETED, which proves the gRPC
// CompleteJob acknowledgement from the stream worker was accepted.
func awaitCompleted(ctx context.Context, t *testing.T, c *camunda.CamundaClient, key camundaapi.ProcessInstanceKey) {
	t.Helper()
	errNotCompleted := errors.New("instance not completed yet")
	_, err := camunda.Poll(ctx, func(ctx context.Context) (*camundaapi.ProcessInstanceResult, error) {
		instance, err := c.GetProcessInstance(ctx, key)
		if err != nil {
			return nil, err
		}
		if instance.GetState() != camundaapi.PROCESSINSTANCESTATEENUM_COMPLETED {
			return nil, errNotCompleted
		}
		return instance, nil
	},
		camunda.WithPollTimeout(60*time.Second),
		camunda.WithRetryOn(func(err error) bool {
			return errors.Is(err, errNotCompleted) || camunda.IsNotFound(err)
		}),
	)
	if err != nil {
		t.Fatalf("process instance %s did not complete: %v", key, err)
	}
}

// TestSaaSStreamWorkerCompletesJobs is the smoke test for the gRPC streaming path:
// jobs created after the stream opens are pushed to the worker and completed over
// gRPC, and the instances run to completion.
func TestSaaSStreamWorkerCompletesJobs(t *testing.T) {
	c, logs := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	p := deployGreet(ctx, t, c)
	h := startStreamWorker(ctx, t, c, logs, p)
	h.waitStreamOpen(5 * time.Second)

	const instances = 3
	keys := make([]camundaapi.ProcessInstanceKey, 0, instances)
	names := make([]string, 0, instances)
	for i := range instances {
		name := "stream-" + time.Now().UTC().Format("150405.000000000") + "-" + string(rune('a'+i))
		names = append(names, name)
		keys = append(keys, startInstance(ctx, t, c, p, name))
	}
	for _, name := range names {
		latency := h.awaitDelivery(name, 30*time.Second)
		t.Logf("job %q delivered over the stream in %s", name, latency.Round(time.Millisecond))
	}
	for _, key := range keys {
		awaitCompleted(ctx, t, c, key)
	}
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
}
