package camunda

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/camunda/orchestration-cluster-api-go/internal/diag"
	"github.com/camunda/orchestration-cluster-api-go/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// fakeGateway is an in-memory GatewayServer that streams a fixed set of jobs and
// records the job-acknowledgement RPCs it receives.
type fakeGateway struct {
	pb.UnimplementedGatewayServer
	jobs      []*pb.ActivatedJob
	completes chan *pb.CompleteJobRequest
	fails     chan *pb.FailJobRequest
	throws    chan *pb.ThrowErrorRequest

	// streamReqs, when non-nil, records each StreamActivatedJobs request.
	streamReqs chan *pb.StreamActivatedJobsRequest

	// failFirstStream is the number of initial StreamActivatedJobs attempts to
	// fail (with codes.Unavailable) before serving jobs; streamAttempts counts
	// how many stream attempts the server has seen.
	failFirstStream int32
	streamAttempts  atomic.Int32

	// openStreams counts server-side StreamActivatedJobs handlers currently in
	// flight. A client that returns without canceling its stream leaves the
	// handler parked here, so this is how a leaked stream becomes visible.
	openStreams atomic.Int32
}

func (f *fakeGateway) StreamActivatedJobs(req *pb.StreamActivatedJobsRequest, stream grpc.ServerStreamingServer[pb.ActivatedJob]) error {
	f.openStreams.Add(1)
	defer f.openStreams.Add(-1)
	if f.streamReqs != nil {
		select {
		case f.streamReqs <- req:
		default:
		}
	}
	if f.streamAttempts.Add(1) <= f.failFirstStream {
		return status.Error(codes.Unavailable, "simulated stream failure")
	}
	for _, j := range f.jobs {
		if err := stream.Send(j); err != nil {
			return err
		}
	}
	// Hold the stream open until the client cancels, so the worker does not
	// reconnect and re-deliver the fixed job set.
	<-stream.Context().Done()
	return stream.Context().Err()
}

func (f *fakeGateway) CompleteJob(_ context.Context, req *pb.CompleteJobRequest) (*pb.CompleteJobResponse, error) {
	f.completes <- req
	return &pb.CompleteJobResponse{}, nil
}

func (f *fakeGateway) FailJob(_ context.Context, req *pb.FailJobRequest) (*pb.FailJobResponse, error) {
	f.fails <- req
	return &pb.FailJobResponse{}, nil
}

func (f *fakeGateway) ThrowError(_ context.Context, req *pb.ThrowErrorRequest) (*pb.ThrowErrorResponse, error) {
	f.throws <- req
	return &pb.ThrowErrorResponse{}, nil
}

// TestStreamJobWorkerDispatchesAndAcksOverGRPC verifies the streaming worker
// receives streamed jobs, invokes the handler, and routes the handler outcome to
// the correct gRPC acknowledgement: complete on success, fail on a plain error
// (with retries decremented), and throw on a *BpmnError.
func TestStreamJobWorkerDispatchesAndAcksOverGRPC(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		jobs: []*pb.ActivatedJob{
			{Key: 111, Type: "greet", Variables: `{"in":1}`},
			{Key: 222, Type: "fail", Retries: 3},
			{Key: 333, Type: "bpmn"},
		},
		completes: make(chan *pb.CompleteJobRequest, 1),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	handler := func(_ context.Context, job *Job) (map[string]any, error) {
		switch job.Type() {
		case "fail":
			return nil, errors.New("boom")
		case "bpmn":
			return nil, &BpmnError{Code: "E1", Message: "nope"}
		default:
			return map[string]any{"ok": true}, nil
		}
	}

	w := client.NewStreamJobWorker("greet", handler, WithStreamPollInterval(-1))
	w.dial = func(context.Context) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()

	comp := waitFor(t, fake.completes)
	if comp.JobKey != 111 {
		t.Errorf("complete JobKey = %d, want 111", comp.JobKey)
	}
	if !strings.Contains(comp.Variables, `"ok"`) {
		t.Errorf("complete Variables = %q, want it to contain \"ok\"", comp.Variables)
	}

	fl := waitFor(t, fake.fails)
	if fl.JobKey != 222 {
		t.Errorf("fail JobKey = %d, want 222", fl.JobKey)
	}
	if fl.Retries != 2 {
		t.Errorf("fail Retries = %d, want 2 (decremented from 3)", fl.Retries)
	}

	th := waitFor(t, fake.throws)
	if th.JobKey != 333 {
		t.Errorf("throw JobKey = %d, want 333", th.JobKey)
	}
	if th.ErrorCode != "E1" {
		t.Errorf("throw ErrorCode = %q, want E1", th.ErrorCode)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not shut down after ctx cancel")
	}
}

func waitFor[T any](t *testing.T, ch chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for gRPC ack")
		panic("unreachable")
	}
}

// bufDial returns a dial seam that connects the streaming worker to an in-memory
// bufconn listener.
func bufDial(lis *bufconn.Listener) func(context.Context) (*grpc.ClientConn, error) {
	return func(context.Context) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
}

// TestStreamJobWorkerForwardsLeaseToken verifies that a job's activation lease
// token is forwarded on the gRPC completion acknowledgement.
func TestStreamJobWorkerForwardsLeaseToken(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	lease := "lease-xyz"
	fake := &fakeGateway{
		jobs:      []*pb.ActivatedJob{{Key: 999, Type: "greet", JobLeaseToken: &lease}},
		completes: make(chan *pb.CompleteJobRequest, 1),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := client.NewStreamJobWorker("greet",
		func(context.Context, *Job) (map[string]any, error) { return nil, nil }, WithStreamPollInterval(-1))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	comp := waitFor(t, fake.completes)
	if comp.JobLeaseToken == nil || *comp.JobLeaseToken != lease {
		t.Errorf("complete LeaseToken = %v, want %q", comp.JobLeaseToken, lease)
	}
}

// TestStreamJobWorkerReconnectsAfterStreamError verifies that the worker reopens
// the stream after it ends with an error and still delivers subsequent jobs.
func TestStreamJobWorkerReconnectsAfterStreamError(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		failFirstStream: 1,
		jobs:            []*pb.ActivatedJob{{Key: 42, Type: "greet"}},
		completes:       make(chan *pb.CompleteJobRequest, 1),
		fails:           make(chan *pb.FailJobRequest, 1),
		throws:          make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := client.NewStreamJobWorker("greet",
		func(context.Context, *Job) (map[string]any, error) { return map[string]any{"ok": true}, nil },
		WithStreamReconnectBackoff(10*time.Millisecond), WithStreamPollInterval(-1))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	comp := waitFor(t, fake.completes)
	if comp.JobKey != 42 {
		t.Errorf("complete JobKey = %d, want 42 (delivered after reconnect)", comp.JobKey)
	}
	if got := fake.streamAttempts.Load(); got < 2 {
		t.Errorf("streamAttempts = %d, want >= 2 (initial failure + reconnect)", got)
	}
}

// sidecarJobResponse is a REST JobActivationResult carrying one job (key 123).
const sidecarJobResponse = `{
  "jobs": [
    {
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
      "jobLeaseToken": null
    }
  ]
}`

// TestStreamJobWorkerSidecarPollActivatesAndAcksOverREST verifies that, while the
// gRPC stream is idle, the REST sidecar poll activates a job over REST and
// acknowledges it over REST.
func TestStreamJobWorkerSidecarPollActivatesAndAcksOverREST(t *testing.T) {
	var activateCount atomic.Int32
	completed := make(chan string, 1)
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs/activation"):
			w.Header().Set("Content-Type", "application/json")
			if activateCount.Add(1) == 1 {
				_, _ = io.WriteString(w, sidecarJobResponse)
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
	defer rest.Close()

	// gRPC stream that never delivers a job (blocks until the worker cancels).
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		completes: make(chan *pb.CompleteJobRequest, 1),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithRestAddress(rest.URL), WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := client.NewStreamJobWorker("demo-task",
		func(context.Context, *Job) (map[string]any, error) { return map[string]any{"ok": true}, nil },
		WithStreamPollInterval(10*time.Millisecond))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	select {
	case path := <-completed:
		if !strings.HasSuffix(path, "/jobs/123/completion") {
			t.Errorf("completion path = %q, want .../jobs/123/completion", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sidecar poll did not activate and complete a job over REST")
	}
}

// TestStreamJobWorkerBoundsConcurrencyToMaxConcurrent verifies that the gRPC
// worker's semaphore caps concurrent handlers at maxConcurrent even when the
// stream pushes many more jobs at once.
func TestStreamJobWorkerBoundsConcurrencyToMaxConcurrent(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	jobs := make([]*pb.ActivatedJob, 6)
	for i := range jobs {
		jobs[i] = &pb.ActivatedJob{Key: int64(1000 + i), Type: "greet"}
	}
	fake := &fakeGateway{
		jobs:      jobs,
		completes: make(chan *pb.CompleteJobRequest, 16),
		fails:     make(chan *pb.FailJobRequest, 16),
		throws:    make(chan *pb.ThrowErrorRequest, 16),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var mu sync.Mutex
	var live, peak int
	release := make(chan struct{})

	w := client.NewStreamJobWorker("greet",
		func(context.Context, *Job) (map[string]any, error) {
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
		WithStreamMaxConcurrentJobs(2), WithStreamPollInterval(-1))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { _ = w.Run(ctx); close(runDone) }()

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
			cancel()
			t.Fatal("worker did not reach two concurrent handlers")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// Give would-be extra handlers time to start if the cap were broken.
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	p := peak
	mu.Unlock()
	if p != 2 {
		t.Errorf("peak concurrency = %d, want 2 (bounded by maxConcurrent)", p)
	}
	close(release)
	cancel()
	<-runDone
}

// TestStreamJobWorkerAcksAfterContextCancel verifies that a streamed job whose
// handler finishes after the worker's context is canceled is still completed
// over gRPC (the ack uses a context detached from Run's lifecycle).
func TestStreamJobWorkerAcksAfterContextCancel(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	started := make(chan struct{})
	proceed := make(chan struct{})
	fake := &fakeGateway{
		jobs:      []*pb.ActivatedJob{{Key: 777, Type: "greet"}},
		completes: make(chan *pb.CompleteJobRequest, 1),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := client.NewStreamJobWorker("greet",
		func(context.Context, *Job) (map[string]any, error) {
			close(started)
			<-proceed
			return map[string]any{"ok": true}, nil
		},
		WithStreamPollInterval(-1))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { _ = w.Run(ctx); close(runDone) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("handler never started")
	}

	cancel()
	close(proceed)

	comp := waitFor(t, fake.completes)
	if comp.JobKey != 777 {
		t.Errorf("complete JobKey = %d, want 777", comp.JobKey)
	}

	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// TestStreamJobWorkerAppliesDefaultTenant verifies that the client's default
// tenant is sent as the stream's tenant filter.
func TestStreamJobWorkerAppliesDefaultTenant(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		streamReqs: make(chan *pb.StreamActivatedJobsRequest, 1),
		completes:  make(chan *pb.CompleteJobRequest, 1),
		fails:      make(chan *pb.FailJobRequest, 1),
		throws:     make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth(), WithLogLevel(LogOff), WithDefaultTenantID("acme"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := client.NewStreamJobWorker("greet",
		func(context.Context, *Job) (map[string]any, error) { return nil, nil },
		WithStreamPollInterval(-1))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	req := waitFor(t, fake.streamReqs)
	if len(req.TenantIds) != 1 || req.TenantIds[0] != "acme" {
		t.Errorf("stream TenantIds = %v, want [acme]", req.TenantIds)
	}
}

// TestStreamJobWorkerRequestsLease verifies the stream request opts into leased
// jobs only when WithStreamJobLease is set. Without the opt-in the engine pushes
// jobs carrying no lease token, so the fencing tokens the worker forwards on
// complete, fail, and throw-error are never issued in the first place.
func TestStreamJobWorkerRequestsLease(t *testing.T) {
	tests := []struct {
		name string
		opts []StreamWorkerOption
		want bool
	}{
		{name: "unleased by default", want: false},
		{name: "enabled", opts: []StreamWorkerOption{WithStreamJobLease(true)}, want: true},
		{name: "explicitly disabled", opts: []StreamWorkerOption{WithStreamJobLease(false)}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lis := bufconn.Listen(1024 * 1024)
			fake := &fakeGateway{
				streamReqs: make(chan *pb.StreamActivatedJobsRequest, 1),
				completes:  make(chan *pb.CompleteJobRequest, 1),
				fails:      make(chan *pb.FailJobRequest, 1),
				throws:     make(chan *pb.ThrowErrorRequest, 1),
			}
			srv := grpc.NewServer()
			pb.RegisterGatewayServer(srv, fake)
			go func() { _ = srv.Serve(lis) }()
			defer srv.Stop()

			client, err := New(WithNoAuth(), WithLogLevel(LogOff))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			w := client.NewStreamJobWorker("greet",
				func(context.Context, *Job) (map[string]any, error) { return nil, nil },
				append([]StreamWorkerOption{WithStreamPollInterval(-1)}, tc.opts...)...)
			w.dial = bufDial(lis)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = w.Run(ctx) }()

			if got := waitFor(t, fake.streamReqs).GetWithLease(); got != tc.want {
				t.Errorf("stream WithLease = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStreamJobWorkerSidecarPollRequestsLease verifies that WithStreamJobLease
// also covers the REST sidecar poll. The poll exists to pick up jobs re-queued
// after a timeout — exactly the case a lease fences — so leaving it unleased
// would leave the race open on the channel most likely to hit it.
func TestStreamJobWorkerSidecarPollRequestsLease(t *testing.T) {
	leases := make(chan bool, 1)
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/jobs/activation") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			WithLease bool `json:"withLease"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		select {
		case leases <- body.WithLease:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jobs":[]}`)
	}))
	defer rest.Close()

	// gRPC stream that never delivers a job, so only the sidecar poll runs.
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		completes: make(chan *pb.CompleteJobRequest, 1),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithRestAddress(rest.URL), WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := client.NewStreamJobWorker("demo-task",
		func(context.Context, *Job) (map[string]any, error) { return nil, nil },
		WithStreamPollInterval(10*time.Millisecond), WithStreamJobLease(true))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	select {
	case got := <-leases:
		if !got {
			t.Error("sidecar poll withLease = false, want true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sidecar poll did not activate jobs")
	}
}

// TestStreamJobWorkerRejectsUnhonoredLease covers the gRPC half of the
// x-present-when contract. A streamed job with no token, after the worker asked for
// one, means the gateway does not support leases; dispatching it would send unfenced
// commands while the caller believed the job was fenced. The worker must end the
// stream instead, so the existing reconnect backoff applies rather than a silent spin.
func TestStreamJobWorkerRejectsUnhonoredLease(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		jobs:      []*pb.ActivatedJob{{Key: 999, Type: "greet"}}, // no JobLeaseToken
		completes: make(chan *pb.CompleteJobRequest, 1),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var handled atomic.Int32
	w := client.NewStreamJobWorker("greet",
		func(context.Context, *Job) (map[string]any, error) {
			handled.Add(1)
			return nil, nil
		},
		WithStreamPollInterval(-1),
		WithStreamReconnectBackoff(time.Millisecond),
		WithStreamJobLease(true))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait for the worker to reopen the stream a few times. Reconnecting is what
	// proves it rejected the job and tore the stream down, rather than simply not
	// having reached it yet.
	deadline := time.After(5 * time.Second)
	for fake.streamAttempts.Load() < 3 {
		select {
		case c := <-fake.completes:
			t.Fatalf("worker acknowledged an unfenced job: %v", c)
		case <-deadline:
			t.Fatalf("stream was attempted %d time(s), want at least 3", fake.streamAttempts.Load())
		case <-time.After(time.Millisecond):
		}
	}

	select {
	case c := <-fake.completes:
		t.Fatalf("worker acknowledged an unfenced job: %v", c)
	default:
	}
	if n := handled.Load(); n != 0 {
		t.Errorf("handler ran %d time(s) for a job whose lease was not honored, want 0", n)
	}
}

// TestStreamJobWorkerSidecarRejectsUnhonoredLease covers the third activation path.
// The sidecar poll activates over REST, so it can reach a lease-unaware server even
// when the gRPC stream is idle, and must refuse an unfenced job on the same terms as
// the other two rather than backfilling one the caller thinks is fenced.
func TestStreamJobWorkerSidecarRejectsUnhonoredLease(t *testing.T) {
	var activations atomic.Int32
	acks := make(chan string, 4)
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobs/activation") {
			activations.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, sidecarJobResponse) // "jobLeaseToken": null
			return
		}
		if strings.Contains(r.URL.Path, "/jobs/") {
			select {
			case acks <- r.URL.Path:
			default:
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer rest.Close()

	// A gRPC stream that never delivers, so anything observed here came from the poll.
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		completes: make(chan *pb.CompleteJobRequest, 1),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithRestAddress(rest.URL), WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var handled atomic.Int32
	w := client.NewStreamJobWorker("demo-task",
		func(context.Context, *Job) (map[string]any, error) {
			handled.Add(1)
			return nil, nil
		},
		WithStreamPollInterval(10*time.Millisecond),
		WithStreamJobLease(true))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for activations.Load() < 3 {
		select {
		case path := <-acks:
			t.Fatalf("sidecar acknowledged an unfenced job at %s", path)
		case <-deadline:
			t.Fatalf("sidecar polled %d time(s), want at least 3", activations.Load())
		case <-time.After(time.Millisecond):
		}
	}

	select {
	case path := <-acks:
		t.Fatalf("sidecar acknowledged an unfenced job at %s", path)
	default:
	}
	if n := handled.Load(); n != 0 {
		t.Errorf("handler ran %d time(s) for a job whose lease was not honored, want 0", n)
	}
}

// TestStreamJobWorkerCancelsRejectedStream verifies the worker closes a stream it
// walks away from. Rejecting a job is the first local return that leaves the loop
// while the stream is still healthy, and StreamActivatedJobs outlives streamOnce
// unless its context is canceled -- so without cancellation each reconnect would
// strand another server-side stream, still able to deliver jobs the worker has
// stopped reading.
func TestStreamJobWorkerCancelsRejectedStream(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		jobs:      []*pb.ActivatedJob{{Key: 999, Type: "greet"}}, // no JobLeaseToken
		completes: make(chan *pb.CompleteJobRequest, 1),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := client.NewStreamJobWorker("greet",
		func(context.Context, *Job) (map[string]any, error) { return nil, nil },
		WithStreamPollInterval(-1),
		WithStreamReconnectBackoff(time.Millisecond),
		WithStreamJobLease(true))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	attemptDeadline := time.After(5 * time.Second)
	for fake.streamAttempts.Load() < 4 {
		select {
		case <-attemptDeadline:
			t.Fatalf("stream was attempted %d time(s), want at least 4", fake.streamAttempts.Load())
		case <-time.After(time.Millisecond):
		}
	}

	// A stranded stream never closes, so the count only climbs; the deadline is a
	// safety net for a slow runner, not the correctness signal.
	deadline := time.Now().Add(5 * time.Second)
	for {
		open := fake.openStreams.Load()
		if open <= 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d server streams still open after %d attempts; rejected streams are being leaked",
				open, fake.streamAttempts.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

// syncBuffer is a concurrency-safe io.Writer so a test can read the SDK log while
// the worker's goroutines write to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestStreamJobWorkerSidecarLogsUnhonoredLeaseLoudly verifies the sidecar's lease
// rejection is visible at the default log level. The sidecar swallows ordinary poll
// failures at Debug because the stream is the primary channel, but a lease-unaware
// server rejects every poll forever, so that one error must surface like the stream
// and REST-worker paths do — otherwise the documented fail-loud behaviour is silent
// on exactly this path.
func TestStreamJobWorkerSidecarLogsUnhonoredLeaseLoudly(t *testing.T) {
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobs/activation") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, sidecarJobResponse) // "jobLeaseToken": null
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer rest.Close()

	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		completes: make(chan *pb.CompleteJobRequest, 1),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithRestAddress(rest.URL), WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Default level is Info; a Debug record would not show, so a captured line proves
	// the rejection was raised above Debug rather than merely emitted somewhere.
	logs := &syncBuffer{}
	client.logger = diag.New(diag.LevelInfo, logs, client.clock)

	w := client.NewStreamJobWorker("demo-task",
		func(context.Context, *Job) (map[string]any, error) { return nil, nil },
		WithStreamPollInterval(10*time.Millisecond),
		WithStreamJobLease(true))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for {
		if s := logs.String(); strings.Contains(s, "lease") && strings.Contains(s, "WARN") {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("no lease-rejection warning at default log level; captured:\n%s", logs.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
}
