# Scope

## Context
- Oggi TelemetryDeck (piano gratuito) è su 3 app; aggiungere la quarta richiederebbe il piano a pagamento.
- L'uso reale è solo l'analisi da Claude via MCP: nessuna dashboard serve.
- Infrastruttura: Hetzner + Coolify, solo Docker / Docker Compose (niente Nixpacks dopo i CVE PHP); backup su Cloudflare.
- Unico utente del servizio: Fabio.

## Out of scope
- Dashboard o interfaccia web.
- Geolocalizzazione da IP.
- Multi-utente o versione SaaS.
- ClickHouse, DuckDB o altri database dedicati.

## Success
- La quarta app invia eventi a Sonda senza TelemetryDeck.
- Da Claude, via MCP, si ottengono funnel di onboarding e paywall e il breakdown di uso delle feature.
- Il ripristino da R2 è stato provato e funziona.

## Tools
- Coolify su Hetzner, bucket Cloudflare R2, Xcode per l'SDK Swift.

## Area: sdk
- Stesso repository del server (D17): Package.swift nella radice punta ai sorgenti in sdk/; le app lo aggiungono con l'URL del repo di Sonda.
- Il contratto degli eventi (D10–D13) è condiviso con il server: una modifica tocca entrambi nello stesso commit.
- Out of scope: vedi Out of scope sopra.
