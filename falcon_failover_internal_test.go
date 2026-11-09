package camunda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// failoverJobFrame is a command-stream "job" frame carrying a complete
// ActivatedJobResult, so the worker's strict decoder accepts it.
const failoverJobFrame = `{"type":"job","job":{
  "type":"email","processDefinitionId":"order-process","processDefinitionVersion":1,
  "elementId":"SendEmail","customHeaders":{},"worker":"w1","retries":3,
  "deadline":1784256664927,"variables":{},"tenantId":"<default>",
  "jobKey":"2251799813685424","processInstanceKey":"2251799813685417",
  "processDefinitionKey":"2251799813685416","elementInstanceKey":"2251799813685423",
  "kind":"BPMN_ELEMENT","listenerEventType":"UNSPECIFIED","userTask":null,"tags":[],
  "rootProcessInstanceKey":"2251799813685417","businessId":null,"priority":0}}`

// Phases of the failover cluster's command streams.
const (
	phaseServe     int32 = iota // accept; the first gateway subscribed to pushes the job
	phaseDown                   // reject every dial: the link is reconnecting
	phaseRecovered              // accept on gateways other than the configured one and the dropped one
)

// failoverCluster is three nanobpmn gateways: "configured" (the client's REST
// address, whose topology lists the other two) and peers "b" and "c". Each serves
// the command stream according to the shared phase and records REST completions
// of the pushed job.
type failoverCluster struct {
	phase       atomic.Int32
	pushed      atomic.Bool
	mu          sync.Mutex
	dropped     string // gateway that pushed the job and is then dropped
	connected   string // gateway accepted in phaseRecovered
	drop        chan struct{}
	redialing   chan struct{} // signalled on a dial during phaseDown
	completions chan string   // gateway name of each REST completion
	configured  *httptest.Server
}

func newFailoverCluster(t *testing.T) *failoverCluster {
	t.Helper()
	fc := &failoverCluster{
		drop:        make(chan struct{}),
		redialing:   make(chan struct{}, 1),
		completions: make(chan string, 4),
	}
	b := httptest.NewServer(fc.gateway("b", nil))
	c := httptest.NewServer(fc.gateway("c", nil))
	var peers []map[string]any
	for _, s := range []*httptest.Server{b, c} {
		u, _ := url.Parse(s.URL)
		port, _ := strconv.Atoi(u.Port())
		peers = append(peers, map[string]any{"host": u.Hostname(), "port": port})
	}
	fc.configured = httptest.NewServer(fc.gateway("configured", peers))
	t.Cleanup(func() {
		fc.configured.Close()
		b.Close()
		c.Close()
	})
	return fc
}

func (fc *failoverCluster) gateway(name string, peers []map[string]any) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/topology", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"nano": map[string]any{"falconPath": "/falcon"}, "brokers": peers})
	})
	mux.HandleFunc("/v2/jobs/2251799813685424/completion", func(w http.ResponseWriter, _ *http.Request) {
		fc.completions <- name
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/falcon", func(w http.ResponseWriter, r *http.Request) {
		switch fc.phase.Load() {
		case phaseDown:
			select {
			case fc.redialing <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case phaseRecovered:
			fc.mu.Lock()
			reject := name == "configured" || name == fc.dropped
			if !reject {
				fc.connected = name
			}
			fc.mu.Unlock()
			if reject {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx := r.Context()
		if _, _, err := conn.Read(ctx); err != nil { // subscribe
			return
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"welcome","heartbeatMs":15000}`))
		if fc.phase.Load() == phaseServe && fc.pushed.CompareAndSwap(false, true) {
			fc.mu.Lock()
			fc.dropped = name
			fc.mu.Unlock()
			_ = conn.Write(ctx, websocket.MessageText, []byte(failoverJobFrame))
			go func() { // drain acks so the socket stays healthy until dropped
				for {
					if _, _, err := conn.Read(ctx); err != nil {
						return
					}
				}
			}()
			select {
			case <-fc.drop:
			case <-ctx.Done():
			}
			return
		}
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	})
	return mux
}

// startFailoverWorker runs a FALCON JobWorker against the cluster whose handler
// waits for release and then completes the job with a result.
func startFailoverWorker(t *testing.T, fc *failoverCluster, ackTimeout time.Duration) (started, release chan struct{}) {
	t.Helper()
	client, err := New(WithRestAddress(fc.configured.URL+"/v2"), WithFalcon(true), WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	w := client.NewJobWorker("email", CompletionHandler(func(context.Context, *Job) (JobCompletion, error) {
		once.Do(func() { close(started) })
		<-release
		return JobCompletion{Result: DenyUserTask("nope")}, nil
	}))
	w.falconAckTimeout = ackTimeout
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()
	return started, release
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// dropStream takes the cluster into the reconnect window: the gateway that pushed
// the job drops the stream and every redial is refused. It returns once the
// worker has noticed and is redialing.
func (fc *failoverCluster) dropStream(t *testing.T) string {
	t.Helper()
	fc.phase.Store(phaseDown)
	close(fc.drop)
	waitSignal(t, fc.redialing, "the worker to redial after the drop")
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.dropped
}

// TestFalconResultAckWaitsForReconnect: a handler that finishes while the command
// stream is reconnecting must not fall back to the configured gateway; the REST
// completion waits for the stream and goes to the gateway it reconnects to,
// rather than to the configured one or to the peer that just dropped it.
func TestFalconResultAckWaitsForReconnect(t *testing.T) {
	fc := newFailoverCluster(t)
	started, release := startFailoverWorker(t, fc, 0)

	waitSignal(t, started, "the handler to receive the pushed job")
	dropped := fc.dropStream(t)
	close(release)
	fc.phase.Store(phaseRecovered)

	select {
	case got := <-fc.completions:
		fc.mu.Lock()
		connected := fc.connected
		fc.mu.Unlock()
		if got != connected {
			t.Errorf("completion went to %q; want %q, the gateway the stream reconnected to (dropped: %q)",
				got, connected, dropped)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("result-bearing completion was never sent")
	}
}

// TestFalconResultAckFallsBackToLastPeer: if the stream does not reconnect within
// the ack's wait budget, the completion goes to the last connected peer (whose
// REST endpoint may still be healthy), not straight to the configured gateway.
func TestFalconResultAckFallsBackToLastPeer(t *testing.T) {
	fc := newFailoverCluster(t)
	started, release := startFailoverWorker(t, fc, 2*time.Second)

	waitSignal(t, started, "the handler to receive the pushed job")
	dropped := fc.dropStream(t)
	close(release)

	select {
	case got := <-fc.completions:
		if got != dropped {
			t.Errorf("completion went to %q; want the last connected peer %q", got, dropped)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("result-bearing completion was never sent")
	}
}

// TestRestCompleteJobViaSkipsUnreachableGateway: an unreachable gateway (no HTTP
// response) moves the completion on to the next client, but a gateway that
// answers, even with an error, decides it.
func TestRestCompleteJobViaSkipsUnreachableGateway(t *testing.T) {
	hits := map[string]*atomic.Int32{"configured": {}, "answering": {}}
	serve := func(name string, status int) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits[name].Add(1)
			w.WriteHeader(status)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	configured := serve("configured", http.StatusNoContent)
	answering := serve("answering", http.StatusConflict)
	unreachable := httptest.NewServer(http.NotFoundHandler())
	unreachable.Close()

	client, err := New(WithRestAddress(configured.URL), WithNoAuth(), WithLogLevel(LogOff),
		WithRetry(RetryConfig{MaxAttempts: 1}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ws := func(s *httptest.Server) string { return "ws" + s.URL[len("http"):] + "/falcon" }
	job := &Job{key: "123"}

	client.restCompleteJobVia(context.Background(), job, nil, nil,
		client.restClientForEndpoint(ws(unreachable)), client.raw)
	if got := hits["configured"].Load(); got != 1 {
		t.Errorf("configured gateway hits after an unreachable peer = %d, want 1", got)
	}

	client.restCompleteJobVia(context.Background(), job, nil, nil,
		client.restClientForEndpoint(ws(answering)), client.raw)
	if got := hits["answering"].Load(); got != 1 {
		t.Errorf("answering peer hits = %d, want 1", got)
	}
	if got := hits["configured"].Load(); got != 1 {
		t.Errorf("configured gateway was tried after a peer answered (hits = %d, want 1)", got)
	}
}
