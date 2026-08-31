// The injected clock has to be usable from *outside* the package.
//
// This file is deliberately `package camunda_test`: an external consumer can only
// name exported identifiers, and can only implement Clock if every method signature
// is expressible from outside. The in-package tests cannot see that distinction —
// in the Rust SDK the equivalent trait shipped unnameable because every test for it
// lived inside the crate.
package camunda_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
)

// recordingClock is a downstream implementation, written against nothing but the
// exported API.
type recordingClock struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (c *recordingClock) Now() time.Time { return time.Now() }

func (c *recordingClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *recordingClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}

func (c *recordingClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

func TestClockIsImplementableFromOutsideThePackage(t *testing.T) {
	var clock camunda.Clock = &recordingClock{}

	if err := clock.Sleep(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	got := clock.(*recordingClock).recorded()
	if len(got) != 1 || got[0] != 30*time.Second {
		t.Fatalf("recorded %v, want [30s]", got)
	}
}

func TestWithClockReachesTheClient(t *testing.T) {
	clock := &recordingClock{}
	client, err := camunda.New(
		camunda.WithRestAddress("http://localhost:8080"),
		camunda.WithNoAuth(),
		camunda.WithClock(clock),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.Clock() != camunda.Clock(clock) {
		t.Fatal("the client is not using the injected clock")
	}
}

func TestTheDefaultClockIsLive(t *testing.T) {
	client, err := camunda.New(
		camunda.WithRestAddress("http://localhost:8080"),
		camunda.WithNoAuth(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := client.Clock().(camunda.LiveClock); !ok {
		t.Fatalf("default clock is %T, want camunda.LiveClock", client.Clock())
	}
}

func TestLiveClockSleepWaitsAndReportsCancellation(t *testing.T) {
	var clock camunda.Clock = camunda.LiveClock{}

	start := time.Now()
	if err := clock.Sleep(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("returned after %v, want at least 20ms", elapsed)
	}

	// A cancelled wait must report why, not report success.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := clock.Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep on a cancelled context returned %v, want context.Canceled", err)
	}

	// A non-positive wait returns immediately rather than blocking forever.
	done := make(chan error, 1)
	go func() { done <- clock.Sleep(context.Background(), 0) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Sleep(0): %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Sleep(0) blocked")
	}
}

// A zero-length wait on a cancelled context is the one case where cancellation can be
// lost. Both select cases are ready at once, and Go picks a ready case at random, so a
// Sleep that relies on the select alone reports cancellation only about half the time.
// Repeated because a single run passes either way.
func TestLiveClockSleepReportsCancellationEvenWhenTheWaitIsZero(t *testing.T) {
	var clock camunda.Clock = camunda.LiveClock{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for i := range 200 {
		if err := clock.Sleep(ctx, 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: Sleep(cancelled, 0) returned %v, want context.Canceled", i, err)
		}
	}
}

func TestLiveClockAfterFires(t *testing.T) {
	var clock camunda.Clock = camunda.LiveClock{}
	select {
	case <-clock.After(10 * time.Millisecond):
	case <-time.After(2 * time.Second):
		t.Fatal("After never fired")
	}
}
