package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/examples/advanced/internal/exampleutil"
)

const processID = "go-example-order"

//go:embed order.bpmn
var processModel []byte

type order struct {
	ID          string `json:"orderId"`
	SKU         string `json:"sku"`
	OutOfStock  bool   `json:"outOfStock"`
	FailFirstIO bool   `json:"failFirstIO"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "order worker example:", err)
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
	if err := exampleutil.Deploy(ctx, client, "order.bpmn", processModel); err != nil {
		return err
	}

	var (
		mu       sync.Mutex
		attempts = map[string]int{}
	)
	worker := client.NewJobWorker("reserve-inventory",
		func(_ context.Context, job *camunda.Job) (map[string]any, error) {
			var input order
			if err := job.Variables(&input); err != nil {
				return nil, fmt.Errorf("decode inventory job %s: %w", job.Key(), err)
			}

			mu.Lock()
			attempts[input.ID]++
			attempt := attempts[input.ID]
			mu.Unlock()

			if input.OutOfStock {
				// Inventory shortage is a business outcome, not an infrastructure
				// failure. A BPMN error follows the modeled exception path and does
				// not burn technical retries.
				return nil, &camunda.BpmnError{
					Code:    "OUT_OF_STOCK",
					Message: fmt.Sprintf("%s is unavailable", input.SKU),
					Variables: map[string]any{
						"rejectionReason": "inventory unavailable",
					},
				}
			}
			if input.FailFirstIO && attempt == 1 {
				// Returning an ordinary error fails the job and decrements retries.
				// Real code should wrap the downstream error with operational context.
				return nil, fmt.Errorf("inventory API timeout for order %s", input.ID)
			}
			return map[string]any{
				"reservationId": fmt.Sprintf("res-%s", input.ID),
				"reserved":      true,
			}, nil
		},
		camunda.WithWorkerName("inventory-reservation"),
		camunda.WithMaxConcurrentJobs(4),
		camunda.WithFetchVariables("orderId", "sku", "outOfStock", "failFirstIO"),
		camunda.WithJobTimeout(20*time.Second),
		camunda.WithRequestTimeout(5*time.Second),
		camunda.WithPollInterval(200*time.Millisecond),
	)

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()

	orders := []order{
		{ID: "order-1001", SKU: "coffee-maker"},
		{ID: "order-1002", SKU: "limited-mug", OutOfStock: true},
		{ID: "order-1003", SKU: "coffee-beans", FailFirstIO: true},
	}
	keys := make([]openapi.ProcessInstanceKey, 0, len(orders))
	for _, item := range orders {
		key, err := exampleutil.StartProcess(ctx, client, processID, item.ID, map[string]any{
			"orderId":     item.ID,
			"sku":         item.SKU,
			"outOfStock":  item.OutOfStock,
			"failFirstIO": item.FailFirstIO,
		})
		if err != nil {
			stopWorker()
			<-workerDone
			return err
		}
		keys = append(keys, key)
	}

	for _, key := range keys {
		if _, err := exampleutil.WaitForCompletion(ctx, client, key); err != nil {
			stopWorker()
			<-workerDone
			return err
		}
	}

	stopWorker()
	if err := <-workerDone; err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("worker stopped: %w", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, item := range orders {
		fmt.Printf("%s completed after %d worker attempt(s)\n", item.ID, attempts[item.ID])
	}
	return nil
}
