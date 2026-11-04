# Local restore test (MinIO in place of R2)

Verified 2026-11-04 on macOS arm64, Docker 29.8 / Compose v5.5.1, both natively and with
`SONDA_TEST_PLATFORM=linux/amd64` (emulated). Run from anywhere:

    scripts/restore-test.sh                                   # host arch, ~1 min after the first build
    SONDA_TEST_PLATFORM=linux/amd64 scripts/restore-test.sh   # x86 images (Hetzner x86)
    KEEP=1 scripts/restore-test.sh                            # leave the stack up; prints the compose command

Exit 0 and a final `PASS:` line = good. On failure it prints `FAIL: <reason>`, `ps -a` and the last 40 log
lines of every service, then tears down.

## What it does
1. Copies `docker-compose.yml` to a temp dir without the `exclude_from_hc` line (Coolify-only key; plain
   compose fails with "additional properties 'exclude_from_hc' not allowed") and writes a temp env file
   (random MCP token and MinIO secret, `R2_ENDPOINT=http://minio:9000`, `R2_FORCE_PATH_STYLE=true`).
2. Runs it with `docker-compose.test.yml` on top: `minio` (Chainguard image, digest-pinned), `minio-init`
   (creates the bucket with `curl --aws-sigv4`), `restore` waits for `minio-init`, `app` published on
   `127.0.0.1:18080`. MinIO's data is an **external** volume (`sonda-restore-test-minio`) so `down -v`
   does not delete the "R2" side.
3. First start on an empty bucket: `restore` must exit 0. `docker compose exec -T app /sonda app create`
   (same as `docker exec` on Coolify), 20 events in two batches with curl, MCP `event_counts` = 20.
4. Waits until the bucket has objects, plus 5 s. `down -v`, asserts the data volume is gone.
5. `up -d`: `restore` rebuilds the DB. Checks via MCP: `event_counts` total 20, `list_apps`,
   `recent_events`; the old ingest key works; resending batch 1 gives `duplicates: 10`; a new event is
   accepted; all files in `/data` (db, wal, shm, `.sonda.db-litestream/`) owned by 65532.
6. `up -d --force-recreate` with the volume in place: `restore` logs `database already exists, skipping`.
7. Ingests 5 more, waits for new objects, `down -v` again, restores: 26 events. Proves replication
   continues after a restore (Litestream logs `detected database behind replica` and carries on).
8. Trap on exit: `down -v --remove-orphans`, removes the MinIO volume and the temp dir.

## Gotchas found
- `litestream restore` prints nothing on success; only the skip case logs. Check data, not logs.
- MinIO no longer publishes images on Docker Hub (`minio/minio`, `minio/mc` gone) and quay.io needs auth.
  `cgr.dev/chainguard/minio` works; free tier is `:latest` only, hence the digest pin. To bump:
  `docker pull cgr.dev/chainguard/minio:latest && docker image inspect cgr.dev/chainguard/minio:latest --format '{{.RepoDigests}}'`.
- With `SONDA_TEST_PLATFORM`, helper images cached for the host arch only make compose fail with
  "does not provide the specified platform": the script pulls `minio` and `minio-init` first.
- Under `set -o pipefail`, `grep` with no match kills `$(...)` assignments silently: wrap with `|| true`.
- Images built by the test are tagged `sonda-restore-test-{app,restore,litestream}`; they stay after the
  run (cache). `docker image rm` them to free ~20 MB.
