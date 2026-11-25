package camunda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/camunda/orchestration-cluster-api-go/v11/internal/diag"
)

// --- slotGate: event-driven wake-up -------------------------------------------

func TestSlotGateThreshold(t *testing.T) {
	g := newSlotGate(8) // threshold 2
	if got := g.free(); got != 8 {
		t.Fatalf("idle free = %d, want 8", got)
	}
	g.acquire(8)
	if got := g.free(); got != 0 {
		t.Fatalf("full free = %d, want 0", got)
	}
	g.release() // 7 in flight, 1 free < threshold: no wake, no poll
	if got := g.free(); got != 0 {
		t.Fatalf("free below threshold = %d, want 0 (no 1-job micro-poll)", got)
	}
	select {
	case <-g.wake:
		t.Fatal("woke below threshold")
	default:
	}
	g.release() // 6 in flight, 2 free == threshold: wake
	select {
	case <-g.wake:
	default:
		t.Fatal("no wake at threshold")
	}
	if got := g.free(); got != 2 {
		t.Fatalf("free at threshold = %d, want 2", got)
	}
}

func TestSlotGateTailWakesWhenIdle(t *testing.T) {
	// A threshold equal to max isolates the idle rule: a tail of in-flight jobs
	// never frees threshold slots, so only reaching zero may wake the poller.
	g := &slotGate{max: 8, threshold: 8, wake: make(chan struct{}, 1)}
	g.acquire(2)
	if got := g.free(); got != 0 {
		t.Fatalf("free with tail below threshold = %d, want 0", got)
	}
	g.release()
	select {
	case <-g.wake:
		t.Fatal("woke with a job still in flight and capacity below threshold")
	default:
	}
	g.release()
	select {
	case <-g.wake:
	default:
		t.Fatal("idle worker was not woken: a tail job would stall")
	}
	if got := g.free(); got != 8 {
		t.Fatalf("idle free = %d, want 8", got)
	}
}

func TestSlotGateReleaseNeverBlocks(t *testing.T) {
	g := newSlotGate(1)
	g.acquire(1)
	g.release()
	g.acquire(1)
	done := make(chan struct{})
	go func() { g.release(); close(done) }() // wake already full
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("release blocked on a full wake channel")
	}
}

func TestSlotGateWaitHonorsCancel(t *testing.T) {
	g := newSlotGate(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait = %v, want context.Canceled", err)
	}
}

// fakeEngine serves activation (handing out up to maxJobs jobs, bounded by
// remaining) and acknowledges completions.
type fakeEngine struct {
	activations atomic.Int32
	completions atomic.Int32
	remaining   atomic.Int64
	keys        atomic.Int64
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/jobs/activation"):
		f.activations.Add(1)
		var body struct {
			MaxJobsToActivate int `json:"maxJobsToActivate"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var jobs []string
		for range body.MaxJobsToActivate {
			if f.remaining.Add(-1) < 0 {
				f.remaining.Add(1)
				break
			}
			jobs = append(jobs, fmt.Sprintf(`{"type":"t","processDefinitionId":"p","processDefinitionVersion":1,"elementId":"e","customHeaders":{},"worker":"w","retries":3,"deadline":1,"variables":{},"tenantId":"<default>","jobKey":"%d","processInstanceKey":"1","processDefinitionKey":"2","elementInstanceKey":"3","kind":"BPMN_ELEMENT","listenerEventType":"UNSPECIFIED","userTask":null,"tags":[],"rootProcessInstanceKey":"1","businessId":null,"priority":0,"jobLeaseToken":null}`, f.keys.Add(1)))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jobs":[`+strings.Join(jobs, ",")+`]}`)
	case strings.HasSuffix(r.URL.Path, "/completion"):
		f.completions.Add(1)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestJobWorkerWakesWhenCapacityFrees pins that a worker at capacity polls again
// when a job finishes, not after pollInterval (here an hour).
func TestJobWorkerWakesWhenCapacityFrees(t *testing.T) {
	eng := &fakeEngine{}
	eng.remaining.Store(5)
	srv := httptest.NewServer(eng)
	defer srv.Close()
	c, err := New(WithRestAddress(srv.URL), WithLogLevel(LogOff))
	if err != nil {
		t.Fatal(err)
	}
	w := c.NewJobWorker("t", func(context.Context, *Job) (map[string]any, error) { return nil, nil },
		WithMaxConcurrentJobs(1), WithPollInterval(time.Hour), WithRequestTimeout(10*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for eng.completions.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := eng.completions.Load(); got != 5 {
		t.Fatalf("completed %d jobs, want 5: worker stalled at capacity", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
}

// TestJobWorkerAtCapacityDoesNotSpin pins that a worker whose slots are all busy
// does not poll at all until a slot frees, and stops cleanly on cancel.
func TestJobWorkerAtCapacityDoesNotSpin(t *testing.T) {
	eng := &fakeEngine{}
	eng.remaining.Store(1 << 30)
	srv := httptest.NewServer(eng)
	defer srv.Close()
	c, err := New(WithRestAddress(srv.URL), WithLogLevel(LogOff))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var started atomic.Int32
	w := c.NewJobWorker("t", func(context.Context, *Job) (map[string]any, error) {
		started.Add(1)
		<-release
		return nil, nil
	}, WithMaxConcurrentJobs(4), WithPollInterval(time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for started.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	before := eng.activations.Load()
	time.Sleep(100 * time.Millisecond)
	if after := eng.activations.Load(); after != before {
		t.Fatalf("activations went %d -> %d while at capacity: busy polling", before, after)
	}
	cancel()
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop at capacity")
	}
}

// --- lighter REST ack path ----------------------------------------------------

type recordedReq struct {
	method, path, contentType, accept, ua, custom string
	body                                          map[string]any
}

// recordAcks returns a server that records each ack request and replies with status.
func recordAcks(t *testing.T, status int) (*httptest.Server, *[]recordedReq, *sync.Mutex) {
	var mu sync.Mutex
	var reqs []recordedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Errorf("body %q is not a JSON object: %v", b, err)
		}
		mu.Lock()
		reqs = append(reqs, recordedReq{r.Method, r.URL.EscapedPath(), r.Header.Get("Content-Type"),
			r.Header.Get("Accept"), r.Header.Get("User-Agent"), r.Header.Get("X-Custom"), m})
		mu.Unlock()
		if status >= 300 {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"title":"nope"}`)
			return
		}
		w.WriteHeader(status)
	}))
	return srv, &reqs, &mu
}

func ackScenarios() []func(c *CamundaClient, ctx context.Context) {
	plain := &Job{key: "1/2", retries: 3}
	leased := &Job{key: "7", retries: 0, leaseToken: "tok"}
	return []func(*CamundaClient, context.Context){
		func(c *CamundaClient, ctx context.Context) { c.restAck(ctx, plain, nil, nil) },
		func(c *CamundaClient, ctx context.Context) { c.restAck(ctx, plain, map[string]any{}, nil) },
		func(c *CamundaClient, ctx context.Context) {
			c.restAck(ctx, leased, map[string]any{"a": 1.0, "n": map[string]any{"b": "x"}}, nil)
		},
		func(c *CamundaClient, ctx context.Context) { c.restAck(ctx, plain, nil, errors.New("boom")) },
		func(c *CamundaClient, ctx context.Context) { c.restAck(ctx, leased, nil, errors.New("")) },
		func(c *CamundaClient, ctx context.Context) {
			c.restAck(ctx, plain, nil, &BpmnError{Code: "E1"})
		},
		func(c *CamundaClient, ctx context.Context) {
			c.restAck(ctx, leased, nil, &BpmnError{Code: "E2", Message: "m", Variables: map[string]any{"v": true}})
		},
	}
}

// TestRESTAckLightPathMatchesGenerated pins that the light ack path sends the
// same method, path, headers and JSON body as the generated client.
func TestRESTAckLightPathMatchesGenerated(t *testing.T) {
	srv, reqs, mu := recordAcks(t, http.StatusNoContent)
	defer srv.Close()
	c, err := New(WithRestAddress(srv.URL), WithLogLevel(LogOff))
	if err != nil {
		t.Fatal(err)
	}
	c.raw.GetConfig().AddDefaultHeader("X-Custom", "yes")
	prev := log.Writer()
	log.SetOutput(io.Discard) // generated Debug mode dumps through the std logger
	defer log.SetOutput(prev)
	ctx := context.Background()
	for i, sc := range ackScenarios() {
		c.raw.GetConfig().Debug = true
		sc(c, ctx)
		c.raw.GetConfig().Debug = false
		sc(c, ctx)
		mu.Lock()
		if len(*reqs) != 2 {
			t.Fatalf("scenario %d: %d requests, want 2", i, len(*reqs))
		}
		gen, light := (*reqs)[0], (*reqs)[1]
		*reqs = nil
		mu.Unlock()
		if g, l := fmt.Sprintf("%+v", gen), fmt.Sprintf("%+v", light); g != l {
			t.Errorf("scenario %d:\n generated %s\n light     %s", i, g, l)
		}
	}
}

// TestRESTAckLightPathLogsAPIError pins that a rejected ack is logged with the
// status and problem body.
func TestRESTAckLightPathLogsAPIError(t *testing.T) {
	srv, _, _ := recordAcks(t, http.StatusConflict)
	defer srv.Close()
	c, err := New(WithRestAddress(srv.URL), WithLogLevel(LogOff))
	if err != nil {
		t.Fatal(err)
	}
	buf := &syncBuffer{}
	c.logger = diag.New(diag.LevelTrace, buf, c.clock)
	c.restAck(context.Background(), &Job{key: "9", retries: 1}, nil, nil)
	out := buf.String()
	if !strings.Contains(out, "complete job failed") || !strings.Contains(out, "409") || !strings.Contains(out, "nope") {
		t.Fatalf("log = %q, want complete-job failure with status 409 and body", out)
	}
}

// --- benchmarks ---------------------------------------------------------------

func benchAck(b *testing.B, generated bool) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c, err := New(WithRestAddress(srv.URL), WithLogLevel(LogOff))
	if err != nil {
		b.Fatal(err)
	}
	if generated {
		prev := log.Writer()
		log.SetOutput(io.Discard)
		c.raw.GetConfig().Debug = true
		b.Cleanup(func() { log.SetOutput(prev) })
	}
	job := &Job{key: "2251799813685423", retries: 3}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.restAck(ctx, job, nil, nil)
	}
}

// BenchmarkRESTCompleteLight measures the worker's completion hot path.
func BenchmarkRESTCompleteLight(b *testing.B) { benchAck(b, false) }

// BenchmarkRESTCompleteGenerated measures the generated-client completion path
// for comparison. Debug dumping is what selects it, so it also pays the dump.
func BenchmarkRESTCompleteGenerated(b *testing.B) { benchAck(b, true) }

// BenchmarkJobWorkerThroughput runs a REST worker against an in-process engine
// with a 2 ms handler and reports jobs/s and allocations per job.
func BenchmarkJobWorkerThroughput(b *testing.B) {
	eng := &fakeEngine{}
	eng.remaining.Store(int64(b.N))
	srv := httptest.NewServer(eng)
	defer srv.Close()
	c, err := New(WithRestAddress(srv.URL), WithLogLevel(LogOff))
	if err != nil {
		b.Fatal(err)
	}
	w := c.NewJobWorker("t", func(context.Context, *Job) (map[string]any, error) {
		time.Sleep(2 * time.Millisecond)
		return nil, nil
	}, WithMaxConcurrentJobs(10), WithPollInterval(100*time.Millisecond), WithRequestTimeout(10*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	go func() { done <- w.Run(ctx) }()
	for eng.completions.Load() < int32(b.N) {
		time.Sleep(100 * time.Microsecond)
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "jobs/s")
	cancel()
	<-done
}
