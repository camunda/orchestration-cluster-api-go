package camunda

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/internal/falcon"
)

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
	tenantIDs      []string
	withLease      bool
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

// WithWorkerTenantIDs restricts job activation to the given tenant ids,
// overriding the client's default tenant.
func WithWorkerTenantIDs(ids ...string) WorkerOption {
	return func(w *JobWorker) { w.tenantIDs = ids }
}

// WithJobLease activates jobs with a lease. Each job then carries a lease token,
// which this worker sends back on complete, fail, and throw-error. The engine
// rejects a command bearing a stale token, fencing the job against a superseded
// activation — for example after the job timed out and another worker picked it
// up.
//
// Off by default, matching the engine's own default. Enabling it requires an
// engine that supports job leases. It has no effect when jobs arrive over the
// FALCON command stream, which activates them outside the REST activation API.
func WithJobLease(enabled bool) WorkerOption {
	return func(w *JobWorker) { w.withLease = enabled }
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
		tenantIDs:      defaultTenantIDs(c.cfg.DefaultTenantID),
	}
	if w.maxConcurrent <= 0 {
		w.maxConcurrent = 1
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Run polls and dispatches jobs until ctx is canceled, then waits for in-flight
// handlers to finish and returns ctx.Err(). Run blocks; call it in a goroutine to
// run alongside other work.
//
// When the gateway advertises the FALCON command stream (a nanobpmn gateway) and
// FALCON is enabled, jobs are pushed over a WebSocket subscription instead of
// REST long-polling. If the subscription cannot be established (e.g. a proxy
// blocks WebSockets) the worker transparently falls back to REST polling.
func (w *JobWorker) Run(ctx context.Context) error {
	if caps := w.client.falconCaps(ctx); caps != nil {
		if err := w.runFalcon(ctx, caps); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.client.logger.Warn("falcon subscribe failed; falling back to REST job polling",
				"type", w.jobType, "error", err)
		} else {
			return ctx.Err()
		}
	}
	return w.runRESTPoll(ctx)
}

// runFalcon subscribes to the command stream and dispatches pushed jobs. It
// returns nil once ctx is canceled (graceful stop) or a non-nil error if the
// initial subscribe fails, so Run can fall back to REST polling.
func (w *JobWorker) runFalcon(ctx context.Context, caps *falcon.Caps) error {
	sw, err := falcon.Subscribe(caps.Endpoints, w.client.falconDialer, falcon.SubscribeArgs{
		JobType:        w.jobType,
		JobCredits:     int64(w.maxConcurrent),
		FetchVariables: w.fetchVariables,
		TimeoutMs:      w.timeout.Milliseconds(),
		Worker:         w.name,
	})
	if err != nil {
		return err
	}
	defer sw.Close()

	var wg sync.WaitGroup
	for ctx.Err() == nil {
		raw, ok := sw.NextJob(ctx, 500*time.Millisecond)
		if !ok {
			continue
		}
		var ajr openapi.ActivatedJobResult
		if err := json.Unmarshal(raw, &ajr); err != nil {
			// Undecodable push (protocol drift or a gateway bug): log a diagnostic,
			// replenish the consumed credit, and skip rather than losing the slot
			// silently. The raw payload is omitted as it may be large or sensitive.
			w.client.logger.Warn("falcon: skipping undecodable job frame", "type", w.jobType, "error", err)
			sw.Replenish(1)
			continue
		}
		job := newRESTJob(ajr, w.client.clock)
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.handleFalcon(ctx, sw, job)
		}()
	}
	wg.Wait()
	return nil
}

// handleFalcon runs the handler and acknowledges the job over the command stream.
// Each ack (complete/fail/throw) replenishes one delivery credit.
func (w *JobWorker) handleFalcon(ctx context.Context, sw *falcon.StreamWorker, job *Job) {
	vars, err := w.handler(ctx, job)
	if err != nil {
		var bpmn *BpmnError
		if errors.As(err, &bpmn) {
			sw.ThrowError(job.Key(), bpmn.Code, bpmn.Message, bpmn.Variables)
			return
		}
		retries := job.Retries() - 1
		if retries < 0 {
			retries = 0
		}
		sw.Fail(job.Key(), retries, err.Error())
		return
	}
	sw.Complete(job.Key(), vars)
}

// runRESTPoll is the REST long-polling worker loop (also the FALCON fallback).
func (w *JobWorker) runRESTPoll(ctx context.Context) error {
	var inFlight atomic.Int32
	var wg sync.WaitGroup

	for ctx.Err() == nil {
		free := w.maxConcurrent - int(inFlight.Load())
		if free <= 0 {
			if err := sleepCtx(ctx, w.client.clock, w.pollInterval); err != nil {
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
			if err := sleepCtx(ctx, w.client.clock, w.pollInterval); err != nil {
				break
			}
			continue
		}

		for i := range jobs {
			job := newRESTJob(jobs[i], w.client.clock)
			inFlight.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer inFlight.Add(-1)
				w.handle(ctx, job)
			}()
		}

		if len(jobs) == 0 {
			if err := sleepCtx(ctx, w.client.clock, w.pollInterval); err != nil {
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
	if len(w.tenantIDs) > 0 {
		req.SetTenantIds(w.tenantIDs)
	}
	if w.withLease {
		req.SetWithLease(true)
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
	// canceled. Bound the ack with its own timeout.
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	w.client.restAck(ackCtx, job, vars, err)
}

// restAck completes, fails, or throws a BPMN error for a REST-activated job based
// on the handler outcome. It is used by the REST JobWorker and by the gRPC stream
// worker's REST sidecar poll (poll-activated jobs are acknowledged over REST).
func (c *CamundaClient) restAck(ctx context.Context, job *Job, vars map[string]any, handlerErr error) {
	if handlerErr != nil {
		var bpmn *BpmnError
		if errors.As(handlerErr, &bpmn) {
			c.restThrowError(ctx, job, bpmn)
		} else {
			c.restFailJob(ctx, job, handlerErr)
		}
		return
	}
	c.restCompleteJob(ctx, job, vars)
}

func (c *CamundaClient) restCompleteJob(ctx context.Context, job *Job, vars map[string]any) {
	req := openapi.NewJobCompletionRequest()
	if len(vars) > 0 {
		req.SetVariables(vars)
	}
	if job.leaseToken != "" {
		req.SetLeaseToken(job.leaseToken)
	}
	_, err := c.raw.JobAPI.CompleteJob(ctx, openapi.JobKey(job.key)).
		JobCompletionRequest(*req).Execute()
	if err != nil {
		c.logger.Error("complete job failed", "job", job.Key(), "error", err)
	}
}

func (c *CamundaClient) restFailJob(ctx context.Context, job *Job, cause error) {
	req := openapi.NewJobFailRequest()
	retries := job.Retries() - 1
	if retries < 0 {
		retries = 0
	}
	req.SetRetries(retries)
	if cause != nil {
		req.SetErrorMessage(cause.Error())
	}
	if job.leaseToken != "" {
		req.SetLeaseToken(job.leaseToken)
	}
	_, err := c.raw.JobAPI.FailJob(ctx, openapi.JobKey(job.key)).
		JobFailRequest(*req).Execute()
	if err != nil {
		c.logger.Error("fail job failed", "job", job.Key(), "error", err)
	}
}

func (c *CamundaClient) restThrowError(ctx context.Context, job *Job, bpmn *BpmnError) {
	req := openapi.NewJobErrorRequest(bpmn.Code)
	if bpmn.Message != "" {
		req.SetErrorMessage(bpmn.Message)
	}
	if len(bpmn.Variables) > 0 {
		req.SetVariables(bpmn.Variables)
	}
	if job.leaseToken != "" {
		req.SetLeaseToken(job.leaseToken)
	}
	_, err := c.raw.JobAPI.ThrowJobError(ctx, openapi.JobKey(job.key)).
		JobErrorRequest(*req).Execute()
	if err != nil {
		c.logger.Error("throw job error failed", "job", job.Key(), "error", err)
	}
}

// defaultTenantIDs returns a single-element tenant slice for a non-empty default
// tenant id, or nil.
func defaultTenantIDs(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

// sleepCtx waits for d on clock, or until ctx is canceled.
//
// A non-positive d becomes one second. Clock.Sleep returns immediately in that case,
// so without this an unset poll interval would spin the poll loop rather than pace it.
func sleepCtx(ctx context.Context, clock Clock, d time.Duration) error {
	if d <= 0 {
		d = time.Second
	}
	return clock.Sleep(ctx, d)
}
