package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meltinbitfarm/sonda/internal/ingestkey"
	"github.com/meltinbitfarm/sonda/internal/store"
)

func envFor(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func run(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, envFor(env), &out, &errb)
	return code, out.String(), errb.String()
}

func TestAppCreateAndList(t *testing.T) {
	db := filepath.Join(t.TempDir(), "sonda.db")
	env := map[string]string{"SONDA_DB_PATH": db}

	code, out, errOut := run(t, env, "app", "create", "  Pulso  ")
	if code != 0 {
		t.Fatalf("create: code %d, stderr %q", code, errOut)
	}
	key := strings.TrimSpace(out)
	if !strings.HasPrefix(key, ingestkey.Prefix) || strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout should be just the key, got %q", out)
	}

	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	app, err := st.AppByKeyHash(context.Background(), ingestkey.Hash(key))
	st.Close()
	if err != nil || app.Name != "Pulso" {
		t.Fatalf("lookup printed key: %+v, %v", app, err)
	}

	if code, _, errOut := run(t, env, "app", "create", "Pulso"); code != 1 || !strings.Contains(errOut, "already exists") {
		t.Fatalf("duplicate: code %d, stderr %q", code, errOut)
	}
	for _, bad := range []string{"", "   ", "a\nb", strings.Repeat("x", 101)} {
		if code, _, _ := run(t, env, "app", "create", bad); code != 1 {
			t.Errorf("name %q: code %d, want 1", bad, code)
		}
	}

	code, out, _ = run(t, env, "app", "list")
	if code != 0 || !strings.Contains(out, "Pulso") || strings.Contains(out, key) {
		t.Fatalf("list: code %d, out %q", code, out)
	}
}

func TestUsage(t *testing.T) {
	if code, _, errOut := run(t, nil); code != 2 || !strings.Contains(errOut, "usage") {
		t.Fatalf("no args: %d %q", code, errOut)
	}
	if code, _, _ := run(t, nil, "app", "create"); code != 2 {
		t.Fatalf("missing name: %d", code)
	}
	if code, out, _ := run(t, nil, "help"); code != 0 || !strings.Contains(out, "usage") {
		t.Fatalf("help: %d", code)
	}
}

func TestServe(t *testing.T) {
	db := filepath.Join(t.TempDir(), "sonda.db")
	addrc := make(chan string, 1)
	onListen = func(a string) { addrc <- a }
	t.Cleanup(func() { onListen = nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"serve"}, envFor(map[string]string{
			"SONDA_DB_PATH": db, "SONDA_ADDR": "127.0.0.1:0", "SONDA_MCP_TOKEN": testToken,
		}), io.Discard, io.Discard)
	}()

	var addr string
	select {
	case addr = <-addrc:
	case code := <-done:
		t.Fatalf("serve exited early with %d", code)
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not start")
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok\n" {
		t.Fatalf("healthz = %d %q", resp.StatusCode, body)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exit code %d", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not shut down")
	}
}

const testToken = "test-mcp-token-0123456789abcdef0123456789abcdef"

// A4: serve refuses to start without a (long enough) MCP token, before
// creating the database.
func TestServeRequiresMCPToken(t *testing.T) {
	for token, want := range map[string]string{"": "SONDA_MCP_TOKEN is not set", "short": "at least 32 characters"} {
		db := filepath.Join(t.TempDir(), "sonda.db")
		code, _, errOut := run(t, map[string]string{"SONDA_DB_PATH": db, "SONDA_ADDR": "127.0.0.1:0", "SONDA_MCP_TOKEN": token}, "serve")
		if code != 1 || !strings.Contains(errOut, want) {
			t.Errorf("token %q: exit %d, stderr %q", token, code, errOut)
		}
		if _, err := os.Stat(db); !os.IsNotExist(err) {
			t.Errorf("token %q: database created", token)
		}
	}
}

func TestServerConfigFromEnv(t *testing.T) {
	cfg, err := serverConfig(envFor(nil))
	if err != nil || cfg.RatePerMinute != 60 || cfg.MaxBatch != 100 || cfg.MaxBodyBytes != 256<<10 {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	cfg, err = serverConfig(envFor(map[string]string{
		"SONDA_INGEST_RATE_PER_MIN": "120", "SONDA_INGEST_MAX_BATCH": "50", "SONDA_INGEST_MAX_BODY_BYTES": "1024",
	}))
	if err != nil || cfg.RatePerMinute != 120 || cfg.MaxBatch != 50 || cfg.MaxBodyBytes != 1024 {
		t.Fatalf("custom: %+v %v", cfg, err)
	}
	for _, bad := range []string{"0", "-1", "abc", "1.5", "99999999999"} {
		if _, err := serverConfig(envFor(map[string]string{"SONDA_INGEST_MAX_BATCH": bad})); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if code, _, errOut := run(t, map[string]string{"SONDA_INGEST_RATE_PER_MIN": "x"}, "serve"); code != 1 || !strings.Contains(errOut, "SONDA_INGEST_RATE_PER_MIN") {
		t.Fatalf("serve with bad env: %d %q", code, errOut)
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// End to end over a real socket: the client address is 127.0.0.1 and must
// not appear in the logs (D9).
func TestServeIngestEndToEnd(t *testing.T) {
	db := filepath.Join(t.TempDir(), "sonda.db")
	_, out, _ := run(t, map[string]string{"SONDA_DB_PATH": db}, "app", "create", "Demo")
	key := strings.TrimSpace(out)

	addrc := make(chan string, 1)
	onListen = func(a string) { addrc <- a }
	t.Cleanup(func() { onListen = nil })
	logs := &lockedBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"serve"}, envFor(map[string]string{
			"SONDA_DB_PATH": db, "SONDA_ADDR": "127.0.0.1:0", "SONDA_INGEST_RATE_PER_MIN": "1",
			"SONDA_MCP_TOKEN": testToken,
		}), io.Discard, logs)
	}()
	var addr string
	select {
	case addr = <-addrc:
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not start")
	}

	post := func() *http.Response {
		ts := time.Now().UnixMilli()
		body := fmt.Sprintf(`{"events":[{"event_id":"3f2504e0-4f89-11d3-9a0c-0305e82c3301","name":"app.first_launch","ts":%d,"user_id":"0f8fad5b-d9cb-469f-a165-70867728950e","session_id":"7c9e6679-7425-40de-944b-e07fc1f90ae7"}]}`, ts)
		req, _ := http.NewRequest("POST", "http://"+addr+"/v1/events", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "198.51.100.23")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}
	if resp := post(); resp.StatusCode != 200 {
		t.Fatalf("ingest: %d", resp.StatusCode)
	}
	if resp := post(); resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("rate limit from env: %d", resp.StatusCode)
	}

	// MCP with the token in the URL (as Claude's remote connector does):
	// the event just ingested is visible, and the token never reaches the logs.
	mcpCall := func(token string) (int, string) {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_apps","arguments":{}}}`
		req, _ := http.NewRequest("POST", "http://"+addr+"/mcp/sonda?token="+token, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	if code, body := mcpCall(testToken); code != 200 || !strings.Contains(body, `\"events\":1`) {
		t.Fatalf("mcp list_apps: %d %s", code, body)
	}
	if code, _ := mcpCall(testToken + "x"); code != 401 {
		t.Fatalf("mcp wrong token: %d", code)
	}

	cancel()
	<-done
	l := logs.String()
	if !strings.Contains(l, "accepted=1") || !strings.Contains(l, "tool=list_apps") {
		t.Fatalf("missing log lines:\n%s", l)
	}
	if strings.Contains(l, testToken[:16]) || strings.Contains(l, "token=") {
		t.Errorf("token in logs:\n%s", l)
	}
	for _, ip := range []string{"127.0.0.1", "198.51.100.23"} {
		if strings.Contains(l, ip) {
			t.Errorf("%s in logs:\n%s", ip, l)
		}
	}
}
