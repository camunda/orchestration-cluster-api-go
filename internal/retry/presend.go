package retry

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptrace"
	"sync"
)

// preSendError marks an error that is known to have happened before the
// request was written to the server (for example, an OAuth token fetch or a
// failed TLS handshake). IsPreSendError recognises it.
type preSendError struct{ err error }

func (e *preSendError) Error() string { return e.err.Error() }
func (e *preSendError) Unwrap() error { return e.err }
func (e *preSendError) PreSend() bool { return true }

// preSendMarker is implemented by errors (in this or other packages, e.g. the
// auth layer's token-fetch error) that know the request was never sent.
type preSendMarker interface{ PreSend() bool }

// MarkPreSend wraps err to record that the request was never sent. A nil err
// stays nil.
func MarkPreSend(err error) error {
	if err == nil {
		return nil
	}
	var p preSendMarker
	if errors.As(err, &p) && p.PreSend() {
		return err
	}
	return &preSendError{err: err}
}

// PreSendTracer is the innermost RoundTripper layer: it observes the TLS
// handshake of the connection dialed for this request and, when the round
// trip fails after a failed handshake and before a connection was obtained,
// marks the error as pre-send. It sits below the auth layer, so the token
// fetch's own connections are not traced.
type PreSendTracer struct {
	Base http.RoundTripper
}

func (t *PreSendTracer) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

// RoundTrip implements http.RoundTripper.
func (t *PreSendTracer) RoundTrip(req *http.Request) (*http.Response, error) {
	var mu sync.Mutex
	var handshakeFailed, gotConn bool
	trace := &httptrace.ClientTrace{
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err != nil {
				mu.Lock()
				handshakeFailed = true
				mu.Unlock()
			}
		},
		GotConn: func(httptrace.GotConnInfo) {
			mu.Lock()
			gotConn = true
			mu.Unlock()
		},
	}
	r := req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := t.base().RoundTrip(r)
	if err != nil {
		mu.Lock()
		preSend := handshakeFailed && !gotConn
		mu.Unlock()
		if preSend {
			return nil, MarkPreSend(err)
		}
	}
	return resp, err
}
