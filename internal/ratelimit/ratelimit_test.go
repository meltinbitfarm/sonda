package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	l := New(60, func() time.Time { return clock })

	for i := 0; i < 60; i++ {
		if ok, _ := l.Allow(1); !ok {
			t.Fatalf("request %d refused within burst", i)
		}
	}
	ok, wait := l.Allow(1)
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("61st: ok=%v wait=%v", ok, wait)
	}
	if RetryAfterSeconds(wait) != 1 {
		t.Fatalf("retry after = %d", RetryAfterSeconds(wait))
	}

	// Other keys are independent.
	if ok, _ := l.Allow(2); !ok {
		t.Fatal("key 2 limited by key 1")
	}

	// One second refills one token at 60/min.
	clock = clock.Add(time.Second)
	if ok, _ := l.Allow(1); !ok {
		t.Fatal("no refill after 1s")
	}
	if ok, _ := l.Allow(1); ok {
		t.Fatal("refilled more than one token")
	}

	// Long idle refills to capacity, not beyond.
	clock = clock.Add(time.Hour)
	n := 0
	for {
		if ok, _ := l.Allow(1); !ok {
			break
		}
		n++
	}
	if n != 60 {
		t.Fatalf("after idle: %d allowed, want 60", n)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for d, want := range map[time.Duration]int{0: 1, time.Millisecond: 1, 1500 * time.Millisecond: 2, 30 * time.Second: 30} {
		if got := RetryAfterSeconds(d); got != want {
			t.Errorf("%v: %d, want %d", d, got, want)
		}
	}
}
