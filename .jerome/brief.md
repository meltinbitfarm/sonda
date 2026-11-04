---
format: brief/v2
name: Sonda
slug: sonda
status: ready
created: 2026-11-04
source: mobile
---

## Goal
Servizio di telemetria self-hosted e leggerissimo che sostituisce TelemetryDeck per le app iOS di Fabio: raccoglie eventi tramite un SDK Swift e li espone a Claude via MCP per analizzare onboarding, uso delle feature, abbandoni e conversioni del paywall.

## Context
- Oggi TelemetryDeck (piano gratuito) è su 3 app; aggiungere la quarta richiederebbe il piano a pagamento.
- L'uso reale è solo l'analisi da Claude via MCP: nessuna dashboard serve.
- Infrastruttura: Hetzner + Coolify, solo Docker / Docker Compose (niente Nixpacks dopo i CVE PHP); backup su Cloudflare.
- Unico utente del servizio: Fabio.

## Constraints
- V1: infrastruttura il più leggera e semplice possibile.
- V2: deploy solo con Docker Compose su Coolify.
- V3: nessuna interfaccia web.

## Decisions
- D1: servizio separato da Pulso, query esposte via MCP — l'ingestione continua ha un profilo di carico diverso dal proxy GA4 di Pulso
- D2: Go, binario unico in immagine scratch/distroless — superficie CVE minima, aggiornamenti rari, consumi irrisori; il codice lo scrive Claude, quindi lo stack familiare non conta
- D3: SQLite in WAL con driver modernc.org/sqlite (senza CGO) — nessun servizio DB, più veloce per un servizio piccolo, binario statico
- D4: Litestream replica in continuo su Cloudflare R2 — backup senza server di database
- D5: Docker Compose con 3 servizi sullo stesso volume /data: `restore` (litestream restore -if-db-not-exists -if-replica-exists), `app` (depends_on restore con service_completed_successfully), `litestream` (replicate) — ripristino automatico prima dell'avvio, immagine app senza shell
- D6: nessuna UI, CLI nello stesso binario: `sonda serve` e `sonda app create "<nome>"` (stampa la ingest key) — basta il terminale del container su Coolify
- D7: due tipi di token: ingest key per app (solo scrittura, considerata pubblica, protetta da rate limit e validazione) e un solo token di lettura segreto per l'MCP — la chiave dentro il binario dell'app è estraibile
- D8: SDK Swift proprio (Swift Package, repo a parte): coda su disco, invio a batch, retry offline; scarta eventi in DEBUG, TestFlight e simulatore — dati di produzione puliti
- D9: nessun IP salvato e nessuna geolocalizzazione da IP — privacy e niente database GeoIP da aggiornare
- D10: campi automatici dall'SDK: name, ts, received_at, app_id (dalla ingest key), user_id (UUID casuale alla prima apertura, non IDFV), session_id (nuovo dopo 30 min di inattività), app_version, build, os_version, locale, region (Locale.current.region), storefront (Storefront.current?.countryCode), device_model (identificatore hardware grezzo, es. iPhone16,2), device_type (phone/pad/mac), props (JSON piatto) — tutto dal device, nulla identifica la persona
- D11: nomi evento `area.azione_al_passato` in snake_case; pochi eventi generici con i dettagli nelle props; mai dati personali nelle props — stesse query e funnel per tutte le app
- D12: catalogo standard: app.first_launch, app.session_started, onboarding.step_viewed {step, step_name}, onboarding.completed, onboarding.skipped {at_step}, screen.viewed {screen}, feature.used {feature}, paywall.shown {placement, variant}, purchase.started / purchase.completed / purchase.cancelled {product_id} (cancelled = userCancelled di StoreKit 2), purchase.failed {error}, purchase.restored, error.occurred {domain, code} — copre onboarding, uso feature e paywall
- D13: validazione server: nome evento ≤ 64 caratteri, props ≤ 2 KB, niente chiavi annidate; un evento malformato si scarta senza far fallire il batch
- D14: tool MCP: list_apps; event_counts (eventi e utenti unici, raggruppabili per evento, prop, app_version, storefront, device_type); funnel (passi ordinati con filtro props, per utente o sessione, finestra massima); retention (coorti settimanali da app.first_launch, giorni 1/7/30); recent_events (campione grezzo per verificare l'instrumentazione); query (SQL in sola lettura, mode=ro, timeout, limite righe). Default ultimi 30 giorni — pochi tool componibili, il ragionamento lo fa Claude
- D15: autenticazione dell'MCP con lo stesso schema di Pulso e Celora — collegamento uniforme da Claude
- D16: nome del progetto: Sonda

## Agent limits
- L1: No sending, publishing, deploying, merging to main, purchases or emails: drafts and proposals only (default).

## Tools
- Coolify su Hetzner, bucket Cloudflare R2, Xcode per l'SDK Swift.

## Open questions
- Q1: Coolify gestisce depends_on con service_completed_successfully? Ripiego proposto: un solo container Litestream che avvia l'app con `litestream replicate -exec`.
- Q2: quale schema di autenticazione usano esattamente gli MCP di Pulso e Celora (da riprendere dal loro codice).
- Q3: dominio o sottodominio del servizio.
- Q4: valori del rate limit sulla ingest key.
- Q5: politica di conservazione degli eventi grezzi nel tempo.
- Q6: ordine di migrazione delle app da TelemetryDeck.

## Parked
- none

## Out of scope
- Dashboard o interfaccia web.
- Geolocalizzazione da IP.
- Multi-utente o versione SaaS.
- ClickHouse, DuckDB o altri database dedicati.

## Success
- La quarta app invia eventi a Sonda senza TelemetryDeck.
- Da Claude, via MCP, si ottengono funnel di onboarding e paywall e il breakdown di uso delle feature.
- Il ripristino da R2 è stato provato e funziona.
