import Foundation
import Testing
@testable import Sonda

/// Against a real `sonda serve`. Skipped unless SONDA_E2E_ENDPOINT and SONDA_E2E_KEY are set
/// (recipe in .jerome/notes/recipes/sdk-tests.md).
@Suite(.enabled(if: ProcessInfo.processInfo.environment["SONDA_E2E_ENDPOINT"] != nil))
struct LiveServerTests {
    @Test func realServerAcceptsOurBatchesAndDuplicates() async throws {
        let env = ProcessInfo.processInfo.environment
        let endpoint = try #require(URL(string: env["SONDA_E2E_ENDPOINT"] ?? ""))
        let key = try #require(env["SONDA_E2E_KEY"])

        let h = Harness(clock: TestClock(Date()))
        var deps = h.dependencies
        deps.endpoint = endpoint
        deps.ingestKey = key
        deps.session = URLSession(configuration: .ephemeral)
        let e = Engine(deps: deps)
        await e.start()
        await e.track("paywall.shown", ["placement": "onboarding", "variant": "b", "step": 1,
                                        "ratio": 0.25, "trial": true], at: h.clock.now)
        for i in 0..<120 { await e.track("feature.used", ["feature": "f\(i % 5)"], at: h.clock.now) }
        let queued = await e.queuedEvents

        // Send once by hand and read the server's counts.
        let request = Transport.request(endpoint: endpoint, ingestKey: key, events: Array(queued.prefix(100)))
        let (data, response) = try await URLSession.shared.data(for: request)
        #expect((response as? HTTPURLResponse)?.statusCode == 200)
        let body = try JSONDecoder().decode(IngestResponse.self, from: data)
        #expect(body.accepted == 100)
        #expect(body.rejected == 0)
        #expect(body.duplicates == 0)

        // The engine sends everything; the first 100 come back as duplicates, still removed.
        await e.flush()
        #expect(await e.queuedEvents.isEmpty)
        #expect(await e.failures == 0)
    }
}
