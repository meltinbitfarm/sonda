# Sonda Swift SDK

Sends app events to a self-hosted Sonda server. iOS 17+ / macOS 14+ (Mac Catalyst and iPad apps on Mac
included), Swift 6, no dependencies.

## Add the package

Xcode: File > Add Package Dependencies, paste the repository URL

    https://github.com/meltinbitfarm/sonda.git

pick a version tag (or a branch while developing) and add the `Sonda` library to the app target.
The repository also contains the Go server; SwiftPM only builds `sdk/Sources/Sonda`.

In a `Package.swift`:

```swift
.package(url: "https://github.com/meltinbitfarm/sonda.git", from: "0.1.0"),
// target dependency:
.product(name: "Sonda", package: "sonda"),
```

## Configure

Create the app on the server and copy the key it prints (the key is public by design, D7):

    sonda app create "My App"

Then, once, as early as possible:

```swift
import Sonda

@main
struct MyApp: App {
    init() {
        Sonda.configure(ingestKey: "sk_...", endpoint: URL(string: "https://sonda.example.com")!)
    }
    var body: some Scene { WindowGroup { ContentView() } }
}
```

`endpoint` is the server's base URL; the SDK posts to `<endpoint>/v1/events`.

From then on Sonda sends, by itself:
- `app.first_launch` on the first launch (a random `user_id` is created and kept in UserDefaults),
- `app.session_started` whenever a session starts (after 30 minutes without activity, D10),
- the automatic fields on every event: app version and build, OS version, locale, region, App Store
  storefront, raw device model (`iPhone16,2`), device type (`phone`/`pad`/`mac`).

## Only production sends

Events are **dropped** (not stored, not sent) in DEBUG builds, in the simulator, in Xcode installs and in
TestFlight (D8). Only App Store builds send. The check uses `AppTransaction.environment`, with the
sandbox receipt as fallback. To see that instrumentation works, use a Release build from the App Store or
read the device console (subsystem `dev.sonda.sdk`): rejected events are logged there.

## Standard events (D12)

Prefer these helpers: the same names and props in every app make the same funnels work everywhere.

```swift
// Onboarding
Sonda.onboardingStepViewed(step: 1, name: "welcome")
Sonda.onboardingSkipped(atStep: 3)
Sonda.onboardingCompleted()

// Usage
Sonda.screenViewed("settings")
Sonda.featureUsed("export_pdf")

// Paywall and purchases (StoreKit 2)
Sonda.paywallShown(placement: "onboarding", variant: "b")
Sonda.purchaseStarted(productID: product.id)
do {
    let result = try await product.purchase()
    Sonda.purchaseResult(result, productID: product.id)   // completed / cancelled / failed
} catch {
    Sonda.purchaseFailed(error, productID: product.id)      // error = "domain:code"
}
Sonda.purchaseRestored()

// Errors
Sonda.errorOccurred(error)                        // domain + code, never the message
Sonda.errorOccurred(domain: "sync", code: 3)
```

| helper | event | props |
|---|---|---|
| `onboardingStepViewed(step:name:)` | `onboarding.step_viewed` | `step`, `step_name` |
| `onboardingCompleted()` | `onboarding.completed` | |
| `onboardingSkipped(atStep:)` | `onboarding.skipped` | `at_step` |
| `screenViewed(_:)` | `screen.viewed` | `screen` |
| `featureUsed(_:)` | `feature.used` | `feature` |
| `paywallShown(placement:variant:)` | `paywall.shown` | `placement`, `variant` |
| `purchaseStarted/Completed/Cancelled(productID:)` | `purchase.started/completed/cancelled` | `product_id` |
| `purchaseFailed(error:productID:)` | `purchase.failed` | `error`, `product_id` |
| `purchaseRestored()` | `purchase.restored` | |
| `errorOccurred(domain:code:)` | `error.occurred` | `domain`, `code` |

## Custom events

```swift
Sonda.track("export.finished", props: ["format": "pdf", "pages": 12, "pro": true])
```

Rules (checked on the device too, an invalid event is dropped and logged):
- name `area.action_in_past_tense`, snake_case, at most 64 characters (`export.finished`, not `ExportFinished`);
- props flat: values string, number or bool; keys snake_case; at most 2 KB in total;
- never personal data in props (no names, emails, free text typed by the user) (D11).

Prefer a few generic events with details in props (`feature.used {feature: "export_pdf"}`) over many
specific names.

## Delivery

Events go to a queue file in Application Support (excluded from backups), so they survive restarts and
offline periods. Sonda sends in gzip batches of up to 100 events every 30 s while the app runs, when it
comes to the foreground and when it goes to the background, and when 100 events are waiting.
On errors it retries with exponential backoff (5 s doubling up to 10 min, with jitter) and honours
`Retry-After`. The queue keeps at most 2000 events and drops events older than 30 days (the server
refuses them). If the server refuses the ingest key (401) sending stops until the next launch, the queue
is kept. `Sonda.flush()` asks for an immediate send.

Nothing here identifies a person: no IDFV, no IDFA, no IP stored server-side (D9, D10).

## Tests

See `.jerome/notes/recipes/sdk-tests.md` in this repository.
