// Package backpressure implements an adaptive global backpressure controller.
//
// It escalates on broker backpressure signals (HTTP 429 / 503 / a body carrying
// RESOURCE_EXHAUSTED) and throttles initiating operations via an adaptive
// concurrency limiter, while letting drain operations (job completion/failure)
// bypass the gate. The algorithm is an AIMD-style limiter with multiplicative
// shrink on backpressure and additive-then-multiplicative recovery while healthy.
//
// It is a faithful port of the JS/Python/Rust SDK BackpressureManager, with two
// profiles:
//
//   - Balanced (default): adaptive gating.
//   - Legacy: observe-only — record severity but never gate.
//
// The manager starts unlimited and only boots to an initial permit cap on the
// first backpressure signal; after sustained health it returns to unlimited.
package backpressure

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"
)

// ErrQueueFull is returned by Acquire when the waiter queue is at capacity. It is
// re-exported as the public camunda.ErrBackpressureQueueFull, so its message keeps
// the "camunda:" prefix to stay consistent with the other public SDK sentinels
// and with the pre-fix user-facing text.
var ErrQueueFull = errors.New("camunda: backpressure waiter queue full")

// Severity is the backpressure severity level reported by the manager.
type Severity int

// Severity levels.
const (
	Healthy Severity = iota
	Soft
	Severe
)

func (s Severity) String() string {
	switch s {
	case Soft:
		return "soft"
	case Severe:
		return "severe"
	default:
		return "healthy"
	}
}

// BALANCED profile tuning constants (matching the JS/Python/Rust SDKs).
const (
	initialMax                = 16
	floor                     = 1
	softFactor                = 0.70
	severeFactor              = 0.50
	recoveryInterval          = 1 * time.Second
	recoveryStep              = 1
	severeThreshold           = 3
	decayQuiet                = 2 * time.Second
	maxWaiters                = 1000
	healthyRecoveryMultiplier = 1.5
	unlimitedAfterHealthy     = 30 * time.Second

	backoffInitial  = 25 * time.Millisecond
	backoffMax      = 2 * time.Second
	backoffEscalate = 2
)

// State is a point-in-time snapshot of the manager's internal state.
type State struct {
	Severity       Severity
	Consecutive    int
	PermitsMax     *int // nil == unlimited
	PermitsCurrent int
	Waiters        int
	BackoffMillis  int64
}

// IsBackpressureResponse reports whether an HTTP status/body pair signals
// cluster backpressure. Mirrors the other SDKs: 429, a bare 503, and any
// response (500 or otherwise) carrying RESOURCE_EXHAUSTED.
func IsBackpressureResponse(status int, body string) bool {
	hasResourceExhausted := strings.Contains(body, "RESOURCE_EXHAUSTED")
	switch status {
	case 429:
		return true
	case 503:
		return hasResourceExhausted || body == ""
	case 500:
		return hasResourceExhausted
	default:
		return hasResourceExhausted
	}
}

// Manager is an adaptive backpressure manager, safe for concurrent use and
// intended to be shared across all clones of a client.
type Manager struct {
	observeOnly bool
	clock       Clock

	mu               sync.Mutex
	notify           chan struct{}
	severity         Severity
	consecutive      int
	lastEventAt      time.Time
	permitsCurrent   int
	permitsMax       *int // nil == unlimited
	lastRecoverCheck time.Time
	healthySince     time.Time
	waiters          int
	backoff          time.Duration
}

// Profile selects the manager's behavior.
type Profile int

// Profiles.
const (
	Balanced Profile = iota
	Legacy
)

// Clock is the part of the SDK clock this package needs. Declared here rather than
// imported so backpressure stays a leaf package (see architecture_test.go); the
// injected clock satisfies it structurally.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// New builds a manager. clock must not be nil; the client resolves one before
// constructing any collaborator.
func New(profile Profile, clock Clock) *Manager {
	return &Manager{
		observeOnly: profile == Legacy,
		clock:       clock,
		notify:      make(chan struct{}),
	}
}

// Severity returns the current severity level.
func (m *Manager) Severity() Severity {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.severity
}

// State returns a snapshot of the manager's internal state.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	var pm *int
	if m.permitsMax != nil {
		v := *m.permitsMax
		pm = &v
	}
	return State{
		Severity:       m.severity,
		Consecutive:    m.consecutive,
		PermitsMax:     pm,
		PermitsCurrent: m.permitsCurrent,
		Waiters:        m.waiters,
		BackoffMillis:  m.backoff.Milliseconds(),
	}
}

// broadcast wakes all current waiters. Must be called with m.mu held.
func (m *Manager) broadcast() {
	close(m.notify)
	m.notify = make(chan struct{})
}

// Acquire blocks until a permit is available, respecting ctx cancellation.
// It returns ErrQueueFull if the waiter queue is at capacity. In the Legacy
// profile it is a no-op.
func (m *Manager) Acquire(ctx context.Context) error {
	if m.observeOnly {
		return nil
	}
	for {
		m.mu.Lock()
		if m.permitsMax == nil {
			m.mu.Unlock()
			return nil // unlimited fast path
		}
		backoff := m.backoff
		m.mu.Unlock()

		if backoff > 0 {
			if err := m.clock.Sleep(ctx, backoff); err != nil {
				return err
			}
		}

		m.mu.Lock()
		if m.permitsMax == nil {
			m.mu.Unlock()
			return nil // went unlimited during backoff
		}
		if m.permitsCurrent < *m.permitsMax {
			m.permitsCurrent++
			m.mu.Unlock()
			return nil
		}
		if m.waiters >= maxWaiters {
			m.mu.Unlock()
			return ErrQueueFull
		}
		m.waiters++
		ch := m.notify
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.waiters--
			m.mu.Unlock()
			return ctx.Err()
		case <-ch:
			m.mu.Lock()
			m.waiters--
			m.mu.Unlock()
			// retry from the top
		}
	}
}

// Release returns a permit and wakes waiters. In the Legacy profile it is a no-op.
func (m *Manager) Release() {
	if m.observeOnly {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.permitsMax == nil {
		return
	}
	if m.permitsCurrent > 0 {
		m.permitsCurrent--
	}
	m.broadcast()
}

// RecordBackpressure records a backpressure signal from the server.
func (m *Manager) RecordBackpressure() {
	now := m.clock.Now()
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastEventAt = now
	m.consecutive++
	m.healthySince = time.Time{}

	if !m.observeOnly && m.permitsMax == nil {
		v := initialMax
		m.permitsMax = &v
		if m.permitsCurrent > v {
			m.permitsCurrent = v
		}
	}

	if m.consecutive >= severeThreshold {
		m.severity = Severe
		if !m.observeOnly {
			m.scalePermits(severeFactor)
		}
	} else if m.severity == Healthy {
		m.severity = Soft
		if !m.observeOnly {
			m.scalePermits(softFactor)
		}
	} else {
		if !m.observeOnly {
			m.scalePermits(softFactor)
		}
	}

	if !m.observeOnly && m.permitsMax != nil && *m.permitsMax <= floor && m.severity == Severe {
		if m.backoff == 0 {
			m.backoff = backoffInitial
		} else {
			m.backoff *= backoffEscalate
			if m.backoff > backoffMax {
				m.backoff = backoffMax
			}
		}
	}
}

// RecordHealthyHint records a successful (non-backpressure) completion, which
// triggers passive recovery.
func (m *Manager) RecordHealthyHint() {
	now := m.clock.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.backoff > 0 {
		m.backoff = 0
	}
	m.maybeRecover(now)
}

// scalePermits reduces permitsMax by factor. Must hold m.mu.
func (m *Manager) scalePermits(factor float64) {
	if m.permitsMax == nil {
		return
	}
	next := int(math.Ceil(float64(*m.permitsMax) * factor))
	if next < floor {
		next = floor
	}
	if next < *m.permitsMax {
		*m.permitsMax = next
	}
}

// maybeRecover performs a passive recovery step. Must hold m.mu.
func (m *Manager) maybeRecover(now time.Time) {
	if m.permitsMax == nil || m.observeOnly {
		return
	}
	if !m.lastRecoverCheck.IsZero() && now.Sub(m.lastRecoverCheck) < recoveryInterval {
		return
	}
	m.lastRecoverCheck = now

	// Decay severity if quiet (stepwise: severe -> soft -> healthy).
	if now.Sub(m.lastEventAt) > decayQuiet {
		switch m.severity {
		case Severe:
			m.severity = Soft
		case Soft:
			m.severity = Healthy
			m.healthySince = now
		}
		if m.severity == Healthy {
			m.consecutive = 0
		}
		if m.backoff > 0 {
			m.backoff = 0
		}
	}

	// Phase 1: additive recovery while not yet healthy.
	if m.severity != Healthy {
		if *m.permitsMax < initialMax {
			next := *m.permitsMax + recoveryStep
			if next > initialMax {
				next = initialMax
			}
			*m.permitsMax = next
			if *m.permitsMax > floor && m.backoff > 0 {
				m.backoff = 0
			}
			m.broadcast()
		}
		return
	}

	// Phase 3: sustained healthy -> return to unlimited.
	if !m.healthySince.IsZero() && now.Sub(m.healthySince) >= unlimitedAfterHealthy {
		m.permitsMax = nil
		m.permitsCurrent = 0
		m.backoff = 0
		m.broadcast()
		return
	}

	// Phase 2: multiplicative growth while healthy (no ceiling).
	next := int(math.Ceil(float64(*m.permitsMax) * healthyRecoveryMultiplier))
	if next > *m.permitsMax {
		*m.permitsMax = next
		m.broadcast()
	}
}
