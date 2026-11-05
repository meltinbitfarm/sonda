import Foundation
import StoreKit
import os

/// Where the app is running (D23). Every event carries it as `environment`; nothing is dropped,
/// so a build can be checked before release, and the server's tools show production by default.
/// The raw values are the wire values of the ingest contract.
enum AppEnvironment: String, Sendable, Codable, CaseIterable {
    /// App Store installs, and Release builds distributed outside it (Ad Hoc, Enterprise, Developer ID).
    case production
    /// TestFlight (StoreKit sandbox, or a sandbox receipt when StoreKit cannot answer).
    case testflight
    /// DEBUG builds, the simulator and Xcode installs.
    case development

    /// The real check. Injected in `Engine.Dependencies` so tests can pick any environment.
    static func detect() async -> AppEnvironment {
        #if DEBUG
        return .development
        #elseif targetEnvironment(simulator)
        return .development
        #else
        if let env = await appTransactionEnvironment() { return env }
        // No App Store answer (offline on first launch, or not signed in): fall back to the
        // receipt file name, which is `sandboxReceipt` for TestFlight and Xcode installs.
        if Bundle.main.appStoreReceiptURL?.lastPathComponent == "sandboxReceipt" { return .testflight }
        return .production
        #endif
    }

    /// AppTransaction.environment, with a timeout: it may need the network.
    private static func appTransactionEnvironment() async -> AppEnvironment? {
        await withTimeout(seconds: 10) {
            guard let result = try? await AppTransaction.shared else { return nil }
            let transaction: AppTransaction
            switch result {
            case .verified(let t): transaction = t
            case .unverified(let t, _): transaction = t
            }
            return environment(for: transaction.environment)
        } ?? nil
    }

    /// StoreKit's environment to ours. An unknown future value is a test install, never production.
    static func environment(for storeKit: AppStore.Environment) -> AppEnvironment {
        switch storeKit {
        case .production: return .production
        case .sandbox: return .testflight
        case .xcode: return .development
        default: return .testflight
        }
    }
}

enum StorefrontProvider {
    /// `Storefront.current?.countryCode` (ISO 3166-1 alpha-3, e.g. `ITA`), with a timeout.
    static func current() async -> String? {
        await withTimeout(seconds: 5) {
            await Storefront.current?.countryCode
        } ?? nil
    }
}

/// Runs `operation`, returning nil if it has not finished after `seconds`.
/// Unstructured on purpose: a task group would wait for a non-cancellable operation
/// (such as `AppTransaction.shared`) before returning; here the late result is ignored.
func withTimeout<T: Sendable>(seconds: Double, _ operation: @escaping @Sendable () async -> T) async -> T? {
    let slot = OSAllocatedUnfairLock<CheckedContinuation<T?, Never>?>(initialState: nil)
    @Sendable func finish(_ value: T?) {
        let cont = slot.withLock { c -> CheckedContinuation<T?, Never>? in
            defer { c = nil }
            return c
        }
        cont?.resume(returning: value)
    }
    return await withCheckedContinuation { (cont: CheckedContinuation<T?, Never>) in
        slot.withLock { $0 = cont }
        Task { finish(await operation()) }
        Task {
            try? await Task.sleep(for: .seconds(seconds))
            finish(nil)
        }
    }
}
