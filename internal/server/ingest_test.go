package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meltinbitfarm/sonda/internal/ingestkey"
	"github.com/meltinbitfarm/sonda/internal/store"
)

var testNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type env struct {
	t     *testing.T
	dir   string
	st    *store.Store
	h     http.Handler
	key   string
	appID int64
	logs  *syncBuf
	clock *time.Time
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "sonda.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, dir: dir, st: st, logs: &syncBuf{}}
	e.key, e.appID = e.createApp("Demo")
	clock := testNow
	e.clock = &clock
	cfg.Logger = NewLogger(e.logs)
	cfg.Now = func() time.Time { return *e.clock }
	e.h = New(st, cfg)
	return e
}

func (e *env) createApp(name string) (string, int64) {
	key, hash := ingestkey.Generate()
	app, err := e.st.CreateApp(context.Background(), name, hash, testNow)
	if err != nil {
		e.t.Fatal(err)
	}
	return key, app.ID
}

func ev(name string, over ...any) map[string]any {
	m := map[string]any{
		"name":        name,
		"ts":          testNow.UnixMilli() - 5000,
		"user_id":     "0f8fad5b-d9cb-469f-a165-70867728950e",
		"session_id":  "7c9e6679-7425-40de-944b-e07fc1f90ae7",
		"app_version": "1.0",
		"device_type": "phone",
		"props":       map[string]any{},
	}
	for i := 0; i+1 < len(over); i += 2 {
		m[over[i].(string)] = over[i+1]
	}
	return m
}

func batchBody(events ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"events": events})
	return b
}

// post sends body with the given key ("" = no Authorization) and extra headers.
func (e *env) post(key string, body []byte, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) count(where string, args ...any) int {
	var n int
	if err := e.st.DB.QueryRow("SELECT count(*) FROM events WHERE "+where, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) IngestResponse {
	t.Helper()
	var r IngestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("response %q: %v", rec.Body.String(), err)
	}
	return r
}

func TestIngestAuth(t *testing.T) {
	e := newEnv(t, Config{})
	body := batchBody(ev("app.first_launch"))

	cases := map[string]string{
		"no header":     "",
		"unknown key":   "sk_" + strings.Repeat("A", 43),
		"not sk prefix": "abc",
	}
	for name, key := range cases {
		rec := e.post(key, body)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
	rec := e.post("", body, "Authorization", "Basic "+e.key)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("basic scheme: %d", rec.Code)
	}
	if n := e.count("1"); n != 0 {
		t.Fatalf("%d events stored without valid key", n)
	}

	// app_id comes from the key; a body field is ignored.
	_, otherID := e.createApp("Other")
	rec = e.post(e.key, batchBody(ev("app.first_launch", "app_id", otherID)))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid key: %d %q", rec.Code, rec.Body.String())
	}
	if e.count("app_id = ?", e.appID) != 1 || e.count("app_id = ?", otherID) != 0 {
		t.Fatal("event not attributed to the key's app")
	}
	// Scheme is case-insensitive.
	if rec := e.post("", body, "Authorization", "bearer "+e.key); rec.Code != http.StatusOK {
		t.Fatalf("lowercase bearer: %d", rec.Code)
	}
}

func TestIngestPartialBatch(t *testing.T) {
	e := newEnv(t, Config{})
	rec := e.post(e.key, batchBody(
		ev("app.session_started"),
		ev("BadName"),
		ev("screen.viewed", "props", map[string]any{"screen": "home"}),
		ev("feature.used", "props", map[string]any{"nested": map[string]any{"a": 1}}),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	r := decode(t, rec)
	if r.Accepted != 2 || r.Rejected != 2 || len(r.Errors) != 2 || r.Errors[0].Index != 1 || r.Errors[1].Index != 3 ||
		!strings.Contains(r.Errors[1].Reason, "nested") {
		t.Fatalf("response = %+v", r)
	}
	if n := e.count("received_at = ?", testNow.UnixMilli()); n != 2 {
		t.Fatalf("stored %d, want 2", n)
	}
	var props string
	e.st.DB.QueryRow("SELECT props FROM events WHERE name = 'screen.viewed'").Scan(&props)
	if props != `{"screen":"home"}` {
		t.Fatalf("props = %q", props)
	}

	// All rejected is still 200: retrying would not help.
	rec = e.post(e.key, batchBody(ev("x"), ev("y", "ts", 0)))
	if r := decode(t, rec); rec.Code != http.StatusOK || r.Accepted != 0 || r.Rejected != 2 {
		t.Fatalf("all rejected: %d %+v", rec.Code, r)
	}
}

func TestIngestBadRequests(t *testing.T) {
	e := newEnv(t, Config{})
	good := batchBody(ev("app.first_launch"))
	cases := []struct {
		name string
		body []byte
		hdr  []string
		code int
	}{
		{"not json", []byte("hello"), nil, 400},
		{"bare array", []byte(`[{"name":"a.b"}]`), nil, 400},
		{"missing events", []byte(`{}`), nil, 400},
		{"empty events", []byte(`{"events":[]}`), nil, 400},
		{"events not array", []byte(`{"events":{}}`), nil, 400},
		{"trailing garbage", append(append([]byte{}, good...), "x"...), nil, 400},
		{"wrong content type", good, []string{"Content-Type", "text/plain"}, 415},
		{"unknown encoding", good, []string{"Content-Encoding", "br"}, 415},
		{"invalid gzip", good, []string{"Content-Encoding", "gzip"}, 400},
		{"charset ok", good, []string{"Content-Type", "application/json; charset=utf-8"}, 200},
	}
	for _, c := range cases {
		rec := e.post(e.key, c.body, c.hdr...)
		if rec.Code != c.code {
			t.Errorf("%s: %d %q, want %d", c.name, rec.Code, rec.Body.String(), c.code)
		}
		if rec.Code != 200 && !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: no error body: %q", c.name, rec.Body.String())
		}
	}
	if rec := e.post(e.key, good, "Content-Type", ""); rec.Code != 200 {
		t.Errorf("no content type: %d", rec.Code)
	}
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func TestIngestLimits(t *testing.T) {
	e := newEnv(t, Config{})

	evs := make([]map[string]any, DefaultMaxBatch)
	for i := range evs {
		evs[i] = ev("screen.viewed")
	}
	if rec := e.post(e.key, batchBody(evs...)); rec.Code != 200 || decode(t, rec).Accepted != DefaultMaxBatch {
		t.Fatalf("max batch: %d %q", rec.Code, rec.Body.String())
	}
	evs = append(evs, ev("screen.viewed"))
	if rec := e.post(e.key, batchBody(evs...)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("batch of %d: %d", len(evs), rec.Code)
	}

	big := batchBody(ev("screen.viewed", "padding", strings.Repeat("x", DefaultMaxBodyBytes)))
	if rec := e.post(e.key, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("big body: %d", rec.Code)
	}

	// gzip: a small batch decompresses fine; a small compressed body that
	// inflates beyond the limit (zip bomb) is refused.
	rec := e.post(e.key, gz(batchBody(ev("app.first_launch"))), "Content-Encoding", "gzip")
	if rec.Code != 200 || decode(t, rec).Accepted != 1 {
		t.Fatalf("gzip: %d %q", rec.Code, rec.Body.String())
	}
	bomb := gz(big)
	if len(bomb) > DefaultMaxBodyBytes/10 {
		t.Fatalf("bomb not small: %d", len(bomb))
	}
	if rec := e.post(e.key, bomb, "Content-Encoding", "gzip"); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("gzip bomb: %d", rec.Code)
	}

	if n := e.count("1"); n != DefaultMaxBatch+1 {
		t.Fatalf("stored %d", n)
	}

	// Configured limits.
	e = newEnv(t, Config{MaxBatch: 2, MaxBodyBytes: 600})
	if rec := e.post(e.key, batchBody(ev("a.b"), ev("a.b"), ev("a.b"))); rec.Code != 413 {
		t.Fatalf("configured batch: %d", rec.Code)
	}
	if rec := e.post(e.key, batchBody(ev("a.b", "props", map[string]any{"k": strings.Repeat("x", 600)}))); rec.Code != 413 {
		t.Fatalf("configured body: %d", rec.Code)
	}
}

func TestIngestRateLimit(t *testing.T) {
	e := newEnv(t, Config{RatePerMinute: 3})
	body := batchBody(ev("app.session_started"))
	for i := 0; i < 3; i++ {
		if rec := e.post(e.key, body); rec.Code != 200 {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	rec := e.post(e.key, body)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "20" {
		t.Fatalf("4th: %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Rejected requests store nothing and do not count against other keys.
	if n := e.count("1"); n != 3 {
		t.Fatalf("stored %d", n)
	}
	other, _ := e.createApp("Other")
	if rec := e.post(other, body); rec.Code != 200 {
		t.Fatalf("other key: %d", rec.Code)
	}
	// Invalid keys get 401, not 429, and do not consume tokens.
	if rec := e.post("sk_nope", body); rec.Code != 401 {
		t.Fatalf("bad key while limited: %d", rec.Code)
	}

	*e.clock = e.clock.Add(20 * time.Second)
	if rec := e.post(e.key, body); rec.Code != 200 {
		t.Fatalf("after refill: %d", rec.Code)
	}
}

// D9: the client address and forwarding headers must not reach the database
// or the logs, not even through a panic.
func TestNoIPAnywhere(t *testing.T) {
	e := newEnv(t, Config{})
	ips := []string{"203.0.113.7", "198.51.100.23", "192.0.2.44", "2001:db8::7"}
	send := func(body []byte) {
		req := httptest.NewRequest("POST", "/v1/events", bytes.NewReader(body))
		req.RemoteAddr = "203.0.113.7:4242"
		req.Header.Set("Authorization", "Bearer "+e.key)
		req.Header.Set("X-Forwarded-For", "198.51.100.23, 192.0.2.44")
		req.Header.Set("X-Real-IP", "198.51.100.23")
		req.Header.Set("Forwarded", "for=\"[2001:db8::7]\"")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "203.0.113.7") {
			t.Errorf("IP echoed in response: %q", rec.Body.String())
		}
	}
	send(batchBody(ev("app.first_launch"), ev("bad")))
	send([]byte("not json"))

	// A panic is logged without the address (net/http would print it).
	srv := &server{cfg: Config{Logger: NewLogger(e.logs)}}
	h := srv.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(fmt.Errorf("boom from 203.0.113.7 and [2001:db8::7]:443"))
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.7:4242"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("panic: %d", rec.Code)
	}

	logs := e.logs.String()
	if !strings.Contains(logs, "accepted=1") || !strings.Contains(logs, "panic") {
		t.Fatalf("expected log lines missing:\n%s", logs)
	}
	e.st.Close()
	var all []byte
	files, _ := filepath.Glob(filepath.Join(e.dir, "*"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
	}
	for _, ip := range ips {
		if strings.Contains(logs, ip) {
			t.Errorf("IP %s in logs:\n%s", ip, logs)
		}
		if bytes.Contains(all, []byte(ip)) {
			t.Errorf("IP %s in database files", ip)
		}
	}
}

func TestRedactIPs(t *testing.T) {
	cases := map[string]string{
		"from 10.0.0.1:5555":           "from [ip]:5555",
		"serving [2001:db8::1]:443: x": "serving [ip]:443: x",
		"::1 and fe80::1%en0":          "[ip] and [ip]",
		"app_version=1.2.0 n=42":       "app_version=1.2.0 n=42",
		"addr=:8080":                   "addr=:8080",
		"addr=[::]:8080":               "addr=[ip]:8080",
		"::ffff:192.0.2.1":             "[ip]:[ip]",
	}
	for in, want := range cases {
		if got := RedactIPs(in); got != want {
			t.Errorf("RedactIPs(%q) = %q, want %q", in, got, want)
		}
	}
}
