package camunda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
	"github.com/camunda/orchestration-cluster-api-go/v11/internal/falcon"
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
	// startupDelay is drawn once, when the worker is built; see startupJitter.
	startupDelay time.Duration
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

// WithPollInterval sets the pause after an empty poll or an activation error. A
// worker at capacity does not wait this interval: it polls again as soon as
// enough slots free up.
func WithPollInterval(d time.Duration) WorkerOption {
	return func(w *JobWorker) { w.pollInterval = d }
}

// WithFetchVariables restricts the variables fetched with each job. Empty fetches all.
func WithFetchVariables(vars ...string) WorkerOption {
	return func(w *JobWorker) { w.fetchVariables = vars }
}

// WithWorkerTenantIDs restricts job activation to the given tenant ids,
// overriding the client's default tenant. An empty list keeps the default.
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
// engine that supports job leases: a server that ignores the flag would leave
// every acknowledgement unfenced, so a job arriving without a token fails the
// activation with ErrLeaseNotHonored rather than being handled unfenced. It has
// no effect when jobs arrive over the FALCON command stream, which activates them
// outside the REST activation API.
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
	c.registerWorkerCapacity(w.maxConcurrent)
	if len(w.tenantIDs) == 0 {
		// An empty tenant option must not erase the default-tenant rule.
		w.tenantIDs = defaultTenantIDs(c.cfg.DefaultTenantID)
	}
	w.startupDelay = startupJitter(c.random, wd.StartupJitterMaxSeconds)
	return w
}

// startupJitter draws the delay a worker waits before it first polls, spreading
// a fleet that restarts together. It is drawn when the worker is built rather
// than in Run: Run is normally started on its own goroutine, so drawing there
// would hand a seeded source's sequence to workers in scheduler order.
func startupJitter(r Random, maxSeconds int) time.Duration {
	if maxSeconds <= 0 {
		return 0
	}
	return time.Duration(r.Float64() * float64(maxSeconds) * float64(time.Second))
}

// waitStartup waits out a worker's startup delay on clock.
func waitStartup(ctx context.Context, clock Clock, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	return clock.Sleep(ctx, d)
}

// Run polls and dispatches jobs until ctx is canceled, then waits for in-flight
// handlers to finish and returns ctx.Err(). Run blocks; call it in a goroutine to
// run alongside other work.
//
// A worker configured with a startup jitter (CAMUNDA_WORKER_STARTUP_JITTER_MAX_SECONDS)
// first waits out its delay on the client's clock.
//
// When the gateway advertises the FALCON command stream (a nanobpmn gateway) and
// FALCON is enabled, jobs are pushed over a WebSocket subscription instead of
// REST long-polling. If the subscription cannot be established (e.g. a proxy
// blocks WebSockets) the worker transparently falls back to REST polling.
func (w *JobWorker) Run(ctx context.Context) error {
	if err := waitStartup(ctx, w.client.clock, w.startupDelay); err != nil {
		return err
	}
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
	if w.client.cfg.FalconEnabled() && ctx.Err() == nil {
		w.client.logger.Info("falcon enabled but command stream not in use; worker using REST job polling",
			"type", w.jobType)
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
		TenantIDs:      w.tenantIDs,
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
		var ajr camundaapi.ActivatedJobResult
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

// falconJobResultUnsupported is the failure message for a job completed with a
// job result over the FALCON command stream.
const falconJobResultUnsupported = "Job results (user task corrections/denial, ad-hoc sub-process results) " +
	"cannot be sent over the FALCON command stream; set CAMUNDA_FORCE_REST=true to use the REST transport"

// handleFalcon runs the handler and acknowledges the job over the command stream.
// Each ack (complete/fail/throw) replenishes one delivery credit.
//
// The command stream's completeJob frame has no result field, so a completion
// carrying a job result fails the job instead. Completing it without the result
// would be wrong, not just lossy: it would approve a task the handler denied.
func (w *JobWorker) handleFalcon(ctx context.Context, sw *falcon.StreamWorker, job *Job) {
	vars, err := w.client.invokeHandler(ctx, w.handler, job)
	if err == nil && job.result != nil {
		w.client.logger.Error("job result not sent: "+falconJobResultUnsupported, "job", job.Key())
		sw.Fail(job.Key(), max(job.Retries()-1, 0), falconJobResultUnsupported)
		return
	}
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

// slotGate tracks a worker's in-flight jobs and wakes the poll loop when enough
// capacity has opened up to make another activation worthwhile.
//
// The poll loop never sleeps a fixed interval while at capacity: it blocks on
// wake, which release signals as soon as free capacity reaches threshold or the
// worker becomes idle. The threshold (a quarter of maxConcurrent, at least one)
// stops a large worker from micro-polling one job at a time; the idle rule
// ensures a tail of fewer than threshold jobs never stalls the worker.
type slotGate struct {
	max       int32
	threshold int32
	inFlight  atomic.Int32
	// wake has capacity one, so a release while the poller is not waiting is
	// remembered instead of lost, and releases never block.
	wake chan struct{}
}

func newSlotGate(maxConcurrent int) *slotGate {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	m := int32(math.MaxInt32)
	if maxConcurrent < math.MaxInt32 {
		m = int32(maxConcurrent)
	}
	return &slotGate{
		max:       m,
		threshold: max(1, m/4),
		wake:      make(chan struct{}, 1),
	}
}

// free returns the number of jobs the worker should request now, or 0 when it
// should wait: below threshold while jobs are still in flight.
func (g *slotGate) free() int {
	n := g.inFlight.Load()
	f := g.max - n
	if f <= 0 || (f < g.threshold && n > 0) {
		return 0
	}
	return int(f)
}

func (g *slotGate) acquire(n int) { g.inFlight.Add(int32(n)) }

// release frees one slot and wakes the poller if capacity crossed the threshold.
func (g *slotGate) release() {
	n := g.inFlight.Add(-1)
	if n == 0 || g.max-n >= g.threshold {
		select {
		case g.wake <- struct{}{}:
		default:
		}
	}
}

// wait blocks until release signals or ctx is canceled.
func (g *slotGate) wait(ctx context.Context) error {
	select {
	case <-g.wake:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runRESTPoll is the REST long-polling worker loop (also the FALCON fallback).
//
// At capacity it waits for a slot to open rather than for pollInterval; the
// interval paces only empty polls and activation-error backoff.
func (w *JobWorker) runRESTPoll(ctx context.Context) error {
	gate := newSlotGate(w.maxConcurrent)
	var wg sync.WaitGroup

	for ctx.Err() == nil {
		free := gate.free()
		if free <= 0 {
			if err := gate.wait(ctx); err != nil {
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

		gate.acquire(len(jobs))
		for i := range jobs {
			job := newRESTJob(jobs[i], w.client.clock)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer gate.release()
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

// toTenantIDs brands a slice of plain tenant-id strings as []camundaapi.TenantId for
// the generated request setters.
func toTenantIDs(ids []string) []camundaapi.TenantId {
	out := make([]camundaapi.TenantId, len(ids))
	for i, id := range ids {
		out[i] = camundaapi.TenantId(id)
	}
	return out
}

func (w *JobWorker) activate(ctx context.Context, maxJobs int) ([]camundaapi.ActivatedJobResult, error) {
	req := camundaapi.NewJobActivationRequest(w.jobType, w.timeout.Milliseconds(), int32(maxJobs))
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
		req.SetTenantIds(toTenantIDs(w.tenantIDs))
	}
	if w.withLease {
		req.SetWithLease(true)
	}
	result, resp, err := w.client.raw.JobAPI.ActivateJobs(ctx).JobActivationRequest(*req).Execute()
	if err != nil {
		return nil, w.client.wrapError(resp, err)
	}
	jobs := result.GetJobs()
	for i := range jobs {
		if err := requireLeasePresence(w.withLease, string(jobs[i].GetJobLeaseToken())); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

// invokeHandler runs h and converts a panic into an ordinary handler error, so
// the caller's existing fail path applies.
//
// A job handler runs on its own goroutine, and in Go an unrecovered panic on any
// goroutine terminates the whole process. Without this, one handler that writes
// to a nil map, indexes out of range, or asserts the wrong type on a job
// variable takes down the host application — and because the job is never
// acknowledged, it also sits unavailable until its timeout expires instead of
// being retried promptly. Recovering turns that into one failed job.
//
// The panic value and its stack go to the SDK log at error level, where a
// crashing process would have printed them anyway. They are kept out of the
// returned error, because that message is sent to the cluster as the incident's
// error message, where it is both widely visible and durable, and a panic value
// can carry job-variable content (see camunda/sdk-infra#89).
func (c *CamundaClient) invokeHandler(ctx context.Context, h JobHandler, job *Job) (vars map[string]any, err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		// Every attacker-influenceable field is quoted, not just the stack. The
		// logger writes field values verbatim and one record per line, so a
		// newline in a panic value — which may be job-variable content — would
		// otherwise let a handler forge log records. %q escapes newlines and
		// other control characters while keeping the detail readable.
		c.logger.Error("job handler panicked; failing the job",
			"job", fmt.Sprintf("%q", job.Key()),
			"type", fmt.Sprintf("%q", job.Type()),
			"panic", fmt.Sprintf("%q", fmt.Sprintf("%v", r)),
			"stack", fmt.Sprintf("%q", debug.Stack()))
		// Discard anything the handler returned before panicking.
		vars, err = nil, ErrHandlerPanic
	}()
	return h(ctx, job)
}

func (w *JobWorker) handle(ctx context.Context, job *Job) {
	vars, err := w.client.invokeHandler(ctx, w.handler, job)

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
	var result *JobResult
	if handlerErr == nil {
		result, handlerErr = completionResult(job, vars)
	}
	if handlerErr != nil {
		var bpmn *BpmnError
		if errors.As(handlerErr, &bpmn) {
			c.restThrowError(ctx, job, bpmn)
		} else {
			c.restFailJob(ctx, job, handlerErr)
		}
		return
	}
	c.restCompleteJob(ctx, job, vars, result)
}

// restCompleteJob completes a job over REST. result must already have been passed
// through completionResult, or be nil.
func (c *CamundaClient) restCompleteJob(ctx context.Context, job *Job, vars map[string]any, result *JobResult) {
	var err error
	if c.useGeneratedAck() {
		req := camundaapi.NewJobCompletionRequest()
		if len(vars) > 0 {
			req.SetVariables(vars)
		}
		if result != nil {
			req.SetResult(*result)
		}
		if job.leaseToken != "" {
			req.SetJobLeaseToken(camundaapi.JobLeaseToken(job.leaseToken))
		}
		var resp *http.Response
		resp, err = c.raw.JobAPI.CompleteJob(ctx, camundaapi.JobKey(job.key)).
			JobCompletionRequest(*req).Execute()
		err = c.wrapError(resp, err)
	} else {
		body := emptyObject
		if len(vars) > 0 || result != nil || job.leaseToken != "" {
			if len(vars) == 0 {
				vars = nil
			}
			body, err = json.Marshal(jobCompletionBody{Variables: vars, Result: result, JobLeaseToken: job.leaseToken})
		}
		if err == nil {
			err = c.postJobAck(ctx, "JobAPIService.CompleteJob", job.key, "completion", body)
		}
	}
	if err != nil {
		c.logJobCommandFailure("complete job", job.Key(), classifyRESTJobCommandErr(err), err)
	}
}

func (c *CamundaClient) restFailJob(ctx context.Context, job *Job, cause error) {
	retries := job.Retries() - 1
	if retries < 0 {
		retries = 0
	}
	var msg *string
	if cause != nil {
		m := cause.Error()
		msg = &m
	}
	var err error
	if c.useGeneratedAck() {
		req := camundaapi.NewJobFailRequest()
		req.SetRetries(retries)
		if msg != nil {
			req.SetErrorMessage(*msg)
		}
		if job.leaseToken != "" {
			req.SetJobLeaseToken(camundaapi.JobLeaseToken(job.leaseToken))
		}
		var resp *http.Response
		resp, err = c.raw.JobAPI.FailJob(ctx, camundaapi.JobKey(job.key)).
			JobFailRequest(*req).Execute()
		err = c.wrapError(resp, err)
	} else {
		var body []byte
		body, err = json.Marshal(jobFailBody{Retries: retries, ErrorMessage: msg, JobLeaseToken: job.leaseToken})
		if err == nil {
			err = c.postJobAck(ctx, "JobAPIService.FailJob", job.key, "failure", body)
		}
	}
	if err != nil {
		c.logJobCommandFailure("fail job", job.Key(), classifyRESTJobCommandErr(err), err)
	}
}

func (c *CamundaClient) restThrowError(ctx context.Context, job *Job, bpmn *BpmnError) {
	var err error
	if c.useGeneratedAck() {
		req := camundaapi.NewJobErrorRequest(bpmn.Code)
		if bpmn.Message != "" {
			req.SetErrorMessage(bpmn.Message)
		}
		if len(bpmn.Variables) > 0 {
			req.SetVariables(bpmn.Variables)
		}
		if job.leaseToken != "" {
			req.SetJobLeaseToken(camundaapi.JobLeaseToken(job.leaseToken))
		}
		var resp *http.Response
		resp, err = c.raw.JobAPI.ThrowJobError(ctx, camundaapi.JobKey(job.key)).
			JobErrorRequest(*req).Execute()
		err = c.wrapError(resp, err)
	} else {
		b := jobErrorBody{ErrorCode: bpmn.Code, JobLeaseToken: job.leaseToken}
		if bpmn.Message != "" {
			b.ErrorMessage = &bpmn.Message
		}
		if len(bpmn.Variables) > 0 {
			b.Variables = bpmn.Variables
		}
		var body []byte
		body, err = json.Marshal(b)
		if err == nil {
			err = c.postJobAck(ctx, "JobAPIService.ThrowJobError", job.key, "error", body)
		}
	}
	if err != nil {
		c.logJobCommandFailure("throw job error", job.Key(), classifyRESTJobCommandErr(err), err)
	}
}

// defaultTenantIDs returns the single-element tenant slice a job worker
// activates for: the configured default tenant id, or DefaultTenantIdentifier
// when none is configured — matching the Camunda Java client, whose
// defaultJobWorkerTenantIds defaults to ["<default>"].
func defaultTenantIDs(id string) []string {
	if id == "" {
		return []string{DefaultTenantIdentifier}
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
