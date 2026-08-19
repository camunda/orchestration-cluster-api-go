package camunda

import (
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
}

func (f *fakeGateway) StreamActivatedJobs(req *pb.StreamActivatedJobsRequest, stream grpc.ServerStreamingServer[pb.ActivatedJob]) error {
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
		jobs:      []*pb.ActivatedJob{{Key: 999, Type: "greet", LeaseToken: &lease}},
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
	if comp.LeaseToken == nil || *comp.LeaseToken != lease {
		t.Errorf("complete LeaseToken = %v, want %q", comp.LeaseToken, lease)
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
      "leaseToken": null
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
