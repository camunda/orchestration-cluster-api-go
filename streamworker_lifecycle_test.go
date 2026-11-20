package camunda

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/camunda/orchestration-cluster-api-go/v11/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// seqGateway sends one job per stream, keyed by the stream ordinal (1, 2, ...),
// then holds the stream open until the client cancels it. With `silent` set it
// sends nothing, so only the inactivity watchdog can end a stream. sent2 closes
// when stream 2's job has been sent, so a test can synchronize on a second
// stream's job being in flight.
type seqGateway struct {
	pb.UnimplementedGatewayServer
	silent    bool
	streams   atomic.Int32
	sent2     chan struct{}
	sent2Once sync.Once
	mu        sync.Mutex
	completed map[int64]int
	completes chan int64
}

func (g *seqGateway) StreamActivatedJobs(_ *pb.StreamActivatedJobsRequest, stream grpc.ServerStreamingServer[pb.ActivatedJob]) error {
	n := g.streams.Add(1)
	if !g.silent {
		if err := stream.Send(&pb.ActivatedJob{Key: int64(n), Type: "t"}); err != nil {
			return err
		}
		if n == 2 && g.sent2 != nil {
			g.sent2Once.Do(func() { close(g.sent2) })
		}
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

func (g *seqGateway) CompleteJob(_ context.Context, req *pb.CompleteJobRequest) (*pb.CompleteJobResponse, error) {
	g.mu.Lock()
	g.completed[req.JobKey]++
	g.mu.Unlock()
	g.completes <- req.JobKey
	return &pb.CompleteJobResponse{}, nil
}

// waitForSignal fails the test unless ch is closed within 5 seconds.
func waitForSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// waitForStreams fails the test unless the gateway has seen at least n streams
// within 5 seconds.
func waitForStreams(t *testing.T, g *seqGateway, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for g.streams.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("streams = %d, want >= %d (watchdog recycles and reopens)", g.streams.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startSeqWorker(t *testing.T, g *seqGateway, opts ...StreamWorkerOption) context.CancelFunc {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, g)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	opts = append([]StreamWorkerOption{WithStreamPollInterval(-1), WithStreamReconnectBackoff(time.Hour)}, opts...)
	w := client.NewStreamJobWorker("t",
		func(context.Context, *Job) (map[string]any, error) { return nil, nil }, opts...)
	w.dial = bufDial(lis)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = w.Run(ctx) }()
	t.Cleanup(cancel)
	return cancel
}

// A stream that reaches its max lifetime is reopened at once (the 1h reconnect
// backoff is skipped), and every job is completed exactly once.
func TestStreamJobWorkerRecyclesAtMaxLifetime(t *testing.T) {
	g := &seqGateway{completed: map[int64]int{}, completes: make(chan int64, 16)}
	startSeqWorker(t, g, WithStreamMaxLifetime(50*time.Millisecond), WithStreamInactivityTimeout(0))
	// Jobs run concurrently, so completions may arrive in any order.
	// One overall deadline: later jobs completing must not mask a stranded one.
	seen := map[int64]bool{}
	deadline := time.After(5 * time.Second)
	for len(seen) < 3 {
		select {
		case got := <-g.completes:
			if got >= 1 && got <= 3 {
				seen[got] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for jobs 1-3 to complete; got %v", seen)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, n := range g.completed {
		if n != 1 {
			t.Errorf("job %d completed %d times, want 1", k, n)
		}
	}
}

// The watchdog recycles a silent stream after the inactivity timeout.
func TestStreamJobWorkerInactivityWatchdogFires(t *testing.T) {
	g := &seqGateway{silent: true, completed: map[int64]int{}, completes: make(chan int64, 1)}
	startSeqWorker(t, g, WithStreamMaxLifetime(0), WithStreamInactivityTimeout(30*time.Millisecond))
	waitForStreams(t, g, 3)
}

// With both safeguards disabled a stream is never recycled.
func TestStreamJobWorkerLifecycleDisabled(t *testing.T) {
	g := &seqGateway{completed: map[int64]int{}, completes: make(chan int64, 4)}
	startSeqWorker(t, g, WithStreamMaxLifetime(0), WithStreamInactivityTimeout(0))
	waitFor(t, g.completes)
	time.Sleep(150 * time.Millisecond)
	if n := g.streams.Load(); n != 1 {
		t.Errorf("streams = %d, want 1", n)
	}
}

// The watchdog resets on each job: jobs arriving faster than the timeout keep
// the stream alive.
func TestStreamJobWorkerWatchdogResetsOnJob(t *testing.T) {
	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatal(err)
	}
	w := client.NewStreamJobWorker("t", nil, WithStreamMaxLifetime(0), WithStreamInactivityTimeout(80*time.Millisecond))
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	activity := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); w.watchStream(ctx, cancel, activity) }()
	for i := 0; i < 10; i++ {
		time.Sleep(20 * time.Millisecond)
		// Bound the send: if the watchdog has already exited (a regression
		// firing the deadline despite activity), an unbuffered send here would
		// block forever, so fail on the cancelled context instead.
		select {
		case activity <- struct{}{}:
		case <-ctx.Done():
			t.Fatalf("watchdog exited during activity: %v", context.Cause(ctx))
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("stream recycled despite activity: %v", context.Cause(ctx))
	}
	// Bound the wait: if a regression prevents the deadline from ever firing,
	// fail with a diagnostic instead of hanging.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not fire after activity stopped")
	}
	if !errors.Is(context.Cause(ctx), errStreamRecycled) {
		t.Fatalf("cause = %v, want errStreamRecycled", context.Cause(ctx))
	}
}

func TestStreamJobWorkerRejectsInactivityNotBelowLifetime(t *testing.T) {
	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatal(err)
	}
	w := client.NewStreamJobWorker("t", nil, WithStreamMaxLifetime(time.Minute), WithStreamInactivityTimeout(time.Minute))
	if err := w.Run(context.Background()); !errors.Is(err, ErrConfig) {
		t.Fatalf("Run = %v, want ErrConfig", err)
	}
}

// Jittered deadlines of many workers spread across [0.9d, d].
func TestJitterDeadlineSpreadsAcrossWindow(t *testing.T) {
	d := 8 * time.Hour
	r := rngAdapter{rand.New(rand.NewPCG(1, 2))}
	lo, hi := d, time.Duration(0)
	for i := 0; i < 1000; i++ {
		got := jitterDeadline(r, d)
		if got < d*9/10 || got > d {
			t.Fatalf("deadline %s outside [0.9d, d]", got)
		}
		lo, hi = min(lo, got), max(hi, got)
	}
	if hi-lo < d/20 {
		t.Errorf("deadlines spread over %s, want most of the %s window", hi-lo, d/10)
	}
	if jitterDeadline(r, 0) != 0 {
		t.Error("disabled deadline should stay 0")
	}
}

type rngAdapter struct{ r *rand.Rand }

func (a rngAdapter) Float64() float64 { return a.r.Float64() }

// recordingController counts engine-clock pin requests.
type recordingController struct{ pins atomic.Int32 }

func (r *recordingController) PinAt(context.Context, time.Time) error {
	r.pins.Add(1)
	return nil
}

func (r *recordingController) ResetToLive(context.Context) error { return nil }

// The stream-lifecycle watchdogs bound a transport, so they run on real time
// even when the client's clock is an EngineClock: they must neither advance the
// engine clock nor fire early because of one.
func TestStreamJobWorkerWatchdogUsesRealTimeWithEngineClock(t *testing.T) {
	ctrl := &recordingController{}
	client, err := New(WithNoAuth(), WithClock(NewEngineClock(ctrl)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	w := client.NewStreamJobWorker("t", nil,
		WithStreamMaxLifetime(40*time.Millisecond), WithStreamInactivityTimeout(0))
	done := make(chan struct{})
	go func() { defer close(done); w.watchStream(ctx, cancel, make(chan struct{})) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not fire in real time")
	}
	if !errors.Is(context.Cause(ctx), errStreamRecycled) {
		t.Fatalf("cause = %v, want errStreamRecycled", context.Cause(ctx))
	}
	if n := ctrl.pins.Load(); n != 0 {
		t.Errorf("engine clock pinned %d times by a transport watchdog, want 0", n)
	}
}

// A saturated worker keeps reopening a recycled stream: the capacity wait
// selects on the worker context, so a recycle propagates and Run reopens at
// once even while a received job is parked behind a busy handler.
func TestStreamJobWorkerRecyclesWhileSaturated(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	release := make(chan struct{})
	handlerStarted := make(chan struct{})
	var handlerOnce sync.Once
	g := &seqGateway{sent2: make(chan struct{}), completed: map[int64]int{}, completes: make(chan int64, 16)}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, g)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := client.NewStreamJobWorker("t",
		func(context.Context, *Job) (map[string]any, error) {
			handlerOnce.Do(func() { close(handlerStarted) })
			<-release
			return nil, nil
		},
		WithStreamMaxConcurrentJobs(1),
		WithStreamMaxLifetime(50*time.Millisecond),
		WithStreamInactivityTimeout(0),
		WithStreamPollInterval(-1),
		WithStreamReconnectBackoff(time.Hour))
	w.dial = bufDial(lis)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()

	// Stream 1 delivers job 1, which occupies the only handler slot.
	waitForSignal(t, handlerStarted, "handler to start on stream 1's job")
	// The max lifetime recycles stream 1; stream 2 opens and delivers job 2,
	// which parks on the busy handler slot.
	waitForSignal(t, g.sent2, "stream 2's job to arrive while the handler slot is busy")
	// With a received job waiting on capacity, stream 2 is recycled in turn and
	// stream 3 must still open at once -- the reopen must not wait for the
	// busy handler.
	waitForStreams(t, g, 3)
	close(release)
}
