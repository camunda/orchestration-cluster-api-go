package camunda_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go/v10"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/v10/client"
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
	instr := camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(
		camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.ProcessDefinitionId("order-process")))
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

// captureCreateFrame runs a FALCON create and returns the raw createInstance
// frame the gateway received, so tests can assert exactly which fields were sent.
func captureCreateFrame(t *testing.T, clientOpts []camunda.Option, instr camundaapi.ProcessInstanceCreationInstruction) map[string]any {
	t.Helper()
	frameCh := make(chan map[string]any, 1)
	srv := falconGateway(t, nil, func(ctx context.Context, c *websocket.Conn) {
		wsWriteJSON(ctx, c, map[string]any{"type": "welcome", "submissionCredits": 5, "heartbeatMs": 15000})
		for {
			f, err := wsReadJSON(ctx, c)
			if err != nil {
				return
			}
			if f["type"] == "createInstance" {
				frameCh <- f
				wsWriteJSON(ctx, c, map[string]any{
					"type": "commandResult", "corr": f["corr"], "status": 200,
					"body": map[string]any{"processInstanceKey": "2251799813685340"},
				})
			}
		}
	})
	opts := append([]camunda.Option{camunda.WithRestAddress(srv.URL + "/v2"), camunda.WithFalcon(true)}, clientOpts...)
	c, err := camunda.New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.CreateProcessInstance(context.Background(), instr); err != nil {
		t.Fatalf("CreateProcessInstance: %v", err)
	}
	select {
	case f := <-frameCh:
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("no createInstance frame reached the gateway")
		return nil
	}
}

// TestCreateProcessInstanceFalconSendsDefaultTenantWhenUnset: with neither an
// instruction tenant nor a configured default, the create frame carries
// "<default>", matching the REST path.
func TestCreateProcessInstanceFalconSendsDefaultTenantWhenUnset(t *testing.T) {
	instr := camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(
		camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.ProcessDefinitionId("order-process")))
	frame := captureCreateFrame(t, nil, instr)
	if got := frame["tenantId"]; got != camunda.DefaultTenantIdentifier {
		t.Errorf("tenantId = %v, want %q", got, camunda.DefaultTenantIdentifier)
	}
}

// TestCreateProcessInstanceFalconSendsConfiguredDefault: a configured default
// tenant is sent explicitly on the create frame.
func TestCreateProcessInstanceFalconSendsConfiguredDefault(t *testing.T) {
	instr := camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(
		camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.ProcessDefinitionId("order-process")))
	frame := captureCreateFrame(t, []camunda.Option{camunda.WithDefaultTenantID("acme")}, instr)
	if got := frame["tenantId"]; got != "acme" {
		t.Errorf("tenantId = %v, want %q", got, "acme")
	}
}

// TestCreateProcessInstanceFalconSendsExplicitTenant: an explicit tenant on the
// instruction always wins over the configured default.
func TestCreateProcessInstanceFalconSendsExplicitTenant(t *testing.T) {
	byID := camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.ProcessDefinitionId("order-process"))
	byID.SetTenantId(camundaapi.MustTenantId("explicit"))
	instr := camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID)
	frame := captureCreateFrame(t, []camunda.Option{camunda.WithDefaultTenantID("acme")}, instr)
	if got := frame["tenantId"]; got != "explicit" {
		t.Errorf("tenantId = %v, want %q (explicit tenant must win over the default)", got, "explicit")
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
	instr := camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(
		camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.ProcessDefinitionId("order-process")))
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

// A worker restricted to a non-default tenant must not use FALCON, whose
// subscription carries no gateway-enforced tenant filter.
func TestJobWorkerWithNonDefaultTenantBypassesFalcon(t *testing.T) {
	for name, opts := range map[string]struct {
		client []camunda.Option
		worker []camunda.WorkerOption
	}{
		"explicit worker tenants":  {worker: []camunda.WorkerOption{camunda.WithWorkerTenantIDs("tenant-a")}},
		"multiple tenants":         {worker: []camunda.WorkerOption{camunda.WithWorkerTenantIDs("<default>", "tenant-a")}},
		"configured client tenant": {client: []camunda.Option{camunda.WithDefaultTenantID("tenant-a")}},
	} {
		t.Run(name, func(t *testing.T) {
			var subscribed atomic.Bool
			polled := make(chan struct{}, 1)
			srv := falconGateway(t, nil, func(ctx context.Context, c *websocket.Conn) {
				subscribed.Store(true)
			})
			mux := http.NewServeMux()
			mux.Handle("/", srv.Config.Handler)
			mux.HandleFunc("/v2/jobs/activation", func(w http.ResponseWriter, _ *http.Request) {
				select {
				case polled <- struct{}{}:
				default:
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jobs":[]}`))
			})
			srv.Config.Handler = mux

			copts := append([]camunda.Option{camunda.WithRestAddress(srv.URL + "/v2"), camunda.WithFalcon(true)}, opts.client...)
			c, err := camunda.New(copts...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			worker := c.NewJobWorker("email", func(context.Context, *camunda.Job) (map[string]any, error) {
				return nil, nil
			}, opts.worker...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = worker.Run(ctx) }()
			select {
			case <-polled:
			case <-time.After(5 * time.Second):
				t.Fatal("worker did not poll over REST")
			}
			if subscribed.Load() {
				t.Error("worker subscribed over FALCON despite a non-default tenant restriction")
			}
		})
	}
}

// TestConcurrentJobWorkersAllSubscribeOverFalcon is the regression test for
// https://github.com/camunda/orchestration-cluster-api-go/issues/240: workers
// started concurrently on one client must all wait for the in-flight capability
// probe and subscribe over FALCON, rather than all but the probe winner locking
// into REST polling.
// concurrentFalconGateway is a fake FALCON-capable gateway whose topology
// handler is supplied by the test; it counts FALCON subscriptions and REST polls.
func concurrentFalconGateway(t *testing.T, topology http.HandlerFunc) (srv *httptest.Server, subscribed, restPolls *atomic.Int32) {
	t.Helper()
	subscribed, restPolls = new(atomic.Int32), new(atomic.Int32)
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/topology", topology)
	mux.HandleFunc("/v2/jobs/activation", func(w http.ResponseWriter, _ *http.Request) {
		restPolls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	})
	mux.HandleFunc("/falcon", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		sub, err := wsReadJSON(ctx, c)
		if err != nil || sub["type"] != "subscribe" {
			return
		}
		subscribed.Add(1)
		wsWriteJSON(ctx, c, map[string]any{"type": "welcome", "heartbeatMs": 15000})
		for {
			if _, err := wsReadJSON(ctx, c); err != nil {
				return
			}
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, subscribed, restPolls
}

func writeNanoTopology(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{"nano": map[string]any{"falconPath": "/falcon"}})
}

func waitSubscribed(t *testing.T, subscribed, restPolls *atomic.Int32, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for subscribed.Load() < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := subscribed.Load(); got != n {
		t.Fatalf("%d of %d concurrent workers subscribed over FALCON (REST polls: %d)", got, n, restPolls.Load())
	}
	if got := restPolls.Load(); got != 0 {
		t.Errorf("workers polled REST %d times; want all on FALCON", got)
	}
}

// TestConcurrentJobWorkersAllSubscribeOverFalcon is the regression test for
// https://github.com/camunda/orchestration-cluster-api-go/issues/240: workers
// started concurrently on one client must all wait for the in-flight capability
// probe and subscribe over FALCON, rather than all but the probe winner locking
// into REST polling.
func TestConcurrentJobWorkersAllSubscribeOverFalcon(t *testing.T) {
	const n = 10
	srv, subscribed, restPolls := concurrentFalconGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond) // keep the probe in flight while the others start
		writeNanoTopology(w)
	})
	c, err := camunda.New(camunda.WithRestAddress(srv.URL+"/v2"), camunda.WithFalcon(true))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		w := c.NewJobWorker("email", func(context.Context, *camunda.Job) (map[string]any, error) {
			return nil, nil
		})
		go func() { <-start; _ = w.Run(ctx) }()
	}
	close(start)
	waitSubscribed(t, subscribed, restPolls, n)
}

// TestConcurrentJobWorkersSurviveCancelledProber: when the worker running the
// first probe has its ctx cancelled mid-probe, workers waiting on that probe
// with a live ctx must re-probe and still subscribe over FALCON, not inherit
// the prober's cancellation and stay on REST.
func TestConcurrentJobWorkersSurviveCancelledProber(t *testing.T) {
	const n = 5
	var probes atomic.Int32
	firstProbe := make(chan struct{})
	srv, subscribed, restPolls := concurrentFalconGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if probes.Add(1) == 1 {
			close(firstProbe)
			<-r.Context().Done() // hang until the prober's ctx is cancelled
			return
		}
		writeNanoTopology(w)
	})
	c, err := camunda.New(camunda.WithRestAddress(srv.URL+"/v2"), camunda.WithFalcon(true))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler := func(context.Context, *camunda.Job) (map[string]any, error) { return nil, nil }

	proberCtx, cancelProber := context.WithCancel(context.Background())
	go func() { _ = c.NewJobWorker("email", handler).Run(proberCtx) }()
	select {
	case <-firstProbe:
	case <-time.After(5 * time.Second):
		t.Fatal("first probe never arrived")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < n; i++ {
		w := c.NewJobWorker("email", handler)
		go func() { _ = w.Run(ctx) }()
	}
	// No sync needed: workers that are waiting re-probe, and workers that
	// arrive after the cancelled probe ends find no backoff armed and probe
	// themselves, so every interleaving must end on FALCON.
	cancelProber()
	waitSubscribed(t, subscribed, restPolls, n)
}
