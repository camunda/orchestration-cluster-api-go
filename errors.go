// Package camunda is the ergonomic Go SDK for the Camunda 8 Orchestration Cluster
// REST API (with gRPC job streaming). It wraps a generated low-level REST client
// with cross-cutting concerns — configuration, authentication, adaptive
// backpressure, transient retry, and eventual-consistency handling — exposed
// through a single CamundaClient facade.
package camunda

import (
	"errors"
	"fmt"
)

// Sentinel errors. Use errors.Is to test for them.
var (
	// ErrConfig indicates configuration was invalid or incomplete.
	ErrConfig = errors.New("camunda: configuration error")
	// ErrAuth indicates a failure obtaining or refreshing an auth token.
	ErrAuth = errors.New("camunda: authentication error")
	// ErrBackpressureQueueFull indicates the client-side backpressure controller
	// rejected the request because its waiter queue is at capacity.
	ErrBackpressureQueueFull = errors.New("camunda: backpressure waiter queue full")
	// ErrEventualConsistencyTimeout indicates an eventual-consistency polling
	// helper timed out before its predicate was met.
	ErrEventualConsistencyTimeout = errors.New("camunda: eventual consistency timeout")
)

// APIError is returned when the server responds with a non-success HTTP status.
// It carries the status code and the (often RFC 7807 problem-detail) response body.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Body is the raw response body, if any.
	Body string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("camunda: API error: HTTP %d", e.Status)
	}
	return fmt.Sprintf("camunda: API error: HTTP %d — %s", e.Status, e.Body)
}

// StatusCode returns the HTTP status code carried by err if it is (or wraps) an
// *APIError, and ok reports whether it was found.
func StatusCode(err error) (status int, ok bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status, true
	}
	return 0, false
}

// ConfigError wraps msg as a configuration error (matches errors.Is(err, ErrConfig)).
func configErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrConfig}, args...)...)
}

// authErrorf wraps msg as an authentication error (matches errors.Is(err, ErrAuth)).
func authErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrAuth}, args...)...)
}
