package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/examples/advanced/internal/exampleutil"
)

const (
	processID           = "go-example-load"
	instanceCount       = 40
	producerConcurrency = 12
	workerConcurrency   = 6
)

//go:embed load.bpmn
var processModel []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "backpressure example:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, err := exampleutil.NewClient()
	if err != nil {
		return err
	}
	if err := exampleutil.Deploy(ctx, client, "load.bpmn", processModel); err != nil {
		return err
	}

	var handled atomic.Int32
	worker := client.NewJobWorker("example-load-work",
		func(ctx context.Context, job *camunda.Job) (map[string]any, error) {
			var input struct {
				OrderID string `json:"orderId"`
			}
			if err := job.Variables(&input); err != nil {
				return nil, fmt.Errorf("decode job %s: %w", job.Key(), err)
			}

			// Simulate a downstream dependency. Worker concurrency is deliberately
			// lower than producer concurrency so work queues in Camunda, not in Go.
			select {
			case <-time.After(75 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			handled.Add(1)
			return map[string]any{"fulfilled": true}, nil
		},
		camunda.WithWorkerName("fulfillment-load-example"),
		camunda.WithMaxConcurrentJobs(workerConcurrency),
		camunda.WithFetchVariables("orderId"),
		camunda.WithJobTimeout(15*time.Second),
		camunda.WithRequestTimeout(5*time.Second),
		camunda.WithPollInterval(100*time.Millisecond),
	)

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()

	keys, err := startBounded(ctx, client)
	if err != nil {
		stopWorker()
		<-workerDone
		return err
	}

	deadline := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	for handled.Load() < instanceCount {
		select {
		case <-ctx.Done():
			stopWorker()
			<-workerDone
			return fmt.Errorf("processed %d/%d jobs: %w", handled.Load(), instanceCount, ctx.Err())
		case <-deadline.C:
		}
	}

	stopWorker()
	if err := <-workerDone; err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("worker stopped: %w", err)
	}

	if _, err := exampleutil.WaitForCompletion(ctx, client, keys[len(keys)-1]); err != nil {
		return err
	}
	fmt.Printf("completed %d instances with producer=%d, worker=%d\n",
		instanceCount, producerConcurrency, workerConcurrency)
	return nil
}

func startBounded(ctx context.Context, client *camunda.CamundaClient) ([]openapi.ProcessInstanceKey, error) {
	// Balanced backpressure adapts after the cluster signals overload. This
	// application-side bound is still important: it prevents an unbounded goroutine
	// and waiter backlog before the first signal reaches the SDK.
	permits := make(chan struct{}, producerConcurrency)
	keys := make([]openapi.ProcessInstanceKey, instanceCount)
	errs := make(chan error, instanceCount)

	var wg sync.WaitGroup
	for i := range instanceCount {
		select {
		case permits <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			defer func() { <-permits }()

			orderID := fmt.Sprintf("load-%03d", index+1)
			key, err := exampleutil.StartProcess(ctx, client, processID, orderID,
				map[string]any{"orderId": orderID})
			if err != nil {
				errs <- err
				return
			}
			keys[index] = key
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		return nil, err
	}
	return keys, nil
}
