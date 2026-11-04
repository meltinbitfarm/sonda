# Open

## Open questions
- Q1: Coolify gestisce depends_on con service_completed_successfully? Risposta (non provata dal vivo): sì, Coolify usa docker compose; `restore` con `restart: "no"` ed `exclude_from_hc: true`. Manca un deploy di prova. Ripiego: un container con Litestream ≥0.5.6 `replicate -restore-if-db-not-exists -exec "sonda serve"`. Dettagli: notes/q1-coolify-depends-on.md
- Q2: quale schema di autenticazione usano esattamente gli MCP di Pulso e Celora? Risposta: streamable HTTP senza OAuth, token come `Authorization: Bearer` o `?token=`; Celora usa un solo segreto da env con confronto a tempo costante. Dettagli: notes/q2-mcp-auth.md
- Q3: dominio o sottodominio del servizio.
- Q4: valori del rate limit sulla ingest key. Valori di partenza in A2 e A8.
- Q5: politica di conservazione degli eventi grezzi nel tempo.
- Q6: ordine di migrazione delle app da TelemetryDeck.

## Assumptions
- A1: D5 resta com'è, con `restart: "no"` ed `exclude_from_hc: true` su `restore` e Litestream fissato a ≥0.5.6 — Q1 dice che funziona; il deploy di prova lo conferma
- A2: rate limit ingest (Q4) di partenza: 60 richieste/min per chiave, al massimo 100 eventi per batch, body ≤ 256 KB; si possono cambiare con variabili d'ambiente — valori larghi per 4 app piccole
- A4: MCP di Sonda come Celora (D15): streamable HTTP su /mcp/sonda, segreto unico `SONDA_MCP_TOKEN`, Bearer e `?token=` accettati, confronto con subtle.ConstantTimeCompare, 401 se sbagliato, niente avvio se manca, query string mai nei log — `?token=` serve ai connettori remoti di Claude che accettano solo l'URL, come per Pulso
- A5: ingest key `sk_` + 256 bit casuali, salvata solo come SHA-256 senza sale — con quella entropia è inattaccabile e la ricerca resta indicizzata; una chiave persa si ruota (O7)
- A6: timestamp in millisecondi UTC interi; tabelle STRICT, props verificate come JSON, user_id e session_id obbligatori — compatti, scansioni per intervallo veloci, dati coerenti
- A7: migrazioni numerate dentro il binario con PRAGMA user_version; un DB più nuovo del binario viene rifiutato — niente strumenti esterni
- A8: quota di 50.000 eventi per app al giorno (SONDA_INGEST_DAILY_QUOTA), 429 fino a mezzanotte UTC; il contatore si azzera al riavvio — una chiave valida estratta non riempie disco e R2
- A9: limiti dei container: memoria per app, 256m per restore e litestream, pids_limit, read_only, cap_drop ALL, no-new-privileges — revisione O6
- A10: immagine senza shell, quindi il terminale del container su Coolify non si apre; `sonda app create` si lancia con `docker exec` dal terminale del server (docs/deploy.md) — D5 prevale sul dettaglio pratico di D6
- A11: l'MCP tiene il default di 30 giorni anche per retention (D14); Claude allarga l'intervallo per vedere il giorno 30
- A3: conservazione (Q5): per ora si tengono tutti gli eventi grezzi, nessuna pulizia — volumi minimi, si decide più avanti

## Parked
- none

## Backlog
- O9: a Fabio — push, tag 0.1.0, deploy su Coolify (chiude Q1), ripristino vero da R2, collegare Claude, integrare l'SDK nella quarta app (docs/deploy.md, sdk/README.md)
