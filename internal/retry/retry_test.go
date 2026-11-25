package retry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stubRT returns programmed responses/errors in sequence and counts calls.
type stubRT struct {
	calls    int
	statuses []int // status per attempt; 0 means "return err instead"
	err      error // error to return when status is 0
	bodies   []string
}

func (s *stubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	i := s.calls
	s.calls++
	var status int
	if i < len(s.statuses) {
		status = s.statuses[i]
	} else if len(s.statuses) > 0 {
		status = s.statuses[len(s.statuses)-1]
	}
	if status == 0 {
		return nil, s.err
	}
	body := ""
	if i < len(s.bodies) {
		body = s.bodies[i]
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func newTransport(base http.RoundTripper) *Transport {
	return &Transport{
		Base:   base,
		Cfg:    Config{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond},
		Clock:  instantClock{},
		Random: fixedRandom(0.5),
	}
}

func req(t *testing.T) *http.Request {
	r, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example/v2/topology", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIsRetryableStatus(t *testing.T) {
	for _, s := range []int{429, 502, 503, 504} {
		if !IsRetryableStatus(s) {
			t.Errorf("status %d should be retryable", s)
		}
	}
	for _, s := range []int{200, 201, 400, 401, 404, 409, 500, 501} {
		if IsRetryableStatus(s) {
			t.Errorf("status %d should not be retryable", s)
		}
	}
}

func problem(title string) string {
	return `{"type":"about:blank","title":"` + title + `","status":504}`
}

// createProbe answers 504 for outcomes that end the probe: a worker collected it and did
// not answer by its deadline, or it still waited for its secrets. Retrying would create
// the probe again and run the worker again, only to fail the same way after another full
// timeout, so those are attempted once, and the caller still gets the problem body.
func TestTerminalProbeTimeoutsAreAttemptedOnce(t *testing.T) {
	for _, title := range []string{"WORKER_DID_NOT_ANSWER", "SECRET_RESOLUTION_TIMEOUT"} {
		base := &stubRT{statuses: []int{504}, bodies: []string{problem(title)}}
		resp, err := newTransport(base).RoundTrip(req(t))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", title, err)
		}
		if resp.StatusCode != 504 {
			t.Errorf("%s: expected the 504 back, got %d", title, resp.StatusCode)
		}
		if base.calls != 1 {
			t.Errorf("%s: expected 1 attempt, got %d", title, base.calls)
		}
		got, _ := io.ReadAll(resp.Body)
		if string(got) != problem(title) {
			t.Errorf("%s: response body not preserved: %q", title, got)
		}
	}
}

// Every other 504 stays transient: a gateway timeout after a leader change
// (DEADLINE_EXCEEDED), and one whose body is missing or unreadable.
func TestOtherGatewayTimeoutsAreStillRetried(t *testing.T) {
	for name, body := range map[string]string{
		"deadline exceeded": problem("DEADLINE_EXCEEDED"),
		"empty":             "",
		"not json":          "<html>Gateway Timeout</html>",
		"array":             "[]",
		"title not string":  `{"title":7}`,
		"no title":          `{"detail":"x"}`,
	} {
		base := &stubRT{statuses: []int{504}, bodies: []string{body, body, body, body}}
		resp, err := newTransport(base).RoundTrip(req(t))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if resp.StatusCode != 504 || base.calls != 4 {
			t.Errorf("%s: expected 4 attempts ending in 504, got %d attempts, status %d", name, base.calls, resp.StatusCode)
		}
	}
}

// The terminal titles only mean "over" on a 504; on any other retryable status the
// body is not consulted.
func TestTerminalTitlesDoNotChangeOtherStatuses(t *testing.T) {
	for _, status := range []int{429, 502, 503} {
		base := &stubRT{statuses: []int{status}, bodies: []string{problem("WORKER_DID_NOT_ANSWER")}}
		if _, err := newTransport(base).RoundTrip(req(t)); err != nil {
			t.Fatal(err)
		}
		if base.calls != 4 {
			t.Errorf("status %d: expected 4 attempts, got %d", status, base.calls)
		}
	}
}

func TestRetriesUntilSuccess(t *testing.T) {
	base := &stubRT{statuses: []int{503, 503, 200}}
	resp, err := newTransport(base).RoundTrip(req(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if base.calls != 3 {
		t.Errorf("expected 3 attempts, got %d", base.calls)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	base := &stubRT{statuses: []int{503}}
	resp, err := newTransport(base).RoundTrip(req(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 503 {
		t.Errorf("expected final 503, got %d", resp.StatusCode)
	}
	if base.calls != 4 {
		t.Errorf("expected 4 attempts (MaxAttempts), got %d", base.calls)
	}
}

func TestDoesNotRetryNonRetryableStatus(t *testing.T) {
	base := &stubRT{statuses: []int{404}}
	resp, err := newTransport(base).RoundTrip(req(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	if base.calls != 1 {
		t.Errorf("expected 1 attempt for non-retryable status, got %d", base.calls)
	}
}

func TestRetriesNetworkError(t *testing.T) {
	base := &stubRT{statuses: []int{0, 0, 200}, err: errors.New("connection refused")}
	resp, err := newTransport(base).RoundTrip(req(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 after network retries, got %d", resp.StatusCode)
	}
	if base.calls != 3 {
		t.Errorf("expected 3 attempts, got %d", base.calls)
	}
}

func TestDoesNotRetryContextCanceled(t *testing.T) {
	base := &stubRT{statuses: []int{0}, err: context.Canceled}
	_, err := newTransport(base).RoundTrip(req(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if base.calls != 1 {
		t.Errorf("expected no retries on context cancellation, got %d attempts", base.calls)
	}
}

// instantClock makes backoff free so the retry tests exercise attempt counting
// rather than wall-clock delay.
type instantClock struct{}

func (instantClock) Sleep(ctx context.Context, d time.Duration) error { return ctx.Err() }

// fixedRandom draws the same value every time.
type fixedRandom float64

func (f fixedRandom) Float64() float64 { return float64(f) }

// The backoff is the draw scaled onto the capped exponential window, so a
// controlled draw yields the exact delay rather than a range.
func TestBackoffScalesTheDrawOntoTheCappedWindow(t *testing.T) {
	tr := &Transport{
		Cfg:    Config{MaxAttempts: 10, BaseDelay: 100 * time.Millisecond, MaxDelay: 5 * time.Second},
		Random: fixedRandom(0.25),
	}
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{0, 25 * time.Millisecond},    // 0.25 * 100ms
		{3, 200 * time.Millisecond},   // 0.25 * 800ms
		{6, 1250 * time.Millisecond},  // 6.4s capped to 5s
		{40, 1250 * time.Millisecond}, // the shift is bounded, then capped
	} {
		if got := tr.backoff(tc.attempt); got != tc.want {
			t.Errorf("backoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}
