# Step 5: hardening after the O6 review (developer, 2026-11-04)

Branch `server/5-hardening` (from release/first). Input: notes/o6-security-review.md.
Package.swift and sdk/ untouched.

## Done

| Finding | Change | Test |
|---|---|---|
| M1.1 writer pool | `store.Open`: `SetMaxOpenConns(8)`, `SetMaxIdleConns(8)` (`store.MaxWriterConns`) | `store.TestWriterPoolBounded` (64 concurrent reads, pool stays at 8) |
| M1.2 malformed keys | `ingestkey.WellFormed`: `sk_` + exactly 43 base64url chars; `authenticate` refuses anything else with 401 before hashing or SQL | `ingestkey.TestWellFormed`; `server.TestIngestMalformedKeySkipsDatabase` (DB closed: malformed key 401, well-formed unknown key 503, proving the lookup is skipped only for junk) |
| M1.3 listener (optional) | `cli.newLimitListener(ln, 512)`, own 50 lines instead of adding golang.org/x/net | `cli.TestLimitListener` |
| M1.4 / M2.1 container limits | compose: `mem_limit` = `memswap_limit` 512m app, 256m litestream and restore; `pids_limit` 200 / 100 / 100; `GOMEMLIMIT=384MiB` on app | `scripts/restore-test.sh` inspects all three containers |
| M2.2 SQLite cap | `store.LimitedConn`: takes a read-only connection and sets `SQLITE_LIMIT_LENGTH` = 16 MiB; the `query` tool runs on it; friendly error "a value built by the query exceeds 16 MiB" | `store.TestLimitedConn` (zeroblob, hex, randomblob, printf, group_concat all "too big"), `analytics.TestQuery` case |
| S1 daily quota | `ratelimit.DailyQuota`, in-memory count per app per UTC day; checked before the rate limiter, counted after the insert (stored = accepted − duplicates); 429 + `Retry-After` until midnight UTC; one Warn log when crossed. `SONDA_INGEST_DAILY_QUOTA`, default 50,000 | `ratelimit.TestDailyQuota`, `server.TestIngestDailyQuota`, `cli.TestServerConfigFromEnv` |
| S2 tool timeouts | `mcpserver` guard on every tool: 25 s deadline (`Options.ToolTimeout`, `DefaultToolTimeout`) and at most 3 concurrent calls (`MaxConcurrentTools`); waiting for a slot counts against the deadline | `TestGuardTimeout`, `TestGuardConcurrency`, `TestToolTimeoutOverMCP` |
| S3 compose hardening | app, litestream, restore: `read_only: true`, `tmpfs /tmp` (128m app, 64m others), `cap_drop: [ALL]`, `no-new-privileges:true`; app gets `SQLITE_TMPDIR=/tmp` | restore-test.sh: hardening check + full restore cycles pass with it |
| S5 prompt injection | sentence in `instructions` and in the descriptions of `recent_events`, `query`, `event_counts` | `TestMCPEndToEnd` checks presence (and absence on the other tools) |
| Notes: D9 proxy logs, incident | docs/deploy.md: proxy access logs also leak client IPs on the ingest path; leaked-key procedure with `rotate-key`; watch disk/R2 size | docs |

## Choices

1. **SQLite length cap per query, not per connection open.** modernc exposes `sqlite3_limit` only
   through `sqlite.Limit(*sql.Conn, ...)`; the connection hook gets the driver conn but `limit` is
   unexported there. So `query` takes a `*sql.Conn` and sets the cap on every call (one C call).
   The cap then stays on that pooled connection, harmless for the typed tools. No function deny list
   added: the length cap covers `||`, `replace`, `hex`, `group_concat` too, as the review preferred.
2. **Tool timeout 25 s, not 30 s**: below the HTTP `WriteTimeout` (30 s), so the error reaches Claude
   before the socket is closed. `query` keeps its own 10 s inside.
3. **Concurrency 3** (read pool 4), as the review suggested; a 4th call waits, then fails with "server busy".
4. **Quota in memory**: no index on `received_at`, so seeding from the DB would scan an app's whole
   history. Consequence: a restart resets the day's count (a key can store up to one more quota that
   day). A batch arriving under the quota is accepted whole, so the overshoot is at most one batch
   (100) per concurrent request.
5. **Memory sizes**: litestream and restore got 256m instead of the review's 128m: Litestream 0.5
   compaction and restore of a few hundred MB database were not measured, and an OOM-killed restore
   blocks the app from starting. tmpfs counts against the memory limit.
6. Quota checked **before** the rate limiter, so 429 for quota does not consume rate tokens.

## Left

- S4: nothing to do (review says so; M1 covers it).
- S6: images not digest-pinned (review: fine for the first deploy).
- Coolify passing `read_only`, `tmpfs`, `cap_drop`, `mem_limit` through is not verified live (Q1/A1);
  docs/deploy.md gives a `docker inspect` check for the first deploy.
- MCP `Version` constant still `0.3.0`.

## Results

- `go vet ./... && go test -count=1 ./...`: all 8 packages ok; `gofmt -l .`: clean
  (notes/recipes/go-via-docker.md). `go test -count=5 ./internal/mcpserver/` ok (timing tests stable).
- `scripts/restore-test.sh`: PASS with the hardened compose (both restore cycles, hardening check).

## Proposals for the lead (decisions.md / open.md, not edited here)

- A5 (from the review) can be recorded as implemented: writer pool 8, key format check, daily quota
  50,000, container limits 512m/256m/256m, tools 25 s and 3 concurrent, 16 MiB value cap.
- Q4: the quota value belongs there; confirm with real traffic of the 4 apps.
