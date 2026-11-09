package camunda_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go/v11"
	"github.com/coder/websocket"
)

// captureRESTAck runs a REST JobWorker against a stub server that activates one
// job, and returns the path and decoded JSON body of the first acknowledgement.
func captureRESTAck(t *testing.T, handler camunda.JobHandler) (string, map[string]any) {
	t.Helper()
	type ack struct {
		path string
		body map[string]any
	}
	acks := make(chan ack, 1)
	var activateCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs/activation"):
			w.Header().Set("Content-Type", "application/json")
			if activateCount.Add(1) == 1 {
				_, _ = io.WriteString(w, oneJobResponse)
			} else {
				_, _ = io.WriteString(w, `{"jobs":[]}`)
			}
		case strings.Contains(r.URL.Path, "/jobs/123/"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode ack body: %v", err)
			}
			select {
			case acks <- ack{r.URL.Path, body}:
			default:
			}
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
	worker := client.NewJobWorker("demo-task", handler,
		camunda.WithRequestTimeout(50*time.Millisecond),
		camunda.WithPollInterval(10*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	select {
	case a := <-acks:
		return a.path, a.body
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not acknowledge the job")
		return "", nil
	}
}

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad expected JSON %q: %v", s, err)
	}
	return m
}

// completeWith adapts a fixed JobCompletion into a JobHandler.
func completeWith(c camunda.JobCompletion) camunda.JobHandler {
	return camunda.CompletionHandler(func(context.Context, *camunda.Job) (camunda.JobCompletion, error) {
		return c, nil
	})
}

func TestJobWorkerCompletionRequestBody(t *testing.T) {
	corrections := camunda.NewJobResultCorrections()
	corrections.SetAssignee("corrected-user")
	corrections.SetPriority(80)
	corrections.SetDueDate(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	corrections.SetCandidateGroups([]string{"reviewers"})
	corrections.SetCandidateUsers([]string{}) // non-nil empty slice clears the list

	adHoc := camunda.NewJobResultAdHocSubProcess()
	adHoc.SetActivateElements([]camunda.JobResultActivateElement{
		camunda.ActivateAdHocElement("task_a", map[string]any{"input": "x"}),
		camunda.ActivateAdHocElement("task_b", nil),
	})
	adHoc.SetIsCompletionConditionFulfilled(true)
	adHoc.SetIsCancelRemainingInstances(false)

	// An ad-hoc result built with the generated union wrapper and no discriminator
	// (the generated constructor defaults both flags to false):
	// the server would default it to "userTask", so the worker must stamp it.
	rawAdHoc := camunda.NewJobResultAdHocSubProcess()
	rawAdHoc.SetIsCompletionConditionFulfilled(true)
	unstamped := camunda.JobResultAdHocSubProcessAsJobResult(rawAdHoc)

	// A discriminator that contradicts the populated variant is overwritten.
	mislabeled := camunda.NewJobResultUserTask()
	mislabeled.SetDenied(true)
	mislabeled.SetType("adHocSubProcess")
	mislabeledResult := camunda.JobResultUserTaskAsJobResult(mislabeled)

	tests := []struct {
		name    string
		handler camunda.JobHandler
		want    string
	}{
		{
			name: "plain JobHandler sends no result",
			handler: func(context.Context, *camunda.Job) (map[string]any, error) {
				return map[string]any{"done": true}, nil
			},
			want: `{"variables":{"done":true}}`,
		},
		{
			name:    "CompletionHandler without a result sends no result",
			handler: completeWith(camunda.JobCompletion{Variables: map[string]any{"done": true}}),
			want:    `{"variables":{"done":true}}`,
		},
		{
			name:    "user task corrections",
			handler: completeWith(camunda.JobCompletion{Result: camunda.CorrectUserTask(*corrections)}),
			want: `{"result":{"type":"userTask","corrections":{
				"assignee":"corrected-user","priority":80,"dueDate":"2026-01-02T03:04:05Z",
				"candidateGroups":["reviewers"],"candidateUsers":[]}}}`,
		},
		{
			name:    "user task denial with reason",
			handler: completeWith(camunda.JobCompletion{Result: camunda.DenyUserTask("Insufficient documentation")}),
			want:    `{"result":{"type":"userTask","denied":true,"deniedReason":"Insufficient documentation"}}`,
		},
		{
			name:    "user task denial without reason",
			handler: completeWith(camunda.JobCompletion{Result: camunda.DenyUserTask("")}),
			want:    `{"result":{"type":"userTask","denied":true}}`,
		},
		{
			name: "ad-hoc sub-process activateElements and flags",
			handler: completeWith(camunda.JobCompletion{
				Variables: map[string]any{"round": 1},
				Result:    camunda.AdHocSubProcessJobResult(*adHoc),
			}),
			want: `{"variables":{"round":1},"result":{"type":"adHocSubProcess",
				"activateElements":[{"elementId":"task_a","variables":{"input":"x"}},{"elementId":"task_b"}],
				"isCompletionConditionFulfilled":true,"isCancelRemainingInstances":false}}`,
		},
		{
			name:    "generated union without discriminator is stamped",
			handler: completeWith(camunda.JobCompletion{Result: &unstamped}),
			want:    `{"result":{"type":"adHocSubProcess","isCompletionConditionFulfilled":true,"isCancelRemainingInstances":false}}`,
		},
		{
			name:    "contradicting discriminator is corrected",
			handler: completeWith(camunda.JobCompletion{Result: &mislabeledResult}),
			want:    `{"result":{"type":"userTask","denied":true}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, body := captureRESTAck(t, tc.handler)
			if !strings.HasSuffix(path, "/jobs/123/completion") {
				t.Fatalf("ack path = %q, want .../jobs/123/completion", path)
			}
			if want := mustJSON(t, tc.want); !reflect.DeepEqual(body, want) {
				got, _ := json.Marshal(body)
				t.Errorf("completion body =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}

	if mislabeled.GetType() != "adHocSubProcess" {
		t.Error("stamping the discriminator mutated the caller's result")
	}
}

// TestJobWorkerCorrectionDatesWireBody pins the three states of a correction date
// on the REST wire: cleared is "", set is the ISO-8601 instant, unset is omitted.
func TestJobWorkerCorrectionDatesWireBody(t *testing.T) {
	due := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name  string
		build func(c *camunda.JobResultCorrections)
		want  string
	}{
		{
			name:  "cleared dueDate",
			build: func(c *camunda.JobResultCorrections) { c.ClearDueDate() },
			want:  `{"dueDate":""}`,
		},
		{
			name:  "cleared followUpDate",
			build: func(c *camunda.JobResultCorrections) { c.ClearFollowUpDate() },
			want:  `{"followUpDate":""}`,
		},
		{
			name: "set dates keep the ISO-8601 form",
			build: func(c *camunda.JobResultCorrections) {
				c.SetDueDate(due)
				c.SetFollowUpDate(due.Add(90 * time.Minute))
			},
			want: `{"dueDate":"2026-01-02T03:04:05Z","followUpDate":"2026-01-02T04:34:05Z"}`,
		},
		{
			name:  "unset dates are omitted",
			build: func(c *camunda.JobResultCorrections) { c.SetAssignee("corrected-user") },
			want:  `{"assignee":"corrected-user"}`,
		},
		{
			name: "clear alongside other corrections",
			build: func(c *camunda.JobResultCorrections) {
				c.SetAssignee("corrected-user")
				c.ClearDueDate()
				c.SetFollowUpDate(due)
				c.SetCandidateGroups([]string{"reviewers"})
				c.SetPriority(80)
			},
			want: `{"assignee":"corrected-user","dueDate":"","followUpDate":"2026-01-02T03:04:05Z",
				"candidateGroups":["reviewers"],"priority":80}`,
		},
		{
			name: "set after clear wins",
			build: func(c *camunda.JobResultCorrections) {
				c.ClearDueDate()
				c.SetDueDate(due)
			},
			want: `{"dueDate":"2026-01-02T03:04:05Z"}`,
		},
		{
			name: "clear after set wins",
			build: func(c *camunda.JobResultCorrections) {
				c.SetDueDate(due)
				c.ClearDueDate()
			},
			want: `{"dueDate":""}`,
		},
		{
			name: "unset after clear omits",
			build: func(c *camunda.JobResultCorrections) {
				c.ClearFollowUpDate()
				c.UnsetFollowUpDate()
			},
			want: `{}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := camunda.NewJobResultCorrections()
			tc.build(c)
			path, body := captureRESTAck(t, completeWith(camunda.JobCompletion{Result: camunda.CorrectUserTask(*c)}))
			if !strings.HasSuffix(path, "/jobs/123/completion") {
				t.Fatalf("ack path = %q, want .../jobs/123/completion", path)
			}
			want := mustJSON(t, `{"result":{"type":"userTask","corrections":`+tc.want+`}}`)
			if !reflect.DeepEqual(body, want) {
				got, _ := json.Marshal(body)
				t.Errorf("completion body =\n  %s\nwant corrections %s", got, tc.want)
			}
		})
	}
}

// TestJobResultCorrectionsClearMarshalsDirectly covers low-level callers who build
// a JobCompletionRequest themselves: the clear lives on the model, not the worker.
func TestJobResultCorrectionsClearMarshalsDirectly(t *testing.T) {
	c := camunda.NewJobResultCorrections()
	c.ClearDueDate()
	c.ClearFollowUpDate()
	ut := camunda.NewJobResultUserTask()
	ut.SetType("userTask")
	ut.SetCorrections(*c)
	req := camunda.NewJobCompletionRequest()
	req.SetResult(camunda.JobResultUserTaskAsJobResult(ut))

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	want := mustJSON(t, `{"result":{"type":"userTask","corrections":{"dueDate":"","followUpDate":""}}}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("JobCompletionRequest = %s, want %v", b, want)
	}
	if !c.IsDueDateCleared() || !c.IsFollowUpDateCleared() || c.HasDueDate() || c.HasFollowUpDate() {
		t.Error("cleared dates must report cleared and not set")
	}
}

// TestJobWorkerFailsJobOnInvalidResult pins the no-silent-failure contract: a
// result the worker cannot put on the wire fails the job instead of completing
// it without the correction or denial the handler asked for.
func TestJobWorkerFailsJobOnInvalidResult(t *testing.T) {
	both := camunda.JobResult{
		JobResultUserTask:        camunda.NewJobResultUserTask(),
		JobResultAdHocSubProcess: camunda.NewJobResultAdHocSubProcess(),
	}
	for name, r := range map[string]*camunda.JobResult{
		"no variant":    {},
		"both variants": &both,
	} {
		t.Run(name, func(t *testing.T) {
			path, body := captureRESTAck(t, completeWith(camunda.JobCompletion{Result: r}))
			if !strings.HasSuffix(path, "/jobs/123/failure") {
				t.Fatalf("ack path = %q, want .../jobs/123/failure", path)
			}
			if msg, _ := body["errorMessage"].(string); !strings.Contains(msg, "invalid job result") {
				t.Errorf("errorMessage = %q, want it to mention the invalid job result", msg)
			}
		})
	}
}

func TestCompletionHandlerKeepsErrorSemantics(t *testing.T) {
	path, body := captureRESTAck(t, camunda.CompletionHandler(
		func(context.Context, *camunda.Job) (camunda.JobCompletion, error) {
			return camunda.JobCompletion{Result: camunda.DenyUserTask("ignored")},
				&camunda.BpmnError{Code: "E1", Message: "nope"}
		}))
	if !strings.HasSuffix(path, "/jobs/123/error") {
		t.Fatalf("ack path = %q, want .../jobs/123/error", path)
	}
	if body["errorCode"] != "E1" {
		t.Errorf("errorCode = %v, want E1", body["errorCode"])
	}
}

// falconJobGateway is a single nanobpmn gateway that pushes pushedJobFrame to
// the first subscriber. It records REST completions of that job and every frame
// the worker sends after subscribing.
func falconJobGateway(t *testing.T) (srv *httptest.Server, restBodies, frames chan map[string]any) {
	t.Helper()
	restBodies = make(chan map[string]any, 1)
	frames = make(chan map[string]any, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/topology", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"nano": map[string]any{"falconPath": "/falcon"}})
	})
	mux.HandleFunc("/v2/jobs/2251799813685424/completion", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		restBodies <- body
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/falcon", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		if sub, err := wsReadJSON(ctx, c); err != nil || sub["type"] != "subscribe" {
			return
		}
		wsWriteJSON(ctx, c, map[string]any{"type": "welcome", "heartbeatMs": 15000})
		_ = c.Write(ctx, websocket.MessageText, []byte(pushedJobFrame))
		for {
			f, err := wsReadJSON(ctx, c)
			if err != nil {
				return
			}
			select {
			case frames <- f:
			default:
			}
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, restBodies, frames
}

// runFalconWorker runs a JobWorker over the FALCON gateway at srv until the test ends.
func runFalconWorker(t *testing.T, srv *httptest.Server, handler camunda.JobHandler) {
	t.Helper()
	client, err := camunda.New(camunda.WithRestAddress(srv.URL+"/v2"), camunda.WithFalcon(true),
		camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	worker := client.NewJobWorker("email", handler)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = worker.Run(ctx) }()
}

// TestFalconWorkerSendsResultOverREST covers the FALCON transport: its
// completeJob frame has no result field, so a completion carrying a result must go
// over REST (and still replenish the delivery credit) rather than drop the result.
func TestFalconWorkerSendsResultOverREST(t *testing.T) {
	srv, restBodies, frames := falconJobGateway(t)
	runFalconWorker(t, srv, completeWith(camunda.JobCompletion{
		Result: camunda.DenyUserTask("Insufficient documentation"),
	}))

	select {
	case body := <-restBodies:
		want := mustJSON(t, `{"result":{"type":"userTask","denied":true,"deniedReason":"Insufficient documentation"}}`)
		if !reflect.DeepEqual(body, want) {
			t.Errorf("REST completion body = %v, want %v", body, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("result-carrying completion was not sent over REST")
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-frames:
			switch f["type"] {
			case "completeJob":
				t.Fatalf("job was also completed over the command stream: %v", f)
			case "jobCredits":
				return
			}
		case <-deadline:
			t.Fatal("delivery credit was not replenished after the REST completion")
		}
	}
}

// unencodableCompletions are result-bearing completions that cannot be put on the
// wire as JSON. Each must fail the job on every transport, rather than being
// logged and left to time out (REST) or sent without its variables (gRPC).
func unencodableCompletions() map[string]camunda.JobCompletion {
	adHoc := camunda.NewJobResultAdHocSubProcess()
	adHoc.SetActivateElements([]camunda.JobResultActivateElement{
		camunda.ActivateAdHocElement("task_a", map[string]any{"score": math.NaN()}),
	})
	corrections := camunda.NewJobResultCorrections()
	corrections.SetDueDate(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)) // outside RFC 3339
	return map[string]camunda.JobCompletion{
		"ad-hoc element variables": {Result: camunda.AdHocSubProcessJobResult(*adHoc)},
		"user task corrections":    {Result: camunda.CorrectUserTask(*corrections)},
		"variables alongside a result": {
			Variables: map[string]any{"score": math.Inf(1)},
			Result:    camunda.DenyUserTask("nope"),
		},
	}
}

func TestJobWorkerFailsJobOnUnencodableResult(t *testing.T) {
	for name, completion := range unencodableCompletions() {
		t.Run(name, func(t *testing.T) {
			path, body := captureRESTAck(t, completeWith(completion))
			if !strings.HasSuffix(path, "/jobs/123/failure") {
				t.Fatalf("ack path = %q, want .../jobs/123/failure", path)
			}
			if msg, _ := body["errorMessage"].(string); !strings.Contains(msg, "invalid job result") {
				t.Errorf("errorMessage = %q, want it to mention the invalid job result", msg)
			}
		})
	}
}

func TestFalconWorkerFailsJobOnUnencodableResult(t *testing.T) {
	for name, completion := range unencodableCompletions() {
		t.Run(name, func(t *testing.T) {
			srv, restBodies, frames := falconJobGateway(t)
			runFalconWorker(t, srv, completeWith(completion))

			deadline := time.After(5 * time.Second)
			for {
				select {
				case body := <-restBodies:
					t.Fatalf("job was completed over REST: %v", body)
				case f := <-frames:
					switch f["type"] {
					case "completeJob":
						t.Fatalf("job was completed over the command stream: %v", f)
					case "failJob":
						if msg, _ := f["errorMessage"].(string); !strings.Contains(msg, "invalid job result") {
							t.Errorf("failJob errorMessage = %q, want it to mention the invalid job result", msg)
						}
						return
					}
				case <-deadline:
					t.Fatal("job was not failed over the command stream")
				}
			}
		})
	}
}

// TestFalconWorkerSendsResultToConnectedGateway covers command-stream failover:
// the configured gateway drops the stream, the worker fails over to a peer from
// the topology, and the REST fallback for a result-bearing completion must go to
// that peer — keeping the REST base path and credentials — not to the
// configured gateway the stream just left.
func TestFalconWorkerSendsResultToConnectedGateway(t *testing.T) {
	type completion struct {
		path, auth string
		body       map[string]any
	}
	peerCompletions := make(chan completion, 1)
	configuredCompletions := make(chan string, 1)
	recordCompletion := func(ch chan completion) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			select {
			case ch <- completion{r.URL.Path, r.Header.Get("Authorization"), body}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}

	peerMux := http.NewServeMux()
	peerMux.HandleFunc("/ctx/v2/jobs/2251799813685424/completion", recordCompletion(peerCompletions))
	peerMux.HandleFunc("/falcon", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		if sub, err := wsReadJSON(ctx, c); err != nil || sub["type"] != "subscribe" {
			return
		}
		wsWriteJSON(ctx, c, map[string]any{"type": "welcome", "heartbeatMs": 15000})
		_ = c.Write(ctx, websocket.MessageText, []byte(pushedJobFrame))
		for {
			if _, err := wsReadJSON(ctx, c); err != nil {
				return
			}
		}
	})
	peer := httptest.NewServer(peerMux)
	defer peer.Close()
	peerURL, _ := url.Parse(peer.URL)
	peerPort, _ := strconv.Atoi(peerURL.Port())

	configuredMux := http.NewServeMux()
	configuredMux.HandleFunc("/ctx/v2/topology", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"nano":    map[string]any{"falconPath": "/falcon"},
			"brokers": []map[string]any{{"host": peerURL.Hostname(), "port": peerPort}},
		})
	})
	configuredMux.HandleFunc("/ctx/v2/jobs/2251799813685424/completion", func(w http.ResponseWriter, r *http.Request) {
		select {
		case configuredCompletions <- r.URL.Path:
		default:
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	// The configured gateway's command stream is failing: it accepts the socket
	// and drops it at once, so the link fails over to the peer.
	configuredMux.HandleFunc("/falcon", func(w http.ResponseWriter, r *http.Request) {
		if c, err := websocket.Accept(w, r, nil); err == nil {
			_ = c.CloseNow()
		}
	})
	configured := httptest.NewServer(configuredMux)
	defer configured.Close()

	client, err := camunda.New(camunda.WithRestAddress(configured.URL+"/ctx"), camunda.WithFalcon(true),
		camunda.WithBasicAuth("demo", "secret"), camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	worker := client.NewJobWorker("email", completeWith(camunda.JobCompletion{Result: camunda.DenyUserTask("nope")}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	select {
	case got := <-peerCompletions:
		if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("demo:secret")); got.auth != want {
			t.Errorf("Authorization = %q, want the configured basic credentials", got.auth)
		}
		want := mustJSON(t, `{"result":{"type":"userTask","denied":true,"deniedReason":"nope"}}`)
		if !reflect.DeepEqual(got.body, want) {
			t.Errorf("completion body = %v, want %v", got.body, want)
		}
	case path := <-configuredCompletions:
		t.Fatalf("completion went to the configured gateway (%s), not the one the stream is connected to", path)
	case <-time.After(5 * time.Second):
		t.Fatal("result-carrying completion was not sent to the connected gateway")
	}
}
