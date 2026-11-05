# Deploying Sonda on Coolify

Sonda runs as one Docker Compose resource with three services on a shared volume `/data` (D5):

| Service | Image | Role |
|---|---|---|
| `restore` | Litestream 0.5.17 (scratch) + config | One-shot. If `/data/sonda.db` is missing and R2 holds a replica, restores it; otherwise exits 0. `restart: "no"`, `exclude_from_hc: true` (A1). |
| `app` | `sonda` on distroless static, nonroot | `sonda serve` on port 8080. Starts only after `restore` exited 0. |
| `litestream` | same image as `restore` | `litestream replicate`: streams every write to R2 (sync every second). |

All three run as uid/gid 65532 and both images ship an empty `/data` owned by 65532, so the fresh
volume gets that owner whichever container mounts it first: no root init step.
Neither image has a shell.

Files: `Dockerfile` (targets `app` and `litestream`), `docker-compose.yml`, `deploy/litestream.yml`,
`.env.example`. Status: the stack is tested locally with MinIO in place of R2
(`scripts/restore-test.sh`, below). First Coolify deploy done 2026-11-05: `restore` → `app` ordering works (Q1 closed).

## 1. Cloudflare R2: bucket and token

1. Cloudflare dashboard → **R2 Object Storage** → **Create bucket**. Name e.g. `sonda-backup`.
   Location: automatic, or a location hint / EU jurisdiction near the server. Keep it private
   (no public access, no custom domain).
2. Note the **Account ID** (R2 overview page, right column). The S3 endpoint is
   `https://<ACCOUNT_ID>.r2.cloudflarestorage.com`
   (EU jurisdiction bucket: `https://<ACCOUNT_ID>.eu.r2.cloudflarestorage.com`).
3. R2 → **Manage API tokens** → **Create API token** (an Account API token survives your user
   leaving the account):
   - Permissions: **Object Read & Write** (Litestream also deletes old files during retention).
   - Specify bucket: only `sonda-backup`.
   - TTL: forever, or a date you put in a calendar.
4. Copy **Access Key ID** and **Secret Access Key** (shown once). They become
   `R2_ACCESS_KEY_ID` and `R2_SECRET_ACCESS_KEY`.

Litestream detects R2 from the endpoint and applies its quirks itself (signed payload,
2 concurrent uploads). `R2_REGION` stays `auto`, `R2_FORCE_PATH_STYLE` stays `false`.

## 2. Coolify resource

1. Project → environment → **+ New** → the Git repository (GitHub App or deploy key for a private
   repo), branch to deploy.
2. **Build pack: Docker Compose**. Base directory `/`, Docker Compose location `/docker-compose.yml`.
   Save, then check **Docker Compose Content** shows the three services.
3. **Environment Variables**: Coolify creates them from the compose file. Required ones
   (`${VAR:?}`) block the deploy until filled:

   | Variable | Value |
   |---|---|
   | `SONDA_MCP_TOKEN` | `openssl rand -hex 32` (≥ 32 chars, else `serve` refuses to start). Mark as secret. |
   | `R2_ENDPOINT` | `https://<ACCOUNT_ID>.r2.cloudflarestorage.com` |
   | `R2_BUCKET` | `sonda-backup` |
   | `R2_ACCESS_KEY_ID` / `R2_SECRET_ACCESS_KEY` | from step 1. Mark as secret. |

   Optional, with defaults: `R2_PATH` (`sonda`, key prefix in the bucket), `R2_REGION` (`auto`),
   `R2_FORCE_PATH_STYLE` (`false`), `SONDA_INGEST_RATE_PER_MIN` (60), `SONDA_INGEST_MAX_BATCH` (100),
   `SONDA_INGEST_MAX_BODY_BYTES` (262144), `SONDA_INGEST_DAILY_QUOTA` (50000 events per app per UTC day),
   `SONDA_VERSION` (`dev`, shown by `sonda version` and in the startup log). See `.env.example`.
4. **Domains**: on service `app` only, host `https://sonda.meltinbitfarm.cloud` (D22), port `8080`.
   In older Coolify versions write it as `https://<host>:8080` in the `app` service's Domains field;
   in newer ones use **Manage Domains** and pick service and port. The port tells Coolify's proxy
   the container port; clients still use 443. No `ports:` are published; `restore` and `litestream`
   get no domain.
   Check under **Manage Domains** that the domain sits under `app`: on the first deploy Coolify
   attached it to `restore` (the first service), and the proxy answered 503 with no TLS certificate
   because `restore` exits after running.
5. **Deploy**. In the deployment log expect, in order: images built, `restore` exited 0, `app` healthy,
   `litestream` started.

Health: `app` has a healthcheck (`/sonda healthcheck`, a GET of `/healthz` over loopback, since the image
has no curl). `restore` is excluded from the stack health (`exclude_from_hc`). `litestream` has no
healthcheck (scratch image, nothing to probe with); it restarts on failure (`unless-stopped`).
Check its logs after the first deploy.

Proxy access logs: keep Coolify's proxy access logs off for this service (the default). claude.ai
connects with `?token=` in the URL, so the MCP token would end up in them; and every `POST /v1/events`
line would carry the client's IP address, which Sonda itself never stores or logs (D9).

### Limits and hardening

`docker-compose.yml` sizes the stack for a small VPS (1-2 GB RAM), so a runaway query or a connection
flood restarts one container instead of freezing the host:

| Service | `mem_limit` (= `memswap_limit`) | `pids_limit` | `/tmp` tmpfs |
|---|---|---|---|
| `app` | 512m (`GOMEMLIMIT=384MiB`) | 200 | 128m (SQLite temp files, `SQLITE_TMPDIR`) |
| `litestream` | 256m | 100 | 64m |
| `restore` | 256m | 100 | 64m |

All three also run with `read_only: true` (only the `/data` volume and `/tmp` are writable),
`cap_drop: [ALL]` and `no-new-privileges`. tmpfs pages count against the memory limit. On a bigger
server raise `mem_limit` and `memswap_limit` together (and `GOMEMLIMIT` to about 75% of the app's
limit). `scripts/restore-test.sh` checks that all of this is in effect and that restore and
replication still work with it. After the first Coolify deploy, check the same with
`docker inspect -f '{{.HostConfig.ReadonlyRootfs}} {{.HostConfig.Memory}}' <container>`
(Coolify passes these compose keys through to Docker; not yet verified on a live Coolify).

Inside the app: at most 8 database connections for ingestion and 4 read-only ones for MCP,
at most 512 open client connections, malformed ingest keys refused before any database work, every
MCP tool stopped after 25 s (at most 3 running at once), and `query` cannot build a string or blob
larger than 16 MiB.

Ingest quota: each app may store `SONDA_INGEST_DAILY_QUOTA` events per UTC day (default 50,000,
duplicates not counted). Past it, `POST /v1/events` answers 429 with `Retry-After` until midnight UTC
and logs `ingest: daily quota reached` once; the SDK keeps the events queued and retries. The count is
kept in memory, so a restart resets it for the rest of the day. Raise the value if a real app gets
near it.

Disk: there is no retention (A3), so watch the volume size (`docker system df -v`, or the server's
disk graph in Coolify) and the R2 bucket size, especially after a quota warning in the logs.

## 3. First app: ingest key

The containers have no shell, so Coolify's per-container **Terminal** cannot open (it starts `sh`).
Run the CLI with `docker exec` from the **server** terminal instead (Coolify → Servers → your server
→ Terminal, or SSH):

    docker ps --format '{{.Names}}' | grep '^app-'          # e.g. app-<resource-uuid>
    docker exec app-<resource-uuid> /sonda app create "Pulso"

The key (`sk_...`) is printed once on stdout and stored only as a hash: copy it into the app's
SDK configuration now. Other commands:

    docker exec app-<uuid> /sonda app list
    docker exec app-<uuid> /sonda app rotate-key "Pulso"    # new key; the old one fails at once
    docker exec app-<uuid> /sonda version

`docker exec` runs as the image user (65532), so files it creates in `/data` keep the right owner.
Do not add `-u root`.

Leaked or abused ingest key (it ships inside the app binary, so anyone can extract it): run
`rotate-key` for that app, ship an app update with the new key, and ask Claude (`event_counts` by
`app_version`, `recent_events`) whether the fake events need excluding from analyses. Until the update
spreads, old app versions fail with 401 and keep their events queued. The daily quota bounds what a
leaked key can write in the meantime.

## 4. Connect Claude

Endpoint `https://<sonda-host>/mcp/sonda`, Streamable HTTP, one secret (`SONDA_MCP_TOKEN`) sent as
`Authorization: Bearer <token>` or `?token=<token>`.

Claude Code (header, the token stays out of URLs and logs):

    claude mcp add --transport http sonda https://<sonda-host>/mcp/sonda \
      --header "Authorization: Bearer <token>"
    claude mcp list      # sonda: connected

claude.ai / Claude desktop and mobile: Settings → Connectors → Add custom connector, name `Sonda`,
URL `https://<sonda-host>/mcp/sonda?token=<token>`, OAuth fields empty.

Changing the token: edit `SONDA_MCP_TOKEN` in Coolify, redeploy, update every client.
Full recipe with curl calls for every tool: `.jerome/notes/recipes/mcp-connect.md`.

## 5. Verify

    HOST=https://<sonda-host>
    curl -s $HOST/healthz                                   # ok

    # one test event (use the key from step 3; ts = now in ms)
    curl -s -X POST $HOST/v1/events -H "Authorization: Bearer sk_..." -H 'Content-Type: application/json' \
      -d '{"events":[{"event_id":"'$(uuidgen)'","name":"app.first_launch","ts":'$(($(date +%s)*1000))',
           "user_id":"'$(uuidgen)'","session_id":"'$(uuidgen)'"}]}'
    # {"accepted":1,"duplicates":0,"rejected":0}

    # MCP: list_apps shows the app with events: 1
    curl -s $HOST/mcp/sonda -H "Authorization: Bearer $SONDA_MCP_TOKEN" -H 'Content-Type: application/json' \
      -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
      -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_apps","arguments":{}}}'

Then in the R2 bucket a `sonda/` prefix with LTX files must appear within seconds; the `litestream`
logs show `replicating to type=s3 ... bucket=sonda-backup`.

## 6. Restore drill (do it once before relying on it)

What `scripts/restore-test.sh` does locally, by hand on Coolify:

1. Note the event count (`list_apps`).
2. Stop the resource. On the server: `docker volume ls | grep sonda-data`, then
   `docker volume rm <that volume>`.
3. Deploy again. `restore` downloads the replica before `app` starts (its log is empty on success;
   `database already exists, skipping` on an ordinary redeploy).
4. `list_apps` shows the same count; an existing ingest key still works.

Moving to a new server is the same: new resource, same R2 variables, deploy.

## 7. Local stack and restore test

    scripts/restore-test.sh                                   # host arch
    SONDA_TEST_PLATFORM=linux/amd64 scripts/restore-test.sh   # x86, emulated on an arm Mac

It runs `docker-compose.yml` (minus `exclude_from_hc`, which plain `docker compose` rejects) plus
`docker-compose.test.yml` (MinIO as the replica, app on `127.0.0.1:18080`), and checks restore twice.
`KEEP=1` leaves the stack up. Recipe: `.jerome/notes/recipes/restore-test.md`.

## If Coolify mishandles the one-shot `restore`

Fallback from Q1: drop `restore` and `litestream`, run one container with Litestream as supervisor,
`litestream replicate -restore-if-db-not-exists -exec "/sonda serve"` (Litestream ≥ 0.5.6). That
needs an image holding both binaries; not built yet.

## Upgrades

Push to the deployed branch and redeploy. `restore` skips (database exists), `app` runs pending
migrations at start; a database newer than the binary is refused, so rolling back the image after a
migration needs a restore to an earlier point (`litestream restore -timestamp`).
