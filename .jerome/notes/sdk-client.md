# SDK Swift client (area sdk, step 1)

Branch `sdk/1-client` (from `server/2-ingest`), 2026-11-04. No Go code touched.
Tests: `swift test` 36/36 (incl. the skipped live suite), `xcodebuild test` on iPhone 17 / iOS 26.2 34/34,
Release builds for generic iOS, macOS and Mac Catalyst clean, no warnings. Live end-to-end against
`sonda serve` (Docker) passed: 100 accepted, resend 100 duplicates, 23 accepted, rows stored as expected.
Commands: notes/recipes/sdk-tests.md. Integration guide: sdk/README.md.

## Layout
- `Package.swift` (root): tools 6.0, Swift 6 language mode, library `Sonda`, target path `sdk/Sources/Sonda`,
  tests `sdk/Tests/SondaTests`.
- `Sonda.swift` public API (`configure`, `track`, `flush`); `Sonda+Catalog.swift` D12 helpers;
  `SondaValue.swift` prop values (string/int/double/bool, literal and interpolation friendly);
  `Engine.swift` actor (identity, session, queue, delivery) + `Dependencies` (injection);
  `EventStore.swift` disk queue; `Transport.swift` request/batching/response table; `Gzip.swift`;
  `DeviceInfo.swift`; `AppEnvironment.swift` production check + storefront + timeout; `Lifecycle.swift`.

## Choices (proposals for decisions.md, lead to decide)
1. **Platforms iOS 17 / macOS 14.** Everything needed exists there (AppTransaction.environment iOS 16,
   Storefront iOS 15, OSAllocatedUnfairLock iOS 16); iOS 18 would only buy `Mutex` and drop iOS 17 devices.
   Mac Catalyst and iPad-on-Mac build and report `device_type = mac`.
2. **Ordering:** public calls are synchronous and push commands into one AsyncStream consumed by one task,
   so events are processed in call order (first_launch/session_started always precede the app's first event)
   and `track` never blocks. ts and event_id are captured at call time.
3. **Startup:** the engine awaits the environment check (AppTransaction, 10 s timeout) and
   Storefront.current (5 s timeout) before processing commands; calls made meanwhile wait in memory.
4. **Production check:** `#if DEBUG` -> drop; `targetEnvironment(simulator)` -> drop; AppTransaction
   environment `.production` sends, `.sandbox` (TestFlight) and `.xcode` drop; if AppTransaction is
   unavailable, receipt file named `sandboxReceipt` -> drop, else send. In non-production nothing is
   written: no queue file, no user_id in UserDefaults. Injectable via `Dependencies.environment`.
   Note: `#if DEBUG` is the package's own build configuration, which Xcode sets equal to the app's.
5. **Identity:** user_id and session_id are lowercase UUIDs in UserDefaults.standard (`dev.sonda.*` keys).
   UserDefaults is backed up, so a restored/migrated device keeps the same user_id (no new first_launch).
   Session is persisted with its last-activity time: a relaunch within 30 min continues the session.
   On the first launch the order is `app.session_started`, then `app.first_launch` (same ts, same session).
6. **Session rule:** new session when an event, a foreground or a launch comes > 30 min after the last
   activity; every event, foreground and background updates the last activity.
7. **device_model on Mac:** `hw.model` (e.g. `Mac14,2`), not `hw.machine` as the brief said: on Apple
   silicon `hw.machine` returns `arm64`, which says nothing. Simulator uses SIMULATOR_MODEL_IDENTIFIER.
   device_type derived from the model prefix (iPhone/iPod -> phone, iPad -> pad), so no UIKit/MainActor.
8. **os_version** `major.minor` plus `.patch` when non-zero (`26.1`, `26.1.2`).
9. **Queue:** JSON Lines in Application Support/Sonda/queue.jsonl (macOS non-sandboxed:
   Application Support/<bundle id>/Sonda), directory excluded from backup, iOS file protection
   `completeUntilFirstUserAuthentication` on rewrites. Each line is the exact wire JSON of an event,
   encoded once at creation: retries are byte-identical and keep the same event_id. Append per event,
   atomic rewrite on removal. Torn last line after a crash is skipped. Cap 2000 events, oldest dropped.
   Events older than 30 days are dropped at launch and before every send.
10. **Batches:** ≤ 100 events and ≤ 250 KB uncompressed JSON (server limit 256 KB applies after gunzip
    too), always gzip (Foundation raw deflate + own gzip header/CRC-32; checked against /usr/bin/gzip and
    the real Go server).
11. **Responses** (contract table): 2xx remove batch (accepted, duplicates, rejected alike; rejection
    reasons logged at debug); 401/403 stop until next launch, keep queue; 413 halve the batch size and
    retry, a single event that gets 413 is dropped; 429 wait Retry-After (seconds or HTTP date), backoff
    if missing; 408, 5xx and network errors backoff; other 4xx drop the batch. Backoff: 5 s · 2^(n-1),
    cap 10 min, random point in the upper half (equal jitter). Batch size doubles back after a success.
12. **Triggers:** timer every 30 s while running, foreground, background (inside a UIKit background task
    on iOS; on macOS didBecomeActive/didResignActive), when the queue reaches the batch size, `Sonda.flush()`.
13. **Client-side validation** mirrors D11/D13: name pattern and ≤ 64, prop keys snake_case ≤ 64, no
    NaN/inf, props ≤ 2048 bytes measured as Go's json.Marshal would store them (`<`, `>`, `&` escaped
    as 6 bytes). Optional fields cleaned of control characters and cut at 64 bytes. Invalid events are
    dropped and logged (os.Logger, subsystem `dev.sonda.sdk`).
14. **Catalog extras:** `purchaseResult(_:productID:)` maps StoreKit 2 results (success verified ->
    completed, userCancelled -> cancelled, pending -> failed{error:"pending"}, unverified ->
    failed{error:"unverified"}); `purchaseFailed` and `errorOccurred` accept an `Error` and send only
    NSError domain/code. `purchase.failed` also carries `product_id` when known (extra prop beyond D12).
15. Second `configure` call is ignored; `track` before `configure` is dropped with a log line.

## Tests (sdk/Tests/SondaTests)
- ContractTests: headers, path, gzip magic, body shape `{"events":[...]}`, every field and type, integer
  ms ts, lowercase UUIDs, flat props, no unknown keys; invalid names/props dropped on device; Go-escaping
  size; field cleaning; gzip CRC + round trip; system gunzip (macOS); device type / os version.
- DeliveryTests: queue survives restart and resends byte-identical events with the same ids; torn line;
  cap; >30-day events dropped; batches 100/100/50; body ≤ 256 KB with big props; 200 with
  duplicates/rejections removes; 400/404/415/422 drop; 401 keeps queue, stops, next launch resumes;
  413 split; single 413 dropped; 503 backoff 5/10/20 s and nothing sent before the deadline; network
  error; backoff cap and jitter bounds; 429 Retry-After 120 s; Retry-After parsing (seconds, HTTP date);
  429 without header.
- SessionTests: first launch, user_id persisted and first_launch once, relaunch < 30 min keeps the
  session, rollover after 30 min of inactivity (sliding), foreground after long background, non-production
  environments (debug, simulator, xcode, TestFlight) store and send nothing, the real check is not
  production in tests, storefront cached, timeout helper.
- PublicAPITests: `Sonda.configure` + every catalog helper + `track` reach the server in call order with
  the right props; second configure ignored.
- LiveServerTests: optional, real `sonda serve` (recipe).

## Open points
- No version tag yet: the README suggests `from: "0.1.0"`. Tags are shared with the server (D17); the lead
  must decide the first tag (and whether it is created on main after merge).
- Debug visibility: since DEBUG builds drop everything, there is no public switch to send from a dev build
  to a test server. Could add an opt-in (e.g. `allowNonProduction:` for a staging endpoint) if Fabio wants
  to verify instrumentation before release.
- App extensions: `Lifecycle` uses `UIApplication.shared`; using Sonda inside an extension would need a
  variant without it. Not needed for the fourth app as far as known.
- Storefront changes during a run are not observed (`Storefront.updates`); refreshed at each launch.
- AppTransaction.shared may hit the network on first launch; if it times out we fall back to the receipt
  name. An App Store install without a receipt and without AppTransaction counts as production.
- Swift 6.4 / Xcode 27 used locally; Package.swift declares tools 6.0, so Xcode 16+ can resolve it.
- App Privacy label for the fourth app: "Product Interaction" + "Other Diagnostic Data", not linked to
  identity, no tracking (Fabio fills it in App Store Connect).
