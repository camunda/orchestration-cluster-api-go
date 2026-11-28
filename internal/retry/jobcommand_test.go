package retry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func jobCommandReq(t *testing.T, url string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url+"/v2/jobs/1/completion", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func strictTransport(base http.RoundTripper) *Transport {
	t := newTransport(base)
	t.NonIdempotent = func(*http.Request) bool { return true }
	return t
}

// A non-idempotent command is retried only on statuses that mean "not applied".
func TestNonIdempotentRetriesOnlyNotAppliedStatuses(t *testing.T) {
	for _, tc := range []struct {
		status int
		calls  int
	}{
		{http.StatusTooManyRequests, 4},
		{http.StatusServiceUnavailable, 4},
		{http.StatusGatewayTimeout, 1},
		{http.StatusBadGateway, 1},
		{http.StatusInternalServerError, 1},
		{http.StatusNotFound, 1},
	} {
		stub := &stubRT{statuses: []int{tc.status}}
		resp, err := strictTransport(stub).RoundTrip(jobCommandReq(t, "http://example"))
		if err != nil {
			t.Fatalf("%d: %v", tc.status, err)
		}
		_ = resp.Body.Close()
		if stub.calls != tc.calls {
			t.Errorf("status %d: calls = %d, want %d", tc.status, stub.calls, tc.calls)
		}
	}
}

// Against a real server returning 504, exactly one request is made.
func TestNonIdempotentGatewayTimeoutIsSingleAttempt(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer srv.Close()
	tr := strictTransport(http.DefaultTransport)
	resp, err := tr.RoundTrip(jobCommandReq(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if n.Load() != 1 {
		t.Fatalf("requests = %d, want 1", n.Load())
	}
}

// A connection that fails before the request is sent is retried.
func TestNonIdempotentRetriesPreSendConnectionFailure(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // nothing listens: connect refused
	var dials atomic.Int32
	base := &http.Transport{DialContext: func(ctx context.Context, network, a string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, a)
	}}
	_, err = strictTransport(base).RoundTrip(jobCommandReq(t, "http://"+addr))
	if err == nil {
		t.Fatal("want error")
	}
	if dials.Load() != 4 {
		t.Fatalf("dials = %d, want 4", dials.Load())
	}
}

// A connection that fails after the request was sent is not retried.
func TestNonIdempotentDoesNotRetryPostSendFailure(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		_ = c.Close() // reset without a response
	}))
	defer srv.Close()
	_, err := strictTransport(&http.Transport{}).RoundTrip(jobCommandReq(t, srv.URL))
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a transport error", err)
	}
	if n.Load() != 1 {
		t.Fatalf("requests = %d, want 1", n.Load())
	}
}

type errRoundTripper struct {
	n   atomic.Int32
	err error
}

func (e *errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	e.n.Add(1)
	return nil, e.err
}

// A custom transport that emits no connection trace and fails with an error
// that does not prove a pre-send failure is not retried.
func TestNonIdempotentDoesNotRetryUntracedTransportError(t *testing.T) {
	for _, err := range []error{
		errors.New("read: connection reset"),
		&net.OpError{Op: "read", Err: errors.New("reset")},
		io.ErrUnexpectedEOF,
	} {
		rt := &errRoundTripper{err: err}
		if _, got := strictTransport(rt).RoundTrip(jobCommandReq(t, "http://example")); got == nil {
			t.Fatal("want error")
		}
		if rt.n.Load() != 1 {
			t.Errorf("%v: attempts = %d, want 1", err, rt.n.Load())
		}
	}
}

// Pre-send failures are retried even when an earlier connection (e.g. an
// OAuth token fetch sharing the request context) succeeded.
func TestNonIdempotentRetriesPreSendErrorAfterOtherConnection(t *testing.T) {
	for _, err := range []error{
		&net.OpError{Op: "dial", Err: errors.New("refused")},
		&net.DNSError{Err: "no such host", Name: "x"},
	} {
		rt := &errRoundTripper{err: err}
		_, _ = strictTransport(rt).RoundTrip(jobCommandReq(t, "http://example"))
		if rt.n.Load() != 4 {
			t.Errorf("%v: attempts = %d, want 4", err, rt.n.Load())
		}
	}
}
