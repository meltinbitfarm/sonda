// Package ratelimit is an in-memory token bucket per key (A2).
//
// Each key gets a bucket of capacity perMinute, refilled continuously at
// perMinute tokens per minute: a client may burst up to a minute's worth of
// requests, then is held to the average rate. State lives in memory only, so a
// restart resets all buckets; with one process per server that is acceptable.
// Keys are app IDs, so the map is bounded by the number of apps.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// Limiter holds one bucket per key. Safe for concurrent use.
type Limiter struct {
	mu       sync.Mutex
	capacity float64
	perSec   float64
	now      func() time.Time
	buckets  map[int64]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a limiter allowing perMinute requests per minute per key.
// now may be nil (time.Now).
func New(perMinute int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		capacity: float64(perMinute),
		perSec:   float64(perMinute) / 60,
		now:      now,
		buckets:  make(map[int64]*bucket),
	}
}

// Allow takes a token for key. If none is available it returns false and how
// long to wait before one is.
func (l *Limiter) Allow(key int64) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.capacity, last: t}
		l.buckets[key] = b
	}
	if el := t.Sub(b.last).Seconds(); el > 0 {
		b.tokens = math.Min(l.capacity, b.tokens+el*l.perSec)
	}
	b.last = t
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.perSec * float64(time.Second))
	return false, wait
}

// RetryAfterSeconds rounds a wait up to whole seconds, at least 1, for the
// Retry-After header.
func RetryAfterSeconds(d time.Duration) int {
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		s = 1
	}
	return s
}
