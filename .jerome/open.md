# Open

## Open questions
- Q1: Coolify gestisce depends_on con service_completed_successfully? Risposta (non provata dal vivo): sì, Coolify usa docker compose; `restore` con `restart: "no"` ed `exclude_from_hc: true`. Manca un deploy di prova. Ripiego: un container con Litestream ≥0.5.6 `replicate -restore-if-db-not-exists -exec "sonda serve"`. Dettagli: notes/q1-coolify-depends-on.md
- Q2: quale schema di autenticazione usano esattamente gli MCP di Pulso e Celora (da riprendere dal loro codice).
- Q3: dominio o sottodominio del servizio.
- Q4: valori del rate limit sulla ingest key.
- Q5: politica di conservazione degli eventi grezzi nel tempo.
- Q6: ordine di migrazione delle app da TelemetryDeck.

## Assumptions
- A1: D5 resta com'è, con `restart: "no"` ed `exclude_from_hc: true` su `restore` e Litestream fissato a ≥0.5.6 — Q1 dice che funziona; il deploy di prova lo conferma
- A2: rate limit ingest (Q4) di partenza: 60 richieste/min per chiave, al massimo 100 eventi per batch, body ≤ 256 KB; si possono cambiare con variabili d'ambiente — valori larghi per 4 app piccole
- A3: conservazione (Q5): per ora si tengono tutti gli eventi grezzi, nessuna pulizia — volumi minimi, si decide più avanti

## Parked
- none

## Backlog
none
