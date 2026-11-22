package camunda_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	camunda "github.com/camunda/orchestration-cluster-api-go/v11"
)

// panicSentinel is the value the handlers below panic with. It is distinctive so
// a test can prove it reached the SDK log and did not reach the cluster.
const panicSentinel = "CAMUNDA-PANIC-SENTINEL-4c71"

// nilMap returns a nil map. Handlers meet one as a helper's return value or an
// unpopulated struct field rather than as an obvious local, which is the shape
// worth reproducing here.
func nilMap() map[string]int { return nil }

// failureRequest is the body of a REST job-failure request.
type failureRequest struct {
	Retries      int32  `json:"retries"`
	ErrorMessage string `json:"errorMessage"`
}

// TestJobWorkerSurvivesHandlerPanic covers the REST polling dispatch path. A
// panicking handler used to take the whole host process down, because in Go an
// unrecovered panic on any goroutine terminates the program and every handler
// runs on its own goroutine. The worker must instead fail the job and keep
// going.
//
// The subtests cover the class of handler panic rather than one instance: an
// explicit panic, a nil map write, an out-of-range index, a failed type
// assertion on a job variable, and a nil-pointer dereference are all routes a
// real handler reaches.
func TestJobWorkerSurvivesHandlerPanic(t *testing.T) {
	panics := map[string]func(job *camunda.Job){
		"explicit panic": func(*camunda.Job) { panic(panicSentinel) },
		"explicit error panic": func(*camunda.Job) {
			panic(io.ErrUnexpectedEOF)
		},
		"nil map write": func(*camunda.Job) {
			nilMap()["boom"] = 1
		},
		"index out of range": func(*camunda.Job) {
			s := []int{}
			_ = s[3]
		},
		"type assertion on a job variable": func(job *camunda.Job) {
			_ = job.RawVariables()["amount"].(string)
		},
		"nil pointer dereference": func(*camunda.Job) {
			type t struct{ n int }
			var p *t
			_ = p.n
		},
	}

	for name, boom := range panics {
		t.Run(name, func(t *testing.T) {
			var activateCount atomic.Int32
			failures := make(chan failureRequest, 1)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/jobs/activation"):
					w.Header().Set("Content-Type", "application/json")
					// Serve the job once, then keep polling empty, so a second
					// delivery cannot mask a dead worker.
					if activateCount.Add(1) == 1 {
						_, _ = io.WriteString(w, oneJobResponse)
					} else {
						_, _ = io.WriteString(w, `{"jobs":[]}`)
					}
				case strings.HasSuffix(r.URL.Path, "/jobs/123/failure"):
					var body failureRequest
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
			defer srv.Close()

			client, err := camunda.New(camunda.WithRestAddress(srv.URL), camunda.WithLogLevel(camunda.LogOff))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			worker := client.NewJobWorker("demo-task",
				func(_ context.Context, job *camunda.Job) (map[string]any, error) {
					boom(job)
					return map[string]any{"unreachable": true}, nil
				},
				camunda.WithRequestTimeout(50*time.Millisecond),
				camunda.WithPollInterval(10*time.Millisecond),
			)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = worker.Run(ctx) }()

			select {
			case got := <-failures:
				// The job is failed like any other handler error, so retries
				// decrement (3 -> 2) and the engine schedules a retry.
				if got.Retries != 2 {
					t.Errorf("retries = %d, want 2", got.Retries)
				}
				if !strings.Contains(got.ErrorMessage, "panicked") {
					t.Errorf("errorMessage %q does not identify a panic", got.ErrorMessage)
				}
				// The panic value is not sent to the cluster: the incident
				// message is durable and widely visible, and a panic value can
				// carry job-variable content.
				if strings.Contains(got.ErrorMessage, panicSentinel) {
					t.Errorf("errorMessage leaked the panic value: %q", got.ErrorMessage)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("job was not failed after the handler panicked")
			}

			// The worker must still be polling, not dead.
			before := activateCount.Load()
			deadline := time.After(3 * time.Second)
			for activateCount.Load() <= before {
				select {
				case <-deadline:
					t.Fatal("worker stopped polling after the handler panicked")
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	}
}

// TestJobWorkerDispatchesAgainAfterHandlerPanic pins recovery of the worker
// itself rather than of one job: a second job must still be delivered to the
// handler after the first one panicked.
func TestJobWorkerDispatchesAgainAfterHandlerPanic(t *testing.T) {
	var activateCount atomic.Int32
	handled := make(chan string, 4)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobs/activation") {
			w.Header().Set("Content-Type", "application/json")
			// Deliver the job on the first two polls, then go quiet.
			if activateCount.Add(1) <= 2 {
				_, _ = io.WriteString(w, oneJobResponse)
			} else {
				_, _ = io.WriteString(w, `{"jobs":[]}`)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client, err := camunda.New(camunda.WithRestAddress(srv.URL), camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var seen atomic.Int32
	worker := client.NewJobWorker("demo-task",
		func(_ context.Context, job *camunda.Job) (map[string]any, error) {
			select {
			case handled <- job.Key():
			default:
			}
			// Panic on the first job; succeed on the second.
			if seen.Add(1) == 1 {
				panic(panicSentinel)
			}
			return nil, nil
		},
		camunda.WithRequestTimeout(50*time.Millisecond),
		camunda.WithPollInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	for i := range 2 {
		select {
		case <-handled:
		case <-time.After(5 * time.Second):
			t.Fatalf("handler was invoked only %d time(s); the worker did not survive the panic", i)
		}
	}
}

// TestJobWorkerSurvivesHandlerPanicOverFalcon covers the FALCON command-stream
// dispatch path, which has its own handler call site and its own acknowledgement
// route (a failJob frame rather than a REST request).
func TestJobWorkerSurvivesHandlerPanicOverFalcon(t *testing.T) {
	failed := make(chan map[string]any, 1)
	srv := falconGateway(t, nil, func(ctx context.Context, c *websocket.Conn) {
		sub, err := wsReadJSON(ctx, c)
		if err != nil || sub["type"] != "subscribe" {
			return
		}
		wsWriteJSON(ctx, c, map[string]any{"type": "welcome", "heartbeatMs": 15000})
		_ = c.Write(ctx, websocket.MessageText, []byte(pushedJobFrame))
		for {
			f, err := wsReadJSON(ctx, c)
			if err != nil {
				return
			}
			if f["type"] == "failJob" {
				select {
				case failed <- f:
				default:
				}
			}
		}
	})

	c, err := camunda.New(camunda.WithRestAddress(srv.URL+"/v2"), camunda.WithFalcon(true),
		camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	worker := c.NewJobWorker("email", func(context.Context, *camunda.Job) (map[string]any, error) {
		panic(panicSentinel)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	select {
	case frame := <-failed:
		if got := frame["jobKey"]; got != "2251799813685424" {
			t.Errorf("failed wrong job: %v", got)
		}
		// retries 3 -> 2, same as any other handler error.
		if got, ok := frame["retries"].(float64); !ok || got != 2 {
			t.Errorf("retries = %v, want 2", frame["retries"])
		}
		msg, _ := frame["errorMessage"].(string)
		if !strings.Contains(msg, "panicked") {
			t.Errorf("errorMessage %q does not identify a panic", msg)
		}
		if strings.Contains(msg, panicSentinel) {
			t.Errorf("errorMessage leaked the panic value: %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job was not failed over the command stream after the handler panicked")
	}
}
