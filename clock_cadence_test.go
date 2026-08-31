// Cadence guards: the runtime must resolve waits and elapsed time through the injected
// clock, not the time package. Without these the seam exists and compiles while nothing
// uses it — the state slice 1 shipped in, and the defect the JS and C# SDKs shipped with.
package camunda_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
)

// countingClock records every wait and returns immediately, so a test observes the
// cadence that was asked for without spending it.
type countingClock struct {
	mu     sync.Mutex
	sleeps []time.Duration
	nows   int
	t      time.Time
}

func newCountingClock() *countingClock {
	return &countingClock{t: time.Unix(1_000_000, 0)}
}

func (c *countingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nows++
	return c.t
}

// maxRecordedSleeps bounds what a single test may wait. A loop that is meant to be
// paced but has fallen back to real time will call Sleep unboundedly against this
// instant-returning clock; failing loudly beats spinning until the test times out.
const maxRecordedSleeps = 10_000

var errRunawayLoop = errors.New("clock: implausible number of waits; the loop is spinning rather than pacing")

func (c *countingClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sleeps) >= maxRecordedSleeps {
		return errRunawayLoop
	}
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return nil
}

func (c *countingClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

func (c *countingClock) recorded() (sleeps []time.Duration, nows int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...), c.nows
}

// Retry backoff must be spent on the injected clock. Left on time.NewTimer this test
// still passes on wall-clock time but records nothing.
func TestRetryBackoffWaitsOnTheInjectedClock(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	// Always 503: the call is expected to fail. What is under test is that the waits
	// between attempts went through the clock, not what the endpoint returns.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	clock := newCountingClock()
	client, err := camunda.New(
		camunda.WithRestAddress(srv.URL),
		camunda.WithNoAuth(),
		camunda.WithClock(clock),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := client.GetTopology(context.Background()); err == nil {
		t.Fatal("expected the call to fail after exhausting retries")
	}

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got < 2 {
		t.Fatalf("server saw %d attempts; the call was not retried", got)
	}

	sleeps, _ := clock.recorded()
	if len(sleeps) == 0 {
		t.Fatal("retry backed off without going through the injected clock")
	}
	if len(sleeps) != got-1 {
		t.Fatalf("%d waits for %d attempts; expected one wait between each", len(sleeps), got)
	}
}

// Poll measures its deadline and paces its interval on the clock it is given.
func TestPollUsesTheInjectedClock(t *testing.T) {
	clock := newCountingClock()

	_, err := camunda.Poll(context.Background(),
		func(context.Context) (int, error) {
			// Poll retries while the predicate holds; the default is IsNotFound.
			return 0, &camunda.APIError{Status: http.StatusNotFound}
		},
		camunda.WithPollTimeout(3*time.Second),
		camunda.WithPollRetryInterval(250*time.Millisecond),
		camunda.WithPollClock(clock),
	)
	// The specific error matters: a runaway loop also returns non-nil, and an
	// "err != nil" assertion would accept it.
	if !errors.Is(err, camunda.ErrEventualConsistencyTimeout) {
		t.Fatalf("Poll returned %v, want a consistency timeout", err)
	}

	sleeps, nows := clock.recorded()
	for i, d := range sleeps {
		if d != 250*time.Millisecond {
			t.Fatalf("wait %d was %v, want the configured 250ms interval", i, d)
		}
	}
	// A 3s budget spent 250ms at a time is exactly 12 waits, because the fake clock
	// advances by each wait. Asserting the count is what catches a deadline measured
	// against real time: the injected clock never reaches it, so the loop spins.
	if want := 12; len(sleeps) != want {
		t.Fatalf("%d waits, want %d; the deadline is not being measured on the injected clock", len(sleeps), want)
	}
	if nows == 0 {
		t.Fatal("the poll deadline was never read from the injected clock")
	}
}
