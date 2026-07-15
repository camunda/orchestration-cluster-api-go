// Package retry provides a transient-error HTTP retry layer implemented as an
// http.RoundTripper. Requests that fail with a retryable signal (HTTP
// 429/502/503/504, or a network-level error) are retried up to Config.MaxAttempts
// times with exponentially increasing, fully-jittered delays. Non-retryable
// responses and errors are returned immediately.
package retry

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"time"
)

// Config is the transient-error retry policy.
type Config struct {
	// MaxAttempts is the maximum number of attempts (1 disables retries).
	MaxAttempts int
	// BaseDelay is the base backoff delay.
	BaseDelay time.Duration
	// MaxDelay caps the backoff delay.
	MaxDelay time.Duration
}

// DefaultConfig returns the SDK's default retry policy.
func DefaultConfig() Config {
	return Config{MaxAttempts: 4, BaseDelay: 100 * time.Millisecond, MaxDelay: 5 * time.Second}
}

// Transport is an http.RoundTripper that retries transient failures.
type Transport struct {
	// Base is the underlying RoundTripper. If nil, http.DefaultTransport is used.
	Base http.RoundTripper
	// Cfg is the retry policy.
	Cfg Config

	// randFloat and sleep are test hooks; nil uses production implementations.
	randFloat func() float64
	sleep     func(ctx context.Context, d time.Duration) error
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t *Transport) rnd() float64 {
	if t.randFloat != nil {
		return t.randFloat()
	}
	return rand.Float64()
}

func (t *Transport) doSleep(ctx context.Context, d time.Duration) error {
	if t.sleep != nil {
		return t.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// IsRetryableStatus reports whether an HTTP status code is a transient failure.
func IsRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, // 429
		http.StatusBadGateway,         // 502
		http.StatusServiceUnavailable, // 503
		http.StatusGatewayTimeout:     // 504
		return true
	default:
		return false
	}
}

// isRetryableErr reports whether a transport-level error is transient. Context
// cancellation/deadline errors are terminal and never retried.
func isRetryableErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// backoff computes the full-jitter delay for a zero-based attempt index:
// random(0, min(maxDelay, base * 2^attempt)).
func (t *Transport) backoff(attempt int) time.Duration {
	shift := attempt
	if shift > 30 {
		shift = 30
	}
	exp := t.Cfg.BaseDelay * (1 << uint(shift))
	if exp <= 0 || exp > t.Cfg.MaxDelay {
		exp = t.Cfg.MaxDelay
	}
	return time.Duration(float64(exp) * t.rnd())
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	max := t.Cfg.MaxAttempts
	if max < 1 {
		max = 1
	}
	ctx := req.Context()

	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt < max; attempt++ {
		if attempt > 0 {
			// Rewind the body for replay. If we can't, we must not retry.
			if req.Body != nil {
				if req.GetBody == nil {
					break
				}
				body, err := req.GetBody()
				if err != nil {
					break
				}
				req.Body = body
			}
			if err := t.doSleep(ctx, t.backoff(attempt-1)); err != nil {
				return nil, err
			}
		}

		resp, err := t.base().RoundTrip(req)
		if err != nil {
			lastErr, lastResp = err, nil
			if !isRetryableErr(err) || attempt == max-1 {
				return nil, err
			}
			continue
		}

		if !IsRetryableStatus(resp.StatusCode) || attempt == max-1 {
			return resp, nil
		}

		// Retryable status and attempts remain: drain+close so the connection
		// can be reused, then retry.
		lastResp, lastErr = resp, nil
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return lastResp, nil
}
