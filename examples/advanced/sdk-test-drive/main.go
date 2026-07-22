package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/examples/advanced/internal/exampleutil"
)

const processID = "go-sdk-test-drive"

//go:embed test-drive.bpmn
var processModel []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "SDK test drive:", err)
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

	topology, err := client.GetTopology(ctx)
	if err != nil {
		return fmt.Errorf("read topology: %w", err)
	}
	fmt.Printf("connected to Camunda %s with %d broker(s)\n",
		topology.GetGatewayVersion(), len(topology.GetBrokers()))

	evaluation, err := client.EvaluateExpression(ctx, *openapi.NewExpressionEvaluationRequest("=21 * 2"))
	if err != nil {
		return fmt.Errorf("evaluate FEEL expression: %w", err)
	}
	if evaluation.GetResult() != float64(42) {
		return fmt.Errorf("unexpected FEEL result: %v", evaluation.GetResult())
	}

	if err := exampleutil.Deploy(ctx, client, "test-drive.bpmn", processModel); err != nil {
		return err
	}

	handled := make(chan string, 1)
	worker := client.NewJobWorker("sdk-test-drive-greet",
		func(_ context.Context, job *camunda.Job) (map[string]any, error) {
			var input struct {
				Name string `json:"name"`
			}
			if err := job.Variables(&input); err != nil {
				return nil, fmt.Errorf("decode job variables: %w", err)
			}
			greeting := "Hello, " + input.Name + "!"
			select {
			case handled <- greeting:
			default:
			}
			return map[string]any{"greeting": greeting}, nil
		},
		camunda.WithWorkerName("go-sdk-test-drive"),
		camunda.WithMaxConcurrentJobs(2),
		camunda.WithFetchVariables("name"),
		camunda.WithJobTimeout(15*time.Second),
		camunda.WithRequestTimeout(5*time.Second),
		camunda.WithPollInterval(200*time.Millisecond),
	)

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()

	runID := time.Now().UTC().Format("20060102T150405.000000000")
	key, err := exampleutil.StartProcess(ctx, client, processID, "test-drive-"+runID,
		map[string]any{"name": "Camunda"})
	if err != nil {
		stopWorker()
		<-workerDone
		return err
	}

	select {
	case greeting := <-handled:
		fmt.Println("worker returned:", greeting)
	case <-ctx.Done():
		stopWorker()
		<-workerDone
		return fmt.Errorf("wait for worker: %w", ctx.Err())
	}

	instance, err := exampleutil.WaitForCompletion(ctx, client, key)
	stopWorker()
	workerErr := <-workerDone
	if err != nil {
		return err
	}
	if workerErr != nil && !errors.Is(workerErr, context.Canceled) {
		return fmt.Errorf("worker stopped: %w", workerErr)
	}

	fmt.Printf("process instance %s reached %s; SDK test drive passed\n",
		key, instance.GetState())
	return nil
}
