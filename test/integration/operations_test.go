//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go"
	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

// errNotYetIndexed signals that an eventually-consistent search has not yet
// surfaced its expected results, so Poll should retry.
var errNotYetIndexed = errors.New("not yet indexed")

// deployGreetResult deploys the greet process and returns its process definition key.
func deployGreetResult(ctx context.Context, t *testing.T, c *camunda.CamundaClient) openapi.ProcessDefinitionKey {
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
	dep, _, err := c.Raw().ResourceAPI.CreateDeployment(ctx).Resources([]*os.File{f}).Execute()
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	for _, d := range dep.GetDeployments() {
		proc := d.GetProcessDefinition()
		if key := proc.GetProcessDefinitionKey(); string(key) != "" {
			return openapi.ProcessDefinitionKey(string(key))
		}
	}
	t.Fatal("deployment response contained no process definition")
	return ""
}

func TestEvaluateExpressionEndToEnd(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The FEEL `=` prefix marks the value as an expression to evaluate (rather
	// than a literal string).
	req := openapi.NewExpressionEvaluationRequest("=2 + 3")
	result, err := c.EvaluateExpression(ctx, *req)
	if err != nil {
		t.Fatalf("EvaluateExpression: %v", err)
	}
	// FEEL numbers decode as JSON numbers (float64).
	if got := result.GetResult(); got != float64(5) {
		t.Errorf("2 + 3 = %v (%T), want 5", got, got)
	}
}

func TestPublishMessageEndToEnd(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := openapi.NewMessagePublicationRequest("integration-msg")
	req.SetCorrelationKey("integration-key")
	if _, err := c.PublishMessage(ctx, *req); err != nil {
		t.Fatalf("PublishMessage: %v", err)
	}
}

func TestBroadcastSignalEndToEnd(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := c.BroadcastSignal(ctx, *openapi.NewSignalBroadcastRequest("integration-signal")); err != nil {
		t.Fatalf("BroadcastSignal: %v", err)
	}
}

func TestDeployAndReadProcessDefinition(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	key := deployGreetResult(ctx, t, c)

	// Reads are eventually consistent: poll until the definition is queryable.
	def, err := camunda.Poll(ctx, func(ctx context.Context) (*openapi.ProcessDefinitionResult, error) {
		return c.GetProcessDefinition(ctx, key)
	}, camunda.WithPollTimeout(30*time.Second))
	if err != nil {
		t.Fatalf("GetProcessDefinition: %v", err)
	}
	if def.GetProcessDefinitionId() != "demo-process" {
		t.Errorf("processDefinitionId = %q, want demo-process", def.GetProcessDefinitionId())
	}

	xml, err := c.GetProcessDefinitionXML(ctx, key)
	if err != nil {
		t.Fatalf("GetProcessDefinitionXML: %v", err)
	}
	if !strings.Contains(xml, "demo-process") {
		t.Error("process definition XML does not contain the process id")
	}
}

func TestCreateAndReadProcessInstance(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	deployGreet(ctx, t, c)

	byID := openapi.NewProcessInstanceCreationInstructionById("demo-process")
	byID.SetVariables(map[string]any{"name": "reader"})
	created, err := c.CreateProcessInstance(ctx,
		openapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID))
	if err != nil {
		t.Fatalf("CreateProcessInstance: %v", err)
	}
	key := openapi.MustProcessInstanceKey(string(created.GetProcessInstanceKey()))

	// The instance is visible in secondary storage only after export; poll for it.
	instance, err := camunda.Poll(ctx, func(ctx context.Context) (*openapi.ProcessInstanceResult, error) {
		return c.GetProcessInstance(ctx, key)
	}, camunda.WithPollTimeout(30*time.Second))
	if err != nil {
		t.Fatalf("GetProcessInstance: %v", err)
	}
	if string(instance.GetProcessInstanceKey()) != key.String() {
		t.Errorf("processInstanceKey = %q, want %q", string(instance.GetProcessInstanceKey()), key.String())
	}
	if instance.GetProcessDefinitionId() != "demo-process" {
		t.Errorf("processDefinitionId = %q, want demo-process", instance.GetProcessDefinitionId())
	}
}

func TestSearchProcessInstancesEndToEnd(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	deployGreet(ctx, t, c)
	key := startGreetProcess(ctx, t, c, "search")
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := c.CancelProcessInstance(cleanupCtx, key, *openapi.NewCancelProcessInstanceRequest()); err != nil {
			t.Errorf("cancel search process instance %s: %v", key, err)
		}
	}()

	// Search hits secondary storage; poll until at least one instance is indexed.
	result, err := camunda.Poll(ctx, func(ctx context.Context) (*openapi.ProcessInstanceSearchQueryResult, error) {
		res, err := c.SearchProcessInstances(ctx, *openapi.NewProcessInstanceSearchQuery())
		if err != nil {
			return nil, err
		}
		if len(res.GetItems()) == 0 {
			return nil, errNotYetIndexed
		}
		return res, nil
	}, camunda.WithPollTimeout(30*time.Second), camunda.WithRetryOn(func(err error) bool { return err == errNotYetIndexed }))
	if err != nil {
		t.Fatalf("SearchProcessInstances: %v", err)
	}
	if len(result.GetItems()) == 0 {
		t.Error("expected at least one process instance")
	}
}

func TestBpmnErrorCompletesModeledExceptionPath(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runID := strconv.FormatInt(time.Now().UnixNano(), 10)
	processID := "integration-bpmn-error-" + runID
	jobType := "integration-error-task-" + runID
	model := strings.ReplaceAll(string(bpmnErrorModel), "integration-bpmn-error", processID)
	model = strings.ReplaceAll(model, "integration-error-task", jobType)
	deployModel(ctx, t, c, "bpmn-error.bpmn", []byte(model))

	worker := c.NewJobWorker(jobType,
		func(context.Context, *camunda.Job) (map[string]any, error) {
			return nil, &camunda.BpmnError{
				Code:      "BUSINESS_ERROR",
				Message:   "expected modeled outcome",
				Variables: map[string]any{"errorHandled": true},
			}
		},
		camunda.WithRequestTimeout(5*time.Second),
		camunda.WithPollInterval(200*time.Millisecond),
	)
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()

	byID := openapi.NewProcessInstanceCreationInstructionById(processID)
	created, err := c.CreateProcessInstance(ctx,
		openapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(byID))
	if err != nil {
		stopWorker()
		<-workerDone
		t.Fatalf("CreateProcessInstance: %v", err)
	}

	key := openapi.ProcessInstanceKey(string(created.GetProcessInstanceKey()))
	_, err = camunda.Poll(ctx, func(ctx context.Context) (*openapi.ProcessInstanceResult, error) {
		instance, err := c.GetProcessInstance(ctx, key)
		if err != nil {
			return nil, err
		}
		if instance.GetState() != openapi.PROCESSINSTANCESTATEENUM_COMPLETED {
			return nil, errNotYetIndexed
		}
		return instance, nil
	},
		camunda.WithPollTimeout(30*time.Second),
		camunda.WithRetryOn(func(err error) bool {
			return err == errNotYetIndexed || camunda.IsNotFound(err)
		}),
	)
	stopWorker()
	<-workerDone
	if err != nil {
		t.Fatalf("modeled BPMN error path did not complete: %v", err)
	}

	filter := openapi.NewElementInstanceFilter()
	filter.SetProcessInstanceKey(openapi.ModelString(key))
	query := openapi.NewElementInstanceSearchQuery()
	query.SetFilter(*filter)
	_, err = camunda.Poll(ctx, func(ctx context.Context) (*openapi.ElementInstanceSearchQueryResult, error) {
		result, err := c.SearchElementInstances(ctx, *query)
		if err != nil {
			return nil, err
		}
		for _, item := range result.GetItems() {
			if item.GetElementId() == "business-error-caught" {
				return result, nil
			}
		}
		return nil, errNotYetIndexed
	},
		camunda.WithPollTimeout(30*time.Second),
		camunda.WithRetryOn(func(err error) bool {
			return err == errNotYetIndexed
		}),
	)
	if err != nil {
		t.Fatalf("modeled BPMN boundary event was not observed: %v", err)
	}
}
