// Package cli implements the sonda command line (D6).
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/meltinbitfarm/sonda/internal/ingestkey"
	"github.com/meltinbitfarm/sonda/internal/mcpserver"
	"github.com/meltinbitfarm/sonda/internal/server"
	"github.com/meltinbitfarm/sonda/internal/store"
)

// Defaults, overridable with SONDA_ADDR and SONDA_DB_PATH.
const (
	DefaultAddr   = ":8080"
	DefaultDBPath = "/data/sonda.db"
	maxAppNameLen = 100
)

const usage = `usage:
  sonda serve                 start the HTTP server
  sonda app create "<name>"   create an app and print its ingest key
  sonda app rotate-key "<name>"
                              replace the app's ingest key and print the new one;
                              the old key stops working immediately
  sonda app list              list apps

environment:
  SONDA_ADDR      listen address (default :8080)
  SONDA_DB_PATH   SQLite database path (default /data/sonda.db)
  SONDA_MCP_TOKEN secret for the MCP endpoint /mcp/sonda, required by serve,
                  at least 32 characters (e.g. openssl rand -hex 32)
  SONDA_INGEST_RATE_PER_MIN    requests per minute per ingest key (default 60)
  SONDA_INGEST_MAX_BATCH       events per request (default 100)
  SONDA_INGEST_MAX_BODY_BYTES  request body size, also after gzip (default 262144)
`

// onListen, if set, is called with the bound address once serve is listening (tests).
var onListen func(addr string)

// Run executes the command in args (without the program name) and returns the exit code.
func Run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	env := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	dbPath := env("SONDA_DB_PATH", DefaultDBPath)

	var err error
	switch {
	case len(args) == 1 && args[0] == "serve":
		var cfg server.Config
		if cfg, err = serverConfig(getenv); err == nil {
			err = serve(ctx, env("SONDA_ADDR", DefaultAddr), dbPath, getenv("SONDA_MCP_TOKEN"), cfg, stderr)
		}
	case len(args) == 3 && args[0] == "app" && args[1] == "create":
		err = appCreate(ctx, dbPath, args[2], stdout, stderr)
	case len(args) == 3 && args[0] == "app" && args[1] == "rotate-key":
		err = appRotateKey(ctx, dbPath, args[2], stdout, stderr)
	case len(args) == 2 && args[0] == "app" && args[1] == "list":
		err = appList(ctx, dbPath, stdout)
	case len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help"):
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "sonda: %v\n", err)
		return 1
	}
	return 0
}

// serverConfig reads the ingest limits (A2) from the environment. Unset
// variables keep the defaults; set ones must be positive integers.
func serverConfig(getenv func(string) string) (server.Config, error) {
	cfg := server.Config{
		RatePerMinute: server.DefaultRatePerMinute,
		MaxBatch:      server.DefaultMaxBatch,
		MaxBodyBytes:  server.DefaultMaxBodyBytes,
	}
	for _, v := range []struct {
		name string
		dst  func(int64)
	}{
		{"SONDA_INGEST_RATE_PER_MIN", func(n int64) { cfg.RatePerMinute = int(n) }},
		{"SONDA_INGEST_MAX_BATCH", func(n int64) { cfg.MaxBatch = int(n) }},
		{"SONDA_INGEST_MAX_BODY_BYTES", func(n int64) { cfg.MaxBodyBytes = n }},
	} {
		s := getenv(v.name)
		if s == "" {
			continue
		}
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil || n <= 0 {
			return server.Config{}, fmt.Errorf("%s must be a positive integer, got %q", v.name, s)
		}
		v.dst(n)
	}
	return cfg, nil
}

func serve(ctx context.Context, addr, dbPath, mcpToken string, cfg server.Config, stderr io.Writer) error {
	// A4: no MCP token, no server. Checked before touching the database.
	if mcpToken == "" {
		return errors.New("SONDA_MCP_TOKEN is not set")
	}
	if len(mcpToken) < mcpserver.MinTokenLen {
		return fmt.Errorf("SONDA_MCP_TOKEN must be at least %d characters (e.g. openssl rand -hex 32)", mcpserver.MinTokenLen)
	}
	log := server.NewLogger(stderr)
	cfg.Logger = log
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()
	ro, err := store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open database read-only: %w", err)
	}
	defer ro.Close()
	if cfg.MCP, err = mcpserver.New(ro.DB, mcpserver.Options{Token: mcpToken, Logger: log}); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           server.New(st, cfg),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	log.Info("listening", "addr", ln.Addr().String(), "db", dbPath,
		"rate_per_min", cfg.RatePerMinute, "max_batch", cfg.MaxBatch, "max_body_bytes", cfg.MaxBodyBytes,
		"mcp", mcpserver.Path)
	if onListen != nil {
		onListen(ln.Addr().String())
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func validateAppName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errors.New("app name is empty")
	case !utf8.ValidString(name):
		return "", errors.New("app name is not valid UTF-8")
	case utf8.RuneCountInString(name) > maxAppNameLen:
		return "", fmt.Errorf("app name longer than %d characters", maxAppNameLen)
	case strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "", errors.New("app name contains control characters")
	}
	return name, nil
}

func appCreate(ctx context.Context, dbPath, name string, stdout, stderr io.Writer) error {
	name, err := validateAppName(name)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	key, hash := ingestkey.Generate()
	app, err := st.CreateApp(ctx, name, hash, time.Now())
	if errors.Is(err, store.ErrDuplicate) {
		return fmt.Errorf("an app named %q already exists", name)
	}
	if err != nil {
		return err
	}
	// The key goes alone to stdout so it can be captured; it cannot be shown again.
	fmt.Fprintf(stderr, "created app %q (id %d). Ingest key below: copy it now, it is not stored and cannot be shown again.\n", app.Name, app.ID)
	fmt.Fprintln(stdout, key)
	return nil
}

func appRotateKey(ctx context.Context, dbPath, name string, stdout, stderr io.Writer) error {
	name, err := validateAppName(name)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	key, hash := ingestkey.Generate()
	app, err := st.RotateKey(ctx, name, hash)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no app named %q (sonda app list shows the names)", name)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "rotated the ingest key of app %q (id %d): the old key no longer works. New key below: copy it now, it is not stored and cannot be shown again.\n", app.Name, app.ID)
	fmt.Fprintln(stdout, key)
	return nil
}

func appList(ctx context.Context, dbPath string, stdout io.Writer) error {
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()
	apps, err := st.ListApps(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tCREATED")
	for _, a := range apps {
		fmt.Fprintf(tw, "%d\t%s\t%s\n", a.ID, a.Name, a.CreatedAt.Format(time.RFC3339))
	}
	return tw.Flush()
}
