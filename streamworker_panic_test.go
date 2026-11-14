package camunda

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/camunda/orchestration-cluster-api-go/v11/internal/diag"
	"github.com/camunda/orchestration-cluster-api-go/v11/pb"

	"google.golang.org/grpc/test/bufconn"
)

// streamPanicSentinel is the value the handlers below panic with, distinctive so
// a test can prove where it does and does not travel.
const streamPanicSentinel = "CAMUNDA-STREAM-PANIC-SENTINEL-4c71"

// TestInvokeHandlerConvertsPanicToError is the unit-level contract the four
// dispatch sites rely on: a panicking handler becomes an ordinary error
// matching ErrHandlerPanic, with nothing returned alongside it.
func TestInvokeHandlerConvertsPanicToError(t *testing.T) {
	client, err := New(WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	job := &Job{key: "123", jobType: "demo-task"}

	tests := map[string]JobHandler{
		"panic with a string": func(context.Context, *Job) (map[string]any, error) {
			panic(streamPanicSentinel)
		},
		"panic with an error": func(context.Context, *Job) (map[string]any, error) {
			panic(errors.New(streamPanicSentinel))
		},
		"panic with nil map write": func(context.Context, *Job) (map[string]any, error) {
			nilIntMap()["boom"] = 1
			return nil, nil
		},
		"panic after producing variables": func(context.Context, *Job) (map[string]any, error) {
			defer panic(streamPanicSentinel)
			return map[string]any{"half": "done"}, nil
		},
	}

	for name, h := range tests {
		t.Run(name, func(t *testing.T) {
			vars, err := client.invokeHandler(context.Background(), h, job)
			if err == nil {
				t.Fatal("invokeHandler returned no error for a panicking handler")
			}
			if !errors.Is(err, ErrHandlerPanic) {
				t.Errorf("error %v does not match ErrHandlerPanic", err)
			}
			if vars != nil {
				t.Errorf("variables = %v, want nil for a panicking handler", vars)
			}
			if strings.Contains(err.Error(), streamPanicSentinel) {
				t.Errorf("error message carries the panic value: %q", err)
			}
		})
	}
}

// TestInvokeHandlerPassesThroughNormalOutcomes is the companion guard: recovery
// must not disturb a handler that behaves.
func TestInvokeHandlerPassesThroughNormalOutcomes(t *testing.T) {
	client, err := New(WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	job := &Job{key: "123", jobType: "demo-task"}

	vars, err := client.invokeHandler(context.Background(),
		func(context.Context, *Job) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		}, job)
	if err != nil {
		t.Fatalf("invokeHandler: %v", err)
	}
	if vars["ok"] != true {
		t.Errorf("variables = %v, want the handler's own result", vars)
	}

	sentinel := errors.New("ordinary failure")
	if _, err := client.invokeHandler(context.Background(),
		func(context.Context, *Job) (map[string]any, error) { return nil, sentinel }, job); !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want the handler's own error", err)
	}

	bpmn := &BpmnError{Code: "E1"}
	_, err = client.invokeHandler(context.Background(),
		func(context.Context, *Job) (map[string]any, error) { return nil, bpmn }, job)
	var got *BpmnError
	if !errors.As(err, &got) {
		t.Errorf("error %v is no longer a *BpmnError; throw-error routing would break", err)
	}
}

// TestInvokeHandlerLogsThePanicValueAndStack pins where the detail goes. It must
// not reach the cluster, but it must not vanish either: without the value and a
// stack, a recovered panic is undiagnosable — and before this change the runtime
// would have printed exactly that to stderr as the process died.
func TestInvokeHandlerLogsThePanicValueAndStack(t *testing.T) {
	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logs := &syncBuffer{}
	client.logger = diag.New(diag.LevelError, logs, client.clock)

	_, err = client.invokeHandler(context.Background(),
		func(context.Context, *Job) (map[string]any, error) { panic(streamPanicSentinel) },
		&Job{key: "123", jobType: "demo-task"})
	if err == nil {
		t.Fatal("expected an error")
	}

	out := logs.String()
	if !strings.Contains(out, streamPanicSentinel) {
		t.Errorf("the panic value was not logged: %s", out)
	}
	// Logged at error level, so it is visible at every level except off.
	if !strings.Contains(out, "ERROR") {
		t.Errorf("the panic was not logged at error level: %s", out)
	}
	if !strings.Contains(out, "invokeHandler") && !strings.Contains(out, "goroutine") {
		t.Errorf("no stack was logged with the panic: %s", out)
	}
	if !strings.Contains(out, `job="123"`) {
		t.Errorf("the log record does not identify the job: %s", out)
	}
}

// TestInvokeHandlerLoggingResistsRecordInjection pins the escaping. The logger
// writes field values verbatim, one record per line, so a panic value carrying
// newlines — and a panic value can be job-variable content, which is untrusted
// input — would otherwise let a handler forge log records.
func TestInvokeHandlerLoggingResistsRecordInjection(t *testing.T) {
	const forged = "\n2026-01-01T00:00:00Z [ERROR] camunda cluster credentials rotated"

	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logs := &syncBuffer{}
	client.logger = diag.New(diag.LevelError, logs, client.clock)

	_, err = client.invokeHandler(context.Background(),
		func(context.Context, *Job) (map[string]any, error) {
			panic("legitimate detail" + forged)
		},
		&Job{key: "123\nalso-forged", jobType: "demo\ntask"})
	if err == nil {
		t.Fatal("expected an error")
	}

	out := logs.String()
	// Exactly one record: the panic must not have produced extra lines.
	if got := strings.Count(strings.TrimRight(out, "\n"), "\n"); got != 0 {
		t.Errorf("the panic injected %d extra log line(s):\n%s", got, out)
	}
	if strings.Contains(out, forged) {
		t.Errorf("an unescaped newline from the panic value reached the log:\n%s", out)
	}
	// The detail is still there, escaped rather than dropped.
	if !strings.Contains(out, "legitimate detail") {
		t.Errorf("escaping dropped the panic detail:\n%s", out)
	}
}

// TestStreamJobWorkerSurvivesHandlerPanicOverGRPC covers the gRPC stream
// dispatch site: the panicking job is failed over gRPC with its retries
// decremented, the stream stays up, and the next streamed job is still handled.
func TestStreamJobWorkerSurvivesHandlerPanicOverGRPC(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		jobs: []*pb.ActivatedJob{
			{Key: 111, Type: "greet", Retries: 3},
			{Key: 222, Type: "greet", Retries: 3},
		},
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

	w := client.NewStreamJobWorker("greet", func(_ context.Context, job *Job) (map[string]any, error) {
		if job.Key() == "111" {
			panic(streamPanicSentinel)
		}
		return map[string]any{"ok": true}, nil
	}, WithStreamPollInterval(-1))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	fl := waitFor(t, fake.fails)
	if fl.JobKey != 111 {
		t.Errorf("fail JobKey = %d, want 111", fl.JobKey)
	}
	if fl.Retries != 2 {
		t.Errorf("fail Retries = %d, want 2 (decremented from 3)", fl.Retries)
	}
	if !strings.Contains(fl.ErrorMessage, "panicked") {
		t.Errorf("ErrorMessage %q does not identify a panic", fl.ErrorMessage)
	}
	if strings.Contains(fl.ErrorMessage, streamPanicSentinel) {
		t.Errorf("ErrorMessage leaked the panic value: %q", fl.ErrorMessage)
	}

	// The worker survived: the second streamed job is still dispatched.
	comp := waitFor(t, fake.completes)
	if comp.JobKey != 222 {
		t.Errorf("complete JobKey = %d, want 222", comp.JobKey)
	}
}

// TestStreamJobWorkerSidecarSurvivesHandlerPanic covers the fourth dispatch
// site, the REST sidecar poll inside the streaming worker. It has its own
// inlined handler call and acknowledges over REST, so it needs its own test.
func TestStreamJobWorkerSidecarSurvivesHandlerPanic(t *testing.T) {
	var activateCount atomic.Int32
	failures := make(chan failureBody, 1)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs/activation"):
			w.Header().Set("Content-Type", "application/json")
			if activateCount.Add(1) == 1 {
				_, _ = io.WriteString(w, sidecarJobResponse)
			} else {
				_, _ = io.WriteString(w, `{"jobs":[]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/failure"):
			var body failureBody
			_ = json.NewDecoder(r.Body).Decode(&body)
			select {
			case failures <- body:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer rest.Close()

	// A gRPC stream that never delivers a job, so only the sidecar is exercised.
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
		func(context.Context, *Job) (map[string]any, error) { panic(streamPanicSentinel) },
		WithStreamPollInterval(10*time.Millisecond))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	select {
	case got := <-failures:
		if got.Retries != 2 {
			t.Errorf("retries = %d, want 2", got.Retries)
		}
		if !strings.Contains(got.ErrorMessage, "panicked") {
			t.Errorf("errorMessage %q does not identify a panic", got.ErrorMessage)
		}
		if strings.Contains(got.ErrorMessage, streamPanicSentinel) {
			t.Errorf("errorMessage leaked the panic value: %q", got.ErrorMessage)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sidecar poll did not fail the job after the handler panicked")
	}

	// The sidecar must keep polling rather than having died with the handler.
	before := activateCount.Load()
	deadline := time.After(3 * time.Second)
	for activateCount.Load() <= before {
		select {
		case <-deadline:
			t.Fatal("sidecar stopped polling after the handler panicked")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// nilIntMap returns a nil map. Handlers meet one as a helper's return value or
// an unpopulated struct field rather than as an obvious local.
func nilIntMap() map[string]int { return nil }

// failureBody is the body of a REST job-failure request.
type failureBody struct {
	Retries      int32  `json:"retries"`
	ErrorMessage string `json:"errorMessage"`
}
