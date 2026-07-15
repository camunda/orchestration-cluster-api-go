package backpressure

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestIsBackpressureResponse(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{429, "", true},
		{503, "", true},
		{503, "gateway down", false},
		{503, "RESOURCE_EXHAUSTED", true},
		{500, "RESOURCE_EXHAUSTED", true},
		{500, "boom", false},
		{200, "", false},
		{404, "RESOURCE_EXHAUSTED", true},
	}
	for _, c := range cases {
		if got := IsBackpressureResponse(c.status, c.body); got != c.want {
			t.Errorf("IsBackpressureResponse(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}

func TestStartsUnlimited(t *testing.T) {
	m := New(Balanced)
	if st := m.State(); st.PermitsMax != nil {
		t.Errorf("expected unlimited on start, got permitsMax=%v", *st.PermitsMax)
	}
	// Acquire should be an immediate no-op while unlimited.
	if err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire while unlimited: %v", err)
	}
}

func TestBackpressureBootsAndShrinks(t *testing.T) {
	m := New(Balanced)
	m.RecordBackpressure() // first signal boots to initialMax and enters soft
	st := m.State()
	if st.PermitsMax == nil {
		t.Fatal("expected a permit cap after first backpressure signal")
	}
	if st.Severity != Soft {
		t.Errorf("expected soft severity, got %v", st.Severity)
	}
	// initialMax(16) * 0.70 = 11.2 -> ceil 12
	if *st.PermitsMax != 12 {
		t.Errorf("expected permitsMax 12 after soft shrink, got %d", *st.PermitsMax)
	}
}

func TestEscalatesToSevere(t *testing.T) {
	m := New(Balanced)
	for i := 0; i < severeThreshold; i++ {
		m.RecordBackpressure()
	}
	if st := m.State(); st.Severity != Severe {
		t.Errorf("expected severe after %d signals, got %v", severeThreshold, st.Severity)
	}
}

func TestAcquireGatesAndReleaseWakes(t *testing.T) {
	m := New(Balanced)
	// Force a tiny cap by driving many severe signals down to the floor.
	for i := 0; i < 10; i++ {
		m.RecordBackpressure()
	}
	st := m.State()
	if st.PermitsMax == nil || *st.PermitsMax != floor {
		t.Fatalf("expected permits at floor(%d), got %v", floor, st.PermitsMax)
	}
	// Clear the backoff-at-floor so Acquire doesn't sleep in this test.
	m.mu.Lock()
	m.backoff = 0
	m.mu.Unlock()

	// Acquire the single permit.
	if err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second acquire must block until Release.
	acquired := make(chan struct{})
	go func() {
		_ = m.Acquire(context.Background())
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("second acquire should have blocked while at capacity")
	case <-time.After(50 * time.Millisecond):
	}
	m.Release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second acquire did not wake after Release")
	}
}

func TestAcquireRespectsContext(t *testing.T) {
	m := New(Balanced)
	for i := 0; i < 10; i++ {
		m.RecordBackpressure()
	}
	m.mu.Lock()
	m.backoff = 0
	m.mu.Unlock()
	if err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire permit: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Acquire(ctx); err == nil {
		t.Fatal("expected context deadline error while at capacity")
	}
}

func TestLegacyObserveOnlyNeverGates(t *testing.T) {
	m := New(Legacy)
	for i := 0; i < 10; i++ {
		m.RecordBackpressure()
	}
	// Severity is still tracked...
	if m.Severity() != Severe {
		t.Errorf("legacy profile should still record severity, got %v", m.Severity())
	}
	// ...but Acquire never blocks, even without releasing.
	for i := 0; i < 100; i++ {
		if err := m.Acquire(context.Background()); err != nil {
			t.Fatalf("legacy acquire should never gate: %v", err)
		}
	}
}

func TestRecoversToUnlimited(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	m := New(Balanced).withClock(clk.now)
	m.RecordBackpressure() // boot + soft

	// Quiet period elapses; repeated healthy hints decay severity and recover.
	for i := 0; i < 200; i++ {
		clk.advance(2 * time.Second)
		m.RecordHealthyHint()
		if m.State().PermitsMax == nil {
			break
		}
	}
	if st := m.State(); st.PermitsMax != nil {
		t.Errorf("expected to return to unlimited after sustained health, got permitsMax=%d", *st.PermitsMax)
	}
	if m.Severity() != Healthy {
		t.Errorf("expected healthy severity after recovery, got %v", m.Severity())
	}
}
