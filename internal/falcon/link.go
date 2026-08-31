package falcon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// heartbeatFrame is the client's reply to a server heartbeat, keeping the
// gateway's phantom-connection reaper from dropping an otherwise-idle socket.
var heartbeatFrame = []byte(`{"type":"heartbeat"}`)

const (
	// defaultHeartbeatMs is the assumed heartbeat cadence before the gateway's
	// Welcome advertises the real one. Mirrors the gateway default.
	defaultHeartbeatMs = 15_000
	// idleHeartbeatMult is the missed-heartbeat tolerance before a silent link is
	// declared dead. Matches the gateway's own reaper (3 × heartbeat), so client
	// and server agree on when a quiet link is actually gone.
	idleHeartbeatMult = 3
	// maxFrameBytes caps a single command-stream frame. Job payloads may carry
	// large variable sets, so this is generous relative to the ws default.
	maxFrameBytes = 1 << 24 // 16 MiB
)

// falconDialTimeout bounds a single connection handshake so a hung TCP/TLS dial
// (e.g. a blackholed endpoint) can't stall the supervisor and block failover.
const falconDialTimeout = 10 * time.Second

// errLinkReconnecting / errLinkClosed are returned by SupervisedLink.send while
// the link has no live socket.
var (
	errLinkReconnecting = errors.New("camunda: falcon stream reconnecting")
	errLinkClosed       = errors.New("camunda: falcon stream closed")
)

// Dialer opens command-stream WebSocket connections. It carries the SDK's HTTP
// client (whose transport supplies TLS material for wss://) and an optional
// header provider yielding fresh auth headers per dial (e.g. an OAuth bearer).
// Injecting these keeps the falcon package a stdlib+websocket leaf, free of any
// dependency on the SDK's internal auth or transport packages.
type Dialer struct {
	HTTPClient *http.Client
	// Header, when non-nil, is called before each dial to obtain request headers
	// (typically Authorization). A returned error aborts the dial.
	Header func(ctx context.Context) (http.Header, error)
	// dialFn is a test seam; nil means dial a real WebSocket.
	dialFn func(ctx context.Context, url string, opts *websocket.DialOptions) (*websocket.Conn, error)
}

// baseFrame carries the fields the supervisor inspects on every frame before
// handing the raw bytes to a hook. Frame-type-specific fields are decoded by the
// consuming hook.
type baseFrame struct {
	Type        string `json:"type"`
	HeartbeatMs int64  `json:"heartbeatMs"`
}

// linkHooks are invoked by the supervisor across the connection lifecycle.
type linkHooks struct {
	// onFrame receives every non-heartbeat server frame as raw JSON.
	onFrame func(raw []byte)
	// onConnect is called right after each (re)connection with a sender bound to
	// the fresh socket — used to (re)send a worker subscription on failover.
	onConnect func(send func(data []byte))
	// onDisconnect is called on every disconnect — used to fail in-flight waiters.
	onDisconnect func()
}

// conn is one live WebSocket with serialized writes (a writer goroutine drains
// send) and decoded text frames delivered on frames.
type conn struct {
	send   chan []byte
	frames chan []byte
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

func (c *conn) trySend(data []byte) {
	select {
	case c.send <- data:
	case <-c.ctx.Done():
	}
}

func (c *conn) close() {
	c.cancel()
	_ = c.ws.Close(websocket.StatusNormalClosure, "")
}

// dial opens a command-stream socket and starts its writer and reader goroutines.
// The reader auto-answers heartbeats and forwards every text frame; the writer
// serializes outbound frames (the ws library permits only one concurrent writer).
func (d *Dialer) dial(ctx context.Context, url string) (*conn, error) {
	opts := &websocket.DialOptions{HTTPClient: d.HTTPClient}
	if d.Header != nil {
		h, err := d.Header(ctx)
		if err != nil {
			return nil, err
		}
		opts.HTTPHeader = h
	}
	dialFn := d.dialFn
	if dialFn == nil {
		dialFn = func(ctx context.Context, url string, o *websocket.DialOptions) (*websocket.Conn, error) {
			ws, _, err := websocket.Dial(ctx, url, o)
			return ws, err
		}
	}
	ws, err := dialFn(ctx, url, opts)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(maxFrameBytes)

	connCtx, cancel := context.WithCancel(context.Background())
	c := &conn{
		send:   make(chan []byte, 64),
		frames: make(chan []byte, 64),
		ws:     ws,
		ctx:    connCtx,
		cancel: cancel,
	}

	go func() {
		for {
			select {
			case <-connCtx.Done():
				return
			case msg := <-c.send:
				wctx, wcancel := context.WithTimeout(connCtx, 10*time.Second)
				err := ws.Write(wctx, websocket.MessageText, msg)
				wcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	go func() {
		defer close(c.frames)
		for {
			typ, data, err := ws.Read(connCtx)
			if err != nil {
				cancel()
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var bf baseFrame
			if json.Unmarshal(data, &bf) == nil && bf.Type == "heartbeat" {
				c.trySend(heartbeatFrame)
			}
			select {
			case c.frames <- data:
			case <-connCtx.Done():
				return
			}
		}
	}()

	return c, nil
}

// SupervisedLink is a command-stream link that transparently fails over across a
// directory of nodes. A background supervisor keeps exactly one socket alive:
// it picks an endpoint at random, runs it until a disconnect or read-idle
// timeout, then re-picks (avoiding the node that just failed) and reconnects —
// re-running onConnect. Because any nanobpmn gateway is a full proxy, reconnecting
// to any survivor restores whole-cluster access.
type SupervisedLink struct {
	mu        sync.Mutex
	writer    *conn // current live socket; nil while disconnected
	endpoints []string
	current   string
	connects  atomic.Uint64
	cancel    context.CancelFunc
}

// startLink launches the supervisor and blocks until the first connection
// succeeds (so the caller can immediately send) or the first dial fails (so a bad
// address fails fast into REST fallback).
func startLink(endpoints []string, d *Dialer, hooks linkHooks) (*SupervisedLink, error) {
	supCtx, cancel := context.WithCancel(context.Background())
	l := &SupervisedLink{endpoints: endpoints, cancel: cancel}
	ready := make(chan error, 1)
	go l.supervise(supCtx, d, hooks, ready)
	if err := <-ready; err != nil {
		cancel()
		return nil, err
	}
	return l, nil
}

// send writes a frame on the current socket, or errors while disconnected.
func (l *SupervisedLink) send(data []byte) error {
	l.mu.Lock()
	c := l.writer
	l.mu.Unlock()
	if c == nil {
		return errLinkReconnecting
	}
	select {
	case c.send <- data:
		return nil
	case <-c.ctx.Done():
		return errLinkClosed
	}
}

// close stops the supervisor and tears down the current socket.
func (l *SupervisedLink) close() {
	l.cancel()
	l.mu.Lock()
	c := l.writer
	l.writer = nil
	l.mu.Unlock()
	if c != nil {
		c.close()
	}
}

func (l *SupervisedLink) supervise(supCtx context.Context, d *Dialer, hooks linkHooks, ready chan<- error) {
	idle := linkIdle(defaultHeartbeatMs)
	seed := uint64(time.Now().UnixNano()) | 1 //nolint:forbidigo // seeds reconnect jitter; no cadence depends on it

	lastFailed := ""
	sentReady := false

	for supCtx.Err() == nil {
		url := pickEndpoint(l.endpoints, lastFailed, &seed)
		// Bound the handshake so a hung dial can't stall failover. dial only uses the
		// context for the upgrade; the live connection runs on its own context, so
		// cancelling here after dial returns is safe.
		dctx, dcancel := context.WithTimeout(supCtx, falconDialTimeout)
		c, err := d.dial(dctx, url)
		dcancel()
		if err != nil {
			if !sentReady {
				ready <- err
				return
			}
			lastFailed = url
			select {
			case <-supCtx.Done():
				return
			case <-time.After(250 * time.Millisecond): //nolint:forbidigo // falcon is deliberately still on real time; see #40

			}
			continue
		}

		l.mu.Lock()
		l.writer = c
		l.current = url
		l.mu.Unlock()
		l.connects.Add(1)
		if !sentReady {
			ready <- nil
			sentReady = true
		}
		if hooks.onConnect != nil {
			hooks.onConnect(c.trySend)
		}

		idle = l.pump(supCtx, c, hooks, idle)

		c.close()
		l.mu.Lock()
		l.writer = nil
		l.mu.Unlock()
		if hooks.onDisconnect != nil {
			hooks.onDisconnect()
		}
		lastFailed = url
	}
}

// pump reads frames until the socket closes, the supervisor is canceled, or the
// read-idle timeout fires (silent node → failover). It returns the (possibly
// refined) idle timeout: the gateway advertises its real heartbeat cadence in the
// Welcome frame, which tightens the derived timeout to 3× it.
func (l *SupervisedLink) pump(supCtx context.Context, c *conn, hooks linkHooks, idle time.Duration) time.Duration {
	timer := time.NewTimer(idle) //nolint:forbidigo // an I/O bound, not cadence: engine time would misfire it

	defer timer.Stop()
	for {
		select {
		case <-supCtx.Done():
			return idle
		case raw, ok := <-c.frames:
			if !ok {
				return idle // socket closed
			}

			var bf baseFrame
			_ = json.Unmarshal(raw, &bf)
			// Refine the idle timeout from the gateway's advertised heartbeat cadence
			// BEFORE (re)arming the timer, so a Welcome that widens the cadence doesn't
			// leave the timer running at the old (shorter) duration and fire a spurious
			// failover before the first heartbeat arrives.
			if bf.Type == "welcome" && bf.HeartbeatMs > 0 {
				idle = linkIdle(bf.HeartbeatMs)
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)

			if bf.Type == "heartbeat" {
				// Liveness only: the reset above already refreshed the idle timer.
				continue
			}
			if hooks.onFrame != nil {
				hooks.onFrame(raw)
			}
		case <-timer.C:
			return idle // idle timeout → failover
		}
	}
}

// pickEndpoint returns a random endpoint, avoiding avoid (the node that just
// failed) when the directory has more than one entry. Uses a cheap xorshift so no
// rng dependency is needed.
func pickEndpoint(endpoints []string, avoid string, seed *uint64) string {
	if len(endpoints) == 1 {
		return endpoints[0]
	}
	*seed ^= *seed << 13
	*seed ^= *seed >> 7
	*seed ^= *seed << 17
	start := int(*seed % uint64(len(endpoints)))
	for i := 0; i < len(endpoints); i++ {
		cand := endpoints[(start+i)%len(endpoints)]
		if cand != avoid {
			return cand
		}
	}
	return endpoints[start]
}

// linkIdle derives the read-idle failover timeout from the gateway's advertised
// heartbeat cadence as 3× it, so a healthy-but-quiet link (backpressured, or with
// no jobs to deliver) is never mistaken for a dead node.
func linkIdle(heartbeatMs int64) time.Duration {
	hb := heartbeatMs
	if hb <= 0 {
		hb = defaultHeartbeatMs
	}
	return time.Duration(hb*idleHeartbeatMult) * time.Millisecond
}
