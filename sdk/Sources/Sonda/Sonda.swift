import Foundation
import os

/// Sonda: self-hosted telemetry for Fabio's apps.
///
/// ```swift
/// Sonda.configure(ingestKey: "sk_...", endpoint: URL(string: "https://sonda.example.com")!)
/// Sonda.paywallShown(placement: "onboarding", variant: "b")
/// Sonda.track("feature.used", props: ["feature": "export"])
/// ```
///
/// Calls never block and never throw. Events are dropped in DEBUG builds, in the simulator,
/// in Xcode installs and in TestFlight (D8); App Store builds send, and so do Release builds
/// distributed outside the App Store (Ad Hoc, Enterprise, Developer ID).
public enum Sonda {
    private static let channel = OSAllocatedUnfairLock<AsyncStream<Command>.Continuation?>(initialState: nil)

    /// Starts Sonda. Call once, early (e.g. in your `App` initialiser). Later calls are ignored.
    /// - Parameters:
    ///   - ingestKey: the app's key printed by `sonda app create` (`sk_...`). It is public by design (D7).
    ///   - endpoint: base URL of the Sonda server, e.g. `https://sonda.example.com`.
    public static func configure(ingestKey: String, endpoint: URL) {
        // The live dependencies touch the file system (queue directory), sysctl and URLSession:
        // built in the background task, so the caller (usually the main thread) only pays for
        // a lock, a stream and a task.
        configure(observeLifecycle: true) { .live(ingestKey: ingestKey, endpoint: endpoint) }
    }

    /// Records an event. `name` is `area.action_in_past_tense` in snake_case (D11), e.g.
    /// `feature.used`; props are flat, snake_case keys, never personal data. Invalid events are dropped.
    public static func track(_ name: String, props: [String: SondaValue] = [:]) {
        send(.track(name: name, props: props, at: Date(), eventID: UUID()))
    }

    /// Asks Sonda to send the queue now (it also sends on a timer, on foreground and on background).
    public static func flush() {
        send(.flush)
    }

    // MARK: - Internal

    /// `makeDependencies` runs in the detached utility task, never on the caller's thread.
    static func configure(observeLifecycle: Bool,
                          makeDependencies: @escaping @Sendable () -> Dependencies) {
        let (stream, continuation) = AsyncStream.makeStream(of: Command.self)
        let installed = channel.withLock { current -> Bool in
            guard current == nil else { return false }
            current = continuation
            return true
        }
        guard installed else {
            Log.sdk.error("Sonda.configure called more than once; ignored")
            return
        }
        Task.detached(priority: .utility) {
            let engine = Engine(deps: makeDependencies())
            await engine.start()
            for await command in stream {
                await engine.handle(command)
            }
        }
        if observeLifecycle {
            Task { @MainActor in Lifecycle.install() }
        }
    }

    static func send(_ command: Command) {
        let continuation = channel.withLock { $0 }
        guard let continuation else {
            if case let .background(_, done) = command { done() }
            if case let .flushAndWait(cont) = command { cont.resume() }
            Log.sdk.error("Sonda: call Sonda.configure first; event dropped")
            return
        }
        continuation.yield(command)
    }

    /// Tests: waits until every earlier call has been processed and a flush has finished.
    static func flushAndWait() async {
        await withCheckedContinuation { send(.flushAndWait($0)) }
    }
}
