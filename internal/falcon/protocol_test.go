package falcon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPickEndpointAvoidsFailedNode(t *testing.T) {
	eps := []string{"ws://a/falcon", "ws://b/falcon", "ws://c/falcon"}
	seed := uint64(0x1234_5678)
	for i := 0; i < 200; i++ {
		if got := pickEndpoint(eps, "ws://b/falcon", &seed); got == "ws://b/falcon" {
			t.Fatalf("pickEndpoint selected the avoided node")
		}
	}
	// A single-element directory always returns its only entry, even if avoided.
	one := []string{"ws://solo/falcon"}
	if got := pickEndpoint(one, "ws://solo/falcon", &seed); got != "ws://solo/falcon" {
		t.Fatalf("single-node directory should return its only entry, got %q", got)
	}
}

func TestLinkIdleScalesWithHeartbeat(t *testing.T) {
	if got := linkIdle(15_000); got != 45_000*time.Millisecond {
		t.Errorf("linkIdle(15000) = %v, want 45s", got)
	}
	if got := linkIdle(0); got != time.Duration(defaultHeartbeatMs*idleHeartbeatMult)*time.Millisecond {
		t.Errorf("linkIdle(0) should fall back to the default cadence, got %v", got)
	}
}

// falconServerHandler runs server-side against one accepted command-stream socket.
type falconServerHandler func(ctx context.Context, c *websocket.Conn)

// startFalconServer starts an in-process WebSocket gateway speaking the FALCON
// protocol and returns a one-endpoint directory plus a Dialer wired to it.
func startFalconServer(t *testing.T, handler falconServerHandler) ([]string, *Dialer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		handler(r.Context(), c)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return []string{"ws://" + u.Host + "/falcon"}, &Dialer{HTTPClient: srv.Client()}
}

func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, data)
}

func readJSON(ctx context.Context, c *websocket.Conn) (map[string]any, error) {
	_, data, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func TestProducerCreateOverStream(t *testing.T) {
	eps, d := startFalconServer(t, func(ctx context.Context, c *websocket.Conn) {
		_ = writeJSON(ctx, c, map[string]any{"type": "welcome", "submissionCredits": 5, "heartbeatMs": 15000})
		for {
			f, err := readJSON(ctx, c)
			if err != nil {
				return
			}
			if f["type"] == "createInstance" {
				_ = writeJSON(ctx, c, map[string]any{
					"type":   "commandResult",
					"corr":   f["corr"],
					"status": 200,
					"body":   map[string]any{"processInstanceKey": "2251799813685340", "processCompleted": false},
				})
			}
		}
	})

	p, err := StartProducer(eps, d)
	if err != nil {
		t.Fatalf("StartProducer: %v", err)
	}
	defer p.Close()

	out, err := p.Create(context.Background(), CreateArgs{ProcessDefinitionID: "order-process"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProcessInstanceKey != "2251799813685340" {
		t.Errorf("ProcessInstanceKey = %q", out.ProcessInstanceKey)
	}
}

func TestProducerCreateAwaitCompletion(t *testing.T) {
	eps, d := startFalconServer(t, func(ctx context.Context, c *websocket.Conn) {
		_ = writeJSON(ctx, c, map[string]any{"type": "welcome", "submissionCredits": 1})
		for {
			f, err := readJSON(ctx, c)
			if err != nil {
				return
			}
			if f["type"] == "createInstance" {
				_ = writeJSON(ctx, c, map[string]any{
					"type": "commandResult", "corr": f["corr"], "status": 200,
					"body": map[string]any{"processInstanceKey": "99"},
				})
				_ = writeJSON(ctx, c, map[string]any{
					"type": "instanceCompleted", "corr": f["corr"],
					"processCompleted": true, "variables": map[string]any{"result": "ok"},
				})
			}
		}
	})

	p, err := StartProducer(eps, d)
	if err != nil {
		t.Fatalf("StartProducer: %v", err)
	}
	defer p.Close()

	out, err := p.Create(context.Background(), CreateArgs{ProcessDefinitionID: "p", AwaitCompletion: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !out.ProcessCompleted || out.Variables["result"] != "ok" {
		t.Errorf("await outcome mismatch: %+v", out)
	}
}

func TestProducerCreditGating(t *testing.T) {
	// Grant exactly one credit and never replenish: the first create succeeds, the
	// second must block on the credit window until its context deadline.
	eps, d := startFalconServer(t, func(ctx context.Context, c *websocket.Conn) {
		_ = writeJSON(ctx, c, map[string]any{"type": "welcome", "submissionCredits": 1})
		for {
			f, err := readJSON(ctx, c)
			if err != nil {
				return
			}
			if f["type"] == "createInstance" {
				_ = writeJSON(ctx, c, map[string]any{
					"type": "commandResult", "corr": f["corr"], "status": 200,
					"body": map[string]any{"processInstanceKey": "1"},
				})
			}
		}
	})

	p, err := StartProducer(eps, d)
	if err != nil {
		t.Fatalf("StartProducer: %v", err)
	}
	defer p.Close()

	if _, err := p.Create(context.Background(), CreateArgs{ProcessDefinitionID: "p"}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = p.Create(ctx, CreateArgs{ProcessDefinitionID: "p"})
	if err == nil {
		t.Fatal("second create should have blocked on the exhausted credit window")
	}
}

func TestProducerCreateAPIError(t *testing.T) {
	eps, d := startFalconServer(t, func(ctx context.Context, c *websocket.Conn) {
		_ = writeJSON(ctx, c, map[string]any{"type": "welcome", "submissionCredits": 1})
		for {
			f, err := readJSON(ctx, c)
			if err != nil {
				return
			}
			if f["type"] == "createInstance" {
				_ = writeJSON(ctx, c, map[string]any{
					"type": "commandResult", "corr": f["corr"], "status": 409, "body": "conflict",
				})
			}
		}
	})

	p, err := StartProducer(eps, d)
	if err != nil {
		t.Fatalf("StartProducer: %v", err)
	}
	defer p.Close()

	_, err = p.Create(context.Background(), CreateArgs{ProcessDefinitionID: "p"})
	var re *RemoteError
	if err == nil || !asRemoteError(err, &re) {
		t.Fatalf("expected *RemoteError, got %v", err)
	}
	if re.Status != 409 || re.Body != "conflict" {
		t.Errorf("RemoteError = %+v", re)
	}
}

func TestStreamWorkerReceivesAndCompletes(t *testing.T) {
	gotSubscribe := make(chan map[string]any, 1)
	gotComplete := make(chan map[string]any, 1)
	eps, d := startFalconServer(t, func(ctx context.Context, c *websocket.Conn) {
		sub, err := readJSON(ctx, c)
		if err != nil {
			return
		}
		gotSubscribe <- sub
		_ = writeJSON(ctx, c, map[string]any{"type": "welcome", "heartbeatMs": 15000})
		_ = writeJSON(ctx, c, map[string]any{
			"type": "job",
			"job":  map[string]any{"jobKey": "777", "type": "email", "retries": 3},
		})
		for {
			f, err := readJSON(ctx, c)
			if err != nil {
				return
			}
			if f["type"] == "completeJob" {
				gotComplete <- f
			}
		}
	})

	w, err := Subscribe(eps, d, SubscribeArgs{JobType: "email", JobCredits: 8, Worker: "w1"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer w.Close()

	select {
	case sub := <-gotSubscribe:
		if sub["jobType"] != "email" || sub["worker"] != "w1" {
			t.Errorf("unexpected subscribe frame: %v", sub)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive subscribe frame")
	}

	job, ok := w.NextJob(context.Background(), 2*time.Second)
	if !ok {
		t.Fatal("expected a pushed job")
	}
	var decoded map[string]any
	if err := json.Unmarshal(job, &decoded); err != nil || decoded["jobKey"] != "777" {
		t.Fatalf("unexpected job payload: %v (err=%v)", decoded, err)
	}

	w.Complete("777", map[string]any{"ok": true})
	select {
	case f := <-gotComplete:
		if f["jobKey"] != "777" {
			t.Errorf("completeJob for wrong key: %v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gateway did not receive completeJob frame")
	}
}

// asRemoteError is a tiny errors.As wrapper kept local to avoid importing errors
// just for the test's type assertion.
func asRemoteError(err error, target **RemoteError) bool {
	re, ok := err.(*RemoteError)
	if ok {
		*target = re
	}
	return ok
}
