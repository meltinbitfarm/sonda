package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
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
			"SONDA_DB_PATH": db, "SONDA_ADDR": "127.0.0.1:0",
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
