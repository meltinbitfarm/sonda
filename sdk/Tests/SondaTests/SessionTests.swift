import Foundation
import Testing
@testable import Sonda

/// Identity and sessions (D10) and the production-only filter (D8).
@Suite struct SessionTests {
    private func sessionIDs(_ e: Engine) async -> [String] {
        await e.queuedEvents.compactMap { (try? JSONDecoder().decode(WireEvent.self, from: $0.json))?.sessionID }
    }

    @Test func firstLaunchCreatesUserAndSession() async {
        let h = Harness()
        let e = await h.engine()
        #expect(await e.queuedNames == ["app.session_started", "app.first_launch"])
        let user = await e.userID
        #expect(user != nil)
        #expect(user == user?.lowercased())
        #expect(h.defaults.string(forKey: Engine.Keys.userID) == user)
    }

    @Test func userIDPersistsAndFirstLaunchIsSentOnce() async {
        let h = Harness()
        let first = await h.engine()
        h.clock.advance(2 * 3600)
        let second = await h.engine()
        #expect(await second.userID == first.userID)
        let names = await second.queuedNames
        #expect(names.filter { $0 == "app.first_launch" }.count == 1)
        // A relaunch after 2 hours starts a new session.
        #expect(names.filter { $0 == "app.session_started" }.count == 2)
    }

    @Test func relaunchWithin30MinutesKeepsSession() async {
        let h = Harness()
        let first = await h.engine()
        h.clock.advance(10 * 60)
        let second = await h.engine()
        #expect(await second.sessionID == first.sessionID)
        #expect(await second.queuedNames == ["app.session_started", "app.first_launch"])
    }

    @Test func sessionRollsOverAfter30MinutesOfInactivity() async {
        let h = Harness()
        let e = await h.engine()
        let s1 = await e.sessionID

        h.clock.advance(29 * 60)
        await e.track("feature.used", at: h.clock.now)
        #expect(await e.sessionID == s1)

        // Activity moved the window: 29 more minutes is still the same session.
        h.clock.advance(29 * 60)
        await e.track("feature.used", at: h.clock.now)
        #expect(await e.sessionID == s1)

        h.clock.advance(30 * 60 + 1)
        await e.track("paywall.shown", ["placement": "settings"], at: h.clock.now)
        let s2 = await e.sessionID
        #expect(s2 != s1)

        let names = await e.queuedNames
        #expect(names.suffix(2) == ["app.session_started", "paywall.shown"])
        let ids = await sessionIDs(e)
        #expect(ids.suffix(2) == [s2!, s2!])
        #expect(Set(ids).count == 2)
    }

    @Test func clockMovedBackwardsStartsSessionAndKeepsCounting() async {
        let h = Harness()
        let e = await h.engine()
        let s1 = await e.sessionID

        // A small step back (out-of-order dates) keeps the session and the last activity.
        await e.track("feature.used", at: h.clock.now.addingTimeInterval(-60))
        #expect(await e.sessionID == s1)
        #expect(await e.lastActivity == h.clock.now)

        // The clock jumps back two hours: a new session, measured from the new time.
        h.clock.advance(-2 * 3600)
        await e.track("feature.used", at: h.clock.now)
        let s2 = await e.sessionID
        #expect(s2 != s1)
        #expect(await e.lastActivity == h.clock.now)

        // From there the 30-minute rule works again, without waiting for the old time.
        h.clock.advance(31 * 60)
        await e.track("feature.used", at: h.clock.now)
        #expect(await e.sessionID != s2)
    }

    @Test func foregroundAfterLongBackgroundStartsSession() async {
        let h = Harness()
        let e = await h.engine()
        let s1 = await e.sessionID
        await e.handle(.background(at: h.clock.now, done: {}))

        h.clock.advance(5 * 60)
        await e.handle(.foreground(at: h.clock.now))
        #expect(await e.sessionID == s1)

        h.clock.advance(31 * 60)
        await e.handle(.foreground(at: h.clock.now))
        #expect(await e.sessionID != s1)
        #expect(await e.queuedNames.last == "app.session_started")
    }

    @Test(arguments: [AppEnvironment.debug, .simulator, .xcode, .testFlight])
    func nonProductionDropsEverything(environment: AppEnvironment) async {
        let h = Harness()
        h.environment = environment
        let e = await h.engine()
        await e.track("feature.used", at: h.clock.now)
        await e.handle(.foreground(at: h.clock.now))
        await e.flush()
        #expect(await e.queuedEvents.isEmpty)
        #expect(h.server.requests.isEmpty)
        #expect(!FileManager.default.fileExists(atPath: h.queueURL.path))
        #expect(h.defaults.string(forKey: Engine.Keys.userID) == nil)
    }

    @Test func testBinaryIsNotProduction() async {
        // The real check: test builds are DEBUG (or run in the simulator), so they must not send.
        #expect(await AppEnvironment.detect() != .production)
    }

    @Test func storefrontIsCached() async {
        let h = Harness()
        let e = await h.engine()
        #expect(await e.storefront == "ITA")
        #expect(h.defaults.string(forKey: Engine.Keys.storefront) == "ITA")
    }

    @Test func timeoutReturnsNilForSlowOperation() async {
        let start = Date()
        let value: Int? = await withTimeout(seconds: 0.1) {
            try? await Task.sleep(for: .seconds(5))
            return 1
        }
        #expect(value == nil)
        #expect(Date().timeIntervalSince(start) < 2)
        #expect(await withTimeout(seconds: 1) { 7 } == 7)
    }
}

/// The public API end to end (one test: `Sonda` is process-wide).
@Suite struct PublicAPITests {
    @Test @MainActor func catalogHelpersReachTheServerInOrder() async throws {
        // The public API stamps events with the real time: the engine clock must agree,
        // or the gap would look like a clock jump and open a new session.
        let h = Harness(clock: TestClock(Date()))
        let builtOnMain = Locked<Bool?>(nil)
        let deps = h.dependencies
        // Called on the main thread, like an app's init: the dependencies (file system,
        // sysctl, URLSession in the live case) must be built elsewhere.
        Sonda.configure(observeLifecycle: false) {
            builtOnMain.withLock { $0 = Thread.isMainThread }
            return deps
        }
        Sonda.onboardingStepViewed(step: 1, name: "welcome")
        Sonda.onboardingSkipped(atStep: 2)
        Sonda.onboardingCompleted()
        Sonda.screenViewed("home")
        Sonda.featureUsed("export")
        Sonda.paywallShown(placement: "onboarding", variant: "b")
        Sonda.purchaseStarted(productID: "pro.yearly")
        Sonda.purchaseCancelled(productID: "pro.yearly")
        Sonda.purchaseFailed(error: "network", productID: "pro.yearly")
        Sonda.purchaseCompleted(productID: "pro.yearly")
        Sonda.purchaseRestored()
        Sonda.errorOccurred(NSError(domain: "NSURLErrorDomain", code: -1009))
        Sonda.track("feature.used", props: ["feature": "share", "count": 3, "pro": true])
        await Sonda.flushAndWait()
        #expect(builtOnMain.withLock { $0 } == false)

        let events = h.server.requests.flatMap(\.events)
        #expect(events.compactMap { $0["name"] as? String } == [
            "app.session_started", "app.first_launch",
            "onboarding.step_viewed", "onboarding.skipped", "onboarding.completed",
            "screen.viewed", "feature.used", "paywall.shown",
            "purchase.started", "purchase.cancelled", "purchase.failed", "purchase.completed",
            "purchase.restored", "error.occurred", "feature.used",
        ])
        let props = events.map { $0["props"] as? [String: Any] ?? [:] }
        #expect((props[2]["step"] as? NSNumber)?.intValue == 1)
        #expect(props[2]["step_name"] as? String == "welcome")
        #expect((props[3]["at_step"] as? NSNumber)?.intValue == 2)
        #expect(props[5]["screen"] as? String == "home")
        #expect(props[6]["feature"] as? String == "export")
        #expect(props[7]["placement"] as? String == "onboarding")
        #expect(props[7]["variant"] as? String == "b")
        #expect(props[8]["product_id"] as? String == "pro.yearly")
        #expect(props[10]["error"] as? String == "network")
        #expect(props[13]["domain"] as? String == "NSURLErrorDomain")
        #expect((props[13]["code"] as? NSNumber)?.intValue == -1009)
        #expect(props[14]["pro"] as? Bool == true)

        // A second configure is ignored.
        Sonda.configure(ingestKey: "sk_other", endpoint: URL(string: "https://elsewhere.test")!)
        Sonda.featureUsed("after")
        await Sonda.flushAndWait()
        let last = try #require(h.server.requests.last)
        #expect(last.request.value(forHTTPHeaderField: "Authorization") == "Bearer sk_test_key")
        #expect(last.names == ["feature.used"])
    }
}
