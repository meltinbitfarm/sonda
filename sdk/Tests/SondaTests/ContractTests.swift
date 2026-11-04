import Foundation
import Testing
@testable import Sonda

/// The request matches .jerome/notes/ingest-contract.md (version 1).
@Suite struct ContractTests {
    let uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

    @Test func requestAndPayloadMatchContract() async throws {
        let h = Harness()
        let engine = await h.engine()
        await engine.track("paywall.shown", ["placement": "onboarding", "variant": "b", "step": 2,
                                             "ratio": 0.5, "trial": true], at: h.clock.now)
        await engine.flush()

        let requests = h.server.requests
        try #require(requests.count == 1)
        let r = requests[0]
        #expect(r.request.httpMethod == "POST")
        #expect(r.request.url?.path == "/v1/events")
        #expect(r.request.value(forHTTPHeaderField: "Authorization") == "Bearer sk_test_key")
        #expect(r.request.value(forHTTPHeaderField: "Content-Type") == "application/json")
        #expect(r.request.value(forHTTPHeaderField: "Content-Encoding") == "gzip")
        #expect(r.rawBody.prefix(2) == Data([0x1f, 0x8b]))

        // Body: exactly {"events":[...]}
        let top = try #require(JSONSerialization.jsonObject(with: r.json) as? [String: Any])
        #expect(Array(top.keys) == ["events"])

        let events = r.events
        #expect(r.names == ["app.session_started", "app.first_launch", "paywall.shown"])
        let expectedTs = Int64(h.clock.now.timeIntervalSince1970 * 1000)
        let allowed: Set<String> = ["event_id", "name", "ts", "user_id", "session_id", "app_version", "build",
                                    "os_version", "locale", "region", "storefront", "device_model",
                                    "device_type", "props"]
        for e in events {
            #expect(Set(e.keys).isSubset(of: allowed), "unexpected keys \(e.keys)")
            #expect((e["event_id"] as? String)?.wholeMatch(of: uuidPattern) != nil)
            #expect((e["user_id"] as? String)?.wholeMatch(of: uuidPattern) != nil)
            #expect((e["session_id"] as? String)?.wholeMatch(of: uuidPattern) != nil)
            #expect((e["ts"] as? NSNumber)?.int64Value == expectedTs)
            #expect(e["app_version"] as? String == "1.4.0")
            #expect(e["build"] as? String == "212")
            #expect(e["os_version"] as? String == "26.1")
            #expect(e["locale"] as? String == "it_IT")
            #expect(e["region"] as? String == "IT")
            #expect(e["storefront"] as? String == "ITA")
            #expect(e["device_model"] as? String == "iPhone16,2")
            #expect(e["device_type"] as? String == "phone")
        }
        // ts is an integer in the raw JSON, never 1.793e12 or a string.
        let raw = String(decoding: r.json, as: UTF8.self)
        #expect(raw.contains(#""ts":\#(expectedTs),"#) || raw.contains(#""ts":\#(expectedTs)}"#))

        #expect(Set(events.compactMap { $0["user_id"] as? String }).count == 1)
        #expect(Set(events.compactMap { $0["session_id"] as? String }).count == 1)
        #expect(Set(r.eventIDs).count == 3)
        #expect(events[0]["props"] == nil)

        let props = try #require(events[2]["props"] as? [String: Any])
        #expect(props["placement"] as? String == "onboarding")
        #expect(props["variant"] as? String == "b")
        #expect((props["step"] as? NSNumber)?.intValue == 2)
        #expect((props["ratio"] as? NSNumber)?.doubleValue == 0.5)
        #expect(props["trial"] as? Bool == true)

        #expect(await engine.queuedEvents.isEmpty)
    }

    @Test func endpointWithOrWithoutPath() {
        #expect(Transport.eventsURL(endpoint: URL(string: "https://s.example.com")!).absoluteString
                == "https://s.example.com/v1/events")
        #expect(Transport.eventsURL(endpoint: URL(string: "https://s.example.com/")!).absoluteString
                == "https://s.example.com/v1/events")
        #expect(Transport.eventsURL(endpoint: URL(string: "https://s.example.com/v1/events")!).absoluteString
                == "https://s.example.com/v1/events")
    }

    @Test func invalidEventsAreDroppedOnDevice() async {
        let h = Harness()
        let engine = await h.engine()
        let before = await engine.queuedEvents.count
        await engine.track("PaywallShown", at: h.clock.now)
        await engine.track("paywall", at: h.clock.now)
        await engine.track("paywall.shown.extra", at: h.clock.now)
        await engine.track("a." + String(repeating: "b", count: 63), at: h.clock.now)
        await engine.track("feature.used", ["Feature": "x"], at: h.clock.now)
        await engine.track("feature.used", ["value": .double(.nan)], at: h.clock.now)
        await engine.track("feature.used", ["blob": .string(String(repeating: "x", count: 2100))], at: h.clock.now)
        #expect(await engine.queuedEvents.count == before)

        await engine.track("feature.used", ["feature": "export_pdf"], at: h.clock.now)
        #expect(await engine.queuedEvents.count == before + 1)
    }

    @Test func propsSizeCountsGoEscaping() {
        // 2040 bytes of '<' fits our encoding but is 6x larger once Go escapes it.
        let props: [String: SondaValue] = ["k": .string(String(repeating: "<", count: 400))]
        #expect(EventRules.serverEncodedSize(of: props) > 2048)
        #expect(EventRules.validate(props: props) == .tooLarge(EventRules.serverEncodedSize(of: props)))
        #expect(EventRules.validate(props: ["k": .string(String(repeating: "a", count: 2000))]) == nil)
    }

    @Test func optionalFieldsAreCleaned() {
        #expect(EventRules.field("") == nil)
        #expect(EventRules.field(nil) == nil)
        #expect(EventRules.field("a\u{7}b") == "ab")
        #expect(EventRules.field(String(repeating: "é", count: 40))?.utf8.count == 64)
    }

    @Test func gzipRoundTripAndCRC() throws {
        #expect(Gzip.crc32(Data("123456789".utf8)) == 0xCBF4_3926) // standard check value
        let input = Data(String(repeating: "sonda ", count: 5000).utf8)
        let gz = try #require(Gzip.compress(input))
        #expect(gz.count < input.count)
        #expect(Gzip.decompress(gz) == input)
    }

    #if os(macOS)
    /// The system gzip (same format check as Go's compress/gzip) accepts our stream.
    @Test func systemGunzipAcceptsOurStream() throws {
        let input = Data(#"{"events":[{"name":"feature.used"}]}"#.utf8)
        let gz = try #require(Gzip.compress(input))
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let file = dir.appendingPathComponent("body.gz")
        try gz.write(to: file)

        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/gzip")
        p.arguments = ["-dc", file.path]
        let out = Pipe()
        p.standardOutput = out
        try p.run()
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        #expect(p.terminationStatus == 0)
        #expect(data == input)
    }
    #endif

    @Test func deviceTypeAndOSVersion() {
        #if os(iOS) && !targetEnvironment(macCatalyst)
        if !ProcessInfo.processInfo.isiOSAppOnMac {
            #expect(DeviceInfo.deviceType(forModel: "iPhone16,2") == "phone")
            #expect(DeviceInfo.deviceType(forModel: "iPad14,1") == "pad")
            #expect(DeviceInfo.deviceType(forModel: "Watch7,1") == nil)
        }
        #else
        #expect(DeviceInfo.deviceType(forModel: "Mac14,2") == "mac")
        #endif
        #expect(DeviceInfo.osVersionString(OperatingSystemVersion(majorVersion: 26, minorVersion: 1, patchVersion: 0)) == "26.1")
        #expect(DeviceInfo.osVersionString(OperatingSystemVersion(majorVersion: 26, minorVersion: 1, patchVersion: 2)) == "26.1.2")
        let current = DeviceInfo.current()
        #expect(current.deviceModel?.isEmpty == false)
        #expect(current.deviceType != nil)
    }
}
