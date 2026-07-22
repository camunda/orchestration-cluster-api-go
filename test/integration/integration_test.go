//go:build integration

// Package integration holds end-to-end tests that require a live Camunda 8
// Orchestration Cluster. They are excluded from the normal unit build by the
// `integration` build tag; run them with:
//
//	go test -tags integration ./test/integration/...
//
// The cluster address is taken from CAMUNDA_REST_ADDRESS / CAMUNDA_GRPC_ADDRESS
// (defaulting to a local stack). Authentication is disabled, matching the
// sdk-infra docker compose stack used in CI.
package integration

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

//go:embed testdata/greet.bpmn
var greetBPMN []byte

//go:embed testdata/bpmn-error.bpmn
var bpmnErrorModel []byte

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func newClient(t *testing.T) *camunda.CamundaClient {
	t.Helper()
	c, err := camunda.New(
		camunda.WithRestAddress(envOr("CAMUNDA_REST_ADDRESS", "http://localhost:8080")),
		camunda.WithGrpcAddress(envOr("CAMUNDA_GRPC_ADDRESS", "localhost:26500")),
		camunda.WithNoAuth(),
		camunda.WithLogLevel(camunda.LogError),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// deployGreet deploys the greet process (start -> "greet" service task -> end).
func deployGreet(ctx context.Context, t *testing.T, c *camunda.CamundaClient) {
	t.Helper()
	deployModel(ctx, t, c, "greet.bpmn", greetBPMN)
}

func deployModel(ctx context.Context, t *testing.T, c *camunda.CamundaClient, name string, model []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, model, 0o644); err != nil {
		t.Fatalf("write bpmn: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open bpmn: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, _, err := c.Raw().ResourceAPI.CreateDeployment(ctx).Resources([]*os.File{f}).Execute(); err != nil {
		t.Fatalf("deploy %s: %v", name, err)
	}
}

func startGreetProcess(ctx context.Context, t *testing.T, c *camunda.CamundaClient, name string) openapi.ProcessInstanceKey {
	t.Helper()
	return startProcess(ctx, t, c, "demo-process", name)
}

func startProcess(ctx context.Context, t *testing.T, c *camunda.CamundaClient, processID, name string) openapi.ProcessInstanceKey {
	t.Helper()
	byID := openapi.NewProcessInstanceCreationInstructionById(processID)
	byID.SetVariables(map[string]any{"name": name})
	instr := openapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID)
	result, err := c.CreateProcessInstance(ctx, instr)
	if err != nil {
		t.Fatalf("create process instance: %v", err)
	}
	return openapi.MustProcessInstanceKey(string(result.GetProcessInstanceKey()))
}

func TestTopology(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	topo, err := c.GetTopology(ctx)
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if len(topo.GetBrokers()) == 0 {
		t.Error("expected at least one broker in the topology")
	}
}

func TestRESTWorkerEndToEnd(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runID := time.Now().UTC().Format("20060102T150405.000000000")
	processID := "integration-rest-worker-" + runID
	jobType := "greet-rest-" + runID
	model := strings.ReplaceAll(string(greetBPMN), "demo-process", processID)
	model = strings.ReplaceAll(model, `type="greet"`, `type="`+jobType+`"`)
	deployModel(ctx, t, c, "rest-worker.bpmn", []byte(model))

	expectedName := "REST-" + runID
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
	worker := c.NewJobWorker(jobType,
		func(_ context.Context, job *camunda.Job) (map[string]any, error) {
			var in struct {
				Name string `json:"name"`
			}
			if err := job.Variables(&in); err != nil {
				err = fmt.Errorf("decode REST job variables: %w", err)
				publishResult(handlerResult{err: err})
				return nil, err
			}
			greeting := "Hello, " + in.Name + "!"
			if in.Name == expectedName {
				publishResult(handlerResult{greeting: greeting})
			}
			return map[string]any{"greeting": greeting}, nil
		},
		camunda.WithRequestTimeout(5*time.Second),
		camunda.WithPollInterval(200*time.Millisecond),
	)

	wctx, stop := context.WithCancel(ctx)
	defer stop()
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(wctx) }()

	_ = startProcess(ctx, t, c, processID, expectedName)

	select {
	case result := <-handled:
		if result.err != nil {
			t.Fatalf("REST worker handler: %v", result.err)
		}
		expectedGreeting := "Hello, " + expectedName + "!"
		if result.greeting != expectedGreeting {
			t.Errorf("greeting = %q, want %q", result.greeting, expectedGreeting)
		}
	case workerErr := <-workerDone:
		t.Fatalf("REST worker exited before handling its job: %v", workerErr)
	case <-ctx.Done():
		workerErr := stopIntegrationWorker(stop, workerDone)
		if workerErr != nil {
			t.Fatalf("REST worker did not handle the job in time: %v; %v", ctx.Err(), workerErr)
		}
		t.Fatalf("REST worker did not handle the job in time: %v", ctx.Err())
	}
	if err := stopIntegrationWorker(stop, workerDone); err != nil {
		t.Fatal(err)
	}
}

func TestStreamWorkerEndToEnd(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runID := time.Now().UTC().Format("20060102T150405.000000000")
	processID := "integration-grpc-worker-" + runID
	jobType := "greet-grpc-" + runID
	model := strings.ReplaceAll(string(greetBPMN), "demo-process", processID)
	model = strings.ReplaceAll(model, `type="greet"`, `type="`+jobType+`"`)
	deployModel(ctx, t, c, "grpc-worker.bpmn", []byte(model))

	expectedName := "gRPC-" + runID
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
	worker := c.NewStreamJobWorker(jobType,
		func(_ context.Context, job *camunda.Job) (map[string]any, error) {
			var in struct {
				Name string `json:"name"`
			}
			if err := job.Variables(&in); err != nil {
				err = fmt.Errorf("decode gRPC stream job variables: %w", err)
				publishResult(handlerResult{err: err})
				return nil, err
			}
			greeting := "Hello, " + in.Name + "!"
			if in.Name == expectedName {
				publishResult(handlerResult{greeting: greeting})
			}
			return map[string]any{"greeting": greeting}, nil
		},
		// Disable the sidecar poll so this test exercises the gRPC stream path only.
		camunda.WithStreamPollInterval(-1),
	)

	wctx, stop := context.WithCancel(ctx)
	defer stop()
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(wctx) }()

	// Give the stream a moment to register before creating the instance.
	select {
	case workerErr := <-workerDone:
		t.Fatalf("gRPC stream worker exited during startup: %v", workerErr)
	case <-time.After(time.Second):
	}
	_ = startProcess(ctx, t, c, processID, expectedName)

	select {
	case result := <-handled:
		if result.err != nil {
			t.Fatalf("gRPC stream worker handler: %v", result.err)
		}
		expectedGreeting := "Hello, " + expectedName + "!"
		if result.greeting != expectedGreeting {
			t.Errorf("greeting = %q, want %q", result.greeting, expectedGreeting)
		}
	case workerErr := <-workerDone:
		t.Fatalf("gRPC stream worker exited before handling its job: %v", workerErr)
	case <-ctx.Done():
		workerErr := stopIntegrationWorker(stop, workerDone)
		if workerErr != nil {
			t.Fatalf("gRPC stream worker did not handle the job in time: %v; %v", ctx.Err(), workerErr)
		}
		t.Fatalf("gRPC stream worker did not handle the job in time: %v", ctx.Err())
	}
	if err := stopIntegrationWorker(stop, workerDone); err != nil {
		t.Fatal(err)
	}
}
