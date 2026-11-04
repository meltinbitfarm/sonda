import Foundation
import Testing
@testable import Sonda

/// Disk queue, batching and the response table of the ingest contract.
@Suite struct DeliveryTests {
    // MARK: Queue persistence

    @Test func queueSurvivesRestartAndResendsSameEvents() async throws {
        let h = Harness()
        h.server.respond { _ in MockServer.Response(status: 0) } // offline
        let first = await h.engine()
        await first.track("feature.used", ["feature": "export"], at: h.clock.now)
        await first.flush()
        let queued = await first.queuedEvents
        #expect(queued.count == 3) // session_started, first_launch, feature.used
        #expect(h.server.requests.count == 1)

        // "Restart": a new engine on the same file and defaults, a minute later, back online.
        h.clock.advance(60)
        h.server.respond { .ok(accepted: $0.events.count) }
        let second = await h.engine()
        #expect(await second.queuedEvents == queued)
        #expect(await second.userID == first.userID)
        #expect(await second.queuedNames == ["app.session_started", "app.first_launch", "feature.used"])
        await second.flush()

        let last = try #require(h.server.requests.last)
        #expect(last.eventIDs == queued.map(\.id)) // same ids: the server can skip duplicates
        #expect(last.json == Transport.body(for: queued)) // byte-identical retry
        #expect(await second.queuedEvents.isEmpty)
        let fileData = try Data(contentsOf: h.queueURL)
        #expect(fileData.isEmpty)
    }

    @Test func tornLastLineIsSkipped() async throws {
        let h = Harness()
        let e = await h.engine()
        await e.track("feature.used", at: h.clock.now)
        let count = await e.queuedEvents.count
        let handle = try FileHandle(forWritingTo: h.queueURL)
        try handle.seekToEnd()
        try handle.write(contentsOf: Data(#"{"event_id":"abc","na"#.utf8))
        try handle.close()

        let store = EventStore(fileURL: h.queueURL, maxEvents: 100)
        #expect(store.count == count)
    }

    @Test func queueIsCappedDroppingOldest() async {
        let h = Harness()
        h.maxQueuedEvents = 5
        let e = await h.engine()
        for i in 0..<8 { await e.track("feature.used", ["n": .int(i)], at: h.clock.now) }
        #expect(await e.queuedEvents.count == 5)
        let reloaded = EventStore(fileURL: h.queueURL, maxEvents: 5)
        #expect(reloaded.events == (await e.queuedEvents))
        let last = String(decoding: reloaded.events.last!.json, as: UTF8.self)
        #expect(last.contains(#""n":7"#))
    }

    @Test func eventsOlderThan30DaysAreDroppedNotSent() async throws {
        let h = Harness()
        h.server.respond { _ in MockServer.Response(status: 0) }
        let e = await h.engine()
        await e.track("feature.used", at: h.clock.now)
        await e.flush()
        let old = Set(await e.queuedEvents.map(\.id))

        h.clock.advance(30 * 24 * 3600 + 1)
        h.server.respond { .ok(accepted: $0.events.count) }
        await e.track("feature.used", at: h.clock.now)
        await e.flush()
        let sent = Set(h.server.requests.last?.eventIDs ?? [])
        #expect(sent.isDisjoint(with: old))
        #expect(sent.count == 2) // new session_started + feature.used
        #expect(await e.queuedEvents.isEmpty)
    }

    // MARK: Batching

    @Test func batchesOfAtMost100() async {
        let h = Harness()
        let e = await h.engine()
        for _ in 0..<248 { await e.track("screen.viewed", ["screen": "home"], at: h.clock.now) }
        await e.flush() // 2 automatic + 248 = 250
        #expect(h.server.requests.map(\.events.count) == [100, 100, 50])
        #expect(Set(h.server.allEventIDs).count == 250)
        #expect(await e.queuedEvents.isEmpty)
    }

    @Test func batchesStayUnderBodyLimit() async {
        let h = Harness()
        let e = await h.engine()
        let big = String(repeating: "x", count: 1900)
        for _ in 0..<200 { await e.track("feature.used", ["blob": .string(big)], at: h.clock.now) }
        await e.flush()
        let requests = h.server.requests
        #expect(requests.count >= 3)
        for r in requests {
            #expect(r.json.count <= 256 * 1024)
            #expect(r.rawBody.count <= 256 * 1024)
            #expect(r.events.count <= 100)
        }
        #expect(h.server.allEventIDs.count == 202)
    }

    // MARK: Responses

    @Test func deliveredBatchIsRemovedEvenWithDuplicatesAndRejections() async {
        let h = Harness()
        h.server.respond { r in
            MockServer.Response(status: 200, body: Data(#"{"accepted":\#(r.events.count - 1),"duplicates":2,"rejected":1,"errors":[{"index":0,"reason":"name: x"}]}"#.utf8))
        }
        let e = await h.engine()
        await e.track("feature.used", at: h.clock.now)
        await e.flush()
        #expect(h.server.requests.count == 1)
        #expect(await e.queuedEvents.isEmpty)
    }

    @Test(arguments: [400, 415])
    func permanentClientErrorDropsBatch(status: Int) async {
        let h = Harness()
        h.server.respond { _ in MockServer.Response(status: status, body: Data(#"{"error":"x"}"#.utf8)) }
        let e = await h.engine()
        await e.flush()
        #expect(h.server.requests.count == 1)
        #expect(await e.queuedEvents.isEmpty)
        #expect(await e.nextAttemptAt == nil)
    }

    /// 404/405 come from a proxy with no route (redeploy, wrong path, http->https redirect
    /// turning the POST into a GET): the data must survive until the endpoint works again.
    @Test(arguments: [402, 404, 405, 408, 409, 410, 421, 422, 425])
    func otherClientErrorsKeepBatchAndRetry(status: Int) async {
        let h = Harness()
        h.server.respond { _ in MockServer.Response(status: status, body: Data("404 page not found".utf8)) }
        let e = await h.engine()
        await e.flush()
        #expect(h.server.requests.count == 1)
        #expect(await e.queuedEvents.count == 2)
        #expect(await e.failures == 1)
        #expect(await e.nextAttemptAt != nil)

        h.server.respond { .ok(accepted: $0.events.count) }
        h.clock.advance(3600)
        await e.flush()
        #expect(await e.queuedEvents.isEmpty)
    }

    /// A captive portal, a proxy or a default page answering 200 must not erase the queue.
    @Test(arguments: [
        "<html><body>Sign in to the Wi-Fi</body></html>",
        "",
        "{}",
        #"{"error":"x"}"#,
        #"{"accepted":"3"}"#,
    ])
    func okWithoutIngestResponseIsRetried(body: String) async {
        let h = Harness()
        h.server.respond { _ in
            MockServer.Response(status: 200, headers: ["Content-Type": "text/html"], body: Data(body.utf8))
        }
        let e = await h.engine()
        await e.flush()
        #expect(h.server.requests.count == 1)
        #expect(await e.queuedEvents.count == 2)
        #expect(await e.failures == 1)
        #expect(await e.nextAttemptAt != nil)
    }

    @Test func okOutcomeNeedsAcceptedCount() {
        let now = Date()
        #expect(Transport.outcome(status: 200, headers: [:], body: Data(#"{"accepted":0}"#.utf8), now: now)
                == .delivered(accepted: 0, duplicates: 0, rejected: 0))
        #expect(Transport.outcome(status: 200, headers: [:], body: Data(#"{"duplicates":2}"#.utf8), now: now)
                == .retry)
        #expect(Transport.outcome(status: 200, headers: [:], body: Data("OK".utf8), now: now) == .retry)
    }

    @Test func unauthorizedKeepsQueueAndStopsUntilNextLaunch() async {
        let h = Harness()
        h.server.respond { _ in MockServer.Response(status: 401, headers: ["WWW-Authenticate": "Bearer"]) }
        let e = await h.engine()
        await e.flush()
        #expect(await e.queuedEvents.count == 2)
        #expect(await e.unauthorized)
        h.clock.advance(3600)
        await e.flush()
        #expect(h.server.requests.count == 1)

        // Next launch tries again.
        h.server.respond { .ok(accepted: $0.events.count) }
        let relaunched = await h.engine()
        await relaunched.flush()
        #expect(h.server.requests.count == 2)
        #expect(await relaunched.queuedEvents.isEmpty)
    }

    @Test func tooLargeSplitsBatch() async {
        let h = Harness()
        h.server.respond { r in r.events.count > 10 ? MockServer.Response(status: 413) : .ok(accepted: r.events.count) }
        let e = await h.engine()
        for _ in 0..<38 { await e.track("feature.used", at: h.clock.now) }
        await e.flush()
        let delivered = h.server.requests.filter { $0.events.count <= 10 }
        #expect(Set(delivered.flatMap(\.eventIDs)).count == 40)
        #expect(await e.queuedEvents.isEmpty)
    }

    @Test func singleEventTooLargeIsDropped() async {
        let h = Harness()
        h.server.respond { _ in MockServer.Response(status: 413) }
        let e = await h.engine()
        await e.flush()
        #expect(await e.queuedEvents.isEmpty)
    }

    // MARK: Retry

    @Test func serverErrorRetriesWithExponentialBackoff() async {
        let h = Harness()
        h.random = 1.0 // upper end of the jitter: delay = base · 2^(n-1)
        h.server.respond { _ in MockServer.Response(status: 503) }
        let e = await h.engine()
        let start = h.clock.now

        await e.flush()
        #expect(await e.failures == 1)
        #expect(await e.nextAttemptAt == start.addingTimeInterval(5))

        // Before the deadline nothing is sent.
        h.clock.advance(4)
        await e.flush()
        #expect(h.server.requests.count == 1)

        h.clock.advance(1)
        await e.flush()
        #expect(h.server.requests.count == 2)
        #expect(await e.nextAttemptAt == h.clock.now.addingTimeInterval(10))

        h.clock.advance(10)
        await e.flush()
        #expect(await e.nextAttemptAt == h.clock.now.addingTimeInterval(20))
        #expect(await e.queuedEvents.count == 2)

        // Recovery: same batch delivered, backoff reset.
        h.server.respond { .ok(accepted: $0.events.count) }
        h.clock.advance(20)
        await e.flush()
        #expect(await e.queuedEvents.isEmpty)
        #expect(await e.failures == 0)
        #expect(await e.nextAttemptAt == nil)
        let ids = h.server.requests.map(\.eventIDs)
        #expect(Set(ids.map { $0.joined() }).count == 1) // every attempt sent the same events
    }

    @Test func networkErrorRetries() async {
        let h = Harness()
        h.server.respond { _ in MockServer.Response(status: 0) }
        let e = await h.engine()
        await e.flush()
        #expect(await e.failures == 1)
        #expect(await e.queuedEvents.count == 2)
        #expect(await e.nextAttemptAt != nil)
    }

    @Test func backoffIsCappedAndJittered() async {
        let low = Harness()
        low.random = 0
        let high = Harness()
        high.random = 0.999_999
        let a = await low.engine()
        let b = await high.engine()
        low.server.respond { _ in MockServer.Response(status: 500) }
        high.server.respond { _ in MockServer.Response(status: 500) }
        for _ in 0..<12 {
            low.clock.advance(3600)
            high.clock.advance(3600)
            await a.flush()
            await b.flush()
        }
        #expect(await a.failures == 12)
        let minDelay = await a.backoffDelay()
        let maxDelay = await b.backoffDelay()
        #expect(minDelay == 300) // half of the 10-minute cap
        #expect(maxDelay > 599 && maxDelay <= 600)
    }

    @Test func retryAfterIsRespected() async {
        let h = Harness()
        h.server.respond { _ in MockServer.Response(status: 429, headers: ["Retry-After": "120"]) }
        let e = await h.engine()
        await e.flush()
        #expect(await e.nextAttemptAt == h.clock.now.addingTimeInterval(120))
        #expect(await e.queuedEvents.count == 2)

        h.server.respond { .ok(accepted: $0.events.count) }
        h.clock.advance(119)
        await e.flush()
        #expect(h.server.requests.count == 1)
        h.clock.advance(1)
        await e.flush()
        #expect(h.server.requests.count == 2)
        #expect(h.server.requests[0].eventIDs == h.server.requests[1].eventIDs)
        #expect(await e.queuedEvents.isEmpty)
    }

    @Test func retryAfterParsing() {
        let now = Date(timeIntervalSince1970: 1_793_880_000) // Sun, 04 Nov 2026 ...
        #expect(Transport.retryAfter(headers: ["Retry-After": "20"], now: now) == 20)
        #expect(Transport.retryAfter(headers: ["retry-after": " 3 "], now: now) == 3)
        #expect(Transport.retryAfter(headers: [:], now: now) == nil)
        #expect(Transport.retryAfter(headers: ["Retry-After": "soon"], now: now) == nil)
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "GMT")
        f.dateFormat = "EEE, dd MMM yyyy HH:mm:ss 'GMT'"
        let date = f.string(from: now.addingTimeInterval(90))
        #expect(Transport.retryAfter(headers: ["Retry-After": date], now: now) == 90)
    }

    @Test func rateLimitedWithoutRetryAfterUsesBackoff() async {
        let h = Harness()
        h.random = 1
        h.server.respond { _ in MockServer.Response(status: 429) }
        let e = await h.engine()
        await e.flush()
        #expect(await e.nextAttemptAt == h.clock.now.addingTimeInterval(5))
    }
}
