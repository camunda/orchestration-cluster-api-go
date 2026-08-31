// Package transport assembles the SDK's http.RoundTripper chain and provides the
// backpressure-observing transport. The chain, from outermost to innermost, is:
//
//	backpressure -> retry -> auth -> base
//
// so a single backpressure permit covers a whole logical request (including its
// retries), auth is re-applied on every retry attempt, and the manager observes
// the final response to update its severity.
package transport

import (
	"bytes"
	"io"
	"net/http"

	"github.com/camunda/orchestration-cluster-api-go/internal/auth"
	"github.com/camunda/orchestration-cluster-api-go/internal/backpressure"
	"github.com/camunda/orchestration-cluster-api-go/internal/retry"
)

// Options configures the RoundTripper chain.
type Options struct {
	// Base is the innermost RoundTripper. If nil, http.DefaultTransport is used.
	Base http.RoundTripper
	// Auth applies authentication. If nil, no auth layer is inserted.
	Auth *auth.Transport
	// Retry is the transient-error retry policy.
	Retry retry.Config

	// Backpressure is the adaptive backpressure manager. If nil, no gating layer
	// is inserted.
	Backpressure *backpressure.Manager
	// Exempt marks requests (e.g. job-completion drains) that bypass the
	// backpressure gate. If nil, all requests are gated.
	Exempt func(*http.Request) bool
}

// New builds the RoundTripper chain described in the package doc.
// New builds the RoundTripper chain. clock is positional so a caller cannot silently
// omit it and leave retry backoff on real time.
func New(o Options, clock retry.Clock) http.RoundTripper {
	inner := o.Base
	if inner == nil {
		inner = http.DefaultTransport
	}
	if o.Auth != nil {
		o.Auth.Base = inner
		inner = o.Auth
	}
	inner = &retry.Transport{Base: inner, Cfg: o.Retry, Clock: clock}
	if o.Backpressure != nil {
		inner = &BackpressureTransport{Base: inner, Mgr: o.Backpressure, Exempt: o.Exempt}
	}
	return inner
}

// BackpressureTransport gates initiating requests through a backpressure.Manager
// and observes responses to update the manager's severity.
type BackpressureTransport struct {
	Base   http.RoundTripper
	Mgr    *backpressure.Manager
	Exempt func(*http.Request) bool
}

func (t *BackpressureTransport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

// RoundTrip implements http.RoundTripper.
func (t *BackpressureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	gated := t.Exempt == nil || !t.Exempt(req)
	if gated {
		if err := t.Mgr.Acquire(req.Context()); err != nil {
			return nil, err
		}
		defer t.Mgr.Release()
	}

	resp, err := t.base().RoundTrip(req)
	if err != nil {
		// A transport-level error is not itself a backpressure signal.
		return nil, err
	}

	t.observe(resp)
	return resp, nil
}

// observe classifies the response and updates the manager. It buffers the body
// only for the 5xx statuses that require inspecting it for RESOURCE_EXHAUSTED,
// restoring it so the caller still sees a readable body.
func (t *BackpressureTransport) observe(resp *http.Response) {
	status := resp.StatusCode
	body := ""
	if status == 500 || status == 503 {
		body = drainAndRestore(resp)
	}
	switch {
	case backpressure.IsBackpressureResponse(status, body):
		t.Mgr.RecordBackpressure()
	case status < 500:
		// Server had capacity to serve (or reject) the request.
		t.Mgr.RecordHealthyHint()
	default:
		// 5xx without a backpressure signal: neither escalate nor recover.
	}
}

// drainAndRestore reads resp.Body (capped) and replaces it with an equivalent
// readable reader, returning the read contents.
func drainAndRestore(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return string(data)
}
