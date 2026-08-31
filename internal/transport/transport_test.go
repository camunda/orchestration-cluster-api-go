package transport

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/camunda/orchestration-cluster-api-go/internal/auth"
	"github.com/camunda/orchestration-cluster-api-go/internal/backpressure"
	"github.com/camunda/orchestration-cluster-api-go/internal/retry"
)

type stubRT struct {
	calls   int
	status  int
	body    string
	sawAuth string
}

func (s *stubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	s.sawAuth = req.Header.Get("Authorization")
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func newReq(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example/v2/topology", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestChainAppliesAuthAndObservesHealthy(t *testing.T) {
	base := &stubRT{status: 200, body: "{}"}
	mgr := backpressure.New(backpressure.Balanced, testClock{})
	rt := New(Options{
		Base:         base,
		Auth:         &auth.Transport{Strategy: auth.Basic, BasicUsername: "u", BasicPassword: "p"},
		Retry:        retry.Config{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
		Backpressure: mgr,
	}, testClock{})
	resp, err := rt.RoundTrip(newReq(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.HasPrefix(base.sawAuth, "Basic ") {
		t.Errorf("expected auth header applied at base, got %q", base.sawAuth)
	}
	// A healthy response keeps the manager unlimited.
	if st := mgr.State(); st.PermitsMax != nil {
		t.Errorf("expected manager to stay unlimited after healthy response, got %d", *st.PermitsMax)
	}
}

func TestBackpressureRecordedOn429(t *testing.T) {
	base := &stubRT{status: 429}
	mgr := backpressure.New(backpressure.Balanced, testClock{})
	rt := &BackpressureTransport{Base: base, Mgr: mgr}
	if _, err := rt.RoundTrip(newReq(t)); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if st := mgr.State(); st.PermitsMax == nil {
		t.Error("expected backpressure to boot a permit cap on 429")
	}
}

func TestBackpressureRecordedOn503BareBody(t *testing.T) {
	base := &stubRT{status: 503, body: ""}
	mgr := backpressure.New(backpressure.Balanced, testClock{})
	rt := &BackpressureTransport{Base: base, Mgr: mgr}
	resp, err := rt.RoundTrip(newReq(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if st := mgr.State(); st.PermitsMax == nil {
		t.Error("expected backpressure recorded on bare 503")
	}
	// Body must still be readable by the caller after observation buffered it.
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Errorf("caller body should still be readable: %v", err)
	}
}

func TestExemptBypassesGate(t *testing.T) {
	base := &stubRT{status: 200}
	mgr := backpressure.New(backpressure.Balanced, testClock{})
	// Drive the manager to the floor with a full waiter queue would block a gated
	// request; an exempt request must pass regardless.
	for i := 0; i < 10; i++ {
		mgr.RecordBackpressure()
	}
	rt := &BackpressureTransport{
		Base:   base,
		Mgr:    mgr,
		Exempt: func(r *http.Request) bool { return true },
	}
	done := make(chan error, 1)
	go func() { _, err := rt.RoundTrip(newReq(t)); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exempt request failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("exempt request should not have been gated")
	}
}

// testClock keeps the transport chain off real time; nothing here asserts on delay.
type testClock struct{}

func (testClock) Now() time.Time                                   { return time.Unix(1_000_000, 0) }
func (testClock) Sleep(ctx context.Context, d time.Duration) error { return ctx.Err() }
