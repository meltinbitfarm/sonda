package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// O6 S2: every tool call gets a deadline, and at most n run at once.
func TestGuardTimeout(t *testing.T) {
	g := newGuard(100*time.Millisecond, 3)
	start := time.Now()
	_, err := run(context.Background(), g, "funnel", func(ctx context.Context) (int, error) {
		<-ctx.Done() // a query that only stops when its context ends
		return 0, ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "funnel stopped after 100ms") || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
	// A failure that is not the deadline keeps its own message.
	if _, err := run(context.Background(), g, "x", func(context.Context) (int, error) { return 0, errors.New("bad app") }); err == nil || err.Error() != "bad app" {
		t.Fatalf("err = %v", err)
	}
	if out, err := run(context.Background(), g, "x", func(context.Context) (int, error) { return 7, nil }); out != 7 || err != nil {
		t.Fatalf("ok call: %d %v", out, err)
	}
	if newGuard(0, 1).timeout != DefaultToolTimeout {
		t.Fatal("zero timeout does not default")
	}
}

func TestGuardConcurrency(t *testing.T) {
	g := newGuard(300*time.Millisecond, 2)
	release := make(chan struct{})
	var running, peak int
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := run(context.Background(), g, "retention", func(ctx context.Context) (int, error) {
				mu.Lock()
				running++
				peak = max(peak, running)
				mu.Unlock()
				select {
				case <-release:
				case <-ctx.Done():
				}
				mu.Lock()
				running--
				mu.Unlock()
				return 0, nil
			})
			errs <- err
		}()
	}
	// Two hold their slot past the third's deadline: the third gives up.
	time.Sleep(500 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	busy := 0
	for err := range errs {
		if err != nil && strings.Contains(err.Error(), "server busy") {
			busy++
		}
	}
	if peak != 2 || busy != 1 {
		t.Fatalf("peak %d concurrent, %d busy errors; want 2 and 1", peak, busy)
	}
}

// The option reaches the tools: a runaway query is cut by ToolTimeout, well
// before the query tool's own 10 s.
func TestToolTimeoutOverMCP(t *testing.T) {
	srv, _ := setupWith(t, Options{ToolTimeout: 300 * time.Millisecond})
	cs := connect(t, srv.URL+Path, &http.Client{Transport: bearerTransport{token}})
	start := time.Now()
	_, res := call(t, cs, "query", map[string]any{"sql": "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c"})
	if !res.IsError || !strings.Contains(errText(res), "query stopped after 300ms") || time.Since(start) > 5*time.Second {
		t.Fatalf("runaway query: error=%v %q after %s", res.IsError, errText(res), time.Since(start))
	}
}
