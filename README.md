# Sonda

**Sonda** is a small, self-hosted analytics service for iOS and macOS apps. Your apps send events through
a Swift package; you ask questions about them through Claude, using an MCP server. There is no dashboard.

It was built to replace TelemetryDeck for a handful of indie apps. The questions it answers are the
usual ones: where do people drop out of onboarding, which features get used, how well does the
paywall convert, do people come back after a week?

## Why

- **Tiny to run.** One Go binary and a SQLite file. Litestream streams the database to Cloudflare R2
  (or any S3-compatible storage) for backup. It runs comfortably next to other things on a small VPS.
- **No dashboard to build or maintain.** You ask Claude, which uses a few composable MCP tools: counts,
  funnels, retention and read-only SQL.
- **Test without polluting your numbers.** Every event is marked `production`, `testflight` or
  `development` (Debug builds, the simulator). Claude sees only real users unless you ask for the tests.
- **Privacy by default.** No IP addresses are stored, there is no IP geolocation and no advertising
  or vendor identifiers. Users are identified by a random UUID created on the device.

## How it works

```
iOS app ── Sonda SDK ──▶ POST /v1/events ──▶ sonda (Go) ──▶ SQLite ──▶ Litestream ──▶ R2
                                                │
                              Claude ◀── MCP ───┘  /mcp/sonda
```

- The **SDK** queues events on disk and sends them in gzip batches. It retries when offline and fills
  in device and app details automatically.
- The **server** checks each event, removes duplicates and stores it. Each app has its own write-only
  ingest key.
- The **MCP endpoint**, protected by a secret token, gives Claude these tools: `list_apps`,
  `event_counts`, `funnel`, `retention`, `recent_events` and `query` (read-only SQL). All of them look at
  production events unless told to use `testflight`, `development` or `all`.

## Using the SDK in an app

Requirements: iOS 17 or macOS 14, Xcode 16 or later. No dependencies.

**1. Add the package.** In Xcode choose File → Add Package Dependencies, enter
`https://github.com/meltinbitfarm/sonda.git`, pick version `0.2.0` or later and add the `Sonda` library
to your app target. Only the Swift sources in `sdk/` are built: the server code in this repository never
ends up in your app.

**2. Configure it once, at launch:**

```swift
import Sonda

@main
struct MyApp: App {
    init() {
        Sonda.configure(
            ingestKey: "sk_...",                                  // from `sonda app create`
            endpoint: URL(string: "https://sonda.example.com")!   // your server
        )
    }
    var body: some Scene { WindowGroup { ContentView() } }
}
```

The SDK records first launch and sessions on its own. A new session starts after 30 minutes of
inactivity. Each event carries the app version, OS version, locale, region, App Store storefront and
device model.

**3. Track what matters.** A standard set of events keeps every app comparable:

```swift
Sonda.onboardingStepViewed(step: 1, name: "welcome")
Sonda.onboardingCompleted()
Sonda.screenViewed("settings")
Sonda.featureUsed("export_pdf")
Sonda.paywallShown(placement: "onboarding", variant: "b")

Sonda.purchaseStarted(productID: product.id)
let result = try await product.purchase()
Sonda.purchaseResult(result, productID: product.id)   // completed, cancelled, pending or failed
```

If none of those fit, track a custom event:

```swift
Sonda.track("export.finished", props: ["format": "pdf", "pages": 12])
```

Event names look like `area.action_in_past_tense`. Props are flat: strings, numbers and booleans only.
**Never put personal data in props.**

Every build sends events, marked with where it runs: `development` for Debug builds, the simulator and
Xcode installs, `testflight` for TestFlight, `production` for the App Store. The MCP tools count only
production by default, so to check your instrumentation run a Debug build and ask Claude something like
*"Show me the recent development events of MyApp"*.

The full SDK guide covers the event catalogue, delivery and retries, privacy, and moving over from
TelemetryDeck: [`sdk/README.md`](sdk/README.md).

## Running the server

Sonda ships as a Docker Compose stack with three services that share one volume:

- `restore`: on first start, restores the database from the R2 backup if one exists;
- `app`: the Sonda server;
- `litestream`: replicates every write to R2.

Losing the server costs nothing: deploy the same stack anywhere with the same R2 settings and the data
comes back by itself.

Step-by-step deployment on Coolify, including R2 setup, environment variables, limits and a restore
drill: [`docs/deploy.md`](docs/deploy.md).

Managing apps (the image has no shell, so run the binary directly):

```sh
docker exec <app-container> /sonda app create "My App"      # prints the ingest key once
docker exec <app-container> /sonda app list
docker exec <app-container> /sonda app rotate-key "My App"  # new key, the old one stops working
```

## Connecting Claude

The MCP endpoint is `https://<your-host>/mcp/sonda`. It uses Streamable HTTP and the token set in
`SONDA_MCP_TOKEN`.

```sh
# Claude Code
claude mcp add --transport http sonda https://<your-host>/mcp/sonda \
  --header "Authorization: Bearer <token>"
```

On claude.ai or the Claude apps, add a custom connector with the URL
`https://<your-host>/mcp/sonda?token=<token>`.

Then ask Claude things like *"How does the onboarding funnel of MyApp look over the last 30 days?"* or
*"Which features do paying users use most?"*

## Development

- Go server: `go test ./...`. If Go isn't installed you can use Docker; see
  [`.jerome/notes/recipes/go-via-docker.md`](.jerome/notes/recipes/go-via-docker.md).
- Swift SDK: `swift test`, or `xcodebuild test` on an iOS simulator.
- Backup and restore end to end, using MinIO in place of R2: `scripts/restore-test.sh`.
- The event format the SDK sends to the server:
  [`.jerome/notes/ingest-contract.md`](.jerome/notes/ingest-contract.md).
