package server

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/meltinbitfarm/sonda/internal/ingest"
	"github.com/meltinbitfarm/sonda/internal/ingestkey"
	"github.com/meltinbitfarm/sonda/internal/ratelimit"
	"github.com/meltinbitfarm/sonda/internal/store"
)

// Rejection reports why one event of the batch was dropped.
type Rejection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// IngestResponse is the 200 body of POST /v1/events. Accepted includes
// Duplicates: events already stored under the same event_id (a retry), which
// the SDK can drop from its queue like any accepted event.
type IngestResponse struct {
	Accepted   int         `json:"accepted"`
	Duplicates int         `json:"duplicates"`
	Rejected   int         `json:"rejected"`
	Errors     []Rejection `json:"errors,omitempty"`
}

// ingest handles POST /v1/events. Order: auth (401), daily quota (429),
// rate limit (429), headers (415), body size and decoding (413/400), per-event validation,
// one transaction for the accepted events. See notes/ingest-contract.md.
func (s *server) ingest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	app, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	// The quota is checked before the batch and counted after it, so the
	// last accepted batch of the day may overshoot it by up to MaxBatch.
	if over, wait := s.quota.Exceeded(app.ID); over {
		w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(wait)))
		jsonError(w, http.StatusTooManyRequests, "daily event quota exceeded, retry after midnight UTC")
		return
	}

	if ok, wait := s.limiter.Allow(app.ID); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(wait)))
		jsonError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
			jsonError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
	}

	body, err := s.requestBody(w, r)
	if err != nil {
		jsonError(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}
	data, err := io.ReadAll(body)
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		jsonError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body larger than %d bytes", s.cfg.MaxBodyBytes))
		return
	case err != nil:
		jsonError(w, http.StatusBadRequest, "cannot read body")
		return
	}

	var batch ingest.Batch
	if err := json.Unmarshal(data, &batch); err != nil {
		jsonError(w, http.StatusBadRequest, `body must be a JSON object {"events": [...]}`)
		return
	}
	switch n := len(batch.Events); {
	case n == 0:
		jsonError(w, http.StatusBadRequest, "events must be a non-empty array")
		return
	case n > s.cfg.MaxBatch:
		jsonError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("more than %d events in one batch", s.cfg.MaxBatch))
		return
	}

	now := s.cfg.Now()
	resp := IngestResponse{}
	accepted := make([]store.Event, 0, len(batch.Events))
	for i, raw := range batch.Events {
		ev, err := ingest.Validate(raw, now)
		if err != nil {
			resp.Errors = append(resp.Errors, Rejection{Index: i, Reason: err.Error()})
			continue
		}
		accepted = append(accepted, ev)
	}
	dup, err := s.st.InsertEvents(r.Context(), app.ID, now, accepted)
	if err != nil {
		s.cfg.Logger.Error("ingest: insert failed", "app_id", app.ID, "events", len(accepted), "err", err)
		jsonError(w, http.StatusInternalServerError, "could not store events, retry later")
		return
	}
	if s.quota.Add(app.ID, len(accepted)-dup) {
		s.cfg.Logger.Warn("ingest: daily quota reached, refusing until midnight UTC", "app_id", app.ID, "quota", s.cfg.DailyQuota)
	}
	resp.Accepted, resp.Duplicates, resp.Rejected = len(accepted), dup, len(resp.Errors)
	s.cfg.Logger.Info("ingest", "app_id", app.ID, "accepted", resp.Accepted, "duplicates", resp.Duplicates, "rejected", resp.Rejected)
	writeJSON(w, http.StatusOK, resp)
}

// authenticate resolves the app from "Authorization: Bearer sk_...". The app
// always comes from the key, never from the body (D7).
func (s *server) authenticate(w http.ResponseWriter, r *http.Request) (store.App, bool) {
	scheme, key, found := strings.Cut(r.Header.Get("Authorization"), " ")
	key = strings.TrimSpace(key)
	// Malformed keys never reach SQLite: a flood of junk keys costs no
	// database connection (O6 M1).
	if !found || !strings.EqualFold(scheme, "Bearer") || !ingestkey.WellFormed(key) {
		unauthorized(w)
		return store.App{}, false
	}
	app, err := s.st.AppByKeyHash(r.Context(), ingestkey.Hash(key))
	if errors.Is(err, store.ErrNotFound) {
		unauthorized(w)
		return store.App{}, false
	}
	if err != nil {
		s.cfg.Logger.Error("ingest: key lookup failed", "err", err)
		jsonError(w, http.StatusServiceUnavailable, "temporarily unavailable")
		return store.App{}, false
	}
	return app, true
}

// requestBody limits the body to MaxBodyBytes and, with Content-Encoding:
// gzip, decompresses it and limits the decompressed size too (zip bombs).
func (s *server) requestBody(w http.ResponseWriter, r *http.Request) (io.Reader, error) {
	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
		return body, nil
	case "gzip":
		return &lazyGzip{w: w, src: body, limit: s.cfg.MaxBodyBytes}, nil
	default:
		return nil, errors.New("Content-Encoding must be gzip or absent")
	}
}

// lazyGzip opens the gzip stream on first read, so a size error on the
// compressed body surfaces from io.ReadAll as *http.MaxBytesError.
type lazyGzip struct {
	w     http.ResponseWriter
	src   io.ReadCloser
	limit int64
	r     io.Reader
}

func (z *lazyGzip) Read(p []byte) (int, error) {
	if z.r == nil {
		gz, err := gzip.NewReader(z.src)
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				return 0, err
			}
			return 0, errors.New("invalid gzip body")
		}
		z.r = http.MaxBytesReader(z.w, gz, z.limit)
	}
	return z.r.Read(p)
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="sonda"`)
	jsonError(w, http.StatusUnauthorized, "missing or invalid ingest key")
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
