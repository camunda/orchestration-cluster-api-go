// Package retry provides a transient-error HTTP retry layer implemented as an
// http.RoundTripper. Requests that fail with a retryable signal (HTTP
// 429/502/503/504, or a network-level error; a 504 that reports a probe as over is
// not) are retried up to Config.MaxAttempts
// times with exponentially increasing, fully-jittered delays. Non-retryable
// responses and errors are returned immediately.
package retry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

	// Clock resolves the backoff wait. Required; transport.New always sets it.
	Clock Clock

	// Random draws the jitter. Required; transport.New always sets it.
	Random Random
}

// Random is the part of the SDK random source this package needs, declared here
// for the same reason as Clock.
type Random interface {
	Float64() float64
}

// Clock is the part of the SDK clock this package needs. Declared here rather than
// imported so retry stays a leaf package (see architecture_test.go); the injected
// clock satisfies it structurally.
type Clock interface {
	Sleep(ctx context.Context, d time.Duration) error
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t *Transport) doSleep(ctx context.Context, d time.Duration) error {
	return t.Clock.Sleep(ctx, d)
}

// terminalGatewayTimeouts are the titles of a 504 that report the end of a probe rather
// than a transient failure: a worker collected it and did not answer by its deadline, or
// it still waited for the values of its secrets then. Retrying would create the probe
// again, and run the worker again, only to fail the same way after another full timeout.
// A 504 titled DEADLINE_EXCEEDED (the gateway timed out, for example after a leader
// change) stays retryable.
var terminalGatewayTimeouts = map[string]bool{
	"WORKER_DID_NOT_ANSWER":     true,
	"SECRET_RESOLUTION_TIMEOUT": true,
}

// maxProblemPeek bounds how much of a 504 body is read to find its problem title.
const maxProblemPeek = 64 << 10

// isTerminalGatewayTimeout reports whether resp is a 504 whose problem title says the
// operation ended. It reads the body and puts an equivalent reader back, so the caller
// still receives the problem detail. A body that is missing, unreadable or not a problem
// document is not terminal.
func isTerminalGatewayTimeout(resp *http.Response) bool {
	if resp.StatusCode != http.StatusGatewayTimeout || resp.Body == nil {
		return false
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxProblemPeek))
	rest := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(data), rest), rest}
	var problem struct {
		Title string `json:"title"`
	}
	if json.Unmarshal(data, &problem) != nil {
		return false
	}
	return terminalGatewayTimeouts[problem.Title]
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
	return time.Duration(float64(exp) * t.Random.Float64())
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

		if !IsRetryableStatus(resp.StatusCode) || isTerminalGatewayTimeout(resp) || attempt == max-1 {
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
