package camunda_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/coder/websocket"
)

// pushedJobFrame is a command-stream "job" frame wrapping a fully-formed
// ActivatedJobResult (mirrors a real /v2/jobs/activation item), so the worker's
// strict decoder accepts it.
const pushedJobFrame = `{
  "type": "job",
  "job": {
    "type": "email",
    "processDefinitionId": "order-process",
    "processDefinitionVersion": 1,
    "elementId": "SendEmail",
    "customHeaders": {},
    "worker": "w1",
    "retries": 3,
    "deadline": 1784256664927,
    "variables": {},
    "tenantId": "<default>",
    "jobKey": "2251799813685424",
    "processInstanceKey": "2251799813685417",
    "processDefinitionKey": "2251799813685416",
    "elementInstanceKey": "2251799813685423",
    "kind": "BPMN_ELEMENT",
    "listenerEventType": "UNSPECIFIED",
    "userTask": null,
    "tags": [],
    "rootProcessInstanceKey": "2251799813685417",
    "businessId": null,
    "priority": 0
  }
}`

// falconGateway is an in-process stand-in for a nanobpmn gateway: it answers
// GET /v2/topology with the "nano" advertisement and accepts the /falcon
// command-stream WebSocket, delegating each socket to protocol.
func falconGateway(t *testing.T, restHit *atomic.Bool, protocol func(ctx context.Context, c *websocket.Conn)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/topology", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"nano": map[string]any{"falconPath": "/falcon"}})
	})
	mux.HandleFunc("/falcon", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		protocol(r.Context(), c)
	})
	mux.HandleFunc("/v2/process-instances", func(w http.ResponseWriter, _ *http.Request) {
		if restHit != nil {
			restHit.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"processDefinitionId":      "order-process",
			"processDefinitionVersion": 1,
			"tenantId":                 "<default>",
			"variables":                map[string]any{},
			"processDefinitionKey":     "555",
			"processInstanceKey":       "999",
			"tags":                     []string{},
			"businessId":               nil,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func wsWriteJSON(ctx context.Context, c *websocket.Conn, v any) {
	data, _ := json.Marshal(v)
	_ = c.Write(ctx, websocket.MessageText, data)
}

func wsReadJSON(ctx context.Context, c *websocket.Conn) (map[string]any, error) {
	_, data, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(data, &m)
}

func TestCreateProcessInstanceRoutesOverFalcon(t *testing.T) {
	srv := falconGateway(t, nil, func(ctx context.Context, c *websocket.Conn) {
		wsWriteJSON(ctx, c, map[string]any{"type": "welcome", "submissionCredits": 5, "heartbeatMs": 15000})
		for {
			f, err := wsReadJSON(ctx, c)
			if err != nil {
				return
			}
			if f["type"] == "createInstance" {
				wsWriteJSON(ctx, c, map[string]any{
					"type": "commandResult", "corr": f["corr"], "status": 200,
					"body": map[string]any{
						"processInstanceKey":       "2251799813685340",
						"processDefinitionKey":     "555",
						"processDefinitionVersion": 3,
						"tenantId":                 "acme",
						"tags":                     []string{"go-sdk"},
						"businessId":               "order-42",
					},
				})
			}
		}
	})

	c, err := camunda.New(camunda.WithRestAddress(srv.URL+"/v2"), camunda.WithFalcon(true))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	instr := openapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(
		openapi.NewProcessInstanceCreationInstructionById("order-process"))
	res, err := c.CreateProcessInstance(context.Background(), instr)
	if err != nil {
		t.Fatalf("CreateProcessInstance: %v", err)
	}
	if got := string(res.GetProcessInstanceKey()); got != "2251799813685340" {
		t.Errorf("ProcessInstanceKey = %q, want the stream-created key", got)
	}
	// The result must reflect the gateway's commandResult body, not just
	// request-derived defaults, for REST-equivalent output.
	if got := string(res.GetProcessDefinitionKey()); got != "555" {
		t.Errorf("ProcessDefinitionKey = %q, want 555 from the gateway body", got)
	}
	if got := res.GetProcessDefinitionVersion(); got != 3 {
		t.Errorf("ProcessDefinitionVersion = %d, want 3 from the gateway body", got)
	}
	if got := res.GetTenantId(); got != "acme" {
		t.Errorf("TenantId = %q, want acme from the gateway body", got)
	}
	if got := res.GetTags(); len(got) != 1 || got[0] != "go-sdk" {
		t.Errorf("Tags = %v, want [go-sdk] from the gateway body", got)
	}
	if got := res.GetBusinessId(); got != "order-42" {
		t.Errorf("BusinessId = %q, want order-42 from the gateway body", got)
	}
	// The definition id was not echoed by the body, so it falls back to the request.
	if got := res.GetProcessDefinitionId(); got != "order-process" {
		t.Errorf("ProcessDefinitionId = %q, want the request's order-process", got)
	}
}

func TestCreateProcessInstanceForceRESTBypassesFalcon(t *testing.T) {
	var restHit atomic.Bool
	srv := falconGateway(t, &restHit, func(ctx context.Context, c *websocket.Conn) {
		// If FALCON were consulted the create would arrive here; assert it does not.
		t.Error("command stream must not be used when CAMUNDA_FORCE_REST is set")
		<-ctx.Done()
	})

	c, err := camunda.New(camunda.WithRestAddress(srv.URL+"/v2"), camunda.WithForceREST(true))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	instr := openapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(
		openapi.NewProcessInstanceCreationInstructionById("order-process"))
	if _, err := c.CreateProcessInstance(context.Background(), instr); err != nil {
		t.Fatalf("CreateProcessInstance: %v", err)
	}
	if !restHit.Load() {
		t.Error("expected the create to go over REST")
	}
}

func TestJobWorkerReceivesPushedJobOverFalcon(t *testing.T) {
	completed := make(chan string, 1)
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
			if f["type"] == "completeJob" {
				completed <- f["jobKey"].(string)
			}
		}
	})

	c, err := camunda.New(camunda.WithRestAddress(srv.URL+"/v2"), camunda.WithFalcon(true))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var handled atomic.Bool
	worker := c.NewJobWorker("email", func(_ context.Context, job *camunda.Job) (map[string]any, error) {
		handled.Store(true)
		if job.Key() != "2251799813685424" {
			t.Errorf("unexpected job key %q", job.Key())
		}
		return map[string]any{"ok": true}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	select {
	case key := <-completed:
		if key != "2251799813685424" {
			t.Errorf("completed wrong job: %q", key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not complete the pushed job over the command stream")
	}
	if !handled.Load() {
		t.Error("handler was not invoked")
	}
}
