# Step 1 (O1): Go skeleton, CLI, SQLite

Branch `server/1-skeleton`, 2026-11-04. `go vet` and `go test ./...` pass via Docker
(recipe: notes/recipes/go-via-docker.md). Binary built and smoke-tested (app create, app list, serve + /healthz).

## Layout
- `go.mod`: module `github.com/meltinbitfarm/sonda` (from `git remote`), go 1.27.3. Deps: modernc.org/sqlite only (plus its transitive deps).
- `cmd/sonda/main.go`: signal handling (SIGINT/SIGTERM -> graceful shutdown), calls `cli.Run`.
- `internal/cli`: `sonda serve`, `sonda app create "<name>"`, `sonda app list`, `sonda help`. Exit codes 0 ok, 1 error, 2 usage.
- `internal/store`: open with pragmas, embedded migrations, apps queries.
- `internal/store/migrations/0001_init.sql`: schema.
- `internal/server`: HTTP mux, only `GET /healthz` (pings the DB, 503 if unavailable).
- `internal/ingestkey`: key generation and hashing.
- Root Package.swift / sdk/ untouched.

## Configuration
- `SONDA_ADDR` (default `:8080`), `SONDA_DB_PATH` (default `/data/sonda.db`).
- Server timeouts: read header 5s, read 15s, write 30s, idle 60s, max header 16 KB; shutdown waits up to 10s.

## Choices to record (proposals for decisions.md)
1. Ingest keys: format `sk_` + 32 random bytes base64url (256 bits). Only hex SHA-256 is stored
   (`apps.ingest_key_hash`, UNIQUE). No salt/slow KDF: with 256 bits of entropy brute force is
   impossible, and a deterministic hash allows an indexed lookup per request. Consequence: a lost key
   cannot be shown again; a rotate command will be needed (not built yet). Printed alone on stdout, notice on stderr.
2. Timestamps `ts`, `received_at`, `created_at`: INTEGER milliseconds since epoch UTC. Why: 8 bytes vs ~24
   for ISO text, numeric range scans on the (app_id, x, ts) indexes, no timezone/format ambiguity, ms
   precision for event ordering within a session; readable in SQL via
   `strftime('%Y-%m-%dT%H:%M:%fZ', ts/1000.0, 'unixepoch')` or `datetime(ts/1000, 'unixepoch')`.
   Note for O3: the `query` tool's users (Claude) must be told the unit.
3. Migrations: numbered files `NNNN_name.sql` embedded in the binary, tracked with `PRAGMA user_version`,
   each applied in a transaction with the version bump; numbering must be contiguous; a DB newer than the
   binary is refused (protects against rolling back the image after a migration). Run automatically on every Open.
4. Connection: `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=1`, `synchronous=NORMAL`, and
   `_txlock=immediate` (BEGIN IMMEDIATE avoids SQLITE_BUSY deadlocks on read->write upgrades). Set per connection via DSN.
5. Schema: STRICT tables. `apps(id, name UNIQUE, ingest_key_hash UNIQUE, created_at)`.
   `events(id, app_id FK ON DELETE CASCADE, name, ts, received_at, user_id, session_id, app_version, build,
   os_version, locale, region, storefront, device_model, device_type, props)`. NOT NULL: app_id, name, ts,
   received_at, user_id, session_id, props (default `'{}'`, `CHECK(json_valid(props))`). Other D10 fields nullable
   (storefront can be nil on device). No IP column (D9, covered by a test). Indexes:
   (app_id, name, ts), (app_id, user_id, ts), (app_id, session_id, ts).
6. App names: trimmed, 1–100 characters, no control characters, unique (case-sensitive).

## Open points for later steps
- O2 must decide what to do with events missing user_id/session_id (schema rejects them; drop per D13).
- Key rotation / app deletion commands are not implemented.
- `go test -race` is not run (needs CGO); see recipe.
