// Package mcpserver exposes the analytics queries (D14) as an MCP server
// over Streamable HTTP, mounted at /mcp/sonda and protected by a single
// secret token (D7, D15, A4).
//
// It uses the official Go SDK (github.com/modelcontextprotocol/go-sdk), pure
// Go, so the binary stays static without CGO (D2). The transport runs
// stateless with JSON responses: every POST is self-contained, there are no
// sessions to keep in memory and no long-lived SSE streams to fight the
// server's write timeout or a reverse proxy.
package mcpserver

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/meltinbitfarm/sonda/internal/analytics"
)

// Path is where the MCP endpoint is mounted.
const Path = "/mcp/sonda"

// Version is reported to MCP clients.
const Version = "0.3.0"

// Options configures the MCP handler.
type Options struct {
	Token        string // SONDA_MCP_TOKEN, required, at least MinTokenLen characters
	Logger       *slog.Logger
	Now          func() time.Time // clock, for tests
	QueryTimeout time.Duration    // query tool; 0 means analytics.DefaultQueryTimeout
}

// New returns the authenticated MCP handler. db must be the read-only store
// (store.OpenReadOnly): every tool runs on it.
func New(db *sql.DB, opts Options) (http.Handler, error) {
	if len(opts.Token) < MinTokenLen {
		return nil, fmt.Errorf("SONDA_MCP_TOKEN must be at least %d characters (e.g. openssl rand -hex 32)", MinTokenLen)
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// The SDK logs every stateless connect/disconnect at Info: keep only its warnings.
	sdkLog := slog.New(minLevel{slog.LevelWarn, log.Handler()})
	svc := &analytics.Service{DB: db, Now: opts.Now, QueryTimeout: opts.QueryTimeout}
	srv := mcp.NewServer(&mcp.Implementation{Name: "sonda", Title: "Sonda analytics", Version: Version},
		&mcp.ServerOptions{Instructions: instructions, Logger: sdkLog})
	register(srv, svc, log)

	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: sdkLog})
	// Browsers cannot drive the endpoint cross-site; Claude's clients are
	// not browsers and send no Origin / Sec-Fetch-Site.
	protected := http.NewCrossOriginProtection().Handler(h)
	return RequireToken(opts.Token, protected), nil
}

// minLevel drops records below level.
type minLevel struct {
	level slog.Level
	slog.Handler
}

func (m minLevel) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= m.level && m.Handler.Enabled(ctx, l)
}

func (m minLevel) WithAttrs(as []slog.Attr) slog.Handler {
	return minLevel{m.level, m.Handler.WithAttrs(as)}
}

func (m minLevel) WithGroup(name string) slog.Handler {
	return minLevel{m.level, m.Handler.WithGroup(name)}
}

// addTool registers a read-only tool and logs one line per call: tool
// name, duration and outcome, never the arguments.
func addTool[In, Out any](srv *mcp.Server, log *slog.Logger, t *mcp.Tool, fn func(context.Context, In) (Out, error)) {
	t.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)}
	mcp.AddTool(srv, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		start := time.Now()
		out, err := fn(ctx, in)
		ms := time.Since(start).Milliseconds()
		if err != nil {
			log.Info("mcp tool", "tool", t.Name, "ms", ms, "ok", false, "err", err.Error())
			var zero Out
			return nil, zero, err
		}
		log.Info("mcp tool", "tool", t.Name, "ms", ms, "ok", true)
		return nil, out, nil
	})
}

func register(srv *mcp.Server, svc *analytics.Service, log *slog.Logger) {
	addTool(srv, log, &mcp.Tool{Name: "list_apps", Title: "List apps", Description: descListApps},
		func(ctx context.Context, _ struct{}) (analytics.AppsResult, error) { return svc.ListApps(ctx) })
	addTool(srv, log, &mcp.Tool{Name: "event_counts", Title: "Event counts", Description: descEventCounts}, svc.EventCounts)
	addTool(srv, log, &mcp.Tool{Name: "funnel", Title: "Funnel", Description: descFunnel}, svc.Funnel)
	addTool(srv, log, &mcp.Tool{Name: "retention", Title: "Retention", Description: descRetention}, svc.Retention)
	addTool(srv, log, &mcp.Tool{Name: "recent_events", Title: "Recent events", Description: descRecent}, svc.RecentEvents)
	addTool(srv, log, &mcp.Tool{Name: "query", Title: "Read-only SQL", Description: descQuery}, svc.Query)
}

const instructions = `Sonda holds product analytics events sent by Fabio's iOS/macOS apps (one database, several apps).

Start with list_apps; every other tool except query takes "app" (name or numeric id).
Time ranges: "from"/"to" accept YYYY-MM-DD (UTC; a date in "to" includes that whole day) or RFC 3339. Default: the last 30 days. All output times are UTC; days and weeks are UTC, weeks start on Monday. Event time is the device clock (ts).

Data model: one row per event with name, ts, user_id (random UUID per install, never a person), session_id (new after 30 minutes of inactivity), app_version, build, os_version, locale, region, storefront (App Store country, e.g. ITA), device_model (e.g. iPhone16,2), device_type (phone, pad, mac) and props (flat JSON object, snake_case keys).

Event names are area.action_past in snake_case. Standard catalogue (apps may add their own): app.first_launch, app.session_started, onboarding.step_viewed {step, step_name}, onboarding.completed, onboarding.skipped {at_step}, screen.viewed {screen}, feature.used {feature}, paywall.shown {placement, variant}, purchase.started / purchase.completed / purchase.cancelled {product_id}, purchase.failed {error}, purchase.restored, error.occurred {domain, code}.

Before filtering on props, look at real events with recent_events (or event_counts grouped by name) to learn which names and prop values exist and whether a prop is a number or a string. Prefer event_counts, funnel and retention; use query (read-only SQL) for anything they cannot express.`

const descListApps = `List all apps with their id, name, creation time and all-time totals: events, unique users, first/last event time (device clock) and last received time (server clock; shows whether data is still arriving). Call this first to get the app names or ids the other tools need.`

const descEventCounts = `Count events and unique users (distinct user_id) for one app over a time range (default: last 30 days).

Optional filters: "names" (event names) and "props" (equality on props, e.g. {"placement": "onboarding"}; numbers match numbers, strings match strings; null matches a missing prop).
Optional grouping: "group_by" with up to 3 of name, app_version, build, os_version, storefront, region, locale, device_type, device_model, prop:<key> (e.g. prop:screen); and "bucket": day or week (UTC, weeks start Monday, bucket = first day as YYYY-MM-DD).

Output: "total" (events and unique users over the whole filtered range; users cannot be summed across rows), and "rows" each with "group" (value per group_by key; null = field missing), optional "bucket", "events" and "users". Rows are sorted by bucket, then events descending. "truncated" is true if the row limit (default 500) cut the result.

Examples: daily active users = bucket day without group_by (users column); feature popularity = names ["feature.used"], group_by ["prop:feature"]; paywall views by placement = names ["paywall.shown"], group_by ["prop:placement"].`

const descFunnel = `Ordered conversion funnel for one app: how many users (or sessions, with by = "session") did step 1, then step 2, ... in that order.

Each step is an event name with an optional props equality filter, e.g. [{"name": "paywall.shown", "props": {"placement": "onboarding"}}, {"name": "purchase.started"}, {"name": "purchase.completed"}].
Rules: step 1 must happen in [from, to) (default last 30 days); each next step must be a later event (by time, then arrival order) within window_hours of that step 1 (default 168 = 7 days); every step-1 occurrence is tried and the furthest progress counts; later steps may fall after "to" if inside the window. With by = "session" all steps must be in the same session (the window still applies).

Output per step: "count" (entities that reached it), "conversion_from_previous" and "conversion_from_start" as ratios 0..1 (null when the denominator is 0).`

const descRetention = `Weekly retention cohorts for one app, from app.first_launch.

A user's cohort is the UTC week (Monday) of their first app.first_launch; only users whose first launch is in [from, to) are included (default last 30 days; day-30 numbers need cohorts older than 31 days, so pass e.g. from = 90 days ago). For each day N in "days" (default [1, 7, 30]) a user "returned" if they sent any event (or only "return_event", e.g. app.session_started) in the 24 hours starting exactly N*24h after their first launch.

Output per cohort ("week_start", "users") and "overall": for each day "eligible" (users whose day-N window has already ended; younger users are left out instead of counted as lost), "returned" and "rate" = returned / eligible (null when nobody is eligible yet).`

const descRecent = `Newest raw events for one app (newest first by device time), to check instrumentation: which event names, props and fields are really being sent, or to follow one user_id / session_id.

Filters: from/to (default last 30 days), names, props (equality), user_id, session_id. "limit" default 50, max 500; "truncated" tells if more matched.
Each event has event_id, name, ts (device time), received_at (server time), user_id, session_id, app_version, build, os_version, locale, region, storefront, device_model, device_type (null when not sent) and props (JSON object).`

const descQuery = `Run one read-only SQL statement (SQLite dialect) when the other tools cannot express the question. Must start with SELECT or WITH; a single statement; 10 s timeout; at most max_rows rows (default 200, max 1000; "truncated" tells if more existed). Writes, ATTACH, PRAGMA statements and transactions are rejected; the connection itself is read-only.

Schema:
  apps(id INTEGER PRIMARY KEY, name TEXT UNIQUE, created_at INTEGER)
  events(id INTEGER PRIMARY KEY, app_id INTEGER -> apps.id, event_id TEXT, name TEXT, ts INTEGER, received_at INTEGER,
         user_id TEXT, session_id TEXT, app_version TEXT, build TEXT, os_version TEXT, locale TEXT, region TEXT,
         storefront TEXT, device_model TEXT, device_type TEXT, props TEXT)
  Indexes: events(app_id, name, ts), events(app_id, user_id, ts), events(app_id, session_id, ts), unique events(app_id, event_id).

Notes: ts (device time) and received_at / created_at (server time) are INTEGER milliseconds since the Unix epoch, UTC: use datetime(ts / 1000, 'unixepoch') or date(ts / 1000, 'unixepoch') to read them, and compare with e.g. ts >= unixepoch('2026-10-01') * 1000. props is a JSON object text: json_extract(props, '$.screen'). Optional text columns are NULL when not sent; device_type is phone, pad or mac. Always filter by app_id (get it from list_apps) and a ts range so the indexes are used. Output: "columns" and "rows" (arrays of values in column order); blobs that are not UTF-8 come back as "base64:..."; text longer than 4096 bytes is cut.`
