package camunda

import (
	"net/http"
	"sync"
	"sync/atomic"
)

// workerPoolHeadroom is the per-worker connection allowance added on top of
// MaxConcurrentJobs when sizing the idle pool: one for the worker's activation
// long-poll (or, for a streaming worker, its REST sidecar poll).
const workerPoolHeadroom = 1

// sizedTransport is the SDK-owned base RoundTripper. It wraps the SDK's own
// *http.Transport and grows its idle-connection pool to cover the in-flight
// capacity of the client's job workers
// (camunda/orchestration-cluster-api-go#196).
//
// http.Transport fields must not be mutated while the transport is in use, so
// growing swaps in a resized clone atomically. Requests already in flight finish
// on the old transport; its idle connections are closed so they do not linger.
// Pool dimensions set explicitly (WithMaxIdleConns(PerHost) or
// CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS(_PER_HOST)) are never changed.
type sizedTransport struct {
	cur atomic.Pointer[http.Transport]

	mu               sync.Mutex
	demand           int // sum over workers of MaxConcurrentJobs + headroom
	fixedIdle        bool
	fixedIdlePerHost bool
}

func newSizedTransport(tr *http.Transport, fixedIdle, fixedIdlePerHost bool) *sizedTransport {
	s := &sizedTransport{fixedIdle: fixedIdle, fixedIdlePerHost: fixedIdlePerHost}
	s.cur.Store(tr)
	return s
}

// RoundTrip implements http.RoundTripper.
func (s *sizedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.cur.Load().RoundTrip(req)
}

// CloseIdleConnections forwards to the current transport.
func (s *sizedTransport) CloseIdleConnections() {
	s.cur.Load().CloseIdleConnections()
}

// transport returns the transport currently serving requests.
func (s *sizedTransport) transport() *http.Transport {
	return s.cur.Load()
}

// addWorker registers a worker's in-flight capacity and grows the pool when
// the total exceeds the current (non-explicit) pool size. It never shrinks.
func (s *sizedTransport) addWorker(maxConcurrent int) {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.demand += maxConcurrent + workerPoolHeadroom
	old := s.cur.Load()
	idle, perHost := old.MaxIdleConns, old.MaxIdleConnsPerHost
	if !s.fixedIdle && idle != 0 && idle < s.demand {
		idle = s.demand // 0 means unlimited; never lower that
	}
	if !s.fixedIdlePerHost && perHost < s.demand {
		perHost = s.demand
	}
	if idle == old.MaxIdleConns && perHost == old.MaxIdleConnsPerHost {
		return
	}
	next := old.Clone()
	next.MaxIdleConns, next.MaxIdleConnsPerHost = idle, perHost
	s.cur.Store(next)
	old.CloseIdleConnections()
}

// registerWorkerCapacity grows the SDK-owned connection pool for a new worker.
// It is a no-op when the user supplied the HTTP plumbing.
func (c *CamundaClient) registerWorkerCapacity(maxConcurrent int) {
	if c.pool != nil {
		c.pool.addWorker(maxConcurrent)
	}
}
