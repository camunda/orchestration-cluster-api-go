package falcon

import (
	"context"
	"encoding/json"
	"time"
)

// SubscribeArgs configures a command-stream job subscription.
type SubscribeArgs struct {
	JobType        string
	JobCredits     int64
	FetchVariables []string
	TimeoutMs      int64
	Worker         string
}

// StreamWorker is a push-based job worker over a single (failover-capable)
// command-stream socket. It subscribes to one job type with an initial credit
// batch; the gateway pushes matching jobs (each consuming a delivery credit).
// After acting on a job the caller replenishes one credit, keeping in-flight work
// bounded by the initial window.
type StreamWorker struct {
	link    *SupervisedLink
	jobs    chan json.RawMessage
	jobType string
}

// Subscribe connects over the failover directory endpoints (≥1), subscribes to
// args.JobType, and starts buffering pushed jobs. On failover the subscription is
// automatically re-sent to the survivor.
func Subscribe(endpoints []string, d *Dialer, args SubscribeArgs) (*StreamWorker, error) {
	w := &StreamWorker{jobs: make(chan json.RawMessage, 256), jobType: args.JobType}

	onFrame := func(raw []byte) {
		var f struct {
			Type string          `json:"type"`
			Job  json.RawMessage `json:"job"`
		}
		if json.Unmarshal(raw, &f) != nil || f.Type != "job" || len(f.Job) == 0 {
			return
		}
		select {
		case w.jobs <- f.Job:
		default:
			// Buffer full: drop rather than block the reader. The delivery-credit
			// window bounds in-flight jobs, so this should not happen in practice.
		}
	}

	sub := map[string]any{
		"type":       "subscribe",
		"jobType":    args.JobType,
		"jobCredits": args.JobCredits,
	}
	if len(args.FetchVariables) > 0 {
		sub["fetchVariable"] = args.FetchVariables
	}
	if args.TimeoutMs > 0 {
		sub["timeout"] = args.TimeoutMs
	}
	if args.Worker != "" {
		sub["worker"] = args.Worker
	}
	subFrame, _ := json.Marshal(sub)

	hooks := linkHooks{
		onFrame:      onFrame,
		onConnect:    func(send func([]byte)) { send(subFrame) },
		onDisconnect: func() {},
	}
	link, err := startLink(endpoints, d, hooks)
	if err != nil {
		return nil, err
	}
	w.link = link
	return w, nil
}

// NextJob waits up to wait for the next pushed job, returning (job, false) if none
// arrives in time.
func (w *StreamWorker) NextJob(ctx context.Context, wait time.Duration) (json.RawMessage, bool) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case job := <-w.jobs:
		return job, true
	case <-ctx.Done():
		return nil, false
	case <-timer.C:
		return nil, false
	}
}

// Replenish grants the gateway n more delivery credits for this worker's job type.
func (w *StreamWorker) Replenish(n int64) {
	frame, _ := json.Marshal(map[string]any{"type": "jobCredits", "jobType": w.jobType, "n": n})
	_ = w.link.send(frame)
}

// Complete completes a job (fire-and-forget) and replenishes one delivery credit.
func (w *StreamWorker) Complete(jobKey string, variables map[string]any) {
	frame := map[string]any{"type": "completeJob", "corr": 0, "jobKey": jobKey}
	if len(variables) > 0 {
		frame["variables"] = variables
	}
	payload, _ := json.Marshal(frame)
	_ = w.link.send(payload)
	w.Replenish(1)
}

// Fail fails a job (fire-and-forget) and replenishes one delivery credit.
func (w *StreamWorker) Fail(jobKey string, retries int32, errorMessage string) {
	frame := map[string]any{"type": "failJob", "corr": 0, "jobKey": jobKey, "retries": retries}
	if errorMessage != "" {
		frame["errorMessage"] = errorMessage
	}
	payload, _ := json.Marshal(frame)
	_ = w.link.send(payload)
	w.Replenish(1)
}

// ThrowError throws a BPMN error from a job (fire-and-forget) and replenishes one
// delivery credit.
func (w *StreamWorker) ThrowError(jobKey, errorCode, errorMessage string, variables map[string]any) {
	frame := map[string]any{"type": "throwError", "corr": 0, "jobKey": jobKey, "errorCode": errorCode}
	if errorMessage != "" {
		frame["errorMessage"] = errorMessage
	}
	if len(variables) > 0 {
		frame["variables"] = variables
	}
	payload, _ := json.Marshal(frame)
	_ = w.link.send(payload)
	w.Replenish(1)
}

// Close tears down the worker's link.
func (w *StreamWorker) Close() {
	if w.link != nil {
		w.link.close()
	}
}
