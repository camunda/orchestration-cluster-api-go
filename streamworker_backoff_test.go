package camunda

import (
	"context"
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
