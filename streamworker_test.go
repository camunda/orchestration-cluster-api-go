package camunda

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/camunda/orchestration-cluster-api-go/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
}

func (f *fakeGateway) StreamActivatedJobs(_ *pb.StreamActivatedJobsRequest, stream grpc.ServerStreamingServer[pb.ActivatedJob]) error {
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
