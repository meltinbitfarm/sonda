import Foundation

/// Everything the engine needs from the outside world. `live` is what apps get; tests
/// replace the clock, the network (URLProtocol mock), storage and the production check.
struct Dependencies: Sendable {
    var ingestKey: String
    var endpoint: URL
    var session: URLSession
    var defaults: Settings
    var queueURL: URL
    var now: @Sendable () -> Date
    /// Uniform in 0..<1, for backoff jitter.
    var random: @Sendable () -> Double
    /// Decides whether events are kept at all (D8). Injectable so tests can run.
    var environment: @Sendable () async -> AppEnvironment
    var storefront: @Sendable () async -> String?
    var device: DeviceInfo
    var locale: @Sendable () -> Locale

    var maxQueuedEvents = 2000
    /// Periodic send while running. nil = manual mode: no timers and no automatic sends,
    /// only explicit `flush()` calls (tests).
    var flushInterval: Duration? = .seconds(30)
    /// D10: a new session after 30 minutes without activity.
    var sessionTimeout: TimeInterval = 30 * 60
    /// The server rejects events older than 31 days; the contract asks the SDK to drop at 30.
    var maxEventAge: TimeInterval = 30 * 24 * 60 * 60
    var backoffBase: TimeInterval = 5
    var backoffCap: TimeInterval = 10 * 60

    static func live(ingestKey: String, endpoint: URL) -> Dependencies {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 30
        config.waitsForConnectivity = false
        config.httpCookieStorage = nil
        config.urlCache = nil
        return Dependencies(
            ingestKey: ingestKey,
            endpoint: endpoint,
            session: URLSession(configuration: config),
            defaults: Settings(.standard),
            queueURL: EventStore.defaultURL(),
            now: { Date() },
            random: { Double.random(in: 0..<1) },
            environment: { await AppEnvironment.detect() },
            storefront: { await StorefrontProvider.current() },
            device: .current(),
            locale: { Locale.current }
        )
    }
}

/// UserDefaults is documented as thread-safe but not marked Sendable in the SDK.
struct Settings: @unchecked Sendable {
    let defaults: UserDefaults
    init(_ defaults: UserDefaults) { self.defaults = defaults }

    func string(forKey key: String) -> String? { defaults.string(forKey: key) }
    func object(forKey key: String) -> Any? { defaults.object(forKey: key) }
    func set(_ value: Any?, forKey key: String) { defaults.set(value, forKey: key) }
}

/// Commands from the public API, processed one at a time and in order.
enum Command: Sendable {
    case track(name: String, props: [String: SondaValue], at: Date, eventID: UUID)
    case foreground(at: Date)
    case background(at: Date, done: @Sendable () -> Void)
    case flush
    /// Tests: flush and resume when done.
    case flushAndWait(CheckedContinuation<Void, Never>)
}

/// Owns identity, session, the disk queue and delivery. One per process.
actor Engine {
    enum Keys {
        static let userID = "dev.sonda.user_id"
        static let sessionID = "dev.sonda.session_id"
        static let lastActivity = "dev.sonda.last_activity"
        static let storefront = "dev.sonda.storefront"
    }

    let deps: Dependencies
    let store: EventStore

    private(set) var environment: AppEnvironment?
    private(set) var userID: String?
    private(set) var sessionID: String?
    private(set) var lastActivity: Date?
    private(set) var storefront: String?

    /// 401: stop sending until the next launch, keep the queue.
    private(set) var unauthorized = false
    /// Max events per request; halved on 413, grows back after a success.
    private(set) var batchLimit = Transport.maxBatchEvents
    /// Consecutive failed attempts, drives the backoff.
    private(set) var failures = 0
    /// No send before this time (backoff or Retry-After).
    private(set) var nextAttemptAt: Date?
    /// The send in progress. A second caller awaits it instead of returning at once, so a
    /// background task ends only when the request in flight has finished.
    private var runningFlush: Task<Void, Never>?
    private var timerTask: Task<Void, Never>?
    private var retryTask: Task<Void, Never>?

    init(deps: Dependencies) {
        self.deps = deps
        self.store = EventStore(fileURL: deps.queueURL, maxEvents: deps.maxQueuedEvents)
    }

    var sendsEvents: Bool { environment?.sendsEvents ?? false }
    var queuedEvents: [QueuedEvent] { store.events }

    // MARK: - Lifecycle

    /// Decides the environment, loads identity, emits app.first_launch / app.session_started
    /// and sends what is left in the queue from earlier launches.
    func start() async {
        storefront = deps.defaults.string(forKey: Keys.storefront)
        async let env = deps.environment()
        async let sf = deps.storefront()
        let environment = await env
        let fetchedStorefront = await sf
        self.environment = environment
        if let fetchedStorefront, !fetchedStorefront.isEmpty {
            storefront = fetchedStorefront
            deps.defaults.set(fetchedStorefront, forKey: Keys.storefront)
        }
        guard environment.sendsEvents else {
            Log.sdk.info("Sonda: \(environment.rawValue, privacy: .public) build, events are dropped")
            return
        }

        let now = deps.now()
        var firstLaunch = false
        if let stored = deps.defaults.string(forKey: Keys.userID), UUID(uuidString: stored) != nil {
            userID = stored
        } else {
            userID = UUID().uuidString.lowercased()
            deps.defaults.set(userID, forKey: Keys.userID)
            firstLaunch = true
        }
        if let s = deps.defaults.string(forKey: Keys.sessionID), UUID(uuidString: s) != nil {
            sessionID = s
            lastActivity = deps.defaults.object(forKey: Keys.lastActivity) as? Date
        }
        if firstLaunch {
            // first_launch always opens a fresh session.
            sessionID = nil
        }
        touchSession(at: now)
        if firstLaunch {
            enqueue(name: "app.first_launch", props: [:], at: now, eventID: UUID())
        }
        store.removeOlder(than: cutoff(now: now))
        scheduleTimer()
        triggerFlush()
    }

    func handle(_ command: Command) async {
        switch command {
        case let .track(name, props, date, id):
            record(name: name, props: props, at: date, eventID: id)
        case let .foreground(date):
            guard sendsEvents else { return }
            touchSession(at: date)
            triggerFlush()
        case let .background(date, done):
            guard sendsEvents else { return done() }
            touchSession(at: date)
            Task {
                await self.flush()
                done()
            }
        case .flush:
            triggerFlush()
        case let .flushAndWait(cont):
            await flush()
            cont.resume()
        }
    }

    // MARK: - Recording

    /// Validates and queues one event. Invalid events are dropped here, never sent.
    func record(name: String, props: [String: SondaValue], at date: Date, eventID: UUID) {
        guard sendsEvents else { return }
        guard EventRules.isValidName(name) else {
            Log.sdk.error("Sonda: dropped event with invalid name \(name, privacy: .public)")
            return
        }
        if let error = EventRules.validate(props: props) {
            Log.sdk.error("Sonda: dropped \(name, privacy: .public): props \(String(describing: error), privacy: .public)")
            return
        }
        touchSession(at: date)
        enqueue(name: name, props: props, at: date, eventID: eventID)
        if store.count >= batchLimit { triggerFlush() }
    }

    /// Starts a new session (and emits app.session_started) if there is none or the last
    /// activity is older than the timeout; then records `date` as the last activity.
    private func touchSession(at date: Date) {
        let expired = lastActivity.map { date.timeIntervalSince($0) > deps.sessionTimeout } ?? true
        if sessionID == nil || expired {
            sessionID = UUID().uuidString.lowercased()
            deps.defaults.set(sessionID, forKey: Keys.sessionID)
            lastActivity = date
            enqueue(name: "app.session_started", props: [:], at: date, eventID: UUID())
        }
        if let last = lastActivity, last > date { return } // out-of-order timestamps
        lastActivity = date
        deps.defaults.set(date, forKey: Keys.lastActivity)
    }

    private func enqueue(name: String, props: [String: SondaValue], at date: Date, eventID: UUID) {
        guard let userID, let sessionID else { return }
        let locale = deps.locale()
        let event = WireEvent(
            eventID: eventID.uuidString.lowercased(),
            name: name,
            ts: Self.milliseconds(date),
            userID: userID,
            sessionID: sessionID,
            appVersion: EventRules.field(deps.device.appVersion),
            build: EventRules.field(deps.device.build),
            osVersion: EventRules.field(deps.device.osVersion),
            locale: EventRules.field(locale.identifier),
            region: EventRules.field(locale.region?.identifier),
            storefront: EventRules.field(storefront),
            deviceModel: EventRules.field(deps.device.deviceModel),
            deviceType: EventRules.field(deps.device.deviceType),
            props: props.isEmpty ? nil : props
        )
        guard let json = try? JSONCoding.encoder.encode(event) else { return }
        store.append(QueuedEvent(id: event.eventID, ts: event.ts, json: json))
    }

    static func milliseconds(_ date: Date) -> Int64 {
        Int64((date.timeIntervalSince1970 * 1000).rounded(.down))
    }

    private func cutoff(now: Date) -> Int64 {
        Self.milliseconds(now.addingTimeInterval(-deps.maxEventAge))
    }

    // MARK: - Delivery

    /// Automatic send; does nothing in manual mode (`flushInterval == nil`, tests).
    private func triggerFlush() {
        guard deps.flushInterval != nil else { return }
        Task { await self.flush() }
    }

    /// Sends queued events in batches until the queue is empty or an attempt fails.
    /// If a send is already running, waits for it to finish instead of starting another one.
    func flush() async {
        if let running = runningFlush {
            await running.value
            return
        }
        guard sendsEvents, !unauthorized else { return }
        if let next = nextAttemptAt, deps.now() < next { return }
        // Created on the actor and stored before it can run: the task body starts only after
        // this method suspends, so `runningFlush` is always set while the send is in progress.
        let task = Task {
            defer { self.runningFlush = nil }
            await self.sendQueue()
        }
        runningFlush = task
        await task.value
    }

    private func sendQueue() async {
        for _ in 0..<1000 {
            let now = deps.now()
            store.removeOlder(than: cutoff(now: now))
            let batch = Transport.nextBatch(from: store.events, limit: batchLimit)
            if batch.isEmpty { return }

            let outcome = await send(batch)
            let ids = Set(batch.map(\.id))
            switch outcome {
            case let .delivered(accepted, duplicates, rejected):
                Log.sdk.debug("Sonda: sent \(batch.count), accepted \(accepted), duplicates \(duplicates), rejected \(rejected)")
                store.remove(ids: ids)
                failures = 0
                nextAttemptAt = nil
                batchLimit = min(Transport.maxBatchEvents, batchLimit * 2)
            case let .dropBatch(status):
                Log.sdk.error("Sonda: batch of \(batch.count) dropped, HTTP \(status)")
                store.remove(ids: ids)
            case .unauthorized:
                Log.sdk.error("Sonda: ingest key refused (401), sending paused until next launch")
                unauthorized = true
                return
            case .tooLarge:
                if batch.count == 1 {
                    store.remove(ids: ids)
                } else {
                    batchLimit = max(1, batch.count / 2)
                }
            case let .rateLimited(retryAfter):
                if let retryAfter {
                    postpone(by: retryAfter)
                } else {
                    failures += 1
                    postpone(by: backoffDelay())
                }
                return
            case .retry:
                failures += 1
                postpone(by: backoffDelay())
                return
            }
        }
    }

    /// Exponential backoff with jitter: base · 2^(failures-1), capped, then a random
    /// point in its upper half (so retries from many devices spread out).
    func backoffDelay() -> TimeInterval {
        let exponent = Double(max(0, failures - 1))
        let delay = min(deps.backoffCap, deps.backoffBase * pow(2, exponent))
        return delay / 2 + deps.random() * delay / 2
    }

    private func postpone(by delay: TimeInterval) {
        let at = deps.now().addingTimeInterval(delay)
        nextAttemptAt = at
        guard deps.flushInterval != nil else { return }
        retryTask?.cancel()
        retryTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            await self?.flush()
        }
    }

    private func scheduleTimer() {
        guard let interval = deps.flushInterval, timerTask == nil else { return }
        timerTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: interval)
                await self?.flush()
            }
        }
    }

    private func send(_ batch: [QueuedEvent]) async -> SendOutcome {
        let request = Transport.request(endpoint: deps.endpoint, ingestKey: deps.ingestKey, events: batch)
        do {
            let (data, response) = try await deps.session.data(for: request)
            guard let http = response as? HTTPURLResponse else { return .retry }
            return Transport.outcome(status: http.statusCode, headers: http.allHeaderFields,
                                     body: data, now: deps.now())
        } catch {
            return .retry
        }
    }
}
