package falcon

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// createAckTimeout bounds the wait for a create CommandResult before giving up.
const createAckTimeout = 30 * time.Second

// RemoteError is a non-2xx command result returned by the gateway for a create.
// The root package maps it into the SDK's public *APIError.
type RemoteError struct {
	Status int
	Body   string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("camunda: falcon create failed with status %d: %s", e.Status, e.Body)
}

// CreateArgs are the fields the command-stream create understands (a definition
// id OR key, plus optional variables and await/fetch/timeout controls).
type CreateArgs struct {
	ProcessDefinitionID  string
	ProcessDefinitionKey string
	Variables            map[string]any
	AwaitCompletion      bool
	FetchVariables       []string
	RequestTimeoutMs     int64
}

// CreateOutcome is the result of a create over the command stream.
type CreateOutcome struct {
	ProcessInstanceKey string
	ProcessCompleted   bool
	// Variables is populated only for awaitCompletion creates.
	Variables map[string]any
	// Body is the raw commandResult body the gateway returned, so callers can
	// build a REST-equivalent result from whatever fields it carries.
	Body json.RawMessage
}

type ackResult struct {
	status int
	body   json.RawMessage
}

type awaitResult struct {
	completed bool
	variables map[string]any
}

// Producer is a persistent, credit-metered create producer over a single
// (failover-capable) command-stream socket. Creation is gated on the
// server-granted submission-credit window: when credits are exhausted, Create
// waits (no 503, no client-side retry) until the gateway replenishes.
type Producer struct {
	link     *SupervisedLink
	nextCorr atomic.Uint64

	mu        sync.Mutex
	credits   int64
	creditCh  chan struct{} // broadcast: closed on replenish, recreated under mu
	pending   map[uint64]chan ackResult
	awaitPend map[uint64]chan awaitResult
}

// StartProducer connects a producer over the failover directory endpoints (≥1).
func StartProducer(endpoints []string, d *Dialer) (*Producer, error) {
	p := &Producer{
		creditCh:  make(chan struct{}),
		pending:   map[uint64]chan ackResult{},
		awaitPend: map[uint64]chan awaitResult{},
	}
	hooks := linkHooks{
		onFrame:      p.onFrame,
		onConnect:    func(func([]byte)) {}, // each Welcome re-grants a fresh credit window
		onDisconnect: p.onDisconnect,
	}
	link, err := startLink(endpoints, d, hooks)
	if err != nil {
		return nil, err
	}
	p.link = link
	return p, nil
}

// Close tears down the producer's link.
func (p *Producer) Close() {
	if p.link != nil {
		p.link.close()
	}
}

func (p *Producer) addCredits(n int64) {
	p.mu.Lock()
	p.credits += n
	old := p.creditCh
	p.creditCh = make(chan struct{})
	p.mu.Unlock()
	close(old)
}

func (p *Producer) onFrame(raw []byte) {
	var f struct {
		Type              string          `json:"type"`
		Corr              uint64          `json:"corr"`
		Status            int             `json:"status"`
		Body              json.RawMessage `json:"body"`
		SubmissionCredits int64           `json:"submissionCredits"`
		N                 int64           `json:"n"`
		ProcessCompleted  bool            `json:"processCompleted"`
		Variables         map[string]any  `json:"variables"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	switch f.Type {
	case "welcome":
		if f.SubmissionCredits > 0 {
			p.addCredits(f.SubmissionCredits)
		}
	case "submissionCredits":
		if f.N != 0 {
			p.addCredits(f.N)
		}
	case "commandResult":
		p.mu.Lock()
		ch := p.pending[f.Corr]
		delete(p.pending, f.Corr)
		p.mu.Unlock()
		if ch != nil {
			status := f.Status
			if status == 0 {
				status = 500
			}
			ch <- ackResult{status: status, body: f.Body}
		}
	case "instanceCompleted":
		p.mu.Lock()
		ch := p.awaitPend[f.Corr]
		delete(p.awaitPend, f.Corr)
		p.mu.Unlock()
		if ch != nil {
			ch <- awaitResult{completed: f.ProcessCompleted, variables: f.Variables}
		}
	}
}

// onDisconnect resets the credit window (the next Welcome re-grants one) and fails
// every in-flight create promptly, so callers see an error and retry on the
// failed-over socket instead of blocking on a result that will never arrive.
func (p *Producer) onDisconnect() {
	p.mu.Lock()
	p.credits = 0
	pending := p.pending
	awaitPend := p.awaitPend
	p.pending = map[uint64]chan ackResult{}
	p.awaitPend = map[uint64]chan awaitResult{}
	p.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	for _, ch := range awaitPend {
		close(ch)
	}
}

// acquireCredit takes one submission credit, waiting for the gateway to replenish
// when exhausted. Registering interest (reading creditCh) happens under the same
// lock addCredits uses to swap it, so a replenish can never be lost between the
// credit check and the wait.
func (p *Producer) acquireCredit(ctx context.Context) error {
	for {
		p.mu.Lock()
		if p.credits > 0 {
			p.credits--
			p.mu.Unlock()
			return nil
		}
		ch := p.creditCh
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// Create starts a process instance over the command stream.
func (p *Producer) Create(ctx context.Context, args CreateArgs) (*CreateOutcome, error) {
	if err := p.acquireCredit(ctx); err != nil {
		return nil, err
	}
	corr := p.nextCorr.Add(1)

	ackCh := make(chan ackResult, 1)
	p.mu.Lock()
	p.pending[corr] = ackCh
	var awaitCh chan awaitResult
	if args.AwaitCompletion {
		awaitCh = make(chan awaitResult, 1)
		p.awaitPend[corr] = awaitCh
	}
	p.mu.Unlock()

	frame := map[string]any{
		"type":            "createInstance",
		"corr":            corr,
		"awaitCompletion": args.AwaitCompletion,
	}
	if args.ProcessDefinitionID != "" {
		frame["processDefinitionId"] = args.ProcessDefinitionID
	}
	if args.ProcessDefinitionKey != "" {
		frame["processDefinitionKey"] = args.ProcessDefinitionKey
	}
	if len(args.Variables) > 0 {
		frame["variables"] = args.Variables
	}
	if len(args.FetchVariables) > 0 {
		frame["fetchVariables"] = args.FetchVariables
	}
	if args.RequestTimeoutMs != 0 {
		frame["requestTimeout"] = args.RequestTimeoutMs
	}
	payload, _ := json.Marshal(frame)

	if err := p.link.send(payload); err != nil {
		p.discard(corr)
		// The frame never left the client, so return the credit we reserved for it
		// rather than leaking it until the next Welcome replenishes the window.
		p.addCredits(1)
		return nil, err
	}

	ack, err := p.awaitAck(ctx, ackCh)
	if err != nil {
		p.discard(corr)
		return nil, err
	}
	if ack.status != 200 {
		p.mu.Lock()
		delete(p.awaitPend, corr)
		p.mu.Unlock()
		return nil, &RemoteError{Status: ack.status, Body: bodyString(ack.body)}
	}

	outcome := &CreateOutcome{Body: ack.body}
	var body struct {
		ProcessInstanceKey string `json:"processInstanceKey"`
		ProcessCompleted   bool   `json:"processCompleted"`
	}
	_ = json.Unmarshal(ack.body, &body)
	outcome.ProcessInstanceKey = body.ProcessInstanceKey
	outcome.ProcessCompleted = body.ProcessCompleted

	if awaitCh != nil {
		budget := createAckTimeout
		if args.RequestTimeoutMs > 0 {
			budget = time.Duration(args.RequestTimeoutMs) * time.Millisecond
		}
		timer := time.NewTimer(budget) //nolint:forbidigo // an I/O bound, not cadence: engine time would misfire it

		defer timer.Stop()
		select {
		case res, ok := <-awaitCh:
			if !ok {
				return nil, errLinkClosed
			}
			outcome.ProcessCompleted = res.completed
			outcome.Variables = res.variables
		case <-ctx.Done():
			p.mu.Lock()
			delete(p.awaitPend, corr)
			p.mu.Unlock()
			return nil, ctx.Err()
		case <-timer.C:
			p.mu.Lock()
			delete(p.awaitPend, corr)
			p.mu.Unlock()
			return nil, fmt.Errorf("camunda: falcon await-completion timed out (node paused or partitioned)")
		}
	}

	return outcome, nil
}

func (p *Producer) awaitAck(ctx context.Context, ackCh chan ackResult) (ackResult, error) {
	timer := time.NewTimer(createAckTimeout) //nolint:forbidigo // an I/O bound, not cadence: engine time would misfire it

	defer timer.Stop()
	select {
	case ack, ok := <-ackCh:
		if !ok {
			return ackResult{}, errLinkClosed
		}
		return ack, nil
	case <-ctx.Done():
		return ackResult{}, ctx.Err()
	case <-timer.C:
		return ackResult{}, fmt.Errorf("camunda: falcon create timed out")
	}
}

func (p *Producer) discard(corr uint64) {
	p.mu.Lock()
	delete(p.pending, corr)
	delete(p.awaitPend, corr)
	p.mu.Unlock()
}

func bodyString(b json.RawMessage) string {
	if len(b) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	return string(b)
}
