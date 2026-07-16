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
	"os"
	"path/filepath"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

//go:embed testdata/greet.bpmn
var greetBPMN []byte

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
	path := filepath.Join(t.TempDir(), "greet.bpmn")
	if err := os.WriteFile(path, greetBPMN, 0o644); err != nil {
		t.Fatalf("write bpmn: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open bpmn: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, _, err := c.Raw().ResourceAPI.CreateDeployment(ctx).Resources([]*os.File{f}).Execute(); err != nil {
		t.Fatalf("deploy: %v", err)
	}
}

func startGreetProcess(ctx context.Context, t *testing.T, c *camunda.CamundaClient, name string) {
	t.Helper()
	byID := openapi.NewProcessInstanceCreationInstructionById("demo-process")
	byID.SetVariables(map[string]any{"name": name})
	instr := openapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID)
	if _, err := c.CreateProcessInstance(ctx, instr); err != nil {
		t.Fatalf("create process instance: %v", err)
	}
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

	deployGreet(ctx, t, c)

	handled := make(chan string, 1)
	worker := c.NewJobWorker("greet",
		func(_ context.Context, job *camunda.Job) (map[string]any, error) {
			var in struct {
				Name string `json:"name"`
			}
			_ = job.Variables(&in)
			greeting := "Hello, " + in.Name + "!"
			select {
			case handled <- greeting:
			default:
			}
			return map[string]any{"greeting": greeting}, nil
		},
		camunda.WithRequestTimeout(5*time.Second),
		camunda.WithPollInterval(200*time.Millisecond),
	)

	wctx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	go func() { _ = worker.Run(wctx); close(done) }()

	startGreetProcess(ctx, t, c, "REST")

	select {
	case greeting := <-handled:
		if greeting != "Hello, REST!" {
			t.Errorf("greeting = %q, want %q", greeting, "Hello, REST!")
		}
	case <-ctx.Done():
		t.Fatal("REST worker did not handle the job in time")
	}
	stop()
	<-done
}

func TestStreamWorkerEndToEnd(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	deployGreet(ctx, t, c)

	handled := make(chan string, 1)
	worker := c.NewStreamJobWorker("greet",
		func(_ context.Context, job *camunda.Job) (map[string]any, error) {
			var in struct {
				Name string `json:"name"`
			}
			_ = job.Variables(&in)
			greeting := "Hello, " + in.Name + "!"
			select {
			case handled <- greeting:
			default:
			}
			return map[string]any{"greeting": greeting}, nil
		},
		// Disable the sidecar poll so this test exercises the gRPC stream path only.
		camunda.WithStreamPollInterval(-1),
	)

	wctx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	go func() { _ = worker.Run(wctx); close(done) }()

	// Give the stream a moment to register before creating the instance.
	time.Sleep(time.Second)
	startGreetProcess(ctx, t, c, "gRPC")

	select {
	case greeting := <-handled:
		if greeting != "Hello, gRPC!" {
			t.Errorf("greeting = %q, want %q", greeting, "Hello, gRPC!")
		}
	case <-ctx.Done():
		t.Fatal("gRPC stream worker did not handle the job in time")
	}
	stop()
	<-done
}
