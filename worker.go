package camunda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

// Job is an activated job passed to a JobHandler.
type Job struct {
	raw openapi.ActivatedJobResult
}

// Key returns the job key.
func (j *Job) Key() string { return string(j.raw.GetJobKey()) }

// Type returns the job type.
func (j *Job) Type() string { return j.raw.GetType() }

// Retries returns the job's remaining retries.
func (j *Job) Retries() int32 { return j.raw.GetRetries() }

// ProcessInstanceKey returns the key of the owning process instance.
func (j *Job) ProcessInstanceKey() string { return string(j.raw.GetProcessInstanceKey()) }

// ElementID returns the BPMN element id that created the job.
func (j *Job) ElementID() string { return j.raw.GetElementId() }

// CustomHeaders returns the job's custom headers.
func (j *Job) CustomHeaders() map[string]any { return j.raw.GetCustomHeaders() }

// RawVariables returns the job variables as a decoded map.
func (j *Job) RawVariables() map[string]any { return j.raw.GetVariables() }

// Variables unmarshals the job variables into v (a pointer to a struct or map).
func (j *Job) Variables(v any) error {
	b, err := json.Marshal(j.raw.GetVariables())
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// BpmnError, when returned by a JobHandler, makes the worker throw a BPMN error
// (raising a catch event) instead of failing the job.
type BpmnError struct {
	Code      string
	Message   string
	Variables map[string]any
}

func (e *BpmnError) Error() string {
	return fmt.Sprintf("bpmn error %q: %s", e.Code, e.Message)
}

// JobHandler processes an activated job:
//   - returning (variables, nil) completes the job with those variables;
//   - returning a *BpmnError throws a BPMN error;
//   - returning any other error fails the job (decrementing its retries).
type JobHandler func(ctx context.Context, job *Job) (map[string]any, error)

// JobWorker polls for jobs of a given type and dispatches them to a handler with
// bounded concurrency. Job completion, failure, and BPMN-error operations are
// drain operations and bypass the client-side backpressure gate.
type JobWorker struct {
	client         *CamundaClient
	jobType        string
	handler        JobHandler
	name           string
	maxConcurrent  int
	timeout        time.Duration
	requestTimeout time.Duration
	pollInterval   time.Duration
	fetchVariables []string
}

// WorkerOption customizes a JobWorker.
type WorkerOption func(*JobWorker)

// WithWorkerName sets the worker name reported to the engine.
func WithWorkerName(name string) WorkerOption {
	return func(w *JobWorker) { w.name = name }
}

// WithMaxConcurrentJobs caps the number of jobs handled concurrently.
func WithMaxConcurrentJobs(n int) WorkerOption {
	return func(w *JobWorker) {
		if n > 0 {
			w.maxConcurrent = n
		}
	}
}

// WithJobTimeout sets how long an activated job is exclusively locked to this worker.
func WithJobTimeout(d time.Duration) WorkerOption {
	return func(w *JobWorker) { w.timeout = d }
}

// WithRequestTimeout sets the long-poll request timeout for job activation.
func WithRequestTimeout(d time.Duration) WorkerOption {
	return func(w *JobWorker) { w.requestTimeout = d }
}

// WithPollInterval sets the pause between polls when idle, at capacity, or after
// an activation error.
func WithPollInterval(d time.Duration) WorkerOption {
	return func(w *JobWorker) { w.pollInterval = d }
}

// WithFetchVariables restricts the variables fetched with each job. Empty fetches all.
func WithFetchVariables(vars ...string) WorkerOption {
	return func(w *JobWorker) { w.fetchVariables = vars }
}

// NewJobWorker creates a worker for jobType. Defaults are seeded from the
// client's CAMUNDA_WORKER_* configuration and can be overridden with options.
func (c *CamundaClient) NewJobWorker(jobType string, handler JobHandler, opts ...WorkerOption) *JobWorker {
	wd := c.cfg.WorkerDefaults
	w := &JobWorker{
		client:         c,
		jobType:        jobType,
		handler:        handler,
		name:           wd.Name,
		maxConcurrent:  wd.MaxConcurrentJobs,
		timeout:        time.Duration(wd.TimeoutMs) * time.Millisecond,
		requestTimeout: time.Duration(wd.RequestTimeoutMs) * time.Millisecond,
		pollInterval:   time.Second,
	}
	if w.maxConcurrent <= 0 {
		w.maxConcurrent = 1
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Run polls and dispatches jobs until ctx is cancelled, then waits for in-flight
// handlers to finish and returns ctx.Err(). Run blocks; call it in a goroutine to
// run alongside other work.
func (w *JobWorker) Run(ctx context.Context) error {
	var inFlight atomic.Int32
	var wg sync.WaitGroup

	for ctx.Err() == nil {
		free := w.maxConcurrent - int(inFlight.Load())
		if free <= 0 {
			if err := sleepCtx(ctx, w.pollInterval); err != nil {
				break
			}
			continue
		}

		jobs, err := w.activate(ctx, free)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			w.client.logger.Warn("activate jobs failed", "type", w.jobType, "error", err)
			if err := sleepCtx(ctx, w.pollInterval); err != nil {
				break
			}
			continue
		}

		for i := range jobs {
			job := &Job{raw: jobs[i]}
			inFlight.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer inFlight.Add(-1)
				w.handle(ctx, job)
			}()
		}

		if len(jobs) == 0 {
			if err := sleepCtx(ctx, w.pollInterval); err != nil {
				break
			}
		}
	}

	wg.Wait()
	return ctx.Err()
}

func (w *JobWorker) activate(ctx context.Context, maxJobs int) ([]openapi.ActivatedJobResult, error) {
	req := openapi.NewJobActivationRequest(w.jobType, w.timeout.Milliseconds(), int32(maxJobs))
	if w.name != "" {
		req.SetWorker(w.name)
	}
	if w.requestTimeout > 0 {
		req.SetRequestTimeout(w.requestTimeout.Milliseconds())
	}
	if len(w.fetchVariables) > 0 {
		req.SetFetchVariable(w.fetchVariables)
	}
	result, resp, err := w.client.raw.JobAPI.ActivateJobs(ctx).JobActivationRequest(*req).Execute()
	if err != nil {
		return nil, w.client.wrapError(resp, err)
	}
	return result.GetJobs(), nil
}

func (w *JobWorker) handle(ctx context.Context, job *Job) {
	vars, err := w.handler(ctx, job)

	// Acknowledge the job even while the worker is shutting down: a handler that
	// already did its work must not have its result dropped because Run's ctx was
	// cancelled. Bound the ack with its own timeout.
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err != nil {
		var bpmn *BpmnError
		if errors.As(err, &bpmn) {
			w.throwError(ackCtx, job, bpmn)
		} else {
			w.failJob(ackCtx, job, err)
		}
		return
	}
	w.completeJob(ackCtx, job, vars)
}

func (w *JobWorker) completeJob(ctx context.Context, job *Job, vars map[string]any) {
	req := openapi.NewJobCompletionRequest()
	if len(vars) > 0 {
		req.SetVariables(vars)
	}
	_, err := w.client.raw.JobAPI.CompleteJob(ctx, openapi.JobKey(job.raw.GetJobKey())).
		JobCompletionRequest(*req).Execute()
	if err != nil {
		w.client.logger.Error("complete job failed", "job", job.Key(), "error", err)
	}
}

func (w *JobWorker) failJob(ctx context.Context, job *Job, cause error) {
	req := openapi.NewJobFailRequest()
	retries := job.Retries() - 1
	if retries < 0 {
		retries = 0
	}
	req.SetRetries(retries)
	if cause != nil {
		req.SetErrorMessage(cause.Error())
	}
	_, err := w.client.raw.JobAPI.FailJob(ctx, openapi.JobKey(job.raw.GetJobKey())).
		JobFailRequest(*req).Execute()
	if err != nil {
		w.client.logger.Error("fail job failed", "job", job.Key(), "error", err)
	}
}

func (w *JobWorker) throwError(ctx context.Context, job *Job, bpmn *BpmnError) {
	req := openapi.NewJobErrorRequest(bpmn.Code)
	if bpmn.Message != "" {
		req.SetErrorMessage(bpmn.Message)
	}
	if len(bpmn.Variables) > 0 {
		req.SetVariables(bpmn.Variables)
	}
	_, err := w.client.raw.JobAPI.ThrowJobError(ctx, openapi.JobKey(job.raw.GetJobKey())).
		JobErrorRequest(*req).Execute()
	if err != nil {
		w.client.logger.Error("throw job error failed", "job", job.Key(), "error", err)
	}
}

// sleepCtx waits for d or until ctx is cancelled, returning ctx.Err() if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		d = time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
