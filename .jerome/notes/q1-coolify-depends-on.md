# Q1: Coolify and depends_on service_completed_successfully (2026-11-04)

Verdict: works with caveats. Not tested on a live instance; confidence medium.

## Findings
- Coolify's compose build pack runs `docker compose`, so Compose semantics apply (condition supported since Compose 1.29 / v2). Coolify does not document this condition itself.
- Community report (Dec 2025) that a `restart: "no"` one-shot with `service_completed_successfully` "works reliably" on Coolify: https://github.com/coollabsio/coolify/discussions/2784 (read via summary).
- Coolify docs: `exclude_from_hc: true` on a service keeps it out of the stack health calculation; the docs name one-time migration services as the use case: https://coolify.io/docs/knowledge-base/docker/compose and https://coolify.io/docs/applications/build-packs/docker-compose
- Not found: any statement of how an exited, excluded service is displayed, or proof Coolify does not mark the deployment degraded without the flag. Needs a live test.
- Restart loop: Compose only restarts per the `restart:` policy. Set `restart: "no"` on restore (do not use `always` or `unless-stopped`). No evidence Coolify adds restarts itself.
- Caveats: Coolify renames services in previews and did not update depends_on (https://github.com/coollabsio/coolify/issues/2005); a failing init step can leave Compose hanging (https://github.com/docker/compose/issues/10728).

## Litestream fallback
- Litestream 0.5.x `replicate` still supports `-exec CMD` (Litestream exits when the child exits; flags go before positional args): https://litestream.io/reference/replicate/
- `restore -if-db-not-exists -if-replica-exists`: dropped by mistake in 0.5.0, back in 0.5.1: https://community.fly.io/t/litestream-v0-5-1-released/26176 , https://mtlynch.io/notes/hold-off-on-litestream-0.5.0/ , https://litestream.io/reference/restore/ . Use >= 0.5.1.
- `replicate -restore-if-db-not-exists` exists from v0.5.6, which could replace the separate restore step.
- Image implications (my assessment): the single container needs both the `litestream` and `sonda` binaries. The official litestream image is not distroless, but verify it has a shell for chaining restore then replicate. Easiest is a multi-stage build that copies the litestream binary into the sonda image, with an entrypoint script (needs a shell, so not scratch/distroless) or the v0.5.6 flag with no script. The `-exec` child is a subprocess, so signals and exit forwarding are handled by Litestream.

## Recommendation
Keep D5 (restore service + depends_on) with `restart: "no"` and `exclude_from_hc: true`, after one live Coolify test (deploy, check status, redeploy). If that fails, use the single-container fallback with Litestream >= 0.5.6.
