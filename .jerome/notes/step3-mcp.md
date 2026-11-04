# Step 3 (O3): MCP server inside `sonda serve`

Branch `server/3-mcp`, 2026-11-04. `go vet ./...`, `go test ./...` and `gofmt -l .` clean via Docker
(recipe notes/recipes/go-via-docker.md, smoke test updated for the token). Static build checked
(`CGO_ENABLED=0`, 12.8 MB stripped). Connecting Claude and curl smoke test: notes/recipes/mcp-connect.md
(curl part run against a local server; Claude Code / claude.ai not tried live, nothing is deployed).

## Commits
1. `internal/store/readonly.go`: `OpenReadOnly` (mode=ro + query_only + SQLite authorizer).
2. `internal/analytics`: the six queries, parameterized, with tests on seeded data.
3. Read-only side hides `apps.ingest_key_hash` (reads as NULL).
4. `internal/mcpserver`: official Go SDK, auth (A4), tool registration and descriptions; go.mod.
5. `internal/server` mounts `Config.MCP` at `/mcp/sonda`; `internal/cli` requires `SONDA_MCP_TOKEN`.
6. SDK logs filtered to Warn (it logged 3 Info lines per stateless request).

## Transport and SDK
- `github.com/modelcontextprotocol/go-sdk` v1.8.0 (official, pure Go; new deps: google/jsonschema-go,
  segmentio/encoding, yosida95/uritemplate, x/oauth2, x/time, x/sync; oauth2/jwt are not linked into our
  code paths but are in go.mod). Build stays static.
- Streamable HTTP, **stateless** and **JSON responses** (`StreamableHTTPOptions{Stateless, JSONResponse}`):
  every POST is self-contained, no session map in memory, no long-lived SSE stream (which would hit the
  30 s `WriteTimeout` and proxies). GET/DELETE answer 405. Tools need no server->client requests, so
  nothing is lost.
- `http.NewCrossOriginProtection` wraps the handler (a browser page cannot drive it cross-site even with a
  leaked token in a URL); Claude's clients send no Origin/Sec-Fetch-Site and pass. The SDK's DNS-rebinding
  guard (loopback listener + non-loopback Host -> 403) stays on; behind Coolify the listener is not loopback.

## Auth (A4)
- `SONDA_MCP_TOKEN`; `serve` exits 1 if unset **or shorter than 32 characters** (my addition: a short
  shared secret on a public URL is brute-forceable, and there is no rate limit on failures). Checked
  before the database is opened. Generate with `openssl rand -hex 32`.
- `Authorization: Bearer` (scheme case-insensitive) or `?token=`. If a Bearer header is present it wins
  (a wrong Bearer + right query = 401). `subtle.ConstantTimeCompare` on SHA-256 digests (no length leak).
- 401 body `{"error":"unauthorized"}`, `WWW-Authenticate: Bearer realm="sonda"`, `Cache-Control: no-store`.
- The token is removed from `r.URL` / `RequestURI` before the MCP handler. Logs: one line per tool call
  (`tool`, `ms`, `ok`, and the error text on failure), never arguments, URLs or tokens. Tests scan logs.

## Read-only enforcement (query tool and all other tools)
All tools run on a second pool opened by `store.OpenReadOnly` (max 4 connections):
1. `mode=ro` (SQLITE_OPEN_READONLY);
2. `PRAGMA query_only=1`;
3. an SQLite **authorizer** (modernc exposes it) installed on every connection through a private
   `sqlite.Driver` connection hook: allows SELECT, READ, RECURSIVE, functions except load_extension /
   fts3_tokenizer / readfile / writefile / edit, and the pragmas table_info, table_xinfo, table_list,
   index_list, index_info, index_xinfo, foreign_key_list (so `pragma_table_info('events')` works); denies
   everything else (ATTACH, DETACH, other PRAGMAs, transactions, DDL, DML, temp tables). `apps.ingest_key_hash`
   is IGNOREd (reads as NULL, cannot be filtered on).
4. In front, `analytics.CheckSelect`: a small lexer (comments, '' strings, "" ` [] identifiers) requires
   the first token SELECT or WITH, a single statement (only a trailing `;`), and rejects unquoted INSERT,
   UPDATE, DELETE, CREATE, DROP, ALTER, ATTACH, DETACH, PRAGMA, VACUUM, REINDEX, ANALYZE (catches
   `WITH ... DELETE`). Needed because modernc runs *all* statements of a multi-statement Query. Max 20 000 chars.
- Timeout 10 s (`context` -> sqlite3_interrupt; tested at 200 ms on an infinite recursive CTE), rows
  default 200 / max 1000 with `truncated`, cells: non-UTF-8 blobs -> `base64:...`, text cut at 4096 bytes.

## Tools (D14)
Common: `app` = name (exact, then case-insensitive) or numeric id; an unknown app returns an error listing
all apps. `from`/`to`: `YYYY-MM-DD` (UTC; a date in `to` includes the whole day) or RFC 3339; default
`to`=now, `from`=to-30 days. Range is `[from, to)` on `ts` (device time). Days/weeks are UTC, weeks start
Monday (`date(ts/1000,'unixepoch','weekday 0','-6 days')`). Props filters: `{"key": value}` equality,
keys validated `^[a-z][a-z0-9_]{0,63}$` (same as ingestion) and used only as bound paths
`json_extract(props, ?)`; numbers compare numerically (integers bound as int64), strings as text,
booleans as 1/0, null = missing. Max 10 prop filters, 50 names. Errors come back as tool results with
`isError` and a readable message. Outputs are structured (outputSchema inferred by the SDK) plus the
same JSON as text.

- **list_apps**: id, name, created_at, all-time events, users, first/last event ts, last received_at.
- **event_counts**: events + unique users; `names`, `props`; `group_by` up to 3 of name, app_version,
  build, os_version, storefront, region, locale, device_type, device_model, `prop:<key>` (column names
  from an allow-list, prop via bound path); `bucket` day|week. Returns `total` (computed separately: users
  cannot be summed) and `rows` (bucket asc, events desc, then group values; limit 500, max 5000).
- **funnel**: 2–10 steps `{name, props?}`, `by` user|session, `window_hours` default 168 (max 2160).
  One SQL reads matching events with a bitmask per step (CASE per step), ordered by (entity, ts, id);
  Go computes per entity the deepest step reached: every step-1 occurrence in `[from, to)` is tried,
  greedy earliest match for later steps, strictly later in (ts, id) order, distinct events, within the
  window (inclusive). Later steps may fall after `to` (events read up to `to + window`). Output count,
  conversion_from_previous / from_start (ratios rounded to 4 decimals, null when denominator 0).
  Cap 2 000 000 events read, then an error asking to narrow.
- **retention**: cohort = UTC week of the user's *first* `app.first_launch` (min over all time, so a
  reinstall does not create a new user), cohort range = `[from, to)`. Day N returned = any event (or only
  `return_event`) in `[t0 + N*24h, t0 + (N+1)*24h)`. `eligible` = users whose day-N window has ended;
  returned counts only eligible users; rate = returned/eligible. Days default [1, 7, 30], up to 5, 1–365.
  Per cohort and `overall`.
- **recent_events**: newest first by (ts, id); filters names, props, user_id, session_id; limit 50/max 500;
  props decoded as JSON with numbers preserved.
- **query**: as above. Description carries the schema, ms units and indexes.

## Tests
- store: read-only pool refuses INSERT/UPDATE/DELETE/CREATE/CREATE TEMP/DROP/PRAGMA writes/ATTACH (no
  file created)/BEGIN/VACUUM/load_extension; allows recursive CTEs, json functions, pragma_table_info;
  hash hidden; writer unaffected and visible to the reader.
- analytics (fixed clock, two apps): app resolution, ranges, props SQL/args and injection-shaped keys;
  counts (totals, by name, by prop, numeric vs string prop, day and Monday weeks, NULL group, limit,
  app isolation, 8 invalid inputs); funnel (order, wrong order, window 7 d / 10 d / 1 h, retry after a
  failed attempt, props filter, start before range, step after `to`, same-ms ordering by id, explicit
  range, by session, repeated step needs two events, empty -> null rates, depth unit test); retention
  (2 cohorts + a too-recent one, eligibility, return_event, reinstall, cohort before range, other app,
  user without first_launch, default range, invalid days); recent (order, limit/truncated, filters,
  props); query (CheckSelect 11 accepted / 30 rejected incl. comments, strings with `;`, `WITH ... DELETE`,
  multi-statement; row cap; blob/NULL/float/long text; pragma_table_info; timeout; direct writes on
  the pool refused).
- mcpserver: 13 auth cases on the middleware + token stripped from URL; short token refused; end to end
  with the SDK client over HTTP (6 tools listed, readOnlyHint, `app` required where expected, every tool
  called on seeded data, 7 error cases as `isError`, hash not readable, logs without token/args);
  `?token=` connection, no/wrong token cannot initialize, raw 401, GET 405, cross-site 403.
- cli: serve refuses without / with a short token and creates no DB; end-to-end over a socket now also
  calls MCP with `?token=` and checks the token and `token=` are absent from logs.

## Choices to record (proposals for decisions.md / open.md)
1. Official Go SDK v1.8.0, stateless + JSON responses (above).
2. `SONDA_MCP_TOKEN` minimum 32 characters (beyond A4).
3. Bearer header takes precedence over `?token=` when both are present.
4. Read-only enforcement in four layers, including an SQLite authorizer; `ingest_key_hash` hidden.
5. Tool semantics as listed (UTC days, Monday weeks, half-open ranges on device ts, funnel best attempt
   with window from step 1, retention day windows of 24 h from first launch with eligibility).
6. Retention keeps the D14 30-day default even though day-30 values then are always "not eligible";
   the tool description tells Claude to widen `from`. Alternative: default 90 days for retention only.

## Open points (for O6 security review / later)
- No rate limit on failed MCP auth (token is ≥ 32 chars; a per-process failure limiter could be added).
- Memory: a query like `SELECT zeroblob(1e9)` or a huge `printf` width can allocate up to SQLite's
  1 GB string limit within the 10 s timeout. Options: deny those functions in the authorizer, or a
  global `sqlite3_hard_heap_limit64` (affects the writer too). The token holder is trusted; left open.
- `?token=` can leak via reverse-proxy access logs (Coolify): documented in the recipe; prefer Bearer in
  Claude Code.
- `query` can read every app's events (by design, one admin token); other tools are app-scoped.
- `go test -race` still not run (needs CGO).
