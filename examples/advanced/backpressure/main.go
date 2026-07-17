package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/examples/advanced/internal/exampleutil"
)

type counters struct {
	succeeded    atomic.Uint64
	backpressure atomic.Uint64
	failed       atomic.Uint64
}

const requiredProtectedCompletions = 100

//go:embed payment-intake.bpmn
var paymentIntakeModel []byte

func main() {
	var (
		duration = flag.Duration("duration", 30*time.Second, "maximum stress-test duration")
		flooders = flag.Int("flooders", 512, "concurrent unprotected pressure generators")
		clients  = flag.Int("clients", 64, "concurrent protected logical requests")
	)
	flag.Parse()

	if err := run(*duration, *flooders, *clients); err != nil {
		fmt.Fprintln(os.Stderr, "backpressure stress test:", err)
		os.Exit(1)
	}
}

func run(duration time.Duration, flooders, protectedClients int) error {
	if duration <= 0 || flooders <= 0 || protectedClients <= 0 {
		return errors.New("duration, flooders, and clients must be positive")
	}

	// The pressure client intentionally opts out of adaptive gating and HTTP
	// retries. It represents noisy neighbors competing with this application and
	// exists only to force a real cluster backpressure condition.
	pressureClient, err := exampleutil.NewClient(
		camunda.WithBackpressureProfile(camunda.ProfileLegacy),
		camunda.WithRetry(camunda.RetryConfig{
			MaxAttempts: 1,
			BaseDelay:   time.Millisecond,
			MaxDelay:    time.Millisecond,
		}),
		camunda.WithLogLevel(camunda.LogOff),
	)
	if err != nil {
		return err
	}

	// Production traffic uses BALANCED. The SDK gates concurrency after a final
	// backpressure response and retries transient responses with full jitter.
	protectedClient, err := exampleutil.NewClient(
		camunda.WithBackpressureProfile(camunda.ProfileBalanced),
		camunda.WithRetry(camunda.RetryConfig{
			MaxAttempts: 3,
			BaseDelay:   25 * time.Millisecond,
			MaxDelay:    500 * time.Millisecond,
		}),
		camunda.WithLogLevel(camunda.LogOff),
	)
	if err != nil {
		return err
	}

	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSetup()
	if err := exampleutil.Deploy(
		setupCtx,
		protectedClient,
		"payment-intake.bpmn",
		paymentIntakeModel,
	); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var pressure, protected counters
	var wg sync.WaitGroup
	start := time.Now()
	runID := start.UTC().Format("20060102T150405.000000000")

	for source := range flooders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			flood(ctx, pressureClient, source, &pressure)
		}()
	}
	for worker := range protectedClients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			protect(ctx, protectedClient, runID, worker, &protected)
		}()
	}

	reportDone := make(chan struct{})
	go report(ctx, start, &pressure, &protected, reportDone)

	// Stop early once the cluster has signaled real backpressure and the protected
	// client has subsequently completed enough useful work to prove recovery.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var (
		backpressureObserved bool
		protectedAtSignal    uint64
	)
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			<-reportDone
			return result(start, &pressure, &protected, backpressureObserved, protectedAtSignal)
		case <-ticker.C:
			if !backpressureObserved &&
				pressure.backpressure.Load()+protected.backpressure.Load() > 0 {
				backpressureObserved = true
				protectedAtSignal = protected.succeeded.Load()
			}
			if backpressureObserved &&
				protected.succeeded.Load() >= protectedAtSignal+requiredProtectedCompletions {
				cancel()
				wg.Wait()
				<-reportDone
				return result(start, &pressure, &protected, true, protectedAtSignal)
			}
		}
	}
}

func flood(
	ctx context.Context,
	client *camunda.CamundaClient,
	source int,
	stats *counters,
) {
	request := *openapi.NewSignalBroadcastRequest("inventory-level-changed")
	for sequence := 0; ctx.Err() == nil; sequence++ {
		// Simulate a runaway warehouse feed broadcasting high-cardinality stock
		// updates during a Black Friday sale.
		request.SetVariables(map[string]any{
			"sku":            fmt.Sprintf("SKU-%04d", sequence%10_000),
			"warehouse":      fmt.Sprintf("warehouse-%02d", source%32),
			"availableUnits": sequence % 250,
			"feedSequence":   sequence,
		})
		_, err := client.BroadcastSignal(ctx, request)
		record(err, stats)
	}
}

func protect(
	ctx context.Context,
	client *camunda.CamundaClient,
	runID string,
	worker int,
	stats *counters,
) {
	for sequence := 0; ctx.Err() == nil; sequence++ {
		orderID := fmt.Sprintf("order-%s-%d-%d", runID, worker, sequence)
		paymentID := fmt.Sprintf("payment-%s-%d-%d", runID, worker, sequence)
		messageID := "payment-provider-event-" + paymentID
		request := openapi.NewMessagePublicationRequest("payment-received")
		request.SetMessageId(messageID)
		request.SetBusinessId(orderID)
		request.SetTimeToLive((30 * time.Second).Milliseconds())
		request.SetVariables(map[string]any{
			"orderId":     orderID,
			"paymentId":   paymentID,
			"amountCents": 12_900,
			"currency":    "EUR",
			"provider":    "example-pay",
			"status":      "captured",
		})

		for attempt := 0; ctx.Err() == nil; attempt++ {
			_, err := client.PublishMessage(ctx, *request)
			if err == nil || isDuplicate(err) {
				stats.succeeded.Add(1)
				break
			}
			if !isBackpressure(err) {
				if !isCancellation(err) {
					stats.failed.Add(1)
				}
				break
			}

			stats.backpressure.Add(1)
			// Reusing the message ID makes application-level retry safe if every
			// transport attempt was rejected. Jitter prevents synchronized retries.
			delay := min(10*time.Millisecond<<min(attempt, 6), time.Second)
			timer := time.NewTimer(time.Duration(rand.Int63n(int64(delay) + 1)))
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}

func record(err error, stats *counters) {
	switch {
	case err == nil:
		stats.succeeded.Add(1)
	case isBackpressure(err):
		stats.backpressure.Add(1)
	case !isCancellation(err):
		stats.failed.Add(1)
	}
}

func isBackpressure(err error) bool {
	status, ok := camunda.StatusCode(err)
	if ok && (status == 429 || status == 503) {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "RESOURCE_EXHAUSTED")
}

func isDuplicate(err error) bool {
	status, ok := camunda.StatusCode(err)
	return ok && status == 409
}

func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func report(
	ctx context.Context,
	start time.Time,
	pressure *counters,
	protected *counters,
	done chan<- struct{},
) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			printStats(time.Since(start), pressure, protected)
			return
		case <-ticker.C:
			printStats(time.Since(start), pressure, protected)
		}
	}
}

func printStats(elapsed time.Duration, pressure, protected *counters) {
	fmt.Printf(
		"%5.1fs pressure: ok=%d bp=%d err=%d | protected: ok=%d bp=%d err=%d\n",
		elapsed.Seconds(),
		pressure.succeeded.Load(),
		pressure.backpressure.Load(),
		pressure.failed.Load(),
		protected.succeeded.Load(),
		protected.backpressure.Load(),
		protected.failed.Load(),
	)
}

func result(
	start time.Time,
	pressure, protected *counters,
	backpressureObserved bool,
	protectedAtSignal uint64,
) error {
	elapsed := time.Since(start)
	backpressure := pressure.backpressure.Load() + protected.backpressure.Load()
	if backpressure == 0 {
		failures := pressure.failed.Load() + protected.failed.Load()
		if failures > 0 &&
			pressure.succeeded.Load()+protected.succeeded.Load() == 0 {
			return fmt.Errorf(
				"all %d requests failed without a cluster response; check address, credentials, and cluster health",
				failures,
			)
		}
		return fmt.Errorf(
			"cluster emitted no backpressure in %s; increase -flooders or -duration",
			elapsed.Round(time.Millisecond),
		)
	}
	completedAfterSignal := uint64(0)
	if backpressureObserved && protected.succeeded.Load() > protectedAtSignal {
		completedAfterSignal = protected.succeeded.Load() - protectedAtSignal
	}
	if completedAfterSignal < requiredProtectedCompletions {
		return fmt.Errorf(
			"cluster emitted backpressure but protected traffic completed only %d/%d requests afterward",
			completedAfterSignal,
			requiredProtectedCompletions,
		)
	}
	fmt.Printf(
		"observed %d real backpressure responses; protected traffic completed %d requests afterward\n",
		backpressure,
		completedAfterSignal,
	)
	return nil
}
