import Foundation

#if canImport(UIKit)
import UIKit

/// Foreground: check the session and send. Background: send inside a background task so
/// the request can finish before the app is suspended.
@MainActor
enum Lifecycle {
    private static var observers: [NSObjectProtocol] = []

    static func install() {
        guard observers.isEmpty else { return }
        let center = NotificationCenter.default
        observers.append(center.addObserver(
            forName: UIApplication.willEnterForegroundNotification, object: nil, queue: .main
        ) { _ in
            Sonda.send(.foreground(at: Date()))
        })
        observers.append(center.addObserver(
            forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: .main
        ) { _ in
            MainActor.assumeIsolated { enteredBackground() }
        })
    }

    private static func enteredBackground() {
        let task = BackgroundTask()
        Sonda.send(.background(at: Date(), done: {
            Task { @MainActor in task.end() }
        }))
    }
}

@MainActor
private final class BackgroundTask {
    private var id: UIBackgroundTaskIdentifier = .invalid

    init() {
        id = UIApplication.shared.beginBackgroundTask(withName: "Sonda flush") { [weak self] in
            MainActor.assumeIsolated { self?.end() }
        }
    }

    func end() {
        guard id != .invalid else { return }
        UIApplication.shared.endBackgroundTask(id)
        id = .invalid
    }
}

#elseif canImport(AppKit)
import AppKit

/// macOS: becoming active counts as foreground, resigning active as background.
@MainActor
enum Lifecycle {
    private static var observers: [NSObjectProtocol] = []

    static func install() {
        guard observers.isEmpty else { return }
        let center = NotificationCenter.default
        observers.append(center.addObserver(
            forName: NSApplication.didBecomeActiveNotification, object: nil, queue: .main
        ) { _ in
            Sonda.send(.foreground(at: Date()))
        })
        observers.append(center.addObserver(
            forName: NSApplication.didResignActiveNotification, object: nil, queue: .main
        ) { _ in
            Sonda.send(.background(at: Date(), done: {}))
        })
    }
}

#else

enum Lifecycle {
    static func install() {}
}

#endif
