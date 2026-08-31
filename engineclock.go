package camunda

import (
	"context"
	"fmt"
	"sync"
	"time"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

// ClockController is the engine-side clock an [EngineClock] drives.
//
// Implemented by [CamundaClient] in terms of PUT /clock and POST /clock/reset. It is
// an interface so the pin semantics can be tested without a running engine.
type ClockController interface {
	// PinAt moves the engine clock to an absolute instant.
	PinAt(ctx context.Context, t time.Time) error
	// ResetToLive returns the engine clock to real time.
	ResetToLive(ctx context.Context) error
}

// PinAt moves the engine clock to t.
func (c *CamundaClient) PinAt(ctx context.Context, t time.Time) error {
	return c.PinClock(ctx, *openapi.NewClockPinRequest(t.UnixMilli()))
}

// ResetToLive returns the engine clock to real time.
func (c *CamundaClient) ResetToLive(ctx context.Context) error {
	return c.ResetClock(ctx)
}

// EngineClock is a clock bound to the engine's own clock.
//
// A wait does not pass time locally: it moves the *engine* forward and reports the new
// instant. Process instances, timers and the SDK therefore agree on what time it is,
// which a purely local test clock cannot achieve.
//
// A wait resolves against an instant read before the engine is contacted, so waits
// that overlap -- those that read the clock before any of them lands -- settle at a
// single instant instead of summing. A wait that begins after an earlier one has
// landed reads the new time and composes from it, which is the intended behaviour: it
// really did start later.
//
// Clock pinning is an alpha engine endpoint intended for tests, not production
// clusters. Pass the client the pin requests should travel on; it keeps real time, so
// the requests themselves are unaffected by the pinning.
type EngineClock struct {
	engine ClockController
	live   LiveClock

	// gate serialises pin round-trips so concurrent waits collapse into one request.
	gate sync.Mutex

	mu     sync.Mutex
	pinned bool
	at     time.Time
}

// NewEngineClock binds to an engine. The clock starts unpinned, following real time
// until the first wait or [EngineClock.PinTo].
//
// engine must not be a client using this clock: a client captures its clock when it is
// built, so the one passed here always predates this clock.
func NewEngineClock(engine ClockController) *EngineClock {
	return &EngineClock{engine: engine}
}

// Now reports the pinned instant, or live time when unpinned.
func (c *EngineClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pinned {
		return c.at
	}
	return c.live.Now()
}

// IsPinned reports whether this clock currently holds the engine clock pinned.
func (c *EngineClock) IsPinned() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pinned
}

// PinTo moves the engine clock to an absolute instant, and reports that instant from
// [EngineClock.Now] once the engine accepts it -- including when t is in the past,
// since the SDK's reading has to match the engine's.
//
// A no-op when the clock already sits at or past t, which is what makes overlapping
// waits settle at a single instant. The local reading is published only after the
// engine accepts the pin, so a failed request leaves the clock untouched.
//
// Waits never move the clock backwards: [EngineClock.Sleep] derives its instant by
// adding to the current reading.
func (c *EngineClock) PinTo(ctx context.Context, t time.Time) error {
	c.gate.Lock()
	defer c.gate.Unlock()

	c.mu.Lock()
	pinned, at := c.pinned, c.at
	c.mu.Unlock()
	if pinned && !t.After(at) {
		return nil
	}

	if err := c.engine.PinAt(ctx, t); err != nil {
		return err
	}

	// Published only after the engine accepts, and reported exactly: the SDK and the
	// engine have to agree on the time, so a pin to an earlier instant moves this
	// clock back with it rather than being clamped forward.
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = t
	c.pinned = true
	return nil
}

// Reset returns the engine to real time. Readings follow live time again afterwards,
// rather than freezing at the last pinned instant.
func (c *EngineClock) Reset(ctx context.Context) error {
	c.gate.Lock()
	defer c.gate.Unlock()
	if err := c.engine.ResetToLive(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pinned = false
	c.at = time.Time{}
	return nil
}

// Sleep advances the engine clock by d rather than waiting for it to pass.
func (c *EngineClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	// Fix the wake instant before contacting the engine. Deriving it from the pinned
	// value at request time instead would make overlapping waits sum.
	return c.PinTo(ctx, c.Now().Add(d))
}

// After advances the engine clock by d and returns a channel already carrying the new
// instant. It cannot report an error, so a failed pin is surfaced on the channel as
// the unadvanced reading; prefer Sleep, which reports it.
func (c *EngineClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	if err := c.Sleep(context.Background(), d); err != nil {
		// Nothing to return the error on; make the failure visible rather than
		// silently reporting a time the engine never moved to.
		panic(fmt.Sprintf("EngineClock.After: could not advance the engine clock by %s: %v", d, err))
	}
	ch <- c.Now()
	return ch
}
