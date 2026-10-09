package camunda

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"sync/atomic"
)

// SeedEnvVar names the environment variable [SeededRandomFromEnv] reads. A test
// that fails under a seeded source can be replayed by setting it to the seed the
// failure reported.
const SeedEnvVar = "CAMUNDA_TEST_SEED"

// Random is the source of randomness the SDK draws jitter from: retry backoff,
// worker startup delay and FALCON endpoint selection. Inject one with
// [WithRandom]; the default is [LiveRandom].
//
// Implementations must be safe for concurrent use, since retries and workers
// draw from one source on many goroutines.
type Random interface {
	// Float64 returns a uniformly distributed value in [0, 1).
	Float64() float64
}

// LiveRandom draws from the process-wide generator. It is the default when no
// [Random] is injected.
type LiveRandom struct{}

// Float64 implements [Random].
func (LiveRandom) Float64() float64 { return rand.Float64() } //nolint:forbidigo // the adapter onto ambient randomness

// SeededRandom is a deterministic [Random]. Two sources with the same seed
// produce the same sequence, so a test can assert the exact delays the SDK
// schedules and replay a failure. It is safe for concurrent use, though
// concurrent callers then share one sequence in nondeterministic order.
//
// The generator is SplitMix64, taking the top 53 bits of each output. It is
// specified across the Camunda SDKs, so one seed yields the same sequence in
// every language.
type SeededRandom struct {
	seed  uint64
	state atomic.Uint64
}

// NewSeededRandom returns a source that replays the sequence for seed.
func NewSeededRandom(seed uint64) *SeededRandom {
	r := &SeededRandom{seed: seed}
	r.state.Store(seed)
	return r
}

// SeededRandomFromEnv seeds from CAMUNDA_TEST_SEED when it is set, otherwise
// from a fresh random seed. Either way the source reports its seed through
// [SeededRandom.Seed] and [SeededRandom.String], so a failing test can be
// replayed.
//
// Only an unset variable means absent. Any other value that is not a decimal
// integer in the uint64 range, including an empty string, returns an error
// wrapping [ErrConfig] rather than silently running with a different seed.
func SeededRandomFromEnv() (*SeededRandom, error) {
	raw, ok := os.LookupEnv(SeedEnvVar)
	if !ok {
		return NewSeededRandom(uint64(LiveRandom{}.Float64() * (1 << 53))), nil
	}
	// Base 10 (not 0) is what keeps the grammar to ASCII [0-9]+: base 0 would also
	// take 0x prefixes and digit-separating underscores.
	seed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return nil, configErrorf("%s must be a decimal integer in the uint64 range, got %q", SeedEnvVar, raw)
	}
	return NewSeededRandom(seed), nil
}

// Seed returns the seed this source was created with.
func (r *SeededRandom) Seed() uint64 { return r.seed }

// Float64 implements [Random].
func (r *SeededRandom) Float64() float64 {
	z := r.state.Add(0x9E3779B97F4A7C15)
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	return float64(z>>11) * 0x1p-53
}

// String names the seed and how to replay it, so a test that prints the source
// on failure says how to reproduce the run.
func (r *SeededRandom) String() string {
	return fmt.Sprintf("SeededRandom(seed=%d; replay with %s=%d)", r.seed, SeedEnvVar, r.seed)
}
