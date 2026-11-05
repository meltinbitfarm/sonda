# Sonda

Self-hosted, lightweight telemetry for Fabio's iOS apps, replacing TelemetryDeck. Apps send events through
the Swift SDK in this repository; Claude reads them through an MCP server to analyse onboarding, feature
usage, drop-offs and paywall conversion. There is no web UI.

- Server: `https://sonda.meltinbitfarm.cloud` (Go, SQLite, Litestream backup to Cloudflare R2, Docker Compose on Coolify)
- SDK: Swift Package `Sonda`, this repository's URL, from version `0.1.0`
- Full SDK reference: [`sdk/README.md`](sdk/README.md) · Deploy and operations: [`docs/deploy.md`](docs/deploy.md)

## Integrating Sonda into an iOS app

This section is written for whoever (human or Claude Code) adds Sonda to an app. Read
[`sdk/README.md`](sdk/README.md) for every detail; the essentials are here.

### 1. Add the package

Xcode → File → Add Package Dependencies → `https://github.com/meltinbitfarm/sonda.git`, rule
"Up to Next Major Version" from `0.1.0`, add the `Sonda` library to the app target. The repository is
private: Xcode needs a GitHub account with access (Settings → Accounts). SwiftPM builds only
`sdk/Sources/Sonda`; the Go code in the repository is never compiled into the app.

Requirements: iOS 17+ / macOS 14+, Xcode 16+, Swift 6. No dependencies, no entitlements.

### 2. Configure once, at launch

```swift
import Sonda

@main
struct MyApp: App {
    init() {
        Sonda.configure(
            ingestKey: "sk_...",   // per-app key, ask Fabio (created with `sonda app create`)
            endpoint: URL(string: "https://sonda.meltinbitfarm.cloud")!
        )
    }
    var body: some Scene { WindowGroup { ContentView() } }
}
```

- The ingest key is write-only and public by design (it ships inside the binary): it may live in source.
  Each app has its own key; never reuse another app's.
- Use the `https` URL exactly as above, without a path.
- Call `configure` before any `track`; events tracked earlier are dropped.

Sonda then sends `app.first_launch`, `app.session_started` (new session after 30 min of inactivity) and
the automatic fields (app version and build, OS version, locale, region, storefront, device model and
type) by itself. Do not track these manually.

### 3. Instrument with the standard catalog

Use the helpers, so every app answers the same funnel queries:

| Moment | Call |
|---|---|
| Each onboarding screen shown | `Sonda.onboardingStepViewed(step: 1, name: "welcome")` (steps numbered from 1, names snake_case and stable) |
| Onboarding finished / skipped | `Sonda.onboardingCompleted()` / `Sonda.onboardingSkipped(atStep: 3)` |
| Main screens | `Sonda.screenViewed("settings")` |
| A meaningful feature used | `Sonda.featureUsed("export_pdf")` |
| Paywall displayed | `Sonda.paywallShown(placement: "onboarding", variant: "b")` |
| Purchase flow (StoreKit 2) | `Sonda.purchaseStarted(productID:)`, then `Sonda.purchaseResult(result, productID:)` with the `Product.PurchaseResult`, or `Sonda.purchaseFailed(error, productID:)` in `catch` |
| Restore purchases succeeded | `Sonda.purchaseRestored()` |
| Errors worth counting | `Sonda.errorOccurred(error)` (sends domain + code only) |

Custom events only when the catalog has nothing fitting:
`Sonda.track("export.finished", props: ["format": "pdf", "pages": 12])`.
Name `area.action_in_past_tense`, snake_case, ≤ 64 characters; props flat (string, number, bool),
snake_case keys, ≤ 2 KB. Prefer one generic event with a prop over many specific names.

**Never put personal data in props**: no names, emails, user-typed text, identifiers of the person.

### 4. Migrating from TelemetryDeck

Remove the TelemetryDeck package and its initialisation, then map each signal to the catalog above
instead of copying names one to one. Typical mapping:

| TelemetryDeck signal (example) | Sonda |
|---|---|
| app launched / session started | nothing: automatic |
| onboarding page N viewed | `onboardingStepViewed(step:name:)` |
| screen or view appeared | `screenViewed(_:)` |
| button or tool used | `featureUsed(_:)` |
| paywall shown | `paywallShown(placement:variant:)` |
| purchase success / cancel / error | `purchaseResult(_:productID:)` / `purchaseFailed(_:productID:)` |

Keep a short list of the events the app sends (name, props, where) in the app's own repository, so
analyses know what to expect.

### 5. Verify

Events are **dropped on purpose** in Debug builds, in the simulator, in Xcode installs and in TestFlight:
only production data reaches the server. So:

- In Debug, check the device console (subsystem `dev.sonda.sdk`): invalid or dropped events are logged.
- For a real end-to-end check use a Release build that is not TestFlight (App Store, or Ad Hoc), open
  the app, background it, then ask Claude via the Sonda MCP for `recent_events` of that app.

## Server

One Go binary (`sonda`) with:
- `POST /v1/events`: batch ingestion with the app's ingest key (validation, dedup by `event_id`,
  rate limit, daily quota, no IP stored);
- `/mcp/sonda`: MCP server (Streamable HTTP, secret token) with tools `list_apps`, `event_counts`,
  `funnel`, `retention`, `recent_events`, `query` (read-only SQL);
- CLI: `sonda app create|list|rotate-key`, `sonda serve`, `sonda healthcheck`, `sonda version`.

Run `sonda` commands on the server with `docker exec <app-container> /sonda …` (the image has no shell).
Wire contract for clients: [`.jerome/notes/ingest-contract.md`](.jerome/notes/ingest-contract.md).
Deploy, backup and restore: [`docs/deploy.md`](docs/deploy.md).

### Development

- Go tests (Go runs in Docker): [`.jerome/notes/recipes/go-via-docker.md`](.jerome/notes/recipes/go-via-docker.md)
- SDK tests: [`.jerome/notes/recipes/sdk-tests.md`](.jerome/notes/recipes/sdk-tests.md)
- Local restore test with MinIO: `scripts/restore-test.sh`
- Connecting Claude to the MCP: [`.jerome/notes/recipes/mcp-connect.md`](.jerome/notes/recipes/mcp-connect.md)
