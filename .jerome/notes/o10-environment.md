# O10: events marked with `environment` instead of dropped (D23)

2026-11-05, developer, branch `feature/environment`. Server and SDK changed together (D17).

## What changed

**Server**
- `internal/store/migrations/0002_environment.sql`: `ALTER TABLE events ADD COLUMN environment TEXT NOT NULL
  DEFAULT 'production' CHECK (environment IN ('production','testflight','development'))`. Existing rows
  become `production` (correct: SDK 0.1.0 sent only production builds). Indexes: the three lookup indexes are
  dropped and rebuilt with `environment` second — `(app_id, environment, name, ts)`, `(app_id, environment,
  user_id, ts)`, `(app_id, environment, session_id, ts)` — plus a new `(app_id, environment, ts)` for
  range-only reads (totals, newest events). The unique `(app_id, event_id)` is untouched. No data is
  rewritten or removed; dropping indexes loses nothing.
- `store.Event.Environment`; empty is stored as `production`.
- `internal/ingest`: optional `environment`; missing / `null` / `""` = `production`; any other value than the
  three (case-sensitive) rejects that event only (`environment: must be production, testflight or
  development`); wrong JSON type rejects it as before.
- `internal/analytics`: every tool takes `environment` = `production` (default) | `testflight` |
  `development` | `all` (trimmed, case-insensitive). The filter is always `environment IN (...)`, `all`
  included (IN over the three values), so the index prefix `(app_id, environment, ...)` is always usable;
  checked with EXPLAIN QUERY PLAN in `TestMigrationFromV1`. Retention filters both the first launch and the
  returns. `event_counts` can `group_by` `environment`. Every result reports `"environment"` (the filter
  applied); `recent_events` returns each event's environment. `list_apps` now has an input
  (`AppsInput{Environment}`) and still lists every app; only the totals are filtered.
- `internal/mcpserver`: descriptions and server instructions say the default is production and excludes
  tests, how to see them, and that `query` does **not** filter (schema lists the column and the new
  indexes). MCP server version 0.3.0 -> 0.4.0.

**SDK**
- `AppEnvironment` is now `production | testflight | development` (raw values = wire values, Codable).
  DEBUG -> development; simulator -> development; `AppTransaction.environment` `.production` ->
  production, `.sandbox` -> testflight, `.xcode` -> development, unknown future value -> testflight;
  StoreKit unavailable: `sandboxReceipt` -> testflight, else production. Still injected through
  `Dependencies.environment`.
- `Engine`: no more early return; `sendsEvents` became `isStarted`. The environment is set before the first
  enqueue and stamped in each `WireEvent` at record time.
- `WireEvent.environment` is optional only so queue lines written by 0.1.0 still decode and send (server
  stores them as production).

## Tests (all pass, 2026-11-05)
- Go via Docker: `gofmt -l .` empty, `go vet ./...` clean, `go test ./...` ok in every package. New:
  `TestMigrationFromV1` (v1 DB with rows -> v2, rows production, old indexes gone, plan uses the new index),
  environment cases in `TestValidateOK/Rejects`, `TestIngestEnvironment`, `TestResolveEnv`,
  `TestEnvironmentFilter` (all five tools x four values, group_by, bad value), MCP end-to-end checks
  (schema has `environment` on every tool but query, descriptions, default hides a development event).
  `TestOpenReadOnly` no longer hard-codes schema version 1.
- `scripts/restore-test.sh`: PASS (20, then 26 events restored).
- `swift test`: 47 tests pass. `xcodebuild test` iPhone 17 / iOS 26.2: 46 pass. Release builds for iOS,
  macOS, Mac Catalyst succeed.
- LiveServerTests against a locally built server: pass (accepted 100 / 100 duplicates / 23), rows stored as
  production; curl with `development` stored as development, `debug` rejected with the reason.
- Upgrade drill: a DB written by the binary at 4c76b61 (current production code) opened by the new binary:
  migrated to version 2, the event is production, `list_apps` works over MCP; the old binary then refuses it
  (`database schema version 2 is newer than this binary (1)`).

## Before release 0.2.0
1. **Deploy the server before any app ships SDK 0.2.0.** An old server ignores unknown fields, so it would
   store TestFlight/Debug events as production.
2. **Rollback:** after 0002 runs, a 0.1.x image will not start on that DB; rolling back needs
   `litestream restore -timestamp` to before the upgrade (docs/deploy.md, Upgrades).
3. Docs now say `from: "0.2.0"` / "pick version 0.2.0" (sdk/README.md, README.md): correct only once the
   tag exists.
4. Test events count toward the per-app daily quota (50,000 default). Fabio's Debug runs are now stored
   forever with the real data; nothing deletes them (a cleanup command could be a later item).
5. Same device, Debug/TestFlight then App Store: the `user_id` usually survives (UserDefaults), so its
   `app.first_launch` stays in the test environment and that device never enters a production retention
   cohort. Negligible for Fabio's own devices; for external TestFlight testers it slightly under-counts
   new production users. Documented in sdk/README.md.
6. Claude with `query` must add `environment = 'production'` itself; raw SQL without it mixes tests in and
   uses only the `app_id` prefix of the indexes. Stated in the tool description.
7. D8 in decisions.md is already marked as partly superseded by D23; no state-file change needed beyond
   closing O10 in open.md when merged.
