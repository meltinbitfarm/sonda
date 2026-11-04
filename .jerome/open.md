# Open

## Open questions
- Q1: Coolify gestisce depends_on con service_completed_successfully? Risposta (non provata dal vivo): sì, Coolify usa docker compose; `restore` con `restart: "no"` ed `exclude_from_hc: true`. Manca un deploy di prova. Ripiego: un container con Litestream ≥0.5.6 `replicate -restore-if-db-not-exists -exec "sonda serve"`. Dettagli: notes/q1-coolify-depends-on.md
- Q2: quale schema di autenticazione usano esattamente gli MCP di Pulso e Celora? Risposta: streamable HTTP senza OAuth, token come `Authorization: Bearer` o `?token=`; Celora usa un solo segreto da env con confronto a tempo costante. Dettagli: notes/q2-mcp-auth.md
- Q3: dominio o sottodominio del servizio.
- Q4: valori del rate limit sulla ingest key.
- Q5: politica di conservazione degli eventi grezzi nel tempo.
- Q6: ordine di migrazione delle app da TelemetryDeck.

## Assumptions
- A1: D5 resta com'è, con `restart: "no"` ed `exclude_from_hc: true` su `restore` e Litestream fissato a ≥0.5.6 — Q1 dice che funziona; il deploy di prova lo conferma
- A2: rate limit ingest (Q4) di partenza: 60 richieste/min per chiave, al massimo 100 eventi per batch, body ≤ 256 KB; si possono cambiare con variabili d'ambiente — valori larghi per 4 app piccole
- A4: MCP di Sonda come Celora (D15): streamable HTTP su /mcp/sonda, segreto unico `SONDA_MCP_TOKEN`, Bearer e `?token=` accettati, confronto con subtle.ConstantTimeCompare, 401 se sbagliato, niente avvio se manca, query string mai nei log — `?token=` serve ai connettori remoti di Claude che accettano solo l'URL, come per Pulso
- A5: ingest key `sk_` + 256 bit casuali, salvata solo come SHA-256 senza sale — con quella entropia è inattaccabile e la ricerca resta indicizzata; una chiave persa si ruota (O7)
- A6: timestamp in millisecondi UTC interi; tabelle STRICT, props verificate come JSON, user_id e session_id obbligatori — compatti, scansioni per intervallo veloci, dati coerenti
- A7: migrazioni numerate dentro il binario con PRAGMA user_version; un DB più nuovo del binario viene rifiutato — niente strumenti esterni
- A3: conservazione (Q5): per ora si tengono tutti gli eventi grezzi, nessuna pulizia — volumi minimi, si decide più avanti

## Parked
- none

## Backlog
- O1: passo 1 — scheletro Go, CLI `serve`/`app create`, SQLite WAL con tabelle apps/events (D19) — pronto sul branch server/1-skeleton, test verdi, da unire a main (L1)
- O2: passo 2 — `POST /v1/events` con ingest key, validazione D13, rate limit A2, niente IP
- O3: passo 3 — MCP con i tool di D14 e autenticazione A4
- O4: passo 4 — Dockerfile distroless, docker-compose.yml D5/A1, litestream.yml per R2
- O5: passo 5 — prova di ripristino in locale con MinIO, ricetta in notes/recipes/
- O6: passo 6 — revisione di sicurezza dell'analyst (D7, D13, tool query)
- O7: comando `sonda app rotate-key` — la ingest key si salva solo come hash e non si può rileggere (A5)
