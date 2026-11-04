package ratelimit

import (
	"sync"
	"time"
)

// DailyQuota counts stored events per key per UTC day (O6 S1). It caps how
// much one ingest key, which is public by design (D7), can write to the disk
// and to the R2 replica in a day: the rate limiter alone allows far more.
//
// Counts live in memory: a restart starts the day again from zero, so after
// a restart a key may store up to one more quota that day. Checking is one
// map lookup under a mutex; keys are app IDs, so the map stays small.
type DailyQuota struct {
	mu    sync.Mutex
	limit int
	now   func() time.Time
	used  map[int64]dayCount
}

type dayCount struct {
	day int64 // days since the epoch, UTC
	n   int
}

// NewDailyQuota returns a quota of limit events per key per UTC day.
// now may be nil (time.Now).
func NewDailyQuota(limit int, now func() time.Time) *DailyQuota {
	if now == nil {
		now = time.Now
	}
	return &DailyQuota{limit: limit, now: now, used: make(map[int64]dayCount)}
}

func utcDay(t time.Time) int64 { return t.Unix() / 86400 }

// Exceeded reports whether key has used up today's quota and, if so, how
// long until it resets at the next midnight UTC.
func (q *DailyQuota) Exceeded(key int64) (bool, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.now()
	day := utcDay(t)
	c := q.used[key]
	if c.day != day || c.n < q.limit {
		return false, 0
	}
	return true, time.Unix((day+1)*86400, 0).Sub(t)
}

// Add records n events stored for key today. It returns true when this call
// crossed the limit, so the caller can log it once.
func (q *DailyQuota) Add(key int64, n int) bool {
	if n <= 0 {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	day := utcDay(q.now())
	c := q.used[key]
	if c.day != day {
		c = dayCount{day: day}
	}
	before := c.n
	c.n += n
	q.used[key] = c
	return before < q.limit && c.n >= q.limit
}
