package camunda_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
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

// TestFalconWorkerFailsJobWithResult: the command stream's completeJob frame
// cannot carry a job result, so over FALCON a result-bearing completion fails the
// job with an actionable message. It is never completed, with or without the
// result (completing a denied task without its denial would approve it), and
// nothing is sent over REST.
func TestFalconWorkerFailsJobWithResult(t *testing.T) {
	corrections := camunda.NewJobResultCorrections()
	corrections.SetAssignee("someone")
	completions := map[string]camunda.JobCompletion{
		"denial":      {Result: camunda.DenyUserTask("nope")},
		"corrections": {Variables: map[string]any{"ok": true}, Result: camunda.CorrectUserTask(*corrections)},
		"ad-hoc":      {Result: camunda.AdHocSubProcessJobResult(*camunda.NewJobResultAdHocSubProcess())},
	}
	for name, c := range unencodableCompletions() {
		completions["unencodable "+name] = c
	}
	for name, completion := range completions {
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
						msg, _ := f["errorMessage"].(string)
						if !strings.Contains(msg, "cannot be sent over the FALCON command stream") ||
							!strings.Contains(msg, "CAMUNDA_FORCE_REST=true") {
							t.Errorf("failJob errorMessage = %q, want the FALCON limitation and its workaround", msg)
						}
						if f["retries"] != float64(2) {
							t.Errorf("failJob retries = %v, want 2 (decremented from 3)", f["retries"])
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

// TestFalconWorkerCompletesWithoutResult: a CompletionHandler that returns no
// result completes over the command stream exactly like a plain JobHandler.
func TestFalconWorkerCompletesWithoutResult(t *testing.T) {
	srv, restBodies, frames := falconJobGateway(t)
	runFalconWorker(t, srv, completeWith(camunda.JobCompletion{Variables: map[string]any{"ok": true}}))

	deadline := time.After(5 * time.Second)
	for {
		select {
		case body := <-restBodies:
			t.Fatalf("job was completed over REST: %v", body)
		case f := <-frames:
			if f["type"] == "completeJob" {
				want := map[string]any{"ok": true}
				if !reflect.DeepEqual(f["variables"], want) {
					t.Errorf("completeJob variables = %v, want %v", f["variables"], want)
				}
				return
			}
		case <-deadline:
			t.Fatal("job was not completed over the command stream")
		}
	}
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

// unencodableCompletions are result-bearing completions that cannot be put on the
// wire as JSON. Each must fail the job on every transport, rather than being
// logged and left to time out (REST) or sent without its variables (gRPC).
// FALCON fails every result-bearing completion; see
// TestFalconWorkerFailsJobWithResult.
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

// TestJobResultCorrectionsDateRoundTrip: every state of a correction date survives
// marshal -> unmarshal: cleared ("" on the wire), set (ISO-8601), null, absent.
func TestJobResultCorrectionsDateRoundTrip(t *testing.T) {
	due := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	type dateAPI struct {
		name    string
		prop    string
		set     func(*camunda.JobResultCorrections, time.Time)
		setNil  func(*camunda.JobResultCorrections)
		clear   func(*camunda.JobResultCorrections)
		cleared func(*camunda.JobResultCorrections) bool
		get     func(*camunda.JobResultCorrections) (*time.Time, bool)
	}
	dates := []dateAPI{
		{"dueDate", "dueDate",
			(*camunda.JobResultCorrections).SetDueDate, (*camunda.JobResultCorrections).SetDueDateNil,
			(*camunda.JobResultCorrections).ClearDueDate, (*camunda.JobResultCorrections).IsDueDateCleared,
			(*camunda.JobResultCorrections).GetDueDateOk},
		{"followUpDate", "followUpDate",
			(*camunda.JobResultCorrections).SetFollowUpDate, (*camunda.JobResultCorrections).SetFollowUpDateNil,
			(*camunda.JobResultCorrections).ClearFollowUpDate, (*camunda.JobResultCorrections).IsFollowUpDateCleared,
			(*camunda.JobResultCorrections).GetFollowUpDateOk},
	}
	for _, d := range dates {
		states := []struct {
			name  string
			build func(*camunda.JobResultCorrections)
			wire  string
			check func(t *testing.T, c *camunda.JobResultCorrections)
		}{
			{"cleared", d.clear, `{"` + d.prop + `":""}`, func(t *testing.T, c *camunda.JobResultCorrections) {
				if v, ok := d.get(c); !d.cleared(c) || ok || v != nil {
					t.Errorf("decoded: cleared=%v value=%v set=%v; want cleared and unset", d.cleared(c), v, ok)
				}
			}},
			{"set", func(c *camunda.JobResultCorrections) { d.set(c, due) }, `{"` + d.prop + `":"2026-01-02T03:04:05Z"}`,
				func(t *testing.T, c *camunda.JobResultCorrections) {
					if v, ok := d.get(c); d.cleared(c) || !ok || v == nil || !v.Equal(due) {
						t.Errorf("decoded: cleared=%v value=%v; want %v", d.cleared(c), v, due)
					}
				}},
			{"null", d.setNil, `{"` + d.prop + `":null}`, func(t *testing.T, c *camunda.JobResultCorrections) {
				if v, ok := d.get(c); d.cleared(c) || !ok || v != nil {
					t.Errorf("decoded: cleared=%v value=%v set=%v; want an explicit null", d.cleared(c), v, ok)
				}
			}},
			{"absent", func(*camunda.JobResultCorrections) {}, `{}`, func(t *testing.T, c *camunda.JobResultCorrections) {
				if v, ok := d.get(c); d.cleared(c) || ok || v != nil {
					t.Errorf("decoded: cleared=%v value=%v set=%v; want absent", d.cleared(c), v, ok)
				}
			}},
		}
		for _, st := range states {
			t.Run(d.name+"/"+st.name, func(t *testing.T) {
				src := camunda.NewJobResultCorrections()
				src.SetAssignee("someone")
				st.build(src)
				b, err := json.Marshal(src)
				if err != nil {
					t.Fatalf("Marshal: %v", err)
				}
				want := mustJSON(t, st.wire)
				want["assignee"] = "someone"
				var got map[string]any
				_ = json.Unmarshal(b, &got)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("wire = %s, want %v", b, want)
				}
				var back camunda.JobResultCorrections
				if err := json.Unmarshal(b, &back); err != nil {
					t.Fatalf("Unmarshal %s: %v", b, err)
				}
				if back.GetAssignee() != "someone" {
					t.Errorf("assignee = %q, want someone", back.GetAssignee())
				}
				st.check(t, &back)
				again, _ := json.Marshal(&back)
				if string(again) != string(b) {
					t.Errorf("re-marshal = %s, want %s", again, b)
				}
			})
		}
	}
}

// TestJobCompletionRequestRoundTripsDateClears restores a serialized completion
// request carrying date clears, e.g. one persisted for a later retry.
func TestJobCompletionRequestRoundTripsDateClears(t *testing.T) {
	c := camunda.NewJobResultCorrections()
	c.ClearDueDate()
	c.SetFollowUpDate(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	c.SetPriority(80)
	req := camunda.NewJobCompletionRequest()
	req.SetVariables(map[string]any{"approved": false})
	req.SetResult(*camunda.CorrectUserTask(*c))

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back camunda.JobCompletionRequest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal %s: %v", b, err)
	}
	ut := back.GetResult().JobResultUserTask
	if ut == nil {
		t.Fatalf("decoded result is not a user task result: %s", b)
	}
	corr := ut.GetCorrections()
	if !corr.IsDueDateCleared() || corr.IsFollowUpDateCleared() || corr.GetPriority() != 80 {
		t.Errorf("decoded corrections: dueCleared=%v followUpCleared=%v priority=%d",
			corr.IsDueDateCleared(), corr.IsFollowUpDateCleared(), corr.GetPriority())
	}
	again, err := json.Marshal(&back)
	if err != nil {
		t.Fatalf("re-Marshal: %v", err)
	}
	if string(again) != string(b) {
		t.Errorf("round trip changed the request:\n got %s\nwant %s", again, b)
	}
}

func TestJobResultCorrectionsDecodesPaddedClear(t *testing.T) {
	var c camunda.JobResultCorrections
	if err := json.Unmarshal([]byte("{ \"dueDate\" :\n \"\" , \"followUpDate\" : null }"), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !c.IsDueDateCleared() || c.IsFollowUpDateCleared() || !c.HasFollowUpDate() {
		t.Errorf("dueCleared=%v followUpCleared=%v followUpSet=%v; want a due-date clear and a follow-up null",
			c.IsDueDateCleared(), c.IsFollowUpDateCleared(), c.HasFollowUpDate())
	}
}

// plainCorrections has JobResultCorrections' fields but none of its methods, so
// it decodes with encoding/json's default semantics: the oracle for the generated
// decoder on inputs that do not clear a date.
type plainCorrections camunda.JobResultCorrections

func populatedCorrections() *camunda.JobResultCorrections {
	c := camunda.NewJobResultCorrections()
	c.SetAssignee("someone")
	c.SetDueDate(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	c.ClearFollowUpDate()
	c.SetCandidateGroups([]string{"reviewers"})
	c.SetCandidateUsers([]string{"alice"})
	c.SetPriority(40)
	return c
}

// TestJobResultCorrectionsDecodeMergesLikeEncodingJSON: decoding into a populated
// receiver leaves absent properties (clear markers included) untouched and
// otherwise behaves exactly like encoding/json's default decoding of the model.
func TestJobResultCorrectionsDecodeMergesLikeEncodingJSON(t *testing.T) {
	for _, in := range []string{
		`{}`,
		`null`,
		`{"priority":80}`,
		`{"assignee":"other"}`,
		`{"assignee":null}`,
		`{"candidateGroups":["x"]}`,
		`{"candidateUsers":[]}`,
		`{"priority":null,"candidateGroups":null}`,
		`{"dueDate":"2027-05-06T07:08:09Z"}`,
		`{"dueDate":null}`,
		`{"unknown":1,"priority":80}`,
	} {
		t.Run(in, func(t *testing.T) {
			got := populatedCorrections()
			if err := json.Unmarshal([]byte(in), got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			oracle := plainCorrections(*populatedCorrections())
			if err := json.Unmarshal([]byte(in), &oracle); err != nil {
				t.Fatalf("oracle Unmarshal: %v", err)
			}
			if want := camunda.JobResultCorrections(oracle); !reflect.DeepEqual(*got, want) {
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(&want)
				t.Errorf("decoded into a populated receiver:\n got %s\nwant %s", gotJSON, wantJSON)
			}
			if !got.IsFollowUpDateCleared() {
				t.Error("the follow-up date clear, absent from the input, was lost")
			}
		})
	}
}

// TestJobResultCorrectionsDecodeClearTransitions: on decode, "" clears a date
// (unsetting its value) and a timestamp or null replaces a clear, matching the
// last-call-wins rule of Set/SetNil/Clear; other properties do not touch it.
func TestJobResultCorrectionsDecodeClearTransitions(t *testing.T) {
	ts := time.Date(2027, 5, 6, 7, 8, 9, 0, time.UTC)
	type date struct {
		prop    string
		set     func(*camunda.JobResultCorrections, time.Time)
		clear   func(*camunda.JobResultCorrections)
		cleared func(*camunda.JobResultCorrections) bool
		get     func(*camunda.JobResultCorrections) (*time.Time, bool)
	}
	for _, d := range []date{
		{"dueDate", (*camunda.JobResultCorrections).SetDueDate, (*camunda.JobResultCorrections).ClearDueDate,
			(*camunda.JobResultCorrections).IsDueDateCleared, (*camunda.JobResultCorrections).GetDueDateOk},
		{"followUpDate", (*camunda.JobResultCorrections).SetFollowUpDate, (*camunda.JobResultCorrections).ClearFollowUpDate,
			(*camunda.JobResultCorrections).IsFollowUpDateCleared, (*camunda.JobResultCorrections).GetFollowUpDateOk},
	} {
		cases := []struct {
			name        string
			start       func(*camunda.JobResultCorrections)
			in          string
			wantCleared bool
			wantSet     bool
			wantValue   *time.Time
		}{
			{"set then decoded clear", func(c *camunda.JobResultCorrections) { d.set(c, ts) }, `{"` + d.prop + `":""}`, true, false, nil},
			{"clear kept when absent", d.clear, `{"priority":80}`, true, false, nil},
			{"clear replaced by timestamp", d.clear, `{"` + d.prop + `":"2027-05-06T07:08:09Z"}`, false, true, &ts},
			{"clear replaced by null", d.clear, `{"` + d.prop + `":null}`, false, true, nil},
			{"set kept when absent", func(c *camunda.JobResultCorrections) { d.set(c, ts) }, `{"priority":80}`, false, true, &ts},
		}
		for _, tc := range cases {
			t.Run(d.prop+"/"+tc.name, func(t *testing.T) {
				c := camunda.NewJobResultCorrections()
				c.SetAssignee("someone")
				tc.start(c)
				if err := json.Unmarshal([]byte(tc.in), c); err != nil {
					t.Fatalf("Unmarshal: %v", err)
				}
				v, set := d.get(c)
				if d.cleared(c) != tc.wantCleared || set != tc.wantSet ||
					(v == nil) != (tc.wantValue == nil) || (v != nil && !v.Equal(*tc.wantValue)) {
					t.Errorf("cleared=%v set=%v value=%v; want cleared=%v set=%v value=%v",
						d.cleared(c), set, v, tc.wantCleared, tc.wantSet, tc.wantValue)
				}
				if c.GetAssignee() != "someone" {
					t.Errorf("assignee = %q, want it left untouched", c.GetAssignee())
				}
			})
		}
	}
}

// TestJobResultCorrectionsClearKeepsMemberOrder: apart from the clear, decoding must
// match encoding/json's default decoding of the model (with null for the date).
// encoding/json matches names case-insensitively and the last member wins, so
// filtering the clear out through a map (which reorders) changes the result.
func TestJobResultCorrectionsClearKeepsMemberOrder(t *testing.T) {
	for _, others := range []string{
		`"priority":80,"Priority":10`,
		`"Priority":10,"priority":80`,
		`"assignee":"a","Assignee":"b"`,
		`"Assignee":"b","assignee":"a"`,
		`"followUpDate":"2026-01-02T03:04:05Z","FollowUpDate":null`,
	} {
		for _, in := range []string{`{` + others + `,"dueDate":%s}`, `{"dueDate":%s,` + others + `}`} {
			t.Run(in, func(t *testing.T) {
				var cleared camunda.JobResultCorrections
				var oracle plainCorrections
				if err := json.Unmarshal([]byte(fmt.Sprintf(in, `""`)), &cleared); err != nil {
					t.Fatalf("Unmarshal clear: %v", err)
				}
				if err := json.Unmarshal([]byte(fmt.Sprintf(in, `null`)), &oracle); err != nil {
					t.Fatalf("Unmarshal null: %v", err)
				}
				if !cleared.IsDueDateCleared() {
					t.Error("dueDate \"\" did not decode as a clear")
				}
				nulled := camunda.JobResultCorrections(oracle)
				cleared.UnsetDueDate()
				nulled.UnsetDueDate()
				if !reflect.DeepEqual(cleared, nulled) {
					a, _ := json.Marshal(&cleared)
					b, _ := json.Marshal(&nulled)
					t.Errorf("other members decode differently with a clear:\n clear %s\n  null %s", a, b)
				}
			})
		}
	}
}

// TestJobResultCorrectionsRepeatedDateLastWins: as with any member, the last of
// several members naming a date (in any letter case) decides whether it is cleared.
func TestJobResultCorrectionsRepeatedDateLastWins(t *testing.T) {
	ts := time.Date(2027, 5, 6, 7, 8, 9, 0, time.UTC)
	for _, tc := range []struct {
		in      string
		cleared bool
	}{
		{`{"dueDate":"","DueDate":"2027-05-06T07:08:09Z"}`, false},
		{`{"dueDate":"2027-05-06T07:08:09Z","DueDate":""}`, true},
		{`{"DUEDATE":""}`, true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			var c camunda.JobResultCorrections
			if err := json.Unmarshal([]byte(tc.in), &c); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			v, ok := c.GetDueDateOk()
			if c.IsDueDateCleared() != tc.cleared || ok == tc.cleared || (!tc.cleared && !v.Equal(ts)) {
				t.Errorf("cleared=%v set=%v value=%v; want cleared=%v", c.IsDueDateCleared(), ok, v, tc.cleared)
			}
		})
	}
}

// TestJobResultCorrectionsCaseVariantClearRoundTrips: encoding/json matches a date's
// name in any letter case, so `{"duedate":""}` is a clear, and it must survive
// re-encoding and decoding again.
func TestJobResultCorrectionsCaseVariantClearRoundTrips(t *testing.T) {
	type date struct {
		prop    string
		cleared func(*camunda.JobResultCorrections) bool
	}
	for _, d := range []date{
		{"dueDate", (*camunda.JobResultCorrections).IsDueDateCleared},
		{"followUpDate", (*camunda.JobResultCorrections).IsFollowUpDateCleared},
	} {
		for _, key := range []string{strings.ToLower(d.prop), strings.ToUpper(d.prop), d.prop, `\u00` + fmt.Sprintf("%x", d.prop[0]) + d.prop[1:]} {
			in := `{"` + key + `":"","priority":5}`
			t.Run(in, func(t *testing.T) {
				var c camunda.JobResultCorrections
				for round := 0; round < 3; round++ {
					if err := json.Unmarshal([]byte(in), &c); err != nil {
						t.Fatalf("round %d: Unmarshal %s: %v", round, in, err)
					}
					if !d.cleared(&c) || c.GetPriority() != 5 {
						t.Errorf("round %d: %s: cleared=%v priority=%d", round, in, d.cleared(&c), c.GetPriority())
					}
					b, err := json.Marshal(&c)
					if err != nil {
						t.Fatalf("round %d: Marshal: %v", round, err)
					}
					in = string(b)
				}
			})
		}
	}
}
