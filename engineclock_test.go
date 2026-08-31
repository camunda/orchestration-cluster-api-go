package camunda_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
)

// fakeEngine records what the engine was asked to do, and can be made to refuse.
type fakeEngine struct {
	mu     sync.Mutex
	pins   []time.Time
	resets int

	refuse bool
	// slow burns real time inside the request, so a pin can outlast the advance it
	// asks for.
	slow bool
	// block, when non-nil, holds every pin until the test closes it.
	block chan struct{}
}

var errRefused = errors.New("engine refused")

func (e *fakeEngine) PinAt(_ context.Context, t time.Time) error {
	if e.refuse {
		return errRefused
	}
	if e.slow {
		time.Sleep(20 * time.Millisecond) //nolint:forbidigo // deliberately outlasts the advance
	}
	if e.block != nil {
		<-e.block
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pins = append(e.pins, t)
	return nil
}

func (e *fakeEngine) ResetToLive(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resets++
	return nil
}

func (e *fakeEngine) recorded() ([]time.Time, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Time(nil), e.pins...), e.resets
}

func TestSequentialWaitsCompose(t *testing.T) {
	engine := &fakeEngine{}
	clock := camunda.NewEngineClock(engine)
	ctx := context.Background()

	if err := clock.Sleep(ctx, time.Second); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	afterFirst := clock.Now()
	if err := clock.Sleep(ctx, 2*time.Second); err != nil {
		t.Fatalf("second wait: %v", err)
	}

	pins, _ := engine.recorded()
	if len(pins) != 2 {
		t.Fatalf("%d pins, want one per wait", len(pins))
	}
	if got := pins[1].Sub(pins[0]); got != 2*time.Second {
		t.Fatalf("second wait advanced the engine by %v, want 2s from where the first ended", got)
	}
	if got := clock.Now().Sub(afterFirst); got != 2*time.Second {
		t.Fatalf("clock advanced %v, want 2s", got)
	}
}

// Waits that overlap an in-flight pin are satisfied by the *same* instant. Deriving
// each wake time from the pinned value at request time would advance the engine ten
// seconds -- the defect the C# SDK was corrected for.
//
// The engine is deliberately slow so all ten waits read the clock while the first pin
// is still in flight. Without that they would not overlap, and each would correctly
// compose from the previous one.
func TestOverlappingWaitsSettleAtOneInstant(t *testing.T) {
	engine := &fakeEngine{slow: true}
	clock := camunda.NewEngineClock(engine)
	ctx := context.Background()

	// Pin to a known instant first, so every reading below is exact rather than
	// depending on how much real time elapsed between them.
	base := time.Unix(1_700_000_000, 0).UTC()
	if err := clock.PinTo(ctx, base); err != nil {
		t.Fatalf("initial pin: %v", err)
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := clock.Sleep(ctx, time.Second); err != nil {
				t.Errorf("wait: %v", err)
			}
		}()
	}
	wg.Wait()

	pins, _ := engine.recorded()
	if len(pins) != 2 {
		t.Fatalf("%d pins, want 2 (the base plus one shared wake instant): %v", len(pins), pins)
	}
	if got := clock.Now(); !got.Equal(base.Add(time.Second)) {
		t.Fatalf("clock at %v, want %v; concurrent waits summed", got, base.Add(time.Second))
	}
}

func TestReadingsFollowLiveTimeUntilTheFirstWait(t *testing.T) {
	clock := camunda.NewEngineClock(&fakeEngine{})
	if clock.IsPinned() {
		t.Fatal("a new engine clock should not be pinned")
	}
	if drift := time.Since(clock.Now()); drift > time.Second { //nolint:forbidigo // comparing against real time is the point
		t.Fatalf("unpinned clock drifted from live time by %v", drift)
	}
	if err := clock.Sleep(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if !clock.IsPinned() {
		t.Fatal("a wait should have pinned the clock")
	}
}

func TestResetReturnsTheClockToLiveTime(t *testing.T) {
	engine := &fakeEngine{}
	clock := camunda.NewEngineClock(engine)
	ctx := context.Background()

	if err := clock.Sleep(ctx, time.Hour); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if err := clock.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if _, resets := engine.recorded(); resets != 1 {
		t.Fatalf("%d resets, want 1", resets)
	}
	if clock.IsPinned() {
		t.Fatal("reset left the clock pinned")
	}
	if drift := time.Since(clock.Now()); drift > time.Second { //nolint:forbidigo // comparing against real time is the point
		t.Fatalf("after reset the clock should follow live time, drifted %v", drift)
	}
}

// A refused pin must leave the clock exactly where it was. Publishing the new reading
// first would report a time the engine never moved to.
func TestARefusedPinLeavesTheClockUntouched(t *testing.T) {
	clock := camunda.NewEngineClock(&fakeEngine{refuse: true})

	err := clock.Sleep(context.Background(), time.Second)
	if !errors.Is(err, errRefused) {
		t.Fatalf("Sleep returned %v, want the engine's refusal", err)
	}
	if clock.IsPinned() {
		t.Fatal("a failed pin was published locally anyway")
	}
}

func TestAZeroWaitDoesNotTouchTheEngine(t *testing.T) {
	engine := &fakeEngine{}
	clock := camunda.NewEngineClock(engine)
	if err := clock.Sleep(context.Background(), 0); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if pins, _ := engine.recorded(); len(pins) != 0 {
		t.Fatalf("a zero wait moved the engine: %v", pins)
	}
}

// A pin to an earlier instant is honoured rather than clamped forward: the SDK's
// reading has to match the engine's, and the engine really did move back.
func TestAPinToThePastIsHonoured(t *testing.T) {
	clock := camunda.NewEngineClock(&fakeEngine{})
	past := time.Unix(1_700_000_000, 0).UTC()

	if err := clock.PinTo(context.Background(), past); err != nil {
		t.Fatalf("PinTo: %v", err)
	}
	if got := clock.Now(); !got.Equal(past) {
		t.Fatalf("Now() is %v, want the pinned instant %v", got, past)
	}
}

// A wait must never move the clock backwards, even when the round-trip outlasts the
// advance it asked for: Sleep adds to the current reading rather than to an instant
// captured elsewhere.
func TestAWaitNeverMovesTheClockBackwards(t *testing.T) {
	clock := camunda.NewEngineClock(&fakeEngine{slow: true})
	ctx := context.Background()

	before := clock.Now()
	for range 5 {
		if err := clock.Sleep(ctx, time.Millisecond); err != nil {
			t.Fatalf("Sleep: %v", err)
		}
		if now := clock.Now(); now.Before(before) {
			t.Fatalf("clock went backwards: %v < %v", now, before)
		}
		before = clock.Now()
	}
}

// After must not block on the engine round-trip: it exists to be used in a select,
// and a select evaluates it before any case can be chosen.
func TestAfterDoesNotBlockOnTheEngine(t *testing.T) {
	engine := &fakeEngine{block: make(chan struct{})}
	clock := camunda.NewEngineClock(engine)

	ch := clock.After(time.Second)

	// The pin is still held open, so reaching here at all is the assertion: a
	// synchronous After would not have returned yet.
	if pins, _ := engine.recorded(); len(pins) != 0 {
		t.Fatalf("the pin completed before After returned: %v", pins)
	}
	select {
	case <-ch:
		t.Fatal("the channel fired before the engine accepted the pin")
	default:
	}

	close(engine.block)
	select {
	case got := <-ch:
		if pins, _ := engine.recorded(); len(pins) != 1 {
			t.Fatalf("%d pins, want 1", len(pins))
		}
		if !got.Equal(clock.Now()) {
			t.Fatalf("channel carried %v, want the advanced reading %v", got, clock.Now())
		}
	case <-time.After(5 * time.Second): //nolint:forbidigo // test-only safety net
		t.Fatal("the channel never fired after the pin landed")
	}
}
