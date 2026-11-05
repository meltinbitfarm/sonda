# Sonda Swift SDK

Sends app events to a self-hosted Sonda server. iOS 17+ / macOS 14+ (Mac Catalyst and iPad apps on Mac
included), Swift 6, no dependencies. The package needs swift-tools 6.0, i.e. Xcode 16 or later.

The SDK links StoreKit (to tell App Store builds from TestFlight and to read the storefront). No
capability or entitlement has to be added to the app.

## Add the package

Xcode: File > Add Package Dependencies, paste the repository URL

    https://github.com/meltinbitfarm/sonda.git

pick a version tag (or a branch while developing; the tags are shared with the Go server) and add the `Sonda` library to the app target.
The repository also contains the Go server; SwiftPM only builds `sdk/Sources/Sonda`.

In a `Package.swift`:

```swift
.package(url: "https://github.com/meltinbitfarm/sonda.git", from: "0.2.0"),
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

`endpoint` is the server's base URL; the SDK posts to `<endpoint>/v1/events`. Use the `https` URL: a
redirect from `http` turns the POST into a GET and the server never sees the events (they stay queued).

Events tracked before `configure` are dropped (and logged), so call it before the first `track`.
`configure` does no file or network work on the calling thread.

From then on Sonda sends, by itself:
- `app.first_launch` on the first launch (a random `user_id` is created and kept in UserDefaults),
- `app.session_started` whenever a session starts (after 30 minutes without activity, D10),
- the automatic fields on every event: app version and build, OS version, locale, region, App Store
  storefront, raw device model (`iPhone16,2`), device type (`phone`/`pad`/`mac`) and environment (below).

## Environments

Every build sends, and every event says where it comes from in its `environment` field (D23):

| environment | when |
|---|---|
| `development` | the build is compiled with the `DEBUG` flag, runs in the simulator, or StoreKit says it was installed from Xcode |
| `testflight` | StoreKit says TestFlight (sandbox) or, when StoreKit cannot answer (offline at launch), the receipt is a sandbox receipt |
| `production` | everything else: App Store builds, and Release builds distributed outside the App Store (Ad Hoc, Enterprise, a Developer ID Mac app) |

The server's tools count **only `production`** unless asked otherwise, so your own tests never mix with
real users. To check instrumentation, run a Debug build and ask Claude for the recent events with
environment `development` (or `testflight` for a TestFlight build); the device console (subsystem
`dev.sonda.sdk`) also logs rejected events.

Notes:
- `DEBUG` is the flag Xcode's standard Debug configuration sets. A custom debug configuration without it is
  caught only by the simulator and StoreKit checks, so keep using the standard configurations.
- The environment is decided once per launch (StoreKit may take up to 10 s offline) and stamped on each
  event when it is recorded; a queued event keeps the environment it was recorded with.
- Test events count toward the app's daily server quota (50,000 events per day by default) like any other.
- The random `user_id` lives in UserDefaults, which usually survives replacing a Debug or TestFlight install with
  the App Store build on the same device. That user's `app.first_launch` then stays in the test
  environment, so the device does not appear as a new production user in retention cohorts.
- SDK 0.1.0 dropped test builds instead and sends no `environment`; the server stores its events as
  `production`, which is what they are.

## Standard events (D12, D24)

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
    Sonda.purchaseResult(result, productID: product.id)   // completed / cancelled / pending / failed
} catch {
    Sonda.purchaseFailed(error, productID: product.id)      // exact StoreKit case + domain/code
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
| `purchasePending(productID:)` | `purchase.pending` | `product_id` |
| `purchaseFailed(_:productID:)` | `purchase.failed` | `error`, `product_id`, `error_domain`, `error_code`, `underlying_error_domain`, `underlying_error_code`, `verification_error` |
| `purchaseFailed(error:productID:)` | `purchase.failed` | `error`, `product_id` |
| `purchaseRestored()` | `purchase.restored` | |
| `errorOccurred(domain:code:)` | `error.occurred` | `domain`, `code` |

### Purchases in detail

`purchaseResult(_:productID:)` maps a StoreKit 2 result:

| result | event | props |
|---|---|---|
| `.success(.verified)` | `purchase.completed` | `product_id` |
| `.success(.unverified)` | `purchase.failed` | `error: "unverified"`, `verification_error` (e.g. `invalidSignature`), `product_id` |
| `.userCancelled` | `purchase.cancelled` | `product_id` |
| `.pending` (Ask to Buy, a payment waiting for approval) | `purchase.pending` | `product_id` |
| a result added by a future StoreKit | `purchase.failed` | `error: "unknownResult"`, `product_id` |

A pending purchase is not a failure: it may still complete later, and then `Transaction.updates`
delivers it (send `purchaseCompleted` from there if you want to count it).

`purchaseFailed(_ error:productID:)` turns a thrown error into flat props, never the error message:

- `error`: the exact case, as a short code. The `StoreKitError` case name (`networkError`,
  `systemError`, `notEntitled`, `notAvailableInStorefront`, `unsupported`, `unknown`, ...) or the
  `Product.PurchaseError` case name (`purchaseNotAllowed`, `ineligibleForOffer`,
  `invalidOfferSignature`, ...); `other` for any other error; `storeKitOther` / `purchaseErrorOther`
  for a case added by a newer StoreKit.
- `error_domain`, `error_code`: the NSError domain and code of the error.
- `underlying_error_domain`, `underlying_error_code`: the cause, when there is one: the error carried
  by `networkError` and `systemError`, otherwise `NSUnderlyingErrorKey`. This is what tells an
  `unknown` apart (e.g. `NSURLErrorDomain` `-1009` = offline).

A thrown `StoreKitError.userCancelled` sends `purchase.cancelled`, not `purchase.failed`, so a plain
`catch { Sonda.purchaseFailed(error, productID: product.id) }` is always right.

Need your own props on the same event (where the paywall was opened, for example)? Build the detail
with `Sonda.PurchaseFailure` and merge it:

```swift
let failure = Sonda.PurchaseFailure(error)          // or PurchaseFailure(verificationError:)
Sonda.track(Sonda.Event.purchaseFailed,
            props: failure.props.merging(["product_id": .string(product.id), "placement": "onboarding"]) { a, _ in a })
```

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
`Retry-After` up to one hour. A batch is removed only when the server confirms it (a 200 with the ingest
response; a captive portal's 200 page does not count) or refuses it for good (400, 415); any other status,
404 and 405 from a proxy included, is retried. The queue keeps at most 2000 events (when full the oldest
200 are dropped at once) and drops events older than 30 days (the server refuses them). If the server
refuses the ingest key (401) sending stops until the next launch, the queue is kept. `Sonda.flush()` asks
for an immediate send. When the app goes to the background the send runs inside a background task that
ends when the request in flight has finished.

## Privacy

No IDFV, no IDFA, no IP stored server-side (D9, D10). `user_id` is a random UUID kept in UserDefaults:
iCloud backup and device-to-device migration restore it, so two devices of the same person may share it.
Each event carries device model, OS version, locale, region and App Store storefront: together they are a
coarse device fingerprint, accepted by D10 and to be declared as such in the App Store privacy details.
Props are the only place personal data could enter: keep them free of it (D11).

## Tests

See `.jerome/notes/recipes/sdk-tests.md` in this repository.
