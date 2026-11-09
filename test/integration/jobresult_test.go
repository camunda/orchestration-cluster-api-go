//go:build integration

package integration

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go/v11"
	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
)

// Job results against a live cluster: user task listener corrections (including
// clearing the due and follow-up dates), a listener denying a completion, and an
// ad-hoc sub-process driven by a job worker. The cluster may be shared with other
// test runs, so every process, element and job type carries a per-test suffix and
// every query is scoped to the test's own process instance.

//go:embed testdata/user-task-listener.bpmn
var userTaskListenerBPMN string

//go:embed testdata/ad-hoc-job-worker.bpmn
var adHocJobWorkerBPMN string

// The schedule user-task-listener.bpmn sets on the task.
var (
	modeledDueDate      = time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	modeledFollowUpDate = time.Date(2029, 12, 1, 10, 0, 0, 0, time.UTC)
)

func uniqueSuffix(t *testing.T) string {
	t.Helper()
	return "go-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// runWorker runs a worker for the rest of the test and checks it stopped cleanly.
func runWorker(t *testing.T, ctx context.Context, run func(context.Context) error) {
	t.Helper()
	wctx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- run(wctx) }()
	t.Cleanup(func() {
		if err := stopIntegrationWorker(stop, done); err != nil {
			t.Error(err)
		}
	})
}

func retryNotYet(err error) bool {
	return errors.Is(err, errNotYetIndexed) || camunda.IsNotFound(err)
}

// awaitUserTask polls the user task search, scoped to one process instance, until
// ready accepts the task.
func awaitUserTask(ctx context.Context, t *testing.T, c *camunda.CamundaClient, pik camundaapi.ProcessInstanceKey,
	ready func(*camundaapi.UserTaskResult) bool,
) *camundaapi.UserTaskResult {
	t.Helper()
	key := pik
	filter := camundaapi.NewUserTaskFilter()
	filter.SetProcessInstanceKey(camundaapi.ProcessInstanceKeyExactMatchAsProcessInstanceKeyFilterProperty(&key))
	query := camundaapi.NewUserTaskSearchQuery()
	query.SetFilter(*filter)
	var last []camundaapi.UserTaskResult
	task, err := camunda.Poll(ctx, func(ctx context.Context) (*camundaapi.UserTaskResult, error) {
		result, err := c.SearchUserTasks(ctx, *query)
		if err != nil {
			return nil, err
		}
		last = result.GetItems()
		for i := range last {
			if ready(&last[i]) {
				return &last[i], nil
			}
		}
		return nil, errNotYetIndexed
	}, camunda.WithPollTimeout(45*time.Second), camunda.WithRetryOn(retryNotYet))
	if err != nil {
		t.Fatalf("user task of process instance %s not observed: %v (last seen: %+v)", pik, err, last)
	}
	return task
}

func deployUserTaskListener(ctx context.Context, t *testing.T, c *camunda.CamundaClient, suffix, event, listenerType string) string {
	t.Helper()
	processID := "go-job-result-" + event + "-" + suffix
	model := strings.NewReplacer(
		"PROCESS_ID", processID,
		"TASK_ID", "review_"+suffix,
		"LISTENER_EVENT", event,
		"LISTENER_TYPE", listenerType,
	).Replace(userTaskListenerBPMN)
	deployModel(ctx, t, c, processID+".bpmn", []byte(model))
	return processID
}

// awaitJobStream waits until the gateway has registered a job stream for jobType,
// so a job created afterwards is pushed over it rather than left for polling.
func awaitJobStream(ctx context.Context, t *testing.T, jobType string) {
	t.Helper()
	url := strings.TrimRight(envOr("CAMUNDA_MONITORING_ADDRESS", "http://localhost:9600"), "/") + "/actuator/jobstreams"
	_, err := camunda.Poll(ctx, func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, fmt.Errorf("%w: %v", errNotYetIndexed, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false, err
		}
		if !strings.Contains(string(body), `"`+jobType+`"`) {
			return false, errNotYetIndexed
		}
		return true, nil
	}, camunda.WithPollTimeout(30*time.Second), camunda.WithRetryOn(retryNotYet))
	if err != nil {
		t.Fatalf("job stream for %q was not registered at %s: %v", jobType, url, err)
	}
}

func TestUserTaskListenerCorrectionsRESTWorker(t *testing.T) {
	testUserTaskListenerCorrections(t, false)
}

func TestUserTaskListenerCorrectionsStreamWorker(t *testing.T) {
	testUserTaskListenerCorrections(t, true)
}

// testUserTaskListenerCorrections completes a `creating` listener job with
// corrections that set the assignee and priority and clear both scheduled dates.
func testUserTaskListenerCorrections(t *testing.T, stream bool) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	suffix := uniqueSuffix(t)
	listenerType := "go-job-result-corrections-" + suffix
	processID := deployUserTaskListener(ctx, t, c, suffix, "creating", listenerType)
	assignee := "go-corrected-" + suffix

	var handled atomic.Int32
	handler := camunda.CompletionHandler(func(context.Context, *camunda.Job) (camunda.JobCompletion, error) {
		handled.Add(1)
		corrections := camunda.NewJobResultCorrections()
		corrections.SetAssignee(assignee)
		corrections.SetPriority(80)
		corrections.ClearDueDate()
		corrections.ClearFollowUpDate()
		return camunda.JobCompletion{Result: camunda.CorrectUserTask(*corrections)}, nil
	})
	if stream {
		// No sidecar poll: the listener job must arrive over the gRPC stream and be
		// completed over gRPC CompleteJob.
		w := c.NewStreamJobWorker(listenerType, handler, camunda.WithStreamPollInterval(-1))
		runWorker(t, ctx, w.Run)
		awaitJobStream(ctx, t, listenerType)
	} else {
		w := c.NewJobWorker(listenerType, handler,
			camunda.WithRequestTimeout(5*time.Second), camunda.WithPollInterval(200*time.Millisecond))
		runWorker(t, ctx, w.Run)
	}

	pik := startProcess(ctx, t, c, processID, suffix)
	task := awaitUserTask(ctx, t, c, pik, func(ut *camundaapi.UserTaskResult) bool {
		return ut.GetState() == camundaapi.USERTASKSTATEENUM_CREATED
	})

	if handled.Load() == 0 {
		t.Error("the creating listener job was never handled")
	}
	if got := task.GetAssignee(); got != assignee {
		t.Errorf("assignee = %q, want %q", got, assignee)
	}
	if got := task.GetPriority(); got != 80 {
		t.Errorf("priority = %d, want 80", got)
	}
	if v, _ := task.GetDueDateOk(); v != nil {
		t.Errorf("dueDate = %v, want it cleared (modeled %v)", v, modeledDueDate)
	}
	if v, _ := task.GetFollowUpDateOk(); v != nil {
		t.Errorf("followUpDate = %v, want it cleared (modeled %v)", v, modeledFollowUpDate)
	}
}

// TestUserTaskListenerDenial denies a completion from a `completing` listener and
// asserts the task stays open, the process keeps waiting, and the listener job
// records the denial.
func TestUserTaskListenerDenial(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	suffix := uniqueSuffix(t)
	listenerType := "go-job-result-denial-" + suffix
	processID := deployUserTaskListener(ctx, t, c, suffix, "completing", listenerType)
	reason := "Insufficient documentation " + suffix

	var handled atomic.Int32
	w := c.NewJobWorker(listenerType,
		camunda.CompletionHandler(func(context.Context, *camunda.Job) (camunda.JobCompletion, error) {
			handled.Add(1)
			return camunda.JobCompletion{Result: camunda.DenyUserTask(reason)}, nil
		}),
		camunda.WithRequestTimeout(5*time.Second), camunda.WithPollInterval(200*time.Millisecond))
	runWorker(t, ctx, w.Run)

	pik := startProcess(ctx, t, c, processID, suffix)
	task := awaitUserTask(ctx, t, c, pik, func(ut *camundaapi.UserTaskResult) bool {
		return ut.GetState() == camundaapi.USERTASKSTATEENUM_CREATED
	})
	// Control for the corrections test: without a correction the modeled schedule
	// is on the task, so a missing date there really was cleared.
	if v, _ := task.GetDueDateOk(); v == nil || !v.Equal(modeledDueDate) {
		t.Errorf("dueDate = %v, want the modeled %v", v, modeledDueDate)
	}
	if v, _ := task.GetFollowUpDateOk(); v == nil || !v.Equal(modeledFollowUpDate) {
		t.Errorf("followUpDate = %v, want the modeled %v", v, modeledFollowUpDate)
	}

	// The engine runs the completing listener before it accepts the completion, so
	// the denial comes back as the rejection of this very request.
	completeErr := c.CompleteUserTask(ctx, task.GetUserTaskKey(), *camundaapi.NewUserTaskCompletionRequest())
	var apiErr *camunda.APIError
	if !errors.As(completeErr, &apiErr) {
		t.Fatalf("CompleteUserTask error = %v, want an *APIError rejecting the denied completion", completeErr)
	}
	t.Logf("denied completion returned HTTP %d: %s", apiErr.Status, apiErr.Body)
	if apiErr.Status != http.StatusConflict {
		t.Errorf("denied completion status = %d, want %d", apiErr.Status, http.StatusConflict)
	}
	if !strings.Contains(apiErr.Body, "denied") {
		t.Errorf("denied completion body = %s, want it to report the denial", apiErr.Body)
	}
	if handled.Load() == 0 {
		t.Error("the completing listener job was never handled")
	}

	// The listener job is indexed as denied, with the worker's reason.
	key := pik
	jobFilter := camundaapi.NewJobFilter()
	jobFilter.SetProcessInstanceKey(camundaapi.ProcessInstanceKeyExactMatchAsProcessInstanceKeyFilterProperty(&key))
	jobQuery := camundaapi.NewJobSearchQuery()
	jobQuery.SetFilter(*jobFilter)
	job, err := camunda.Poll(ctx, func(ctx context.Context) (*camundaapi.JobSearchResult, error) {
		result, err := c.SearchJobs(ctx, *jobQuery)
		if err != nil {
			return nil, err
		}
		for i, j := range result.GetItems() {
			if j.GetType() == listenerType && j.GetIsDenied() {
				return &result.GetItems()[i], nil
			}
		}
		return nil, errNotYetIndexed
	}, camunda.WithPollTimeout(45*time.Second), camunda.WithRetryOn(retryNotYet))
	if err != nil {
		t.Fatalf("denied listener job not observed: %v", err)
	}
	if got := job.GetDeniedReason(); got != reason {
		t.Errorf("deniedReason = %q, want %q", got, reason)
	}

	// The task and its process instance stay open.
	got, err := c.GetUserTask(ctx, task.GetUserTaskKey())
	if err != nil {
		t.Fatalf("GetUserTask: %v", err)
	}
	if got.GetState() != camundaapi.USERTASKSTATEENUM_CREATED {
		t.Errorf("user task state after denial = %s, want %s", got.GetState(), camundaapi.USERTASKSTATEENUM_CREATED)
	}
	instance, err := c.GetProcessInstance(ctx, pik)
	if err != nil {
		t.Fatalf("GetProcessInstance: %v", err)
	}
	if instance.GetState() != camundaapi.PROCESSINSTANCESTATEENUM_ACTIVE {
		t.Errorf("process instance state after denial = %s, want ACTIVE", instance.GetState())
	}
}

// TestAdHocSubProcessJobResult drives a job-worker ad-hoc sub-process: the first
// job activates exactly one of the two inner tasks; once that task's flow has
// completed (the output collection holds one entry), the next job fulfills the
// completion condition.
func TestAdHocSubProcessJobResult(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	suffix := uniqueSuffix(t)
	processID := "go-job-result-ad-hoc-" + suffix
	jobType := "go-job-result-ad-hoc-" + suffix
	taskA, taskB := "task_a_"+suffix, "task_b_"+suffix
	model := strings.NewReplacer(
		"PROCESS_ID", processID,
		"AD_HOC_JOB_TYPE", jobType,
		"AD_HOC_ID", "plan_"+suffix,
		"TASK_A_ID", taskA,
		"TASK_B_ID", taskB,
	).Replace(adHocJobWorkerBPMN)
	deployModel(ctx, t, c, processID+".bpmn", []byte(model))

	var activations, completions atomic.Int32
	w := c.NewJobWorker(jobType,
		camunda.CompletionHandler(func(_ context.Context, job *camunda.Job) (camunda.JobCompletion, error) {
			var vars struct {
				Done []any `json:"done"`
			}
			if err := job.Variables(&vars); err != nil {
				return camunda.JobCompletion{}, err
			}
			result := camunda.NewJobResultAdHocSubProcess()
			if len(vars.Done) == 0 {
				activations.Add(1)
				result.SetActivateElements([]camunda.JobResultActivateElement{
					camunda.ActivateAdHocElement(camunda.ElementId(taskA), map[string]any{"activatedBy": suffix}),
				})
			} else {
				completions.Add(1)
				result.SetIsCompletionConditionFulfilled(true)
			}
			return camunda.JobCompletion{Result: camunda.AdHocSubProcessJobResult(*result)}, nil
		}),
		camunda.WithRequestTimeout(5*time.Second), camunda.WithPollInterval(200*time.Millisecond))
	runWorker(t, ctx, w.Run)

	pik := startProcess(ctx, t, c, processID, suffix)
	_, err := camunda.Poll(ctx, func(ctx context.Context) (*camundaapi.ProcessInstanceResult, error) {
		instance, err := c.GetProcessInstance(ctx, pik)
		if err != nil {
			return nil, err
		}
		if instance.GetState() != camundaapi.PROCESSINSTANCESTATEENUM_COMPLETED {
			return nil, errNotYetIndexed
		}
		return instance, nil
	}, camunda.WithPollTimeout(45*time.Second), camunda.WithRetryOn(retryNotYet))
	if err != nil {
		t.Fatalf("process instance did not complete: %v", err)
	}
	if activations.Load() == 0 || completions.Load() == 0 {
		t.Errorf("ad-hoc jobs handled: %d activating, %d completing; want at least one of each",
			activations.Load(), completions.Load())
	}

	// Element instances of a completed instance are indexed eventually; wait for
	// the activated task, then check nothing else inside the sub-process ran.
	filter := camundaapi.NewElementInstanceFilter()
	filter.SetProcessInstanceKey(pik)
	query := camundaapi.NewElementInstanceSearchQuery()
	query.SetFilter(*filter)
	counts, err := camunda.Poll(ctx, func(ctx context.Context) (map[string]int, error) {
		result, err := c.SearchElementInstances(ctx, *query)
		if err != nil {
			return nil, err
		}
		counts := map[string]int{}
		for _, item := range result.GetItems() {
			if item.GetState() == camundaapi.ELEMENTINSTANCESTATEENUM_COMPLETED {
				counts[string(item.GetElementId())]++
			}
		}
		if counts[taskA] == 0 || counts["end"] == 0 {
			return nil, errNotYetIndexed
		}
		return counts, nil
	}, camunda.WithPollTimeout(45*time.Second), camunda.WithRetryOn(retryNotYet))
	if err != nil {
		t.Fatalf("activated element %q not observed: %v", taskA, err)
	}
	if counts[taskA] != 1 {
		t.Errorf("%s completed %d times, want exactly 1", taskA, counts[taskA])
	}
	if counts[taskB] != 0 {
		t.Errorf("%s completed %d times, want 0 (it was never activated)", taskB, counts[taskB])
	}
}
