package camunda_test

import (
	"errors"
	"fmt"
	"testing"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	"github.com/camunda/orchestration-cluster-api-go/internal/backpressure"
)

// TestBackpressureQueueFullMapsToPublicSentinel guards issue #6: a request
// rejected because the adaptive-backpressure waiter queue is full must be
// detectable with errors.Is(err, camunda.ErrBackpressureQueueFull).
//
// The backpressure gate rejects a saturated queue with backpressure.ErrQueueFull,
// which transport.BackpressureTransport.RoundTrip returns verbatim (it cannot
// import the root package to remap it). The public sentinel must therefore be the
// same value, or the documented errors.Is check is always false. Forcing a real
// queue-full through the Manager needs maxWaiters (1000) parked goroutines, which
// is racy in a unit test; this asserts the error identity that makes the mapping
// hold on every code path (facade, Raw client, and job workers).
func TestBackpressureQueueFullMapsToPublicSentinel(t *testing.T) {
	// The exact value the transport returns must match the public sentinel.
	if !errors.Is(backpressure.ErrQueueFull, camunda.ErrBackpressureQueueFull) {
		t.Fatal("backpressure.ErrQueueFull must match camunda.ErrBackpressureQueueFull via errors.Is")
	}

	// It must still match after being wrapped the way http.Client.Do wraps a
	// RoundTrip error (a *url.Error using %w), which is how it reaches callers
	// through the generated client.
	wrapped := fmt.Errorf(`Post "https://cluster/v2/process-instances": %w`, backpressure.ErrQueueFull)
	if !errors.Is(wrapped, camunda.ErrBackpressureQueueFull) {
		t.Fatal("a wrapped queue-full error must still match camunda.ErrBackpressureQueueFull")
	}
}
