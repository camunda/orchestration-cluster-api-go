package camunda

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
	"github.com/camunda/orchestration-cluster-api-go/v11/internal/auth"
	"github.com/camunda/orchestration-cluster-api-go/v11/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// oauthPerRPC attaches an OAuth bearer token to each gRPC call.
type oauthPerRPC struct {
	ts     *auth.TokenSource
	secure bool
}

func (o *oauthPerRPC) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	tok, err := o.ts.Token(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"authorization": "Bearer " + tok}, nil
}

func (o *oauthPerRPC) RequireTransportSecurity() bool { return o.secure }

// grpcConn dials the Zeebe gRPC gateway using the client's transport-security and
// auth configuration. Transport security is enabled when mTLS material is
// configured or the REST address is https; otherwise the connection is plaintext.
func (c *CamundaClient) grpcConn() (*grpc.ClientConn, error) {
	tlsConf, err := buildTLSConfig(c.cfg.TLS)
	if err != nil {
		return nil, err
	}
	var creds credentials.TransportCredentials
	secure := false
	switch {
	case tlsConf != nil:
		creds = credentials.NewTLS(tlsConf)
		secure = true
	case strings.HasPrefix(strings.ToLower(c.cfg.RestAddress), "https"):
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
		secure = true
	default:
		creds = insecure.NewCredentials()
	}

	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds), grpc.WithUserAgent(userAgent(c.cfg))}
	if c.cfg.AuthStrategy == AuthOAuth {
		ts := auth.NewTokenSource(auth.OAuthConfig{
			TokenURL:     c.cfg.OAuthURL,
			ClientID:     c.cfg.ClientID,
			ClientSecret: c.cfg.ClientSecret,
			Audience:     c.cfg.TokenAudience,
			Scope:        c.cfg.OAuthScope,
			CacheDir:     c.cfg.OAuthCacheDir,
			UserAgent:    userAgent(c.cfg),
		}, c.clock)
		opts = append(opts, grpc.WithPerRPCCredentials(&oauthPerRPC{ts: ts, secure: secure}))
	}
	return grpc.NewClient(c.cfg.GrpcAddress, opts...)
}

// StreamJobWorker activates jobs over the Zeebe gRPC StreamActivatedJobs stream
// and completes (with any job result, see CompletionHandler), fails, or throws
// BPMN errors over gRPC. Unlike the REST JobWorker it does not poll: the engine
// pushes jobs as they become available.
type StreamJobWorker struct {
	client           *CamundaClient
	jobType          string
	handler          JobHandler
	name             string
	maxConcurrent    int
	timeout          time.Duration
	fetchVariables   []string
	reconnectBackoff time.Duration
	pollInterval     time.Duration
	pollMaxJobs      int
	pollBackoff      StreamPollBackoff
	tenantIDs        []string
	withLease        bool
	// streamUp reports whether a gRPC stream is currently open. The sidecar poll
	// only backs off while it is: during a stream outage the poll is the only
	// delivery channel and must run at the base interval.
	streamUp atomic.Bool
	// streamChanged is signalled (non-blocking, capacity 1) whenever streamUp
	// flips, so a sidecar sleeping out a backed-off delay wakes immediately and
	// polls again from the base interval.
	streamChanged chan struct{}
	// maxLifetime and inactivityTimeout bound each stream; <= 0 disables. See
	// WithStreamMaxLifetime and WithStreamInactivityTimeout.
	maxLifetime       time.Duration
	inactivityTimeout time.Duration
	// startupDelay is drawn once, when the worker is built; see startupJitter.
	startupDelay time.Duration

	// dial is an injectable seam for tests; nil means use client.grpcConn.
	dial func(ctx context.Context) (*grpc.ClientConn, error)
}

// StreamWorkerOption customizes a StreamJobWorker.
type StreamWorkerOption func(*StreamJobWorker)

// WithStreamWorkerName sets the worker name reported to the engine.
func WithStreamWorkerName(name string) StreamWorkerOption {
	return func(w *StreamJobWorker) { w.name = name }
}

// WithStreamMaxConcurrentJobs caps the number of jobs handled concurrently.
func WithStreamMaxConcurrentJobs(n int) StreamWorkerOption {
	return func(w *StreamJobWorker) {
		if n > 0 {
			w.maxConcurrent = n
		}
	}
}

// WithStreamJobTimeout sets how long a streamed job is exclusively locked to this worker.
func WithStreamJobTimeout(d time.Duration) StreamWorkerOption {
	return func(w *StreamJobWorker) { w.timeout = d }
}

// WithStreamFetchVariables restricts the variables fetched with each job. Empty fetches all.
func WithStreamFetchVariables(vars ...string) StreamWorkerOption {
	return func(w *StreamJobWorker) { w.fetchVariables = vars }
}

// WithStreamReconnectBackoff sets the pause before reopening the stream after it ends.
func WithStreamReconnectBackoff(d time.Duration) StreamWorkerOption {
	return func(w *StreamJobWorker) { w.reconnectBackoff = d }
}

// WithStreamPollInterval sets the interval between REST sidecar-poll cycles. The
// sidecar poll is a low-frequency safety net that picks up jobs the stream may
// have missed (e.g. jobs re-queued after a timeout or during a brief reconnect).
// A value <= 0 disables the sidecar poll entirely (pure gRPC streaming).
func WithStreamPollInterval(d time.Duration) StreamWorkerOption {
	return func(w *StreamJobWorker) { w.pollInterval = d }
}

// StreamPollBackoff configures how the REST sidecar poll backs off while it
// keeps coming back empty (Camunda Java client parity). After a poll that
// activates no jobs, the next delay is the current delay × Multiplier, capped at
// Max, then jittered by ±Jitter (a fraction of the delay) and clamped to Max
// again. Backoff applies only while the gRPC stream is open; while it is
// disconnected the poll runs at the poll interval. A poll that activates
// at least one job resets the delay to the poll interval (WithStreamPollInterval).
// A failed poll keeps the current delay.
//
// A Multiplier <= 1 disables the backoff: the poll then runs at the fixed poll
// interval regardless of the result.
type StreamPollBackoff struct {
	Multiplier float64
	Jitter     float64
	Max        time.Duration
}

// DefaultStreamPollBackoff is the sidecar-poll empty-result backoff the Camunda
// Java client uses: ×1.6, ±10% jitter, capped at 60 s.
var DefaultStreamPollBackoff = StreamPollBackoff{Multiplier: 1.6, Jitter: 0.1, Max: time.Minute}

// WithStreamPollBackoff sets the empty-poll backoff of the REST sidecar poll.
// Defaults to DefaultStreamPollBackoff; pass StreamPollBackoff{} to disable it
// and poll at the fixed WithStreamPollInterval.
func WithStreamPollBackoff(b StreamPollBackoff) StreamWorkerOption {
	return func(w *StreamJobWorker) { w.pollBackoff = b }
}

// nextDelay returns the delay before the next sidecar poll, given the delay
// that preceded the poll just made and how many jobs it activated. base is the
// configured poll interval; the result is never below it.
func (b StreamPollBackoff) nextDelay(current, base time.Duration, activated int, r Random) time.Duration {
	if activated > 0 || b.Multiplier <= 1 {
		return base
	}
	next := float64(current) * b.Multiplier
	if b.Max > 0 && next > float64(b.Max) {
		next = float64(b.Max)
	}
	if b.Jitter > 0 && r != nil {
		next += next * b.Jitter * (r.Float64()*2 - 1)
	}
	if b.Max > 0 && next > float64(b.Max) {
		next = float64(b.Max)
	}
	d := time.Duration(next)
	if d < base {
		d = base
	}
	return d
}

// sidecarDelay returns the next sidecar-poll delay. While the gRPC stream is
// down the poll is the only delivery channel, so it runs at the base interval
// instead of backing off.
func (w *StreamJobWorker) sidecarDelay(current time.Duration, activated int, streamUp bool) time.Duration {
	if !streamUp {
		return w.pollInterval
	}
	return w.pollBackoff.nextDelay(current, w.pollInterval, activated, w.client.random)
}

// WithStreamPollMaxJobs caps the number of jobs activated per REST sidecar-poll cycle.
func WithStreamPollMaxJobs(n int) StreamWorkerOption {
	return func(w *StreamJobWorker) {
		if n > 0 {
			w.pollMaxJobs = n
		}
	}
}

// WithStreamTenantIDs restricts job activation to the given tenant ids,
// overriding the client's default tenant.
func WithStreamTenantIDs(ids ...string) StreamWorkerOption {
	return func(w *StreamJobWorker) { w.tenantIDs = ids }
}

// WithStreamJobLease requests leased jobs. Each job then carries a distinct
// lease token, which this worker sends back on complete, fail, and throw-error.
// The engine rejects a command bearing a stale token, fencing the job against a
// superseded activation — for example after the job timed out and another worker
// picked it up.
//
// Off by default, matching the gateway's own default. Enabling it requires an
// engine that supports job leases: a gateway that ignores the field would leave
// every acknowledgement unfenced, so a job arriving without a token fails the
// activation with ErrLeaseNotHonored rather than being handled unfenced. It
// covers both channels: the gRPC stream and the REST sidecar poll (see
// WithStreamPollInterval).
func WithStreamJobLease(enabled bool) StreamWorkerOption {
	return func(w *StreamJobWorker) { w.withLease = enabled }
}

// Defaults for the stream-lifecycle safeguards, matching the Camunda Java client.
const (
	DefaultStreamMaxLifetime       = 8 * time.Hour
	DefaultStreamInactivityTimeout = 10 * time.Minute
)

// streamDeadlineJitter is the fraction by which each stream's lifetime and
// inactivity deadlines are shortened at random: a deadline is drawn uniformly
// from [(1-streamDeadlineJitter)*d, d], so a fleet started together does not
// recycle all its streams in the same instant.
const streamDeadlineJitter = 0.1

// errStreamRecycled is the cancellation cause for a stream the worker recycles
// itself (max lifetime or inactivity). Run reopens such a stream immediately.
var errStreamRecycled = errors.New("job stream recycled")

// WithStreamMaxLifetime sets the maximum lifetime of one job stream (default
// DefaultStreamMaxLifetime, 8h). When it is reached the stream is closed and a
// new one opened at once, so streams rebalance across gateways after a scale-out
// or restart. Jobs already received keep running and are acknowledged normally.
// Each stream's deadline is jittered down by up to 10% to spread recycling
// across a fleet. A value <= 0 disables it.
//
// Rebalancing only helps when a new stream can reach a different gateway: true
// behind a request-level (L7) gRPC load balancer, but not behind a plain L4 load
// balancer, because the worker reuses its HTTP/2 connection. L4 deployments need
// DNS or client-side balancing to benefit.
func WithStreamMaxLifetime(d time.Duration) StreamWorkerOption {
	return func(w *StreamJobWorker) { w.maxLifetime = d }
}

// WithStreamInactivityTimeout sets how long a stream may go without delivering a
// job before it is canceled and reopened (default
// DefaultStreamInactivityTimeout, 10m). The timer resets on every received job,
// and is jittered down by up to 10%. It catches streams whose transport stays
// healthy but which no longer deliver jobs. A value <= 0 disables it. When both
// are enabled it must be less than the max lifetime, or Run returns an ErrConfig
// error.
func WithStreamInactivityTimeout(d time.Duration) StreamWorkerOption {
	return func(w *StreamJobWorker) { w.inactivityTimeout = d }
}

// jitterDeadline draws a deadline uniformly from [(1-streamDeadlineJitter)*d, d].
func jitterDeadline(r Random, d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d - time.Duration(r.Float64()*streamDeadlineJitter*float64(d))
}

// NewStreamJobWorker creates a gRPC streaming worker for jobType. Defaults are
// seeded from the client's CAMUNDA_WORKER_* configuration and can be overridden
// with options.
func (c *CamundaClient) NewStreamJobWorker(jobType string, handler JobHandler, opts ...StreamWorkerOption) *StreamJobWorker {
	wd := c.cfg.WorkerDefaults
	w := &StreamJobWorker{
		client:           c,
		jobType:          jobType,
		handler:          handler,
		name:             wd.Name,
		maxConcurrent:    wd.MaxConcurrentJobs,
		timeout:          time.Duration(wd.TimeoutMs) * time.Millisecond,
		reconnectBackoff: time.Second,
		streamChanged:    make(chan struct{}, 1),
		pollInterval:     30 * time.Second,
		pollMaxJobs:      32,
		pollBackoff:      DefaultStreamPollBackoff,
		tenantIDs:        defaultTenantIDs(c.cfg.DefaultTenantID),

		maxLifetime:       DefaultStreamMaxLifetime,
		inactivityTimeout: DefaultStreamInactivityTimeout,
	}
	if w.maxConcurrent <= 0 {
		w.maxConcurrent = 1
	}
	for _, o := range opts {
		o(w)
	}
	c.registerWorkerCapacity(w.maxConcurrent)
	w.startupDelay = startupJitter(c.random, wd.StartupJitterMaxSeconds)
	return w
}

// Run opens the job stream and dispatches jobs until ctx is canceled. The gRPC
// connection is held for the worker's lifetime and the stream is reopened (after
// reconnectBackoff) whenever it ends, so in-flight acknowledgements are never cut
// off by a reconnect. Run blocks; call it in a goroutine to run alongside other
// work.
//
// A worker configured with a startup jitter (CAMUNDA_WORKER_STARTUP_JITTER_MAX_SECONDS)
// first waits out its delay on the client's clock.
func (w *StreamJobWorker) Run(ctx context.Context) error {
	if w.maxLifetime > 0 && w.inactivityTimeout > 0 && w.inactivityTimeout >= w.maxLifetime {
		return configErrorf("stream inactivity timeout (%s) must be less than the max stream lifetime (%s)",
			w.inactivityTimeout, w.maxLifetime)
	}
	if err := waitStartup(ctx, w.client.clock, w.startupDelay); err != nil {
		return err
	}
	dial := w.dial
	if dial == nil {
		dial = func(context.Context) (*grpc.ClientConn, error) { return w.client.grpcConn() }
	}
	conn, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	gw := pb.NewGatewayClient(conn)
	sem := make(chan struct{}, w.maxConcurrent)
	var wg sync.WaitGroup

	// Sidecar poll: a low-frequency REST safety net running alongside the stream,
	// sharing the same concurrency limit. Poll-activated jobs are acknowledged
	// over REST (they carry no gRPC lease token); the broker guarantees single
	// activation, so a job is never delivered by both the stream and the poll.
	if w.pollInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.runSidecarPoll(ctx, sem, &wg)
		}()
	}

	for ctx.Err() == nil {
		streamErr := w.streamOnce(ctx, gw, sem, &wg)
		if ctx.Err() != nil {
			break
		}
		if errors.Is(streamErr, errStreamRecycled) {
			// A deliberate recycle: reopen at once, with no backoff.
			continue
		}
		if streamErr != nil {
			w.client.logger.Warn("job stream ended; reconnecting", "type", w.jobType, "error", streamErr)
		}
		if err := sleepCtx(ctx, w.client.clock, w.reconnectBackoff); err != nil {
			break
		}
	}

	wg.Wait()
	return ctx.Err()
}

// streamOnce opens a single StreamActivatedJobs stream and dispatches jobs until
// the stream ends or ctx is canceled. It returns the stream's terminating error
// (nil on a clean close).
func (w *StreamJobWorker) streamOnce(ctx context.Context, gw pb.GatewayClient, sem chan struct{}, wg *sync.WaitGroup) error {
	req := &pb.StreamActivatedJobsRequest{
		Type:      w.jobType,
		Worker:    w.name,
		Timeout:   w.timeout.Milliseconds(),
		WithLease: w.withLease,
	}
	if len(w.fetchVariables) > 0 {
		req.FetchVariable = w.fetchVariables
	}
	if len(w.tenantIDs) > 0 {
		req.TenantIds = w.tenantIDs
	}

	// The RPC outlives this call unless its context is canceled: returning while the
	// stream is still healthy (a rejected lease) would leave the gateway holding it
	// open and delivering onto it while Run reconnects. Handlers keep the worker's
	// ctx, so canceling this one does not disturb jobs already dispatched.
	streamCtx, cancelStream := context.WithCancelCause(ctx)
	defer cancelStream(nil)

	stream, err := gw.StreamActivatedJobs(streamCtx, req)
	if err != nil {
		return err
	}
	// A deliberate recycle (max lifetime / inactivity) is not an outage: the
	// sidecar-visible stream state stays up across it, so a healthy recycle
	// neither wakes a backed-off poll nor resets its delay. Only a real
	// disconnect -- a failed open or a stream that ends for any other reason --
	// flips it down (and back up once a new stream actually opens).
	if !w.streamUp.Load() {
		w.setStreamUp(true)
	}
	defer func() {
		if !errors.Is(context.Cause(streamCtx), errStreamRecycled) {
			w.setStreamUp(false)
		}
	}()

	activity := make(chan struct{}, 1)
	watchDone := make(chan struct{})
	// Cancel before waiting: the watcher only returns once the stream ends.
	defer func() { cancelStream(nil); <-watchDone }()
	go func() {
		defer close(watchDone)
		w.watchStream(streamCtx, cancelStream, activity)
	}()

	for {
		aj, err := stream.Recv()
		if err != nil {
			if cause := context.Cause(streamCtx); errors.Is(cause, errStreamRecycled) && ctx.Err() == nil {
				return cause
			}
			return err
		}
		select {
		case activity <- struct{}{}:
		default:
		}
		// Ending the stream rather than skipping the job: the reconnect loop logs and
		// backs off, where skipping would spin silently on every job the server sends.
		if err := requireLeasePresence(w.withLease, aj.GetJobLeaseToken()); err != nil {
			return err
		}
		job := newGRPCJob(aj, w.client.clock)
		// A deliberately recycled stream must not wait on a free handler slot to
		// reopen: with every slot busy, parking here only on the worker context
		// would miss the recycle -- the watchdog cancels streamCtx and returns,
		// so nothing wakes this select until a handler finishes, and with a
		// permanently parked handler the stream would never be recycled nor
		// reopened. Select on streamCtx too, so the recycle interrupts the wait
		// and Run reopens the stream at once; the received job is not lost,
		// because the gateway requeues an activated job that is never
		// acknowledged once its lock expires.
		select {
		case sem <- struct{}{}:
		case <-streamCtx.Done():
			if cause := context.Cause(streamCtx); errors.Is(cause, errStreamRecycled) && ctx.Err() == nil {
				return cause
			}
			return streamCtx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			w.handle(ctx, gw, job)
		}()
	}
}

// watchStream enforces the stream's max lifetime and inactivity timeout,
// canceling the stream with errStreamRecycled when either expires. It returns
// when the stream context ends. Each received job is signaled on activity.
//
// The deadlines are real time, not the injected clock: they bound a transport,
// so they must pass in wall time even for a client running on an EngineClock,
// whose After would advance engine time instead of waiting.
func (w *StreamJobWorker) watchStream(ctx context.Context, cancel context.CancelCauseFunc, activity <-chan struct{}) {
	rnd := w.client.random
	var lifetimeC <-chan time.Time
	if d := jitterDeadline(rnd, w.maxLifetime); d > 0 {
		lifetimeC = time.After(d) //nolint:forbidigo // a transport bound, not cadence: engine time would misfire it
	}
	// One inactivity timer per stream, reset on activity: the receive path is
	// hot, so it must not allocate a fresh timer per job.
	var idle *time.Timer
	var idleC <-chan time.Time
	if d := jitterDeadline(rnd, w.inactivityTimeout); d > 0 {
		idle = time.NewTimer(d) //nolint:forbidigo // a transport bound, not cadence: engine time would misfire it
		defer idle.Stop()
		idleC = idle.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-activity:
			// A job arrived: restart the inactivity timer. Since Go 1.23 Reset
			// on an unstopped timer discards any stale pending value.
			if idle != nil {
				idle.Reset(jitterDeadline(rnd, w.inactivityTimeout))
			}
		case <-lifetimeC:
			w.client.logger.Debug("job stream reached max lifetime; recycling", "type", w.jobType)
			cancel(fmt.Errorf("%w: max lifetime reached", errStreamRecycled))
			return
		case <-idleC:
			w.client.logger.Debug("job stream inactive; recycling", "type", w.jobType)
			cancel(fmt.Errorf("%w: inactivity timeout", errStreamRecycled))
			return
		}
	}
}

// runSidecarPoll runs an immediate backfill poll and then a recurring poll until
// ctx is canceled. The delay starts at pollInterval, grows by pollBackoff while
// polls come back empty while the stream is open, and resets to pollInterval when a poll activates jobs or the stream is down. Poll errors are swallowed (the stream is
// the primary channel); the next cycle retries. Poll-activated jobs are
// acknowledged over REST.
func (w *StreamJobWorker) runSidecarPoll(ctx context.Context, sem chan struct{}, wg *sync.WaitGroup) {
	delay := w.pollInterval
	for ctx.Err() == nil {
		jobs, err := w.pollOnce(ctx)
		if err != nil && ctx.Err() == nil {
			if errors.Is(err, ErrLeaseNotHonored) {
				// The documented fail-loud path. An ordinary sidecar failure stays at
				// Debug because the stream is the primary channel and the next cycle
				// retries, but a lease-incompatible server rejects every poll forever,
				// so it must be visible at the default level like the other two paths.
				w.client.logger.Warn("sidecar poll rejected: server does not honor job leases",
					"type", w.jobType, "error", err)
			} else {
				w.client.logger.Debug("sidecar poll failed", "type", w.jobType, "error", err)
			}
		}
		for i := range jobs {
			job := newRESTJob(jobs[i], w.client.clock)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				vars, herr := w.client.invokeHandler(ctx, w.handler, job)
				ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				w.client.restAck(ackCtx, job, vars, herr)
			}()
		}
		if err == nil {
			delay = w.sidecarDelay(delay, len(jobs), w.streamUp.Load())
		} else if !w.streamUp.Load() {
			delay = w.pollInterval
		}
		start := w.client.clock.Now()
		woke, err := w.sidecarSleep(ctx, delay)
		if err != nil {
			return
		}
		if woke {
			// Stream state changed mid-sleep: a disconnect makes the poll the only
			// channel, a reconnect starts a fresh backoff. Either way, restart from
			// the base interval — but never poll sooner than pollInterval after the
			// previous poll, so a flapping stream cannot drive REST polls at the
			// reconnect cadence.
			delay = w.pollInterval
			if rest := w.pollInterval - w.client.clock.Now().Sub(start); rest > 0 {
				if sleepCtx(ctx, w.client.clock, rest) != nil {
					return
				}
			}
		}
	}
}

// setStreamUp records the stream state and wakes the sidecar poll if it changed.
func (w *StreamJobWorker) setStreamUp(up bool) {
	if w.streamUp.Swap(up) == up {
		return
	}
	select {
	case w.streamChanged <- struct{}{}:
	default:
	}
}

// sidecarSleep sleeps for d, returning early with woke=true when the stream
// state changes. It returns ctx's error if ctx is done.
func (w *StreamJobWorker) sidecarSleep(ctx context.Context, d time.Duration) (woke bool, err error) {
	sleepCtx2, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-w.streamChanged:
			woke = true
			cancel()
		case <-sleepCtx2.Done():
		}
	}()
	serr := sleepCtx(sleepCtx2, w.client.clock, d)
	cancel()
	<-done
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if woke {
		return true, nil
	}
	return false, serr
}

// pollOnce activates up to pollMaxJobs jobs over REST.
func (w *StreamJobWorker) pollOnce(ctx context.Context) ([]camundaapi.ActivatedJobResult, error) {
	req := camundaapi.NewJobActivationRequest(w.jobType, w.timeout.Milliseconds(), int32(w.pollMaxJobs))
	if w.name != "" {
		req.SetWorker(w.name)
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

func (w *StreamJobWorker) handle(ctx context.Context, gw pb.GatewayClient, job *Job) {
	vars, err := w.client.invokeHandler(ctx, w.handler, job)

	// Acknowledge even while shutting down so a completed handler's result is not
	// dropped because Run's ctx was canceled. Bound the ack with its own timeout.
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	var result *pb.JobResult
	if err == nil {
		var r *JobResult
		if r, err = completionResult(job, vars); err == nil {
			result, err = grpcJobResult(r)
		}
	}
	if err != nil {
		var bpmn *BpmnError
		if errors.As(err, &bpmn) {
			w.throwError(ackCtx, gw, job, bpmn)
		} else {
			w.failJob(ackCtx, gw, job, err)
		}
		return
	}
	w.completeJob(ackCtx, gw, job, vars, result)
}

func (w *StreamJobWorker) completeJob(ctx context.Context, gw pb.GatewayClient, job *Job, vars map[string]any, result *pb.JobResult) {
	key, err := strconv.ParseInt(job.key, 10, 64)
	if err != nil {
		w.client.logger.Error("complete job failed: invalid key", "job", job.key, "error", err)
		return
	}
	req := &pb.CompleteJobRequest{JobKey: key}
	if len(vars) > 0 {
		if b, err := json.Marshal(vars); err == nil {
			req.Variables = string(b)
		}
	}
	req.Result = result
	if job.leaseToken != "" {
		req.JobLeaseToken = &job.leaseToken
	}
	if _, err := gw.CompleteJob(ctx, req); err != nil {
		w.client.logger.Error("complete job failed", "job", job.key, "error", err)
	}
}

func (w *StreamJobWorker) failJob(ctx context.Context, gw pb.GatewayClient, job *Job, cause error) {
	key, err := strconv.ParseInt(job.key, 10, 64)
	if err != nil {
		w.client.logger.Error("fail job failed: invalid key", "job", job.key, "error", err)
		return
	}
	retries := job.retries - 1
	if retries < 0 {
		retries = 0
	}
	req := &pb.FailJobRequest{JobKey: key, Retries: retries}
	if cause != nil {
		req.ErrorMessage = cause.Error()
	}
	if job.leaseToken != "" {
		req.JobLeaseToken = &job.leaseToken
	}
	if _, err := gw.FailJob(ctx, req); err != nil {
		w.client.logger.Error("fail job failed", "job", job.key, "error", err)
	}
}

func (w *StreamJobWorker) throwError(ctx context.Context, gw pb.GatewayClient, job *Job, bpmn *BpmnError) {
	key, err := strconv.ParseInt(job.key, 10, 64)
	if err != nil {
		w.client.logger.Error("throw job error failed: invalid key", "job", job.key, "error", err)
		return
	}
	req := &pb.ThrowErrorRequest{JobKey: key, ErrorCode: bpmn.Code, ErrorMessage: bpmn.Message}
	if len(bpmn.Variables) > 0 {
		if b, err := json.Marshal(bpmn.Variables); err == nil {
			req.Variables = string(b)
		}
	}
	if job.leaseToken != "" {
		req.JobLeaseToken = &job.leaseToken
	}
	if _, err := gw.ThrowError(ctx, req); err != nil {
		w.client.logger.Error("throw job error failed", "job", job.key, "error", err)
	}
}
