// Package server exposes Sonda's HTTP API: GET /healthz, POST /v1/events
// and, when configured, the MCP endpoint at /mcp/sonda.
//
// D9: the client IP is never read, stored or logged. Handlers do not look at
// r.RemoteAddr or forwarding headers, panics are recovered here (so net/http
// does not log "panic serving <addr>"), and NewLogger redacts anything that
// looks like an IP address as a second line of defence.
package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/meltinbitfarm/sonda/internal/ratelimit"
	"github.com/meltinbitfarm/sonda/internal/store"
)

// Defaults for the ingest limits (A2).
const (
	DefaultRatePerMinute = 60
	DefaultMaxBatch      = 100
	DefaultMaxBodyBytes  = 256 << 10
)

// Config holds the server settings. Zero fields take the defaults.
type Config struct {
	Logger        *slog.Logger
	RatePerMinute int              // requests per minute per ingest key
	MaxBatch      int              // events per request
	MaxBodyBytes  int64            // request body, after gzip decompression too
	Now           func() time.Time // clock, for tests
	MCP           http.Handler     // mounted at /mcp/sonda if set (mcpserver.New, already authenticated)
}

func (c *Config) defaults() {
	if c.Logger == nil {
		c.Logger = NewLogger(io.Discard)
	}
	if c.RatePerMinute <= 0 {
		c.RatePerMinute = DefaultRatePerMinute
	}
	if c.MaxBatch <= 0 {
		c.MaxBatch = DefaultMaxBatch
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

type server struct {
	st      *store.Store
	cfg     Config
	limiter *ratelimit.Limiter
}

// New returns the HTTP handler.
func New(st *store.Store, cfg Config) http.Handler {
	cfg.defaults()
	s := &server{st: st, cfg: cfg, limiter: ratelimit.New(cfg.RatePerMinute, cfg.Now)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /v1/events", s.ingest)
	if cfg.MCP != nil {
		mux.Handle("/mcp/sonda", cfg.MCP)
	}
	return s.recoverPanics(mux)
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.st.DB.PingContext(ctx); err != nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}

// recoverPanics answers 500 and logs the panic without the client address,
// instead of letting net/http log "http: panic serving <ip>:<port>".
func (s *server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			s.cfg.Logger.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
			http.Error(w, "internal error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}
