package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/examples/advanced/internal/exampleutil"
)

const processID = "go-example-parcel-delivery"

//go:embed parcel-delivery.bpmn
var processModel []byte

type parcel struct {
	ID               string `json:"parcelId"`
	Address          string `json:"address"`
	InvalidAddress   bool   `json:"invalidAddress"`
	FailFirstAttempt bool   `json:"failFirstAttempt"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gRPC stream worker example:", err)
		os.Exit(1)
	}
}

func run() error {
	signalCtx, stopSignals := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()

	ctx, cancel := context.WithTimeout(signalCtx, 2*time.Minute)
	defer cancel()

	client, err := exampleutil.NewClient(oauthOptionsFromEnvironment()...)
	if err != nil {
		return err
	}
	if err := exampleutil.Deploy(ctx, client, "parcel-delivery.bpmn", processModel); err != nil {
		return err
	}

	var (
		mu       sync.Mutex
		attempts = map[string]int{}
	)
	worker := client.NewStreamJobWorker("dispatch-parcel",
		func(_ context.Context, job *camunda.Job) (map[string]any, error) {
			var input parcel
			if err := job.Variables(&input); err != nil {
				return nil, fmt.Errorf("decode parcel job %s: %w", job.Key(), err)
			}

			mu.Lock()
			attempts[input.ID]++
			attempt := attempts[input.ID]
			mu.Unlock()

			switch {
			case input.InvalidAddress:
				return nil, &camunda.BpmnError{
					Code:    "ADDRESS_REJECTED",
					Message: fmt.Sprintf("carrier rejected address %q", input.Address),
					Variables: map[string]any{
						"deliveryStatus": "manual-review",
					},
				}
			case input.FailFirstAttempt && attempt == 1:
				return nil, fmt.Errorf("carrier API unavailable for parcel %s", input.ID)
			default:
				return map[string]any{
					"deliveryStatus": "dispatched",
					"trackingId":     "track-" + input.ID,
				}, nil
			}
		},
		camunda.WithStreamWorkerName("parcel-dispatch"),
		camunda.WithStreamMaxConcurrentJobs(8),
		camunda.WithStreamFetchVariables(
			"parcelId",
			"address",
			"invalidAddress",
			"failFirstAttempt",
		),
		// The simulated carrier call is immediate, so 15 seconds leaves ample
		// acknowledgement headroom without making a re-queued demo job wait long.
		camunda.WithStreamJobTimeout(15*time.Second),
		camunda.WithStreamReconnectBackoff(time.Second),
		// Keep the REST safety net enabled for jobs re-queued while the stream
		// reconnects. This short demo interval makes that recovery path observable.
		camunda.WithStreamPollInterval(10*time.Second),
		camunda.WithStreamPollMaxJobs(8),
	)

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- worker.Run(workerCtx)
	}()

	runID := time.Now().UTC().Format("20060102T150405.000000000")
	parcels := []parcel{
		{ID: "parcel-1001-" + runID, Address: "42 Market Street, Berlin"},
		{
			ID:             "parcel-1002-" + runID,
			Address:        "unknown",
			InvalidAddress: true,
		},
		{
			ID:               "parcel-1003-" + runID,
			Address:          "8 River Road, London",
			FailFirstAttempt: true,
		},
	}

	keys := make([]openapi.ProcessInstanceKey, 0, len(parcels))
	for _, item := range parcels {
		key, err := exampleutil.StartProcess(ctx, client, processID, item.ID, map[string]any{
			"parcelId":         item.ID,
			"address":          item.Address,
			"invalidAddress":   item.InvalidAddress,
			"failFirstAttempt": item.FailFirstAttempt,
		})
		if err != nil {
			return stopAndWait(stopWorker, workerDone, err)
		}
		keys = append(keys, key)
	}

	for i, key := range keys {
		if _, err := exampleutil.WaitForCompletion(ctx, client, key); err != nil {
			return stopAndWait(stopWorker, workerDone, err)
		}
		mu.Lock()
		attemptCount := attempts[parcels[i].ID]
		mu.Unlock()
		fmt.Printf("%s completed after %d streamed worker attempt(s)\n",
			parcels[i].ID, attemptCount)
	}

	return stopAndWait(stopWorker, workerDone, nil)
}

func oauthOptionsFromEnvironment() []camunda.Option {
	clientID := os.Getenv("CAMUNDA_CLIENT_ID")
	clientSecret := os.Getenv("CAMUNDA_CLIENT_SECRET")
	tokenURL := os.Getenv("CAMUNDA_OAUTH_URL")
	if clientID == "" && clientSecret == "" && tokenURL == "" {
		return nil
	}

	return []camunda.Option{
		camunda.WithOAuth(clientID, clientSecret, tokenURL),
		camunda.WithOAuthAudience(os.Getenv("CAMUNDA_TOKEN_AUDIENCE")),
		camunda.WithOAuthScope(os.Getenv("CAMUNDA_TOKEN_SCOPE")),
	}
}

func stopAndWait(stop context.CancelFunc, done <-chan error, runErr error) error {
	stop()
	workerErr := <-done
	if workerErr != nil && !errors.Is(workerErr, context.Canceled) {
		if runErr != nil {
			return errors.Join(runErr, fmt.Errorf("worker stopped: %w", workerErr))
		}
		return fmt.Errorf("worker stopped: %w", workerErr)
	}
	return runErr
}
