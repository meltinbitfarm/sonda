# Step 2 (O2): POST /v1/events

Branch `server/2-ingest`, 2026-11-04. `go vet ./...`, `go test ./...` and `gofmt -l .` clean via Docker
(recipe notes/recipes/go-via-docker.md, unchanged). Contract for the SDK: notes/ingest-contract.md.

## Commits
1. Store: `InsertEvents(ctx, appID, receivedAt, []Event)`: one transaction, prepared statement, empty optional strings -> NULL.
2. `internal/ingest`: wire format and per-event validation (`Validate(raw, now) (store.Event, error)`).
3. `internal/ratelimit`: in-memory token bucket per app ID.
4. `internal/server`: handler, `Config`, panic recovery, IP-redacting logger; `internal/cli`: env vars.
5. Notes (this file, the contract).

## Configuration (new env vars, read by `sonda serve`)
- `SONDA_INGEST_RATE_PER_MIN` (default 60), `SONDA_INGEST_MAX_BATCH` (100), `SONDA_INGEST_MAX_BODY_BYTES` (262144).
- Must be positive integers; anything else makes `serve` exit 1 with the variable name. Values are logged at startup.
- `server.New(st, server.Config{...})` signature changed (was `New(st)`); zero fields take defaults.

## Request handling order
auth (401) -> rate limit (429) -> Content-Type / Content-Encoding (415) -> body read under
`http.MaxBytesReader` (413) -> JSON shape (400) -> batch size (413) -> validate each event -> one
transaction for the accepted ones -> 200 with counts. Rate limit runs after auth and is keyed on app ID,
so a bad key cannot spend another app's budget and the bucket map is bounded by the number of apps.

## Choices to record (proposals for decisions.md / open.md)
1. Body shape `{"events":[...]}` (object, not bare array): room for batch-level fields later without a v2.
2. `ts` is integer milliseconds, matching storage (step 1, choice 2). Accepted window: now-31 d .. now+24 h.
   Justification in the contract. Bounds are constants in `internal/ingest`, not env.
3. `user_id` and `session_id` must be UUIDs, normalised to lowercase. Keeps emails/names out of the column
   (D10, D11) and merges Swift's uppercase `uuidString` with lowercase. Answers step 1's open point:
   missing ids -> event rejected.
4. `device_type` restricted to phone/pad/mac (D10). A future visionOS/watchOS app needs a contract change.
5. Prop keys must be snake_case ≤ 64 chars (not stated by D13; follows the D12 catalogue). Props are stored
   re-encoded compact with sorted keys and numbers kept verbatim (`json.Number`); 2 KB measured on that form.
6. Unknown event fields are ignored, not rejected (forward compatibility of SDK -> server). `app_id` in the body is ignored.
7. Optional strings ≤ 64 bytes, no control chars; empty = NULL.
8. All-rejected batch is still 200 (retrying cannot help); 413 for both oversize body and >100 events.
9. Gzip: `Content-Encoding: gzip` accepted; the 256 KB limit applies to the compressed stream and again to
   the decompressed one (zip-bomb guard). Other encodings -> 415.
10. Auth scheme `Bearer` is case-insensitive; key must start with `sk_` before hitting the DB.
11. D9 in practice: handlers never read `RemoteAddr` or forwarding headers; a panic-recovery middleware
    replaces net/http's "panic serving <ip>" log; `server.NewLogger` wraps slog and redacts IPv4/IPv6-shaped
    strings in messages and attributes, and is used for `http.Server.ErrorLog` too. Side effect: the startup
    line shows `addr=[ip]:8080` instead of `[::]:8080`, and strings like `12:00:00` would be redacted.
12. One Info log line per accepted request: `ingest app_id=… accepted=… rejected=…` (no key, no IP).

## Tests
- `internal/ingest`: valid event, minimal event, scalar props, ts edges; 30 rejection cases (name pattern/length,
  missing/typed fields, ts bounds incl. seconds-instead-of-ms, UUIDs, control chars, device_type, props shape/keys/size);
  reasons never echo values.
- `internal/ratelimit`: burst, refill, cap, per-key independence, Retry-After rounding.
- `internal/server`: auth (missing, unknown, non-sk, Basic, lowercase bearer, body app_id ignored), partial batch
  (indices and reasons, received_at set), bad requests (400/415), limits (100 ok, 101 -> 413, >256 KB -> 413,
  gzip ok, gzip bomb -> 413, configured limits), rate limit (429 + Retry-After 20 at 3/min, other key unaffected,
  bad key -> 401 not 429, refill), no IP: RemoteAddr + X-Forwarded-For/X-Real-IP/Forwarded + a panic carrying
  IPs, then scans logs, responses and the raw DB files (db, wal, shm) for every IP. `RedactIPs` cases.
- `internal/cli`: env parsing (defaults, custom, invalid -> exit 1), end-to-end over a real socket
  (200 then 429 with rate from env, logs contain neither 127.0.0.1 nor the forwarded IP).
- `internal/store`: InsertEvents, NULLs, rollback of a whole batch on one bad row.

## Open points (for O6 security review / later)
- Unknown-key requests are not rate limited: each costs one SHA-256 and one indexed SELECT. Cheap, but a
  global or per-connection limit could be added if needed (cannot be per-IP: D9).
- Rate limiter state is in memory: resets on restart, not shared across replicas (one replica by design).
- No dedup: an SDK retry after a lost 200 duplicates events. An optional per-event `id` (UUID) with a unique
  index would fix it; worth deciding before the SDK ships.
- `go test -race` still not run (needs CGO, see recipe).
