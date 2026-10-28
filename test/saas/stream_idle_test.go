//go:build saas

package saas

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/client"
)

// The tests below target the Cloudflare-fronted SaaS gateway, which closes a gRPC
// stream after 60 seconds without HTTP/2 keepalive traffic. The Java client was hit
// by this because unary calls on the shared channel delayed its first keepalive PING
// past that limit. This SDK configures no gRPC keepalive at all (grpc-go leaves it
// off), so an idle job stream sends no PING; these tests check what actually happens
// to such a stream against a real SaaS cluster.
//
// They are long-running (over a minute of deliberate idling each) and are skipped by
// `go test -short`. Select just them with `-run 'TestSaaSStreamIdle'`.

const (
	// defaultIdleWindow comfortably exceeds the 60s limit.
	defaultIdleWindow = 90 * time.Second
	// deliveryWithin is far below the 30s default sidecar-poll interval (which these
	// tests disable anyway), so a delivery inside it is the stream pushing the job.
	deliveryWithin = 10 * time.Second
)

// idleWindow is how long a stream must stay up. CAMUNDA_SAAS_IDLE_SECONDS overrides
// it, e.g. to probe a different limit.
func idleWindow(t *testing.T) time.Duration {
	t.Helper()
	if v := os.Getenv("CAMUNDA_SAAS_IDLE_SECONDS"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs <= 0 {
			t.Fatalf("CAMUNDA_SAAS_IDLE_SECONDS=%q: want a positive integer", v)
		}
		return time.Duration(secs) * time.Second
	}
	return defaultIdleWindow
}

func skipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("long-running idle-stream test; skipped with -short")
	}
}

// TestSaaSStreamIdleSurvivesSixtySeconds opens a stream with no job available, keeps
// it idle past the 60s limit, then creates an instance and requires the job to be
// pushed down the same stream.
func TestSaaSStreamIdleSurvivesSixtySeconds(t *testing.T) {
	skipIfShort(t)
	window := idleWindow(t)
	c, logs := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), window+3*time.Minute)
	defer cancel()

	p := deployGreet(ctx, t, c)
	h := startStreamWorker(ctx, t, c, logs, p)
	h.waitStreamOpen(5 * time.Second)

	// No instance exists, so nothing flows on the stream: any PING must come from
	// the client or the gateway, not from job traffic.
	start := time.Now()
	h.idleFor(window, start)
	t.Logf("stream stayed up through %s of idleness", time.Since(start).Round(time.Second))

	name := "after-idle-" + time.Now().UTC().Format("150405.000000000")
	key := startInstance(ctx, t, c, p, name)
	latency := h.awaitDelivery(name, deliveryWithin)
	t.Logf("job delivered over the idle stream in %s", latency.Round(time.Millisecond))

	// A drop right after the idle window would also be a failure, even if the
	// reconnect happened to catch the job.
	if n := h.reconnects(); n > 0 {
		t.Fatalf("stream reconnected %d time(s) during the test:\n%s", n, logs.String())
	}
	awaitCompleted(ctx, t, c, key)
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
}

// TestSaaSStreamIdleWithInterleavedUnaryCalls repeats the idle test but, every 20s,
// makes unary calls while the stream is open: a REST topology request, plus a new
// instance whose job the worker completes with a unary gRPC CompleteJob on the same
// connection that carries the stream. That is the traffic pattern that delayed the
// Java client's first keepalive.
func TestSaaSStreamIdleWithInterleavedUnaryCalls(t *testing.T) {
	skipIfShort(t)
	window := idleWindow(t)
	c, logs := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), window+3*time.Minute)
	defer cancel()

	p := deployGreet(ctx, t, c)
	h := startStreamWorker(ctx, t, c, logs, p)
	h.waitStreamOpen(5 * time.Second)

	const step = 20 * time.Second
	start := time.Now()
	deadline := start.Add(window)
	var keys []camundaapi.ProcessInstanceKey
	for i := 0; time.Now().Add(step).Before(deadline); i++ {
		h.idleFor(step, start)

		if _, err := c.GetTopology(ctx); err != nil {
			t.Fatalf("GetTopology during idle window: %v", err)
		}
		name := "interleaved-" + strconv.Itoa(i) + "-" + time.Now().UTC().Format("150405.000000000")
		key := startInstance(ctx, t, c, p, name)
		keys = append(keys, key)
		latency := h.awaitDelivery(name, deliveryWithin)
		t.Logf("%s in: job %q delivered over the stream in %s", time.Since(start).Round(time.Second), name, latency.Round(time.Millisecond))
	}
	// Idle out whatever remains of the window after the last unary call.
	if rest := time.Until(deadline); rest > 0 {
		h.idleFor(rest, start)
	}
	t.Logf("stream stayed up through %s with %d interleaved job round-trips", time.Since(start).Round(time.Second), len(keys))

	// One more delivery after the full window, as in the plain idle test.
	name := "after-interleaved-" + time.Now().UTC().Format("150405.000000000")
	keys = append(keys, startInstance(ctx, t, c, p, name))
	h.awaitDelivery(name, deliveryWithin)

	if n := h.reconnects(); n > 0 {
		t.Fatalf("stream reconnected %d time(s) during the test:\n%s", n, logs.String())
	}
	for _, key := range keys {
		awaitCompleted(ctx, t, c, key)
	}
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
}
