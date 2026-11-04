import Foundation
import StoreKit
import os

/// Where the app is running. Only `.production` sends events (D8): DEBUG builds,
/// the simulator, Xcode installs and TestFlight are dropped so the data stays clean.
enum AppEnvironment: String, Sendable {
    case production
    case debug
    case simulator
    case xcode
    case testFlight

    var sendsEvents: Bool { self == .production }

    /// The real check. Injected in `Engine.Dependencies` so tests can say "production".
    static func detect() async -> AppEnvironment {
        #if DEBUG
        return .debug
        #elseif targetEnvironment(simulator)
        return .simulator
        #else
        if let env = await appTransactionEnvironment() { return env }
        // No App Store answer (offline on first launch, or not signed in): fall back to the
        // receipt file name, which is `sandboxReceipt` for TestFlight and Xcode installs.
        if Bundle.main.appStoreReceiptURL?.lastPathComponent == "sandboxReceipt" { return .testFlight }
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
            switch transaction.environment {
            case .production: return .production
            case .sandbox: return .testFlight
            case .xcode: return .xcode
            default: return .testFlight
            }
        } ?? nil
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
