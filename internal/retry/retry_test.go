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
		Base:      base,
		Cfg:       Config{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond},
		randFloat: func() float64 { return 1.0 },
		sleep:     func(ctx context.Context, d time.Duration) error { return nil },
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
