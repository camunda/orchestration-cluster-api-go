package camunda

import (
	"context"
	"errors"
	"net"
	"strings"
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

	// failFirstStream is the number of initial StreamActivatedJobs attempts to
	// fail (with codes.Unavailable) before serving jobs; streamAttempts counts
	// how many stream attempts the server has seen.
	failFirstStream int32
	streamAttempts  atomic.Int32
}

func (f *fakeGateway) StreamActivatedJobs(_ *pb.StreamActivatedJobsRequest, stream grpc.ServerStreamingServer[pb.ActivatedJob]) error {
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

	w := client.NewStreamJobWorker("greet", handler)
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
		func(context.Context, *Job) (map[string]any, error) { return nil, nil })
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
		WithStreamReconnectBackoff(10*time.Millisecond))
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
