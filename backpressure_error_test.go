package camunda_test

import (
	"errors"
	"net/url"
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
	// The public sentinel must be the *same value* as the internal error the
	// transport returns — not merely errors.Is-compatible — so the mapping can't
	// silently regress to a distinct value with a custom Is(). Compare identity.
	if backpressure.ErrQueueFull != camunda.ErrBackpressureQueueFull {
		t.Fatal("camunda.ErrBackpressureQueueFull must be the same value as backpressure.ErrQueueFull")
	}

	// It must still match after being wrapped as a *url.Error, which is how
	// http.Client.Do surfaces a RoundTrip error through the generated client.
	wrapped := &url.Error{Op: "Post", URL: "https://cluster/v2/process-instances", Err: backpressure.ErrQueueFull}
	if !errors.Is(wrapped, camunda.ErrBackpressureQueueFull) {
		t.Fatal("a wrapped queue-full error must still match camunda.ErrBackpressureQueueFull")
	}
}
