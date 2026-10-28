//go:build saas

// Package saas holds end-to-end tests that run against a Camunda 8 SaaS cluster.
// They are excluded from the normal build by the `saas` build tag; run them with:
//
//	go test -tags saas ./test/saas/...
//
// Every test skips cleanly unless the SaaS connection is configured, so the tag
// alone never fails a developer machine. The connection is read from the SDK's own
// environment variables:
//
//	CAMUNDA_REST_ADDRESS    https://<region>.zeebe.camunda.io/<cluster-id>
//	CAMUNDA_GRPC_ADDRESS    <cluster-id>.<region>.zeebe.camunda.io:443
//	CAMUNDA_CLIENT_ID       OAuth client id of a cluster API client
//	CAMUNDA_CLIENT_SECRET   OAuth client secret of that client
//
// CAMUNDA_OAUTH_URL (default https://login.cloud.camunda.io/oauth/token) and
// CAMUNDA_TOKEN_AUDIENCE (default zeebe.camunda.io) are optional. The long-running
// idle-stream tests also skip under `go test -short`; see stream_idle_test.go.
//
// This is deliberately separate from test/integration, whose client is hard-wired
// to an unauthenticated local stack.
package saas

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/client"
)

//go:embed testdata/greet.bpmn
var greetBPMN []byte

// requiredEnv names the variables without which there is no SaaS cluster to talk to.
var requiredEnv = []string{
	"CAMUNDA_REST_ADDRESS",
	"CAMUNDA_GRPC_ADDRESS",
	"CAMUNDA_CLIENT_ID",
	"CAMUNDA_CLIENT_SECRET",
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// logCapture records what the SDK logger writes to stderr. The logger is internal
// and writes to os.Stderr as it was when the client was built, so swapping os.Stderr
// around camunda.New is the only way to observe stream reconnects from outside the
// module without touching production code.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// captureStderr redirects os.Stderr into a logCapture (still echoing to the real
// stderr so CI logs keep the SDK output) until the test ends. Tests that use it must
// not run in parallel.
func captureStderr(t *testing.T) *logCapture {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	lc := &logCapture{}
	os.Stderr = w
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.MultiWriter(lc, orig), r)
	}()
	t.Cleanup(func() {
		os.Stderr = orig
		_ = w.Close()
		<-done
		_ = r.Close()
	})
	return lc
}

// newClient builds an OAuth client from the environment, skipping the test when the
// SaaS connection is not configured. It returns the capture of the client's log.
func newClient(t *testing.T) (*camunda.CamundaClient, *logCapture) {
	t.Helper()
	for _, k := range requiredEnv {
		if os.Getenv(k) == "" {
			t.Skipf("SaaS connection not configured: %s is unset", k)
		}
	}
	logs := captureStderr(t)
	c, err := camunda.New(
		camunda.WithOAuth(
			os.Getenv("CAMUNDA_CLIENT_ID"),
			os.Getenv("CAMUNDA_CLIENT_SECRET"),
			envOr("CAMUNDA_OAUTH_URL", "https://login.cloud.camunda.io/oauth/token"),
		),
		camunda.WithOAuthAudience(envOr("CAMUNDA_TOKEN_AUDIENCE", "zeebe.camunda.io")),
		// Warn is the level the stream worker logs "job stream ended; reconnecting" at.
		camunda.WithLogLevel(camunda.LogWarn),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, logs
}

// process identifies a freshly deployed copy of the greet process.
type process struct {
	id      string
	jobType string
}

// deployGreet deploys the greet process (start -> service task -> end) under a
// unique process id and job type, so concurrent runs against a shared cluster never
// pick up each other's jobs.
func deployGreet(ctx context.Context, t *testing.T, c *camunda.CamundaClient) process {
	t.Helper()
	runID := time.Now().UTC().Format("20060102T150405.000000000")
	p := process{id: "saas-it-" + runID, jobType: "saas-it-greet-" + runID}
	model := strings.ReplaceAll(string(greetBPMN), "demo-process", p.id)
	model = strings.ReplaceAll(model, `type="greet"`, `type="`+p.jobType+`"`)

	path := filepath.Join(t.TempDir(), "greet.bpmn")
	if err := os.WriteFile(path, []byte(model), 0o644); err != nil {
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
	return p
}

// startInstance creates an instance of p carrying the given name as a variable.
func startInstance(ctx context.Context, t *testing.T, c *camunda.CamundaClient, p process, name string) camundaapi.ProcessInstanceKey {
	t.Helper()
	byID := camundaapi.NewProcessInstanceCreationInstructionById(camundaapi.ProcessDefinitionId(p.id))
	byID.SetVariables(map[string]any{"name": name})
	instr := camundaapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID)
	result, err := c.CreateProcessInstance(ctx, instr)
	if err != nil {
		t.Fatalf("create process instance: %v", err)
	}
	return camundaapi.MustProcessInstanceKey(string(result.GetProcessInstanceKey()))
}

func TestSaaSTopology(t *testing.T) {
	c, _ := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A successful call proves the OAuth exchange and TLS path to the REST gateway.
	topo, err := c.GetTopology(ctx)
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if len(topo.GetBrokers()) == 0 {
		t.Error("expected at least one broker in the topology")
	}
}

func TestSaaSDeployAndCreateInstance(t *testing.T) {
	c, _ := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	p := deployGreet(ctx, t, c)
	key := startInstance(ctx, t, c, p, "deploy-and-create")

	// Reads are eventually consistent: poll until the instance is queryable.
	got, err := camunda.Poll(ctx, func(ctx context.Context) (*camundaapi.ProcessInstanceResult, error) {
		return c.GetProcessInstance(ctx, key)
	}, camunda.WithPollTimeout(60*time.Second))
	if err != nil {
		t.Fatalf("GetProcessInstance: %v", err)
	}
	if got.GetProcessDefinitionId() != camundaapi.ProcessDefinitionId(p.id) {
		t.Errorf("processDefinitionId = %q, want %q", got.GetProcessDefinitionId(), p.id)
	}
}
