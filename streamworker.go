package camunda

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/internal/auth"
	"github.com/camunda/orchestration-cluster-api-go/pb"
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

	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	if c.cfg.AuthStrategy == AuthOAuth {
		ts := auth.NewTokenSource(auth.OAuthConfig{
			TokenURL:     c.cfg.OAuthURL,
			ClientID:     c.cfg.ClientID,
			ClientSecret: c.cfg.ClientSecret,
			Audience:     c.cfg.TokenAudience,
			Scope:        c.cfg.OAuthScope,
			CacheDir:     c.cfg.OAuthCacheDir,
		})
		opts = append(opts, grpc.WithPerRPCCredentials(&oauthPerRPC{ts: ts, secure: secure}))
	}
	return grpc.NewClient(c.cfg.GrpcAddress, opts...)
}

// StreamJobWorker activates jobs over the Zeebe gRPC StreamActivatedJobs stream
// and completes, fails, or throws BPMN errors over gRPC. Unlike the REST
// JobWorker it does not poll: the engine pushes jobs as they become available.
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

// WithStreamPollMaxJobs caps the number of jobs activated per REST sidecar-poll cycle.
func WithStreamPollMaxJobs(n int) StreamWorkerOption {
	return func(w *StreamJobWorker) {
		if n > 0 {
			w.pollMaxJobs = n
		}
	}
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
		pollInterval:     30 * time.Second,
		pollMaxJobs:      32,
	}
	if w.maxConcurrent <= 0 {
		w.maxConcurrent = 1
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Run opens the job stream and dispatches jobs until ctx is cancelled. The gRPC
// connection is held for the worker's lifetime and the stream is reopened (after
// reconnectBackoff) whenever it ends, so in-flight acknowledgements are never cut
// off by a reconnect. Run blocks; call it in a goroutine to run alongside other
// work.
func (w *StreamJobWorker) Run(ctx context.Context) error {
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
		if streamErr != nil {
			w.client.logger.Warn("job stream ended; reconnecting", "type", w.jobType, "error", streamErr)
		}
		if err := sleepCtx(ctx, w.reconnectBackoff); err != nil {
			break
		}
	}

	wg.Wait()
	return ctx.Err()
}

// streamOnce opens a single StreamActivatedJobs stream and dispatches jobs until
// the stream ends or ctx is cancelled. It returns the stream's terminating error
// (nil on a clean close).
func (w *StreamJobWorker) streamOnce(ctx context.Context, gw pb.GatewayClient, sem chan struct{}, wg *sync.WaitGroup) error {
	req := &pb.StreamActivatedJobsRequest{
		Type:    w.jobType,
		Worker:  w.name,
		Timeout: w.timeout.Milliseconds(),
	}
	if len(w.fetchVariables) > 0 {
		req.FetchVariable = w.fetchVariables
	}

	stream, err := gw.StreamActivatedJobs(ctx, req)
	if err != nil {
		return err
	}

	for {
		aj, err := stream.Recv()
		if err != nil {
			return err
		}
		job := newGRPCJob(aj)
		select {
		case sem <- struct{}{}:
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

// runSidecarPoll runs an immediate backfill poll and then a recurring poll every
// pollInterval until ctx is cancelled. Poll errors are swallowed (the stream is
// the primary channel); the next cycle retries. Poll-activated jobs are
// acknowledged over REST.
func (w *StreamJobWorker) runSidecarPoll(ctx context.Context, sem chan struct{}, wg *sync.WaitGroup) {
	for ctx.Err() == nil {
		jobs, err := w.pollOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.client.logger.Debug("sidecar poll failed", "type", w.jobType, "error", err)
		}
		for i := range jobs {
			job := newRESTJob(jobs[i])
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				vars, herr := w.handler(ctx, job)
				ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				w.client.restAck(ackCtx, job, vars, herr)
			}()
		}
		if err := sleepCtx(ctx, w.pollInterval); err != nil {
			return
		}
	}
}

// pollOnce activates up to pollMaxJobs jobs over REST.
func (w *StreamJobWorker) pollOnce(ctx context.Context) ([]openapi.ActivatedJobResult, error) {
	req := openapi.NewJobActivationRequest(w.jobType, w.timeout.Milliseconds(), int32(w.pollMaxJobs))
	if w.name != "" {
		req.SetWorker(w.name)
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

func (w *StreamJobWorker) handle(ctx context.Context, gw pb.GatewayClient, job *Job) {
	vars, err := w.handler(ctx, job)

	// Acknowledge even while shutting down so a completed handler's result is not
	// dropped because Run's ctx was cancelled. Bound the ack with its own timeout.
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err != nil {
		var bpmn *BpmnError
		if errors.As(err, &bpmn) {
			w.throwError(ackCtx, gw, job, bpmn)
		} else {
			w.failJob(ackCtx, gw, job, err)
		}
		return
	}
	w.completeJob(ackCtx, gw, job, vars)
}

func (w *StreamJobWorker) completeJob(ctx context.Context, gw pb.GatewayClient, job *Job, vars map[string]any) {
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
	if job.leaseToken != "" {
		req.LeaseToken = &job.leaseToken
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
		req.LeaseToken = &job.leaseToken
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
		req.LeaseToken = &job.leaseToken
	}
	if _, err := gw.ThrowError(ctx, req); err != nil {
		w.client.logger.Error("throw job error failed", "job", job.key, "error", err)
	}
}
