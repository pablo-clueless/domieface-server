package httpx

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter is an in-process token bucket keyed by client. It is deliberately
// per-process: with more than one replica, move this to Redis rather than
// raising the limits, or a client gets N times the intended budget.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	refillPerSecond float64
	burst           float64
	now             func() time.Time // swapped in tests
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// NewRateLimiter allows perMinute requests sustained, with burst spare capacity.
func NewRateLimiter(perMinute, burst int) *RateLimiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	if burst <= 0 {
		burst = perMinute
	}
	return &RateLimiter{
		buckets:         make(map[string]*bucket),
		refillPerSecond: float64(perMinute) / 60,
		burst:           float64(burst),
		now:             time.Now,
	}
}

// Allow consumes a token for key. When it returns false, retryAfter is how long
// the caller should wait before the next token is available.
func (rl *RateLimiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: rl.burst, lastSeen: now}
		rl.buckets[key] = b
	}

	// Refill for the time that has passed since we last saw this client.
	b.tokens = math.Min(rl.burst, b.tokens+now.Sub(b.lastSeen).Seconds()*rl.refillPerSecond)
	b.lastSeen = now

	if b.tokens < 1 {
		deficit := 1 - b.tokens
		wait := time.Duration(deficit / rl.refillPerSecond * float64(time.Second))
		return false, max(wait, time.Second)
	}

	b.tokens--
	return true, 0
}

// Middleware rejects over-budget callers with 429 and a Retry-After header, as
// the contract requires. keyFor decides what counts as one client.
func (rl *RateLimiter) Middleware(keyFor func(*http.Request) string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, retryAfter := rl.Allow(keyFor(r))
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
				WriteError(w, r, ErrRateLimited())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Reap drops buckets untouched for idleFor, so the map does not grow without
// bound across a long-running process.
func (rl *RateLimiter) Reap(idleFor time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := rl.now().Add(-idleFor)
	for key, b := range rl.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(rl.buckets, key)
		}
	}
}

// StartReaper runs Reap on a ticker until stop is closed.
func (rl *RateLimiter) StartReaper(every, idleFor time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.Reap(idleFor)
		case <-stop:
			return
		}
	}
}
