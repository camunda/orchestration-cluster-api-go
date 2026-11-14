// Package camunda is the ergonomic Go SDK for the Camunda 8 Orchestration Cluster
// REST API (with gRPC job streaming). It wraps a generated low-level REST client
// with cross-cutting concerns — configuration, authentication, adaptive
// backpressure, transient retry, and eventual-consistency handling — exposed
// through a single CamundaClient facade.
package camunda

import (
	"errors"
	"fmt"

	"github.com/camunda/orchestration-cluster-api-go/v11/internal/backpressure"
)

// Sentinel errors. Use errors.Is to test for them.
var (
	// ErrConfig indicates configuration was invalid or incomplete.
	ErrConfig = errors.New("camunda: configuration error")
	// ErrAuth indicates a failure obtaining or refreshing an auth token.
	ErrAuth = errors.New("camunda: authentication error")
	// ErrBackpressureQueueFull indicates the client-side backpressure controller
	// rejected the request because its waiter queue is at capacity. It is the same
	// value the backpressure gate returns, so errors.Is matches it on any request
	// rejected for this reason (facade, Raw client, or job workers).
	ErrBackpressureQueueFull = backpressure.ErrQueueFull
	// ErrEventualConsistencyTimeout indicates an eventual-consistency polling
	// helper timed out before its predicate was met.
	ErrEventualConsistencyTimeout = errors.New("camunda: eventual consistency timeout")
	// ErrHandlerPanic indicates a JobHandler panicked. The worker recovers the
	// panic and fails the job with it, rather than letting the panic terminate
	// the host process — in Go an unrecovered panic on any goroutine takes the
	// whole program down, and a job handler runs on its own goroutine.
	//
	// The job is failed exactly as it would be for a returned error, so its
	// retries decrement and the engine schedules a retry.
	//
	// The sentinel is consumed inside the worker: it is not returned from
	// JobWorker.Run or handed to any caller, so application code cannot match it
	// with errors.Is. A recovered panic is observable through the SDK log (the
	// panic value and stack, at error level) and through the failed job and its
	// incident on the cluster. The sentinel is what the worker's own failure
	// path matches on to tell a panic from an ordinary handler error.
	//
	// The panic value and its stack are written to the SDK log at error level;
	// they are deliberately kept out of the error message sent to the cluster,
	// which is stored on the incident and visible far more widely. See the
	// "Panicking handlers" section of the README.
	ErrHandlerPanic = errors.New("camunda: job handler panicked")
	// ErrLeaseNotHonored indicates a worker activated jobs with a lease but the
	// server returned a job carrying no lease token.
	//
	// The specification declares the token present exactly when the activation sets
	// the lease flag (see presentwhen.go). A server that predates job leases, or one
	// that ignores the flag, breaks that quietly: the worker would go on to complete,
	// fail, or throw an error for the job with no token, so the engine could not fence
	// the command against a superseded activation. The caller asked for fencing and
	// would not be getting it, which is worth failing over rather than logging.
	ErrLeaseNotHonored = errors.New("camunda: activation requested a job lease but the server returned no lease token")
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
