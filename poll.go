package camunda

import (
	"context"
	"fmt"
	"time"
)

// Default eventual-consistency polling parameters. The timeout mirrors the
// CAMUNDA_SDK_EVENTUAL_POLL_DEFAULT_MS config default; pass WithPollTimeout to
// use a client's resolved value (client.Config().EventualPollDefault).
const (
	defaultPollTimeout  = 5 * time.Second
	defaultPollInterval = 250 * time.Millisecond
)

type pollConfig struct {
	timeout  time.Duration
	interval time.Duration
	retry    func(error) bool
}

// PollOption customizes Poll.
type PollOption func(*pollConfig)

// WithPollTimeout sets the overall polling deadline.
func WithPollTimeout(d time.Duration) PollOption {
	return func(c *pollConfig) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithPollRetryInterval sets the delay between polling attempts.
func WithPollRetryInterval(d time.Duration) PollOption {
	return func(c *pollConfig) {
		if d > 0 {
			c.interval = d
		}
	}
}

// WithRetryOn overrides the predicate that decides whether an error is
// retryable (the entity is not yet consistent). The default retries on 404.
func WithRetryOn(pred func(error) bool) PollOption {
	return func(c *pollConfig) {
		if pred != nil {
			c.retry = pred
		}
	}
}

// IsNotFound reports whether err is (or wraps) an *APIError with HTTP 404.
func IsNotFound(err error) bool {
	status, ok := StatusCode(err)
	return ok && status == 404
}

// Poll repeatedly calls fn until it succeeds, the retry predicate returns false,
// the timeout elapses, or ctx is cancelled. It is intended for
// eventually-consistent reads: newly created or modified entities may not be
// immediately visible in the cluster's secondary storage, surfacing as a 404.
//
// By default Poll retries while fn returns a 404 and gives up after the timeout
// with ErrEventualConsistencyTimeout (wrapping the last error). A non-retryable
// error is returned immediately.
//
// Example:
//
//	pi, err := camunda.Poll(ctx, func(ctx context.Context) (*openapi.ProcessInstanceResult, error) {
//	    return client.GetProcessInstance(ctx, key)
//	})
func Poll[T any](ctx context.Context, fn func(context.Context) (T, error), opts ...PollOption) (T, error) {
	cfg := pollConfig{timeout: defaultPollTimeout, interval: defaultPollInterval, retry: IsNotFound}
	for _, o := range opts {
		o(&cfg)
	}

	var zero T
	deadline := time.Now().Add(cfg.timeout)
	for {
		res, err := fn(ctx)
		if err == nil {
			return res, nil
		}
		if !cfg.retry(err) {
			return zero, err
		}
		if !time.Now().Before(deadline) {
			return zero, fmt.Errorf("%w after %s: %v", ErrEventualConsistencyTimeout, cfg.timeout, err)
		}
		if serr := sleepCtx(ctx, cfg.interval); serr != nil {
			return zero, serr
		}
	}
}
