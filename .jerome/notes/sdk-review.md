# Swift SDK review before the 4th app (2026-11-04, branch release/first)

Scope: Package.swift, sdk/Sources/Sonda, sdk/Tests, sdk/README.md against internal/ingest/ingest.go and ingest-contract.md.
`swift test`: 36 tests in 5 suites pass. Not exercised: real device, TestFlight, App Store build (cannot be tested from here).

## Must-fix before release

M1. 200 with an unreadable body counts as delivered (data loss). Transport.swift:73-81: `try? decode` yields nil and
`.delivered(0,0,0)` is returned, so the whole batch is removed (Engine.swift:270). A captive-portal page (URLSession follows the
redirect and gets 200 HTML), a proxy or a Coolify/Traefik page returning 200 silently erases the queue.
Fix: treat 200 as delivered only if the body decodes to IngestResponse with `accepted` non-nil; otherwise `.retry`. Add a test.

M2. Any unlisted 4xx drops the batch, 404 and 405 included (Transport.swift:90-91; the test at DeliveryTests.swift:122 locks in 404).
404 is what Traefik/Coolify returns when no route matches (redeploy, wrong path); a 301/302 http->https redirect turns the POST
into a GET and ends in 404/405. A mistyped endpoint during integration, or a redeploy, deletes data permanently.
Fix: drop only on 400 and 415 (the contract); every other 4xx (404, 405, 409, 410, 421, 425...) goes to `.retry` with backoff.
Change the test arguments to [400, 415].

M3. A second flush ends the background task early. Engine.swift:254 `!isFlushing` returns at once, so on
`.background` (Engine.swift:165-171) `done()` runs while the periodic flush is still sending, and iOS may suspend mid-request.
Fix: keep the running flush as a `Task` and let a second caller `await` it (or have `done` wait for the in-flight flush).
Consequence today is a delayed resend (no loss, the event_id dedups), so the severity is borderline; it hurts exactly the
background send the README promises.

## Should-fix

S1. Retry-After has no upper bound (Engine.swift:287-289). A proxy answering 429 with `86400` pauses sending for a day, while the
queue cap drops events. Clamp to 1 h (or backoffCap).

S2. Queue at the cap rewrites the whole file on every event (EventStore.swift:31-35, up to ~1 MB each call, on the actor, i.e. a long
offline period). Fix: trim in blocks (e.g. to 90% of the cap), so the rewrite happens once every 200 events.

S3. Release builds distributed outside the App Store (Ad Hoc, Enterprise, Developer ID Mac) count as `.production` when
AppTransaction throws (AppEnvironment.swift:23-27). Fine for the App Store flow, but document it in the README ("only App Store
builds send" is stricter than the code). Also, `#if DEBUG` depends on the app's configuration name: a custom Release-like
configuration works (AppTransaction covers it), a custom debug configuration without DEBUG is protected only by the second
layer. Say "use Debug/Release" in the README.

S4. Clock moved backwards: `touchSession` (Engine.swift:208) returns without updating `lastActivity`, so a session never rolls over until
the clock catches up. Use a monotonic fallback or reset when `last - date` > timeout. Also events with ts >24 h ahead (wrong clock)
are rejected by the server (ingest.go:97) and the SDK only learns it in DEBUG logs. Optional: clamp `ts` to `now`.

S5. `Sonda.configure` does synchronous file and system work on the caller (usually main): `EventStore.defaultURL()` (createDirectory,
setResourceValues), `DeviceInfo.current()` (sysctl/uname), `URLSession(configuration:)` at Sonda.swift:22 -> Engine.swift:31-49.
Likely 1-3 ms, but cheap to avoid: build `Dependencies.live` inside the detached task (it needs no main-actor state).
The queue load and AppTransaction/Storefront run in the detached utility task: correct, nothing blocks main, and tracked calls are buffered
in the AsyncStream meanwhile (up to 10 s offline on a first launch; unbounded buffer but only tiny structs).

S6. `user_id` lives in UserDefaults, which iCloud backup and device-to-device migration restore: two devices can share a `user_id`.
Acceptable (device_model differs), but mention it in the README privacy line or store it in a file excluded from backups, like the queue.

S7. README: (a) `swift-tools 6.0` / Xcode 16+ is required by the package: say so. (b) Add "events tracked before configure are dropped"
(Sonda.swift:63-67). (c) Mention that StoreKit is linked (import StoreKit pulls no entitlement, but the README should say that no capability is needed).
(d) The tag in the example `from: "0.1.0"` does not exist yet (shared tags with the Go server, D17): create it before the 4th app
adopts. (e) Document the privacy fields (device_model, locale, region, storefront are combined fingerprint data, accepted by D10).

## Notes (verified correct)

N1. Contract conformance. Field names, types and case match ingest.go:50-65 (event_id, user_id, session_id lowercase UUID; ts Int64 ms,
`rounded(.down)`; optional fields omitted when nil or empty, so no `null`/`""` problem). Name regex is re-implemented byte-wise and
equals `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$` (both ASCII-only, utf8 length <= 64). Props: keys regex, finite numbers, flat by type,
2048-byte check computed with Go's escaping (`<`,`>`,`&`,U+2028/9, Event.swift:99-111). One small gap: Go escapes control characters in
prop strings as `\u00XX` (6 bytes) where Swift writes `\n` (2 bytes); a value full of control characters could pass the client check and be
rejected by the server (one event, never the batch). Negligible.
N2. Limits. 100 events per batch, body kept under 250 KiB, 413 halves the batch, single event too large is dropped (Transport.swift:21-47).
Gzip is raw deflate + header + CRC32/size trailer, tested against a gunzip and the live server test (LiveServerTests, skipped without env).
Order is preserved, `event_id` is created in `track` (Sonda.swift:28) and persisted with the JSON, retries resend identical bytes.
N3. Duplicates: lost 200, app killed during send and retry after timeout are safe by event_id (server dedup). Events recorded while a
send is in flight are not removed (removal by id set, Engine.swift:266-270). Actor reentrancy is harmless here.
N4. Queue crash safety: append is a single write; a torn last line is skipped on load and the file rewritten (EventStore.swift:59-77,
test tornLastLineIsSkipped); full rewrites are atomic. Window: a crash between UserDefaults write and queue append can lose one event
(not a concern). One process per queue (Application Support/<bundle id>/Sonda on macOS); app extensions with their own Sonda.configure would
have their own sandbox queue, no conflict. Multiple `Sonda.configure` calls are ignored safely.
N5. Production filter: `#if DEBUG` -> simulator -> `AppTransaction.shared` (iOS 16+/macOS 13+, so fine on 17/14), timeout 10 s, `.sandbox`
= TestFlight, `.xcode`; offline fallback `sandboxReceipt` correct for TestFlight and Xcode installs. Mac TestFlight is `.sandbox` too.
Dropping is early: nothing is queued or created (no user_id) in non-production.
N6. Session: 30 min measured from last activity (track, foreground, background), `first_launch` opens a fresh session, session_started is
emitted once per rollover with the same ts, out-of-order tracked dates do not move `lastActivity` backwards. Covered by SessionTests.
N7. Concurrency: Swift 6 language mode, an actor owns state, commands go through an AsyncStream (order kept, never blocks the caller),
`@unchecked Sendable` only on the UserDefaults wrapper (documented thread-safe). Lifecycle observers use `.main` queue +
`assumeIsolated`; no main-thread work except the observer closure. 401/403 pauses until next launch, queue kept (the cap still trims).
N8. Privacy: no IDFV/IDFA, random UUID, no IP, logs print only event name (public) and reasons; error helpers send domain+code, never messages.
Matches D10. Props are the only place personal data could enter; this relies on D11 and README rules.
N9. App-launch cost: main thread = one lock, an AsyncStream and a Task creation, plus S5. Everything else is utility priority. The 30 s timer
is cheap; the process is suspended in background.
