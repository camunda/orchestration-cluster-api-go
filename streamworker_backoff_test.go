package camunda

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixedRandom float64

func (f fixedRandom) Float64() float64 { return float64(f) }

// Consecutive empty polls grow the delay by the multiplier and cap it at Max.
func TestStreamPollBackoffGrowsOnEmptyPollsAndCaps(t *testing.T) {
	b := DefaultStreamPollBackoff
	base := 100 * time.Millisecond
	r := fixedRandom(0.5) // zero jitter
	d := base
	var prev time.Duration
	for i := 0; i < 40; i++ {
		d = b.nextDelay(d, base, 0, r)
		if d > b.Max {
			t.Fatalf("poll %d: delay %v exceeds max %v", i, d, b.Max)
		}
		if d < prev {
			t.Fatalf("poll %d: delay %v shrank from %v", i, d, prev)
		}
		prev = d
	}
	if d != b.Max {
		t.Fatalf("delay after many empty polls = %v, want %v", d, b.Max)
	}
	if got := b.nextDelay(base, base, 0, r); got != 160*time.Millisecond {
		t.Fatalf("first backoff = %v, want 160ms", got)
	}
}

// A poll that activates jobs resets the delay to the configured interval.
func TestStreamPollBackoffResetsOnNonEmptyPoll(t *testing.T) {
	base := 30 * time.Second
	if got := DefaultStreamPollBackoff.nextDelay(time.Minute, base, 3, fixedRandom(0.9)); got != base {
		t.Fatalf("delay after non-empty poll = %v, want %v", got, base)
	}
}

// Jitter stays within ±Jitter of the un-jittered delay, and never below base.
func TestStreamPollBackoffJitterBounds(t *testing.T) {
	b := DefaultStreamPollBackoff
	base := time.Second
	lo := b.nextDelay(10*time.Second, base, 0, fixedRandom(0))
	hi := b.nextDelay(10*time.Second, base, 0, fixedRandom(0.999999))
	if lo < 14400*time.Millisecond || hi > 17600*time.Millisecond || lo >= hi {
		t.Fatalf("jitter range [%v, %v] outside 16s ±10%%", lo, hi)
	}
	if got := b.nextDelay(base, base, 0, fixedRandom(0)); got < base {
		t.Fatalf("jittered delay %v below base %v", got, base)
	}
}

// A zero StreamPollBackoff disables the backoff: the fixed interval is kept.
func TestStreamPollBackoffDisabled(t *testing.T) {
	base := 30 * time.Second
	if got := (StreamPollBackoff{}).nextDelay(base, base, 0, fixedRandom(0.5)); got != base {
		t.Fatalf("disabled backoff delay = %v, want %v", got, base)
	}
}

// The default is wired into new stream workers and the option overrides it.
func TestStreamWorkerPollBackoffOption(t *testing.T) {
	c, err := New(WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatal(err)
	}
	h := func(_ context.Context, _ *Job) (map[string]any, error) { return nil, nil }
	if w := c.NewStreamJobWorker("t", h); w.pollBackoff != DefaultStreamPollBackoff {
		t.Fatalf("default backoff = %+v", w.pollBackoff)
	}
	if w := c.NewStreamJobWorker("t", h, WithStreamPollBackoff(StreamPollBackoff{})); w.pollBackoff != (StreamPollBackoff{}) {
		t.Fatalf("override backoff = %+v", w.pollBackoff)
	}
}

// Positive jitter at the cap must not push the delay past Max.
func TestStreamPollBackoffJitterClampedToMax(t *testing.T) {
	b := DefaultStreamPollBackoff
	if got := b.nextDelay(time.Minute, time.Second, 0, fixedRandom(0.999999)); got > b.Max {
		t.Fatalf("jittered delay %v exceeds Max %v", got, b.Max)
	}
}

// While the gRPC stream is down the sidecar poll is the only delivery channel:
// empty polls must not back off, and a reconnect resumes backoff from base.
func TestStreamSidecarDelayNoBackoffWhileStreamDown(t *testing.T) {
	c, err := New(WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatal(err)
	}
	h := func(_ context.Context, _ *Job) (map[string]any, error) { return nil, nil }
	w := c.NewStreamJobWorker("t", h, WithStreamPollInterval(time.Second))
	if got := w.sidecarDelay(30*time.Second, 0, false); got != time.Second {
		t.Fatalf("stream down: delay = %v, want base", got)
	}
	if got := w.sidecarDelay(time.Second, 0, true); got <= time.Second {
		t.Fatalf("stream up: delay = %v, want growth", got)
	}
}

func TestSidecarSleepWakesOnStreamStateChange(t *testing.T) {
	c := &CamundaClient{clock: LiveClock{}}
	w := &StreamJobWorker{client: c, streamChanged: make(chan struct{}, 1)}
	w.setStreamUp(true)
	<-w.streamChanged // drain the up signal
	go func() { time.Sleep(20 * time.Millisecond); w.setStreamUp(false) }()
	start := time.Now()
	woke, err := w.sidecarSleep(context.Background(), time.Minute)
	if err != nil || !woke {
		t.Fatalf("woke=%v err=%v", woke, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("sleep not interrupted by disconnect")
	}
	// No-op transition must not signal.
	w.setStreamUp(false)
	if len(w.streamChanged) != 0 {
		t.Fatal("unchanged state signalled")
	}
}

// A flapping stream wakes the sidecar on every transition, but REST polls must
// still respect the base interval (backoff disabled): a wake never polls sooner
// than pollInterval after the previous poll.
func TestSidecarPollFlappingStreamRespectsBaseInterval(t *testing.T) {
	var polls atomic.Int32
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jobs":[]}`)
	}))
	defer rest.Close()
	c, err := New(WithRestAddress(rest.URL), WithNoAuth(), WithLogLevel(LogOff))
	if err != nil {
		t.Fatal(err)
	}
	h := func(_ context.Context, _ *Job) (map[string]any, error) { return nil, nil }
	w := c.NewStreamJobWorker("t", h, WithStreamPollInterval(200*time.Millisecond),
		WithStreamPollBackoff(StreamPollBackoff{}))
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() { defer close(done); w.runSidecarPoll(ctx, make(chan struct{}, 1), &wg) }()
	stop := time.After(time.Second)
	up := false
flap:
	for {
		select {
		case <-stop:
			break flap
		case <-time.After(5 * time.Millisecond):
			up = !up
			w.setStreamUp(up)
		}
	}
	cancel()
	<-done
	wg.Wait()
	// ~1s / 200ms ≈ 6 polls; without the floor this is ~200.
	if n := polls.Load(); n > 8 {
		t.Fatalf("flapping stream drove %d polls in 1s, want <= 8", n)
	}
}
