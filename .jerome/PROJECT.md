# Sonda

## Goal
Servizio di telemetria self-hosted e leggerissimo che sostituisce TelemetryDeck per le app iOS di Fabio: raccoglie eventi tramite un SDK Swift e li espone a Claude via MCP per analizzare onboarding, uso delle feature, abbandoni e conversioni del paywall.

## Constraints
- V1: infrastruttura il più leggera e semplice possibile.
- V2: deploy solo con Docker Compose su Coolify.
- V3: nessuna interfaccia web.

## Agent limits
- L1: No sending, publishing, deploying, merging to main, purchases or emails: drafts and proposals only (default).
