# Step 4 (O4, O5, O7): Docker, Compose, Litestream/R2, restore test, key rotation

Branch `server/4-deploy`, 2026-11-04. `go vet ./...`, `go test ./...`, `gofmt -l .` clean (recipe
go-via-docker.md). `scripts/restore-test.sh` PASS natively (arm64) and with
`SONDA_TEST_PLATFORM=linux/amd64`. Nothing deployed or pushed; no R2 bucket touched.

## Commits
1. `sonda app rotate-key "<name>"` (O7): store `RotateKey`, CLI, tests.
2. `sonda healthcheck` and `sonda version` (`cli.Version` via ldflags), version in the startup log.
3. `Dockerfile`, `.dockerignore`, `deploy/litestream.yml`, `docker-compose.yml` (O4).
4. `docker-compose.test.yml`, `scripts/restore-test.sh` (O5).
5. `docs/deploy.md`, `.env.example`, `.env` in .gitignore.
6. Notes: this file, recipes/restore-test.md.

## O7 rotate-key
- `UPDATE apps SET ingest_key_hash=? WHERE name=? RETURNING ...`: exact name after the same trim/validation
  as `create`; unknown name -> exit 1 "no app named ..." (`app list` shows names). New key alone on stdout,
  notice on stderr (same convention as create). Events and app id are kept.
- Old key stops working on the next request with no restart: ingestion hashes and looks the key up per
  request, nothing caches it. Rate-limit bucket is keyed on app id, so it carries over.
- Tests: store (old hash gone, new works, other app untouched, case-sensitive name, unknown -> ErrNotFound,
  hash collision -> ErrDuplicate); CLI against a live `server.New` handler on the same DB: old key 200 ->
  rotate -> old 401, new 200, events kept; unknown/empty/missing name.

## O4 images
- One Dockerfile, two targets. `build`: `golang:1.27-alpine` on `$BUILDPLATFORM`, cross-compiles with
  `TARGETOS/TARGETARCH`, `CGO_ENABLED=0 -trimpath -ldflags "-s -w -X ...cli.Version=$VERSION"`, BuildKit
  cache mounts. `app`: `gcr.io/distroless/static-debian13:nonroot`, `/sonda`, `USER 65532`,
  `SONDA_ADDR=:8080`, `SONDA_DB_PATH=/data/sonda.db`, `EXPOSE 8080`, exec-form `HEALTHCHECK /sonda healthcheck`
  (30 s, start period 30 s at 2 s). `litestream`: `litestream/litestream:0.5.17-scratch` (official hardened
  variant, static binary, already user nonroot = 65532) + `/etc/litestream.yml` baked in.
- **Ownership (uid 65532)**: both final images contain an empty `/data` owned 65532:65532
  (`COPY --chown` of an empty dir, works on scratch/distroless without RUN). Docker copies the image's
  mount-point ownership onto an empty named volume at first mount, so whichever service mounts first, the
  volume root is 65532. All three services run as 65532. Verified after `down -v` + restore: db, wal, shm and
  `.sonda.db-litestream/` all 65532. No root init container, no chmod 777.
- `restore`/`litestream` are built from the repo (not `image:` directly) because the stock image has no
  `/data` (volume would stay root-owned) and the config must get in without a bind mount. The Litestream
  version is pinned in the Dockerfile `FROM` (ARG `LITESTREAM_VERSION=0.5.17`; 0.5.18 came out 2026-11-03,
  left for later). Base images are pinned by tag, not digest.
- `sonda healthcheck`: GET `http://127.0.0.1:<port of SONDA_ADDR>/healthz`, 3 s timeout; empty/0.0.0.0/::
  host -> 127.0.0.1. Exit 1 with the reason otherwise. The MCP DNS-rebinding guard does not involve /healthz.
- Image sizes (amd64): app ~6.7 MB, litestream ~13.8 MB.

## O4 compose (D5, A1)
- `restore`: `restore -if-db-not-exists -if-replica-exists /data/sonda.db`, `restart: "no"`, `exclude_from_hc: true`.
- `app`: `depends_on restore: service_completed_successfully`, `expose: 8080`, no `ports`, healthcheck
  repeated in compose (Coolify reads either), `restart: unless-stopped`, `stop_grace_period: 15s`.
- `litestream`: `replicate`, depends on restore completed **and app healthy** (so the DB file exists;
  Litestream seemed fine either way, the dependency just removes the question). `stop_grace_period: 30s`
  for the shutdown sync.
- Env: `${VAR:?}` for required (Coolify blocks the deploy until set): SONDA_MCP_TOKEN, R2_ENDPOINT,
  R2_BUCKET, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY. Defaults: R2_PATH=sonda, R2_REGION=auto,
  R2_FORCE_PATH_STYLE=false, the three ingest limits, SONDA_VERSION=dev (build arg). Compose maps R2_* to
  LITESTREAM_* and litestream.yml expands those. No YAML anchors (unsure how Coolify's parser treats them).
- Domain: not via magic `SERVICE_URL_APP_8080` / `SERVICE_FQDN_*` (needs a wildcard domain and Q3 is open);
  docs say set `https://<host>:8080` on service `app` in Coolify's Domains, per Coolify docs.
- `exclude_from_hc` makes plain `docker compose` fail validation; the test script strips the line.

## O5 restore test
Recipe: notes/recipes/restore-test.md. Covers: empty-bucket first start, app create via exec, ingest,
replication, `down -v`, restore, data via MCP (event_counts, list_apps, recent_events), old key valid,
duplicates on resend, file ownership, skip on recreate, and a second disaster after post-restore writes.

## Findings to record (proposals for decisions.md / open.md)
1. **D6 vs D5 on Coolify**: the container has no shell, so Coolify's container Terminal cannot open
   (it runs `sh`). CLI commands go through the server terminal/SSH: `docker exec app-<uuid> /sonda app create "X"`.
   Documented in docs/deploy.md. If Fabio wants the in-UI terminal, the only way is a shell in the image
   (distroless `:debug-nonroot`, busybox), against D5. Proposed: keep D5, accept docker exec.
2. MinIO images are gone from Docker Hub; test uses `cgr.dev/chainguard/minio` pinned by digest.
3. Litestream 0.5.17 scratch image chosen over the Debian one (no shell, same uid as distroless).
4. O7 was not in open.md's backlog: add it (done here) or mark it done.

## Open points
- Not tried on Coolify (Q1/A1 live test, D19 leaves it to Fabio): one-shot display, `exclude_from_hc`,
  build of two targets from one compose, whether a service without healthcheck (`litestream`) shows as
  healthy.
- Not tried against real R2 (only MinIO). Litestream's R2 auto-detection is by endpoint host.
- `litestream` has no healthcheck: if it hangs without exiting, nothing notices. Option: enable
  Litestream's metrics `addr:` and probe it, which needs a probe binary in the scratch image.
- R2 token needs delete rights for Litestream retention (Object Read & Write has them). A write-only
  token is possible with `retention.enabled: false` + an R2 lifecycle rule (Litestream ≥ 0.5.8); not done.
- Rolling back the image after a future migration needs a point-in-time restore (documented).
