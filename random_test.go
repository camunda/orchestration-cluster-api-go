package camunda_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
)

// seed42 is the cross-SDK conformance vector: the first five draws of a source
// seeded with 42, in every Camunda SDK.
var seed42 = []float64{
	0.7415648787718233,
	0.1599103928769201,
	0.27860113025513866,
	0.34419071652363753,
	0.03803016854024621,
}

func TestSeededRandomMatchesTheCrossSDKVector(t *testing.T) {
	r := camunda.NewSeededRandom(42)
	for i, want := range seed42 {
		if got := r.Float64(); got != want {
			t.Fatalf("draw %d = %v, want %v", i, got, want)
		}
	}
	// Seed 0's first SplitMix64 output is 0xE220A8397B1DCDAF; the draw is its top
	// 53 bits.
	if got, want := camunda.NewSeededRandom(0).Float64(), float64(uint64(0xE220A8397B1DCDAF)>>11)*0x1p-53; got != want {
		t.Fatalf("seed 0 first draw = %v, want %v", got, want)
	}
}

// Concurrent callers share one sequence: every draw is handed out exactly once,
// so the multiset of draws equals the sequential one.
func TestSeededRandomIsSafeForConcurrentUse(t *testing.T) {
	const goroutines, each = 8, 1000
	shared := camunda.NewSeededRandom(7)
	got := make([]float64, 0, goroutines*each)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]float64, each)
			for i := range local {
				local[i] = shared.Float64()
			}
			mu.Lock()
			got = append(got, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	sequential := camunda.NewSeededRandom(7)
	want := make([]float64, goroutines*each)
	for i := range want {
		want[i] = sequential.Float64()
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatal("concurrent draws are not the sequential sequence: a state update was lost or repeated")
	}
}

func TestSeededRandomNamesItsSeed(t *testing.T) {
	r := camunda.NewSeededRandom(42)
	if r.Seed() != 42 {
		t.Fatalf("Seed() = %d, want 42", r.Seed())
	}
	if got, want := r.String(), "SeededRandom(seed=42; replay with CAMUNDA_TEST_SEED=42)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestSeededRandomFromEnvReadsTheSeed(t *testing.T) {
	for raw, want := range map[string]uint64{
		"42":                   42,
		"0":                    0,
		"007":                  7,
		"18446744073709551615": math.MaxUint64,
	} {
		t.Setenv(camunda.SeedEnvVar, raw)
		r, err := camunda.SeededRandomFromEnv()
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if r.Seed() != want {
			t.Errorf("%q: Seed() = %d, want %d", raw, r.Seed(), want)
		}
	}
}

// Only an unset variable means absent. A value that is set but malformed fails
// rather than running the test under a seed nobody asked for.
func TestSeededRandomFromEnvRejectsAMalformedSeed(t *testing.T) {
	for _, raw := range []string{
		"", " 42", "42 ", "+1", "-1", "0x2A", "1_0", "4.2", "1e3",
		"18446744073709551616", "٤٢",
	} {
		t.Setenv(camunda.SeedEnvVar, raw)
		r, err := camunda.SeededRandomFromEnv()
		if !errors.Is(err, camunda.ErrConfig) {
			t.Errorf("%q: err = %v, want ErrConfig", raw, err)
		}
		if r != nil {
			t.Errorf("%q: returned a source alongside the error", raw)
		}
	}
}

func TestSeededRandomFromEnvDrawsAFreshSeedWhenUnset(t *testing.T) {
	t.Setenv(camunda.SeedEnvVar, "") // restored after the test
	if err := os.Unsetenv(camunda.SeedEnvVar); err != nil {
		t.Fatal(err)
	}
	a, err := camunda.SeededRandomFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	b, err := camunda.SeededRandomFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	// A fresh seed is a 53-bit draw, so it survives a round trip through a double
	// in the SDKs that hold seeds that way.
	if a.Seed() >= 1<<53 || b.Seed() >= 1<<53 {
		t.Fatalf("fresh seeds %d, %d exceed 53 bits", a.Seed(), b.Seed())
	}
	if a.Seed() == b.Seed() {
		t.Fatalf("two fresh seeds were both %d; the seed is not being drawn", a.Seed())
	}
}

func TestWithRandomReachesTheClient(t *testing.T) {
	r := camunda.NewSeededRandom(1)
	client, err := camunda.New(camunda.WithRestAddress("http://127.0.0.1:1"), camunda.WithNoAuth(), camunda.WithRandom(r))
	if err != nil {
		t.Fatal(err)
	}
	if client.Random() != r {
		t.Fatalf("Random() = %v, want the injected source", client.Random())
	}

	client, err = camunda.New(camunda.WithRestAddress("http://127.0.0.1:1"), camunda.WithNoAuth(), camunda.WithRandom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.Random().(camunda.LiveRandom); !ok {
		t.Fatalf("a nil Random should select LiveRandom, got %T", client.Random())
	}
}

func TestWithRandomRejectsATypedNil(t *testing.T) {
	_, err := camunda.New(camunda.WithRestAddress("http://127.0.0.1:1"), camunda.WithNoAuth(), camunda.WithRandom((*camunda.SeededRandom)(nil)))
	if !errors.Is(err, camunda.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestLiveRandomDrawsInTheUnitInterval(t *testing.T) {
	for range 1000 {
		if u := (camunda.LiveRandom{}).Float64(); u < 0 || u >= 1 {
			t.Fatalf("draw %v outside [0, 1)", u)
		}
	}
}

// With a seeded source, retry backoff is an exact sequence rather than a range.
func TestSeededRetryBackoffIsExact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	clock := newCountingClock()
	client, err := camunda.New(
		camunda.WithRestAddress(srv.URL),
		camunda.WithNoAuth(),
		camunda.WithClock(clock),
		camunda.WithRandom(camunda.NewSeededRandom(42)),
		camunda.WithRetry(camunda.RetryConfig{MaxAttempts: 4, BaseDelay: 100 * time.Millisecond, MaxDelay: 5 * time.Second}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetTopology(context.Background()); err == nil {
		t.Fatal("expected the call to fail after exhausting retries")
	}

	sleeps, _ := clock.recorded()
	want := []time.Duration{
		time.Duration(float64(100*time.Millisecond) * seed42[0]),
		time.Duration(float64(200*time.Millisecond) * seed42[1]),
		time.Duration(float64(400*time.Millisecond) * seed42[2]),
	}
	if !slices.Equal(sleeps, want) {
		t.Fatalf("backoff = %v, want %v", sleeps, want)
	}
}

// The same property for an arbitrary seed: the wait is exactly the one a replay
// of the seed predicts. Set CAMUNDA_TEST_SEED to the reported seed to reproduce.
func TestRetryBackoffIsReplayableForAnySeed(t *testing.T) {
	random, err := camunda.SeededRandomFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	clock := newCountingClock()
	client, err := camunda.New(
		camunda.WithRestAddress(srv.URL),
		camunda.WithNoAuth(),
		camunda.WithClock(clock),
		camunda.WithRandom(random),
		camunda.WithRetry(camunda.RetryConfig{MaxAttempts: 2, BaseDelay: 100 * time.Millisecond, MaxDelay: 5 * time.Second}),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = client.GetTopology(context.Background())

	want := time.Duration(float64(100*time.Millisecond) * camunda.NewSeededRandom(random.Seed()).Float64())
	if want < 0 || want >= 100*time.Millisecond {
		t.Fatalf("%v: predicted wait %v is outside the full-jitter window [0, 100ms)", random, want)
	}
	if sleeps, _ := clock.recorded(); !slices.Equal(sleeps, []time.Duration{want}) {
		t.Fatalf("%v: backoff = %v, want [%v]", random, sleeps, want)
	}
}

// stopClock records the first wait it is asked for and ends the run there, so a
// test observes a worker's startup delay without anything after it.
type stopClock struct {
	mu    sync.Mutex
	waits []time.Duration
}

var errStopped = errors.New("stopClock: stopped at the first wait")

func (*stopClock) Now() time.Time                       { return time.Unix(1_000_000, 0) }
func (*stopClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

func (c *stopClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	return errStopped
}

func (c *stopClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.waits)
}

func noopHandler(context.Context, *camunda.Job) (map[string]any, error) { return nil, nil }

// Each worker draws its startup delay when it is built, so a seeded source hands
// out delays in construction order however the Run goroutines are scheduled.
func TestWorkerStartupDelayIsDrawnInConstructionOrder(t *testing.T) {
	t.Setenv("CAMUNDA_WORKER_STARTUP_JITTER_MAX_SECONDS", "10")
	type runner interface{ Run(context.Context) error }
	for name, build := range map[string]func(*camunda.CamundaClient) runner{
		"job worker":    func(c *camunda.CamundaClient) runner { return c.NewJobWorker("t", noopHandler) },
		"stream worker": func(c *camunda.CamundaClient) runner { return c.NewStreamJobWorker("t", noopHandler) },
	} {
		t.Run(name, func(t *testing.T) {
			clock := &stopClock{}
			client, err := camunda.New(
				camunda.WithRestAddress("http://127.0.0.1:1"),
				camunda.WithNoAuth(),
				camunda.WithClock(clock),
				camunda.WithRandom(camunda.NewSeededRandom(42)),
			)
			if err != nil {
				t.Fatal(err)
			}
			first, second := build(client), build(client)

			// Run them in the opposite order to construction.
			for _, w := range []runner{second, first} {
				if err := w.Run(context.Background()); !errors.Is(err, errStopped) {
					t.Fatalf("Run = %v, want it to stop at the startup wait", err)
				}
			}

			want := []time.Duration{
				time.Duration(seed42[1] * 10 * float64(time.Second)),
				time.Duration(seed42[0] * 10 * float64(time.Second)),
			}
			if got := clock.recorded(); !slices.Equal(got, want) {
				t.Fatalf("startup waits = %v, want %v", got, want)
			}
		})
	}
}

// With no startup jitter configured a worker neither waits nor consumes a draw,
// so turning the feature off leaves every other draw where it was.
func TestWorkerWithoutStartupJitterDrawsNothing(t *testing.T) {
	t.Setenv("CAMUNDA_WORKER_STARTUP_JITTER_MAX_SECONDS", "0")
	r := camunda.NewSeededRandom(42)
	client, err := camunda.New(camunda.WithRestAddress("http://127.0.0.1:1"), camunda.WithNoAuth(), camunda.WithRandom(r))
	if err != nil {
		t.Fatal(err)
	}
	client.NewJobWorker("a", noopHandler)
	client.NewStreamJobWorker("b", noopHandler)
	if got := r.Float64(); got != seed42[0] {
		t.Fatalf("next draw = %v, want the first of the sequence %v: a worker consumed a draw", got, seed42[0])
	}
}
