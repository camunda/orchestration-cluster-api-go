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
	"net"
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

	// NonIdempotent marks requests (job commands: completion, failure, error)
	// that must not be replayed when their outcome is unknown. Such a request is
	// retried only when the server says it was not applied (429, 503) or when the
	// connection failed before the request was sent. A 502, 504, other status, or
	// a transport error after the request was sent is returned after one attempt.
	// If nil, every request follows the default policy.
	NonIdempotent func(*http.Request) bool
}

// IsNotAppliedStatus reports whether a status code means the server did not
// apply the command (backpressure or unavailable), so replaying it is safe even
// for a non-idempotent command.
func IsNotAppliedStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
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

// IsPreSendError reports whether err proves the request never reached the
// server: a DNS failure, a failed dial, or an error marked pre-send
// (MarkPreSend, or any error with a PreSend() bool method, e.g. an OAuth token-fetch failure or a failed TLS handshake). Any other transport error (including
// one from a custom RoundTripper) may have happened after the request was sent,
// so a non-idempotent request must not be retried on it.
func IsPreSendError(err error) bool {
	var marked preSendMarker
	if errors.As(err, &marked) && marked.PreSend() {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
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

	strict := t.NonIdempotent != nil && t.NonIdempotent(req)

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
			if !isRetryableErr(err) || (strict && !IsPreSendError(err)) || attempt == max-1 {
				return nil, err
			}
			continue
		}

<<<<<<< HEAD
		if !IsRetryableStatus(resp.StatusCode) || attempt == max-1 {
=======
		retryable := IsRetryableStatus(resp.StatusCode)
		if strict {
			retryable = IsNotAppliedStatus(resp.StatusCode)
		}
		if !retryable || isTerminalGatewayTimeout(resp) || attempt == max-1 {
>>>>>>> 3377ac5 (fix: retry job commands only when the server did not apply them (#247))
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
