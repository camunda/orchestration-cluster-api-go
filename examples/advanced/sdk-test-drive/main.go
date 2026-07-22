package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/examples/advanced/internal/exampleutil"
)

const (
	processIDPlaceholder = "go-sdk-test-drive"
	jobTypePlaceholder   = "sdk-test-drive-greet"
)

//go:embed test-drive.bpmn
var processModel []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "SDK test drive:", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
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

	runID := strconv.FormatInt(time.Now().UnixNano(), 10)
	processID := processIDPlaceholder + "-" + runID
	jobType := jobTypePlaceholder + "-" + runID
	model := strings.ReplaceAll(string(processModel), processIDPlaceholder, processID)
	model = strings.ReplaceAll(model, jobTypePlaceholder, jobType)
	if err := exampleutil.Deploy(ctx, client, "test-drive-"+runID+".bpmn", []byte(model)); err != nil {
		return err
	}

	key, err := exampleutil.StartProcess(ctx, client, processID, "test-drive-"+runID,
		map[string]any{"name": "Camunda"})
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := client.CancelProcessInstance(
			cleanupCtx, key, *openapi.NewCancelProcessInstanceRequest(),
		); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("cancel incomplete process instance %s: %w", key, err))
		}
	}()

	type handlerResult struct {
		greeting string
		err      error
	}
	handled := make(chan handlerResult, 1)
	publishResult := func(result handlerResult) {
		select {
		case handled <- result:
		default:
		}
	}
	worker := client.NewJobWorker(jobType,
		func(_ context.Context, job *camunda.Job) (map[string]any, error) {
			var input struct {
				Name string `json:"name"`
			}
			if err := job.Variables(&input); err != nil {
				err = fmt.Errorf("decode job variables: %w", err)
				publishResult(handlerResult{err: err})
				return nil, err
			}
			if input.Name != "Camunda" {
				err := fmt.Errorf("job name = %q, want Camunda", input.Name)
				publishResult(handlerResult{err: err})
				return nil, err
			}
			greeting := "Hello, " + input.Name + "!"
			publishResult(handlerResult{greeting: greeting})
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
	defer stopWorker()
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()

	select {
	case result := <-handled:
		if result.err != nil {
			stopWorker()
			<-workerDone
			return result.err
		}
		if result.greeting != "Hello, Camunda!" {
			stopWorker()
			<-workerDone
			return fmt.Errorf("worker greeting = %q, want %q", result.greeting, "Hello, Camunda!")
		}
		fmt.Println("worker returned:", result.greeting)
	case workerErr := <-workerDone:
		if workerErr == nil {
			return errors.New("worker exited before handling a job")
		}
		return fmt.Errorf("worker exited before handling a job: %w", workerErr)
	case <-ctx.Done():
		stopWorker()
		<-workerDone
		return fmt.Errorf("wait for worker: %w", ctx.Err())
	}

	instance, err := exampleutil.WaitForCompletion(ctx, client, key)
	stopWorker()
	workerErr := <-workerDone
	if errors.Is(workerErr, context.Canceled) {
		workerErr = nil
	}
	if err != nil || workerErr != nil {
		return errors.Join(err, wrapWorkerError(workerErr))
	}

	completed = true
	fmt.Printf("process instance %s reached %s; SDK test drive passed\n",
		key, instance.GetState())
	return nil
}

func wrapWorkerError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("worker stopped: %w", err)
}
