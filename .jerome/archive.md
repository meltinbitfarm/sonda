# Archive

## Closed
- Q1: Coolify gestisce depends_on con service_completed_successfully? — done 2026-11-05: sì, primo deploy reale ok (restore → app); attenzione al dominio, Coolify lo aveva messo su restore
- Q3: dominio o sottodominio del servizio — done 2026-11-05: sonda.meltinbitfarm.cloud (D22)
- O1: passo 1 — scheletro Go, CLI `serve`/`app create`, SQLite WAL con tabelle apps/events (D19) — done 2026-11-04: unito a main da release/first, test verdi
- O2: passo 2 — `POST /v1/events` con ingest key, validazione D13, rate limit A2, niente IP — done 2026-11-04: unito a main da release/first, test verdi
- O3: passo 3 — MCP con i tool di D14 e autenticazione A4 — done 2026-11-04: unito a main da release/first, test verdi
- O4: passo 4 — Dockerfile distroless, docker-compose.yml D5/A1, litestream.yml per R2 — done 2026-11-04: unito a main da release/first, test verdi
- O5: passo 5 — prova di ripristino in locale con MinIO, ricetta in notes/recipes/ — done 2026-11-04: unito a main da release/first, test verdi
- O6: passo 6 — revisione di sicurezza dell'analyst (D7, D13, tool query) — done 2026-11-04: unito a main da release/first, test verdi
- O7: comando `sonda app rotate-key` — la ingest key si salva solo come hash e non si può rileggere (A5) — done 2026-11-04: unito a main da release/first, test verdi
- O8: SDK Swift `Sonda` (area sdk) con revisione e correzioni — done 2026-11-04: unito a main da release/first, test verdi

## Decisions

## Assumptions
