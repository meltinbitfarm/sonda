# Areas

## main
- Goal: Servizio di telemetria self-hosted e leggerissimo che sostituisce TelemetryDeck per le app iOS di Fabio: raccoglie eventi tramite un SDK Swift e li espone a Claude via MCP per analizzare onboarding, uso delle feature, abbandoni e conversioni del paywall.
- Milestone: first
- Criteria: La quarta app invia eventi a Sonda senza TelemetryDeck.; Da Claude, via MCP, si ottengono funnel di onboarding e paywall e il breakdown di uso delle feature.; Il ripristino da R2 è stato provato e funziona.
- Folder: .
- Agents: developer, analyst, researcher, documentarist
- Pace: daily
- Priority: 1

## sdk
- Goal: SDK Swift (Swift Package in sdk/, Package.swift nella radice del repo) che manda gli eventi a Sonda secondo D8 e D10–D12.
- Milestone: first
- Criteria: la quarta app aggiunge il pacchetto dall'URL del repo e invia eventi di produzione a Sonda; coda su disco, batch e retry offline coperti da test; eventi in DEBUG, TestFlight e simulatore scartati.
- Folder: sdk/
- Agents: developer, analyst, documentarist
- Pace: daily
- Priority: 2
