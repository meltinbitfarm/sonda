package ratelimit

import (
	"testing"
	"time"
)

func TestDailyQuota(t *testing.T) {
	clock := time.Date(2026, 11, 4, 23, 0, 0, 0, time.UTC)
	q := NewDailyQuota(100, func() time.Time { return clock })

	if ex, _ := q.Exceeded(1); ex {
		t.Fatal("fresh key exceeded")
	}
	if q.Add(1, 60) {
		t.Fatal("60/100 reported as crossing")
	}
	if ex, _ := q.Exceeded(1); ex {
		t.Fatal("60/100 exceeded")
	}
	// A batch may overshoot: the check is before the batch, the count after.
	if !q.Add(1, 60) {
		t.Fatal("120/100 not reported as crossing")
	}
	if q.Add(1, 5) {
		t.Fatal("crossing reported twice")
	}
	ex, wait := q.Exceeded(1)
	if !ex || wait != time.Hour {
		t.Fatalf("over quota: exceeded=%v wait=%v, want true 1h", ex, wait)
	}
	if RetryAfterSeconds(wait) != 3600 {
		t.Fatalf("retry after %d", RetryAfterSeconds(wait))
	}
	// Other keys are independent; zero adds change nothing.
	q.Add(2, 0)
	if ex, _ := q.Exceeded(2); ex {
		t.Fatal("key 2 limited by key 1")
	}

	// Midnight UTC resets the count.
	clock = clock.Add(time.Hour)
	if ex, _ := q.Exceeded(1); ex {
		t.Fatal("still exceeded after midnight UTC")
	}
	q.Add(1, 99)
	if ex, _ := q.Exceeded(1); ex {
		t.Fatal("99/100 exceeded on the new day")
	}
	q.Add(1, 1)
	if ex, wait := q.Exceeded(1); !ex || wait != 24*time.Hour {
		t.Fatalf("new day full: %v %v", ex, wait)
	}
}
