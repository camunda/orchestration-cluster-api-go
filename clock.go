package camunda

import (
	"context"
	"time"
)

// Clock is time and waiting, as an injectable dependency.
//
// It exists so that runtime cadence can be resolved through a clock a test controls
// rather than by calling the time package directly. Inject one with [WithClock]; the
// default is [LiveClock].
//
// Runtime call sites are being migrated onto it (see camunda/orchestration-cluster-api-go#40);
// until that lands, an injected clock is stored and reachable via CamundaClient.Clock
// but does not yet drive retry, backpressure, worker or consistency cadence.
//
// Implementations must be safe for concurrent use.
type Clock interface {
	// Now reports the current time.
	Now() time.Time

	// Sleep waits for d, or until ctx is canceled, in which case it returns
	// ctx.Err(). A non-positive d returns immediately.
	Sleep(ctx context.Context, d time.Duration) error

	// After returns a channel that receives once d has elapsed. Use it where a wait
	// has to be selected against other channels; prefer Sleep otherwise, because it
	// reports cancellation.
	After(d time.Duration) <-chan time.Time
}

// LiveClock is real time, backed by the time package. It is the clock used when none
// is injected.
type LiveClock struct{}

// Now reports the current system time.
func (LiveClock) Now() time.Time { return time.Now() }

// Sleep waits for d or until ctx is canceled.
func (LiveClock) Sleep(ctx context.Context, d time.Duration) error {
	// Report an already-canceled context rather than the wait succeeding, so a
	// canceled caller never sees a wait it did not get.
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// After returns a channel that receives once d has elapsed.
func (LiveClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
