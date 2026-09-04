package auth

import (
	"sync"
	"time"
)

// Limiter throttles repeated failures from one client within a sliding window.
//
// Admission is atomic: Acquire prunes, checks and charges under one lock, so a
// parallel burst can never slip past the budget the way a separate check and
// record can. An admitted attempt is resolved by the caller — Commit keeps it
// charged, Release returns it and forgives the client's earlier failures — so
// presenting a valid credential costs nothing.
//
// Each budget is separate, so exhausting one — say, passkey guesses — can never
// close the portal token recovery path.
type Limiter struct {
	mu       sync.Mutex
	maximum  int
	window   time.Duration
	failures map[string][]time.Time
}

// Reservation is one admitted attempt. Resolve it exactly once; later calls on
// the same reservation do nothing, so `defer reservation.Commit()` is safe to
// pair with a Release on the success path.
type Reservation struct {
	limiter  *Limiter
	clientID string
	resolved bool
}

// NewLimiter creates a limiter that admits at most maximum unresolved-or-failed
// attempts per client within window.
func NewLimiter(maximum int, window time.Duration) *Limiter {
	return &Limiter{maximum: maximum, window: window, failures: make(map[string][]time.Time)}
}

// Acquire atomically admits one attempt, or reports false when the client's
// budget is spent.
func (l *Limiter) Acquire(clientID string) (*Reservation, bool) {
	return l.AcquireAt(clientID, time.Now())
}

// AcquireAt is Acquire against a caller-supplied clock.
func (l *Limiter) AcquireAt(clientID string, now time.Time) (*Reservation, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	if len(l.failures[clientID]) >= l.maximum {
		return nil, false
	}
	l.failures[clientID] = append(l.failures[clientID], now)
	return &Reservation{limiter: l, clientID: clientID}, true
}

// Allow admits one attempt against a budget that charges every attempt, such as
// starting a ceremony, and reports whether there was room for it.
func (l *Limiter) Allow(clientID string) bool {
	reservation, ok := l.Acquire(clientID)
	if ok {
		reservation.Commit()
	}
	return ok
}

// Commit keeps the attempt charged, which is what a rejected credential costs.
func (r *Reservation) Commit() {
	if r == nil || r.resolved {
		return
	}
	r.resolved = true
}

// Release returns the attempt and forgives the client's earlier failures, which
// is what accepting a credential does.
func (r *Reservation) Release() {
	if r == nil || r.resolved {
		return
	}
	r.resolved = true
	r.limiter.mu.Lock()
	delete(r.limiter.failures, r.clientID)
	r.limiter.mu.Unlock()
}

func (l *Limiter) pruneLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	for clientID, attempts := range l.failures {
		kept := attempts[:0]
		for _, attempt := range attempts {
			if attempt.After(cutoff) {
				kept = append(kept, attempt)
			}
		}
		if len(kept) == 0 {
			delete(l.failures, clientID)
		} else {
			l.failures[clientID] = kept
		}
	}
}
