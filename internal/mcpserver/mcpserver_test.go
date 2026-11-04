package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/meltinbitfarm/sonda/internal/store"
)

const token = "0123456789abcdef0123456789abcdef-secret"

func TestRequireToken(t *testing.T) {
	var seen *http.Request
	h := RequireToken(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.WriteHeader(204)
	}))
	for _, c := range []struct {
		name, url, auth string
		want            int
	}{
		{"no token", "/mcp/sonda", "", 401},
		{"bearer", "/mcp/sonda", "Bearer " + token, 204},
		{"bearer lowercase scheme", "/mcp/sonda", "bearer " + token, 204},
		{"query", "/mcp/sonda?token=" + token, "", 204},
		{"wrong bearer", "/mcp/sonda", "Bearer " + token + "x", 401},
		{"prefix of token", "/mcp/sonda", "Bearer " + token[:10], 401},
		{"empty bearer", "/mcp/sonda", "Bearer ", 401},
		{"basic scheme", "/mcp/sonda", "Basic " + token, 401},
		{"raw header", "/mcp/sonda", token, 401},
		{"wrong query", "/mcp/sonda?token=nope", "", 401},
		{"empty query", "/mcp/sonda?token=", "", 401},
		{"header wins over query", "/mcp/sonda?token=" + token, "Bearer wrong", 401},
		{"basic header, good query", "/mcp/sonda?token=" + token, "Basic abc", 204},
	} {
		seen = nil
		req := httptest.NewRequest("POST", c.url, nil)
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
			continue
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: no Cache-Control", c.name)
		}
		if c.want == 401 {
			if seen != nil || rec.Body.String() != `{"error":"unauthorized"}`+"\n" ||
				rec.Header().Get("WWW-Authenticate") != `Bearer realm="sonda"` || strings.Contains(rec.Body.String(), token) {
				t.Errorf("%s: 401 response %q %v", c.name, rec.Body.String(), rec.Header())
			}
		}
	}

	// The token is removed from the URL handed to the MCP handler; other
	// parameters stay.
	seen = nil
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp/sonda?a=1&token="+token+"&b=2", nil))
	if seen == nil || strings.Contains(seen.URL.String(), token) || strings.Contains(seen.RequestURI, token) ||
		seen.URL.Query().Get("a") != "1" || seen.URL.Query().Get("b") != "2" {
		t.Fatalf("token not stripped: %v %q", seen.URL, seen.RequestURI)
	}
}

func TestNewRejectsShortToken(t *testing.T) {
	for _, tok := range []string{"", "short", strings.Repeat("x", MinTokenLen-1)} {
		if _, err := New(nil, Options{Token: tok}); err == nil {
			t.Errorf("token %q accepted", tok)
		}
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

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// setup seeds a database and serves the MCP handler on a real socket.
func setup(t *testing.T) (*httptest.Server, *lockedBuf) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	app, err := st.CreateApp(ctx, "Demo", "h", now)
	if err != nil {
		t.Fatal(err)
	}
	ev := func(id, name string, ago time.Duration, user, props string) store.Event {
		return store.Event{EventID: "00000000-0000-4000-8000-" + id, Name: name, TS: now.Add(-ago).UnixMilli(),
			UserID: user, SessionID: user, DeviceType: "phone", Props: props}
	}
	u1, u2 := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	if _, err := st.InsertEvents(ctx, app.ID, now, []store.Event{
		ev("000000000001", "app.first_launch", 48*time.Hour, u1, ""),
		ev("000000000002", "paywall.shown", 47*time.Hour, u1, `{"placement":"onboarding"}`),
		ev("000000000003", "purchase.completed", 46*time.Hour, u1, `{"product_id":"pro"}`),
		ev("000000000004", "app.session_started", 23*time.Hour, u1, ""),
		ev("000000000005", "app.first_launch", 10*time.Hour, u2, ""),
		ev("000000000006", "paywall.shown", 9*time.Hour, u2, `{"placement":"onboarding"}`),
	}); err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	logs := &lockedBuf{}
	h, err := New(ro.DB, Options{Token: token, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(Path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, logs
}

func connect(t *testing.T, endpoint string, hc *http.Client) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args any) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out map[string]any
	if !res.IsError {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%s: structured content %s: %v", name, b, err)
		}
		// The text content carries the same JSON for clients without structured output.
		if tc, ok := res.Content[0].(*mcp.TextContent); !ok || !json.Valid([]byte(tc.Text)) {
			t.Errorf("%s: text content is not JSON", name)
		}
	}
	return out, res
}

func errText(res *mcp.CallToolResult) string {
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

func TestMCPEndToEnd(t *testing.T) {
	srv, logs := setup(t)
	cs := connect(t, srv.URL+Path, &http.Client{Transport: bearerTransport{token}})

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint || len(tl.Description) < 100 {
			t.Errorf("%s: annotations %+v, description %d chars", tl.Name, tl.Annotations, len(tl.Description))
		}
		schema, _ := json.Marshal(tl.InputSchema)
		needsApp := tl.Name != "list_apps" && tl.Name != "query"
		if needsApp != strings.Contains(string(schema), `"required":["app"`) {
			t.Errorf("%s: input schema %s", tl.Name, schema)
		}
	}
	slices.Sort(names)
	if strings.Join(names, ",") != "event_counts,funnel,list_apps,query,recent_events,retention" {
		t.Fatalf("tools = %v", names)
	}

	out, _ := call(t, cs, "list_apps", map[string]any{})
	if apps := out["apps"].([]any); len(apps) != 1 || apps[0].(map[string]any)["events"] != 6.0 {
		t.Errorf("list_apps = %v", out)
	}

	out, _ = call(t, cs, "event_counts", map[string]any{"app": "demo", "group_by": []string{"name"}})
	if total := out["total"].(map[string]any); total["events"] != 6.0 || total["users"] != 2.0 {
		t.Errorf("event_counts = %v", out)
	}

	out, _ = call(t, cs, "funnel", map[string]any{"app": "Demo", "steps": []map[string]any{
		{"name": "paywall.shown", "props": map[string]any{"placement": "onboarding"}}, {"name": "purchase.completed"},
	}})
	steps := out["steps"].([]any)
	if s2 := steps[1].(map[string]any); steps[0].(map[string]any)["count"] != 2.0 || s2["count"] != 1.0 || s2["conversion_from_previous"] != 0.5 {
		t.Errorf("funnel = %v", out)
	}

	out, _ = call(t, cs, "retention", map[string]any{"app": "1", "days": []int{1}})
	overall := out["overall"].(map[string]any)
	if overall["users"] != 2.0 {
		t.Errorf("retention = %v", out)
	}
	if d := overall["days"].([]any)[0].(map[string]any); d["eligible"] != 1.0 || d["returned"] != 1.0 || d["rate"] != 1.0 {
		t.Errorf("retention day 1 = %v", d)
	}

	out, _ = call(t, cs, "recent_events", map[string]any{"app": "Demo", "names": []string{"purchase.completed"}})
	evs := out["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["props"].(map[string]any)["product_id"] != "pro" {
		t.Errorf("recent_events = %v", out)
	}

	out, _ = call(t, cs, "query", map[string]any{"sql": "SELECT name, count(*) AS n FROM events WHERE app_id = 1 GROUP BY name ORDER BY n DESC, name LIMIT 1"})
	if b, _ := json.Marshal(out); string(b) != `{"columns":["name","n"],"row_count":1,"rows":[["app.first_launch",2]],"truncated":false}` {
		t.Errorf("query = %s", b)
	}

	// Tool errors come back as results with isError, with a useful message.
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"event_counts", map[string]any{"app": "Nope"}, `available apps: "Demo" (id 1)`},
		{"event_counts", map[string]any{"app": "Demo", "group_by": []string{"prop:Bad Key"}}, "invalid prop key"},
		{"funnel", map[string]any{"app": "Demo", "steps": []map[string]any{{"name": "a.b"}}}, "2 to 10 steps"},
		{"query", map[string]any{"sql": "DELETE FROM events"}, "only SELECT or WITH"},
		{"query", map[string]any{"sql": "SELECT 1; DROP TABLE events"}, "only one statement"},
		{"query", map[string]any{"sql": "WITH x AS (SELECT 1) DELETE FROM events"}, "DELETE is not allowed"},
		{"query", map[string]any{"sql": "SELECT ingest_key_hash FROM apps WHERE ingest_key_hash IS NOT NULL"}, ""},
		{"event_counts", map[string]any{"app": "Demo", "unknown": 1}, "unknown"},
	} {
		out, res := call(t, cs, c.tool, c.args)
		if c.want == "" { // not an error, but the hash is hidden
			if out["row_count"] != 0.0 {
				t.Errorf("ingest_key_hash visible: %v", out)
			}
			continue
		}
		if !res.IsError || !strings.Contains(errText(res), c.want) {
			t.Errorf("%s %v: isError=%v %q", c.tool, c.args, res.IsError, errText(res))
		}
	}
	out, _ = call(t, cs, "event_counts", map[string]any{"app": "Demo"})
	if out["total"].(map[string]any)["events"] != 6.0 {
		t.Errorf("data changed: %v", out)
	}

	l := logs.String()
	if !strings.Contains(l, "tool=funnel") || !strings.Contains(l, "ok=false") || strings.Contains(l, token) ||
		strings.Contains(l, "onboarding") || strings.Contains(l, "session connected") {
		t.Errorf("logs:\n%s", l)
	}
}

func TestMCPAuthOverHTTP(t *testing.T) {
	srv, logs := setup(t)

	// The SDK client works with ?token= in the URL, as Claude's connectors do.
	cs := connect(t, srv.URL+Path+"?token="+token, nil)
	if out, _ := call(t, cs, "list_apps", map[string]any{}); len(out["apps"].([]any)) != 1 {
		t.Fatalf("list_apps via ?token= = %v", out)
	}

	// Without or with a wrong token the client cannot even initialize.
	for _, ep := range []string{srv.URL + Path, srv.URL + Path + "?token=wrong"} {
		c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		if cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ep, MaxRetries: -1}, nil); err == nil {
			cs.Close()
			t.Errorf("%s: connected", ep)
		}
	}

	post := func(auth, body string) (int, string) {
		req, _ := http.NewRequest("POST", srv.URL+Path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	listTools := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if code, body := post("", listTools); code != 401 || strings.Contains(body, "list_apps") {
		t.Errorf("no token: %d %s", code, body)
	}
	if code, body := post("Bearer "+token, listTools); code != 200 || !strings.Contains(body, "list_apps") {
		t.Errorf("bearer: %d %s", code, body)
	}

	// Stateless transport: GET (standalone SSE stream) is not offered.
	req, _ := http.NewRequest("GET", srv.URL+Path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d", resp.StatusCode)
	}

	// A cross-site browser request is refused even with the token.
	req, _ = http.NewRequest("POST", srv.URL+Path, strings.NewReader(listTools))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site = %d", resp.StatusCode)
	}

	if strings.Contains(logs.String(), token) {
		t.Errorf("token in logs:\n%s", logs.String())
	}
}
