import Foundation
@testable import Sonda

/// A fake Sonda server behind URLProtocol. Each instance has its own host, so tests that
/// run in parallel never see each other's requests.
final class MockServer: @unchecked Sendable {
    struct Request {
        let request: URLRequest
        /// Body as sent (gzip if Content-Encoding is gzip).
        let rawBody: Data

        var json: Data {
            request.value(forHTTPHeaderField: "Content-Encoding") == "gzip"
                ? (Gzip.decompress(rawBody) ?? Data()) : rawBody
        }

        var events: [[String: Any]] {
            let obj = try? JSONSerialization.jsonObject(with: json) as? [String: Any]
            return obj?["events"] as? [[String: Any]] ?? []
        }

        var eventIDs: [String] { events.compactMap { $0["event_id"] as? String } }
        var names: [String] { events.compactMap { $0["name"] as? String } }
    }

    struct Response {
        var status: Int
        var headers: [String: String] = [:]
        var body: Data = Data()

        static func ok(accepted: Int, duplicates: Int = 0, rejected: Int = 0) -> Response {
            Response(status: 200, body: Data(#"{"accepted":\#(accepted),"duplicates":\#(duplicates),"rejected":\#(rejected)}"#.utf8))
        }
    }

    typealias Handler = @Sendable (Request) -> Response

    let host = "\(UUID().uuidString.lowercased()).sonda.test"
    var endpoint: URL { URL(string: "https://\(host)")! }

    private let lock = NSLock()
    private var handler: Handler
    private var received: [Request] = []

    init(handler: @escaping Handler = { .ok(accepted: $0.events.count) }) {
        self.handler = handler
        Self.registry.withLock { $0[host] = self }
    }

    deinit {
        let host = self.host
        Self.registry.withLock { _ = $0.removeValue(forKey: host) }
    }

    func respond(_ handler: @escaping Handler) { lock.withLock { self.handler = handler } }
    var requests: [Request] { lock.withLock { received } }
    var allEventIDs: [String] { requests.flatMap(\.eventIDs) }

    lazy var session: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [MockURLProtocol.self]
        return URLSession(configuration: config)
    }()

    fileprivate func handle(_ request: URLRequest, body: Data) -> Response {
        let r = Request(request: request, rawBody: body)
        let h = lock.withLock { () -> Handler in
            received.append(r)
            return handler
        }
        return h(r)
    }

    fileprivate static let registry = Locked<[String: MockServer]>([:])
}

final class Locked<Value>: @unchecked Sendable {
    private let lock = NSLock()
    private var value: Value
    init(_ value: Value) { self.value = value }
    func withLock<R>(_ body: (inout Value) -> R) -> R { lock.withLock { body(&value) } }
}

final class MockURLProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let host = request.url?.host ?? ""
        guard let server = MockServer.registry.withLock({ $0[host] }) else {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
            return
        }
        let body = request.httpBody ?? Self.read(request.httpBodyStream)
        let r = server.handle(request, body: body)
        if r.status == 0 {
            client?.urlProtocol(self, didFailWithError: URLError(.notConnectedToInternet))
            return
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: r.status, httpVersion: "HTTP/1.1",
                                       headerFields: r.headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: r.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    private static func read(_ stream: InputStream?) -> Data {
        guard let stream else { return Data() }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 16 * 1024)
        while stream.hasBytesAvailable {
            let n = stream.read(&buffer, maxLength: buffer.count)
            if n <= 0 { break }
            data.append(buffer, count: n)
        }
        return data
    }
}

/// A clock the test moves by hand.
final class TestClock: @unchecked Sendable {
    private let lock = NSLock()
    private var current: Date
    init(_ start: Date = Date(timeIntervalSince1970: 1_793_880_000)) { current = start }
    var now: Date { lock.withLock { current } }
    func advance(_ seconds: TimeInterval) { lock.withLock { current += seconds } }
}

/// A fresh sandbox per test: temp queue file, private UserDefaults suite, mock server, clock.
final class Harness: @unchecked Sendable {
    let server: MockServer
    let clock: TestClock
    let suiteName = "dev.sonda.tests.\(UUID().uuidString)"
    let directory: URL
    var environment: AppEnvironment = .production
    var random: Double = 0.5
    var maxQueuedEvents = 2000
    var sessionTimeout: TimeInterval = 30 * 60

    init(server: MockServer = MockServer(), clock: TestClock = TestClock()) {
        self.server = server
        self.clock = clock
        directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("sonda-tests-\(UUID().uuidString)", isDirectory: true)
    }

    deinit {
        UserDefaults().removePersistentDomain(forName: suiteName)
        try? FileManager.default.removeItem(at: directory)
    }

    var queueURL: URL { directory.appendingPathComponent("queue.jsonl") }
    var defaults: UserDefaults { UserDefaults(suiteName: suiteName)! }

    var dependencies: Dependencies {
        let clock = self.clock
        let env = environment
        let random = self.random
        var deps = Dependencies(
            ingestKey: "sk_test_key",
            endpoint: server.endpoint,
            session: server.session,
            defaults: Settings(defaults),
            queueURL: queueURL,
            now: { clock.now },
            random: { random },
            environment: { env },
            storefront: { "ITA" },
            device: DeviceInfo(appVersion: "1.4.0", build: "212", osVersion: "26.1",
                               deviceModel: "iPhone16,2", deviceType: "phone"),
            locale: { Locale(identifier: "it_IT") }
        )
        deps.flushInterval = nil
        deps.maxQueuedEvents = maxQueuedEvents
        deps.sessionTimeout = sessionTimeout
        return deps
    }

    /// A started engine, like the one `Sonda.configure` creates.
    func engine() async -> Engine {
        let e = Engine(deps: dependencies)
        await e.start()
        return e
    }
}

extension Engine {
    func track(_ name: String, _ props: [String: SondaValue] = [:], at date: Date) {
        record(name: name, props: props, at: date, eventID: UUID())
    }

    var queuedNames: [String] {
        queuedEvents.compactMap { (try? JSONDecoder().decode(WireEvent.self, from: $0.json))?.name }
    }
}
