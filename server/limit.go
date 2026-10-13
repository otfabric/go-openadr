// SPDX-License-Identifier: MIT

package server

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/otfabric/go-openadr/auth"
)

// Limiter decides whether a VTN takes a request now. It is asked once for
// every request of a client the VTN has identified and found entitled to
// the operation: a request without a valid token, or without the scope,
// is refused before, and costs a Limiter nothing.
//
// Implementations must be safe for concurrent use, and must not block: a
// request that has to wait is refused, with the time to wait.
type Limiter interface {
	// Allow reports whether the request is taken. clientID is the client
	// of the token, and empty for a client that did not authenticate, on a
	// VTN that allows that. op is the operation, as Metrics names it
	// ("GET /events/{id}"). When it returns false, retryAfter is how long
	// the client is told to wait; zero tells it nothing.
	Allow(clientID, op string) (ok bool, retryAfter time.Duration)
}

// limited answers a request the limiter refuses, and reports that it did.
func (s *Server) limited(w http.ResponseWriter, r *http.Request, id auth.Identity) bool {
	if s.limiter == nil {
		return false
	}
	ok, wait := s.limiter.Allow(id.ClientID, r.Pattern)
	if ok {
		return false
	}
	if wait > 0 {
		// Whole seconds, rounded up: a client that comes back a moment
		// early is refused again.
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	}
	s.problem(w, r, http.StatusTooManyRequests, "too many requests; slow down")
	return true
}

// RateLimiter is a Limiter that gives each client a budget of requests: a
// token bucket per client ID. All clients that do not authenticate share
// one bucket.
type RateLimiter struct {
	rate  float64 // tokens per second
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
	swept   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewRateLimiter returns a Limiter that lets each client make perSecond
// requests a second on average, and burst at once after a quiet time. A
// burst below 1 is 1.
//
// It is a guard against a client that has gone wrong, a loop without a
// pause, not a defence against an attack: it counts requests of clients it
// knows, after their token was checked. What arrives without a valid token
// is for a gateway before the VTN to limit.
func NewRateLimiter(perSecond float64, burst int) *RateLimiter {
	if burst < 1 {
		burst = 1
	}
	return &RateLimiter{rate: perSecond, burst: float64(burst), buckets: map[string]*bucket{}, now: time.Now}
}

// Allow implements Limiter.
func (l *RateLimiter) Allow(clientID, _ string) (bool, time.Duration) {
	if l.rate <= 0 {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[clientID]
	if b == nil {
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[clientID] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	b.at = now
	l.sweep(now)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// sweep forgets, now and then, the clients whose bucket is full again: a
// client that is not heard of costs no memory. The caller holds the lock.
func (l *RateLimiter) sweep(now time.Time) {
	full := time.Duration(l.burst / l.rate * float64(time.Second))
	if now.Sub(l.swept) < full+time.Minute {
		return
	}
	l.swept = now
	for id, b := range l.buckets {
		if now.Sub(b.at) > full {
			delete(l.buckets, id)
		}
	}
}
